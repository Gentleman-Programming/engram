//go:build e2e

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

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func newE2EServer(t *testing.T) (*store.Store, *httptest.Server) {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
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

func TestSearchNoHitsReturnsEmptyArrayE2E(t *testing.T) {
	_, ts := newE2EServer(t)

	resp, err := ts.Client().Get(ts.URL + "/search?q=no-hits")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for no-hit search, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read search response: %v", err)
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "[]" {
		t.Fatalf("expected no-hit search body [], got %q", trimmed)
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

func TestPassiveCaptureEndpointReturnsNotFoundWhenSessionMissing(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	// No session created; passive capture now fails before attempting a DB insert.
	captureResp := postJSON(t, client, ts.URL+"/observations/passive", map[string]any{
		"session_id": "missing-session",
		"project":    "engram",
		"content":    "## Key Learnings:\n\n1. This long learning should trigger validation before DB insert",
	})
	if captureResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when session does not exist, got %d", captureResp.StatusCode)
	}
}

func TestDeleteSessionPropagatesForCloudEnrolledProjectE2E(t *testing.T) {
	s, ts := newE2EServer(t)
	client := ts.Client()

	createResp := postJSON(t, client, ts.URL+"/sessions", map[string]any{
		"id":        "s-cloud-enrolled",
		"project":   "engram",
		"directory": "/tmp/engram",
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating session, got %d", createResp.StatusCode)
	}
	createResp.Body.Close()

	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	deleteReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/sessions/s-cloud-enrolled", nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	deleteResp, err := client.Do(deleteReq)
	if err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if deleteResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 deleting cloud-enrolled session, got %d", deleteResp.StatusCode)
	}
	_ = deleteResp.Body.Close()

	mutations, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending sync mutations: %v", err)
	}
	var foundDelete bool
	for _, mutation := range mutations {
		if mutation.Entity == store.SyncEntitySession && mutation.EntityKey == "s-cloud-enrolled" && mutation.Op == store.SyncOpDelete {
			foundDelete = true
			break
		}
	}
	if !foundDelete {
		t.Fatalf("expected pending session/delete mutation for cloud-enrolled session, got %+v", mutations)
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

	getSessionResp, err := client.Get(ts.URL + "/sessions/s-core")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if getSessionResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 get session, got %d", getSessionResp.StatusCode)
	}
	getSession := decodeJSON[map[string]any](t, getSessionResp)
	if getSession["started_at"] == "" || getSession["project"] != "engram" {
		t.Fatalf("expected get session JSON with started_at/project, got %#v", getSession)
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

	listObsResp, err := client.Get(ts.URL + "/observations?project=engram&limit=1&sort=created_at:desc")
	if err != nil {
		t.Fatalf("list observations compatibility endpoint: %v", err)
	}
	if listObsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 list observations compatibility endpoint, got %d", listObsResp.StatusCode)
	}
	listObs := decodeJSON[[]map[string]any](t, listObsResp)
	if len(listObs) != 1 || listObs[0]["title"] != "Core test" || listObs[0]["created_at"] == "" {
		t.Fatalf("expected latest observation with created_at, got %#v", listObs)
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

func TestCompactionContextE2EIsSessionScoped(t *testing.T) {
	s, ts := newE2EServer(t)
	client := ts.Client()

	for _, sessionID := range []string{"runtime-a", "runtime-b"} {
		resp := postJSON(t, client, ts.URL+"/sessions", map[string]any{"id": sessionID, "project": "engram"})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: got %d", sessionID, resp.StatusCode)
		}
		resp.Body.Close()
	}
	for _, item := range []struct {
		sessionID string
		title     string
		content   string
		pinned    bool
	}{
		{"runtime-a", "pinned-a", "pinned-content-a", true},
		{"runtime-a", "recent-a", "recent-content-a", false},
		{"runtime-b", "pinned-b", "pinned-content-b", true},
		{"runtime-b", "recent-b", "recent-content-b", false},
	} {
		resp := postJSON(t, client, ts.URL+"/observations", map[string]any{"session_id": item.sessionID, "type": "decision", "title": item.title, "content": item.content, "project": "engram"})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: got %d", item.title, resp.StatusCode)
		}
		id := int64(decodeJSON[map[string]any](t, resp)["id"].(float64))
		if item.pinned {
			if err := s.PinObservation(id); err != nil {
				t.Fatalf("pin %s: %v", item.title, err)
			}
		}
	}

	for _, item := range []struct{ sessionID, content string }{{"runtime-a", "prompt-a"}, {"runtime-b", "prompt-b"}} {
		resp := postJSON(t, client, ts.URL+"/prompts", map[string]any{"session_id": item.sessionID, "content": item.content, "project": "engram"})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: got %d", item.content, resp.StatusCode)
		}
		resp.Body.Close()
	}

	response, err := client.Get(ts.URL + "/context/compaction?session_id=runtime-a&project=foreign")
	if err != nil {
		t.Fatalf("get compaction context: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("compaction context status = %d, want 200", response.StatusCode)
	}
	context := decodeJSON[map[string]string](t, response)["context"]
	for _, value := range []string{"runtime-a", "prompt-a", "recent-a"} {
		if !strings.Contains(context, value) {
			t.Errorf("context missing %q:\n%s", value, context)
		}
	}
	for _, value := range []string{"runtime-b", "prompt-b", "recent-b"} {
		if strings.Contains(context, value) {
			t.Errorf("context leaked %q:\n%s", value, context)
		}
	}

	missing, err := client.Get(ts.URL + "/context/compaction")
	if err != nil {
		t.Fatalf("get missing compaction session: %v", err)
	}
	if missing.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing session status = %d, want 400", missing.StatusCode)
	}
	missing.Body.Close()

	unknown, err := client.Get(ts.URL + "/context/compaction?session_id=unknown")
	if err != nil {
		t.Fatalf("get unknown compaction session: %v", err)
	}
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", unknown.StatusCode)
	}
	unknown.Body.Close()

	manual, err := client.Get(ts.URL + "/context?project=engram")
	if err != nil {
		t.Fatalf("get manual context: %v", err)
	}
	if manual.StatusCode != http.StatusOK {
		t.Fatalf("manual context status = %d, want 200", manual.StatusCode)
	}
	manualContext := decodeJSON[map[string]string](t, manual)["context"]
	if !strings.Contains(manualContext, "prompt-a") || !strings.Contains(manualContext, "prompt-b") ||
		!strings.Contains(manualContext, "recent-a") || !strings.Contains(manualContext, "recent-b") {
		t.Fatalf("manual project context must remain project-scoped:\n%s", manualContext)
	}
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
	if deleteInvalidBoolResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 deleting already hard-deleted observation, got %d", deleteInvalidBoolResp.StatusCode)
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

	deleteMissingReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/observations/"+strconv.FormatInt(obsID, 10), nil)
	deleteMissingResp, err := client.Do(deleteMissingReq)
	if err != nil {
		t.Fatalf("delete missing observation: %v", err)
	}
	if deleteMissingResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 deleting missing observation, got %d", deleteMissingResp.StatusCode)
	}
	deleteMissingResp.Body.Close()

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

// TestPiPromptPersistenceE2E replays the exact wire sequence the Pi plugin's mem_save_prompt
// issues (POST /sessions, then POST /prompts) and proves the prompt is durably persisted,
// retrievable, and scoped to its project.
//
// Regression for #706: the reported symptom was a "saved" response carrying an id that resolved
// to an unrelated entry from another project. The id was never stale — prompts are numbered from
// user_prompts, a sequence independent of observations — so the response id must read back as the
// prompt that was just saved, and must not resolve as an observation.
func TestPiPromptPersistenceE2E(t *testing.T) {
	_, ts := newE2EServer(t)
	client := ts.Client()

	const (
		targetProject = "paidosdep"
		otherProject  = "skill-registry"
		promptContent = "preserve this exact user prompt about auth token rotation"
	)

	// The plugin derives a stable per-project session id when the caller passes an explicit
	// project, and creates that session before writing the prompt.
	targetSession := "manual-save-" + targetProject
	otherSession := "manual-save-" + otherProject

	for _, s := range []struct{ id, project string }{
		{targetSession, targetProject},
		{otherSession, otherProject},
	} {
		sessionResp := postJSON(t, client, ts.URL+"/sessions", map[string]any{
			"id":        s.id,
			"project":   s.project,
			"directory": "/tmp/" + s.project,
		})
		if sessionResp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 creating session %q, got %d", s.id, sessionResp.StatusCode)
		}
		sessionResp.Body.Close()
	}

	// An observation in the other project gives both id sequences live rows, so the namespace
	// assertions below exercise the collision #706 actually hit rather than an empty table.
	obsResp := postJSON(t, client, ts.URL+"/observations", map[string]any{
		"session_id": otherSession,
		"title":      "unrelated entry",
		"content":    "an observation that must never answer for a prompt id",
		"type":       "manual",
		"project":    otherProject,
		"scope":      "project",
	})
	if obsResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating observation, got %d", obsResp.StatusCode)
	}
	obsResp.Body.Close()

	promptResp := postJSON(t, client, ts.URL+"/prompts", map[string]any{
		"session_id": targetSession,
		"content":    promptContent,
		"project":    targetProject,
	})
	if promptResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating prompt, got %d", promptResp.StatusCode)
	}
	created := decodeJSON[map[string]any](t, promptResp)
	if created["status"] != "saved" {
		t.Fatalf("expected saved status, got %v", created["status"])
	}
	promptID, ok := created["id"].(float64)
	if !ok || promptID <= 0 {
		t.Fatalf("expected a positive prompt id, got %v", created["id"])
	}

	// The prompt is retrievable under its own project, and the returned id resolves to the
	// content that was just written — not to some pre-existing row.
	recentResp, err := client.Get(ts.URL + "/prompts/recent?project=" + targetProject)
	if err != nil {
		t.Fatalf("recent prompts: %v", err)
	}
	if recentResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 recent prompts, got %d", recentResp.StatusCode)
	}
	recent := decodeJSON[[]store.Prompt](t, recentResp)
	var saved *store.Prompt
	for i := range recent {
		if recent[i].ID == int64(promptID) {
			saved = &recent[i]
			break
		}
	}
	if saved == nil {
		t.Fatalf("prompt id %d not retrievable from /prompts/recent for project %q (got %d prompts)", int64(promptID), targetProject, len(recent))
	}
	if saved.Content != promptContent {
		t.Fatalf("prompt id %d resolved to unexpected content %q", int64(promptID), saved.Content)
	}
	if saved.Project != targetProject {
		t.Fatalf("expected prompt project %q, got %q", targetProject, saved.Project)
	}
	if saved.SessionID != targetSession {
		t.Fatalf("expected prompt session %q, got %q", targetSession, saved.SessionID)
	}
	// A sync_id is what carries this prompt to the cloud dashboard; without it the row is local-only.
	if strings.TrimSpace(saved.SyncID) == "" {
		t.Fatalf("expected prompt %d to carry a sync_id for cloud replication", int64(promptID))
	}

	// Project scoping: another project must not see this prompt.
	otherResp, err := client.Get(ts.URL + "/prompts/recent?project=" + otherProject)
	if err != nil {
		t.Fatalf("recent prompts other project: %v", err)
	}
	if otherResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 recent prompts for other project, got %d", otherResp.StatusCode)
	}
	for _, p := range decodeJSON[[]store.Prompt](t, otherResp) {
		if p.ID == int64(promptID) {
			t.Fatalf("prompt %d leaked into project %q", int64(promptID), otherProject)
		}
	}

	// Search is the other retrieval surface the dashboard and agents use.
	searchResp, err := client.Get(ts.URL + "/prompts/search?q=rotation&project=" + targetProject + "&limit=5")
	if err != nil {
		t.Fatalf("search prompts: %v", err)
	}
	if searchResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 searching prompts, got %d", searchResp.StatusCode)
	}
	found := false
	for _, p := range decodeJSON[[]store.Prompt](t, searchResp) {
		if p.ID == int64(promptID) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("prompt %d not found via /prompts/search", int64(promptID))
	}

	// The disambiguation #706 asked for: the prompt id is not an observation id. Reading it as one
	// must never answer with this prompt's content.
	obsLookup, err := client.Get(ts.URL + "/observations/" + strconv.FormatInt(int64(promptID), 10))
	if err != nil {
		t.Fatalf("observation lookup: %v", err)
	}
	defer obsLookup.Body.Close()
	if obsLookup.StatusCode == http.StatusOK {
		raw, err := io.ReadAll(obsLookup.Body)
		if err != nil {
			t.Fatalf("read observation lookup: %v", err)
		}
		if strings.Contains(string(raw), promptContent) {
			t.Fatalf("prompt id %d resolved to an observation carrying the prompt content", int64(promptID))
		}
	}
}
