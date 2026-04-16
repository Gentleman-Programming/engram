package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/types"
)

// ─── helpers ────────────────────────────────────────────────────────────────

func newTestRemoteStore(t *testing.T, handler http.Handler) *RemoteStore {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := NewClient(srv.URL, "tok-test", "test")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return NewRemoteStore(client, "testproject")
}

func handlerReturning(status int, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}
}

// ─── T28: Compile-time interface check ──────────────────────────────────────

// This test verifies that RemoteStore exists and satisfies StoreInterface at compile time.
// If the var _ line in store.go is missing or wrong, this file won't compile.
func TestRemoteStore_ImplementsStoreInterface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	client, err := NewClient(srv.URL, "tok-test", "test")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	var _ types.StoreInterface = NewRemoteStore(client, "proj")
}

// ─── T29: Read methods ───────────────────────────────────────────────────────

// REQ-REMOTE-002: two identical GetObservation calls → 2 HTTP requests (no caching)
func TestRemoteStore_GetObservation_NoCache(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		json.NewEncoder(w).Encode(types.Observation{ID: 42, SyncID: "obs-abc", Title: "test"})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	ctx := context.Background()
	_, _ = rs.GetObservationCtx(ctx, 42)
	_, _ = rs.GetObservationCtx(ctx, 42)

	if requestCount != 2 {
		t.Errorf("expected 2 HTTP requests for 2 GetObservation calls, got %d", requestCount)
	}
}

// REQ-REMOTE-005: ErrNotFound → types.ErrObservationNotFound (or nil, formatted error)
func TestRemoteStore_GetObservation_NotFound(t *testing.T) {
	rs := newTestRemoteStore(t, handlerReturning(http.StatusNotFound, map[string]string{"error": "not found"}))
	ctx := context.Background()
	obs, err := rs.GetObservationCtx(ctx, 999)
	if obs != nil {
		t.Errorf("expected nil observation on 404, got %+v", obs)
	}
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	// Must wrap ErrNotFound from remote errors
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected error to wrap ErrNotFound, got: %v", err)
	}
}

func TestRemoteStore_GetObservation_Success(t *testing.T) {
	want := types.Observation{ID: 42, SyncID: "obs-abc", Title: "hello"}
	rs := newTestRemoteStore(t, handlerReturning(http.StatusOK, want))
	ctx := context.Background()
	got, err := rs.GetObservationCtx(ctx, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != 42 || got.Title != "hello" {
		t.Errorf("unexpected observation: %+v", got)
	}
}

func TestRemoteStore_Search(t *testing.T) {
	want := map[string]any{
		"results": []map[string]any{
			{"id": float64(1), "title": "found it", "rank": 0.9},
		},
	}
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.String()
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")
	ctx := context.Background()

	results, err := rs.SearchCtx(ctx, "found it", types.SearchOptions{Project: "proj", Limit: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	parsed, _ := url.Parse(capturedPath)
	if parsed.Path != "/api/v1/search" {
		t.Errorf("expected /api/v1/search path, got %q", capturedPath)
	}
}

func TestRemoteStore_FormatContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `"## Context\nsome content"`)
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")
	ctx := context.Background()

	result, err := rs.FormatContextCtx(ctx, "proj", "project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty context string")
	}
}

func TestRemoteStore_Stats(t *testing.T) {
	want := types.Stats{TotalSessions: 3, TotalObservations: 10, Projects: []string{"proj"}}
	rs := newTestRemoteStore(t, handlerReturning(http.StatusOK, want))
	ctx := context.Background()

	got, err := rs.StatsCtx(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TotalSessions != 3 || got.TotalObservations != 10 {
		t.Errorf("unexpected stats: %+v", got)
	}
}

func TestRemoteStore_RecentSessions(t *testing.T) {
	want := map[string]any{
		"sessions": []map[string]any{
			{"id": "sess-1", "project": "proj"},
		},
	}
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.String()
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")
	ctx := context.Background()

	sessions, err := rs.RecentSessionsCtx(ctx, "proj", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session, got %d", len(sessions))
	}
	parsedURL, _ := url.Parse(capturedPath)
	if parsedURL.Path != "/api/v1/sessions" {
		t.Errorf("unexpected path: %q", capturedPath)
	}
}

func TestRemoteStore_AllSessions(t *testing.T) {
	want := map[string]any{
		"sessions": []map[string]any{
			{"id": "sess-1", "project": "proj"},
			{"id": "sess-2", "project": "proj"},
		},
	}
	rs := newTestRemoteStore(t, handlerReturning(http.StatusOK, want))
	ctx := context.Background()

	sessions, err := rs.AllSessionsCtx(ctx, "proj", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestRemoteStore_SessionObservations(t *testing.T) {
	want := map[string]any{
		"observations": []map[string]any{
			{"id": float64(1), "title": "obs1"},
		},
	}
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.String()
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")
	ctx := context.Background()

	obs, err := rs.SessionObservationsCtx(ctx, "sess-abc", 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 1 {
		t.Errorf("expected 1 observation, got %d", len(obs))
	}
	parsedURL2, _ := url.Parse(capturedPath)
	if parsedURL2.Path != "/api/v1/sessions/sess-abc/observations" {
		t.Errorf("unexpected path: %q", capturedPath)
	}
}

func TestRemoteStore_RecentObservations(t *testing.T) {
	want := map[string]any{
		"observations": []map[string]any{
			{"id": float64(5), "title": "recent"},
		},
	}
	rs := newTestRemoteStore(t, handlerReturning(http.StatusOK, want))
	ctx := context.Background()

	obs, err := rs.RecentObservationsCtx(ctx, "proj", "", 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 1 {
		t.Errorf("expected 1 observation, got %d", len(obs))
	}
}

func TestRemoteStore_AllObservations(t *testing.T) {
	want := map[string]any{
		"observations": []map[string]any{
			{"id": float64(1), "title": "all-obs"},
			{"id": float64(2), "title": "all-obs2"},
		},
	}
	rs := newTestRemoteStore(t, handlerReturning(http.StatusOK, want))
	ctx := context.Background()

	obs, err := rs.AllObservationsCtx(ctx, "proj", "", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(obs) != 2 {
		t.Errorf("expected 2 observations, got %d", len(obs))
	}
}

func TestRemoteStore_Timeline(t *testing.T) {
	want := map[string]any{
		"focus":  map[string]any{"id": float64(10), "title": "focus"},
		"before": []any{},
		"after":  []any{},
	}
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.String()
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")
	ctx := context.Background()

	result, err := rs.TimelineCtx(ctx, 10, 5, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil timeline result")
	}
	parsedURL3, _ := url.Parse(capturedPath)
	if parsedURL3.Path != "/api/v1/observations/10/timeline" {
		t.Errorf("unexpected path: %q", capturedPath)
	}
}

func TestRemoteStore_RecentPrompts(t *testing.T) {
	want := map[string]any{
		"prompts": []map[string]any{
			{"id": float64(1), "content": "what did we do?"},
		},
	}
	rs := newTestRemoteStore(t, handlerReturning(http.StatusOK, want))
	ctx := context.Background()

	prompts, err := rs.RecentPromptsCtx(ctx, "proj", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prompts) != 1 {
		t.Errorf("expected 1 prompt, got %d", len(prompts))
	}
}

func TestRemoteStore_SearchPrompts(t *testing.T) {
	want := map[string]any{
		"prompts": []map[string]any{
			{"id": float64(2), "content": "memory query"},
		},
	}
	rs := newTestRemoteStore(t, handlerReturning(http.StatusOK, want))
	ctx := context.Background()

	prompts, err := rs.SearchPromptsCtx(ctx, "memory", "proj", 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prompts) != 1 {
		t.Errorf("expected 1 prompt, got %d", len(prompts))
	}
}

// ─── T30: Write methods via sync/push ────────────────────────────────────────

// REQ-REMOTE-004: AddObservation → push returns entity_ids[syncID] = 99 → int64 99
func TestRemoteStore_AddObservation_ReturnsEntityID(t *testing.T) {
	// Handler extracts the sync_id from the push request and mirrors it back
	// so RemoteStore can find it in entity_ids and return 99.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		mutations, _ := req["mutations"].([]any)
		var syncID string
		if len(mutations) > 0 {
			if m, ok := mutations[0].(map[string]any); ok {
				syncID, _ = m["entity_key"].(string)
			}
		}
		entityIDs := map[string]any{}
		if syncID != "" {
			entityIDs[syncID] = float64(99)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"acked_seq":  1,
			"server_seq": 1,
			"entity_ids": entityIDs,
		})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	id, err := rs.AddObservation(types.AddObservationParams{
		SessionID: "sess-1",
		Type:      "decision",
		Title:     "test",
		Content:   "content",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 99 {
		t.Errorf("expected entity ID 99, got %d", id)
	}
}

func TestRemoteStore_CreateSession_CallsPush(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		json.NewEncoder(w).Encode(map[string]any{"acked_seq": 1, "server_seq": 1, "entity_ids": map[string]any{}})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	err := rs.CreateSession("sess-x", "proj", "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedBody == nil {
		t.Fatal("expected push body, got nil")
	}
	mutations, _ := capturedBody["mutations"].([]any)
	if len(mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(mutations))
	}
	m, _ := mutations[0].(map[string]any)
	if m["entity"] != "session" {
		t.Errorf("expected entity=session, got %v", m["entity"])
	}
}

func TestRemoteStore_EndSession_CallsPush(t *testing.T) {
	var capturedMutations []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		capturedMutations, _ = req["mutations"].([]any)
		json.NewEncoder(w).Encode(map[string]any{"acked_seq": 1, "server_seq": 1, "entity_ids": map[string]any{}})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	err := rs.EndSession("sess-x", "summary text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedMutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(capturedMutations))
	}
	m, _ := capturedMutations[0].(map[string]any)
	if m["entity"] != "session" {
		t.Errorf("expected entity=session, got %v", m["entity"])
	}
}

func TestRemoteStore_DeleteSession_CallsPush(t *testing.T) {
	var capturedMutations []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		capturedMutations, _ = req["mutations"].([]any)
		json.NewEncoder(w).Encode(map[string]any{"acked_seq": 1, "server_seq": 1, "entity_ids": map[string]any{}})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	err := rs.DeleteSession("sess-x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, _ := capturedMutations[0].(map[string]any)
	if m["op"] != "delete" {
		t.Errorf("expected op=delete, got %v", m["op"])
	}
}

func TestRemoteStore_UpdateObservation_CallsPush(t *testing.T) {
	var capturedMutations []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		capturedMutations, _ = req["mutations"].([]any)
		json.NewEncoder(w).Encode(map[string]any{"acked_seq": 1, "server_seq": 1, "entity_ids": map[string]any{}})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	title := "updated"
	obs, err := rs.UpdateObservation(42, types.UpdateObservationParams{Title: &title})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs == nil {
		t.Fatal("expected non-nil observation")
	}
	m, _ := capturedMutations[0].(map[string]any)
	if m["entity"] != "observation" {
		t.Errorf("expected entity=observation, got %v", m["entity"])
	}
}

func TestRemoteStore_DeleteObservation_CallsPush(t *testing.T) {
	var capturedMutations []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		capturedMutations, _ = req["mutations"].([]any)
		json.NewEncoder(w).Encode(map[string]any{"acked_seq": 1, "server_seq": 1, "entity_ids": map[string]any{}})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	err := rs.DeleteObservation(42, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, _ := capturedMutations[0].(map[string]any)
	if m["op"] != "delete" {
		t.Errorf("expected op=delete, got %v", m["op"])
	}
}

func TestRemoteStore_AddPrompt_ReturnsEntityID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		mutations, _ := req["mutations"].([]any)
		var syncID string
		if len(mutations) > 0 {
			if m, ok := mutations[0].(map[string]any); ok {
				syncID, _ = m["entity_key"].(string)
			}
		}
		entityIDs := map[string]any{}
		if syncID != "" {
			entityIDs[syncID] = float64(77)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"acked_seq": 1, "server_seq": 1, "entity_ids": entityIDs,
		})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	id, err := rs.AddPrompt(types.AddPromptParams{
		SessionID: "sess-1",
		Content:   "what did we do?",
		Project:   "proj",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 77 {
		t.Errorf("expected prompt ID 77, got %d", id)
	}
}

func TestRemoteStore_DeletePrompt_CallsPush(t *testing.T) {
	var capturedMutations []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		capturedMutations, _ = req["mutations"].([]any)
		json.NewEncoder(w).Encode(map[string]any{"acked_seq": 1, "server_seq": 1, "entity_ids": map[string]any{}})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	err := rs.DeletePrompt(42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, _ := capturedMutations[0].(map[string]any)
	if m["entity"] != "prompt" {
		t.Errorf("expected entity=prompt, got %v", m["entity"])
	}
	if m["op"] != "delete" {
		t.Errorf("expected op=delete, got %v", m["op"])
	}
}

// ─── T31: Composite methods ───────────────────────────────────────────────────

func TestRemoteStore_PassiveCapture(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{
			"extracted": 2, "saved": 2, "duplicates": 0,
		})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	result, err := rs.PassiveCapture(types.PassiveCaptureParams{
		SessionID: "sess-1",
		Content:   "some content",
		Project:   "proj",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Saved != 2 {
		t.Errorf("expected saved=2, got %d", result.Saved)
	}
	if capturedPath != "/api/v1/passive-capture" {
		t.Errorf("unexpected path: %q", capturedPath)
	}
}

func TestRemoteStore_MigrateProject(t *testing.T) {
	var capturedPath string
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&capturedBody)
		json.NewEncoder(w).Encode(map[string]any{
			"migrated":             true,
			"observations_updated": float64(10),
			"sessions_updated":     float64(2),
			"prompts_updated":      float64(5),
		})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	result, err := rs.MigrateProject("old-proj", "new-proj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.ObservationsUpdated != 10 {
		t.Errorf("expected 10 observations updated, got %d", result.ObservationsUpdated)
	}
	if capturedPath != "/api/v1/projects/migrate" {
		t.Errorf("unexpected path: %q", capturedPath)
	}
	if capturedBody["old_name"] != "old-proj" || capturedBody["new_name"] != "new-proj" {
		t.Errorf("unexpected body: %v", capturedBody)
	}
}

// Triangulation: Search with type and scope options (exercises those code branches)
func TestRemoteStore_Search_WithTypeAndScope(t *testing.T) {
	var capturedQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query()
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")
	ctx := context.Background()

	_, err := rs.SearchCtx(ctx, "query", types.SearchOptions{Type: "decision", Scope: "project", Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedQuery.Get("type") != "decision" {
		t.Errorf("expected type=decision in query, got %q", capturedQuery.Get("type"))
	}
	if capturedQuery.Get("scope") != "project" {
		t.Errorf("expected scope=project in query, got %q", capturedQuery.Get("scope"))
	}
}

// Triangulation: MigrateProject error path
func TestRemoteStore_MigrateProject_Error(t *testing.T) {
	rs := newTestRemoteStore(t, handlerReturning(http.StatusUnauthorized, map[string]string{"error": "forbidden"}))
	_, err := rs.MigrateProject("old", "new")
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
}

// Triangulation: UpdateObservation with no optional fields set
func TestRemoteStore_UpdateObservation_NoFields(t *testing.T) {
	var capturedMutations []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		capturedMutations, _ = req["mutations"].([]any)
		json.NewEncoder(w).Encode(map[string]any{"acked_seq": 1, "server_seq": 1, "entity_ids": map[string]any{}})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")

	obs, err := rs.UpdateObservation(7, types.UpdateObservationParams{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs == nil || obs.ID != 7 {
		t.Errorf("expected observation with ID=7, got %+v", obs)
	}
	if len(capturedMutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(capturedMutations))
	}
}

// Triangulation: StoreInterface non-Ctx wrappers are reachable
func TestRemoteStore_StoreInterface_DirectCalls(t *testing.T) {
	want := map[string]any{
		"sessions": []any{},
	}
	rs := newTestRemoteStore(t, handlerReturning(http.StatusOK, want))

	// These call the non-Ctx wrappers, which delegate to Ctx variants
	sessions, err := rs.RecentSessions("proj", 5)
	if err != nil {
		t.Fatalf("RecentSessions: %v", err)
	}
	if sessions == nil {
		sessions = []types.SessionSummary{}
	}

	sessions2, err := rs.AllSessions("proj", 5)
	if err != nil {
		t.Fatalf("AllSessions: %v", err)
	}
	if sessions2 == nil {
		sessions2 = []types.SessionSummary{}
	}
}

// Triangulation: PassiveCapture with error response
func TestRemoteStore_PassiveCapture_Error(t *testing.T) {
	rs := newTestRemoteStore(t, handlerReturning(http.StatusInternalServerError, map[string]string{"error": "server down"}))
	_, err := rs.PassiveCapture(types.PassiveCaptureParams{SessionID: "s", Content: "c"})
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	if !errors.Is(err, ErrServerError) {
		t.Errorf("expected ErrServerError, got: %v", err)
	}
}

// ─── sync.go helper coverage (entityKeyFromData, opFromData) ─────────────────

func TestEntityKeyFromData_SyncID(t *testing.T) {
	data := map[string]any{"sync_id": "obs-abc123"}
	got := entityKeyFromData(data)
	if got != "obs-abc123" {
		t.Errorf("expected obs-abc123, got %q", got)
	}
}

func TestEntityKeyFromData_FallbackID(t *testing.T) {
	// No sync_id — falls back to "id"
	data := map[string]any{"id": "sess-xyz"}
	got := entityKeyFromData(data)
	if got != "sess-xyz" {
		t.Errorf("expected sess-xyz, got %q", got)
	}
}

func TestOpFromData_Delete(t *testing.T) {
	data := map[string]any{"deleted_at": "2026-01-01"}
	got := opFromData(data)
	if got != "delete" {
		t.Errorf("expected delete, got %q", got)
	}
}

func TestOpFromData_Upsert(t *testing.T) {
	data := map[string]any{"title": "no deleted_at key"}
	got := opFromData(data)
	if got != "upsert" {
		t.Errorf("expected upsert, got %q", got)
	}
}

// ─── Triangulation: GetObservation — two calls produce exactly 2 requests (not 1, not 0)
func TestRemoteStore_GetObservation_TwoCalls_TwoRequests(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		json.NewEncoder(w).Encode(types.Observation{ID: 1})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok-test", "test")
	rs := NewRemoteStore(client, "proj")
	ctx := context.Background()

	_, _ = rs.GetObservationCtx(ctx, 1)
	_, _ = rs.GetObservationCtx(ctx, 1)

	if count != 2 {
		t.Errorf("no-cache: expected exactly 2 HTTP requests, got %d", count)
	}
}
