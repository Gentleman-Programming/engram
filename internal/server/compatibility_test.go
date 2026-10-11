package server

import (
	"encoding/json"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompatibilityEndpointBoundToActualStore(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", "")
	st, err := store.New(store.FallbackConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	s := New(st, 0)
	s.SetVersion("old-unrelated-version")
	s.instanceID = "untrusted-external-metadata"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/compatibility", nil))
	var result store.Compatibility
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !result.ActiveReuseAuthorized || result.InstanceID != st.InstanceID() || result.RuntimeID == "" || result.OfflineAutomaticOpeningAuthorized || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unbound result: %d %+v", w.Code, result)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/compatibility", nil))
	if w.Code != 405 {
		t.Fatalf("POST status %d", w.Code)
	}
	t.Setenv("ENGRAM_HTTP_TOKEN", "fixture-token")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/compatibility", nil))
	if w.Code != 401 {
		t.Fatalf("auth status %d", w.Code)
	}
	_ = st.Close()
	t.Setenv("ENGRAM_HTTP_TOKEN", "")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/compatibility", nil))
	if !strings.Contains(w.Body.String(), `"active_reuse_authorized":false`) {
		t.Fatal(w.Body.String())
	}
}

func TestCompatibilityUnknownHandle(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", "")
	s := New(nil, 0)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/compatibility", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"active_reuse_authorized":false`) {
		t.Fatalf("unknown handle must deny: %d %s", w.Code, w.Body.String())
	}
}
