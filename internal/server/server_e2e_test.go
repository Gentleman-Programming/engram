package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/alanbuscaglia/engram/internal/store"
)

func newE2EServer(t *testing.T) (*store.Store, *httptest.Server) {
	t.Helper()
	cfg := store.DefaultConfig()
	cfg.DataDir = t.TempDir()

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	httpServer := httptest.NewServer(New(s, 0).Handler())
	t.Cleanup(func() {
		httpServer.Close()
		_ = s.Close()
	})

	return s, httpServer
}

func postJSON(t *testing.T, client *http.Client, url string, body any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

func decodeJSON[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	return out
}

func TestObservationsTopicUpsertAndDeleteE2E(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	sessionResp := postJSON(t, client, ts.URL+"/sessions", map[string]any{
		"id":        "s-e2e",
		"project":   "engram",
		"directory": "/tmp/engram",
	})
	if sessionResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", sessionResp.StatusCode)
	}
	sessionResp.Body.Close()

	firstResp := postJSON(t, client, ts.URL+"/observations", map[string]any{
		"session_id": "s-e2e",
		"type":       "architecture",
		"title":      "Auth architecture",
		"content":    "Use middleware chain for auth",
		"project":    "engram",
		"scope":      "project",
		"topic_key":  "architecture/auth-model",
	})
	if firstResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating first observation, got %d", firstResp.StatusCode)
	}
	firstBody := decodeJSON[map[string]any](t, firstResp)
	firstID := int64(firstBody["id"].(float64))

	secondResp := postJSON(t, client, ts.URL+"/observations", map[string]any{
		"session_id": "s-e2e",
		"type":       "architecture",
		"title":      "Auth architecture",
		"content":    "Move auth to gateway and middleware chain",
		"project":    "engram",
		"scope":      "project",
		"topic_key":  "architecture/auth-model",
	})
	if secondResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 upserting observation, got %d", secondResp.StatusCode)
	}
	secondBody := decodeJSON[map[string]any](t, secondResp)
	secondID := int64(secondBody["id"].(float64))
	if firstID != secondID {
		t.Fatalf("expected topic upsert to return same id, got %d and %d", firstID, secondID)
	}

	getResp, err := client.Get(ts.URL + "/observations/" + strconv.FormatInt(firstID, 10))
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 getting observation, got %d", getResp.StatusCode)
	}
	obs := decodeJSON[map[string]any](t, getResp)
	if int(obs["revision_count"].(float64)) != 2 {
		t.Fatalf("expected revision_count=2, got %v", obs["revision_count"])
	}
	if !strings.Contains(obs["content"].(string), "gateway") {
		t.Fatalf("expected latest content after upsert, got %q", obs["content"].(string))
	}

	bugResp := postJSON(t, client, ts.URL+"/observations", map[string]any{
		"session_id": "s-e2e",
		"type":       "bugfix",
		"title":      "Fix auth panic",
		"content":    "Fix nil token panic",
		"project":    "engram",
		"scope":      "project",
		"topic_key":  "bug/auth-nil-panic",
	})
	if bugResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating bug observation, got %d", bugResp.StatusCode)
	}
	bugBody := decodeJSON[map[string]any](t, bugResp)
	bugID := int64(bugBody["id"].(float64))
	if bugID == firstID {
		t.Fatalf("expected different topic to create new observation")
	}

	deleteReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/observations/"+strconv.FormatInt(firstID, 10), nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	deleteResp, err := client.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete observation: %v", err)
	}
	if deleteResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 soft-deleting observation, got %d", deleteResp.StatusCode)
	}
	deleteResp.Body.Close()

	deletedGetResp, err := client.Get(ts.URL + "/observations/" + strconv.FormatInt(firstID, 10))
	if err != nil {
		t.Fatalf("get deleted observation: %v", err)
	}
	if deletedGetResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for soft-deleted observation, got %d", deletedGetResp.StatusCode)
	}
	deletedGetResp.Body.Close()

	searchResp, err := client.Get(ts.URL + "/search?q=panic&project=engram&scope=project&limit=10")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 search, got %d", searchResp.StatusCode)
	}
	searchResults := decodeJSON[[]map[string]any](t, searchResp)
	if len(searchResults) != 1 {
		t.Fatalf("expected one search result after soft-delete, got %d", len(searchResults))
	}
	if int64(searchResults[0]["id"].(float64)) != bugID {
		t.Fatalf("expected bug observation in search results")
	}
}

func TestPassiveCaptureEndpointE2E(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	// Create session
	sessionResp := postJSON(t, client, ts.URL+"/sessions", map[string]any{
		"id":        "s-passive",
		"project":   "engram",
		"directory": "/tmp/engram",
	})
	if sessionResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", sessionResp.StatusCode)
	}
	sessionResp.Body.Close()

	// POST passive capture with learnings
	captureResp := postJSON(t, client, ts.URL+"/observations/passive", map[string]any{
		"session_id": "s-passive",
		"project":    "engram",
		"source":     "subagent-stop",
		"content":    "## Key Learnings:\n\n1. bcrypt cost=12 is the right balance for our server performance\n2. JWT refresh tokens need atomic rotation to prevent race conditions\n",
	})
	if captureResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 passive capture, got %d", captureResp.StatusCode)
	}
	body := decodeJSON[map[string]any](t, captureResp)
	if int(body["extracted"].(float64)) != 2 {
		t.Fatalf("expected 2 extracted, got %v", body["extracted"])
	}
	if int(body["saved"].(float64)) != 2 {
		t.Fatalf("expected 2 saved, got %v", body["saved"])
	}

	// Verify observations are searchable
	searchResp, err := client.Get(ts.URL + "/search?q=bcrypt&project=engram&limit=10")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 search, got %d", searchResp.StatusCode)
	}
	results := decodeJSON[[]map[string]any](t, searchResp)
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
}

func TestPassiveCaptureEndpointEmptyContentE2E(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	sessionResp := postJSON(t, client, ts.URL+"/sessions", map[string]any{
		"id":        "s-empty",
		"project":   "engram",
		"directory": "/tmp/engram",
	})
	if sessionResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", sessionResp.StatusCode)
	}
	sessionResp.Body.Close()

	captureResp := postJSON(t, client, ts.URL+"/observations/passive", map[string]any{
		"session_id": "s-empty",
		"content":    "just some text without any learning section",
		"project":    "engram",
	})
	if captureResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for empty capture, got %d", captureResp.StatusCode)
	}
	body := decodeJSON[map[string]any](t, captureResp)
	if int(body["extracted"].(float64)) != 0 {
		t.Fatalf("expected 0 extracted, got %v", body["extracted"])
	}
}

func TestPassiveCaptureEndpointRequiresSessionID(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	captureResp := postJSON(t, client, ts.URL+"/observations/passive", map[string]any{
		"project": "engram",
		"content": "## Key Learnings:\n\n1. This should fail because session_id is missing",
	})
	if captureResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when session_id is missing, got %d", captureResp.StatusCode)
	}
}

func TestPassiveCaptureEndpointInvalidJSON(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	resp, err := client.Post(ts.URL+"/observations/passive", "application/json", strings.NewReader("{"))
	if err != nil {
		t.Fatalf("post invalid json: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid json, got %d", resp.StatusCode)
	}
}

func TestPassiveCaptureEndpointReturnsServerErrorWhenSessionMissing(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	// No session created; saving observations should fail with FK constraint.
	captureResp := postJSON(t, client, ts.URL+"/observations/passive", map[string]any{
		"session_id": "missing-session",
		"project":    "engram",
		"content":    "## Key Learnings:\n\n1. This long learning should trigger a DB insert and fail on FK",
	})
	if captureResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 when session does not exist, got %d", captureResp.StatusCode)
	}
}
