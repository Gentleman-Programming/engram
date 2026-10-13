package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestSessionOwnedFullHandlerAuth(t *testing.T) {
	cfg := store.FallbackConfig(t.TempDir())
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	srv := New(s, 0)
	owned, ok := any(srv).(interface{ SetSessionOwnedToken(string) })
	if !ok {
		t.Fatal("server lacks session-owned full-handler auth")
	}
	token := strings.Repeat("b", 64)
	owned.SetSessionOwnedToken(token)
	for _, path := range []string{"/health", "/stats", "/sessions", "/compatibility", "/missing"} {
		for _, auth := range []string{"", "Bearer wrong", "Bearer " + token} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", auth)
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if auth != "Bearer "+token && w.Code != 401 {
				t.Fatalf("%s status %d", path, w.Code)
			}
			if auth == "Bearer "+token && w.Code == 401 {
				t.Fatalf("authenticated %s rejected", path)
			}
		}
	}
}

func TestSessionOwnedSharedHealthUnchanged(t *testing.T) {
	cfg := store.FallbackConfig(t.TempDir())
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Setenv("ENGRAM_HTTP_TOKEN", "shared")
	w := httptest.NewRecorder()
	New(s, 0).Handler().ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 200 {
		t.Fatalf("shared health changed: %d", w.Code)
	}
}
