package cloudserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
)

// This file locks down the /sync/* route table and wire schemas as they existed
// before the cloud-user-token-management change (PR1-PR4 replaced the auth
// internals behind these routes). It exists to catch accidental contract drift
// introduced by the principal-resolution refactor — paths, HTTP methods,
// request field names, and response field names for existing sync clients
// MUST NOT change for this MVP (design.md "Sync Contract Preservation").

// syncRouteSpec pins one registered /sync/* route to its exact method+path.
type syncRouteSpec struct {
	method string
	path   string
}

var expectedSyncRoutes = []syncRouteSpec{
	{http.MethodGet, "/sync/pull?project=proj-a"},
	{http.MethodGet, "/sync/pull/chunk-id?project=proj-a"},
	{http.MethodPost, "/sync/push"},
	{http.MethodPost, "/sync/mutations/push"},
	{http.MethodGet, "/sync/mutations/pull"},
}

// TestSyncRouteTableUnchanged proves every pre-existing /sync/* route is still
// registered at its original method+path and still requires bearer auth (401,
// not 404/405), and that using the wrong HTTP verb on each path still returns
// 405 rather than silently changing to a new path shape.
func TestSyncRouteTableUnchanged(t *testing.T) {
	srv := New(&fakeStore{}, fakeAuth{err: errors.New("unauthorized fixture")}, 0)

	for _, route := range expectedSyncRoutes {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(route.method, route.path, nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("route %s %s: expected 401 (unauthorized, route still exists), got %d body=%q", route.method, route.path, rec.Code, rec.Body.String())
		}

		wrongMethod := http.MethodPost
		if route.method == http.MethodPost {
			wrongMethod = http.MethodGet
		}
		wrongRec := httptest.NewRecorder()
		wrongReq := httptest.NewRequest(wrongMethod, route.path, nil)
		srv.Handler().ServeHTTP(wrongRec, wrongReq)
		if wrongRec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("route %s %s: expected 405 for wrong method %s, got %d", route.method, route.path, wrongMethod, wrongRec.Code)
		}
	}
}

// TestSyncPullManifestResponseSchemaUnchanged proves GET /sync/pull still
// returns exactly the {version, chunks:[{id, created_by, created_at, sessions,
// memories, prompts}]} shape existing clients decode.
func TestSyncPullManifestResponseSchemaUnchanged(t *testing.T) {
	st := &fakeStore{manifest: engramsync.Manifest{
		Version: 1,
		Chunks: []engramsync.ChunkEntry{
			{ID: "chunk-1", CreatedBy: "tester", CreatedAt: "2026-01-01T00:00:00Z", Sessions: 1, Memories: 2, Prompts: 3},
		},
	}}
	srv := New(st, fakeAuth{}, 0)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sync/pull?project=proj-a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode manifest top-level: %v", err)
	}
	assertExactKeys(t, "manifest", top, "version", "chunks")

	var chunks []map[string]json.RawMessage
	if err := json.Unmarshal(top["chunks"], &chunks); err != nil {
		t.Fatalf("decode manifest chunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	assertExactKeys(t, "manifest chunk", chunks[0], "id", "created_by", "created_at", "sessions", "memories", "prompts")
}

// TestSyncPushChunkRequestAndResponseSchemaUnchanged proves POST /sync/push
// still accepts the {chunk_id, created_by, client_created_at, project, data}
// request shape and still replies with exactly {status, chunk_id}.
func TestSyncPushChunkRequestAndResponseSchemaUnchanged(t *testing.T) {
	st := &fakeStore{}
	srv := New(st, fakeAuth{}, 0)

	payload := []byte(`{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}`)
	normalized, err := coerceChunkProject(payload, "proj-a")
	if err != nil {
		t.Fatalf("coerce payload: %v", err)
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
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sync/push", bytes.NewReader(encoded)))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode push response: %v", err)
	}
	assertExactKeys(t, "push response", resp, "status", "chunk_id")
}

// TestSyncPullChunkResponseSchemaUnchanged proves GET /sync/pull/{chunkID}
// still replies with exactly `Content-Type: application/json` and the raw
// stored chunk bytes verbatim (no wrapping/enveloping), for an authenticated
// request. This is a REVIEW REMEDIATION addition (FIX 4): the manifest,
// push, and mutation schemas were already locked above, but the pull-chunk
// response itself — content-type plus exact body shape — was not, so a
// future accidental wrap/envelope change would not have been caught here.
func TestSyncPullChunkResponseSchemaUnchanged(t *testing.T) {
	st := &fakeStore{}
	srv := New(st, fakeAuth{}, 0)

	payload := []byte(`{"sessions":[{"id":"s-1","directory":"/tmp/s-1"}]}`)
	normalized, err := coerceChunkProject(payload, "proj-a")
	if err != nil {
		t.Fatalf("coerce payload: %v", err)
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

	pushRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(pushRec, httptest.NewRequest(http.MethodPost, "/sync/push", bytes.NewReader(encoded)))
	if pushRec.Code != http.StatusOK {
		t.Fatalf("expected push 200, got %d body=%q", pushRec.Code, pushRec.Body.String())
	}

	pullRec := httptest.NewRecorder()
	pullReq := httptest.NewRequest(http.MethodGet, "/sync/pull/"+chunkID+"?project=proj-a", nil)
	srv.Handler().ServeHTTP(pullRec, pullReq)
	if pullRec.Code != http.StatusOK {
		t.Fatalf("expected pull 200, got %d body=%q", pullRec.Code, pullRec.Body.String())
	}
	if ct := pullRec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected pull-chunk Content-Type to remain exactly %q, got %q", "application/json", ct)
	}
	if !bytes.Equal(pullRec.Body.Bytes(), normalized) {
		t.Fatalf("expected pull-chunk body to be the exact stored chunk bytes verbatim (no envelope/wrap), got %s want %s", pullRec.Body.Bytes(), normalized)
	}
}

// TestSyncMutationsPushResponseSchemaUnchanged proves POST
// /sync/mutations/push still replies with exactly {accepted_seqs, project,
// project_source, project_path} for existing clients (REQ-414 envelope).
func TestSyncMutationsPushResponseSchemaUnchanged(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	entries := makeMutationEntries(1, "proj-a")
	body := marshalPushRequest(t, entries)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/mutations/push", body)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode mutations push response: %v", err)
	}
	assertExactKeys(t, "mutations push response", resp, "accepted_seqs", "project", "project_source", "project_path")
}

// TestSyncMutationsPullResponseSchemaUnchanged proves GET
// /sync/mutations/pull still replies with exactly {mutations, has_more,
// latest_seq, project, project_source, project_path} for existing clients.
func TestSyncMutationsPullResponseSchemaUnchanged(t *testing.T) {
	ms := newFakeMutationStore()
	srv := newMutationTestServer(ms, "secret", []string{"proj-a"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sync/mutations/pull", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode mutations pull response: %v", err)
	}
	assertExactKeys(t, "mutations pull response", resp, "mutations", "has_more", "latest_seq", "project", "project_source", "project_path")
}

// assertExactKeys fails the test if m does not contain exactly the given keys
// (no missing keys, no unexpected extra keys) — the strongest possible
// assertion against silent field renames/additions/removals in a wire schema.
func assertExactKeys(t *testing.T, label string, m map[string]json.RawMessage, keys ...string) {
	t.Helper()
	want := append([]string(nil), keys...)
	sort.Strings(want)

	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("%s: expected exactly keys %v, got %v", label, want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: expected exactly keys %v, got %v", label, want, got)
		}
	}
}
