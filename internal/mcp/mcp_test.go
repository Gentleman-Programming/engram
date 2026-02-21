package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alanbuscaglia/engram/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func newMCPTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg := store.DefaultConfig()
	cfg.DataDir = t.TempDir()

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func callResultText(t *testing.T, res *mcppkg.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("expected non-empty tool result")
	}
	text, ok := mcppkg.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("expected text content")
	}
	return text.Text
}

func TestNewServerRegistersTools(t *testing.T) {
	s := newMCPTestStore(t)
	srv := NewServer(s)
	if srv == nil {
		t.Fatalf("expected MCP server instance")
	}
}

func TestHandleSuggestTopicKeyReturnsFamilyBasedKey(t *testing.T) {
	h := handleSuggestTopicKey()
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"type":  "architecture",
		"title": "Auth model",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "Suggested topic_key: architecture/auth-model") {
		t.Fatalf("unexpected suggestion output: %q", text)
	}
}

func TestHandleSuggestTopicKeyRequiresInput(t *testing.T) {
	h := handleSuggestTopicKey()
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error when input is empty")
	}
}

func TestHandleSaveSuggestsTopicKeyWhenMissing(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSave(s)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Auth architecture",
		"content": "Define boundaries for auth middleware",
		"type":    "architecture",
		"project": "engram",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "Suggested topic_key: architecture/auth-architecture") {
		t.Fatalf("expected suggestion in save response, got %q", text)
	}
}

func TestHandleSaveDoesNotSuggestWhenTopicKeyProvided(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSave(s)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":     "Auth architecture",
		"content":   "Define boundaries for auth middleware",
		"type":      "architecture",
		"project":   "engram",
		"topic_key": "architecture/auth-model",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected save error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	if strings.Contains(text, "Suggested topic_key:") {
		t.Fatalf("did not expect suggestion when topic_key provided, got %q", text)
	}
}

func TestHandleCapturePassiveExtractsAndSaves(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleCapturePassive(s)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "## Key Learnings:\n\n1. bcrypt cost=12 is the right balance for our server\n2. JWT refresh tokens need atomic rotation to prevent races\n",
		"project": "engram",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "extracted=2") {
		t.Fatalf("expected extracted=2 in response, got %q", text)
	}
	if !strings.Contains(text, "saved=2") {
		t.Fatalf("expected saved=2 in response, got %q", text)
	}
}

func TestHandleCapturePassiveRequiresContent(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleCapturePassive(s)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"project": "engram",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error when content is missing")
	}
}

func TestHandleCapturePassiveWithNoLearningSection(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleCapturePassive(s)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "plain text without learning headers",
		"project": "engram",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "extracted=0") || !strings.Contains(text, "saved=0") {
		t.Fatalf("expected zero extraction/save counters, got %q", text)
	}
}

func TestHandleCapturePassiveDefaultsSourceAndSession(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleCapturePassive(s)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "## Key Learnings:\n\n1. This learning is long enough to be persisted with default source",
		"project": "engram",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", callResultText(t, res))
	}

	obs, err := s.RecentObservations("engram", "project", 5)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if len(obs) == 0 {
		t.Fatalf("expected at least one observation")
	}
	if obs[0].ToolName == nil || *obs[0].ToolName != "mcp-passive" {
		t.Fatalf("expected default source mcp-passive, got %+v", obs[0].ToolName)
	}
}

func TestHandleCapturePassiveReturnsToolErrorOnStoreFailure(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleCapturePassive(s)

	// Force FK failure: explicit session_id that does not exist.
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"session_id": "missing-session",
		"project":    "engram",
		"content":    "## Key Learnings:\n\n1. This learning is long enough to trigger insert and fail on FK",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error when store returns failure")
	}
}

func TestHelperArgsAndTruncate(t *testing.T) {
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"limit": 7.0,
		"flag":  true,
	}}}

	if got := intArg(req, "limit", 10); got != 7 {
		t.Fatalf("expected intArg=7, got %d", got)
	}
	if got := intArg(req, "missing", 10); got != 10 {
		t.Fatalf("expected default intArg=10, got %d", got)
	}
	if got := boolArg(req, "flag", false); !got {
		t.Fatalf("expected boolArg true")
	}
	if got := boolArg(req, "missing", true); !got {
		t.Fatalf("expected default boolArg=true")
	}

	if got := truncate("short", 10); got != "short" {
		t.Fatalf("unexpected truncate for short input: %q", got)
	}
	if got := truncate("this is long", 4); got != "this..." {
		t.Fatalf("unexpected truncate for long input: %q", got)
	}
}

func TestHandleSearchAndCRUDHandlers(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-mcp", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-mcp",
		Type:      "bugfix",
		Title:     "Fix panic",
		Content:   "Fix panic in parser branch when args are missing",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	search := handleSearch(s)
	searchReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "panic",
		"project": "engram",
		"scope":   "project",
		"limit":   5.0,
	}}}
	searchRes, err := search(context.Background(), searchReq)
	if err != nil {
		t.Fatalf("search handler error: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("unexpected search error: %s", callResultText(t, searchRes))
	}
	if !strings.Contains(callResultText(t, searchRes), "Found 1 memories") {
		t.Fatalf("expected non-empty search result")
	}

	update := handleUpdate(s)
	updateReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":    float64(obsID),
		"title": "Fix parser panic",
	}}}
	updateRes, err := update(context.Background(), updateReq)
	if err != nil {
		t.Fatalf("update handler error: %v", err)
	}
	if updateRes.IsError {
		t.Fatalf("unexpected update error: %s", callResultText(t, updateRes))
	}

	getObs := handleGetObservation(s)
	getReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id": float64(obsID),
	}}}
	getRes, err := getObs(context.Background(), getReq)
	if err != nil {
		t.Fatalf("get handler error: %v", err)
	}
	if getRes.IsError {
		t.Fatalf("unexpected get error: %s", callResultText(t, getRes))
	}

	deleteHandler := handleDelete(s)
	delReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":          float64(obsID),
		"hard_delete": true,
	}}}
	delRes, err := deleteHandler(context.Background(), delReq)
	if err != nil {
		t.Fatalf("delete handler error: %v", err)
	}
	if delRes.IsError {
		t.Fatalf("unexpected delete error: %s", callResultText(t, delRes))
	}
	if !strings.Contains(callResultText(t, delRes), "permanently deleted") {
		t.Fatalf("expected hard delete message")
	}
}

func TestHandlePromptContextStatsTimelineAndSessionHandlers(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-flow", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-flow",
		Type:      "decision",
		Title:     "Auth decision",
		Content:   "Keep auth in middleware",
		Project:   "engram",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	savePrompt := handleSavePrompt(s)
	savePromptReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "how do we fix auth race conditions?",
		"project": "engram",
	}}}
	savePromptRes, err := savePrompt(context.Background(), savePromptReq)
	if err != nil {
		t.Fatalf("save prompt handler error: %v", err)
	}
	if savePromptRes.IsError {
		t.Fatalf("unexpected save prompt error: %s", callResultText(t, savePromptRes))
	}

	contextHandler := handleContext(s)
	contextReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"project": "engram",
		"scope":   "project",
	}}}
	contextRes, err := contextHandler(context.Background(), contextReq)
	if err != nil {
		t.Fatalf("context handler error: %v", err)
	}
	if contextRes.IsError {
		t.Fatalf("unexpected context error: %s", callResultText(t, contextRes))
	}
	if !strings.Contains(callResultText(t, contextRes), "Memory stats") {
		t.Fatalf("expected context output with memory stats")
	}

	statsHandler := handleStats(s)
	statsRes, err := statsHandler(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("stats handler error: %v", err)
	}
	if statsRes.IsError {
		t.Fatalf("unexpected stats error: %s", callResultText(t, statsRes))
	}

	recent, err := s.RecentObservations("engram", "project", 1)
	if err != nil || len(recent) == 0 {
		t.Fatalf("recent observations for timeline: %v len=%d", err, len(recent))
	}

	timelineHandler := handleTimeline(s)
	timelineReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"observation_id": float64(recent[0].ID),
		"before":         2.0,
		"after":          2.0,
	}}}
	timelineRes, err := timelineHandler(context.Background(), timelineReq)
	if err != nil {
		t.Fatalf("timeline handler error: %v", err)
	}
	if timelineRes.IsError {
		t.Fatalf("unexpected timeline error: %s", callResultText(t, timelineRes))
	}

	sessionSummary := handleSessionSummary(s)
	summaryReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"project": "engram",
		"content": "## Goal\nImprove tests",
	}}}
	summaryRes, err := sessionSummary(context.Background(), summaryReq)
	if err != nil {
		t.Fatalf("session summary handler error: %v", err)
	}
	if summaryRes.IsError {
		t.Fatalf("unexpected session summary error: %s", callResultText(t, summaryRes))
	}

	sessionStart := handleSessionStart(s)
	startReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":        "s-new",
		"project":   "engram",
		"directory": "/tmp/engram",
	}}}
	startRes, err := sessionStart(context.Background(), startReq)
	if err != nil {
		t.Fatalf("session start handler error: %v", err)
	}
	if startRes.IsError {
		t.Fatalf("unexpected session start error: %s", callResultText(t, startRes))
	}

	sessionEnd := handleSessionEnd(s)
	endReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":      "s-new",
		"summary": "done",
	}}}
	endRes, err := sessionEnd(context.Background(), endReq)
	if err != nil {
		t.Fatalf("session end handler error: %v", err)
	}
	if endRes.IsError {
		t.Fatalf("unexpected session end error: %s", callResultText(t, endRes))
	}
}

func TestMCPHandlersErrorBranches(t *testing.T) {
	s := newMCPTestStore(t)

	search := handleSearch(s)
	noResultsReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"query": "definitely-no-hit"}}}
	noResultsRes, err := search(context.Background(), noResultsReq)
	if err != nil {
		t.Fatalf("search handler error: %v", err)
	}
	if noResultsRes.IsError {
		t.Fatalf("expected non-error no-results response")
	}
	if !strings.Contains(callResultText(t, noResultsRes), "No memories found") {
		t.Fatalf("expected no memories response")
	}

	update := handleUpdate(s)
	missingIDRes, err := update(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{}}})
	if err != nil {
		t.Fatalf("update missing id error: %v", err)
	}
	if !missingIDRes.IsError {
		t.Fatalf("expected update missing id to return tool error")
	}

	noFieldsReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"id": 1.0}}}
	noFieldsRes, err := update(context.Background(), noFieldsReq)
	if err != nil {
		t.Fatalf("update no fields error: %v", err)
	}
	if !noFieldsRes.IsError {
		t.Fatalf("expected update no fields to return tool error")
	}

	deleteHandler := handleDelete(s)
	delMissingIDRes, err := deleteHandler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{}}})
	if err != nil {
		t.Fatalf("delete missing id error: %v", err)
	}
	if !delMissingIDRes.IsError {
		t.Fatalf("expected delete missing id to return tool error")
	}

	timeline := handleTimeline(s)
	timelineMissingIDRes, err := timeline(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{}}})
	if err != nil {
		t.Fatalf("timeline missing id error: %v", err)
	}
	if !timelineMissingIDRes.IsError {
		t.Fatalf("expected timeline missing id to return tool error")
	}

	getObs := handleGetObservation(s)
	getMissingIDRes, err := getObs(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{}}})
	if err != nil {
		t.Fatalf("get observation missing id error: %v", err)
	}
	if !getMissingIDRes.IsError {
		t.Fatalf("expected get observation missing id to return tool error")
	}

	getNotFoundReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"id": 9999.0}}}
	getNotFoundRes, err := getObs(context.Background(), getNotFoundReq)
	if err != nil {
		t.Fatalf("get observation not found error: %v", err)
	}
	if !getNotFoundRes.IsError {
		t.Fatalf("expected get observation not found to return tool error")
	}
}

func TestMCPHandlersReturnErrorsWhenStoreClosed(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-closed", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-closed",
		Type:      "decision",
		Title:     "Title",
		Content:   "Content",
		Project:   "engram",
	})
	if err != nil {
		t.Fatalf("seed observation: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	searchRes, err := handleSearch(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"query": "title"}}})
	if err != nil {
		t.Fatalf("closed store search call: %v", err)
	}
	if !searchRes.IsError {
		t.Fatalf("expected search to return tool error when store is closed")
	}

	updateRes, err := handleUpdate(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"id": 1.0, "title": "new"}}})
	if err != nil {
		t.Fatalf("closed store update call: %v", err)
	}
	if !updateRes.IsError {
		t.Fatalf("expected update to return tool error when store is closed")
	}

	deleteRes, err := handleDelete(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"id": 1.0}}})
	if err != nil {
		t.Fatalf("closed store delete call: %v", err)
	}
	if !deleteRes.IsError {
		t.Fatalf("expected delete to return tool error when store is closed")
	}

	promptRes, err := handleSavePrompt(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"content": "prompt", "project": "engram"}}})
	if err != nil {
		t.Fatalf("closed store save prompt call: %v", err)
	}
	if !promptRes.IsError {
		t.Fatalf("expected save prompt to return tool error when store is closed")
	}

	contextRes, err := handleContext(s)(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("closed store context call: %v", err)
	}
	if !contextRes.IsError {
		t.Fatalf("expected context to return tool error when store is closed")
	}

	statsRes, err := handleStats(s)(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("closed store stats call: %v", err)
	}
	if statsRes.IsError {
		t.Fatalf("expected stats fallback result even when store is closed")
	}

	timelineRes, err := handleTimeline(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"observation_id": 1.0}}})
	if err != nil {
		t.Fatalf("closed store timeline call: %v", err)
	}
	if !timelineRes.IsError {
		t.Fatalf("expected timeline to return tool error when store is closed")
	}

	getObsRes, err := handleGetObservation(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"id": 1.0}}})
	if err != nil {
		t.Fatalf("closed store get observation call: %v", err)
	}
	if !getObsRes.IsError {
		t.Fatalf("expected get observation to return tool error when store is closed")
	}

	sessionSummaryRes, err := handleSessionSummary(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"project": "engram", "content": "summary"}}})
	if err != nil {
		t.Fatalf("closed store session summary call: %v", err)
	}
	if !sessionSummaryRes.IsError {
		t.Fatalf("expected session summary to return tool error when store is closed")
	}

	sessionStartRes, err := handleSessionStart(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"id": "s1", "project": "engram"}}})
	if err != nil {
		t.Fatalf("closed store session start call: %v", err)
	}
	if !sessionStartRes.IsError {
		t.Fatalf("expected session start to return tool error when store is closed")
	}

	sessionEndRes, err := handleSessionEnd(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"id": "s1"}}})
	if err != nil {
		t.Fatalf("closed store session end call: %v", err)
	}
	if !sessionEndRes.IsError {
		t.Fatalf("expected session end to return tool error when store is closed")
	}
}

func TestMCPAdditionalCoverageBranches(t *testing.T) {
	s := newMCPTestStore(t)

	contextRes, err := handleContext(s)(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("context empty store: %v", err)
	}
	if contextRes.IsError {
		t.Fatalf("expected non-error context for empty store")
	}
	if !strings.Contains(callResultText(t, contextRes), "No previous session memories found") {
		t.Fatalf("expected empty context message")
	}

	statsRes, err := handleStats(s)(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("stats empty store: %v", err)
	}
	if statsRes.IsError {
		t.Fatalf("expected non-error stats for empty store")
	}
	if !strings.Contains(callResultText(t, statsRes), "Projects: none yet") {
		t.Fatalf("expected none yet projects in stats output")
	}

	if err := s.CreateSession("s-extra", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	firstID, err := s.AddObservation(store.AddObservationParams{SessionID: "s-extra", Type: "note", Title: "first", Content: "first content", Project: "engram"})
	if err != nil {
		t.Fatalf("add first: %v", err)
	}
	_, err = s.AddObservation(store.AddObservationParams{SessionID: "s-extra", Type: "note", Title: "second", Content: "second content", Project: "engram"})
	if err != nil {
		t.Fatalf("add second: %v", err)
	}

	timelineReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"observation_id": float64(firstID), "before": 1.0, "after": 2.0}}}
	timelineRes, err := handleTimeline(s)(context.Background(), timelineReq)
	if err != nil {
		t.Fatalf("timeline with header branches: %v", err)
	}
	if timelineRes.IsError {
		t.Fatalf("expected non-error timeline with data")
	}
	text := callResultText(t, timelineRes)
	if !strings.Contains(text, "Session:") || !strings.Contains(text, "After") {
		t.Fatalf("expected timeline session/after sections, got %q", text)
	}

	save := handleSave(s)
	saveReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Default values",
		"content": "Ensure defaults for type and session are used",
		"project": "engram",
	}}}
	saveRes, err := save(context.Background(), saveReq)
	if err != nil {
		t.Fatalf("save defaults: %v", err)
	}
	if saveRes.IsError {
		t.Fatalf("expected save defaults to succeed: %s", callResultText(t, saveRes))
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	saveClosedRes, err := save(context.Background(), saveReq)
	if err != nil {
		t.Fatalf("save closed store call: %v", err)
	}
	if !saveClosedRes.IsError {
		t.Fatalf("expected save to fail when store is closed")
	}
}

func TestHandleSuggestTopicKeyReturnsErrorWhenSuggestionEmpty(t *testing.T) {
	prev := suggestTopicKey
	suggestTopicKey = func(typ, title, content string) string {
		return ""
	}
	t.Cleanup(func() {
		suggestTopicKey = prev
	})

	h := handleSuggestTopicKey()
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title": "valid title",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error when suggestion is empty")
	}
}

func TestHandleUpdateAcceptsAllOptionalFields(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-all-fields", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-all-fields",
		Type:      "decision",
		Title:     "Original",
		Content:   "Original content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	res, err := handleUpdate(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":        float64(id),
		"title":     "Updated",
		"content":   "Updated content",
		"type":      "architecture",
		"project":   "engram",
		"scope":     "personal",
		"topic_key": "architecture/auth-model",
	}}})
	if err != nil {
		t.Fatalf("update handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected update error: %s", callResultText(t, res))
	}
}

func TestHandleContextWithSessionOnlyUsesNoneProjects(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-context-none", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	res, err := handleContext(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"project": "engram",
	}}})
	if err != nil {
		t.Fatalf("context handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected context error: %s", callResultText(t, res))
	}
	if !strings.Contains(callResultText(t, res), "projects: none") {
		t.Fatalf("expected context output with projects: none")
	}
}

func TestHandleStatsReturnsErrorWhenLoaderFails(t *testing.T) {
	prev := loadMCPStats
	loadMCPStats = func(s *store.Store) (*store.Stats, error) {
		return nil, errors.New("stats unavailable")
	}
	t.Cleanup(func() {
		loadMCPStats = prev
	})

	s := newMCPTestStore(t)
	res, err := handleStats(s)(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("stats handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected tool error when stats loader fails")
	}
}

func TestHandleTimelineBeforeSectionAndSummaryBranches(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-timeline", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := s.AddObservation(store.AddObservationParams{SessionID: "s-timeline", Type: "note", Title: "first", Content: "first", Project: "engram"})
	if err != nil {
		t.Fatalf("add first observation: %v", err)
	}
	focusID, err := s.AddObservation(store.AddObservationParams{SessionID: "s-timeline", Type: "note", Title: "second", Content: "second", Project: "engram"})
	if err != nil {
		t.Fatalf("add second observation: %v", err)
	}
	_, err = s.AddObservation(store.AddObservationParams{SessionID: "s-timeline", Type: "note", Title: "third", Content: "third", Project: "engram"})
	if err != nil {
		t.Fatalf("add third observation: %v", err)
	}
	if err := s.EndSession("s-timeline", "timeline summary"); err != nil {
		t.Fatalf("end session: %v", err)
	}

	res, err := handleTimeline(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"observation_id": float64(focusID),
		"before":         2.0,
		"after":          1.0,
	}}})
	if err != nil {
		t.Fatalf("timeline handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected timeline error: %s", callResultText(t, res))
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "timeline summary") || !strings.Contains(text, "Before") {
		t.Fatalf("expected timeline output with summary and before section, got %q", text)
	}
}

func TestHandleGetObservationIncludesTopicAndToolMetadata(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-get-meta", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-get-meta",
		Type:      "architecture",
		Title:     "Auth model",
		Content:   "Details",
		Project:   "engram",
		ToolName:  "mcp-passive",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	res, err := handleGetObservation(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id": float64(id),
	}}})
	if err != nil {
		t.Fatalf("get observation handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected get observation error: %s", callResultText(t, res))
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "Topic: architecture/auth-model") || !strings.Contains(text, "Tool: mcp-passive") {
		t.Fatalf("expected topic and tool metadata in output, got %q", text)
	}
}
