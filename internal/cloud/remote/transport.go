package remote

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v2/internal/sync"
)

const (
	ordinaryOperationTimeout = 30 * time.Second
	writeChunkTimeout        = 5 * time.Minute
)

type RemoteTransport struct {
	baseURL         string
	token           string
	project         string
	httpClient      *http.Client
	writeHTTPClient *http.Client
	extraHeaders    []extraHeader
}

type HTTPStatusError struct {
	Operation  string
	StatusCode int
	ErrorClass string
	ErrorCode  string
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("cloud: %s: status %d: %s", e.Operation, e.StatusCode, strings.TrimSpace(e.Body))
}

func (e *HTTPStatusError) IsAuthFailure() bool {
	return e != nil && e.StatusCode == http.StatusUnauthorized
}

func (e *HTTPStatusError) IsPolicyFailure() bool {
	return e != nil && e.StatusCode == http.StatusForbidden
}

func (e *HTTPStatusError) IsRepairableMigrationFailure() bool {
	return e != nil && strings.TrimSpace(strings.ToLower(e.ErrorClass)) == "repairable"
}

func (e *HTTPStatusError) IsRepairable() bool {
	return e.IsRepairableMigrationFailure()
}

func newHTTPStatusError(operation string, statusCode int, body []byte) error {
	errorClass := ""
	errorCode := ""
	message := strings.TrimSpace(string(body))
	var payload struct {
		ErrorClass string `json:"error_class"`
		ErrorCode  string `json:"error_code"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		errorClass = strings.TrimSpace(payload.ErrorClass)
		errorCode = strings.TrimSpace(payload.ErrorCode)
		if msg := strings.TrimSpace(payload.Error); msg != "" {
			message = msg
		}
	}
	return &HTTPStatusError{
		Operation:  operation,
		StatusCode: statusCode,
		ErrorClass: errorClass,
		ErrorCode:  errorCode,
		Body:       message,
	}
}

func NewRemoteTransport(baseURL, token, project string) (*RemoteTransport, error) {
	normalized, token, err := validateBearerBaseURL(baseURL, token)
	if err != nil {
		return nil, err
	}
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("cloud: project is required")
	}
	extraHeaders := extraHeadersFromEnv()
	if err := validateExtraHeadersScheme(normalized, extraHeaders); err != nil {
		return nil, err
	}
	return &RemoteTransport{
		baseURL:         normalized,
		token:           token,
		project:         project,
		httpClient:      newRemoteHTTPClient(ordinaryOperationTimeout, token, extraHeaders),
		writeHTTPClient: newRemoteHTTPClient(writeChunkTimeout, token, extraHeaders),
		extraHeaders:    extraHeaders,
	}, nil
}

func validateBearerBaseURL(baseURL, token string) (string, string, error) {
	normalized, err := validateBaseURL(baseURL)
	if err != nil {
		return "", "", err
	}
	token = strings.TrimSpace(token)
	if token != "" && !strings.HasPrefix(normalized, "https://") {
		return "", "", fmt.Errorf("cloud: bearer token requires an HTTPS remote URL")
	}
	return normalized, token, nil
}

// newRemoteHTTPClient builds the HTTP client used by the cloud transports.
//
// The redirect policy deliberately keeps the pre-extra-header behavior of
// main for bearer-token-only clients: they must reject HTTPS downgrades, but
// may follow cross-origin HTTPS redirects, where Go withholds the
// Authorization header as appropriate. Configured ENGRAM_CLOUD_EXTRA_HEADERS
// can carry service credentials that Go would forward verbatim across a
// cross-origin redirect, so clients with extra headers additionally require
// every redirect to stay on the same normalized origin.
func newRemoteHTTPClient(timeout time.Duration, token string, extraHeaders []extraHeader) *http.Client {
	client := &http.Client{Timeout: timeout}
	if token == "" && len(extraHeaders) == 0 {
		return client
	}
	requireSameOrigin := len(extraHeaders) > 0
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("cloud: redirect requires HTTPS")
		}
		// Go already drops the bearer on cross-origin redirects, but
		// configured extra headers (which can carry service credentials)
		// would be forwarded verbatim. Never follow a redirect away from
		// the original normalized origin (scheme, hostname, effective
		// port); path-only and same-origin redirects remain allowed.
		if requireSameOrigin && len(via) > 0 && redirectOrigin(req.URL) != redirectOrigin(via[0].URL) {
			return fmt.Errorf("cloud: redirect to a different origin is rejected to keep configured credentials private")
		}
		return nil
	}
	return client
}

// redirectOrigin returns the normalized scheme://host:port identity used to
// decide whether a redirect keeps configured credentials on the same origin.
func redirectOrigin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return scheme + "://" + host + ":" + port
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
	if strings.TrimSpace(parsed.RawQuery) != "" {
		return "", fmt.Errorf("cloud: invalid remote url: query is not allowed")
	}
	if strings.TrimSpace(parsed.Fragment) != "" {
		return "", fmt.Errorf("cloud: invalid remote url: fragment is not allowed")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (rt *RemoteTransport) endpointURL(query url.Values, parts ...string) (string, error) {
	endpoint, err := url.JoinPath(rt.baseURL, parts...)
	if err != nil {
		return "", fmt.Errorf("cloud: build request url: %w", err)
	}
	if len(query) == 0 {
		return endpoint, nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("cloud: build request url: %w", err)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// applyHeaders sets the configured bearer authorization (when a token is
// present) followed by the statically configured ENGRAM_CLOUD_EXTRA_HEADERS on
// every outgoing request.
func (rt *RemoteTransport) applyHeaders(req *http.Request) {
	if rt.token != "" {
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	for _, header := range rt.extraHeaders {
		req.Header.Set(header.key, header.value)
	}
}

func (rt *RemoteTransport) ReadManifest() (*engramsync.Manifest, error) {
	reqURL, err := rt.endpointURL(url.Values{"project": []string{rt.project}}, "sync", "pull")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build manifest request: %w", err)
	}
	rt.applyHeaders(req)

	resp, err := rt.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, newHTTPStatusError("fetch manifest", resp.StatusCode, body)
	}

	var m engramsync.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("cloud: parse manifest: %w", err)
	}
	return &m, nil
}

func (rt *RemoteTransport) WriteManifest(_ *engramsync.Manifest) error {
	return nil
}

func (rt *RemoteTransport) WriteChunk(chunkID string, data []byte, entry engramsync.ChunkEntry) error {
	canonicalData, err := chunkcodec.CanonicalizeForProject(data, rt.project)
	if err != nil {
		return fmt.Errorf("cloud: canonicalize push chunk: %w", err)
	}
	canonicalChunkID := chunkcodec.ChunkID(canonicalData)
	if strings.TrimSpace(chunkID) != "" && strings.TrimSpace(chunkID) != canonicalChunkID {
		chunkID = canonicalChunkID
	}

	pushPayload, err := json.Marshal(map[string]any{
		"chunk_id":          canonicalChunkID,
		"created_by":        entry.CreatedBy,
		"client_created_at": strings.TrimSpace(entry.CreatedAt),
		"project":           rt.project,
		"data":              json.RawMessage(canonicalData),
	})
	if err != nil {
		return fmt.Errorf("cloud: marshal push request: %w", err)
	}
	body, err := chunkcodec.EncodeCompressedEnvelope(pushPayload)
	if err != nil {
		return fmt.Errorf("cloud: compress push request: %w", err)
	}
	pushURL, err := rt.endpointURL(nil, "sync", "push")
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, pushURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cloud: build push request: %w", err)
	}
	req.Header.Set("Content-Type", chunkcodec.CompressedEnvelopeContentType())
	rt.applyHeaders(req)

	resp, err := rt.writeHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("cloud: push chunk %s: %w", chunkID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return newHTTPStatusError(fmt.Sprintf("push chunk %s", chunkID), resp.StatusCode, body)
	}
	return nil
}

func (rt *RemoteTransport) ReadChunk(chunkID string) ([]byte, error) {
	reqURL, err := rt.endpointURL(url.Values{"project": []string{rt.project}}, "sync", "pull", chunkID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build pull request: %w", err)
	}
	req.Header.Set("Accept", chunkcodec.CompressedEnvelopeContentType())
	rt.applyHeaders(req)
	resp, err := rt.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: pull chunk %s: %w", chunkID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, engramsync.ErrChunkNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, newHTTPStatusError(fmt.Sprintf("pull chunk %s", chunkID), resp.StatusCode, body)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloud: read chunk %s response: %w", chunkID, err)
	}
	if len(data) == 0 {
		return nil, errors.New("cloud: empty chunk payload")
	}
	compressed, err := chunkcodec.IsCompressedEnvelopeContentType(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, fmt.Errorf("cloud: parse chunk response encoding: %w", err)
	}
	if compressed {
		data, err = chunkcodec.DecodeCompressedEnvelope(data, chunkcodec.DefaultMaxDecodedBytes)
		if err != nil {
			return nil, fmt.Errorf("cloud: decode compressed chunk %s: %w", chunkID, err)
		}
	}
	return data, nil
}

type MutationEntry struct {
	Project   string          `json:"project"`
	Entity    string          `json:"entity"`
	EntityKey string          `json:"entity_key"`
	Op        string          `json:"op"`
	Payload   json.RawMessage `json:"payload"`
}

type PushMutationsResult struct {
	AcceptedSeqs []int64 `json:"accepted_seqs"`
}

type PulledMutation struct {
	Seq        int64           `json:"seq"`
	Project    string          `json:"project"`
	Entity     string          `json:"entity"`
	EntityKey  string          `json:"entity_key"`
	Op         string          `json:"op"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt string          `json:"occurred_at"`
}

type PullMutationsResponse struct {
	Mutations []PulledMutation `json:"mutations"`
	HasMore   bool             `json:"has_more"`
	LatestSeq int64            `json:"latest_seq"`
}

func (rt *RemoteTransport) PushMutations(_ []MutationEntry) (*PushMutationsResult, error) {
	return nil, fmt.Errorf("cloud: mutation push is not available in this release")
}

func (rt *RemoteTransport) PullMutations(_ int64, _ int) (*PullMutationsResponse, error) {
	return nil, fmt.Errorf("cloud: mutation pull is not available in this release")
}

// ─── MutationTransport ────────────────────────────────────────────────────────

// MutationTransport handles push/pull of fine-grained mutations to the cloud server.
// Unlike RemoteTransport (which handles chunk-level sync), this operates on the
// mutation journal and supports cursor-based pull.
type MutationTransport struct {
	baseURL      string
	token        string
	httpClient   *http.Client
	extraHeaders []extraHeader
}

// NewMutationTransport creates a MutationTransport. baseURL must be a valid HTTP(S) URL;
// bearer-token transports require HTTPS.
func NewMutationTransport(baseURL, token string) (*MutationTransport, error) {
	normalized, token, err := validateBearerBaseURL(baseURL, token)
	if err != nil {
		return nil, err
	}
	extraHeaders := extraHeadersFromEnv()
	if err := validateExtraHeadersScheme(normalized, extraHeaders); err != nil {
		return nil, err
	}
	return &MutationTransport{
		baseURL:      normalized,
		token:        token,
		httpClient:   newRemoteHTTPClient(ordinaryOperationTimeout, token, extraHeaders),
		extraHeaders: extraHeaders,
	}, nil
}

// applyHeaders sets the configured bearer authorization (when a token is
// present) followed by the statically configured ENGRAM_CLOUD_EXTRA_HEADERS on
// every outgoing request.
func (mt *MutationTransport) applyHeaders(req *http.Request) {
	if mt.token != "" {
		req.Header.Set("Authorization", "Bearer "+mt.token)
	}
	for _, header := range mt.extraHeaders {
		req.Header.Set(header.key, header.value)
	}
}

// PushMutations POSTs a batch of mutations to the cloud server.
// REQ-200: 404 → reason_code=server_unsupported; 401 → IsAuthFailure.
func (mt *MutationTransport) PushMutations(entries []MutationEntry) ([]int64, error) {
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		return nil, fmt.Errorf("cloud: marshal mutation push: %w", err)
	}

	reqURL := mt.baseURL + "/sync/mutations/push"
	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cloud: build mutation push request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	mt.applyHeaders(req)

	resp, err := mt.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: mutation push: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		statusErr := newMutationHTTPStatusError("mutation push", resp.StatusCode, respBody)
		return nil, statusErr
	}

	var result struct {
		AcceptedSeqs []int64 `json:"accepted_seqs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("cloud: decode mutation push response: %w", err)
	}
	return result.AcceptedSeqs, nil
}

// PullMutations fetches mutations from the cloud server since the given sequence.
// REQ-201: 404 → reason_code=server_unsupported; 401 → IsAuthFailure.
func (mt *MutationTransport) PullMutations(sinceSeq int64, limit int) (*PullMutationsResponse, error) {
	reqURL := fmt.Sprintf("%s/sync/mutations/pull?since_seq=%d&limit=%d", mt.baseURL, sinceSeq, limit)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cloud: build mutation pull request: %w", err)
	}
	mt.applyHeaders(req)

	resp, err := mt.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: mutation pull: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		statusErr := newMutationHTTPStatusError("mutation pull", resp.StatusCode, respBody)
		return nil, statusErr
	}

	var result PullMutationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("cloud: decode mutation pull response: %w", err)
	}
	return &result, nil
}

// newMutationHTTPStatusError creates an HTTPStatusError for mutation transport operations.
// REQ-214: 404 → ErrorCode="server_unsupported".
func newMutationHTTPStatusError(operation string, statusCode int, body []byte) error {
	// Try to parse standard error envelope first.
	var payload struct {
		ErrorClass string `json:"error_class"`
		ErrorCode  string `json:"error_code"`
		Error      string `json:"error"`
	}
	message := strings.TrimSpace(string(body))
	if err := json.Unmarshal(body, &payload); err == nil {
		if msg := strings.TrimSpace(payload.Error); msg != "" {
			message = msg
		}
	}

	// REQ-214 + BC3: 404 maps to server_unsupported and emits an operator warning.
	errorCode := strings.TrimSpace(payload.ErrorCode)
	if statusCode == http.StatusNotFound {
		errorCode = "server_unsupported"
		log.Printf("[autosync] cloud mutation endpoint returned 404 (server_unsupported); deploy the new server first before enabling ENGRAM_CLOUD_AUTOSYNC=1")
	}

	return &HTTPStatusError{
		Operation:  operation,
		StatusCode: statusCode,
		ErrorClass: strings.TrimSpace(payload.ErrorClass),
		ErrorCode:  errorCode,
		Body:       message,
	}
}
