package remote

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMutationProvenanceRequests(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		call             func(*MutationTransport) error
	}{
		{"register", "/sync/session-authorities", `{"session_id":"session","project":"owner"}`, func(mt *MutationTransport) error { return mt.RegisterSessionAuthority("session", "owner") }},
		{"claim", "/sync/prompt-pair-claims", `{"session_id":"session","source_inbox_id":"inbox","sync_id":"sync","owner_project":"owner","project":"prompt"}`, func(mt *MutationTransport) error {
			return mt.ClaimPromptPair("session", "inbox", "sync", "owner", "prompt")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPost || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" || string(data) != tc.body {
					t.Errorf("request method=%s path=%s auth=%q content=%q body=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), data)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			}))
			defer server.Close()
			mt, err := NewMutationTransport(server.URL, "secret")
			if err != nil {
				t.Fatal(err)
			}
			mt.httpClient = server.Client()
			if err := tc.call(mt); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMutationProvenanceStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
	}{
		{"unauthorized", 401, `{"error":"denied"}`, ""}, {"forbidden", 403, `{"error":"denied"}`, ""},
		{"conflict", 409, `{"error":"conflict"}`, ""}, {"oversize", 413, `{"error":"too large"}`, ""},
		{"authority unavailable", 404, `{"error":"session authority unavailable","error_code":"session_authority_unavailable"}`, "session_authority_unavailable"},
		{"old server", 404, "404 page not found", "server_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			mt, err := NewMutationTransport(server.URL, "")
			if err != nil {
				t.Fatal(err)
			}
			for i, call := range []func() error{func() error { return mt.RegisterSessionAuthority("s", "owner") }, func() error { return mt.ClaimPromptPair("s", "i", "y", "owner", "prompt") }} {
				var status *HTTPStatusError
				wantCode := tc.code
				if i == 0 && tc.status == 404 {
					wantCode = "server_unsupported"
				}
				if err := call(); !errors.As(err, &status) || status.StatusCode != tc.status || status.ErrorCode != wantCode {
					t.Fatalf("error=%v status=%+v", err, status)
				}
			}
		})
	}
}

func TestMutationProvenanceRejectsUnverifiedSuccess(t *testing.T) {
	for _, body := range []string{"", `{"status":"pending"}`, `<html>login</html>`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			mt := mustNewMutationTransport(t, server.URL, "")
			if err := mt.ClaimPromptPair("s", "i", "y", "owner", "prompt"); err == nil {
				t.Fatal("accepted response without confirmed claim")
			}
		})
	}
}

func TestAttestPromptSource(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/sync/prompt-source-attestations" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" || string(body) != `{"session_id":"s","source_inbox_id":"i","sync_id":"y","owner_project":"owner","prompt_project":"prompt"}` {
			t.Errorf("unexpected attestation request: method=%s path=%s body=%q", r.Method, r.URL.Path, body)
		}
		_, _ = w.Write([]byte(`{"status":"ok","attestation_id":42}`))
	}))
	defer server.Close()
	mt := mustNewMutationTransport(t, server.URL, "secret")
	mt.httpClient = server.Client()
	id, err := mt.AttestPromptSource("s", "i", "y", "owner", "prompt")
	if err != nil || id != 42 {
		t.Fatalf("id=%d err=%v", id, err)
	}
}

func TestAttestPromptSourceRejectsInvalidResponse(t *testing.T) {
	for _, body := range []string{"", `{}`, `{"status":"ok"}`, `{"status":"pending","attestation_id":1}`, `{"status":"ok","attestation_id":0}`, `{"status":"ok","attestation_id":-1}`, `{"status":"ok","attestation_id":"1"}`, `{"status":"ok","attestation_id":1} {}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			mt := mustNewMutationTransport(t, server.URL, "")
			if id, err := mt.AttestPromptSource("s", "i", "y", "owner", "prompt"); err == nil || id != 0 {
				t.Fatalf("id=%d err=%v", id, err)
			}
		})
	}
}

func TestAttestPromptSourceStatusRedactsToken(t *testing.T) {
	for _, code := range []int{401, 403, 404, 409} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"secret","error_code":"secret"}`))
		}))
		mt := mustNewMutationTransport(t, server.URL, "secret")
		mt.httpClient = server.Client()
		_, err := mt.AttestPromptSource("s", "i", "y", "owner", "prompt")
		var status *HTTPStatusError
		if !errors.As(err, &status) || status.StatusCode != code || strings.Contains(err.Error(), "secret") || strings.Contains(status.Body, "secret") {
			t.Errorf("code=%d status=%+v err=%v", code, status, err)
		}
		server.Close()
	}
}

func TestAttestPromptSourceInvalidInputAndOversizedResponse(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"status":"ok","attestation_id":1,"padding":"` + strings.Repeat("x", 65536) + `"}`))
	}))
	defer server.Close()
	mt := mustNewMutationTransport(t, server.URL, "")
	if _, err := mt.AttestPromptSource(" ", "i", "y", "owner", "prompt"); err == nil || requests != 0 {
		t.Fatalf("invalid input sent request: requests=%d err=%v", requests, err)
	}
	if _, err := mt.AttestPromptSource("s", "i", "y", "owner", "prompt"); err == nil || requests != 1 {
		t.Fatalf("oversized response accepted: requests=%d err=%v", requests, err)
	}
}

func TestAttestPromptSourceNetworkFailureRedactsToken(t *testing.T) {
	mt := mustNewMutationTransport(t, "https://cloud.example.test", "secret")
	mt.httpClient = &http.Client{Transport: remoteRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network failure with bearer secret")
	})}
	id, err := mt.AttestPromptSource("s", "i", "y", "owner", "prompt")
	if err == nil || id != 0 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("network error leaked bearer token: %v", err)
	}
}

func TestMutationProvenanceNetworkFailure(t *testing.T) {
	mt, err := NewMutationTransport("http://cloud.example.test", "")
	if err != nil {
		t.Fatal(err)
	}
	mt.httpClient = &http.Client{Transport: remoteRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}
	if err := mt.ClaimPromptPair("s", "i", "y", "owner", "prompt"); err == nil {
		t.Fatal("network failure accepted")
	}
	if _, err := NewMutationTransport("http://example.com", "secret"); err == nil {
		t.Fatal("insecure bearer accepted")
	}
}
