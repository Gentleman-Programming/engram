package remote

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/chunkcodec"
	engramsync "github.com/Gentleman-Programming/engram/v2/internal/sync"
)

type remoteRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f remoteRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewRemoteTransportUsesOperationSpecificTimeouts(t *testing.T) {
	rt, err := NewRemoteTransport("https://cloud.example.test", "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	if ordinaryOperationTimeout != 30*time.Second {
		t.Fatalf("ordinary operation timeout = %s, want 30s", ordinaryOperationTimeout)
	}
	if writeChunkTimeout != 5*time.Minute {
		t.Fatalf("write chunk timeout = %s, want 5m", writeChunkTimeout)
	}
	if rt.httpClient.Timeout != ordinaryOperationTimeout {
		t.Fatalf("ordinary client timeout = %s, want %s", rt.httpClient.Timeout, ordinaryOperationTimeout)
	}
	if rt.writeHTTPClient.Timeout != writeChunkTimeout {
		t.Fatalf("write client timeout = %s, want %s", rt.writeHTTPClient.Timeout, writeChunkTimeout)
	}
	if rt.httpClient == rt.writeHTTPClient {
		t.Fatal("ordinary and write operations must use distinct HTTP clients")
	}
}

func TestRemoteTransportUsesDedicatedWriteClient(t *testing.T) {
	var ordinaryRequests, writeRequests int
	rt := &RemoteTransport{
		baseURL: "https://cloud.example.test",
		project: "proj-a",
		httpClient: &http.Client{
			Timeout: ordinaryOperationTimeout,
			Transport: remoteRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				ordinaryRequests++
				if req.Method != http.MethodGet {
					return nil, errors.New("ordinary client used for write")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"version":1,"chunks":[]}`)),
					Header:     make(http.Header),
				}, nil
			}),
		},
		writeHTTPClient: &http.Client{
			Timeout: writeChunkTimeout,
			Transport: remoteRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				writeRequests++
				if req.Method != http.MethodPost {
					return nil, errors.New("write client used for read")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	if _, err := rt.ReadManifest(); err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if err := rt.WriteChunk("chunk-1", []byte(`{"sessions":[]}`), engramsync.ChunkEntry{CreatedBy: "tester", CreatedAt: "2026-04-01T00:00:00Z"}); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	if ordinaryRequests != 1 {
		t.Fatalf("ordinary client requests = %d, want 1", ordinaryRequests)
	}
	if writeRequests != 1 {
		t.Fatalf("write client requests = %d, want 1", writeRequests)
	}
}

func TestReadManifestReturnsHTTPStatusErrorForAuthAndPolicyFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized},
		{name: "forbidden", statusCode: http.StatusForbidden},
		{name: "server error", statusCode: http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "failed", tc.statusCode)
			}))
			defer srv.Close()

			rt, err := NewRemoteTransport(srv.URL, "token", "proj-a")
			if err != nil {
				t.Fatalf("NewRemoteTransport: %v", err)
			}

			_, err = rt.ReadManifest()
			if err == nil {
				t.Fatal("expected ReadManifest error")
			}

			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("expected HTTPStatusError, got %T (%v)", err, err)
			}
			if statusErr.StatusCode != tc.statusCode {
				t.Fatalf("expected status %d, got %d", tc.statusCode, statusErr.StatusCode)
			}
		})
	}
}

func TestReadManifestParsesMachineActionableErrorPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error_class":"repairable","error_code":"upgrade_repairable_payload_invalid","error":"invalid push payload: sessions[0].directory is required"}`))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	_, err = rt.ReadManifest()
	if err == nil {
		t.Fatal("expected ReadManifest error")
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected HTTPStatusError, got %T (%v)", err, err)
	}
	if statusErr.ErrorClass != "repairable" {
		t.Fatalf("expected repairable class, got %q", statusErr.ErrorClass)
	}
	if statusErr.ErrorCode != "upgrade_repairable_payload_invalid" {
		t.Fatalf("expected actionable error code, got %q", statusErr.ErrorCode)
	}
	if !statusErr.IsRepairableMigrationFailure() {
		t.Fatalf("expected IsRepairableMigrationFailure=true, got false")
	}
	if !statusErr.IsRepairable() {
		t.Fatalf("expected IsRepairable=true, got false")
	}
	if !strings.Contains(statusErr.Error(), "sessions[0].directory is required") {
		t.Fatalf("expected error message to preserve actionable detail, got %q", statusErr.Error())
	}
}

func TestWriteChunkCanonicalizesPayloadAndChunkID(t *testing.T) {
	var gotChunkID string
	var gotClientCreatedAt string
	var gotData json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sync/push" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var req struct {
			ChunkID         string          `json:"chunk_id"`
			ClientCreatedAt string          `json:"client_created_at"`
			Data            json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		gotChunkID = req.ChunkID
		gotClientCreatedAt = req.ClientCreatedAt
		gotData = append([]byte(nil), req.Data...)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	originalPayload := []byte(`{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}`)
	createdAt := "2026-04-01T12:30:00Z"
	if err := rt.WriteChunk("deadbeef", originalPayload, engramsync.ChunkEntry{CreatedBy: "tester", CreatedAt: createdAt}); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}

	canonicalPayload, err := chunkcodec.CanonicalizeForProject(originalPayload, "proj-a")
	if err != nil {
		t.Fatalf("CanonicalizeForProject: %v", err)
	}
	wantChunkID := chunkcodec.ChunkID(canonicalPayload)

	if gotChunkID != wantChunkID {
		t.Fatalf("expected canonical chunk_id %q, got %q", wantChunkID, gotChunkID)
	}
	if gotClientCreatedAt != createdAt {
		t.Fatalf("expected client_created_at %q, got %q", createdAt, gotClientCreatedAt)
	}
	if strings.TrimSpace(string(gotData)) != strings.TrimSpace(string(canonicalPayload)) {
		t.Fatalf("expected canonical payload %s, got %s", string(canonicalPayload), string(gotData))
	}
}

func TestNewRemoteTransportRejectsBaseURLWithQueryOrFragment(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr string
	}{
		{name: "query", baseURL: "https://cloud.example.test/api?debug=1", wantErr: "query is not allowed"},
		{name: "fragment", baseURL: "https://cloud.example.test/api#frag", wantErr: "fragment is not allowed"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRemoteTransport(tc.baseURL, "token", "proj-a")
			if err == nil {
				t.Fatalf("expected error for %s", tc.baseURL)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestRemoteTransportBuildsRequestURLsFromBasePath(t *testing.T) {
	requestPaths := make([]string, 0, 3)
	requestProjects := make([]string, 0, 2)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sync/pull" && r.URL.Query().Get("project") == "proj-a":
			requestProjects = append(requestProjects, r.URL.Query().Get("project"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":1,"chunks":[]}`))
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sync/pull/chunk-1" && r.URL.Query().Get("project") == "proj-a":
			requestProjects = append(requestProjects, r.URL.Query().Get("project"))
			_, _ = w.Write([]byte(`{"sessions":[]}`))
			return
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sync/push":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL+"/api/v1", "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	if _, err := rt.ReadManifest(); err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if _, err := rt.ReadChunk("chunk-1"); err != nil {
		t.Fatalf("ReadChunk: %v", err)
	}
	if err := rt.WriteChunk("chunk-1", []byte(`{"sessions":[]}`), engramsync.ChunkEntry{CreatedBy: "tester", CreatedAt: "2026-04-01T00:00:00Z"}); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}

	if len(requestPaths) != 3 {
		t.Fatalf("expected 3 requests, got %d (%v)", len(requestPaths), requestPaths)
	}
	if requestPaths[0] != "/api/v1/sync/pull" || requestPaths[1] != "/api/v1/sync/pull/chunk-1" || requestPaths[2] != "/api/v1/sync/push" {
		t.Fatalf("unexpected request paths: %v", requestPaths)
	}
	if len(requestProjects) != 2 || requestProjects[0] != "proj-a" || requestProjects[1] != "proj-a" {
		t.Fatalf("expected project query on pull endpoints, got %v", requestProjects)
	}
}
