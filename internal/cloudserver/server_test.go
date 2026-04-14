//go:build integration

package cloudserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloudstore"
)

const defaultTestDSN = "postgres://engram:testpass@localhost:5433/engram_test?sslmode=disable"

func testDSN() string {
	if v := os.Getenv("ENGRAM_TEST_DSN"); v != "" {
		return v
	}
	return defaultTestDSN
}

// testSetup creates a real store, runs migrations, cleans tables, creates a user+project,
// and returns the wired HTTP handler plus the API key for authenticated requests.
type testEnv struct {
	handler http.Handler
	store   *cloudstore.Store
	userID  string
	apiKey  string
	project string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()

	store, err := cloudstore.New(ctx, testDSN())
	if err != nil {
		t.Skipf("PostgreSQL not available: %v", err)
	}

	if err := store.RunMigrations(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	// Clean all tables (order respects FK dependencies)
	tables := []string{
		"rate_limits", "idempotency_keys", "sync_cursors",
		"observation_revisions", "prompts", "sessions", "observations",
		"project_members", "projects", "users", "server_seq_counter",
	}
	for _, table := range tables {
		if _, err := store.Pool().Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}

	// Create user + project + membership
	userID, rawKey, err := store.CreateUser(ctx, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := store.CreateProject(ctx, "test-proj"); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := store.AddMember(ctx, "test-proj", "test@example.com", "owner"); err != nil {
		t.Fatalf("add member: %v", err)
	}

	handler := New(store)
	t.Cleanup(func() { store.Close() })

	return &testEnv{
		handler: handler,
		store:   store,
		userID:  userID,
		apiKey:  rawKey,
		project: "test-proj",
	}
}

// do creates and executes an HTTP request against the test handler.
func (e *testEnv) do(t *testing.T, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody *bytes.Buffer
	if body != nil {
		data, _ := json.Marshal(body)
		reqBody = bytes.NewBuffer(data)
	} else {
		reqBody = &bytes.Buffer{}
	}

	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// authHeaders returns standard headers for an authenticated request.
func (e *testEnv) authHeaders() map[string]string {
	return map[string]string{
		"Authorization":    "Bearer " + e.apiKey,
		"X-Engram-Protocol": "1",
	}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

// ─── Health ─────────────────────────────────────────────────────────────────

func TestHTTP_HealthNoAuth(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, "GET", "/api/v1/health", nil, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["status"] != "ok" {
		t.Fatalf("expected status=ok, got %v", body["status"])
	}
	if body["version"] != version {
		t.Fatalf("expected version=%s, got %v", version, body["version"])
	}
}

// ─── Auth Middleware ─────────────────────────────────────────────────────────

func TestHTTP_AuthMissing(t *testing.T) {
	env := newTestEnv(t)

	// No Authorization header, but with protocol header
	rec := env.do(t, "GET", "/api/v1/projects", nil, map[string]string{
		"X-Engram-Protocol": "1",
	})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHTTP_AuthInvalidKey(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, "GET", "/api/v1/projects", nil, map[string]string{
		"Authorization":    "Bearer engram_sk_invalid_key",
		"X-Engram-Protocol": "1",
	})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHTTP_AuthValidKey(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, "GET", "/api/v1/projects", nil, env.authHeaders())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// ─── Protocol Version Middleware ─────────────────────────────────────────────

func TestHTTP_ProtocolVersionMissing(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, "GET", "/api/v1/projects", nil, map[string]string{
		"Authorization": "Bearer " + env.apiKey,
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHTTP_ProtocolVersionZero(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, "GET", "/api/v1/projects", nil, map[string]string{
		"Authorization":    "Bearer " + env.apiKey,
		"X-Engram-Protocol": "0",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["error"] != "client version too old, please upgrade" {
		t.Fatalf("unexpected error: %v", body["error"])
	}
}

// ─── Rate Limiting ──────────────────────────────────────────────────────────

func TestHTTP_RateLimitExceeded(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Pre-fill the rate limit counter to exceed the push limit (60/min)
	_, err := env.store.Pool().Exec(ctx, `
		INSERT INTO rate_limits (user_id, endpoint, window_start, request_count)
		VALUES ($1, 'push', date_trunc('minute', now()), 61)
	`, env.userID)
	if err != nil {
		t.Fatalf("insert rate limit: %v", err)
	}

	pushBody := map[string]any{
		"device_id":  "dev1",
		"project":    env.project,
		"mutations":  []any{},
	}
	rec := env.do(t, "POST", "/api/v1/sync/push", pushBody, env.authHeaders())

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}

// ─── Push: Non-Member ───────────────────────────────────────────────────────

func TestHTTP_PushForbiddenNonMember(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Create a second user who is NOT a member
	_, bobKey, err := env.store.CreateUser(ctx, "Bob", "bob@test.com")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	pushBody := map[string]any{
		"device_id": "dev1",
		"project":   env.project,
		"mutations": []map[string]any{{
			"seq": 1, "entity": "observation", "entity_key": "obs-1", "op": "upsert",
			"payload": map[string]any{"sync_id": "obs-1", "session_id": "s1", "type": "decision", "title": "T", "content": "C", "scope": "project"},
		}},
	}
	rec := env.do(t, "POST", "/api/v1/sync/push", pushBody, map[string]string{
		"Authorization":    "Bearer " + bobKey,
		"X-Engram-Protocol": "1",
	})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// ─── Pull: Non-Member ───────────────────────────────────────────────────────

func TestHTTP_PullForbiddenNonMember(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	_, bobKey, err := env.store.CreateUser(ctx, "Bob", "bob@test.com")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	rec := env.do(t, "GET", "/api/v1/sync/pull?project="+env.project+"&since_seq=0", nil, map[string]string{
		"Authorization":    "Bearer " + bobKey,
		"X-Engram-Protocol": "1",
	})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// ─── Search ─────────────────────────────────────────────────────────────────

func TestHTTP_SearchReturnsResults(t *testing.T) {
	env := newTestEnv(t)

	// Push an observation first
	pushBody := map[string]any{
		"device_id": "dev1",
		"project":   env.project,
		"mutations": []map[string]any{{
			"seq": 1, "entity": "observation", "entity_key": "obs-search", "op": "upsert",
			"payload": map[string]any{
				"sync_id": "obs-search", "session_id": "s1", "type": "decision",
				"title": "Kubernetes deployment strategy", "content": "Rolling update with canary", "scope": "project",
			},
		}},
	}
	rec := env.do(t, "POST", "/api/v1/sync/push", pushBody, env.authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("push failed: %d", rec.Code)
	}

	// Search
	rec = env.do(t, "GET", "/api/v1/search?q=kubernetes+deployment&project="+env.project, nil, env.authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := decodeBody(t, rec)
	count, _ := body["count"].(float64)
	if count < 1 {
		t.Fatalf("expected at least 1 result, got %v", body["count"])
	}
}

func TestHTTP_SearchMissingParams(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, "GET", "/api/v1/search?project="+env.project, nil, env.authHeaders())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing q, got %d", rec.Code)
	}

	rec = env.do(t, "GET", "/api/v1/search?q=test", nil, env.authHeaders())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing project, got %d", rec.Code)
	}
}

// ─── Batch ──────────────────────────────────────────────────────────────────

func TestHTTP_BatchProcessesOperations(t *testing.T) {
	env := newTestEnv(t)

	// Use protected endpoints — batch sub-requests go through the full router
	batchBody := map[string]any{
		"operations": []map[string]any{
			{"method": "GET", "path": "/api/v1/stats?project=" + env.project},
			{"method": "GET", "path": "/api/v1/projects"},
		},
	}
	rec := env.do(t, "POST", "/api/v1/batch", batchBody, env.authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := decodeBody(t, rec)
	results, ok := body["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("expected 2 results, got %v", body["results"])
	}

	// Stats should be 200
	r0, _ := results[0].(map[string]any)
	if status, _ := r0["status"].(float64); status != 200 {
		t.Fatalf("expected batch[0] status=200, got %v", r0["status"])
	}
	// Projects should be 200
	r1, _ := results[1].(map[string]any)
	if status, _ := r1["status"].(float64); status != 200 {
		t.Fatalf("expected batch[1] status=200, got %v", r1["status"])
	}
}

func TestHTTP_BatchPostSubOperation(t *testing.T) {
	env := newTestEnv(t)

	// Batch with a POST push sub-operation to validate chi routing for POST
	pushPayload := map[string]any{
		"device_id": "dev1",
		"project":   env.project,
		"mutations": []map[string]any{{
			"seq": 1, "entity": "observation", "entity_key": "obs-batch-post", "op": "upsert",
			"payload": map[string]any{
				"sync_id": "obs-batch-post", "session_id": "s1", "type": "decision",
				"title": "Batch POST test", "content": "Created via batch", "scope": "project",
			},
		}},
	}
	pushBody, _ := json.Marshal(pushPayload)

	batchBody := map[string]any{
		"operations": []map[string]any{
			{"method": "POST", "path": "/api/v1/sync/push", "body": json.RawMessage(pushBody)},
			{"method": "GET", "path": "/api/v1/stats?project=" + env.project},
		},
	}
	rec := env.do(t, "POST", "/api/v1/batch", batchBody, env.authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := decodeBody(t, rec)
	results, ok := body["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("expected 2 results, got %v", body["results"])
	}

	// Push sub-operation should succeed
	r0, _ := results[0].(map[string]any)
	if status, _ := r0["status"].(float64); status != 200 {
		t.Fatalf("expected batch[0] (push) status=200, got %v — body: %s", r0["status"], r0["body"])
	}

	// Stats should also succeed
	r1, _ := results[1].(map[string]any)
	if status, _ := r1["status"].(float64); status != 200 {
		t.Fatalf("expected batch[1] (stats) status=200, got %v", r1["status"])
	}
}

func TestHTTP_BatchEmptyOperations(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, "POST", "/api/v1/batch", map[string]any{"operations": []any{}}, env.authHeaders())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHTTP_BatchExceedsMax(t *testing.T) {
	env := newTestEnv(t)

	ops := make([]map[string]any, 21)
	for i := range ops {
		ops[i] = map[string]any{"method": "GET", "path": "/api/v1/health"}
	}
	rec := env.do(t, "POST", "/api/v1/batch", map[string]any{"operations": ops}, env.authHeaders())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ─── Push + Pull happy path ─────────────────────────────────────────────────

func TestHTTP_PushPullHappyPath(t *testing.T) {
	env := newTestEnv(t)

	// Push
	pushBody := map[string]any{
		"device_id": "dev1",
		"project":   env.project,
		"mutations": []map[string]any{{
			"seq": 1, "entity": "observation", "entity_key": "obs-hp", "op": "upsert",
			"payload": map[string]any{
				"sync_id": "obs-hp", "session_id": "s1", "type": "decision",
				"title": "Happy path obs", "content": "Test content", "scope": "project",
			},
		}},
	}
	rec := env.do(t, "POST", "/api/v1/sync/push", pushBody, env.authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("push: expected 200, got %d", rec.Code)
	}
	pushResult := decodeBody(t, rec)
	if pushResult["server_seq"].(float64) < 1 {
		t.Fatal("expected server_seq >= 1")
	}

	// Pull
	rec = env.do(t, "GET", "/api/v1/sync/pull?project="+env.project+"&since_seq=0", nil, env.authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("pull: expected 200, got %d", rec.Code)
	}
	pullResult := decodeBody(t, rec)
	entities, _ := pullResult["entities"].([]any)
	if len(entities) == 0 {
		t.Fatal("expected entities from pull")
	}
}
