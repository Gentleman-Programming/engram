package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// mutationPullTestServer is a minimal fake cloud server serving pull until a
// configured cursor, mirroring the real /sync/mutations/pull contract:
// GET /sync/mutations/pull?since_seq=N&limit=M over the whole mutation journal.
func mutationPullTestServer(t *testing.T, mutations []map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/mutations/pull":
			sinceSeq := int64(0)
			if v := r.URL.Query().Get("since_seq"); v != "" {
				sinceSeq, _ = strconv.ParseInt(v, 10, 64)
			}
			var result []map[string]any
			maxSeq := int64(0)
			for _, m := range mutations {
				seq, _ := m["seq"].(int64)
				if seq <= sinceSeq {
					continue
				}
				if seq > maxSeq {
					maxSeq = seq
				}
				result = append(result, m)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mutations":  result,
				"has_more":   false,
				"latest_seq": maxSeq,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldDefaultTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldDefaultTransport })
	return srv
}

// mutationTestSession builds a valid session upsert mutation for the fake
// cloud server.
func mutationTestSession(seq int64, id, project string) map[string]any {
	payload, _ := json.Marshal(map[string]any{
		"id":        id,
		"project":   project,
		"directory": "/tmp/" + project,
	})
	return map[string]any{
		"seq":         seq,
		"project":     project,
		"entity":      store.SyncEntitySession,
		"entity_key":  id,
		"op":          store.SyncOpUpsert,
		"payload":     json.RawMessage(payload),
		"occurred_at": "2026-05-01T00:00:00Z",
	}
}

func setupPullMutationsConfig(t *testing.T, srvURL, token string) store.Config {
	t.Helper()
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: srvURL,
		Token:     token,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	return cfg
}

func runCloudPullMutations(t *testing.T, cfg store.Config) (stdout string, stderr string, recovered any) {
	t.Helper()
	stubExitWithPanic(t)
	withArgs(t, "engram", "cloud", "pull-mutations")
	return captureOutputAndRecover(t, func() { cmdCloud(cfg) })
}

// TestCloudPullMutations_AppliesRemoteMutations exercises the happy path: the
// command pulls mutations since the local cursor and applies them, then
// advances last_pulled_seq on the global "cloud" target.
func TestCloudPullMutations_AppliesRemoteMutations(t *testing.T) {
	srv := mutationPullTestServer(t, []map[string]any{
		mutationTestSession(1, "sess-1", "proj-a"),
		mutationTestSession(2, "sess-2", "proj-b"),
	})
	cfg := setupPullMutationsConfig(t, srv.URL, "test-token")

	stdout, _, recovered := runCloudPullMutations(t, cfg)
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("cloud pull-mutations fataled on happy path; expected success")
	}
	if !strings.Contains(stdout, "Pulled 2") {
		t.Fatalf("expected success output to report 2 pulled mutations, got stdout:\n%s", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = s.Close() }()
	state, err := s.GetSyncState(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if state.LastPulledSeq != 2 {
		t.Fatalf("last_pulled_seq: want 2, got %d", state.LastPulledSeq)
	}
}

// TestCloudPullMutations_NoNewMutations asserts the no-op output when the
// server has nothing after the local cursor.
func TestCloudPullMutations_NoNewMutations(t *testing.T) {
	srv := mutationPullTestServer(t, nil)
	cfg := setupPullMutationsConfig(t, srv.URL, "test-token")

	stdout, _, recovered := runCloudPullMutations(t, cfg)
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("cloud pull-mutations fataled with no new mutations; expected success")
	}
	if !strings.Contains(stdout, "No new cloud mutations") {
		t.Fatalf("expected no-op output, got stdout:\n%s", stdout)
	}
}

// TestCloudPullMutations_ResumesFromLocalCursor asserts the command pulls only
// mutations after the last-pulled sequence persisted locally (a pre-imported
// cursor must not be replayed).
func TestCloudPullMutations_ResumesFromLocalCursor(t *testing.T) {
	srv := mutationPullTestServer(t, []map[string]any{
		mutationTestSession(1, "sess-1", "proj-a"),
		mutationTestSession(2, "sess-2", "proj-a"),
	})
	cfg := setupPullMutationsConfig(t, srv.URL, "test-token")

	// Pre-advance the local cursor to seq 1, as an earlier pull would have.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	state, err := s.GetSyncState(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	_ = state
	if err := s.ApplyPulledMutation(store.DefaultSyncTargetKey, store.SyncMutation{
		Seq:       1,
		TargetKey: store.DefaultSyncTargetKey,
		Project:   "proj-a",
		Entity:    store.SyncEntitySession,
		EntityKey: "sess-1",
		Op:        store.SyncOpUpsert,
		Payload:   mustSessionPayload("sess-1", "proj-a"),
		Source:    store.SyncSourceRemote,
	}); err != nil {
		_ = s.Close()
		t.Fatalf("pre-apply seq 1: %v", err)
	}
	_ = s.Close()

	stdout, _, recovered := runCloudPullMutations(t, cfg)
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("cloud pull-mutations fataled; expected success")
	}
	if !strings.Contains(stdout, "Pulled 1") {
		t.Fatalf("expected exactly 1 new mutation pulled, got stdout:\n%s", stdout)
	}
}

// TestCloudPullMutations_AuthFailure asserts a 401 surfaced by the server
// becomes a fatal error with actionable guidance and does not panic.
func TestCloudPullMutations_AuthFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync/mutations/pull" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error_class":"auth","error_code":"auth_required","error":"unauthorized"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	oldDefaultTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldDefaultTransport })

	cfg := setupPullMutationsConfig(t, srv.URL, "bad-token")
	_, stderr, recovered := runCloudPullMutations(t, cfg)
	if _, ok := recovered.(exitCode); !ok {
		t.Fatal("expected fatal on 401, got success")
	}
	if !strings.Contains(stderr, "cloud sync") && !strings.Contains(stderr, "401") {
		t.Fatalf("expected actionable auth guidance in stderr, got:\n%s", stderr)
	}
}

// TestCloudPullMutations_NoAuthMode asserts that the command works when the
// cloud config has no token: ENGRAM_CLOUD_INSECURE_NO_AUTH servers accept
// unauthenticated mutation pull (issue #327, code review follow-up).
func TestCloudPullMutations_NoAuthMode(t *testing.T) {
	srv := mutationPullTestServer(t, []map[string]any{
		mutationTestSession(7, "sess-7", "proj-a"),
	})
	cfg := setupPullMutationsConfig(t, srv.URL, "")

	stdout, _, recovered := runCloudPullMutations(t, cfg)
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("cloud pull-mutations fataled without a token; expected success in no-auth mode")
	}
	if !strings.Contains(stdout, "Pulled 1") {
		t.Fatalf("expected 1 pulled mutation in no-auth mode, got stdout:\n%s", stdout)
	}
}

// TestCloudPullMutations_CloseFailureSurfaces asserts that a store close
// failure is reported to the operator instead of silently discarded: the
// command must fatal before reporting success (CodeRabbit data-integrity
// follow-up).
func TestCloudPullMutations_CloseFailureSurfaces(t *testing.T) {
	srv := mutationPullTestServer(t, []map[string]any{
		mutationTestSession(1, "sess-1", "proj-a"),
	})
	cfg := setupPullMutationsConfig(t, srv.URL, "test-token")

	oldClose := closeCloudPullMutationsStore
	closeCloudPullMutationsStore = func(*store.Store) error {
		return fmt.Errorf("injected close failure")
	}
	t.Cleanup(func() { closeCloudPullMutationsStore = oldClose })

	stdout, stderr, recovered := runCloudPullMutations(t, cfg)
	if _, ok := recovered.(exitCode); !ok {
		t.Fatal("expected fatal on store close failure, got success")
	}
	if strings.Contains(stdout, "Pulled") {
		t.Fatalf("success must not be reported when close fails, got stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "injected close failure") {
		t.Fatalf("expected close error in stderr, got:\n%s", stderr)
	}
}

// TestCloudPullMutations_MissingServerConfig asserts a clear failure when no
// cloud server URL is configured.
func TestCloudPullMutations_MissingServerConfig(t *testing.T) {
	cfg := setupPullMutationsConfig(t, "", "")
	_, stderr, recovered := runCloudPullMutations(t, cfg)
	if _, ok := recovered.(exitCode); !ok {
		t.Fatal("expected fatal with missing server config")
	}
	if !strings.Contains(stderr, "cloud config") && !strings.Contains(stderr, "server") {
		t.Fatalf("expected guidance about missing server config in stderr, got:\n%s", stderr)
	}
}

func mustSessionPayload(id, project string) string {
	payload, err := json.Marshal(map[string]any{
		"id":        id,
		"project":   project,
		"directory": "/tmp/" + project,
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}
