package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/autosync"
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

// mutationPullStatusServer serves a fixed HTTP status for the mutation pull
// endpoint, so tests can exercise command-level error mapping (401/403/404).
func mutationPullStatusServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync/mutations/pull" {
			w.WriteHeader(status)
			if body != "" {
				_, _ = w.Write([]byte(body))
			}
			return
		}
		http.NotFound(w, r)
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

// setupPullMutationsConfig builds a test config with the fake server URL and
// clears ambient cloud environment variables so each test controls auth
// explicitly.
func setupPullMutationsConfig(t *testing.T, srvURL, token string) store.Config {
	t.Helper()
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")
	if err := saveCloudConfig(cfg, &cloudConfig{
		ServerURL: srvURL,
		Token:     token,
	}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	return cfg
}

// runCloudPullMutations executes the pull command against the given config and
// returns captured stdout, stderr, and the recovered exit value.
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
// cloud config has no token and the server runs in insecure local-dev mode:
// ENGRAM_CLOUD_INSECURE_NO_AUTH=1 servers accept unauthenticated mutation pull
// (issue #327, code review follow-up). The env var is set explicitly so the
// test exercises the documented no-auth configuration.
func TestCloudPullMutations_NoAuthMode(t *testing.T) {
	srv := mutationPullTestServer(t, []map[string]any{
		mutationTestSession(7, "sess-7", "proj-a"),
	})
	cfg := setupPullMutationsConfig(t, srv.URL, "")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "1")

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
// follow-up). The injected seam closes the real store first so the SQLite file
// is never leaked on platforms that cannot remove an open database (Windows).
func TestCloudPullMutations_CloseFailureSurfaces(t *testing.T) {
	srv := mutationPullTestServer(t, []map[string]any{
		mutationTestSession(1, "sess-1", "proj-a"),
	})
	cfg := setupPullMutationsConfig(t, srv.URL, "test-token")

	oldClose := closeCloudPullMutationsStore
	closeCloudPullMutationsStore = func(s cloudPullMutationsStore) error {
		_ = s.Close()
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

// TestCloudPullMutations_PolicyForbidden asserts a 403 surfaced by the server
// becomes a fatal error instead of a healthy pull.
func TestCloudPullMutations_PolicyForbidden(t *testing.T) {
	srv := mutationPullStatusServer(t, http.StatusForbidden,
		`{"error_class":"policy","error_code":"policy_forbidden","error":"project not allowed"}`)
	cfg := setupPullMutationsConfig(t, srv.URL, "test-token")

	_, stderr, recovered := runCloudPullMutations(t, cfg)
	if _, ok := recovered.(exitCode); !ok {
		t.Fatal("expected fatal on 403, got success")
	}
	if !strings.Contains(stderr, "403") {
		t.Fatalf("expected policy-forbidden status in stderr, got:\n%s", stderr)
	}
}

// TestCloudPullMutations_ServerUnsupported asserts a 404 from the mutation
// endpoint fails the command with the server_unsupported reason (the deployed
// server predates the mutation API).
func TestCloudPullMutations_ServerUnsupported(t *testing.T) {
	srv := mutationPullStatusServer(t, http.StatusNotFound, `{"error":"not found"}`)
	cfg := setupPullMutationsConfig(t, srv.URL, "test-token")

	_, stderr, recovered := runCloudPullMutations(t, cfg)
	if _, ok := recovered.(exitCode); !ok {
		t.Fatal("expected fatal on 404, got success")
	}
	if !strings.Contains(stderr, "404") && !strings.Contains(stderr, "server_unsupported") {
		t.Fatalf("expected server_unsupported guidance in stderr, got:\n%s", stderr)
	}
}

// deferredListFailStore wraps a real store whose deferred-project enumeration
// fails, proving the pull command exits non-zero without marking the target
// healthy when deferred relation work could not be inspected.
type deferredListFailStore struct {
	*store.Store
}

// ListDeferredProjectsForTarget injects a deterministic enumeration failure.
func (s *deferredListFailStore) ListDeferredProjectsForTarget(string) ([]string, error) {
	return nil, fmt.Errorf("injected deferred list failure")
}

// TestCloudPullMutations_DeferredFailureNotMarkedHealthy asserts the fail-closed
// health contract: when deferred replay inspection fails, the command exits
// non-zero and the target stays degraded instead of being marked healthy.
func TestCloudPullMutations_DeferredFailureNotMarkedHealthy(t *testing.T) {
	srv := mutationPullTestServer(t, nil)
	cfg := setupPullMutationsConfig(t, srv.URL, "test-token")

	// Mark the target degraded before running; a regressed healthy mark would
	// reset it, so the state read after the command is deterministic.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.MarkSyncFailure(store.DefaultSyncTargetKey, "previous failure", time.Now().UTC().Add(30*time.Second)); err != nil {
		_ = s.Close()
		t.Fatalf("mark degraded: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	oldNew := newCloudPullMutationsStore
	newCloudPullMutationsStore = func(c store.Config) (cloudPullMutationsStore, error) {
		real, err := storeNew(c)
		if err != nil {
			return nil, err
		}
		return &deferredListFailStore{Store: real}, nil
	}
	t.Cleanup(func() { newCloudPullMutationsStore = oldNew })

	_, stderr, recovered := runCloudPullMutations(t, cfg)
	if _, ok := recovered.(exitCode); !ok {
		t.Fatal("expected fatal when deferred replay inspection fails, got success")
	}
	if !strings.Contains(stderr, "injected deferred list failure") {
		t.Fatalf("expected deferred failure in stderr, got:\n%s", stderr)
	}

	s2, err := store.New(cfg)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = s2.Close() }()
	state, err := s2.GetSyncState(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if state.Lifecycle != store.SyncLifecycleDegraded {
		t.Fatalf("target must not be marked healthy after deferred replay failure, lifecycle=%q", state.Lifecycle)
	}
	if state.ConsecutiveFailures < 1 {
		t.Fatalf("expected the degraded marker to persist, consecutive_failures=%d", state.ConsecutiveFailures)
	}
}

// TestPrintCloudPullMutationsResult_ReportsDeferredReplaysOnEmptyPull asserts
// the deferred-replay rows are still reported when no new mutations were
// applied: PullMutations replays pending deferred work for touched projects, so
// an empty remote response can still change local state (CodeRabbit
// outside-diff follow-up).
func TestPrintCloudPullMutationsResult_ReportsDeferredReplaysOnEmptyPull(t *testing.T) {
	report := autosync.PullReport{
		Applied: 0,
		Replays: []autosync.ProjectReplay{
			{Project: "proj-a", Retried: 2, Succeeded: 1, Failed: 0, Dead: 1},
		},
	}

	stdout, _, recovered := captureOutputAndRecover(t, func() {
		printCloudPullMutationsResult(report)
	})
	if recovered != nil {
		t.Fatalf("printCloudPullMutationsResult panicked: %v", recovered)
	}
	if !strings.Contains(stdout, "No new cloud mutations to pull.") {
		t.Fatalf("expected no-new-mutations message, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Deferred relations replayed for proj-a: 2 retried, 1 succeeded, 0 failed, 1 dead") {
		t.Fatalf("deferred replays must be reported even with zero applied mutations, got:\n%s", stdout)
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

// mustSessionPayload builds the JSON payload used by session upsert mutations.
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
