package cloudserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

type mutationHTTPHarness struct {
	store  *fakeMutationStore
	server *httptest.Server
	client *http.Client
}

func newMutationHTTPHarness(t *testing.T, token string, projects []string) *mutationHTTPHarness {
	t.Helper()
	mutationStore := newFakeMutationStore()
	cloudServer := newMutationTestServer(mutationStore, token, projects)
	httpServer := httptest.NewServer(cloudServer.Handler())
	t.Cleanup(httpServer.Close)
	return &mutationHTTPHarness{
		store:  mutationStore,
		server: httpServer,
		client: httpServer.Client(),
	}
}

func postMutationHTTP(t *testing.T, harness *mutationHTTPHarness, token string, entries []MutationEntry) (*http.Response, []byte) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatalf("marshal mutation request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, harness.server.URL+"/sync/mutations/push", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build mutation request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := harness.client.Do(req)
	if err != nil {
		t.Fatalf("post mutation request: %v", err)
	}
	responseBody, err := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if err != nil {
		t.Fatalf("read mutation response: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("close mutation response: %v", closeErr)
	}
	return resp, responseBody
}

func canonicalMutationCompatibilityBatch() []MutationEntry {
	return []MutationEntry{
		{
			Project:   "proj-a",
			Entity:    store.SyncEntitySession,
			EntityKey: "session-1",
			Op:        store.SyncOpUpsert,
			Payload:   json.RawMessage(`{"id":"session-1","directory":"/tmp/session-1"}`),
		},
		{
			Project:   "proj-a",
			Entity:    store.SyncEntityObservation,
			EntityKey: "obs-1",
			Op:        store.SyncOpUpsert,
			Payload:   json.RawMessage(`{"sync_id":"obs-1","session_id":"session-1","type":"decision","title":"Title","content":"Content","scope":"project"}`),
		},
		{
			Project:   "proj-a",
			Entity:    store.SyncEntityPrompt,
			EntityKey: "prompt-1",
			Op:        store.SyncOpUpsert,
			Payload:   json.RawMessage(`{"sync_id":"prompt-1","session_id":"session-1","content":"Prompt"}`),
		},
		{
			Project:   "proj-a",
			Entity:    store.SyncEntityRelation,
			EntityKey: "relation-1",
			Op:        store.SyncOpUpsert,
			Payload:   json.RawMessage(`{"sync_id":"relation-1","source_id":"obs-1","target_id":"obs-2","relation":"related","judgment_status":"judged","marked_by_actor":"agent","marked_by_kind":"agent"}`),
		},
		{
			Project:   "proj-a",
			Entity:    store.SyncEntitySession,
			EntityKey: "session-1",
			Op:        store.SyncOpDelete,
			Payload:   json.RawMessage(`{"id":"session-1"}`),
		},
		{
			Project:   "proj-a",
			Entity:    store.SyncEntityObservation,
			EntityKey: "obs-1",
			Op:        store.SyncOpDelete,
			Payload:   json.RawMessage(`{"sync_id":"obs-1"}`),
		},
		{
			Project:   "proj-a",
			Entity:    store.SyncEntityPrompt,
			EntityKey: "prompt-1",
			Op:        store.SyncOpDelete,
			Payload:   json.RawMessage(`{"sync_id":"prompt-1"}`),
		},
	}
}

func invalidObservationMutation(project string) MutationEntry {
	return MutationEntry{
		Project:   project,
		Entity:    store.SyncEntityObservation,
		EntityKey: "obs-invalid",
		Op:        store.SyncOpUpsert,
		Payload:   json.RawMessage(`{"sync_id":"obs-invalid","session_id":"session-1","type":"decision","title":"Title","content":"","scope":"project"}`),
	}
}

func TestMutationPushHTTPServerAcceptsCanonicalUpsertsAndDeletes(t *testing.T) {
	harness := newMutationHTTPHarness(t, "secret", []string{"proj-a"})
	entries := canonicalMutationCompatibilityBatch()

	resp, body := postMutationHTTP(t, harness, "secret", entries)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", resp.StatusCode, body)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode success response: %v; body=%q", err, body)
	}
	assertExactKeys(t, "mutation push success", raw, "accepted_seqs", "project", "project_source", "project_path")

	var accepted []int64
	if err := json.Unmarshal(raw["accepted_seqs"], &accepted); err != nil {
		t.Fatalf("decode accepted_seqs: %v", err)
	}
	wantAccepted := []int64{1, 2, 3, 4, 5, 6, 7}
	if !reflect.DeepEqual(accepted, wantAccepted) {
		t.Fatalf("accepted_seqs: got %v, want %v", accepted, wantAccepted)
	}
	if harness.store.insertCalls != 1 || len(harness.store.mutations) != len(entries) {
		t.Fatalf("expected one storage call for all %d entries, calls=%d stored=%d", len(entries), harness.store.insertCalls, len(harness.store.mutations))
	}
}

func TestMutationPushHTTPServerReturnsExactRepairable400Envelope(t *testing.T) {
	harness := newMutationHTTPHarness(t, "secret", []string{"proj-a"})

	resp, body := postMutationHTTP(t, harness, "secret", []MutationEntry{invalidObservationMutation("proj-a")})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", resp.StatusCode, body)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode validation response: %v; body=%q", err, body)
	}
	assertExactKeys(t, "mutation push validation response", raw, "error", "error_class", "error_code", "invalid", "reason_code")

	var response mutationPushValidationResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode validation envelope: %v", err)
	}
	if response.ErrorClass != "repairable" || response.ErrorCode != "payload_invalid" || response.ReasonCode != "validation_error" {
		t.Fatalf("unexpected validation envelope: %+v", response)
	}
	wantInvalid := []mutationValidationDetail{{Index: 0, Entity: store.SyncEntityObservation, Field: "content"}}
	if !reflect.DeepEqual(response.Invalid, wantInvalid) {
		t.Fatalf("invalid details: got %+v, want %+v", response.Invalid, wantInvalid)
	}
	if _, ok := raw["accepted_seqs"]; ok {
		t.Fatalf("validation response unexpectedly acknowledged mutations: %q", body)
	}
	if harness.store.insertCalls != 0 || len(harness.store.mutations) != 0 {
		t.Fatalf("validation response crossed storage boundary: calls=%d stored=%d", harness.store.insertCalls, len(harness.store.mutations))
	}
}

func TestMutationPushHTTPServerHidesValidationDetailsBehindPolicyErrors(t *testing.T) {
	tests := []struct {
		name           string
		projects       []string
		project        string
		pauseProject   string
		wantStatus     int
		wantCode       string
		wantKeys       []string
		forbiddenTerms []string
	}{
		{
			name:           "unauthorized project",
			projects:       []string{"proj-a"},
			project:        "proj-b",
			wantStatus:     http.StatusForbidden,
			wantCode:       "policy_forbidden",
			wantKeys:       []string{"error", "error_class", "error_code"},
			forbiddenTerms: []string{"payload_invalid", "invalid", "content"},
		},
		{
			name:           "paused project",
			projects:       []string{"proj-a"},
			project:        "proj-a",
			pauseProject:   "proj-a",
			wantStatus:     http.StatusConflict,
			wantCode:       "sync-paused",
			wantKeys:       []string{"error", "error_code", "error_class", "project", "project_path", "project_source"},
			forbiddenTerms: []string{"payload_invalid", "invalid", "content"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newMutationHTTPHarness(t, "secret", tt.projects)
			if tt.pauseProject != "" {
				harness.store.syncEnabledMap[tt.pauseProject] = false
			}

			resp, body := postMutationHTTP(t, harness, "secret", []MutationEntry{invalidObservationMutation(tt.project)})
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("expected %d, got %d body=%q", tt.wantStatus, resp.StatusCode, body)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(body, &raw); err != nil {
				t.Fatalf("decode policy response: %v; body=%q", err, body)
			}
			assertExactKeys(t, tt.name, raw, tt.wantKeys...)
			var code string
			if err := json.Unmarshal(raw["error_code"], &code); err != nil {
				t.Fatalf("decode error_code: %v", err)
			}
			if code != tt.wantCode {
				t.Fatalf("error_code: got %q, want %q", code, tt.wantCode)
			}
			for _, term := range tt.forbiddenTerms {
				if strings.Contains(string(body), term) {
					t.Fatalf("policy response exposed validation term %q: %q", term, body)
				}
			}
			if harness.store.insertCalls != 0 || len(harness.store.mutations) != 0 {
				t.Fatalf("policy response crossed storage boundary: calls=%d stored=%d", harness.store.insertCalls, len(harness.store.mutations))
			}
		})
	}
}

func TestMutationPushHTTPServerRejectsMixedBatchWithoutPersistence(t *testing.T) {
	harness := newMutationHTTPHarness(t, "secret", []string{"proj-a"})
	valid := canonicalMutationCompatibilityBatch()[0]
	invalid := invalidObservationMutation("proj-a")

	resp, body := postMutationHTTP(t, harness, "secret", []MutationEntry{valid, invalid})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", resp.StatusCode, body)
	}
	var response mutationPushValidationResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode mixed-batch response: %v", err)
	}
	if len(response.Invalid) != 1 || response.Invalid[0] != (mutationValidationDetail{Index: 1, Entity: store.SyncEntityObservation, Field: "content"}) {
		t.Fatalf("unexpected mixed-batch invalid details: %+v", response.Invalid)
	}
	if harness.store.insertCalls != 0 || len(harness.store.mutations) != 0 {
		t.Fatalf("mixed batch persisted a subset: calls=%d stored=%d", harness.store.insertCalls, len(harness.store.mutations))
	}
}
