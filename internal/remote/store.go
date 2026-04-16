package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"time"

	"github.com/Gentleman-Programming/engram/internal/types"
)

// RemoteStore implements types.StoreInterface by proxying all calls
// to the engram cloud server over HTTP. It is stateless — every method
// call results in exactly one HTTP request (REQ-REMOTE-002).
type RemoteStore struct {
	client  *Client
	project string
	ctx     context.Context
}

// Compile-time interface check (REQ-REMOTE-001).
var _ types.StoreInterface = (*RemoteStore)(nil)

// NewRemoteStore creates a new RemoteStore for the given project.
func NewRemoteStore(client *Client, project string) *RemoteStore {
	return &RemoteStore{
		client:  client,
		project: project,
		ctx:     context.Background(),
	}
}

// ─── Read methods (REQ-REMOTE-002 through REQ-REMOTE-005) ──────────────────

// GetObservation fetches an observation by numeric ID.
// Maps ErrNotFound → wraps ErrNotFound (REQ-REMOTE-005).
func (rs *RemoteStore) GetObservation(id int64) (*types.Observation, error) {
	return rs.GetObservationCtx(rs.ctx, id)
}

func (rs *RemoteStore) GetObservationCtx(ctx context.Context, id int64) (*types.Observation, error) {
	path := fmt.Sprintf("/api/v1/observations/%d?project=%s", id, rs.project)
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var obs types.Observation
	if err := json.Unmarshal(body, &obs); err != nil {
		return nil, fmt.Errorf("decode observation: %w", err)
	}
	return &obs, nil
}

// Search calls GET /api/v1/search.
func (rs *RemoteStore) Search(query string, opts types.SearchOptions) ([]types.SearchResult, error) {
	return rs.SearchCtx(rs.ctx, query, opts)
}

func (rs *RemoteStore) SearchCtx(ctx context.Context, query string, opts types.SearchOptions) ([]types.SearchResult, error) {
	project := opts.Project
	if project == "" {
		project = rs.project
	}
	limit := opts.Limit
	if limit == 0 {
		limit = 20
	}
	params := url.Values{}
	params.Set("q", query)
	params.Set("project", project)
	params.Set("limit", fmt.Sprintf("%d", limit))
	if opts.Type != "" {
		params.Set("type", opts.Type)
	}
	if opts.Scope != "" {
		params.Set("scope", opts.Scope)
	}
	path := "/api/v1/search?" + params.Encode()
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Results []types.SearchResult `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode search: %w", err)
	}
	return resp.Results, nil
}

// RecentSessions calls GET /api/v1/sessions.
func (rs *RemoteStore) RecentSessions(project string, limit int) ([]types.SessionSummary, error) {
	return rs.RecentSessionsCtx(rs.ctx, project, limit)
}

func (rs *RemoteStore) RecentSessionsCtx(ctx context.Context, project string, limit int) ([]types.SessionSummary, error) {
	path := fmt.Sprintf("/api/v1/sessions?project=%s&limit=%d", project, limit)
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Sessions []types.SessionSummary `json:"sessions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode sessions: %w", err)
	}
	return resp.Sessions, nil
}

// AllSessions calls GET /api/v1/sessions/all.
func (rs *RemoteStore) AllSessions(project string, limit int) ([]types.SessionSummary, error) {
	return rs.AllSessionsCtx(rs.ctx, project, limit)
}

func (rs *RemoteStore) AllSessionsCtx(ctx context.Context, project string, limit int) ([]types.SessionSummary, error) {
	path := fmt.Sprintf("/api/v1/sessions/all?project=%s&limit=%d", project, limit)
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Sessions []types.SessionSummary `json:"sessions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode sessions: %w", err)
	}
	return resp.Sessions, nil
}

// SessionObservations calls GET /api/v1/sessions/{id}/observations.
func (rs *RemoteStore) SessionObservations(sessionID string, limit int) ([]types.Observation, error) {
	return rs.SessionObservationsCtx(rs.ctx, sessionID, limit)
}

func (rs *RemoteStore) SessionObservationsCtx(ctx context.Context, sessionID string, limit int) ([]types.Observation, error) {
	path := fmt.Sprintf("/api/v1/sessions/%s/observations?limit=%d", sessionID, limit)
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Observations []types.Observation `json:"observations"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode observations: %w", err)
	}
	return resp.Observations, nil
}

// RecentObservations calls GET /api/v1/observations/list.
func (rs *RemoteStore) RecentObservations(project, scope string, limit int) ([]types.Observation, error) {
	return rs.RecentObservationsCtx(rs.ctx, project, scope, limit)
}

func (rs *RemoteStore) RecentObservationsCtx(ctx context.Context, project, scope string, limit int) ([]types.Observation, error) {
	path := fmt.Sprintf("/api/v1/observations/list?project=%s&scope=%s&limit=%d", project, scope, limit)
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Observations []types.Observation `json:"observations"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode observations: %w", err)
	}
	return resp.Observations, nil
}

// AllObservations calls GET /api/v1/observations/all.
func (rs *RemoteStore) AllObservations(project, scope string, limit int) ([]types.Observation, error) {
	return rs.AllObservationsCtx(rs.ctx, project, scope, limit)
}

func (rs *RemoteStore) AllObservationsCtx(ctx context.Context, project, scope string, limit int) ([]types.Observation, error) {
	path := fmt.Sprintf("/api/v1/observations/all?project=%s&scope=%s&limit=%d", project, scope, limit)
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Observations []types.Observation `json:"observations"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode observations: %w", err)
	}
	return resp.Observations, nil
}

// FormatContext calls GET /api/v1/context (REQ-REMOTE-007).
func (rs *RemoteStore) FormatContext(project, scope string) (string, error) {
	return rs.FormatContextCtx(rs.ctx, project, scope)
}

func (rs *RemoteStore) FormatContextCtx(ctx context.Context, project, scope string) (string, error) {
	path := fmt.Sprintf("/api/v1/context?project=%s&scope=%s", project, scope)
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return "", err
	}
	// Response is a JSON string — unmarshal it
	var result string
	if err := json.Unmarshal(body, &result); err != nil {
		// If not a JSON string, return raw body as string
		return string(body), nil
	}
	return result, nil
}

// Timeline calls GET /api/v1/observations/{id}/timeline.
func (rs *RemoteStore) Timeline(observationID int64, before, after int) (*types.TimelineResult, error) {
	return rs.TimelineCtx(rs.ctx, observationID, before, after)
}

func (rs *RemoteStore) TimelineCtx(ctx context.Context, observationID int64, before, after int) (*types.TimelineResult, error) {
	path := fmt.Sprintf("/api/v1/observations/%d/timeline?before=%d&after=%d", observationID, before, after)
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var result types.TimelineResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode timeline: %w", err)
	}
	return &result, nil
}

// Stats calls GET /api/v1/stats.
func (rs *RemoteStore) Stats() (*types.Stats, error) {
	return rs.StatsCtx(rs.ctx)
}

func (rs *RemoteStore) StatsCtx(ctx context.Context) (*types.Stats, error) {
	path := fmt.Sprintf("/api/v1/stats?project=%s", rs.project)
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var stats types.Stats
	if err := json.Unmarshal(body, &stats); err != nil {
		return nil, fmt.Errorf("decode stats: %w", err)
	}
	return &stats, nil
}

// RecentPrompts calls GET /api/v1/prompts.
func (rs *RemoteStore) RecentPrompts(project string, limit int) ([]types.Prompt, error) {
	return rs.RecentPromptsCtx(rs.ctx, project, limit)
}

func (rs *RemoteStore) RecentPromptsCtx(ctx context.Context, project string, limit int) ([]types.Prompt, error) {
	path := fmt.Sprintf("/api/v1/prompts?project=%s&limit=%d", project, limit)
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Prompts []types.Prompt `json:"prompts"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode prompts: %w", err)
	}
	return resp.Prompts, nil
}

// SearchPrompts calls GET /api/v1/prompts/search.
func (rs *RemoteStore) SearchPrompts(query string, project string, limit int) ([]types.Prompt, error) {
	return rs.SearchPromptsCtx(rs.ctx, query, project, limit)
}

func (rs *RemoteStore) SearchPromptsCtx(ctx context.Context, query string, project string, limit int) ([]types.Prompt, error) {
	params := url.Values{}
	params.Set("q", query)
	params.Set("project", project)
	params.Set("limit", fmt.Sprintf("%d", limit))
	path := "/api/v1/prompts/search?" + params.Encode()
	body, err := rs.client.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Prompts []types.Prompt `json:"prompts"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode prompts: %w", err)
	}
	return resp.Prompts, nil
}

// ─── Write methods via sync/push (Decision 4) ───────────────────────────────

// pushResult is the server response to POST /api/v1/sync/push.
type pushResult struct {
	AckedSeq  int64            `json:"acked_seq"`
	ServerSeq int64            `json:"server_seq"`
	EntityIDs map[string]int64 `json:"entity_ids"`
}

// pushMutation sends a single mutation via POST /api/v1/sync/push and returns the result.
func (rs *RemoteStore) pushMutation(ctx context.Context, entity, entityKey, op string, payload map[string]any) (*pushResult, error) {
	mutation := map[string]any{
		"seq":         1,
		"entity":      entity,
		"entity_key":  entityKey,
		"op":          op,
		"payload":     payload,
		"occurred_at": time.Now().UTC().Format(time.RFC3339),
	}
	reqBody, err := json.Marshal(map[string]any{
		"device_id": "remote-store",
		"project":   rs.project,
		"mutations": []any{mutation},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal push: %w", err)
	}
	body, err := rs.client.Post(ctx, "/api/v1/sync/push", reqBody)
	if err != nil {
		return nil, err
	}
	var result pushResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode push result: %w", err)
	}
	return &result, nil
}

// randomHex generates a random hex suffix for sync IDs.
func randomHex(n int) string {
	const letters = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// AddObservation creates an observation via push and returns the server-assigned numeric ID.
func (rs *RemoteStore) AddObservation(p types.AddObservationParams) (int64, error) {
	syncID := "obs-" + randomHex(8)
	payload := map[string]any{
		"sync_id":    syncID,
		"session_id": p.SessionID,
		"type":       p.Type,
		"title":      p.Title,
		"content":    p.Content,
		"tool_name":  p.ToolName,
		"project":    p.Project,
		"scope":      p.Scope,
		"topic_key":  p.TopicKey,
	}
	result, err := rs.pushMutation(rs.ctx, "observation", syncID, "upsert", payload)
	if err != nil {
		return 0, err
	}
	return result.EntityIDs[syncID], nil
}

// UpdateObservation updates an observation via push.
// Returns a minimal Observation with the ID set.
func (rs *RemoteStore) UpdateObservation(id int64, p types.UpdateObservationParams) (*types.Observation, error) {
	syncID := fmt.Sprintf("obs-update-%d", id)
	payload := map[string]any{
		"numeric_id": id,
	}
	if p.Type != nil {
		payload["type"] = *p.Type
	}
	if p.Title != nil {
		payload["title"] = *p.Title
	}
	if p.Content != nil {
		payload["content"] = *p.Content
	}
	if p.Project != nil {
		payload["project"] = *p.Project
	}
	if p.Scope != nil {
		payload["scope"] = *p.Scope
	}
	if p.TopicKey != nil {
		payload["topic_key"] = *p.TopicKey
	}
	_, err := rs.pushMutation(rs.ctx, "observation", syncID, "upsert", payload)
	if err != nil {
		return nil, err
	}
	// Return a minimal observation — the cloud stores it, we return what we know
	return &types.Observation{ID: id}, nil
}

// DeleteObservation deletes an observation via push.
func (rs *RemoteStore) DeleteObservation(id int64, hardDelete bool) error {
	syncID := fmt.Sprintf("obs-delete-%d", id)
	payload := map[string]any{
		"numeric_id":  id,
		"hard_delete": hardDelete,
	}
	_, err := rs.pushMutation(rs.ctx, "observation", syncID, "delete", payload)
	return err
}

// CreateSession creates a session via push.
func (rs *RemoteStore) CreateSession(id, project, directory string) error {
	payload := map[string]any{
		"sync_id":   id,
		"project":   project,
		"directory": directory,
	}
	_, err := rs.pushMutation(rs.ctx, "session", id, "upsert", payload)
	return err
}

// EndSession ends a session via push.
func (rs *RemoteStore) EndSession(id string, summary string) error {
	payload := map[string]any{
		"sync_id": id,
		"summary": summary,
		"ended":   true,
	}
	_, err := rs.pushMutation(rs.ctx, "session", id, "upsert", payload)
	return err
}

// DeleteSession deletes a session via push.
func (rs *RemoteStore) DeleteSession(id string) error {
	payload := map[string]any{
		"sync_id": id,
	}
	_, err := rs.pushMutation(rs.ctx, "session", id, "delete", payload)
	return err
}

// AddPrompt creates a prompt via push and returns the server-assigned numeric ID.
func (rs *RemoteStore) AddPrompt(p types.AddPromptParams) (int64, error) {
	syncID := "prompt-" + randomHex(8)
	payload := map[string]any{
		"sync_id":    syncID,
		"session_id": p.SessionID,
		"content":    p.Content,
		"project":    p.Project,
	}
	result, err := rs.pushMutation(rs.ctx, "prompt", syncID, "upsert", payload)
	if err != nil {
		return 0, err
	}
	return result.EntityIDs[syncID], nil
}

// DeletePrompt deletes a prompt via push.
func (rs *RemoteStore) DeletePrompt(id int64) error {
	syncID := fmt.Sprintf("prompt-delete-%d", id)
	payload := map[string]any{
		"numeric_id": id,
	}
	_, err := rs.pushMutation(rs.ctx, "prompt", syncID, "delete", payload)
	return err
}

// ─── Composite methods (REQ-REMOTE-006 Tier 2) ─────────────────────────────

// PassiveCapture calls POST /api/v1/passive-capture.
func (rs *RemoteStore) PassiveCapture(p types.PassiveCaptureParams) (*types.PassiveCaptureResult, error) {
	reqBody, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal passive-capture: %w", err)
	}
	body, err := rs.client.Post(rs.ctx, "/api/v1/passive-capture", reqBody)
	if err != nil {
		return nil, err
	}
	var result types.PassiveCaptureResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode passive-capture: %w", err)
	}
	return &result, nil
}

// MigrateProject calls POST /api/v1/projects/migrate.
func (rs *RemoteStore) MigrateProject(oldName, newName string) (*types.MigrateResult, error) {
	reqBody, err := json.Marshal(map[string]string{
		"old_name": oldName,
		"new_name": newName,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal migrate: %w", err)
	}
	body, err := rs.client.Post(rs.ctx, "/api/v1/projects/migrate", reqBody)
	if err != nil {
		return nil, err
	}
	var result types.MigrateResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode migrate: %w", err)
	}
	return &result, nil
}

