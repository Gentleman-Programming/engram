package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
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

// ─── Sync Status Tests ───────────────────────────────────────────────────────

// stubSyncStatusProvider is a fake SyncStatusProvider for tests.
type stubSyncStatusProvider struct {
	status SyncStatus
}

func (s *stubSyncStatusProvider) Status() SyncStatus {
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

func TestPiFlowPrivacyTagsAreRedactedThroughServerEndpoints(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

	if err := st.CreateSession("pi-session", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	obsReq := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(`{"session_id":"pi-session","type":"note","title":"Title <private>secret-title</private>","content":"Body <private>secret-body</private>"}`))
	obsReq.Header.Set("Content-Type", "application/json")
	obsRec := httptest.NewRecorder()
	h.ServeHTTP(obsRec, obsReq)
	if obsRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 saving observation, got %d", obsRec.Code)
	}

	var obsCreated map[string]any
	if err := json.NewDecoder(obsRec.Body).Decode(&obsCreated); err != nil {
		t.Fatalf("decode observation create response: %v", err)
	}
	obsID := int(obsCreated["id"].(float64))

	getObsReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/observations/%d", obsID), nil)
	getObsRec := httptest.NewRecorder()
	h.ServeHTTP(getObsRec, getObsReq)
	if getObsRec.Code != http.StatusOK {
		t.Fatalf("expected 200 fetching observation, got %d", getObsRec.Code)
	}

	var obs map[string]any
	if err := json.NewDecoder(getObsRec.Body).Decode(&obs); err != nil {
		t.Fatalf("decode observation response: %v", err)
	}

	obsTitle := obs["title"].(string)
	obsContent := obs["content"].(string)
	if !strings.Contains(obsTitle, "[REDACTED]") || !strings.Contains(obsContent, "[REDACTED]") {
		t.Fatalf("expected redaction markers in observation, got title=%q content=%q", obsTitle, obsContent)
	}
	if strings.Contains(obsTitle, "secret-title") || strings.Contains(obsContent, "secret-body") {
		t.Fatalf("private payload leaked in observation, got title=%q content=%q", obsTitle, obsContent)
	}

	promptReq := httptest.NewRequest(http.MethodPost, "/prompts", strings.NewReader(`{"session_id":"pi-session","project":"engram","content":"Prompt <private>secret-prompt</private>"}`))
	promptReq.Header.Set("Content-Type", "application/json")
	promptRec := httptest.NewRecorder()
	h.ServeHTTP(promptRec, promptReq)
	if promptRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 saving prompt, got %d", promptRec.Code)
	}

	recentReq := httptest.NewRequest(http.MethodGet, "/prompts/recent?project=engram&limit=1", nil)
	recentRec := httptest.NewRecorder()
	h.ServeHTTP(recentRec, recentReq)
	if recentRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for recent prompts, got %d", recentRec.Code)
	}

	var prompts []map[string]any
	if err := json.NewDecoder(recentRec.Body).Decode(&prompts); err != nil {
		t.Fatalf("decode recent prompts response: %v", err)
	}
	if len(prompts) != 1 {
		t.Fatalf("expected one recent prompt, got %d", len(prompts))
	}

	promptContent := prompts[0]["content"].(string)
	if !strings.Contains(promptContent, "[REDACTED]") {
		t.Fatalf("expected redaction marker in prompt content, got %q", promptContent)
	}
	if strings.Contains(promptContent, "secret-prompt") {
		t.Fatalf("private prompt content leaked: %q", promptContent)
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

// ─── DELETE /prompts/{id} tests ───────────────────────────────────────────────

func TestHandleDeletePrompt_Success(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	h := srv.Handler()

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
