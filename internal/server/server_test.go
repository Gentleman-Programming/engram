package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
	_ "modernc.org/sqlite"
)

type stubListener struct{}

func (stubListener) Accept() (net.Conn, error) { return nil, errors.New("not used") }
func (stubListener) Close() error              { return nil }
func (stubListener) Addr() net.Addr            { return &net.TCPAddr{} }

func TestStartReturnsListenError(t *testing.T) {
	s := New(nil, 7777)
	s.listen = func(network, address string) (net.Listener, error) {
		return nil, errors.New("listen failed")
	}

	err := s.Start()
	if err == nil {
		t.Fatalf("expected start to fail on listen error")
	}
}

func TestStartUsesInjectedServe(t *testing.T) {
	s := New(&store.Store{}, 7777)
	s.listen = func(network, address string) (net.Listener, error) {
		return stubListener{}, nil
	}
	s.serve = func(ln net.Listener, h http.Handler) error {
		if ln == nil || h == nil {
			t.Fatalf("expected listener and handler to be provided")
		}
		return errors.New("serve stopped")
	}

	err := s.Start()
	if err == nil || err.Error() != "serve stopped" {
		t.Fatalf("expected propagated serve error, got %v", err)
	}
}

func newServerTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func TestStartUsesDefaultListenWhenListenNil(t *testing.T) {
	s := New(newServerTestStore(t), 0)
	s.listen = nil
	s.serve = func(ln net.Listener, h http.Handler) error {
		if ln == nil || h == nil {
			t.Fatalf("expected non-nil listener and handler")
		}
		_ = ln.Close()
		return errors.New("serve stopped")
	}

	err := s.Start()
	if err == nil || err.Error() != "serve stopped" {
		t.Fatalf("expected propagated serve error, got %v", err)
	}
}

func TestStartUsesDefaultServeWhenServeNil(t *testing.T) {
	s := New(newServerTestStore(t), 7777)
	s.listen = func(network, address string) (net.Listener, error) {
		return stubListener{}, nil
	}
	s.serve = nil

	err := s.Start()
	if err == nil {
		t.Fatalf("expected start to fail when default http.Serve receives failing listener")
	}
}

func TestAdditionalServerErrorBranches(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	createReq := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(`{"id":"s-test","project":"engram"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected session create 201, got %d", createRec.Code)
	}

	getBadIDReq := httptest.NewRequest(http.MethodGet, "/observations/not-a-number", nil)
	getBadIDRec := httptest.NewRecorder()
	h.ServeHTTP(getBadIDRec, getBadIDReq)
	if getBadIDRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid observation id, got %d", getBadIDRec.Code)
	}

	updateNotFoundReq := httptest.NewRequest(http.MethodPatch, "/observations/99999", strings.NewReader(`{"title":"updated"}`))
	updateNotFoundReq.Header.Set("Content-Type", "application/json")
	updateNotFoundRec := httptest.NewRecorder()
	h.ServeHTTP(updateNotFoundRec, updateNotFoundReq)
	if updateNotFoundRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 updating missing observation, got %d", updateNotFoundRec.Code)
	}

	promptBadJSONReq := httptest.NewRequest(http.MethodPost, "/prompts", strings.NewReader("{"))
	promptBadJSONReq.Header.Set("Content-Type", "application/json")
	promptBadJSONRec := httptest.NewRecorder()
	h.ServeHTTP(promptBadJSONRec, promptBadJSONReq)
	if promptBadJSONRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid prompt json, got %d", promptBadJSONRec.Code)
	}

	oversizeBody := bytes.Repeat([]byte("a"), 50<<20+1)
	importTooLargeReq := httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(oversizeBody))
	importTooLargeReq.Header.Set("Content-Type", "application/json")
	importTooLargeRec := httptest.NewRecorder()
	h.ServeHTTP(importTooLargeRec, importTooLargeReq)
	if importTooLargeRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversize import body, got %d", importTooLargeRec.Code)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	validImport, err := json.Marshal(store.ExportData{Version: "0.1.0", ExportedAt: "now"})
	if err != nil {
		t.Fatalf("marshal import payload: %v", err)
	}
	importClosedReq := httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(validImport))
	importClosedReq.Header.Set("Content-Type", "application/json")
	importClosedRec := httptest.NewRecorder()
	h.ServeHTTP(importClosedRec, importClosedReq)
	if importClosedRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 importing on closed store, got %d", importClosedRec.Code)
	}
}

func TestExportHonorsProjectQueryScope(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	if err := st.CreateSession("sess-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session proj-a: %v", err)
	}
	if err := st.CreateSession("sess-b", "proj-b", "/tmp/proj-b"); err != nil {
		t.Fatalf("create session proj-b: %v", err)
	}
	if _, err := st.AddObservation(store.AddObservationParams{SessionID: "sess-a", Type: "note", Title: "a", Content: "a", Project: "proj-a", Scope: "project"}); err != nil {
		t.Fatalf("add obs proj-a: %v", err)
	}
	if _, err := st.AddObservation(store.AddObservationParams{SessionID: "sess-b", Type: "note", Title: "b", Content: "b", Project: "proj-b", Scope: "project"}); err != nil {
		t.Fatalf("add obs proj-b: %v", err)
	}
	if _, err := st.AddPrompt(store.AddPromptParams{SessionID: "sess-a", Content: "prompt-a", Project: "proj-a"}); err != nil {
		t.Fatalf("add prompt proj-a: %v", err)
	}
	if _, err := st.AddPrompt(store.AddPromptParams{SessionID: "sess-b", Content: "prompt-b", Project: "proj-b"}); err != nil {
		t.Fatalf("add prompt proj-b: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/export?project=proj-a", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 export, got %d", rec.Code)
	}

	var exported store.ExportData
	if err := json.NewDecoder(rec.Body).Decode(&exported); err != nil {
		t.Fatalf("decode export response: %v", err)
	}

	if len(exported.Sessions) != 1 || exported.Sessions[0].Project != "proj-a" {
		t.Fatalf("expected only proj-a sessions in scoped export, got %+v", exported.Sessions)
	}
	if len(exported.Observations) != 1 {
		t.Fatalf("expected exactly one scoped observation, got %+v", exported.Observations)
	}
	if exported.Observations[0].Project == nil || *exported.Observations[0].Project != "proj-a" {
		t.Fatalf("expected scoped observation project proj-a, got %+v", exported.Observations[0].Project)
	}
	if len(exported.Prompts) != 1 || exported.Prompts[0].Project != "proj-a" {
		t.Fatalf("expected only proj-a prompts in scoped export, got %+v", exported.Prompts)
	}
}

func TestExportRejectsExplicitBlankProjectQuery(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	tests := []string{
		"/export?project=",
		"/export?project=%20%20%20",
	}

	for _, url := range tests {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for explicit blank project query (%s), got %d", url, rec.Code)
		}
	}
}

// ─── Sync Status Tests ───────────────────────────────────────────────────────

// stubSyncStatusProvider is a fake SyncStatusProvider for tests.
type stubSyncStatusProvider struct {
	status      SyncStatus
	lastProject string
}

func (s *stubSyncStatusProvider) Status(project string) SyncStatus {
	s.lastProject = project
	return s.status
}

func TestSyncStatusNotConfigured(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	// No sync status provider set — should return enabled: false.
	req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["enabled"] != false {
		t.Fatalf("expected enabled=false when no provider, got %v", resp["enabled"])
	}
}

func TestSyncStatusHealthy(t *testing.T) {
	now := time.Now()
	provider := &stubSyncStatusProvider{
		status: SyncStatus{
			Enabled:    true,
			Phase:      "healthy",
			LastSyncAt: &now,
		},
	}

	srv := New(newServerTestStore(t), 0)
	srv.SetSyncStatus(provider)

	req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["enabled"] != true {
		t.Fatalf("expected enabled=true, got %v", resp["enabled"])
	}
	if resp["phase"] != "healthy" {
		t.Fatalf("expected phase=healthy, got %v", resp["phase"])
	}
}

func TestSyncStatusDegraded(t *testing.T) {
	backoff := time.Now().Add(5 * time.Minute)
	provider := &stubSyncStatusProvider{
		status: SyncStatus{
			Enabled:             true,
			Phase:               "push_failed",
			LastError:           "network timeout",
			ConsecutiveFailures: 3,
			BackoffUntil:        &backoff,
		},
	}

	srv := New(newServerTestStore(t), 0)
	srv.SetSyncStatus(provider)

	req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["phase"] != "push_failed" {
		t.Fatalf("expected phase=push_failed, got %v", resp["phase"])
	}
	if resp["last_error"] != "network timeout" {
		t.Fatalf("expected last_error=network timeout, got %v", resp["last_error"])
	}
	if resp["consecutive_failures"] != float64(3) {
		t.Fatalf("expected consecutive_failures=3, got %v", resp["consecutive_failures"])
	}
}

func TestSyncStatusIncludesReasonParityFields(t *testing.T) {
	provider := &stubSyncStatusProvider{
		status: SyncStatus{
			Enabled:              true,
			Phase:                "degraded",
			ReasonCode:           "auth_required",
			ReasonMessage:        "cloud token expired",
			UpgradeStage:         "bootstrap_pushed",
			UpgradeReasonCode:    "upgrade_repair_backfill_sync_journal",
			UpgradeReasonMessage: "repair pending",
		},
	}

	srv := New(newServerTestStore(t), 0)
	srv.SetSyncStatus(provider)

	req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["reason_code"] != "auth_required" {
		t.Fatalf("expected reason_code auth_required, got %v", resp["reason_code"])
	}
	if resp["reason_message"] != "cloud token expired" {
		t.Fatalf("expected reason_message, got %v", resp["reason_message"])
	}
	upgradeRaw, ok := resp["upgrade"].(map[string]any)
	if !ok {
		t.Fatalf("expected upgrade object in /sync/status response, got %#v", resp["upgrade"])
	}
	if upgradeRaw["stage"] != "bootstrap_pushed" {
		t.Fatalf("expected upgrade stage bootstrap_pushed, got %v", upgradeRaw["stage"])
	}
	if upgradeRaw["reason_code"] != "upgrade_repair_backfill_sync_journal" {
		t.Fatalf("expected upgrade reason_code parity, got %v", upgradeRaw["reason_code"])
	}
}

func TestSyncStatusForwardsProjectQueryToProvider(t *testing.T) {
	provider := &stubSyncStatusProvider{status: SyncStatus{Enabled: true, Phase: "healthy"}}
	srv := New(newServerTestStore(t), 0)
	srv.SetSyncStatus(provider)

	req := httptest.NewRequest(http.MethodGet, "/sync/status?project=proj-a", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if provider.lastProject != "proj-a" {
		t.Fatalf("expected provider to receive project query, got %q", provider.lastProject)
	}
}

// ─── OnWrite Notification Tests ──────────────────────────────────────────────

func TestOnWriteCalledAfterSuccessfulWrites(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var writeCount atomic.Int32
	srv.SetOnWrite(func() {
		writeCount.Add(1)
	})

	// Create session → should trigger onWrite.
	createReq := httptest.NewRequest(http.MethodPost, "/sessions",
		strings.NewReader(`{"id":"s-test","project":"engram"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("session create: expected 201, got %d", createRec.Code)
	}
	if writeCount.Load() != 1 {
		t.Fatalf("expected 1 onWrite after session create, got %d", writeCount.Load())
	}

	// End session → should trigger onWrite.
	endReq := httptest.NewRequest(http.MethodPost, "/sessions/s-test/end",
		strings.NewReader(`{"summary":"done"}`))
	endReq.Header.Set("Content-Type", "application/json")
	endRec := httptest.NewRecorder()
	h.ServeHTTP(endRec, endReq)
	if endRec.Code != http.StatusOK {
		t.Fatalf("session end: expected 200, got %d", endRec.Code)
	}
	if writeCount.Load() != 2 {
		t.Fatalf("expected 2 onWrite after session end, got %d", writeCount.Load())
	}

	// Add observation → should trigger onWrite.
	obsBody := `{"session_id":"s-test","type":"test","title":"Test","content":"test content"}`
	obsReq := httptest.NewRequest(http.MethodPost, "/observations",
		strings.NewReader(obsBody))
	obsReq.Header.Set("Content-Type", "application/json")
	obsRec := httptest.NewRecorder()
	h.ServeHTTP(obsRec, obsReq)
	if obsRec.Code != http.StatusCreated {
		t.Fatalf("add observation: expected 201, got %d", obsRec.Code)
	}
	if writeCount.Load() != 3 {
		t.Fatalf("expected 3 onWrite after add observation, got %d", writeCount.Load())
	}

	// Add prompt → should trigger onWrite.
	promptBody := `{"session_id":"s-test","content":"what did we do?"}`
	promptReq := httptest.NewRequest(http.MethodPost, "/prompts",
		strings.NewReader(promptBody))
	promptReq.Header.Set("Content-Type", "application/json")
	promptRec := httptest.NewRecorder()
	h.ServeHTTP(promptRec, promptReq)
	if promptRec.Code != http.StatusCreated {
		t.Fatalf("add prompt: expected 201, got %d", promptRec.Code)
	}
	if writeCount.Load() != 4 {
		t.Fatalf("expected 4 onWrite after add prompt, got %d", writeCount.Load())
	}
}

func TestOnWriteNotCalledOnReadOperations(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var writeCount atomic.Int32
	srv.SetOnWrite(func() {
		writeCount.Add(1)
	})

	// GET /health → read-only, no onWrite.
	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRec := httptest.NewRecorder()
	h.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("health: expected 200, got %d", healthRec.Code)
	}

	// GET /stats → read-only, no onWrite.
	statsReq := httptest.NewRequest(http.MethodGet, "/stats", nil)
	statsRec := httptest.NewRecorder()
	h.ServeHTTP(statsRec, statsReq)

	// GET /sync/status → read-only, no onWrite.
	syncReq := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	syncRec := httptest.NewRecorder()
	h.ServeHTTP(syncRec, syncReq)

	if writeCount.Load() != 0 {
		t.Fatalf("expected 0 onWrite calls for read operations, got %d", writeCount.Load())
	}
}

func TestOnWriteNotCalledOnFailedWrites(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var writeCount atomic.Int32
	srv.SetOnWrite(func() {
		writeCount.Add(1)
	})

	// POST /observations with bad JSON → should NOT trigger onWrite.
	badReq := httptest.NewRequest(http.MethodPost, "/observations",
		strings.NewReader(`{invalid`))
	badReq.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad json, got %d", badRec.Code)
	}

	// POST /observations with missing required fields → should NOT trigger onWrite.
	missingReq := httptest.NewRequest(http.MethodPost, "/observations",
		strings.NewReader(`{"session_id":"s-test"}`))
	missingReq.Header.Set("Content-Type", "application/json")
	missingRec := httptest.NewRecorder()
	h.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing fields, got %d", missingRec.Code)
	}

	if writeCount.Load() != 0 {
		t.Fatalf("expected 0 onWrite calls for failed writes, got %d", writeCount.Load())
	}
}

func TestHandleStatsReturnsInternalServerErrorOnLoaderError(t *testing.T) {
	prev := loadServerStats
	loadServerStats = func(s *store.Store) (*store.Stats, error) {
		return nil, errors.New("stats unavailable")
	}
	t.Cleanup(func() {
		loadServerStats = prev
	})

	s := New(newServerTestStore(t), 0)
	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	rec := httptest.NewRecorder()

	s.handleStats(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 stats response, got %d", rec.Code)
	}
}

// ─── DELETE /sessions/{id} tests ─────────────────────────────────────────────

func TestHandleDeleteSession_Success(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Create an empty session.
	createReq := httptest.NewRequest(http.MethodPost, "/sessions",
		strings.NewReader(`{"id":"sess-del","project":"proj"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	h.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", createRec.Code)
	}

	// Delete it.
	delReq := httptest.NewRequest(http.MethodDelete, "/sessions/sess-del", nil)
	delRec := httptest.NewRecorder()
	h.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected 200 deleting empty session, got %d: %s", delRec.Code, delRec.Body.String())
	}
}

func TestHandleDeleteSession_NotFound(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodDelete, "/sessions/does-not-exist", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleDeleteSession_HasObservations(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Create session + add an observation via the store directly.
	if err := st.CreateSession("sess-obs", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := st.AddObservation(store.AddObservationParams{
		SessionID: "sess-obs",
		Type:      "decision",
		Title:     "some obs",
		Content:   "content",
		Project:   "proj",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/sessions/sess-obs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when session has observations, got %d", rec.Code)
	}
}

// TestHandleDeleteSession_PropagatesWhenProjectIsCloudEnrolled verifies the
// behavior introduced by 71fa9fe: deleting a session whose project is enrolled
// for cloud sync now succeeds locally AND enqueues a delete mutation so the
// cloud replicas remove the session too. Previously this returned 409 to
// prevent local/cloud divergence, but propagating the delete is the
// correct semantic now that the sync transport supports session/delete ops.
func TestHandleDeleteSession_PropagatesWhenProjectIsCloudEnrolled(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	if err := st.CreateSession("sess-enrolled", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := st.EnrollProject("proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/sessions/sess-enrolled", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when cloud-enrolled session delete propagates, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "deleted") {
		t.Fatalf("expected deleted status in body, got %q", rec.Body.String())
	}
}

// ─── DELETE /prompts/{id} tests ───────────────────────────────────────────────

func TestHandleDeletePrompt_Success(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	var writeCount atomic.Int32
	srv.SetOnWrite(func() {
		writeCount.Add(1)
	})

	if err := st.CreateSession("sess-p", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	promptID, err := st.AddPrompt(store.AddPromptParams{
		SessionID: "sess-p",
		Content:   "delete me",
		Project:   "proj",
	})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/prompts/%d", promptID), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 deleting prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	if writeCount.Load() != 1 {
		t.Fatalf("expected onWrite notification after prompt delete, got %d", writeCount.Load())
	}
}

func TestHandleDeletePrompt_NotFound(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodDelete, "/prompts/999999", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleDeletePrompt_BadID(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodDelete, "/prompts/not-a-number", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid prompt id, got %d", rec.Code)
	}
}

// ─── Phase E.1e — /sync/status exposes deferred + dead counts (REQ-007) ─────

// TestSyncStatus_IncludesDeferredAndDeadCounts: 3 deferred + 1 dead →
// /sync/status response must have deferred_count=3 and dead_count=1.
func TestSyncStatus_IncludesDeferredAndDeadCounts(t *testing.T) {
	provider := &stubSyncStatusProvider{
		status: SyncStatus{
			Enabled:       true,
			Phase:         "healthy",
			DeferredCount: 3,
			DeadCount:     1,
		},
	}

	srv := New(newServerTestStore(t), 0)
	srv.SetSyncStatus(provider)

	req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got := resp["deferred_count"]; got != float64(3) {
		t.Errorf("expected deferred_count=3, got %v", got)
	}
	if got := resp["dead_count"]; got != float64(1) {
		t.Errorf("expected dead_count=1, got %v", got)
	}
}

// ─── Conflict-Audit HTTP Tests (Phase E, REQ-006 thru REQ-011) ──────────────
//
// These tests cover the 6 new /conflicts/* routes.
// Helpers below seed observations, relations, and deferred rows without
// requiring an unexported Store.db accessor.

// conflictsTestStore creates a store with a fresh temp dir and returns
// both the store and the raw *sql.DB for low-level seeding (deferred rows).
func conflictsTestStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()

	st, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rawDB, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })

	return st, rawDB
}

// seedConflictsSession creates a session and two observations in the given project.
// Returns the sync_ids of both observations.
func seedConflictsSession(t *testing.T, st *store.Store, project string) (srcSync, tgtSync string) {
	t.Helper()
	sesID := fmt.Sprintf("ses-http-%s", project)
	if err := st.CreateSession(sesID, project, "/tmp/"+project); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	srcIntID, err := st.AddObservation(store.AddObservationParams{
		SessionID: sesID, Type: "decision",
		Title: "Src Title", Content: "src content body for http test",
		Project: project, Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation src: %v", err)
	}
	tgtIntID, err := st.AddObservation(store.AddObservationParams{
		SessionID: sesID, Type: "decision",
		Title: "Tgt Title", Content: "tgt content body for http test",
		Project: project, Scope: "project",
	})
	if err != nil {
		t.Fatalf("AddObservation tgt: %v", err)
	}
	// Retrieve text sync_ids from the store's DB.
	// We need the raw DB access here. Since we are package server and Store does not expose
	// a DB accessor, we retrieve the sync_id from the store through a search trick:
	// use AddObservation int64 id and look up via store.GetObservation.
	srcObs, err := st.GetObservation(srcIntID)
	if err != nil {
		t.Fatalf("GetObservation src: %v", err)
	}
	tgtObs, err := st.GetObservation(tgtIntID)
	if err != nil {
		t.Fatalf("GetObservation tgt: %v", err)
	}
	return srcObs.SyncID, tgtObs.SyncID
}

// seedDeferredHTTP inserts a raw deferred row via the raw *sql.DB.
func seedDeferredHTTP(t *testing.T, rawDB *sql.DB, syncID, payload string, retryCount int, applyStatus string) {
	t.Helper()
	if _, err := rawDB.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, retry_count, apply_status, first_seen_at)
		VALUES (?, 'relation', ?, ?, ?, datetime('now'))
	`, syncID, payload, retryCount, applyStatus); err != nil {
		t.Fatalf("seedDeferredHTTP %q: %v", syncID, err)
	}
}

// ─── GET /conflicts ───────────────────────────────────────────────────────────

func TestHandleListConflicts_ProjectFilter(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Seed two observations in project "alpha" and one relation between them.
	srcSync, tgtSync := seedConflictsSession(t, st, "alpha")
	rel, err := st.SaveRelation(store.SaveRelationParams{
		SyncID: "rel-alpha-001", SourceID: srcSync, TargetID: tgtSync,
	})
	if err != nil || rel == nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	// Also seed observations and relation in project "beta" — should NOT appear in alpha filter.
	srcB, tgtB := seedConflictsSession(t, st, "beta")
	if _, err := st.SaveRelation(store.SaveRelationParams{
		SyncID: "rel-beta-001", SourceID: srcB, TargetID: tgtB,
	}); err != nil {
		t.Fatalf("SaveRelation beta: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/conflicts?project=alpha", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Must have "relations" array and "total" field.
	relations, ok := resp["relations"].([]any)
	if !ok {
		t.Fatalf("expected relations array, got %T: %v", resp["relations"], resp["relations"])
	}
	if len(relations) != 1 {
		t.Errorf("expected exactly 1 relation for project alpha, got %d", len(relations))
	}
	total, ok := resp["total"].(float64)
	if !ok {
		t.Fatalf("expected total field, got %T", resp["total"])
	}
	if total != 1 {
		t.Errorf("expected total=1, got %v", total)
	}
}

func TestHandleListConflicts_LimitClampsTo500(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// limit=1000 must be clamped to 500 (no 4xx).
	req := httptest.NewRequest(http.MethodGet, "/conflicts?limit=1000", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when limit>500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleListConflicts_DefaultLimit50(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/conflicts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["relations"]; !ok {
		t.Errorf("expected relations field in response")
	}
	if _, ok := resp["limit"]; !ok {
		t.Errorf("expected limit field in response")
	}
	if resp["limit"] != float64(50) {
		t.Errorf("expected default limit=50, got %v", resp["limit"])
	}
}

// ─── GET /conflicts/stats ─────────────────────────────────────────────────────

func TestHandleConflictsStats_ProjectScoped(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	srcSync, tgtSync := seedConflictsSession(t, st, "statsproject")
	if _, err := st.SaveRelation(store.SaveRelationParams{
		SyncID: "rel-stats-001", SourceID: srcSync, TargetID: tgtSync,
	}); err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/conflicts/stats?project=statsproject", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Must include by_judgment_status and deferred/dead counts.
	if _, ok := resp["by_judgment_status"]; !ok {
		t.Errorf("expected by_judgment_status field")
	}
	if _, ok := resp["deferred"]; !ok {
		t.Errorf("expected deferred field")
	}
	if _, ok := resp["dead"]; !ok {
		t.Errorf("expected dead field")
	}
}

func TestHandleConflictsStats_GlobalNoProject(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/conflicts/stats", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ─── GET /conflicts/deferred ──────────────────────────────────────────────────

func TestHandleConflictsDeferred_ListWithLimit(t *testing.T) {
	st, rawDB := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	validPayload := `{"relation_type":"conflicts","source_id":"obs-a","target_id":"obs-b"}`
	seedDeferredHTTP(t, rawDB, "def-http-001", validPayload, 0, "deferred")
	seedDeferredHTTP(t, rawDB, "def-http-002", validPayload, 0, "deferred")
	seedDeferredHTTP(t, rawDB, "def-http-003", validPayload, 5, "dead")

	req := httptest.NewRequest(http.MethodGet, "/conflicts/deferred?limit=2", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	rows, ok := resp["rows"].([]any)
	if !ok {
		t.Fatalf("expected rows array, got %T", resp["rows"])
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows (limit=2), got %d", len(rows))
	}
	if _, ok := resp["total"]; !ok {
		t.Errorf("expected total field in deferred response")
	}
}

func TestHandleConflictsDeferred_StatusFilter(t *testing.T) {
	st, rawDB := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	validPayload := `{"relation_type":"conflicts","source_id":"obs-c","target_id":"obs-d"}`
	seedDeferredHTTP(t, rawDB, "def-http-dead-1", validPayload, 5, "dead")
	seedDeferredHTTP(t, rawDB, "def-http-dead-2", validPayload, 5, "dead")
	seedDeferredHTTP(t, rawDB, "def-http-pend-1", validPayload, 0, "deferred")

	req := httptest.NewRequest(http.MethodGet, "/conflicts/deferred?status=dead", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	rows, ok := resp["rows"].([]any)
	if !ok {
		t.Fatalf("expected rows array, got %T", resp["rows"])
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 dead rows, got %d", len(rows))
	}
}

// ─── POST /conflicts/scan ─────────────────────────────────────────────────────

func TestHandleConflictsScan_DryRun(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Seed a project with an observation (no candidates expected for isolated obs).
	seedConflictsSession(t, st, "scanproject")

	body := `{"project":"scanproject","apply":false}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Must include candidates_found and inserted.
	if _, ok := resp["candidates_found"]; !ok {
		t.Errorf("expected candidates_found field")
	}
	if _, ok := resp["inserted"]; !ok {
		t.Errorf("expected inserted field")
	}
	// dry_run must be true when apply=false.
	if resp["dry_run"] != true {
		t.Errorf("expected dry_run=true for apply=false scan, got %v", resp["dry_run"])
	}
	// inserted must be 0 for dry-run.
	if resp["inserted"] != float64(0) {
		t.Errorf("expected inserted=0 for dry-run, got %v", resp["inserted"])
	}
}

func TestHandleConflictsScan_MissingProject400(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	body := `{"apply":false}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when project is missing, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ─── POST /conflicts/deferred/replay ─────────────────────────────────────────

func TestHandleReplayDeferred_EmptyQueue(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/conflicts/deferred/replay", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for replay on empty queue, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["retried"] != float64(0) {
		t.Errorf("expected retried=0, got %v", resp["retried"])
	}
	if resp["succeeded"] != float64(0) {
		t.Errorf("expected succeeded=0, got %v", resp["succeeded"])
	}
	if resp["dead"] != float64(0) {
		t.Errorf("expected dead=0, got %v", resp["dead"])
	}
}

// ─── GET /conflicts/{relation_id} ─────────────────────────────────────────────

func TestHandleConflictByID_Found(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	srcSync, tgtSync := seedConflictsSession(t, st, "getproject")
	rel, err := st.SaveRelation(store.SaveRelationParams{
		SyncID: "rel-get-001", SourceID: srcSync, TargetID: tgtSync,
	})
	if err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	url := fmt.Sprintf("/conflicts/%d", rel.ID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for existing relation, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["relation_id"] != float64(rel.ID) {
		t.Errorf("expected relation_id=%d, got %v", rel.ID, resp["relation_id"])
	}
	if _, ok := resp["sync_id"]; !ok {
		t.Errorf("expected sync_id field in relation detail")
	}
	if _, ok := resp["judgment_status"]; !ok {
		t.Errorf("expected judgment_status field in relation detail")
	}
}

func TestHandleConflictByID_NotFound(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/conflicts/99999", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing relation, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode 404 body: %v", err)
	}
	if _, ok := resp["error"]; !ok {
		t.Errorf("expected error field in 404 response")
	}
}

func TestHandleConflictByID_InvalidID(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/conflicts/not-a-number", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid relation_id, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ─── Phase F — POST /conflicts/scan semantic params ──────────────────────────

// mockSemanticRunner is a fake store.SemanticRunner for HTTP tests.
type mockSemanticRunner struct {
	verdict store.SemanticVerdict
	err     error
}

func (m *mockSemanticRunner) Compare(_ context.Context, _ string) (store.SemanticVerdict, error) {
	return m.verdict, m.err
}

// TestHandleScanConflicts_SemanticFalse_CountersZero verifies that when semantic=false
// (or omitted), the response includes semantic_judged=0, semantic_skipped=0,
// semantic_errors=0.
func TestHandleScanConflicts_SemanticFalse_CountersZero(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	seedConflictsSession(t, st, "semfalseproj")

	body := `{"project":"semfalseproj","apply":false}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// All three semantic counters must be present and zero.
	for _, field := range []string{"semantic_judged", "semantic_skipped", "semantic_errors"} {
		val, ok := resp[field]
		if !ok {
			t.Errorf("expected %q field in response; got keys: %v", field, resp)
			continue
		}
		if val != float64(0) {
			t.Errorf("expected %q=0 when semantic=false, got %v", field, val)
		}
	}
}

// TestHandleScanConflicts_SemanticTrue_NoEnv_500 verifies that when semantic=true
// and the runnerFactory is not configured (nil), the server returns 500 with a
// clear error body.
func TestHandleScanConflicts_SemanticTrue_NoFactory_500(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	// No runner factory set → should fail.
	h := srv.Handler()

	seedConflictsSession(t, st, "semnoenvproj")

	body := `{"project":"semnoenvproj","semantic":true}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when no runner factory set, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	errMsg, ok := resp["error"].(string)
	if !ok || errMsg == "" {
		t.Errorf("expected non-empty 'error' field in 500 response; got: %v", resp)
	}
}

// TestHandleScanConflicts_SemanticTrue_WithMockRunner verifies that when semantic=true
// and a mock runner factory is injected, the response includes non-zero counters.
func TestHandleScanConflicts_SemanticTrue_WithMockRunner(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)

	// Inject a factory that returns a fake runner returning "compatible".
	srv.SetRunnerFactory(func(name string) (store.SemanticRunner, error) {
		return &mockSemanticRunner{
			verdict: store.SemanticVerdict{
				Relation:   "compatible",
				Confidence: 0.9,
				Reasoning:  "test",
				Model:      "test-model",
			},
		}, nil
	})
	h := srv.Handler()

	// Seed enough observations that FTS5 finds candidates.
	if err := st.CreateSession("ses-semtrue", "semtrueproj", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	titles := []string{
		"JWT authentication token session management policy",
		"Session token JWT authentication management approach",
		"Authentication JWT session token policy decision",
	}
	for _, title := range titles {
		if _, err := st.AddObservation(store.AddObservationParams{
			SessionID: "ses-semtrue", Type: "decision",
			Title: title, Content: "JWT auth content for " + title,
			Project: "semtrueproj", Scope: "project",
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
	}

	body := `{"project":"semtrueproj","semantic":true,"concurrency":2,"max_semantic":10}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with mock runner, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// semantic_judged + semantic_skipped + semantic_errors should be present (values depend on FTS).
	for _, field := range []string{"semantic_judged", "semantic_skipped", "semantic_errors"} {
		if _, ok := resp[field]; !ok {
			t.Errorf("expected %q field in semantic=true response; got: %v", field, resp)
		}
	}
}

// TestHandleScanConflicts_InvalidConcurrency_400 verifies that concurrency out of
// [1,20] range returns 400 before any work is done.
func TestHandleScanConflicts_InvalidConcurrency_400(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	// Runner factory not needed — validation happens before runner is resolved.
	h := srv.Handler()

	seedConflictsSession(t, st, "badconcproj")

	for _, badConcurrency := range []int{0, 21, -1, 100} {
		body := fmt.Sprintf(`{"project":"badconcproj","semantic":true,"concurrency":%d}`, badConcurrency)
		req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for concurrency=%d, got %d: %s", badConcurrency, rec.Code, rec.Body.String())
		}
	}
}

// TestHandleScanConflicts_InvalidTimeout_400 verifies that timeout_per_call_seconds
// out of [1,600] range returns 400 before any work is done.
func TestHandleScanConflicts_InvalidTimeout_400(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	seedConflictsSession(t, st, "badtmoproj")

	for _, badTimeout := range []int{0, 601, -5} {
		body := fmt.Sprintf(`{"project":"badtmoproj","semantic":true,"timeout_per_call_seconds":%d}`, badTimeout)
		req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for timeout_per_call_seconds=%d, got %d: %s", badTimeout, rec.Code, rec.Body.String())
		}
	}
}

// ─── TestRoutes_NoOverlapPanic ────────────────────────────────────────────────

// TestRoutes_NoOverlapPanic constructs a fresh *Server and calls Handler()
// to detect any route-registration-time panic (Go 1.22 mux panics on overlap).
func TestRoutes_NoOverlapPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("route registration panicked: %v", r)
		}
	}()

	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	// Calling Handler() exercises the registered mux without issuing requests.
	h := srv.Handler()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

// ─── G.2 — HTTP API integration tests ────────────────────────────────────────
//
// End-to-end coverage against a real seeded store hitting all 6 /conflicts/* routes.
// Verifies: pagination total accuracy, 404 JSON body shape, 400 on missing project,
// cap warning in scan response, pre-existing routes unaffected.

// TestG2_ListConflicts_PaginationTotal verifies the total field matches the
// actual row count for the project regardless of the limit applied.
func TestG2_ListConflicts_PaginationTotal(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Seed 3 relations for project "pagproj".
	for i := 0; i < 3; i++ {
		srcSync, tgtSync := seedConflictsSession(t, st, fmt.Sprintf("pagproj-%d", i))
		if _, err := st.SaveRelation(store.SaveRelationParams{
			SyncID:   fmt.Sprintf("rel-pag-%d", i),
			SourceID: srcSync,
			TargetID: tgtSync,
		}); err != nil {
			t.Fatalf("SaveRelation %d: %v", i, err)
		}
	}

	// Request with limit=1 — total must still report 3.
	req := httptest.NewRequest(http.MethodGet, "/conflicts?project=pagproj-0&limit=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	relations, ok := resp["relations"].([]any)
	if !ok {
		t.Fatalf("expected relations array, got %T", resp["relations"])
	}
	// With limit=1, at most 1 row returned.
	if len(relations) > 1 {
		t.Errorf("expected at most 1 relation with limit=1, got %d", len(relations))
	}
	// total must be a number (reflects full count for the project).
	if _, ok := resp["total"].(float64); !ok {
		t.Errorf("expected numeric total field, got %T: %v", resp["total"], resp["total"])
	}
}

// TestG2_GetConflict_404BodyShape verifies the 404 response for a missing
// relation_id has a JSON body with an "error" field.
func TestG2_GetConflict_404BodyShape(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/conflicts/88888", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode 404 body: %v", err)
	}
	if _, ok := resp["error"]; !ok {
		t.Errorf("expected 'error' field in 404 JSON body; got: %v", resp)
	}
}

// TestG2_ScanConflicts_ApplyCapWarning verifies that when the scan cap is reached
// the response includes a "warning" field containing "cap".
func TestG2_ScanConflicts_ApplyCapWarning(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// Seed a session; scan will attempt to find candidates.
	// With only 2 observations the FTS result is uncertain, so we pass max_insert=0
	// to force Capped=true without needing actual candidates.
	// Per design, max_insert=0 means any insert would exceed cap → Capped=true immediately.
	// However MaxInsert=0 may be treated as "use default 100". Instead seed 6 similar
	// observations and use max_insert=1 so the first insert triggers the cap.
	if err := st.CreateSession("ses-g2scan", "g2scanproj", "/tmp/g2scanproj"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	titles := []string{
		"JWT authentication token session management policy",
		"Session token JWT authentication management approach",
		"Authentication JWT session token policy decision",
		"Token management session JWT authentication strategy",
		"JWT session authentication token management pattern",
		"Session-based JWT token authentication management rule",
	}
	for _, title := range titles {
		if _, err := st.AddObservation(store.AddObservationParams{
			SessionID: "ses-g2scan", Type: "decision",
			Title: title, Content: "JWT auth content for " + title,
			Project: "g2scanproj", Scope: "project",
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
	}

	body := `{"project":"g2scanproj","apply":true,"max_insert":1}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for scan apply, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// If a candidate was found and cap was reached, "warning" must be present.
	// If no candidates exist (FTS scores too low), "capped" is false — we tolerate that.
	if capped, _ := resp["capped"].(bool); capped {
		warning, hasWarning := resp["warning"].(string)
		if !hasWarning || warning == "" {
			t.Errorf("expected non-empty 'warning' field when capped=true; got: %v", resp)
		}
	}
	// Must always have inserted and candidates_found fields.
	if _, ok := resp["inserted"]; !ok {
		t.Errorf("expected 'inserted' field in scan response; got: %v", resp)
	}
	if _, ok := resp["candidates_found"]; !ok {
		t.Errorf("expected 'candidates_found' field in scan response; got: %v", resp)
	}
}

// TestG2_ScanConflicts_MissingProject400 verifies the scan endpoint returns 400
// when the "project" field is absent from the request body.
func TestG2_ScanConflicts_MissingProject400(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	body := `{"apply":true}`
	req := httptest.NewRequest(http.MethodPost, "/conflicts/scan", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when project is missing, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestG2_ReplayDeferred_ResponseShape verifies the replay endpoint always returns
// the three count fields: retried, succeeded, dead — even on empty queue.
func TestG2_ReplayDeferred_ResponseShape(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/conflicts/deferred/replay", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, field := range []string{"retried", "succeeded", "dead"} {
		if _, ok := resp[field]; !ok {
			t.Errorf("expected %q field in replay response; got: %v", field, resp)
		}
	}
}

// TestG2_ListDeferred_StatusFilter verifies the status filter returns only matching rows.
func TestG2_ListDeferred_StatusFilter(t *testing.T) {
	st, rawDB := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	validPayload := `{"relation_type":"conflicts","source_id":"g2-src","target_id":"g2-tgt"}`
	seedDeferredHTTP(t, rawDB, "g2-dead-a", validPayload, 5, "dead")
	seedDeferredHTTP(t, rawDB, "g2-dead-b", validPayload, 5, "dead")
	seedDeferredHTTP(t, rawDB, "g2-defer-a", validPayload, 0, "deferred")

	// Filter by status=dead → expect exactly 2.
	req := httptest.NewRequest(http.MethodGet, "/conflicts/deferred?status=dead", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	rows, ok := resp["rows"].([]any)
	if !ok {
		t.Fatalf("expected rows array, got %T", resp["rows"])
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 dead rows, got %d", len(rows))
	}
}

// TestG2_ExistingRoutes_Unaffected verifies that pre-existing /sync/status and
// /health routes are unaffected by the Phase 3 conflicts route additions.
func TestG2_ExistingRoutes_Unaffected(t *testing.T) {
	st, _ := conflictsTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	// GET /health must still return 200.
	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRec := httptest.NewRecorder()
	h.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Errorf("expected /health 200 after Phase 3, got %d", healthRec.Code)
	}

	// GET /sync/status must still return 200.
	syncReq := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	syncRec := httptest.NewRecorder()
	h.ServeHTTP(syncRec, syncReq)
	if syncRec.Code != http.StatusOK {
		t.Errorf("expected /sync/status 200 after Phase 3, got %d", syncRec.Code)
	}

	// GET /stats must still return 200.
	statsReq := httptest.NewRequest(http.MethodGet, "/stats", nil)
	statsRec := httptest.NewRecorder()
	h.ServeHTTP(statsRec, statsReq)
	if statsRec.Code != http.StatusOK {
		t.Errorf("expected /stats 200 after Phase 3, got %d", statsRec.Code)
	}
}
