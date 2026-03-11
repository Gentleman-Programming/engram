// Package remote implements the cloud-backed sync transport for Engram.
//
// It provides a Transport implementation that communicates with the
// Engram Cloud server for push/pull sync operations over HTTP.
package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
)

// ─── RemoteTransport ─────────────────────────────────────────────────────────

// RemoteTransport pushes/pulls chunks over HTTP to an Engram cloud server.
// It implements sync.Transport.
type RemoteTransport struct {
	baseURL    string       // e.g. "https://engram.example.com"
	token      string       // JWT or API key
	httpClient *http.Client // configurable for testing

	mu             sync.Mutex
	refreshToken   string
	onTokenRefresh func(string) error
	teamSync       bool // enable cross-user team sync on pull (default: true)
}

// NewRemoteTransport creates a RemoteTransport targeting the given cloud server.

func NewRemoteTransport(baseURL, token string) (*RemoteTransport, error) {
	normalizedURL, err := validateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	return &RemoteTransport{
		baseURL: normalizedURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		teamSync: true, // enabled by default
	}, nil
}

// SetTokenRefresher configures optional access-token refresh for sync operations.
func (rt *RemoteTransport) SetTokenRefresher(refreshToken string, onTokenRefresh func(string) error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.refreshToken = strings.TrimSpace(refreshToken)
	rt.onTokenRefresh = onTokenRefresh
}

// SetTeamSync enables or disables cross-user team sync on pull operations.
// When enabled (default), pull requests include observation mutations from
// teammates enrolled in the same projects.
func (rt *RemoteTransport) SetTeamSync(enabled bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.teamSync = enabled
}

func validateBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("cloud: remote url is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("cloud: invalid remote url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("cloud: invalid remote url: scheme must be http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("cloud: invalid remote url: host is required")
	}

	return strings.TrimRight(parsed.String(), "/"), nil
}

// ─── Transport interface implementation ──────────────────────────────────────

// ReadManifest fetches the chunk manifest from the cloud server.
// GET /sync/pull returns {"version": 1, "chunks": [...]}.
func (rt *RemoteTransport) ReadManifest() (*engramsync.Manifest, error) {
	req, err := http.NewRequest("GET", rt.baseURL+"/sync/pull", nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build manifest request: %w", err)
	}
	rt.setAuthorization(req)

	resp, err := rt.doWithRetry(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, rt.httpError("fetch manifest", resp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloud: read manifest body: %w", err)
	}

	var m engramsync.Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("cloud: parse manifest: %w", err)
	}
	return &m, nil
}

// WriteManifest is a no-op for remote: the cloud server manages its own manifest.
func (rt *RemoteTransport) WriteManifest(_ *engramsync.Manifest) error {
	return nil
}

// WriteChunk pushes a chunk to the cloud server via POST /sync/push.
// The data parameter is raw ChunkData JSON; this method wraps it in the
// push request format the server expects.
func (rt *RemoteTransport) WriteChunk(chunkID string, data []byte, entry engramsync.ChunkEntry) error {
	// Parse ChunkData from the raw bytes to build the push request.
	var chunk chunkData
	if err := json.Unmarshal(data, &chunk); err != nil {
		return fmt.Errorf("cloud: parse chunk for push: %w", err)
	}

	pushReq := pushRequest{
		ChunkID:   chunkID,
		CreatedBy: entry.CreatedBy,
		Data:      convertChunkToPushData(&chunk),
	}

	body, err := json.Marshal(pushReq)
	if err != nil {
		return fmt.Errorf("cloud: marshal push request: %w", err)
	}

	req, err := http.NewRequest("POST", rt.baseURL+"/sync/push", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cloud: build push request: %w", err)
	}
	rt.setAuthorization(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := rt.doWithRetry(req)
	if err != nil {
		return fmt.Errorf("cloud: push chunk %s: %w", chunkID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return rt.httpError("push chunk "+chunkID, resp)
	}
	return nil
}

// ReadChunk downloads a specific chunk from the cloud server.
// GET /sync/pull/{chunk_id} returns the raw stored push body.
// This method extracts the data field and converts it back to ChunkData JSON.
func (rt *RemoteTransport) ReadChunk(chunkID string) ([]byte, error) {
	req, err := http.NewRequest("GET", rt.baseURL+"/sync/pull/"+chunkID, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build pull request: %w", err)
	}
	rt.setAuthorization(req)

	resp, err := rt.doWithRetry(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: pull chunk %s: %w", chunkID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, rt.httpError("pull chunk "+chunkID, resp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloud: read chunk body: %w", err)
	}

	// The server returns the raw push body. Parse it and extract the data field.
	var pushResp pushRequest
	if err := json.Unmarshal(body, &pushResp); err != nil {
		return nil, fmt.Errorf("cloud: parse pull response: %w", err)
	}

	// Convert push data back to ChunkData format for the Syncer.
	chunkData := convertPushDataToChunk(&pushResp.Data)
	result, err := json.Marshal(chunkData)
	if err != nil {
		return nil, fmt.Errorf("cloud: marshal chunk data: %w", err)
	}
	return result, nil
}

// ─── Retry Logic ─────────────────────────────────────────────────────────────

const (
	maxRetries = 3
	baseDelay  = 500 * time.Millisecond
)

// retryable returns true for status codes that should be retried.
func retryable(statusCode int) bool {
	return statusCode == 429 || statusCode >= 500
}

// doWithRetry executes an HTTP request with exponential backoff retry
// for transient failures (429, 5xx) and network errors.
// Client errors (400, 401, 403, 404) are NOT retried.
func (rt *RemoteTransport) doWithRetry(req *http.Request) (*http.Response, error) {
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Reset body for retries.
		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
		rt.setAuthorization(req)

		resp, err := rt.httpClient.Do(req)
		if err != nil {
			// Network error: retry if we have attempts left.
			if attempt < maxRetries {
				sleepWithJitter(attempt)
				continue
			}
			return nil, fmt.Errorf("request failed after %d retries: %w", maxRetries, err)
		}

		if resp.StatusCode == http.StatusUnauthorized && rt.canRefreshToken() {
			resp.Body.Close()
			if err := rt.refreshAccessToken(); err != nil {
				return nil, fmt.Errorf("refresh access token: %w", err)
			}
			if bodyBytes != nil {
				req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
			rt.setAuthorization(req)
			resp, err = rt.httpClient.Do(req)
			if err != nil {
				return nil, fmt.Errorf("request failed after refresh: %w", err)
			}
			if resp.StatusCode != http.StatusUnauthorized {
				return resp, nil
			}
		}

		// Success or non-retryable client error: return immediately.
		if !retryable(resp.StatusCode) {
			return resp, nil
		}

		// Server error or rate limit: close body and retry.
		resp.Body.Close()
		if attempt < maxRetries {
			sleepWithJitter(attempt)
		}
	}
	return nil, fmt.Errorf("server error after %d retries", maxRetries)
}

func (rt *RemoteTransport) setAuthorization(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+rt.currentToken())
}

func (rt *RemoteTransport) currentToken() string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.token
}

func (rt *RemoteTransport) canRefreshToken() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.refreshToken != ""
}

func (rt *RemoteTransport) refreshAccessToken() error {
	rt.mu.Lock()
	refreshToken := rt.refreshToken
	onTokenRefresh := rt.onTokenRefresh
	rt.mu.Unlock()

	body, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return fmt.Errorf("marshal refresh request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, rt.baseURL+"/auth/refresh", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := rt.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return rt.httpError("refresh token", resp)
	}

	var refreshResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refreshResp); err != nil {
		return fmt.Errorf("parse refresh response: %w", err)
	}
	if strings.TrimSpace(refreshResp.AccessToken) == "" {
		return fmt.Errorf("refresh response missing access token")
	}

	rt.mu.Lock()
	rt.token = refreshResp.AccessToken
	rt.mu.Unlock()

	if onTokenRefresh != nil {
		if err := onTokenRefresh(refreshResp.AccessToken); err != nil {
			return fmt.Errorf("persist refreshed token: %w", err)
		}
	}

	return nil
}

// sleepWithJitter sleeps for baseDelay * 2^attempt plus random jitter.
func sleepWithJitter(attempt int) {
	delay := baseDelay * (1 << uint(attempt))
	jitter := time.Duration(rand.Int63n(int64(delay / 4)))
	time.Sleep(delay + jitter)
}

// httpError reads the response body and returns a descriptive error.
func (rt *RemoteTransport) httpError(action string, resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)

	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("cloud: %s: %d %s", action, resp.StatusCode, errResp.Error)
	}
	return fmt.Errorf("cloud: %s: %d %s", action, resp.StatusCode, http.StatusText(resp.StatusCode))
}

// ─── Mutation Push/Pull ──────────────────────────────────────────────────────

// MutationEntry represents a single mutation for push/pull operations.
type MutationEntry struct {
	Entity    string          `json:"entity"`
	EntityKey string          `json:"entity_key"`
	Op        string          `json:"op"`
	Payload   json.RawMessage `json:"payload"`
}

// PushMutationsResult holds the server response for a mutation push.
type PushMutationsResult struct {
	Accepted int   `json:"accepted"`
	LastSeq  int64 `json:"last_seq"`
}

// PullMutationResult represents a single mutation returned by the server.
type PullMutationResult struct {
	Seq        int64           `json:"seq"`
	Author     string          `json:"author,omitempty"`
	Entity     string          `json:"entity"`
	EntityKey  string          `json:"entity_key"`
	Op         string          `json:"op"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt string          `json:"occurred_at"`
}

// PullMutationsResult holds the server response for a mutation pull.
type PullMutationsResponse struct {
	Mutations []PullMutationResult `json:"mutations"`
	HasMore   bool                 `json:"has_more"`
}

// PushMutations sends a batch of mutations to the cloud server.
// POST /sync/mutations/push
func (rt *RemoteTransport) PushMutations(mutations []MutationEntry) (*PushMutationsResult, error) {
	body, err := json.Marshal(map[string]any{"mutations": mutations})
	if err != nil {
		return nil, fmt.Errorf("cloud: marshal mutation push: %w", err)
	}

	req, err := http.NewRequest("POST", rt.baseURL+"/sync/mutations/push", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cloud: build mutation push request: %w", err)
	}
	rt.setAuthorization(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := rt.doWithRetry(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: mutation push: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, rt.httpError("mutation push", resp)
	}

	var result PushMutationsResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("cloud: decode mutation push response: %w", err)
	}
	return &result, nil
}

// PullMutations fetches mutations from the cloud server since a given sequence.
// When teamSync is true (via the transport setting), includes cross-user
// observation mutations from teammates enrolled in the same projects.
// GET /sync/mutations/pull?since_seq=N&limit=M&team_sync=true|false
func (rt *RemoteTransport) PullMutations(sinceSeq int64, limit int) (*PullMutationsResponse, error) {
	if limit <= 0 {
		limit = 100
	}

	teamSyncParam := "true"
	rt.mu.Lock()
	if !rt.teamSync {
		teamSyncParam = "false"
	}
	rt.mu.Unlock()

	u := fmt.Sprintf("%s/sync/mutations/pull?since_seq=%d&limit=%d&team_sync=%s", rt.baseURL, sinceSeq, limit, teamSyncParam)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build mutation pull request: %w", err)
	}
	rt.setAuthorization(req)

	resp, err := rt.doWithRetry(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: mutation pull: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, rt.httpError("mutation pull", resp)
	}

	var result PullMutationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("cloud: decode mutation pull response: %w", err)
	}
	return &result, nil
}

// ─── Enrollment Sync ─────────────────────────────────────────────────────────

// SyncEnrollments pushes the client's enrolled project list to the server.
// POST /sync/enrollments
func (rt *RemoteTransport) SyncEnrollments(projects []string) error {
	body, err := json.Marshal(map[string]any{"projects": projects})
	if err != nil {
		return fmt.Errorf("cloud: marshal enrollment sync: %w", err)
	}

	req, err := http.NewRequest("POST", rt.baseURL+"/sync/enrollments", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cloud: build enrollment sync request: %w", err)
	}
	rt.setAuthorization(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := rt.doWithRetry(req)
	if err != nil {
		return fmt.Errorf("cloud: enrollment sync: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return rt.httpError("enrollment sync", resp)
	}
	return nil
}

// ListEnrollments fetches the server-side enrolled project list.
// GET /sync/enrollments
func (rt *RemoteTransport) ListEnrollments() ([]string, error) {
	req, err := http.NewRequest("GET", rt.baseURL+"/sync/enrollments", nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build enrollment list request: %w", err)
	}
	rt.setAuthorization(req)

	resp, err := rt.doWithRetry(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: enrollment list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, rt.httpError("enrollment list", resp)
	}

	var result struct {
		Projects []string `json:"projects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("cloud: decode enrollment list: %w", err)
	}
	return result.Projects, nil
}

// ─── Data Conversion ─────────────────────────────────────────────────────────

// Internal types matching the cloud server's push/pull JSON format.
// These mirror cloudserver/push_pull.go types.

type pushRequest struct {
	ChunkID   string   `json:"chunk_id"`
	CreatedBy string   `json:"created_by"`
	Data      pushData `json:"data"`
}

type pushData struct {
	Sessions     []pushSession     `json:"sessions"`
	Observations []pushObservation `json:"observations"`
	Prompts      []pushPrompt      `json:"prompts"`
}

type pushSession struct {
	ID        string  `json:"id"`
	Project   string  `json:"project"`
	Directory string  `json:"directory"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
	Summary   *string `json:"summary,omitempty"`
}

type pushObservation struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	ToolName  string `json:"tool_name,omitempty"`
	Project   string `json:"project,omitempty"`
	Scope     string `json:"scope,omitempty"`
	TopicKey  string `json:"topic_key,omitempty"`
}

type pushPrompt struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Project   string `json:"project,omitempty"`
}

// chunkData mirrors sync.ChunkData but avoids importing store types directly
// to keep this package decoupled from the SQLite store.
type chunkData struct {
	Sessions     []sessionData     `json:"sessions"`
	Observations []observationData `json:"observations"`
	Prompts      []promptData      `json:"prompts"`
}

type sessionData struct {
	ID        string  `json:"id"`
	Project   string  `json:"project"`
	Directory string  `json:"directory"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
	Summary   *string `json:"summary,omitempty"`
}

type observationData struct {
	ID             int64   `json:"id"`
	SessionID      string  `json:"session_id"`
	Type           string  `json:"type"`
	Title          string  `json:"title"`
	Content        string  `json:"content"`
	ToolName       *string `json:"tool_name,omitempty"`
	Project        *string `json:"project,omitempty"`
	Scope          string  `json:"scope"`
	TopicKey       *string `json:"topic_key,omitempty"`
	RevisionCount  int     `json:"revision_count"`
	DuplicateCount int     `json:"duplicate_count"`
	LastSeenAt     *string `json:"last_seen_at,omitempty"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	DeletedAt      *string `json:"deleted_at,omitempty"`
}

type promptData struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Project   string `json:"project,omitempty"`
	CreatedAt string `json:"created_at"`
}

// convertChunkToPushData converts ChunkData format to the push request format.
func convertChunkToPushData(c *chunkData) pushData {
	pd := pushData{
		Sessions:     make([]pushSession, 0, len(c.Sessions)),
		Observations: make([]pushObservation, 0, len(c.Observations)),
		Prompts:      make([]pushPrompt, 0, len(c.Prompts)),
	}

	for _, s := range c.Sessions {
		pd.Sessions = append(pd.Sessions, pushSession{
			ID:        s.ID,
			Project:   s.Project,
			Directory: s.Directory,
			StartedAt: s.StartedAt,
			EndedAt:   s.EndedAt,
			Summary:   s.Summary,
		})
	}

	for _, o := range c.Observations {
		obs := pushObservation{
			SessionID: o.SessionID,
			Type:      o.Type,
			Title:     o.Title,
			Content:   o.Content,
			Scope:     o.Scope,
		}
		if o.ToolName != nil {
			obs.ToolName = *o.ToolName
		}
		if o.Project != nil {
			obs.Project = *o.Project
		}
		if o.TopicKey != nil {
			obs.TopicKey = *o.TopicKey
		}
		pd.Observations = append(pd.Observations, obs)
	}

	for _, p := range c.Prompts {
		pd.Prompts = append(pd.Prompts, pushPrompt{
			SessionID: p.SessionID,
			Content:   p.Content,
			Project:   p.Project,
		})
	}

	return pd
}

// convertPushDataToChunk converts the push response format back to ChunkData.
func convertPushDataToChunk(pd *pushData) *chunkData {
	c := &chunkData{
		Sessions:     make([]sessionData, 0, len(pd.Sessions)),
		Observations: make([]observationData, 0, len(pd.Observations)),
		Prompts:      make([]promptData, 0, len(pd.Prompts)),
	}

	for _, s := range pd.Sessions {
		c.Sessions = append(c.Sessions, sessionData{
			ID:        s.ID,
			Project:   s.Project,
			Directory: s.Directory,
			StartedAt: s.StartedAt,
			EndedAt:   s.EndedAt,
			Summary:   s.Summary,
		})
	}

	for _, o := range pd.Observations {
		obs := observationData{
			SessionID: o.SessionID,
			Type:      o.Type,
			Title:     o.Title,
			Content:   o.Content,
			Scope:     o.Scope,
		}
		if o.ToolName != "" {
			obs.ToolName = &o.ToolName
		}
		if o.Project != "" {
			obs.Project = &o.Project
		}
		if o.TopicKey != "" {
			obs.TopicKey = &o.TopicKey
		}
		c.Observations = append(c.Observations, obs)
	}

	for _, p := range pd.Prompts {
		c.Prompts = append(c.Prompts, promptData{
			SessionID: p.SessionID,
			Content:   p.Content,
			Project:   p.Project,
		})
	}

	return c
}
