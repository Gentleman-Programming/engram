package cloudserver

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaskAPIKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"full token", "engram_sk_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "engram_sk_0123…cdef"},
		{"short token fully masked", "engram_sk_abc", "engram_sk_****"},
		{"empty", "", ""},
		{"no prefix passthrough", "not-a-token", "not-a-token"},
		{"bearer scheme stripped then masked", "Bearer engram_sk_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "Bearer engram_sk_0123…cdef"},
		{"bearer empty", "Bearer ", "Bearer "},
		{"bearer non-engram token masked", "Bearer my-opaque-jwt-value", "Bearer ***masked***"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MaskAPIKey(c.in)
			if got != c.want {
				t.Fatalf("MaskAPIKey(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestRequestLogMiddleware_MasksAuthorization(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	handler := RequestLogMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rawToken := "engram_sk_abc123def456abc123def456abc123def456abc123def456abc123def456"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	out := buf.String()
	if strings.Contains(out, rawToken) {
		t.Fatalf("log output contains raw token: %s", out)
	}
	if !strings.Contains(out, "GET") || !strings.Contains(out, "/api/v1/anything") {
		t.Fatalf("log output missing method/path: %s", out)
	}
	if !strings.Contains(out, "200") {
		t.Fatalf("log output missing status code: %s", out)
	}
}

func TestRequestLogMiddleware_NoAuthHeader(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	handler := RequestLogMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	out := buf.String()
	if !strings.Contains(out, "201") || !strings.Contains(out, "/health") {
		t.Fatalf("unexpected log line: %s", out)
	}
}
