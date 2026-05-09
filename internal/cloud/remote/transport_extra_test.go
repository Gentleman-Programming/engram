package remote

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
)

// ─── NewRemoteTransport extraHeaders env wiring ──────────────────────────────

func TestNewRemoteTransport_ExtraHeaders_ValidEnv(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_EXTRA_HEADERS", "X-Tenant-ID: acme")
	rt, err := NewRemoteTransport("http://localhost:8080", "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}
	if len(rt.extraHeaders) == 0 {
		t.Fatal("expected extraHeaders to be populated from env, got empty")
	}
	if v, ok := rt.extraHeaders["X-Tenant-Id"]; !ok || v != "acme" {
		t.Fatalf("expected X-Tenant-Id=acme, got %v", rt.extraHeaders)
	}
}

func TestNewRemoteTransport_ExtraHeaders_Unset(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_EXTRA_HEADERS", "")
	rt, err := NewRemoteTransport("http://localhost:8080", "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}
	if rt.extraHeaders != nil {
		t.Fatalf("expected extraHeaders to be nil when env not set, got %v", rt.extraHeaders)
	}
}

func TestNewRemoteTransport_ExtraHeaders_EmptyString(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_EXTRA_HEADERS", "")
	rt, err := NewRemoteTransport("http://localhost:8080", "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}
	if rt.extraHeaders != nil {
		t.Fatalf("expected extraHeaders=nil for empty env, got %v", rt.extraHeaders)
	}
}

// ─── NewMutationTransport extraHeaders env wiring ────────────────────────────

func TestNewMutationTransport_ExtraHeaders_ValidEnv(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_EXTRA_HEADERS", "X-Tenant-ID: acme")
	mt, err := NewMutationTransport("http://localhost:8080", "token")
	if err != nil {
		t.Fatalf("NewMutationTransport: %v", err)
	}
	if len(mt.extraHeaders) == 0 {
		t.Fatal("expected extraHeaders to be populated from env, got empty")
	}
	if v, ok := mt.extraHeaders["X-Tenant-Id"]; !ok || v != "acme" {
		t.Fatalf("expected X-Tenant-Id=acme, got %v", mt.extraHeaders)
	}
}

func TestNewMutationTransport_ExtraHeaders_Unset(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_EXTRA_HEADERS", "")
	mt, err := NewMutationTransport("http://localhost:8080", "token")
	if err != nil {
		t.Fatalf("NewMutationTransport: %v", err)
	}
	if mt.extraHeaders != nil {
		t.Fatalf("expected extraHeaders=nil when env not set, got %v", mt.extraHeaders)
	}
}

// ─── HTTP header propagation: RemoteTransport ────────────────────────────────

// captureHeadersServer starts a test server that captures request headers and returns 200.
// capturedHeaders is set once the first request arrives; call the returned close func when done.
func captureHeadersServer(t *testing.T, method, path string, responseBody string) (*httptest.Server, *http.Header, *sync.Once) {
	t.Helper()
	var captured http.Header
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == method && r.URL.Path == path {
			once.Do(func() {
				captured = r.Header.Clone()
			})
			if responseBody != "" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(responseBody))
			} else {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			}
			return
		}
		// Allow other paths through (e.g. push during WriteChunk test)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	return srv, &captured, &once
}

func TestRemoteTransport_ReadManifest_CarriesExtraHeaders(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_EXTRA_HEADERS", "X-Tenant-ID: acme")

	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":1,"chunks":[]}`))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	_, err = rt.ReadManifest()
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	if capturedHeaders.Get("X-Tenant-Id") != "acme" {
		t.Fatalf("expected X-Tenant-Id=acme on request, got %q", capturedHeaders.Get("X-Tenant-Id"))
	}
}

func TestRemoteTransport_WriteChunk_CarriesExtraHeaders(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_EXTRA_HEADERS", "X-Tenant-ID: acme")

	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	err = rt.WriteChunk("", []byte(`{"sessions":[]}`), engramsync.ChunkEntry{CreatedBy: "test", CreatedAt: "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}

	if capturedHeaders.Get("X-Tenant-Id") != "acme" {
		t.Fatalf("expected X-Tenant-Id=acme on POST request, got %q", capturedHeaders.Get("X-Tenant-Id"))
	}
}

func TestRemoteTransport_ReadChunk_CarriesExtraHeaders(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_EXTRA_HEADERS", "X-Tenant-ID: acme")

	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		_, _ = w.Write([]byte(`{"sessions":[]}`))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	_, err = rt.ReadChunk("chunk-1")
	if err != nil {
		t.Fatalf("ReadChunk: %v", err)
	}

	if capturedHeaders.Get("X-Tenant-Id") != "acme" {
		t.Fatalf("expected X-Tenant-Id=acme on GET chunk request, got %q", capturedHeaders.Get("X-Tenant-Id"))
	}
}

// TestRemoteTransport_AuthorizationNotOverwritten verifies that even if the env contains
// "authorization: fake", the real Bearer token from ENGRAM_CLOUD_TOKEN wins.
func TestRemoteTransport_AuthorizationNotOverwritten(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_EXTRA_HEADERS", "authorization: fake")

	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":1,"chunks":[]}`))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "real-token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	_, err = rt.ReadManifest()
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	auth := capturedHeaders.Get("Authorization")
	if auth != "Bearer real-token" {
		t.Fatalf("expected Authorization=Bearer real-token, got %q", auth)
	}
}

// ─── HTTP header propagation: MutationTransport ──────────────────────────────

func TestMutationTransport_PushMutations_CarriesExtraHeaders(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_EXTRA_HEADERS", "X-Tenant-ID: acme")

	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted_seqs":[1]}`))
	}))
	defer srv.Close()

	mt, err := NewMutationTransport(srv.URL, "token")
	if err != nil {
		t.Fatalf("NewMutationTransport: %v", err)
	}

	entries := []MutationEntry{
		{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
	}
	_, err = mt.PushMutations(entries)
	if err != nil {
		t.Fatalf("PushMutations: %v", err)
	}

	if capturedHeaders.Get("X-Tenant-Id") != "acme" {
		t.Fatalf("expected X-Tenant-Id=acme on push request, got %q", capturedHeaders.Get("X-Tenant-Id"))
	}
}

func TestMutationTransport_PullMutations_CarriesExtraHeaders(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_EXTRA_HEADERS", "X-Tenant-ID: acme")

	var capturedHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"mutations":[],"has_more":false,"latest_seq":0}`))
	}))
	defer srv.Close()

	mt, err := NewMutationTransport(srv.URL, "token")
	if err != nil {
		t.Fatalf("NewMutationTransport: %v", err)
	}

	_, err = mt.PullMutations(0, 10)
	if err != nil {
		t.Fatalf("PullMutations: %v", err)
	}

	if capturedHeaders.Get("X-Tenant-Id") != "acme" {
		t.Fatalf("expected X-Tenant-Id=acme on pull request, got %q", capturedHeaders.Get("X-Tenant-Id"))
	}
}
