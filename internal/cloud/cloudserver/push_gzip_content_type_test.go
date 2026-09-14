package cloudserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// buildGzipPushBody returns a valid gzip-compressed /sync/push body for
// project "proj-a" plus the chunk ID the server must compute for it.
func buildGzipPushBody(t *testing.T) ([]byte, string) {
	t.Helper()
	payload := []byte(`{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}`)
	normalized, err := coerceChunkProject(payload, "proj-a")
	if err != nil {
		t.Fatalf("coerceChunkProject: %v", err)
	}
	chunkID := chunkIDFromPayload(normalized)
	reqBody := map[string]any{
		"chunk_id":          chunkID,
		"created_by":        "tester",
		"client_created_at": "2026-01-01T00:00:00Z",
		"project":           "proj-a",
		"data":              json.RawMessage(payload),
	}
	encoded, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal push request: %v", err)
	}
	return mustEncodeCompressedEnvelope(t, encoded), chunkID
}

// TestHandlerPushGzipBodyWithMismatchedContentTypeIsAccepted locks the
// server-side recovery for the reported production failure: a proxy drops or
// rewrites the request Content-Type, the gzip-compressed push body arrives
// with a non-envelope Content-Type, and the server must still decode it by
// sniffing the gzip magic bytes instead of failing with
// "invalid character '\x1f' looking for beginning of value".
func TestHandlerPushGzipBodyWithMismatchedContentTypeIsAccepted(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		setHeader   bool
	}{
		{name: "rewritten to application/json", contentType: "application/json", setHeader: true},
		{name: "dropped entirely", contentType: "", setHeader: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{}
			srv := New(st, fakeAuth{}, 0)
			wireBody, wantChunkID := buildGzipPushBody(t)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/sync/push", bytes.NewReader(wireBody))
			if tt.setHeader {
				req.Header.Set("Content-Type", tt.contentType)
			}
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d body=%q", rec.Code, http.StatusOK, rec.Body.String())
			}
			if got := st.chunks[wantChunkID]; len(got) == 0 {
				t.Fatalf("chunk %q was not stored; store=%v", wantChunkID, st.chunks)
			}
		})
	}
}

// TestHandlerPushLegacyJSONWithApplicationJSONContentTypeStillAccepted proves
// the gzip-sniff recovery does not disturb the legacy plain-JSON push path.
func TestHandlerPushLegacyJSONWithApplicationJSONContentTypeStillAccepted(t *testing.T) {
	st := &fakeStore{}
	srv := New(st, fakeAuth{}, 0)

	payload := []byte(`{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}`)
	normalized, err := coerceChunkProject(payload, "proj-a")
	if err != nil {
		t.Fatalf("coerceChunkProject: %v", err)
	}
	chunkID := chunkIDFromPayload(normalized)
	reqBody := map[string]any{
		"chunk_id":          chunkID,
		"created_by":        "tester",
		"client_created_at": "2026-01-01T00:00:00Z",
		"project":           "proj-a",
		"data":              json.RawMessage(payload),
	}
	encoded, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal push request: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/push", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := st.chunks[chunkID]; len(got) == 0 {
		t.Fatalf("chunk %q was not stored; store=%v", chunkID, st.chunks)
	}
}

// TestHandlerPushGzipBodyWithMismatchedContentTypeStillEnforcesLimits proves
// the sniff path keeps every body-size guard: a gzip body over the wire limit
// is still rejected by http.MaxBytesReader (413), and a gzip body whose
// decoded size exceeds the limit is still rejected via the chunkcodec limit.
func TestHandlerPushGzipBodyWithMismatchedContentTypeStillEnforcesLimits(t *testing.T) {
	const wireLimit = int64(128)
	const lowCompressibilityAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	fixtureData := make([]byte, 384)
	state := uint32(1)
	for i := range fixtureData {
		state = state*1664525 + 1013904223
		fixtureData[i] = lowCompressibilityAlphabet[(state>>24)%uint32(len(lowCompressibilityAlphabet))]
	}
	wireBody := mustEncodeCompressedEnvelope(t, []byte(`{"project":"proj-a","data":"`+string(fixtureData)+`"}`))
	if int64(len(wireBody)) <= wireLimit {
		t.Fatalf("compressed wire fixture = %d bytes, must exceed configured wire limit %d", len(wireBody), wireLimit)
	}

	tests := []struct {
		name       string
		body       []byte
		wantStatus int
	}{
		{
			name:       "wire body over MaxBytesReader limit",
			body:       wireBody,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "decoded body over limit",
			body:       mustEncodeCompressedEnvelope(t, []byte(`{"project":"proj-a","data":"`+strings.Repeat("a", 512)+`"}`)),
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(&fakeStore{}, fakeAuth{}, 0, WithMaxPushBodyBytes(wireLimit))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/sync/push", bytes.NewReader(tt.body))
			// The Content-Type does not advertise the envelope: the sniff path
			// must still preserve the oversized-payload rejection.
			req.Header.Set("Content-Type", "application/json")
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d body=%q", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestHandlerPushSniffedGzipBodyWithCorruptPayloadStillRejected proves the
// sniff path never panics and maps undecodable gzip bodies to the standard
// 400 invalid push payload error.
func TestHandlerPushSniffedGzipBodyWithCorruptPayloadStillRejected(t *testing.T) {
	validBody, _ := buildGzipPushBody(t)

	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "truncated gzip body",
			body: validBody[:len(validBody)/2],
		},
		{
			name: "gzip magic followed by garbage",
			body: []byte{0x1f, 0x8b, 'g', 'a', 'r', 'b', 'a', 'g', 'e'},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(&fakeStore{}, fakeAuth{}, 0)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/sync/push", bytes.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d body=%q", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "invalid push payload") {
				t.Fatalf("expected invalid push payload error, got %q", rec.Body.String())
			}
			// The decode error must report the Content-Type the server actually
			// received, so operators can tell a rewritten header apart from a
			// stale server binary. The response body is JSON-encoded, so the
			// quoted header value appears escaped inside the error string.
			if !strings.Contains(rec.Body.String(), `(content-type: \"application/json\")`) {
				t.Fatalf("expected response to report the received Content-Type, got %q", rec.Body.String())
			}
		})
	}
}
