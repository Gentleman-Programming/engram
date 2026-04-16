package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/Gentleman-Programming/engram/internal/types"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func newMCPTestStore(t *testing.T) *store.Store {
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
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

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
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

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
	h := handleCapturePassive(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

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
	h := handleCapturePassive(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

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
	h := handleCapturePassive(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

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
	h := handleCapturePassive(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

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
	h := handleCapturePassive(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

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
	// Multibyte UTF-8 safety
	if got := truncate("Decisión de arquitectura", 8); got != "Decisión..." {
		t.Fatalf("truncate spanish accents = %q, want %q", got, "Decisión...")
	}
	if got := truncate("🐛🔧🚀✨🎉💡", 3); got != "🐛🔧🚀..." {
		t.Fatalf("truncate emoji = %q, want %q", got, "🐛🔧🚀...")
	}
	if got := truncate("café☕latte", 5); got != "café☕..." {
		t.Fatalf("truncate mixed = %q, want %q", got, "café☕...")
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

	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
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

	savePrompt := handleSavePrompt(s, MCPConfig{})
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

	contextHandler := handleContext(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
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

	sessionSummary := handleSessionSummary(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
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

	sessionStart := handleSessionStart(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
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

	sessionEnd := handleSessionEnd(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
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

	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
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

	searchRes, err := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"query": "title"}}})
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

	promptRes, err := handleSavePrompt(s, MCPConfig{})(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"content": "prompt", "project": "engram"}}})
	if err != nil {
		t.Fatalf("closed store save prompt call: %v", err)
	}
	if !promptRes.IsError {
		t.Fatalf("expected save prompt to return tool error when store is closed")
	}

	contextRes, err := handleContext(s, MCPConfig{}, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{})
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

	sessionSummaryRes, err := handleSessionSummary(s, MCPConfig{}, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"project": "engram", "content": "summary"}}})
	if err != nil {
		t.Fatalf("closed store session summary call: %v", err)
	}
	if !sessionSummaryRes.IsError {
		t.Fatalf("expected session summary to return tool error when store is closed")
	}

	sessionStartRes, err := handleSessionStart(s, MCPConfig{}, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"id": "s1", "project": "engram"}}})
	if err != nil {
		t.Fatalf("closed store session start call: %v", err)
	}
	if !sessionStartRes.IsError {
		t.Fatalf("expected session start to return tool error when store is closed")
	}

	sessionEndRes, err := handleSessionEnd(s, MCPConfig{}, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"id": "s1"}}})
	if err != nil {
		t.Fatalf("closed store session end call: %v", err)
	}
	if !sessionEndRes.IsError {
		t.Fatalf("expected session end to return tool error when store is closed")
	}
}

func TestMCPAdditionalCoverageBranches(t *testing.T) {
	s := newMCPTestStore(t)

	contextRes, err := handleContext(s, MCPConfig{}, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{})
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

	save := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
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

	res, err := handleContext(s, MCPConfig{}, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
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
	loadMCPStats = func(s types.StoreInterface) (*store.Stats, error) {
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

// ─── Tool Profile Tests ─────────────────────────────────────────────────────

func TestResolveToolsEmpty(t *testing.T) {
	result := ResolveTools("")
	if result != nil {
		t.Fatalf("expected nil for empty input, got %v", result)
	}
}

func TestResolveToolsAll(t *testing.T) {
	result := ResolveTools("all")
	if result != nil {
		t.Fatalf("expected nil for 'all', got %v", result)
	}
}

func TestResolveToolsAgentProfile(t *testing.T) {
	result := ResolveTools("agent")
	if result == nil {
		t.Fatal("expected non-nil allowlist for 'agent'")
	}

	expectedTools := []string{
		"mem_save", "mem_search", "mem_context", "mem_session_summary",
		"mem_session_start", "mem_session_end", "mem_get_observation",
		"mem_suggest_topic_key", "mem_capture_passive", "mem_save_prompt",
		"mem_update", // skills explicitly say "use mem_update when you have an exact ID to correct"
	}
	for _, tool := range expectedTools {
		if !result[tool] {
			t.Errorf("agent profile missing tool: %s", tool)
		}
	}

	// Admin-only tools should NOT be in agent profile
	adminOnly := []string{"mem_delete", "mem_stats", "mem_timeline"}
	for _, tool := range adminOnly {
		if result[tool] {
			t.Errorf("agent profile should NOT contain admin tool: %s", tool)
		}
	}

	if len(result) != len(expectedTools) {
		t.Errorf("agent profile has %d tools, expected %d", len(result), len(expectedTools))
	}
}

func TestResolveToolsAdminProfile(t *testing.T) {
	result := ResolveTools("admin")
	if result == nil {
		t.Fatal("expected non-nil allowlist for 'admin'")
	}

	expectedTools := []string{"mem_delete", "mem_stats", "mem_timeline", "mem_merge_projects"}
	for _, tool := range expectedTools {
		if !result[tool] {
			t.Errorf("admin profile missing tool: %s", tool)
		}
	}

	if len(result) != len(expectedTools) {
		t.Errorf("admin profile has %d tools, expected %d", len(result), len(expectedTools))
	}
}

func TestResolveToolsCombinedProfiles(t *testing.T) {
	result := ResolveTools("agent,admin")
	if result == nil {
		t.Fatal("expected non-nil allowlist for combined profiles")
	}

	// Should have all 15 tools
	allTools := []string{
		"mem_save", "mem_search", "mem_context", "mem_session_summary",
		"mem_session_start", "mem_session_end", "mem_get_observation",
		"mem_suggest_topic_key", "mem_capture_passive", "mem_save_prompt",
		"mem_update", "mem_delete", "mem_stats", "mem_timeline", "mem_merge_projects",
	}
	for _, tool := range allTools {
		if !result[tool] {
			t.Errorf("combined profile missing tool: %s", tool)
		}
	}
}

func TestResolveToolsIndividualNames(t *testing.T) {
	result := ResolveTools("mem_save,mem_search")
	if result == nil {
		t.Fatal("expected non-nil allowlist")
	}

	if !result["mem_save"] || !result["mem_search"] {
		t.Fatalf("expected mem_save and mem_search, got %v", result)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 tools, got %d", len(result))
	}
}

func TestResolveToolsMixedProfileAndNames(t *testing.T) {
	result := ResolveTools("admin,mem_save")
	if result == nil {
		t.Fatal("expected non-nil allowlist")
	}

	// Should have admin tools + mem_save
	if !result["mem_save"] {
		t.Error("missing mem_save")
	}
	if !result["mem_stats"] {
		t.Error("missing mem_stats from admin profile")
	}
	if !result["mem_timeline"] {
		t.Error("missing mem_timeline from admin profile")
	}
}

func TestResolveToolsAllInMixed(t *testing.T) {
	result := ResolveTools("agent,all")
	if result != nil {
		t.Fatalf("expected nil when 'all' is in the mix, got %v", result)
	}
}

func TestResolveToolsWhitespace(t *testing.T) {
	result := ResolveTools("  agent  ")
	if result == nil {
		t.Fatal("expected non-nil for agent with whitespace")
	}
	if !result["mem_save"] {
		t.Error("agent profile should include mem_save")
	}
}

func TestResolveToolsCommaWhitespace(t *testing.T) {
	result := ResolveTools("mem_save , mem_search")
	if result == nil {
		t.Fatal("expected non-nil allowlist")
	}
	if !result["mem_save"] || !result["mem_search"] {
		t.Fatalf("expected both tools, got %v", result)
	}
}

func TestResolveToolsEmptyTokenBetweenCommas(t *testing.T) {
	result := ResolveTools("mem_save,,mem_search")
	if result == nil {
		t.Fatal("expected non-nil allowlist")
	}
	if !result["mem_save"] || !result["mem_search"] {
		t.Fatalf("expected mem_save and mem_search in result, got %v", result)
	}
}

func TestResolveToolsAllAfterRealTool(t *testing.T) {
	result := ResolveTools("mem_save,all")
	if result != nil {
		t.Fatalf("expected nil when 'all' appears anywhere in list, got %v", result)
	}
}

func TestResolveToolsOnlyCommas(t *testing.T) {
	result := ResolveTools(",,,")
	if result != nil {
		t.Fatalf("expected nil when input is only commas (empty tokens), got %v", result)
	}
}

func TestShouldRegisterNilAllowlist(t *testing.T) {
	if !shouldRegister("anything", nil) {
		t.Error("nil allowlist should allow everything")
	}
}

func TestShouldRegisterWithAllowlist(t *testing.T) {
	allowlist := map[string]bool{"mem_save": true, "mem_search": true}

	if !shouldRegister("mem_save", allowlist) {
		t.Error("mem_save should be allowed")
	}
	if shouldRegister("mem_delete", allowlist) {
		t.Error("mem_delete should NOT be allowed")
	}
}

func TestNewServerWithToolsAgentProfile(t *testing.T) {
	s := newMCPTestStore(t)
	allowlist := ResolveTools("agent")

	srv := NewServerWithTools(s, allowlist)
	if srv == nil {
		t.Fatal("expected MCP server instance")
	}

	tools := srv.ListTools()

	// Agent tools should be present (11 tools)
	agentTools := []string{
		"mem_save", "mem_search", "mem_context", "mem_session_summary",
		"mem_session_start", "mem_session_end", "mem_get_observation",
		"mem_suggest_topic_key", "mem_capture_passive", "mem_save_prompt",
		"mem_update",
	}
	for _, name := range agentTools {
		if tools[name] == nil {
			t.Errorf("agent profile: expected tool %q to be registered", name)
		}
	}

	// Admin-only tools should NOT be present
	adminTools := []string{"mem_delete", "mem_stats", "mem_timeline"}
	for _, name := range adminTools {
		if tools[name] != nil {
			t.Errorf("agent profile: tool %q should NOT be registered", name)
		}
	}
}

func TestNewServerWithToolsAdminProfile(t *testing.T) {
	s := newMCPTestStore(t)
	allowlist := ResolveTools("admin")

	srv := NewServerWithTools(s, allowlist)
	if srv == nil {
		t.Fatal("expected MCP server instance")
	}

	tools := srv.ListTools()

	// Admin tools should be present (4 tools)
	adminTools := []string{"mem_delete", "mem_stats", "mem_timeline", "mem_merge_projects"}
	for _, name := range adminTools {
		if tools[name] == nil {
			t.Errorf("admin profile: expected tool %q to be registered", name)
		}
	}

	// Agent-only tools should NOT be present
	agentOnlyTools := []string{"mem_save", "mem_search", "mem_context", "mem_update"}
	for _, name := range agentOnlyTools {
		if tools[name] != nil {
			t.Errorf("admin profile: tool %q should NOT be registered", name)
		}
	}
}

func TestNewServerWithToolsNilRegistersAll(t *testing.T) {
	s := newMCPTestStore(t)

	srv := NewServerWithTools(s, nil)
	if srv == nil {
		t.Fatal("expected MCP server instance")
	}

	tools := srv.ListTools()

	allTools := []string{
		"mem_save", "mem_search", "mem_context", "mem_session_summary",
		"mem_session_start", "mem_session_end", "mem_get_observation",
		"mem_suggest_topic_key", "mem_capture_passive", "mem_save_prompt",
		"mem_update", "mem_delete", "mem_stats", "mem_timeline", "mem_merge_projects",
	}

	for _, name := range allTools {
		if tools[name] == nil {
			t.Errorf("nil allowlist: expected tool %q to be registered", name)
		}
	}

	if len(tools) != len(allTools) {
		t.Errorf("expected %d tools with nil allowlist, got %d", len(allTools), len(tools))
	}
}

func TestNewServerWithToolsIndividualSelection(t *testing.T) {
	s := newMCPTestStore(t)
	allowlist := ResolveTools("mem_save,mem_search")

	srv := NewServerWithTools(s, allowlist)
	tools := srv.ListTools()

	if tools["mem_save"] == nil {
		t.Error("expected mem_save to be registered")
	}
	if tools["mem_search"] == nil {
		t.Error("expected mem_search to be registered")
	}
	if len(tools) != 2 {
		t.Errorf("expected exactly 2 tools, got %d", len(tools))
	}
}

func TestNewServerBackwardsCompatible(t *testing.T) {
	s := newMCPTestStore(t)

	// NewServer (no tools filter) should register all tools
	srv := NewServer(s)
	tools := srv.ListTools()

	// 11 agent + 4 admin = 15 total
	if len(tools) != 15 {
		t.Errorf("NewServer should register all 15 tools, got %d", len(tools))
	}
}

func TestProfileConsistency(t *testing.T) {
	// Verify that agent + admin = all 15 tools
	combined := make(map[string]bool)
	for tool := range ProfileAgent {
		combined[tool] = true
	}
	for tool := range ProfileAdmin {
		combined[tool] = true
	}

	if len(combined) != 15 {
		t.Errorf("agent + admin should cover all 15 tools, got %d", len(combined))
	}

	// Verify no overlap between profiles
	for tool := range ProfileAgent {
		if ProfileAdmin[tool] {
			t.Errorf("tool %q appears in both agent and admin profiles", tool)
		}
	}
}

// ─── Server Instructions ─────────────────────────────────────────────────────

func TestServerInstructionsConstantIsNonEmpty(t *testing.T) {
	if serverInstructions == "" {
		t.Fatal("serverInstructions should not be empty — it drives Tool Search discovery")
	}
	// Must mention key tool names so Tool Search can index them
	for _, keyword := range []string{"mem_save", "mem_search", "mem_context", "mem_session_summary"} {
		if !strings.Contains(serverInstructions, keyword) {
			t.Errorf("serverInstructions should mention %q for Tool Search indexing", keyword)
		}
	}
}

// ─── Tool Annotations ────────────────────────────────────────────────────────

func TestCoreToolsAreNotDeferred(t *testing.T) {
	s := newMCPTestStore(t)
	srv := NewServer(s)
	tools := srv.ListTools()

	coreTools := []string{
		"mem_save", "mem_search", "mem_context", "mem_session_summary",
		"mem_get_observation", "mem_save_prompt",
	}
	for _, name := range coreTools {
		tool := tools[name]
		if tool == nil {
			t.Errorf("core tool %q should be registered", name)
			continue
		}
		if tool.Tool.DeferLoading {
			t.Errorf("core tool %q should NOT have DeferLoading=true — it must always be in context", name)
		}
	}
}

func TestNonCoreToolsAreDeferred(t *testing.T) {
	s := newMCPTestStore(t)
	srv := NewServer(s)
	tools := srv.ListTools()

	deferredTools := []string{
		"mem_update", "mem_suggest_topic_key",
		"mem_session_start", "mem_session_end",
		"mem_stats", "mem_delete", "mem_timeline",
		"mem_capture_passive", "mem_merge_projects",
	}
	for _, name := range deferredTools {
		tool := tools[name]
		if tool == nil {
			t.Errorf("deferred tool %q should be registered", name)
			continue
		}
		if !tool.Tool.DeferLoading {
			t.Errorf("non-core tool %q should have DeferLoading=true", name)
		}
	}
}

func TestAllToolsHaveAnnotations(t *testing.T) {
	s := newMCPTestStore(t)
	srv := NewServer(s)
	tools := srv.ListTools()

	for name, tool := range tools {
		ann := tool.Tool.Annotations
		if ann.Title == "" {
			t.Errorf("tool %q should have a Title annotation", name)
		}
		// Every tool must explicitly set ReadOnlyHint and DestructiveHint
		if ann.ReadOnlyHint == nil {
			t.Errorf("tool %q should have ReadOnlyHint set", name)
		}
		if ann.DestructiveHint == nil {
			t.Errorf("tool %q should have DestructiveHint set", name)
		}
	}
}

func TestReadOnlyToolAnnotations(t *testing.T) {
	s := newMCPTestStore(t)
	srv := NewServer(s)
	tools := srv.ListTools()

	readOnlyTools := []string{
		"mem_search", "mem_context", "mem_get_observation",
		"mem_suggest_topic_key", "mem_stats", "mem_timeline",
	}
	for _, name := range readOnlyTools {
		tool := tools[name]
		if tool == nil {
			continue
		}
		ann := tool.Tool.Annotations
		if ann.ReadOnlyHint == nil || !*ann.ReadOnlyHint {
			t.Errorf("tool %q should be marked readOnly", name)
		}
		if ann.DestructiveHint == nil || *ann.DestructiveHint {
			t.Errorf("tool %q should NOT be marked destructive", name)
		}
	}
}

// ─── Issue #25: Session collision regression tests ──────────────────────────

func TestDefaultSessionIDScopedByProject(t *testing.T) {
	if got := defaultSessionID(""); got != "manual-save" {
		t.Fatalf("expected manual-save for empty project, got %q", got)
	}
	if got := defaultSessionID("engram"); got != "manual-save-engram" {
		t.Fatalf("expected manual-save-engram, got %q", got)
	}
	if got := defaultSessionID("my-app"); got != "manual-save-my-app" {
		t.Fatalf("expected manual-save-my-app, got %q", got)
	}
}

func TestHandleSaveCreatesProjectScopedSession(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// Save from project A without session_id
	reqA := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Decision A",
		"content": "Architecture for project A",
		"type":    "architecture",
		"project": "projectA",
	}}}
	resA, err := h(context.Background(), reqA)
	if err != nil || resA.IsError {
		t.Fatalf("save A: err=%v isError=%v", err, resA.IsError)
	}

	// Save from project B without session_id
	reqB := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Decision B",
		"content": "Architecture for project B",
		"type":    "architecture",
		"project": "projectB",
	}}}
	resB, err := h(context.Background(), reqB)
	if err != nil || resB.IsError {
		t.Fatalf("save B: err=%v isError=%v", err, resB.IsError)
	}

	// Verify separate sessions exist for each project
	// Note: project names are normalized to lowercase, so projectA → projecta
	sessA, err := s.GetSession("manual-save-projecta")
	if err != nil {
		t.Fatalf("expected session manual-save-projecta to exist: %v", err)
	}
	if sessA.Project != "projecta" {
		t.Fatalf("expected project=projecta (normalized), got %q", sessA.Project)
	}

	sessB, err := s.GetSession("manual-save-projectb")
	if err != nil {
		t.Fatalf("expected session manual-save-projectb to exist: %v", err)
	}
	if sessB.Project != "projectb" {
		t.Fatalf("expected project=projectb (normalized), got %q", sessB.Project)
	}
}

func TestHandleSavePromptCreatesProjectScopedSession(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSavePrompt(s, MCPConfig{})

	reqA := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "How do I set up auth?",
		"project": "alpha",
	}}}
	resA, err := h(context.Background(), reqA)
	if err != nil || resA.IsError {
		t.Fatalf("save prompt A: err=%v isError=%v", err, resA.IsError)
	}

	reqB := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "How do I deploy?",
		"project": "beta",
	}}}
	resB, err := h(context.Background(), reqB)
	if err != nil || resB.IsError {
		t.Fatalf("save prompt B: err=%v isError=%v", err, resB.IsError)
	}

	if _, err := s.GetSession("manual-save-alpha"); err != nil {
		t.Fatalf("expected session manual-save-alpha: %v", err)
	}
	if _, err := s.GetSession("manual-save-beta"); err != nil {
		t.Fatalf("expected session manual-save-beta: %v", err)
	}
}

func TestHandleSessionSummaryCreatesProjectScopedSession(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSessionSummary(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "Worked on auth module",
		"project": "gamma",
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("session summary: err=%v isError=%v", err, res.IsError)
	}

	if _, err := s.GetSession("manual-save-gamma"); err != nil {
		t.Fatalf("expected session manual-save-gamma: %v", err)
	}
}

func TestHandleCapturePassiveCreatesProjectScopedSession(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleCapturePassive(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "## Key Learnings:\nAuth needs rate limiting",
		"project": "delta",
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("capture passive: err=%v isError=%v text=%s", err, res.IsError, callResultText(t, res))
	}

	if _, err := s.GetSession("manual-save-delta"); err != nil {
		t.Fatalf("expected session manual-save-delta: %v", err)
	}
}

func TestExplicitSessionIDBypassesDefault(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// Provide explicit session_id — should NOT use defaultSessionID
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":      "Explicit session test",
		"content":    "Testing explicit session ID",
		"type":       "discovery",
		"project":    "myproject",
		"session_id": "custom-session-123",
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("save: err=%v isError=%v", err, res.IsError)
	}

	// Should use the explicit session, NOT "manual-save-myproject"
	if _, err := s.GetSession("custom-session-123"); err != nil {
		t.Fatalf("expected custom-session-123: %v", err)
	}
	// The default session should NOT exist
	_, err = s.GetSession("manual-save-myproject")
	if err == nil {
		t.Fatal("manual-save-myproject should NOT exist when explicit session_id provided")
	}
}

func TestDestructiveToolAnnotation(t *testing.T) {
	s := newMCPTestStore(t)
	srv := NewServer(s)
	tools := srv.ListTools()

	tool := tools["mem_delete"]
	if tool == nil {
		t.Fatal("mem_delete should be registered")
	}
	ann := tool.Tool.Annotations
	if ann.DestructiveHint == nil || !*ann.DestructiveHint {
		t.Error("mem_delete should be marked destructive")
	}
	if ann.ReadOnlyHint == nil || *ann.ReadOnlyHint {
		t.Error("mem_delete should NOT be marked readOnly")
	}
}

// ─── Phase 3: MCPConfig, Default Project, Normalization, Similar Warnings ────

func TestNewServerWithConfig(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "engram"}
	srv := NewServerWithConfig(s, cfg, nil)
	if srv == nil {
		t.Fatal("expected MCP server instance")
	}
	tools := srv.ListTools()
	// Should have all 15 tools
	if len(tools) != 15 {
		t.Errorf("NewServerWithConfig should register all 15 tools, got %d", len(tools))
	}
}

func TestHandleSaveDefaultProjectFillIn(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "myproject"}
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	// Send empty project — should use default
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Test memory",
		"content": "Some content here",
		// no project field
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	// Verify observation was stored with default project
	obs, err := s.RecentObservations("myproject", "project", 5)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if len(obs) == 0 {
		t.Fatal("expected at least one observation stored with default project")
	}
	if obs[0].Project == nil || *obs[0].Project != "myproject" {
		t.Fatalf("expected project=myproject, got %v", obs[0].Project)
	}
}

func TestHandleSaveNormalizationWarning(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// Send mixed-case project name — should be normalized and warning returned
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Normalization test",
		"content": "Testing project name normalization",
		"project": "Engram", // uppercase — should normalize to "engram"
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "normalized") {
		t.Fatalf("expected normalization warning in response, got %q", text)
	}

	// Verify observation was stored with normalized project name
	obs, err := s.RecentObservations("engram", "project", 5)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if len(obs) == 0 {
		t.Fatal("expected observation stored under normalized project name 'engram'")
	}
}

func TestHandleSaveSimilarProjectWarning(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// First save to "engram" to establish an existing project
	req1 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "First memory",
		"content": "Memory for engram project",
		"project": "engram",
	}}}
	res1, err := h(context.Background(), req1)
	if err != nil || res1.IsError {
		t.Fatalf("first save: err=%v isError=%v text=%s", err, res1.IsError, callResultText(t, res1))
	}

	// Now save to "engam" (typo) — should warn about similar project "engram"
	req2 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Typo project memory",
		"content": "Memory saved under typo project name",
		"project": "engam", // typo — Levenshtein distance 1 from "engram"
	}}}
	res2, err := h(context.Background(), req2)
	if err != nil {
		t.Fatalf("second save handler error: %v", err)
	}
	if res2.IsError {
		t.Fatalf("unexpected error on second save: %s", callResultText(t, res2))
	}

	text := callResultText(t, res2)
	if !strings.Contains(text, "Similar project") {
		t.Fatalf("expected similar project warning, got %q", text)
	}
	// Verify spec-compliant format: ⚠️ emoji, obs count, and "Consider using"
	if !strings.Contains(text, "⚠️") {
		t.Errorf("expected ⚠️ emoji in warning, got %q", text)
	}
	if !strings.Contains(text, "memories") {
		t.Errorf("expected observation count (memories) in warning, got %q", text)
	}
	if !strings.Contains(text, "Consider using") {
		t.Errorf("expected 'Consider using' in warning, got %q", text)
	}
}

func TestHandleSaveNoSimilarWarningWhenProjectExists(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// Save twice to the same project — second save should NOT show similar project warning
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "First memory",
		"content": "Memory content",
		"project": "engram",
	}}}
	h(context.Background(), req) // first save establishes the project

	req2 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Second memory",
		"content": "Another memory content",
		"project": "engram",
	}}}
	res2, err := h(context.Background(), req2)
	if err != nil || res2.IsError {
		t.Fatalf("second save: err=%v", err)
	}

	text := callResultText(t, res2)
	if strings.Contains(text, "Similar project") {
		t.Fatalf("unexpected similar project warning on existing project, got %q", text)
	}
}

func TestHandleMergeProjects(t *testing.T) {
	s := newMCPTestStore(t)

	// Set up observations under different project name variants
	if err := s.CreateSession("s-Engram", "Engram", ""); err != nil {
		t.Fatalf("create session Engram: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-Engram",
		Type:      "decision",
		Title:     "From Engram",
		Content:   "Content from Engram",
		Project:   "engram", // store normalizes to lowercase
	}); err != nil {
		t.Fatalf("add observation Engram: %v", err)
	}

	if err := s.CreateSession("s-engram-memory", "engram-memory", ""); err != nil {
		t.Fatalf("create session engram-memory: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-engram-memory",
		Type:      "decision",
		Title:     "From engram-memory",
		Content:   "Content from engram-memory",
		Project:   "engram-memory",
	}); err != nil {
		t.Fatalf("add observation engram-memory: %v", err)
	}

	h := handleMergeProjects(s)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"from": "engram-memory, ENGRAM", // comma-separated, with spaces and uppercase
		"to":   "engram",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "engram") {
		t.Fatalf("expected merge result mentioning canonical project, got %q", text)
	}
	if !strings.Contains(text, "Observations moved") {
		t.Fatalf("expected observations count in result, got %q", text)
	}

	// Verify that engram-memory observations are now under "engram"
	obs, err := s.RecentObservations("engram", "project", 10)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	// Should have both: original "engram" obs + migrated "engram-memory" obs
	if len(obs) < 2 {
		t.Fatalf("expected at least 2 observations after merge, got %d", len(obs))
	}
}

func TestHandleMergeProjectsRequiresFromAndTo(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleMergeProjects(s)

	// Missing "from"
	res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"to": "engram",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error when 'from' is missing")
	}

	// Missing "to"
	res, err = h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"from": "engram-old",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error when 'to' is missing")
	}

	// Empty from after parsing
	res, err = h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"from": "  , , ",
		"to":   "engram",
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error when all 'from' values are empty after trimming")
	}
}

func TestHandleMergeProjectsIsInAdminProfile(t *testing.T) {
	s := newMCPTestStore(t)
	allowlist := ResolveTools("admin")
	srv := NewServerWithTools(s, allowlist)
	tools := srv.ListTools()

	if tools["mem_merge_projects"] == nil {
		t.Fatal("mem_merge_projects should be in admin profile")
	}

	// Verify it's marked destructive
	tool := tools["mem_merge_projects"]
	ann := tool.Tool.Annotations
	if ann.DestructiveHint == nil || !*ann.DestructiveHint {
		t.Error("mem_merge_projects should be marked destructive")
	}
}

func TestHandleMergeProjectsIsDeferred(t *testing.T) {
	s := newMCPTestStore(t)
	srv := NewServer(s)
	tools := srv.ListTools()

	tool := tools["mem_merge_projects"]
	if tool == nil {
		t.Fatal("mem_merge_projects should be registered")
	}
	if !tool.Tool.DeferLoading {
		t.Error("mem_merge_projects should have DeferLoading=true")
	}
}

func TestHandleSaveDefaultProjectDoesNotOverrideExplicit(t *testing.T) {
	s := newMCPTestStore(t)
	cfg := MCPConfig{DefaultProject: "default-project"}
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	// Explicit project should override default
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Explicit project test",
		"content": "Should go to explicit-project, not default-project",
		"project": "explicit-project",
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("save: err=%v", err)
	}

	// Verify it went to explicit-project, NOT default-project
	obs, err := s.RecentObservations("explicit-project", "project", 5)
	if err != nil || len(obs) == 0 {
		t.Fatal("expected observation in explicit-project")
	}
	defaultObs, err := s.RecentObservations("default-project", "project", 5)
	if err != nil {
		t.Fatalf("lookup default-project: %v", err)
	}
	if len(defaultObs) > 0 {
		t.Fatal("observation should NOT be in default-project")
	}
}

func TestSearchResponseIncludesNudgeAfterInactivity(t *testing.T) {
	s := newMCPTestStore(t)

	// Seed a memory to search for
	s.CreateSession("s1", "myproject", "")
	s.AddObservation(store.AddObservationParams{
		SessionID: "s1",
		Type:      "manual",
		Title:     "test memory",
		Content:   "some content",
		Project:   "myproject",
	})

	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	activity := NewSessionActivity(10 * time.Minute)
	activity.now = func() time.Time { return now }

	sessionID := defaultSessionID("myproject")

	// Simulate prior activity: > 5 tool calls so nudge criteria is met
	for i := 0; i < 6; i++ {
		activity.RecordToolCall(sessionID)
	}

	// Advance time past nudge threshold
	now = now.Add(15 * time.Minute)

	search := handleSearch(s, MCPConfig{}, activity)
	res, err := search(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"query":   "test memory",
			"project": "myproject",
		}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "No mem_save calls for this project") {
		t.Fatalf("expected nudge in search response, got: %q", text)
	}
}

func TestSessionSummaryResponseIncludesActivityScore(t *testing.T) {
	s := newMCPTestStore(t)

	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	activity := NewSessionActivity(10 * time.Minute)
	activity.now = func() time.Time { return now }

	// Use defaultSessionID so we test the real wiring — session summary
	// looks up activity via defaultSessionID(project), not an explicit session_id.
	project := "myproject"
	sessionID := defaultSessionID(project)

	// Simulate activity
	for i := 0; i < 12; i++ {
		activity.RecordToolCall(sessionID)
	}
	activity.RecordSave(sessionID)
	activity.RecordSave(sessionID)

	summary := handleSessionSummary(s, MCPConfig{}, activity)
	res, err := summary(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"project": project,
			"content": "## Goal\nTest session",
		}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "Session activity:") {
		t.Fatalf("expected activity score in session summary response, got: %q", text)
	}
	if !strings.Contains(text, "12 tool calls") {
		t.Fatalf("expected 12 tool calls in score, got: %q", text)
	}
	if !strings.Contains(text, "2 saves") {
		t.Fatalf("expected 2 saves in score, got: %q", text)
	}
}

func TestSessionEndClearsActivity(t *testing.T) {
	s := newMCPTestStore(t)

	activity := NewSessionActivity(10 * time.Minute)
	project := "myproject"
	sessionID := defaultSessionID(project)

	// Record some activity
	activity.RecordToolCall(sessionID)
	activity.RecordSave(sessionID)

	// Verify activity exists
	score := activity.ActivityScore(sessionID)
	if score == "" {
		t.Fatal("expected activity score before session end")
	}

	// Create session in store so EndSession works
	s.CreateSession("real-session-id", project, "")

	end := handleSessionEnd(s, MCPConfig{DefaultProject: project}, activity)
	_, err := end(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id": "real-session-id",
		}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	// Activity should be cleared
	score = activity.ActivityScore(sessionID)
	if score != "" {
		t.Fatalf("expected empty activity after session end, got: %q", score)
	}
}

func TestCapturePassiveRecordsToolCall(t *testing.T) {
	s := newMCPTestStore(t)

	activity := NewSessionActivity(10 * time.Minute)
	project := "myproject"
	sessionID := defaultSessionID(project)

	capture := handleCapturePassive(s, MCPConfig{DefaultProject: project}, activity)
	_, err := capture(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"content": "## Key Learnings:\n1. Test learning",
			"project": project,
		}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	// Verify tool call was recorded
	score := activity.ActivityScore(sessionID)
	if !strings.Contains(score, "1 tool call") {
		t.Fatalf("expected 1 tool call recorded for capture passive, got: %q", score)
	}
}

func TestSessionStartUsesDefaultSessionID(t *testing.T) {
	s := newMCPTestStore(t)

	activity := NewSessionActivity(10 * time.Minute)
	project := "myproject"

	start := handleSessionStart(s, MCPConfig{}, activity)
	_, err := start(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id":      "real-unique-session-id",
			"project": project,
		}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	// Activity should be recorded under defaultSessionID, not the real session ID
	defaultSID := defaultSessionID(project)
	score := activity.ActivityScore(defaultSID)
	if !strings.Contains(score, "1 tool call") {
		t.Fatalf("expected activity under defaultSessionID, got: %q", score)
	}

	// The real session ID should NOT have activity
	realScore := activity.ActivityScore("real-unique-session-id")
	if realScore != "" {
		t.Fatalf("expected no activity under real session ID, got: %q", realScore)
	}
}

// ─── T33: NewServerWithConfig accepts types.StoreInterface ───────────────────

// mcpStoreInterfaceStub satisfies types.StoreInterface with no-op implementations.
// It is NOT a *store.Store — used to verify type-assertion safety in MCP.
type mcpStoreInterfaceStub struct{}

func (s *mcpStoreInterfaceStub) GetObservation(id int64) (*store.Observation, error) {
	return &store.Observation{ID: id}, nil
}
func (s *mcpStoreInterfaceStub) Search(query string, opts store.SearchOptions) ([]store.SearchResult, error) {
	return nil, nil
}
func (s *mcpStoreInterfaceStub) RecentSessions(project string, limit int) ([]store.SessionSummary, error) {
	return nil, nil
}
func (s *mcpStoreInterfaceStub) AllSessions(project string, limit int) ([]store.SessionSummary, error) {
	return nil, nil
}
func (s *mcpStoreInterfaceStub) SessionObservations(sessionID string, limit int) ([]store.Observation, error) {
	return nil, nil
}
func (s *mcpStoreInterfaceStub) RecentObservations(project, scope string, limit int) ([]store.Observation, error) {
	return nil, nil
}
func (s *mcpStoreInterfaceStub) AllObservations(project, scope string, limit int) ([]store.Observation, error) {
	return nil, nil
}
func (s *mcpStoreInterfaceStub) FormatContext(project, scope string) (string, error) { return "", nil }
func (s *mcpStoreInterfaceStub) Timeline(observationID int64, before, after int) (*store.TimelineResult, error) {
	return &store.TimelineResult{}, nil
}
func (s *mcpStoreInterfaceStub) Stats() (*store.Stats, error) { return &store.Stats{}, nil }
func (s *mcpStoreInterfaceStub) RecentPrompts(project string, limit int) ([]store.Prompt, error) {
	return nil, nil
}
func (s *mcpStoreInterfaceStub) SearchPrompts(query, project string, limit int) ([]store.Prompt, error) {
	return nil, nil
}
func (s *mcpStoreInterfaceStub) CreateSession(id, project, directory string) error { return nil }
func (s *mcpStoreInterfaceStub) EndSession(id, summary string) error               { return nil }
func (s *mcpStoreInterfaceStub) DeleteSession(id string) error                     { return nil }
func (s *mcpStoreInterfaceStub) AddObservation(p store.AddObservationParams) (int64, error) {
	return 1, nil
}
func (s *mcpStoreInterfaceStub) UpdateObservation(id int64, p store.UpdateObservationParams) (*store.Observation, error) {
	return &store.Observation{ID: id}, nil
}
func (s *mcpStoreInterfaceStub) DeleteObservation(id int64, hardDelete bool) error { return nil }
func (s *mcpStoreInterfaceStub) AddPrompt(p store.AddPromptParams) (int64, error)  { return 1, nil }
func (s *mcpStoreInterfaceStub) DeletePrompt(id int64) error                       { return nil }
func (s *mcpStoreInterfaceStub) PassiveCapture(p store.PassiveCaptureParams) (*store.PassiveCaptureResult, error) {
	return &store.PassiveCaptureResult{}, nil
}
func (s *mcpStoreInterfaceStub) MigrateProject(oldName, newName string) (*store.MigrateResult, error) {
	return &store.MigrateResult{}, nil
}

func TestNewServerWithConfigAcceptsStoreInterface(t *testing.T) {
	// NewServerWithConfig must accept types.StoreInterface, not just *store.Store.
	stub := &mcpStoreInterfaceStub{}
	srv := NewServerWithConfig(stub, MCPConfig{DefaultProject: "test"}, nil)
	if srv == nil {
		t.Fatal("expected non-nil MCP server from non-*store.Store implementation")
	}
}

func TestMergeProjectsToolReturnsErrorWithNonLocalStore(t *testing.T) {
	// When MCP server is backed by a non-*store.Store, mem_merge_projects must
	// return a tool error (not panic), since MergeProjects is local-only.
	stub := &mcpStoreInterfaceStub{}
	activity := NewSessionActivity(10 * time.Minute)
	srv := newServerWithActivity(stub, MCPConfig{}, nil, activity)
	if srv == nil {
		t.Fatal("expected non-nil server")
	}

	handler := handleMergeProjects(stub)
	req := mcppkg.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"from": "project-a",
		"to":   "project-b",
	}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.IsError {
		t.Fatalf("expected tool error result for non-local store MergeProjects, got: %+v", result)
	}
}
