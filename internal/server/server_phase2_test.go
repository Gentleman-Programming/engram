package server

// server_phase2_test.go — Phase 2 audit tests for HTTP API (2026-04-22).
//
// T9 (from the 10-test list): HTTP /search passes since/until to store.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func newTestServerStore(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("default config: %v", err)
	}
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	srv := New(s, 0)
	return srv, s
}

// TestHTTPSearchPassesSinceUntilToStore verifies that GET /search?since=&until=
// are forwarded to store.Search, not silently ignored.
func TestHTTPSearchPassesSinceUntilToStore(t *testing.T) {
	srv, s := newTestServerStore(t)

	// Seed data
	s.CreateSession("s1", "proj", "")
	s.AddObservation(store.AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "test obs", Content: "search target content", Project: "proj",
	})

	// Search with future since → should return 0 results
	req := httptest.NewRequest("GET", "/search?q=search+target&since=2999-01-01", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var results []store.SearchResult
	if err := json.Unmarshal(w.Body.Bytes(), &results); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results with future since, got %d — since parameter not passed to store", len(results))
	}

	// Search with past since → should return results
	req2 := httptest.NewRequest("GET", "/search?q=search+target&since=2000-01-01", nil)
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	var results2 []store.SearchResult
	if err := json.Unmarshal(w2.Body.Bytes(), &results2); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(results2) == 0 {
		t.Error("expected results with past since, got 0")
	}
}

// TestHTTPSearchInvalidDateReturns500 verifies the HTTP layer surfaces
// validation errors from the store, not silently ignoring bad dates.
func TestHTTPSearchInvalidDateReturns500(t *testing.T) {
	srv, s := newTestServerStore(t)

	s.CreateSession("s1", "proj", "")
	s.AddObservation(store.AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "test", Content: "content", Project: "proj",
	})

	req := httptest.NewRequest("GET", "/search?q=test&since=not-a-date", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Fatalf("expected non-200 for invalid since date, got 200 — bad date silently ignored")
	}
}
