package server

import (
	"bytes"
	"encoding/json"
	"io"
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

func TestCoreReadHandlersAndHelpersE2E(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	healthResp, err := client.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 health, got %d", healthResp.StatusCode)
	}
	health := decodeJSON[map[string]any](t, healthResp)
	if health["status"] != "ok" {
		t.Fatalf("expected health status ok, got %v", health["status"])
	}

	create := postJSON(t, client, ts.URL+"/sessions", map[string]any{
		"id":      "s-core",
		"project": "engram",
	})
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", create.StatusCode)
	}
	create.Body.Close()

	obs := postJSON(t, client, ts.URL+"/observations", map[string]any{
		"session_id": "s-core",
		"type":       "decision",
		"title":      "Core test",
		"content":    "exercise handlers",
		"project":    "engram",
	})
	if obs.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating observation, got %d", obs.StatusCode)
	}
	obsData := decodeJSON[map[string]any](t, obs)
	obsID := int64(obsData["id"].(float64))

	recentSessionsResp, err := client.Get(ts.URL + "/sessions/recent?project=engram&limit=oops")
	if err != nil {
		t.Fatalf("recent sessions: %v", err)
	}
	if recentSessionsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 recent sessions, got %d", recentSessionsResp.StatusCode)
	}
	recentSessions := decodeJSON[[]map[string]any](t, recentSessionsResp)
	if len(recentSessions) == 0 {
		t.Fatalf("expected at least one recent session")
	}

	recentObsResp, err := client.Get(ts.URL + "/observations/recent?project=engram&scope=project&limit=bad")
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if recentObsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 recent observations, got %d", recentObsResp.StatusCode)
	}
	recentObs := decodeJSON[[]map[string]any](t, recentObsResp)
	if len(recentObs) == 0 {
		t.Fatalf("expected recent observations")
	}

	timelineResp, err := client.Get(ts.URL + "/timeline?observation_id=" + strconv.FormatInt(obsID, 10) + "&before=bad&after=bad")
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if timelineResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 timeline, got %d", timelineResp.StatusCode)
	}
	timeline := decodeJSON[map[string]any](t, timelineResp)
	if timeline["focus"] == nil {
		t.Fatalf("expected focus observation in timeline")
	}

	contextResp, err := client.Get(ts.URL + "/context?project=engram&scope=project")
	if err != nil {
		t.Fatalf("context: %v", err)
	}
	if contextResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 context, got %d", contextResp.StatusCode)
	}
	contextData := decodeJSON[map[string]string](t, contextResp)
	if !strings.Contains(contextData["context"], "Memory from Previous Sessions") {
		t.Fatalf("expected formatted context output")
	}

	statsResp, err := client.Get(ts.URL + "/stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if statsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 stats, got %d", statsResp.StatusCode)
	}
	stats := decodeJSON[map[string]any](t, statsResp)
	if stats["total_sessions"].(float64) < 1 {
		t.Fatalf("expected at least one session in stats")
	}

	endResp := postJSON(t, client, ts.URL+"/sessions/s-core/end", map[string]any{"summary": "done"})
	if endResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 ending session, got %d", endResp.StatusCode)
	}
	endResp.Body.Close()
}

func TestValidationAndImportExportErrorsE2E(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	invalidSessionResp, err := client.Post(ts.URL+"/sessions", "application/json", strings.NewReader("{"))
	if err != nil {
		t.Fatalf("post invalid session json: %v", err)
	}
	if invalidSessionResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 invalid session json, got %d", invalidSessionResp.StatusCode)
	}
	invalidSessionResp.Body.Close()

	missingFieldsResp := postJSON(t, client, ts.URL+"/sessions", map[string]any{"id": "only-id"})
	if missingFieldsResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 missing required fields, got %d", missingFieldsResp.StatusCode)
	}
	missingFieldsResp.Body.Close()

	create := postJSON(t, client, ts.URL+"/sessions", map[string]any{
		"id":      "s-validate",
		"project": "engram",
	})
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", create.StatusCode)
	}
	create.Body.Close()

	updateBadIDReq, _ := http.NewRequest(http.MethodPatch, ts.URL+"/observations/not-a-number", strings.NewReader(`{"title":"x"}`))
	updateBadIDReq.Header.Set("Content-Type", "application/json")
	updateBadIDResp, err := client.Do(updateBadIDReq)
	if err != nil {
		t.Fatalf("patch bad id: %v", err)
	}
	if updateBadIDResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 update bad id, got %d", updateBadIDResp.StatusCode)
	}
	updateBadIDResp.Body.Close()

	searchMissingQResp, err := client.Get(ts.URL + "/search")
	if err != nil {
		t.Fatalf("search without q: %v", err)
	}
	if searchMissingQResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 search missing q, got %d", searchMissingQResp.StatusCode)
	}
	searchMissingQResp.Body.Close()

	promptsMissingQResp, err := client.Get(ts.URL + "/prompts/search")
	if err != nil {
		t.Fatalf("search prompts without q: %v", err)
	}
	if promptsMissingQResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 prompts search missing q, got %d", promptsMissingQResp.StatusCode)
	}
	promptsMissingQResp.Body.Close()

	invalidImportResp, err := client.Post(ts.URL+"/import", "application/json", strings.NewReader("{"))
	if err != nil {
		t.Fatalf("import invalid json: %v", err)
	}
	if invalidImportResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 import invalid json, got %d", invalidImportResp.StatusCode)
	}
	invalidImportResp.Body.Close()

	exportResp, err := client.Get(ts.URL + "/export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if exportResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 export, got %d", exportResp.StatusCode)
	}
	exportedBody, err := io.ReadAll(exportResp.Body)
	if err != nil {
		t.Fatalf("read export body: %v", err)
	}
	exportResp.Body.Close()

	reimportResp, err := client.Post(ts.URL+"/import", "application/json", bytes.NewReader(exportedBody))
	if err != nil {
		t.Fatalf("reimport: %v", err)
	}
	if reimportResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 import after export, got %d", reimportResp.StatusCode)
	}
	reimportResp.Body.Close()

	recentPromptsResp, err := client.Get(ts.URL + "/prompts/recent?project=engram&limit=bad")
	if err != nil {
		t.Fatalf("recent prompts: %v", err)
	}
	if recentPromptsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 recent prompts, got %d", recentPromptsResp.StatusCode)
	}
	recentPromptsResp.Body.Close()
}

func TestPromptAndObservationMutationHandlersE2E(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	create := postJSON(t, client, ts.URL+"/sessions", map[string]any{
		"id":      "s-mutate",
		"project": "engram",
	})
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", create.StatusCode)
	}
	create.Body.Close()

	addPrompt := postJSON(t, client, ts.URL+"/prompts", map[string]any{
		"session_id": "s-mutate",
		"content":    "How to fix auth panic?",
		"project":    "engram",
	})
	if addPrompt.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 adding prompt, got %d", addPrompt.StatusCode)
	}
	addPrompt.Body.Close()

	addPromptMissing := postJSON(t, client, ts.URL+"/prompts", map[string]any{
		"session_id": "s-mutate",
	})
	if addPromptMissing.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for prompt missing content, got %d", addPromptMissing.StatusCode)
	}
	addPromptMissing.Body.Close()

	searchPromptResp, err := client.Get(ts.URL + "/prompts/search?q=auth&project=engram&limit=5")
	if err != nil {
		t.Fatalf("search prompts: %v", err)
	}
	if searchPromptResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 searching prompts, got %d", searchPromptResp.StatusCode)
	}
	prompts := decodeJSON[[]map[string]any](t, searchPromptResp)
	if len(prompts) == 0 {
		t.Fatalf("expected prompt search results")
	}

	obs := postJSON(t, client, ts.URL+"/observations", map[string]any{
		"session_id": "s-mutate",
		"type":       "decision",
		"title":      "Auth handling",
		"content":    "Use middleware",
		"project":    "engram",
	})
	if obs.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 adding observation, got %d", obs.StatusCode)
	}
	obsBody := decodeJSON[map[string]any](t, obs)
	obsID := int64(obsBody["id"].(float64))

	updateReq, err := http.NewRequest(http.MethodPatch, ts.URL+"/observations/"+strconv.FormatInt(obsID, 10), strings.NewReader(`{"title":"Auth handling updated","topic_key":"architecture/auth"}`))
	if err != nil {
		t.Fatalf("new patch request: %v", err)
	}
	updateReq.Header.Set("Content-Type", "application/json")
	updateResp, err := client.Do(updateReq)
	if err != nil {
		t.Fatalf("patch observation: %v", err)
	}
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 updating observation, got %d", updateResp.StatusCode)
	}
	updated := decodeJSON[map[string]any](t, updateResp)
	if updated["title"] != "Auth handling updated" {
		t.Fatalf("expected updated title, got %v", updated["title"])
	}

	emptyUpdateReq, _ := http.NewRequest(http.MethodPatch, ts.URL+"/observations/"+strconv.FormatInt(obsID, 10), strings.NewReader(`{}`))
	emptyUpdateReq.Header.Set("Content-Type", "application/json")
	emptyUpdateResp, err := client.Do(emptyUpdateReq)
	if err != nil {
		t.Fatalf("patch empty update: %v", err)
	}
	if emptyUpdateResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty update payload, got %d", emptyUpdateResp.StatusCode)
	}
	emptyUpdateResp.Body.Close()

	badUpdateReq, _ := http.NewRequest(http.MethodPatch, ts.URL+"/observations/"+strconv.FormatInt(obsID, 10), strings.NewReader("{"))
	badUpdateReq.Header.Set("Content-Type", "application/json")
	badUpdateResp, err := client.Do(badUpdateReq)
	if err != nil {
		t.Fatalf("patch invalid json: %v", err)
	}
	if badUpdateResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid update json, got %d", badUpdateResp.StatusCode)
	}
	badUpdateResp.Body.Close()

	deleteHardReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/observations/"+strconv.FormatInt(obsID, 10)+"?hard=true", nil)
	deleteHardResp, err := client.Do(deleteHardReq)
	if err != nil {
		t.Fatalf("delete hard observation: %v", err)
	}
	if deleteHardResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 hard delete, got %d", deleteHardResp.StatusCode)
	}
	deleteHardResp.Body.Close()

	deleteInvalidBoolReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/observations/"+strconv.FormatInt(obsID, 10)+"?hard=not-bool", nil)
	deleteInvalidBoolResp, err := client.Do(deleteInvalidBoolReq)
	if err != nil {
		t.Fatalf("delete with invalid bool: %v", err)
	}
	if deleteInvalidBoolResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 delete with invalid bool fallback, got %d", deleteInvalidBoolResp.StatusCode)
	}
	deleteInvalidBoolResp.Body.Close()

	timelineMissingIDResp, err := client.Get(ts.URL + "/timeline")
	if err != nil {
		t.Fatalf("timeline missing id: %v", err)
	}
	if timelineMissingIDResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 timeline missing observation_id, got %d", timelineMissingIDResp.StatusCode)
	}
	timelineMissingIDResp.Body.Close()

	timelineBadIDResp, err := client.Get(ts.URL + "/timeline?observation_id=abc")
	if err != nil {
		t.Fatalf("timeline bad id: %v", err)
	}
	if timelineBadIDResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 invalid timeline id, got %d", timelineBadIDResp.StatusCode)
	}
	timelineBadIDResp.Body.Close()
}

func TestServerHandlersReturn500WhenStoreClosed(t *testing.T) {
	s, ts := newE2EServer(t)
	client := ts.Client()

	create := postJSON(t, client, ts.URL+"/sessions", map[string]any{
		"id":      "s-closed",
		"project": "engram",
	})
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", create.StatusCode)
	}
	create.Body.Close()

	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	addPrompt := postJSON(t, client, ts.URL+"/prompts", map[string]any{
		"session_id": "s-closed",
		"content":    "prompt",
		"project":    "engram",
	})
	if addPrompt.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 add prompt with closed store, got %d", addPrompt.StatusCode)
	}
	addPrompt.Body.Close()

	recentPromptsResp, err := client.Get(ts.URL + "/prompts/recent")
	if err != nil {
		t.Fatalf("recent prompts closed store: %v", err)
	}
	if recentPromptsResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 recent prompts with closed store, got %d", recentPromptsResp.StatusCode)
	}
	recentPromptsResp.Body.Close()

	searchPromptsResp, err := client.Get(ts.URL + "/prompts/search?q=test")
	if err != nil {
		t.Fatalf("search prompts closed store: %v", err)
	}
	if searchPromptsResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 search prompts with closed store, got %d", searchPromptsResp.StatusCode)
	}
	searchPromptsResp.Body.Close()

	contextResp, err := client.Get(ts.URL + "/context")
	if err != nil {
		t.Fatalf("context closed store: %v", err)
	}
	if contextResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 context with closed store, got %d", contextResp.StatusCode)
	}
	contextResp.Body.Close()

	statsResp, err := client.Get(ts.URL + "/stats")
	if err != nil {
		t.Fatalf("stats closed store: %v", err)
	}
	if statsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 stats with closed store fallback, got %d", statsResp.StatusCode)
	}
	statsResp.Body.Close()
}

func TestObservationAndSessionErrorBranchesE2E(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	addObsBadJSONResp, err := client.Post(ts.URL+"/observations", "application/json", strings.NewReader("{"))
	if err != nil {
		t.Fatalf("post bad observation json: %v", err)
	}
	if addObsBadJSONResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 invalid observation json, got %d", addObsBadJSONResp.StatusCode)
	}
	addObsBadJSONResp.Body.Close()

	addObsMissingFieldsResp := postJSON(t, client, ts.URL+"/observations", map[string]any{"session_id": "s-x"})
	if addObsMissingFieldsResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 observation missing fields, got %d", addObsMissingFieldsResp.StatusCode)
	}
	addObsMissingFieldsResp.Body.Close()

	create := postJSON(t, client, ts.URL+"/sessions", map[string]any{
		"id":      "s-errors",
		"project": "engram",
	})
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", create.StatusCode)
	}
	create.Body.Close()

	obs := postJSON(t, client, ts.URL+"/observations", map[string]any{
		"session_id": "s-errors",
		"type":       "decision",
		"title":      "Delete me",
		"content":    "content",
		"project":    "engram",
	})
	if obs.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 adding observation, got %d", obs.StatusCode)
	}
	obsData := decodeJSON[map[string]any](t, obs)
	obsID := int64(obsData["id"].(float64))

	deleteBadIDReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/observations/not-number", nil)
	deleteBadIDResp, err := client.Do(deleteBadIDReq)
	if err != nil {
		t.Fatalf("delete bad id: %v", err)
	}
	if deleteBadIDResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 delete bad id, got %d", deleteBadIDResp.StatusCode)
	}
	deleteBadIDResp.Body.Close()

	deleteReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/observations/"+strconv.FormatInt(obsID, 10), nil)
	deleteResp, err := client.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete observation: %v", err)
	}
	if deleteResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 deleting observation, got %d", deleteResp.StatusCode)
	}
	deleteResp.Body.Close()

	timelineNotFoundResp, err := client.Get(ts.URL + "/timeline?observation_id=" + strconv.FormatInt(obsID, 10))
	if err != nil {
		t.Fatalf("timeline for deleted obs: %v", err)
	}
	if timelineNotFoundResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 timeline for deleted observation, got %d", timelineNotFoundResp.StatusCode)
	}
	timelineNotFoundResp.Body.Close()

	searchBadFTSResp, err := client.Get(ts.URL + "/search?q=%22%22%22")
	if err != nil {
		t.Fatalf("search malformed fts input: %v", err)
	}
	if searchBadFTSResp.StatusCode != http.StatusOK {
		t.Fatalf("expected handled malformed query response, got %d", searchBadFTSResp.StatusCode)
	}
	searchBadFTSResp.Body.Close()
}

func TestStoreClosedExtraServerBranchesE2E(t *testing.T) {
	s, ts := newE2EServer(t)
	client := ts.Client()

	create := postJSON(t, client, ts.URL+"/sessions", map[string]any{
		"id":      "s-closed-2",
		"project": "engram",
	})
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", create.StatusCode)
	}
	create.Body.Close()

	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	createSessionResp := postJSON(t, client, ts.URL+"/sessions", map[string]any{"id": "s2", "project": "engram"})
	if createSessionResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 creating session on closed store, got %d", createSessionResp.StatusCode)
	}
	createSessionResp.Body.Close()

	endResp := postJSON(t, client, ts.URL+"/sessions/s-closed-2/end", map[string]any{"summary": "done"})
	if endResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 ending session on closed store, got %d", endResp.StatusCode)
	}
	endResp.Body.Close()

	recentSessionsResp, err := client.Get(ts.URL + "/sessions/recent")
	if err != nil {
		t.Fatalf("recent sessions closed store: %v", err)
	}
	if recentSessionsResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 recent sessions on closed store, got %d", recentSessionsResp.StatusCode)
	}
	recentSessionsResp.Body.Close()

	addObsResp := postJSON(t, client, ts.URL+"/observations", map[string]any{
		"session_id": "s-closed-2",
		"type":       "decision",
		"title":      "t",
		"content":    "c",
	})
	if addObsResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 add observation on closed store, got %d", addObsResp.StatusCode)
	}
	addObsResp.Body.Close()

	recentObsResp, err := client.Get(ts.URL + "/observations/recent")
	if err != nil {
		t.Fatalf("recent observations closed store: %v", err)
	}
	if recentObsResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 recent observations on closed store, got %d", recentObsResp.StatusCode)
	}
	recentObsResp.Body.Close()

	searchResp, err := client.Get(ts.URL + "/search?q=test")
	if err != nil {
		t.Fatalf("search closed store: %v", err)
	}
	if searchResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 search on closed store, got %d", searchResp.StatusCode)
	}
	searchResp.Body.Close()

	getResp, err := client.Get(ts.URL + "/observations/1")
	if err != nil {
		t.Fatalf("get observation closed store: %v", err)
	}
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 get observation on closed store, got %d", getResp.StatusCode)
	}
	getResp.Body.Close()

	deleteReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/observations/1", nil)
	deleteResp, err := client.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete observation closed store: %v", err)
	}
	if deleteResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 delete observation on closed store, got %d", deleteResp.StatusCode)
	}
	deleteResp.Body.Close()

	exportResp, err := client.Get(ts.URL + "/export")
	if err != nil {
		t.Fatalf("export closed store: %v", err)
	}
	if exportResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 export on closed store, got %d", exportResp.StatusCode)
	}
	exportResp.Body.Close()
}
