package remote

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNewClient_EmptyAPIKey(t *testing.T) {
	_, err := NewClient("http://localhost", "", "1.0.0")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestNewClient_Valid(t *testing.T) {
	c, err := NewClient("http://localhost", "tok-abc", "1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestClient_AuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "tok-abc", "1.0.0")
	_, _ = c.Get(context.Background(), "/test")

	if gotAuth != "Bearer tok-abc" {
		t.Fatalf("expected 'Bearer tok-abc', got %q", gotAuth)
	}
}

func TestClient_UserAgentHeader(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "tok-abc", "1.2.3")
	_, _ = c.Get(context.Background(), "/test")

	if gotUA != "engram-client/1.2.3" {
		t.Fatalf("expected 'engram-client/1.2.3', got %q", gotUA)
	}
}

func TestClient_ProtocolVersionHeader(t *testing.T) {
	var gotProto string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProto = r.Header.Get("X-Engram-Protocol")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "tok-abc", "1.0.0")
	_, _ = c.Get(context.Background(), "/test")

	if gotProto != "1" {
		t.Fatalf("expected '1', got %q", gotProto)
	}
}

func TestClient_ErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantErr    error
	}{
		{"401 → ErrUnauthorized", 401, ErrUnauthorized},
		{"403 → ErrUnauthorized", 403, ErrUnauthorized},
		{"404 → ErrNotFound", 404, ErrNotFound},
		{"429 → ErrRateLimited", 429, ErrRateLimited},
		{"500 → ErrServerError", 500, ErrServerError},
		{"502 → ErrServerError", 502, ErrServerError},
		{"503 → ErrServerError", 503, ErrServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				w.Write([]byte(`{"error":"test"}`))
			}))
			defer srv.Close()

			c, _ := NewClient(srv.URL, "tok-abc", "1.0.0")
			c.maxRetry = 1 // no retries for error mapping test
			_, err := c.Get(context.Background(), "/test")

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestClient_RetryOn500ThenSuccess(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(500)
			w.Write([]byte(`{"error":"fail"}`))
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "tok-abc", "1.0.0")
	c.baseBackoff = 1 // 1ms for fast tests
	resp, err := c.Get(context.Background(), "/test")
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestClient_NoRetryOn401(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "tok-abc", "1.0.0")
	_, err := c.Get(context.Background(), "/test")

	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected 1 attempt (no retry), got %d", attempts.Load())
	}
}

func TestClient_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"fail"}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "tok-abc", "1.0.0")
	c.baseBackoff = 1000 // long backoff so cancellation hits during wait

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := c.Get(ctx, "/test")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestClient_Post(t *testing.T) {
	var gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody = make([]byte, r.ContentLength)
		r.Body.Read(gotBody)
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"123"}`))
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "tok-abc", "1.0.0")
	resp, err := c.Post(context.Background(), "/items", []byte(`{"name":"test"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if gotMethod != "POST" {
		t.Fatalf("expected POST, got %s", gotMethod)
	}
	if string(gotBody) != `{"name":"test"}` {
		t.Fatalf("unexpected body: %s", gotBody)
	}
}
