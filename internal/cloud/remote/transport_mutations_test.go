package remote

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// mustNewMutationTransport is a test helper that panics on error.
func mustNewMutationTransport(t *testing.T, baseURL, token string) *MutationTransport {
	t.Helper()
	mt, err := NewMutationTransport(baseURL, token)
	if err != nil {
		t.Fatalf("NewMutationTransport(%q): %v", baseURL, err)
	}
	return mt
}

// TestMutationTransportPushAccepted verifies REQ-200: valid push returns accepted_seqs.
func TestMutationTransportPushAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sync/mutations/push" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted_seqs":[1,2,3]}`))
	}))
	defer srv.Close()

	mt := mustNewMutationTransport(t, srv.URL, "")
	entries := []MutationEntry{
		{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert", Payload: json.RawMessage(`{}`)},
		{Project: "proj-a", Entity: "obs", EntityKey: "k2", Op: "upsert", Payload: json.RawMessage(`{}`)},
		{Project: "proj-a", Entity: "obs", EntityKey: "k3", Op: "upsert", Payload: json.RawMessage(`{}`)},
	}
	seqs, err := mt.PushMutations(entries)
	if err != nil {
		t.Fatalf("PushMutations: %v", err)
	}
	if len(seqs) != 3 {
		t.Fatalf("expected 3 accepted_seqs, got %d", len(seqs))
	}
}

// TestMutationTransportPushUnauth verifies REQ-200: 401 → HTTPStatusError.IsAuthFailure.
func TestMutationTransportPushUnauth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	mt := mustNewMutationTransport(t, srv.URL, "")
	_, err := mt.PushMutations([]MutationEntry{{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert"}})
	if err == nil {
		t.Fatal("expected error")
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected HTTPStatusError, got %T", err)
	}
	if !statusErr.IsAuthFailure() {
		t.Fatalf("expected IsAuthFailure, got status=%d", statusErr.StatusCode)
	}
}

// TestMutationTransportPullSinceSeq verifies REQ-201: pull returns mutations + has_more + latest_seq.
func TestMutationTransportPullSinceSeq(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sync/mutations/pull" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		sinceSeq := r.URL.Query().Get("since_seq")
		limit := r.URL.Query().Get("limit")
		if sinceSeq == "" || limit == "" {
			http.Error(w, "missing params", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"mutations":[{"seq":6,"project":"project-a","entity":"obs","entity_key":"k6","op":"upsert","payload":{}}],"has_more":false,"latest_seq":10}`))
	}))
	defer srv.Close()

	mt := mustNewMutationTransport(t, srv.URL, "")
	resp, err := mt.PullMutations(5, 100)
	if err != nil {
		t.Fatalf("PullMutations: %v", err)
	}
	if len(resp.Mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(resp.Mutations))
	}
	if resp.HasMore {
		t.Fatal("expected has_more=false")
	}
	if resp.LatestSeq != 10 {
		t.Fatalf("expected latest_seq=10, got %d", resp.LatestSeq)
	}
	if resp.Mutations[0].Project != "project-a" {
		t.Fatalf("expected mutation project project-a, got %q", resp.Mutations[0].Project)
	}
}

// TestMutationTransportPullUnauth verifies REQ-201: 401 → error.
func TestMutationTransportPullUnauth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	mt := mustNewMutationTransport(t, srv.URL, "")
	_, err := mt.PullMutations(0, 100)
	if err == nil {
		t.Fatal("expected error")
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected HTTPStatusError, got %T", err)
	}
	if !statusErr.IsAuthFailure() {
		t.Fatalf("expected IsAuthFailure, got status=%d", statusErr.StatusCode)
	}
}

// TestMutationTransportPush404ServerUnsupported verifies REQ-214: 404 → reason_code=server_unsupported.
func TestMutationTransportPush404ServerUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	mt := mustNewMutationTransport(t, srv.URL, "")
	_, err := mt.PushMutations([]MutationEntry{{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert"}})
	if err == nil {
		t.Fatal("expected error")
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected HTTPStatusError, got %T", err)
	}
	if statusErr.ErrorCode != "server_unsupported" {
		t.Fatalf("expected error_code=server_unsupported, got %q", statusErr.ErrorCode)
	}
}

// TestMutationTransportPull404ServerUnsupported verifies REQ-214: pull 404 → reason_code=server_unsupported.
func TestMutationTransportPull404ServerUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	mt := mustNewMutationTransport(t, srv.URL, "")
	_, err := mt.PullMutations(0, 100)
	if err == nil {
		t.Fatal("expected error")
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected HTTPStatusError, got %T", err)
	}
	if statusErr.ErrorCode != "server_unsupported" {
		t.Fatalf("expected error_code=server_unsupported, got %q", statusErr.ErrorCode)
	}
}

// TestMutationTransportPush401VsNotFound verifies REQ-214: 401 → auth_required, not server_unsupported.
func TestMutationTransportPush401VsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	mt := mustNewMutationTransport(t, srv.URL, "")
	_, err := mt.PushMutations([]MutationEntry{{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert"}})
	if err == nil {
		t.Fatal("expected error")
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected HTTPStatusError, got %T", err)
	}
	if statusErr.ErrorCode == "server_unsupported" {
		t.Fatal("401 must not map to server_unsupported")
	}
	if !statusErr.IsAuthFailure() {
		t.Fatalf("expected IsAuthFailure for 401, got status=%d code=%q", statusErr.StatusCode, statusErr.ErrorCode)
	}
}

// ─── BW6: NewMutationTransport URL validation ──────────────────────────────

// TestNewMutationTransportRejectsInvalidURL verifies BW6:
// NewMutationTransport must reject empty or malformed URLs.
func TestNewMutationTransportRejectsInvalidURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{name: "empty", url: ""},
		{name: "no-scheme", url: "example.com/sync"},
		{name: "invalid-scheme", url: "ftp://example.com"},
		{name: "no-host", url: "http://"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mt, err := NewMutationTransport(tc.url, "token")
			if err == nil {
				t.Fatalf("expected error for URL %q, got nil (transport=%v)", tc.url, mt)
			}
		})
	}
}

func TestNewMutationTransportBearerHTTPSPolicy(t *testing.T) {
	t.Run("rejects authenticated HTTP", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			token string
		}{
			{name: "plain token", token: "token"},
			{name: "padded token", token: "  token\t"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				transport, err := NewMutationTransport("http://127.0.0.1:8080", tc.token)
				if err == nil || transport != nil {
					t.Fatalf("NewMutationTransport authenticated HTTP = (%v, %v), want nil transport and error", transport, err)
				}
			})
		}
	})

	t.Run("preserves tokenless HTTP", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			token string
		}{
			{name: "empty token", token: ""},
			{name: "whitespace-only token", token: " \t\n "},
		} {
			t.Run(tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "" {
						t.Errorf("tokenless request authorization = %q, want empty", r.Header.Get("Authorization"))
					}
					_, _ = w.Write([]byte(`{"accepted_seqs":[1]}`))
				}))
				defer server.Close()

				transport := mustNewMutationTransport(t, server.URL, tc.token)
				seqs, err := transport.PushMutations([]MutationEntry{{Project: "project-a", Entity: "obs", EntityKey: "key", Op: "upsert"}})
				if err != nil || !reflect.DeepEqual(seqs, []int64{1}) {
					t.Fatalf("tokenless HTTP push = (%v, %v), want ([1], nil)", seqs, err)
				}
			})
		}
	})

	t.Run("allows authenticated HTTPS", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer token" {
				t.Errorf("authorization = %q, want bearer token", r.Header.Get("Authorization"))
			}
			switch r.URL.Path {
			case "/sync/mutations/push":
				if r.Method != http.MethodPost {
					t.Errorf("push method = %s, want POST", r.Method)
				}
				_, _ = w.Write([]byte(`{"accepted_seqs":[1]}`))
			case "/sync/mutations/pull":
				if r.Method != http.MethodGet {
					t.Errorf("pull method = %s, want GET", r.Method)
				}
				_, _ = w.Write([]byte(`{"mutations":[],"has_more":false,"latest_seq":1}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		transport := mustNewMutationTransport(t, server.URL, "token")
		transport.httpClient.Transport = server.Client().Transport
		if _, err := transport.PushMutations([]MutationEntry{{Project: "project-a", Entity: "obs", EntityKey: "key", Op: "upsert"}}); err != nil {
			t.Fatalf("authenticated HTTPS push: %v", err)
		}
		if _, err := transport.PullMutations(0, 100); err != nil {
			t.Fatalf("authenticated HTTPS pull: %v", err)
		}
	})

	t.Run("allows authenticated HTTPS redirects", func(t *testing.T) {
		type requestDetails struct {
			method        string
			authorization string
		}
		targetRequests := make(chan requestDetails, 1)
		targetServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			targetRequests <- requestDetails{method: r.Method, authorization: r.Header.Get("Authorization")}
			_, _ = w.Write([]byte(`{"accepted_seqs":[1]}`))
		}))
		defer targetServer.Close()
		sourceServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, targetServer.URL+r.URL.Path, http.StatusTemporaryRedirect)
		}))
		defer sourceServer.Close()

		transport := mustNewMutationTransport(t, sourceServer.URL, "token")
		transport.httpClient.Transport = sourceServer.Client().Transport
		if _, err := transport.PushMutations([]MutationEntry{{Project: "project-a", Entity: "obs", EntityKey: "key", Op: "upsert"}}); err != nil {
			t.Fatalf("authenticated HTTPS redirect push: %v", err)
		}
		got := <-targetRequests
		if got.method != http.MethodPost || got.authorization != "Bearer token" {
			t.Fatalf("HTTPS redirect request = method %q authorization %q, want POST with bearer token", got.method, got.authorization)
		}
	})

	t.Run("rejects HTTPS downgrade redirects", func(t *testing.T) {
		downgraded := false
		downgradeServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			downgraded = true
		}))
		defer downgradeServer.Close()
		secureServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, downgradeServer.URL, http.StatusFound)
		}))
		defer secureServer.Close()

		transport := mustNewMutationTransport(t, secureServer.URL, "token")
		transport.httpClient.Transport = secureServer.Client().Transport
		_, err := transport.PushMutations([]MutationEntry{{Project: "project-a", Entity: "obs", EntityKey: "key", Op: "upsert"}})
		if err == nil || !strings.Contains(err.Error(), "bearer token redirect requires HTTPS") {
			t.Fatalf("HTTPS downgrade redirect error = %v, want bearer HTTPS redirect error", err)
		}
		if downgraded {
			t.Fatal("HTTPS downgrade redirect reached the HTTP server")
		}
	})
}

// ─── BC3: 404 warning log ─────────────────────────────────────────────────────

// TestTransport404LogsServerUnsupportedWarning verifies BC3 / REQ-214:
// When the server returns 404, newMutationHTTPStatusError must emit a log warning
// containing "server_unsupported" and advice to deploy the server.
func TestTransport404LogsServerUnsupportedWarning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	// Capture log output — capture original writer so we restore it exactly.
	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(orig) // restore to original (never nil — prevents process-wide log corruption)

	mt, err := NewMutationTransport(srv.URL, "")
	if err != nil {
		t.Fatalf("NewMutationTransport: %v", err)
	}
	_, _ = mt.PushMutations([]MutationEntry{{Project: "proj-a", Entity: "obs", EntityKey: "k1", Op: "upsert"}})

	logOutput := buf.String()
	if !strings.Contains(logOutput, "server_unsupported") {
		t.Fatalf("expected log to contain 'server_unsupported', got: %q", logOutput)
	}
}
