package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// authCase describes a single endpoint that should be guarded when
// ENGRAM_HTTP_TOKEN is set.
type authCase struct {
	method string
	path   string
	body   string
}

// destructiveEndpoints lists every route that must be protected when a token
// is configured. Read-only endpoints must NOT be in this list.
var destructiveEndpoints = []authCase{
	{http.MethodDelete, "/sessions/some-id", ""},
	{http.MethodDelete, "/observations/1", ""},
	{http.MethodDelete, "/prompts/1", ""},
	{http.MethodGet, "/export", ""},
	{http.MethodPost, "/import", `{}`},
	{http.MethodPost, "/projects/migrate", `{"old_project":"a","new_project":"b"}`},
}

// safeEndpoints are read-only routes that must never require auth, even when
// the token is set.
var safeEndpoints = []authCase{
	{http.MethodGet, "/health", ""},
	{http.MethodGet, "/observations/recent", ""},
	{http.MethodGet, "/search?q=test", ""},
	{http.MethodGet, "/stats", ""},
	{http.MethodGet, "/sync/status", ""},
}

// TestOptionalAuth_NoToken verifies that when ENGRAM_HTTP_TOKEN is unset,
// destructive endpoints are reachable (zero-config preserved). We only check
// that the response is NOT 401/403 — we don't assert specific success codes
// because the store is empty (404/400 are acceptable).
func TestOptionalAuth_NoToken(t *testing.T) {
	os.Unsetenv("ENGRAM_HTTP_TOKEN")

	st := newServerTestStore(t)
	h := New(st, 0).Handler()

	for _, tc := range destructiveEndpoints {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var body *strings.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
				t.Fatalf("expected no auth enforcement without token, got %d for %s %s",
					rec.Code, tc.method, tc.path)
			}
		})
	}
}

// TestOptionalAuth_WithToken_NoCredential verifies that when ENGRAM_HTTP_TOKEN
// is set, destructive endpoints return 401 when no Authorization header is
// provided.
func TestOptionalAuth_WithToken_NoCredential(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", "super-secret-token")

	st := newServerTestStore(t)
	h := New(st, 0).Handler()

	for _, tc := range destructiveEndpoints {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var body *strings.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 for unauthenticated destructive request, got %d for %s %s body=%q",
					rec.Code, tc.method, tc.path, rec.Body.String())
			}
		})
	}
}

// TestOptionalAuth_WithToken_WrongCredential verifies that a wrong token value
// also returns 401.
func TestOptionalAuth_WithToken_WrongCredential(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", "super-secret-token")

	st := newServerTestStore(t)
	h := New(st, 0).Handler()

	for _, tc := range destructiveEndpoints {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var body *strings.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Authorization", "Bearer wrong-token")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 for wrong token, got %d for %s %s body=%q",
					rec.Code, tc.method, tc.path, rec.Body.String())
			}
		})
	}
}

// TestOptionalAuth_WithToken_CorrectCredential verifies that the correct Bearer
// token grants access (response must not be 401 or 403).
func TestOptionalAuth_WithToken_CorrectCredential(t *testing.T) {
	const token = "super-secret-token"
	t.Setenv("ENGRAM_HTTP_TOKEN", token)

	st := newServerTestStore(t)
	h := New(st, 0).Handler()

	for _, tc := range destructiveEndpoints {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var body *strings.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			} else {
				body = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.path, body)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
				t.Fatalf("expected access with correct token, got %d for %s %s body=%q",
					rec.Code, tc.method, tc.path, rec.Body.String())
			}
		})
	}
}

// TestOptionalAuth_ReadEndpointsUnaffected verifies that read-only endpoints
// remain accessible even when the token is set (no auth required for reads).
func TestOptionalAuth_ReadEndpointsUnaffected(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", "super-secret-token")

	st := newServerTestStore(t)
	h := New(st, 0).Handler()

	for _, tc := range safeEndpoints {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
				t.Fatalf("expected read endpoint to be accessible without token header, got %d for %s %s",
					rec.Code, tc.method, tc.path)
			}
		})
	}
}

// TestOptionalAuth_TokenReadFromEnvAtRequestTime verifies that the token is
// read from the environment at request time, not captured at server init. This
// ensures the zero-config guarantee: if the env var is set after startup, it
// takes effect immediately; if unset, everything is open.
func TestOptionalAuth_TokenReadFromEnvAtRequestTime(t *testing.T) {
	os.Unsetenv("ENGRAM_HTTP_TOKEN")

	st := newServerTestStore(t)
	// Server is created WITHOUT the token set.
	h := New(st, 0).Handler()

	// First request: no token → open access.
	req := httptest.NewRequest(http.MethodGet, "/export", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("expected open access when token unset at request time, got 401")
	}

	// Now set the token.
	t.Setenv("ENGRAM_HTTP_TOKEN", "late-token")

	// Second request without Authorization → must be blocked.
	req2 := httptest.NewRequest(http.MethodGet, "/export", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after token was set in env, got %d body=%q",
			rec2.Code, rec2.Body.String())
	}
}
