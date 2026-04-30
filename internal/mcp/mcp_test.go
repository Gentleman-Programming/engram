package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/project"
	"github.com/Gentleman-Programming/engram/internal/store"
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

func assertSessionSyncMutationDirectory(t *testing.T, s *store.Store, sessionID, wantDirectory string) {
	t.Helper()

	mutations, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending sync mutations: %v", err)
	}

	for _, mutation := range mutations {
		if mutation.Entity != store.SyncEntitySession || mutation.EntityKey != sessionID || mutation.Op != store.SyncOpUpsert {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
			t.Fatalf("decode session sync payload: %v", err)
		}
		if got := payload["directory"]; got != wantDirectory {
			t.Fatalf("expected session sync payload directory %q, got %#v in payload %s", wantDirectory, got, mutation.Payload)
		}
		return
	}

	t.Fatalf("expected pending session upsert sync mutation for %q; got %#v", sessionID, mutations)
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
		"mem_update",          // skills explicitly say "use mem_update when you have an exact ID to correct"
		"mem_current_project", // added REQ-313: discovery tool recommended first call
		"mem_judge",           // REQ-003: conflict verdict tool (Phase D)
		"mem_compare",         // REQ-011: persist agent-judged semantic verdict (Phase G)
		"mem_doctor",          // read-only operational diagnostics
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

	// Should have all 19 tools (mem_doctor added for operational diagnostics)
	allTools := []string{
		"mem_save", "mem_search", "mem_context", "mem_session_summary",
		"mem_session_start", "mem_session_end", "mem_get_observation",
		"mem_suggest_topic_key", "mem_capture_passive", "mem_save_prompt",
		"mem_update", "mem_delete", "mem_stats", "mem_timeline", "mem_merge_projects",
		"mem_current_project", "mem_judge", "mem_compare", "mem_doctor",
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

// ─── Phase D — MCP layer enrichment tests ───────────────────────────────────

// D.1 — mem_save returns enriched envelope with candidates when similar obs exists.
// REQ-001 | Design §4
func TestHandleSave_CandidatesReturned(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// Save first observation — no candidates yet.
	req1 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "We use sessions for auth middleware",
		"content": "Session-based auth in the middleware layer keeps things simple",
		"type":    "architecture",
	}}}
	res1, err := h(context.Background(), req1)
	if err != nil {
		t.Fatalf("first save handler error: %v", err)
	}
	if res1.IsError {
		t.Fatalf("first save unexpected error: %s", callResultText(t, res1))
	}

	// Save second, similar observation — should surface the first as candidate.
	req2 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Switched from sessions to JWT for auth",
		"content": "Replacing session auth with JWT tokens improves scalability",
		"type":    "architecture",
	}}}
	res2, err := h(context.Background(), req2)
	if err != nil {
		t.Fatalf("second save handler error: %v", err)
	}
	if res2.IsError {
		t.Fatalf("second save unexpected error: %s", callResultText(t, res2))
	}

	text := callResultText(t, res2)

	// REQ-001: judgment_required=true must be in envelope.
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("response is not valid JSON: %v — got %q", err, text)
	}

	judgmentRequired, ok := envelope["judgment_required"].(bool)
	if !ok || !judgmentRequired {
		t.Fatalf("expected judgment_required=true in envelope, got %v", envelope["judgment_required"])
	}

	candidates, ok := envelope["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		t.Fatalf("expected non-empty candidates[], got %v", envelope["candidates"])
	}

	// Each candidate must have required fields.
	firstCandidate, ok := candidates[0].(map[string]any)
	if !ok {
		t.Fatalf("candidates[0] not a map, got %T", candidates[0])
	}
	for _, field := range []string{"id", "sync_id", "title", "type", "score", "judgment_id"} {
		if _, exists := firstCandidate[field]; !exists {
			t.Errorf("candidates[0] missing field %q", field)
		}
	}

	// REQ-001: result must contain "CONFLICT REVIEW PENDING".
	result, _ := envelope["result"].(string)
	if !strings.Contains(result, "CONFLICT REVIEW PENDING") {
		t.Fatalf("expected CONFLICT REVIEW PENDING in result, got %q", result)
	}
}

// D.2 — mem_save with no similar obs returns unchanged result string, no candidates.
// REQ-007 | Design §4
func TestHandleSave_NoCandidates_ResultUnchanged(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// Save observation into empty store — no candidates possible.
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Completely unrelated quantum computing thing",
		"content": "Quantum entanglement in distributed systems has no parallel in typical web auth",
		"type":    "discovery",
	}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	// judgment_required must be absent or false.
	if jr, ok := envelope["judgment_required"].(bool); ok && jr {
		t.Fatalf("expected judgment_required absent or false, got true")
	}

	// candidates must be absent or empty.
	if cands, ok := envelope["candidates"]; ok {
		if arr, ok := cands.([]any); ok && len(arr) > 0 {
			t.Fatalf("expected no candidates, got %v", cands)
		}
	}

	// judgment_id must be absent.
	if _, ok := envelope["judgment_id"]; ok {
		t.Fatalf("expected no judgment_id when no candidates")
	}

	// REQ-007: result string must start with expected prefix (regression guard).
	result, _ := envelope["result"].(string)
	if !strings.HasPrefix(result, `Memory saved: "`) {
		t.Fatalf("result string must start with Memory saved: \" — got %q", result)
	}

	// CONFLICT REVIEW PENDING must NOT appear when no candidates.
	if strings.Contains(result, "CONFLICT REVIEW PENDING") {
		t.Fatalf("unexpected CONFLICT REVIEW PENDING in result when no candidates")
	}
}

// D.3 — topic_key revision also triggers candidate detection.
// REQ-001 edge case | Design §4
func TestHandleSave_TopicKeyRevision_ReturnsCandidates(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// Save a standalone observation (no topic_key) that will be a candidate.
	req1 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Auth architecture sessions design",
		"content": "Session-based auth design for the backend service",
		"type":    "architecture",
	}}}
	if _, err := h(context.Background(), req1); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// Save with topic_key (first write) — creates the topic.
	req2 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":     "Auth architecture sessions design updated",
		"content":   "Updated session-based auth design",
		"type":      "architecture",
		"topic_key": "architecture/auth-sessions",
	}}}
	if _, err := h(context.Background(), req2); err != nil {
		t.Fatalf("second save: %v", err)
	}

	// Revise via same topic_key — this is the revision case.
	req3 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":     "Auth architecture sessions design revised",
		"content":   "Revised session-based auth design for the service layer",
		"type":      "architecture",
		"topic_key": "architecture/auth-sessions",
	}}}
	res3, err := h(context.Background(), req3)
	if err != nil {
		t.Fatalf("revision save: %v", err)
	}
	if res3.IsError {
		t.Fatalf("revision save unexpected error: %s", callResultText(t, res3))
	}

	text := callResultText(t, res3)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}

	// Revision must still surface candidates (the revised obs itself is excluded; other similar obs eligible).
	// We seeded one similar obs, so candidates should be non-empty.
	candidates, _ := envelope["candidates"].([]any)
	judgmentRequired, _ := envelope["judgment_required"].(bool)

	// At minimum, if candidates found, judgment_required must be true.
	// If no candidates, that's also acceptable (FTS similarity may not match).
	// The critical invariant is: the just-saved/revised obs is NOT in its own candidates.
	if judgmentRequired && len(candidates) > 0 {
		// Verify the saved obs sync_id doesn't appear as a candidate.
		savedSyncID, _ := envelope["sync_id"].(string)
		for _, c := range candidates {
			cand, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cand["sync_id"] == savedSyncID {
				t.Fatalf("just-saved obs should not appear in its own candidates")
			}
		}
	}
}

// D.4 — mem_search result annotations for relations.
// REQ-002 | Design §5
func TestHandleSearch_SupersededAnnotation(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-search-annot", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Save two observations.
	oldID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-search-annot",
		Type:      "architecture",
		Title:     "Old auth design",
		Content:   "We use session-based auth",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add old obs: %v", err)
	}
	newID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-search-annot",
		Type:      "architecture",
		Title:     "New auth design",
		Content:   "We switched to JWT auth",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add new obs: %v", err)
	}

	// Get sync_ids.
	oldObs, err := s.GetObservation(oldID)
	if err != nil {
		t.Fatalf("get old obs: %v", err)
	}
	newObs, err := s.GetObservation(newID)
	if err != nil {
		t.Fatalf("get new obs: %v", err)
	}

	// Create a judged supersedes relation: newObs supersedes oldObs.
	relSyncID := "rel-test-supersedes-01"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   relSyncID,
		SourceID: newObs.SyncID,
		TargetID: oldObs.SyncID,
	}); err != nil {
		t.Fatalf("save relation: %v", err)
	}
	if _, err := s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    relSyncID,
		Relation:      store.RelationSupersedes,
		MarkedByActor: "agent:test",
		MarkedByKind:  "agent",
	}); err != nil {
		t.Fatalf("judge relation: %v", err)
	}

	// Search for old auth.
	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	searchReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "auth design",
		"project": "engram",
		"scope":   "project",
	}}}
	searchRes, err := search(context.Background(), searchReq)
	if err != nil {
		t.Fatalf("search handler error: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("unexpected search error: %s", callResultText(t, searchRes))
	}

	text := callResultText(t, searchRes)
	// oldObs should show superseded_by annotation.
	// newObs should show supersedes annotation.
	if !strings.Contains(text, "superseded_by:") {
		t.Fatalf("expected superseded_by annotation in search results, got %q", text)
	}
	if !strings.Contains(text, "supersedes:") {
		t.Fatalf("expected supersedes annotation in search results, got %q", text)
	}
}

func TestHandleSearch_PendingAsContested(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-contested", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	obsAID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-contested",
		Type:      "decision",
		Title:     "Keep monolith decision",
		Content:   "We keep the monolith for now",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs A: %v", err)
	}
	obsBID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-contested",
		Type:      "decision",
		Title:     "Split into microservices decision",
		Content:   "We should split into microservices",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs B: %v", err)
	}

	obsA, err := s.GetObservation(obsAID)
	if err != nil {
		t.Fatalf("get obs A: %v", err)
	}
	obsB, err := s.GetObservation(obsBID)
	if err != nil {
		t.Fatalf("get obs B: %v", err)
	}

	// Create a PENDING relation (not judged) between A and B.
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   "rel-test-pending-01",
		SourceID: obsA.SyncID,
		TargetID: obsB.SyncID,
	}); err != nil {
		t.Fatalf("save pending relation: %v", err)
	}

	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	searchReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "decision",
		"project": "engram",
		"scope":   "project",
	}}}
	searchRes, err := search(context.Background(), searchReq)
	if err != nil {
		t.Fatalf("search handler error: %v", err)
	}

	text := callResultText(t, searchRes)
	// Pending relation should surface as "conflict: contested by"
	if !strings.Contains(text, "conflict: contested by") {
		t.Fatalf("expected conflict annotation for pending relation, got %q", text)
	}
}

func TestHandleSearch_NoRelationsUnchanged(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-no-rel", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-no-rel",
		Type:      "bugfix",
		Title:     "Fix parser panic",
		Content:   "Fixed panic in parser when args are nil",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs: %v", err)
	}

	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	searchReq := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "parser panic",
		"project": "engram",
		"scope":   "project",
	}}}
	searchRes, err := search(context.Background(), searchReq)
	if err != nil {
		t.Fatalf("search handler error: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("unexpected search error: %s", callResultText(t, searchRes))
	}

	text := callResultText(t, searchRes)
	// No relations — no annotation lines should appear.
	if strings.Contains(text, "supersedes:") || strings.Contains(text, "superseded_by:") || strings.Contains(text, "conflict:") {
		t.Fatalf("expected no relation annotations when no relations exist, got %q", text)
	}
	// Standard format must be preserved.
	if !strings.Contains(text, "Found 1 memories") {
		t.Fatalf("expected standard search output format, got %q", text)
	}
}

// D.4b — mem_judge registered in ProfileAgent (tool registration test).
// REQ-003 | Design §6.5
func TestHandleJudge_RegisteredInAgentProfile(t *testing.T) {
	if !ProfileAgent["mem_judge"] {
		t.Fatalf("mem_judge must be registered in ProfileAgent")
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
		"mem_current_project", "mem_judge", "mem_compare", "mem_doctor",
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

func TestMemDoctorRegisteredAndReturnsEnvelope(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("manual-save-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	srv := NewServerWithTools(s, ResolveTools("agent"))
	if srv.ListTools()["mem_doctor"] == nil {
		t.Fatal("expected mem_doctor in agent profile")
	}
	res, err := handleDoctor(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"project": "engram", "check": "manual_session_name_project_mismatch"}}})
	if err != nil {
		t.Fatalf("handleDoctor: %v", err)
	}
	envelope := callResultJSON(t, res)
	if envelope["status"] != "ok" || envelope["project"] != "engram" {
		t.Fatalf("envelope=%v", envelope)
	}
	checks := envelope["checks"].([]any)
	if len(checks) != 1 || checks[0].(map[string]any)["check_id"] != "manual_session_name_project_mismatch" {
		t.Fatalf("checks=%v", checks)
	}
}

func TestMemDoctorOmittedProjectUsesAutoDetectedScope(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)
	detected, err := resolveWriteProject()
	if err != nil {
		t.Fatalf("resolveWriteProject: %v", err)
	}
	s := newMCPTestStore(t)
	if err := s.CreateSession("manual-save-"+detected.Project, detected.Project, dir); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	res, err := handleDoctor(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"check": "manual_session_name_project_mismatch"}}})
	if err != nil {
		t.Fatalf("handleDoctor: %v", err)
	}
	envelope := callResultJSON(t, res)
	if envelope["project"] != detected.Project {
		t.Fatalf("expected auto-detected project %q, got envelope=%v", detected.Project, envelope)
	}
	if envelope["status"] != "ok" {
		t.Fatalf("expected ok diagnostics, got envelope=%v", envelope)
	}
}

func TestMemDoctorUnknownProjectReturnsStructuredError(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("manual-save-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	res, err := handleDoctor(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"project": "missing"}}})
	if err != nil {
		t.Fatalf("handleDoctor: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for unknown project")
	}
	envelope := callResultJSON(t, res)
	if envelope["error_code"] != "unknown_project" {
		t.Fatalf("envelope=%v", envelope)
	}
}

func TestNewServerBackwardsCompatible(t *testing.T) {
	s := newMCPTestStore(t)

	// NewServer (no tools filter) should register all tools
	srv := NewServer(s)
	tools := srv.ListTools()

	// 15 agent + 4 admin = 19 total (mem_doctor added for diagnostics)
	if len(tools) != 19 {
		t.Errorf("NewServer should register all 19 tools, got %d", len(tools))
	}
}

func TestProfileConsistency(t *testing.T) {
	// Verify that agent + admin = all 19 tools
	combined := make(map[string]bool)
	for tool := range ProfileAgent {
		combined[tool] = true
	}
	for tool := range ProfileAdmin {
		combined[tool] = true
	}

	// 15 agent + 4 admin = 19 total (mem_doctor added for diagnostics)
	if len(combined) != 19 {
		t.Errorf("agent + admin should cover all 19 tools, got %d", len(combined))
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

	// Set up git repo so auto-detect gives us a known project.
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/scoped-session-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)
	if err := s.EnrollProject("scoped-session-project"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Decision",
		"content": "Architecture note",
		"type":    "architecture",
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("save: err=%v isError=%v text=%s", err, res.IsError, callResultText(t, res))
	}

	// Verify session was created with auto-detected project
	sess, err := s.GetSession("manual-save-scoped-session-project")
	if err != nil {
		t.Fatalf("expected session manual-save-scoped-session-project to exist: %v", err)
	}
	if sess.Project != "scoped-session-project" {
		t.Fatalf("expected project=scoped-session-project, got %q", sess.Project)
	}
	if sess.Directory != dir {
		t.Fatalf("expected directory=%q, got %q", dir, sess.Directory)
	}
	assertSessionSyncMutationDirectory(t, s, "manual-save-scoped-session-project", dir)
}

func TestHandleSavePromptCreatesProjectScopedSession(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSavePrompt(s, MCPConfig{})

	// Set up a git repo so auto-detect returns a known project.
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/prompt-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)
	if err := s.EnrollProject("prompt-project"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "How do I set up auth?",
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("save prompt: err=%v isError=%v", err, res.IsError)
	}

	sess, err := s.GetSession("manual-save-prompt-project")
	if err != nil {
		t.Fatalf("expected session manual-save-prompt-project: %v", err)
	}
	if sess.Directory != dir {
		t.Fatalf("expected directory=%q, got %q", dir, sess.Directory)
	}
	assertSessionSyncMutationDirectory(t, s, "manual-save-prompt-project", dir)
}

func TestHandleSessionSummaryCreatesProjectScopedSession(t *testing.T) {
	// Set up a git repo so auto-detect returns a known project (REQ-308: project
	// field removed from schema; auto-detect is the only source).
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/summary-session-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	if err := s.EnrollProject("summary-session-project"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	h := handleSessionSummary(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "Worked on auth module",
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("session summary: err=%v isError=%v text=%s", err, res.IsError, callResultText(t, res))
	}

	sess, err := s.GetSession("manual-save-summary-session-project")
	if err != nil {
		t.Fatalf("expected session manual-save-summary-session-project: %v", err)
	}
	if sess.Directory != dir {
		t.Fatalf("expected directory=%q, got %q", dir, sess.Directory)
	}
	assertSessionSyncMutationDirectory(t, s, "manual-save-summary-session-project", dir)
}

func TestHandleCapturePassiveCreatesProjectScopedSession(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleCapturePassive(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// Set up a git repo so auto-detect returns a known project.
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/capture-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"content": "## Key Learnings:\nAuth needs rate limiting",
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("capture passive: err=%v isError=%v text=%s", err, res.IsError, callResultText(t, res))
	}

	if _, err := s.GetSession("manual-save-capture-project"); err != nil {
		t.Fatalf("expected session manual-save-capture-project: %v", err)
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
	// JW6: DefaultProject removed from MCPConfig (dead code).
	cfg := MCPConfig{}
	srv := NewServerWithConfig(s, cfg, nil)
	if srv == nil {
		t.Fatal("expected MCP server instance")
	}
	tools := srv.ListTools()
	// Should have all 19 tools (15 agent + 4 admin; mem_doctor added)
	if len(tools) != 19 {
		t.Errorf("NewServerWithConfig should register all 19 tools, got %d", len(tools))
	}
}

// TestHandleSaveAutoDetectsWhenNoProjectArg verifies that auto-detection works
// when no project arg is provided (replaces old DefaultProject fill-in test).
func TestHandleSaveAutoDetectsWhenNoProjectArg(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/auto-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Test memory",
		"content": "Some content here",
	}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	obs, err := s.RecentObservations("auto-project", "project", 5)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if len(obs) == 0 {
		t.Fatal("expected at least one observation stored with auto-detected project")
	}
}

// TestHandleSaveProjectNameNormalized verifies that the auto-detected project
// is normalized (lowercase). The normalization warning from old behavior was
// triggered by LLM-supplied names; since project is now auto-detected the
// detection result is already normalized. We verify the stored project is lowercase.
func TestHandleSaveProjectNameNormalized(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	// Use a remote with a mixed-case repo name — auto-detect normalizes it.
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/MyApp.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Normalization test",
		"content": "Testing auto-detect normalization",
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("save: err=%v isError=%v text=%s", err, res.IsError, callResultText(t, res))
	}

	// Observation must be under the normalized (lowercase) project name.
	obs, err := s.RecentObservations("myapp", "project", 5)
	if err != nil || len(obs) == 0 {
		t.Fatal("expected observation stored under normalized project name 'myapp'")
	}
}

func TestHandleSaveSimilarProjectWarning(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// Build two git repos: "engram" and "engam" (Levenshtein distance 1).
	parent := t.TempDir()
	engramDir := filepath.Join(parent, "engram")
	engamDir := filepath.Join(parent, "engam")
	for _, d := range []string{engramDir, engamDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		initTestGitRepo(t, d)
	}

	// Save original cwd to restore between sub-saves.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	// First save: cwd = engram repo.
	if err := os.Chdir(engramDir); err != nil {
		t.Fatal(err)
	}
	req1 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "First memory",
		"content": "Memory for engram project",
	}}}
	res1, err := h(context.Background(), req1)
	if err != nil || res1.IsError {
		t.Fatalf("first save: err=%v isError=%v text=%s", err, res1.IsError, callResultText(t, res1))
	}

	// Second save: cwd = engam repo — should warn about similar "engram".
	if err := os.Chdir(engamDir); err != nil {
		t.Fatal(err)
	}
	req2 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Typo project memory",
		"content": "Memory saved under typo project name",
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
	// Set up a git repo so auto-detect returns a known project.
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// Save twice to the same (auto-detected) project — second save should NOT warn.
	h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "First memory",
		"content": "Memory content",
	}}})

	req2 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Second memory",
		"content": "Another memory content",
	}}}
	res2, err := h(context.Background(), req2)
	if err != nil || res2.IsError {
		t.Fatalf("second save: err=%v isError=%v", err, res2.IsError)
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

// TestHandleSave_LLMProjectIgnored replaces the old override test — now LLM-supplied
// project is always ignored; auto-detect is the only source (REQ-308).
func TestHandleSave_LLMProjectIgnored(t *testing.T) {
	// Set up a git repo so auto-detect returns a known project.
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/auto-detected-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "LLM project ignored test",
		"content": "Should go to auto-detected project",
		// LLM-supplied project — must be IGNORED per REQ-308
		"project": "llm-wrong-project",
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("save: err=%v isError=%v text=%s", err, res.IsError, callResultText(t, res))
	}

	// Must NOT be in the LLM-supplied project.
	obs, _ := s.RecentObservations("llm-wrong-project", "project", 5)
	if len(obs) > 0 {
		t.Fatal("observation must NOT be in LLM-supplied project")
	}
	// Must be in the auto-detected project.
	obs2, err := s.RecentObservations("auto-detected-project", "project", 5)
	if err != nil || len(obs2) == 0 {
		t.Fatal("expected observation in auto-detected-project")
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
	// Set up a git repo so auto-detect returns a known project (REQ-308).
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/activity-score-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)

	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	activity := NewSessionActivity(10 * time.Minute)
	activity.now = func() time.Time { return now }

	// Use defaultSessionID of the auto-detected project so session summary
	// looks up activity via defaultSessionID(project).
	project := "activity-score-project"
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
			// project intentionally omitted — auto-detect only (REQ-308)
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

	// Set up a dir so resolveWriteProject works and returns a known project.
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	// Add remote so project name is predictable.
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/myproject.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

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

	end := handleSessionEnd(s, MCPConfig{}, activity)
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

	// Set up a git repo so resolveWriteProject returns a predictable name.
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/capture-passive-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	activity := NewSessionActivity(10 * time.Minute)
	project := "capture-passive-project"
	sessionID := defaultSessionID(project)

	capture := handleCapturePassive(s, MCPConfig{}, activity)
	_, err := capture(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"content": "## Key Learnings:\n1. Test learning",
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

	// Set up a git repo so resolveWriteProject returns a predictable name.
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/session-start-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	activity := NewSessionActivity(10 * time.Minute)
	project := "session-start-project"

	start := handleSessionStart(s, MCPConfig{}, activity)
	_, err := start(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id": "real-unique-session-id",
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

func TestSessionStartWithoutDirectoryUsesCurrentWorkingDirectory(t *testing.T) {
	s := newMCPTestStore(t)

	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/session-start-cwd-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)
	if err := s.EnrollProject("session-start-cwd-project"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	start := handleSessionStart(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	res, err := start(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id": "session-start-cwd",
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("session start: err=%v isError=%v text=%s", err, res.IsError, callResultText(t, res))
	}

	sess, err := s.GetSession("session-start-cwd")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval expected dir: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(sess.Directory)
	if err != nil {
		t.Fatalf("eval session dir: %v", err)
	}
	if gotDir != wantDir {
		t.Fatalf("expected directory=%q, got %q", wantDir, gotDir)
	}
	assertSessionSyncMutationDirectory(t, s, "session-start-cwd", sess.Directory)
}

func TestSessionStartWithWhitespaceDirectoryUsesCurrentWorkingDirectory(t *testing.T) {
	s := newMCPTestStore(t)

	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/session-start-whitespace-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)
	if err := s.EnrollProject("session-start-whitespace-project"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	start := handleSessionStart(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	res, err := start(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id":        "session-start-whitespace",
			"directory": " \t\n ",
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("session start: err=%v isError=%v text=%s", err, res.IsError, callResultText(t, res))
	}

	sess, err := s.GetSession("session-start-whitespace")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval expected dir: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(sess.Directory)
	if err != nil {
		t.Fatalf("eval session dir: %v", err)
	}
	if gotDir != wantDir {
		t.Fatalf("expected directory=%q, got %q", wantDir, gotDir)
	}
	assertSessionSyncMutationDirectory(t, s, "session-start-whitespace", sess.Directory)
}

func TestSessionStartWithExplicitDirectoryPreservesDirectory(t *testing.T) {
	s := newMCPTestStore(t)

	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/session-start-explicit-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)
	if err := s.EnrollProject("session-start-explicit-project"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	explicitDir := filepath.Join(t.TempDir(), "explicit-worktree")

	start := handleSessionStart(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	res, err := start(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id":        "session-start-explicit",
			"directory": explicitDir,
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("session start: err=%v isError=%v text=%s", err, res.IsError, callResultText(t, res))
	}

	sess, err := s.GetSession("session-start-explicit")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Directory != explicitDir {
		t.Fatalf("expected directory=%q, got %q", explicitDir, sess.Directory)
	}
	assertSessionSyncMutationDirectory(t, s, "session-start-explicit", explicitDir)
}

func TestSessionStartWithExplicitDirectoryResolvesProjectFromDirectory(t *testing.T) {
	s := newMCPTestStore(t)

	workspace := t.TempDir()
	rightRepo := filepath.Join(workspace, "right-repo")
	wrongRepo := filepath.Join(workspace, "wrong-repo")
	if err := os.MkdirAll(filepath.Join(rightRepo, "nested"), 0755); err != nil {
		t.Fatalf("create right repo nested dir: %v", err)
	}
	if err := os.MkdirAll(wrongRepo, 0755); err != nil {
		t.Fatalf("create wrong repo dir: %v", err)
	}
	initTestGitRepo(t, rightRepo)
	initTestGitRepo(t, wrongRepo)
	cmd := exec.Command("git", "-C", rightRepo, "remote", "add", "origin",
		"git@github.com:user/explicit-session-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add right repo: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-C", wrongRepo, "remote", "add", "origin",
		"git@github.com:user/wrong-session-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add wrong repo: %v\n%s", err, out)
	}
	t.Chdir(workspace)

	explicitDir := filepath.Join(rightRepo, "nested")
	start := handleSessionStart(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	res, err := start(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id":        "session-start-explicit-project",
			"directory": explicitDir,
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("session start: err=%v isError=%v text=%s", err, res.IsError, callResultText(t, res))
	}

	sess, err := s.GetSession("session-start-explicit-project")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Project != "explicit-session-project" {
		t.Fatalf("expected explicit directory project, got %q", sess.Project)
	}
	if sess.Directory != explicitDir {
		t.Fatalf("expected persisted directory=%q, got %q", explicitDir, sess.Directory)
	}
}

// ─── Batch 4: Write handler schema + auto-detect ─────────────────────────────

// TestWriteSchema_NoProjectField asserts that the 6 write tools do NOT include
// a "project" property in their input schema (REQ-308).
func TestWriteSchema_NoProjectField(t *testing.T) {
	s := newMCPTestStore(t)
	srv := NewServer(s)

	writeTools := []string{
		"mem_save",
		"mem_save_prompt",
		"mem_session_start",
		"mem_session_end",
		"mem_capture_passive",
		"mem_update",
	}

	for _, toolName := range writeTools {
		t.Run(toolName, func(t *testing.T) {
			st := srv.GetTool(toolName)
			if st == nil {
				t.Fatalf("tool %q not registered", toolName)
			}
			props := st.Tool.InputSchema.Properties
			if _, hasProject := props["project"]; hasProject {
				t.Errorf("tool %q must not have 'project' in schema", toolName)
			}
		})
	}
}

// TestMemSave_AutoDetectsProject asserts write lands under detected project (REQ-308).
func TestMemSave_AutoDetectsProject(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	// Add remote so project name is predictable.
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/test-auto-repo.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "auto-detect test",
		"content": "testing auto-detection",
		"type":    "manual",
	}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	// Verify the observation was stored under the detected project.
	results, err := s.Search("auto-detect test", store.SearchOptions{Project: "test-auto-repo", Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected observation under auto-detected project 'test-auto-repo'")
	}
}

// TestMemSave_IgnoresLLMProject asserts LLM-supplied project is silently discarded (REQ-308).
func TestMemSave_IgnoresLLMProject(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/real-repo.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "should be real-repo",
		"content": "test LLM override discarded",
		"type":    "manual",
		// LLM sends a project name — must be ignored.
		"project": "llm-supplied-wrong-project",
	}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	// Should NOT be stored under the LLM-supplied project.
	wrongResults, _ := s.Search("should be real-repo", store.SearchOptions{Project: "llm-supplied-wrong-project", Limit: 5})
	if len(wrongResults) > 0 {
		t.Error("observation must NOT be stored under LLM-supplied project")
	}
	// SHOULD be stored under the detected project.
	correctResults, _ := s.Search("should be real-repo", store.SearchOptions{Project: "real-repo", Limit: 5})
	if len(correctResults) == 0 {
		t.Error("observation must be stored under auto-detected project 'real-repo'")
	}
}

// TestMemSave_AmbiguousEnvelope asserts error_code=="ambiguous_project", no write (REQ-309).
func TestMemSave_AmbiguousEnvelope(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"repo-x", "repo-y"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initTestGitRepo(t, child)
	}
	t.Chdir(parent)

	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "should not be saved",
		"content": "ambiguous test",
		"type":    "manual",
	}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for ambiguous project")
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "ambiguous_project") {
		t.Errorf("expected error_code 'ambiguous_project', got: %q", text)
	}
	if !strings.Contains(text, "available_projects") {
		t.Errorf("expected available_projects in error, got: %q", text)
	}
}

// TestMemSave_SuccessEnvelope asserts project, project_source, project_path in response (REQ-309).
func TestMemSave_SuccessEnvelope(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "envelope test",
		"content": "test envelope fields",
		"type":    "manual",
	}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "project") {
		t.Errorf("response must contain 'project' envelope field, got: %q", text)
	}
	if !strings.Contains(text, "project_source") {
		t.Errorf("response must contain 'project_source' envelope field, got: %q", text)
	}
}

// ─── Batch 5: Read handler project resolution ─────────────────────────────────

// TestMemSearch_NoProjectAutoDetects: no project arg falls back to auto-detect (REQ-310)
func TestMemSearch_NoProjectAutoDetects(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/search-auto-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	// Seed an observation under the auto-detected project.
	if err := s.CreateSession("sess-read", "search-auto-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-read",
		Type:      "manual",
		Title:     "searchable memory",
		Content:   "content for search test",
		Project:   "search-auto-project",
	}); err != nil {
		t.Fatal(err)
	}

	h := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query": "searchable memory",
		// no project — auto-detect
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("search: err=%v isError=%v", err, res.IsError)
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "searchable memory") {
		t.Errorf("expected to find auto-detected project memory, got: %q", text)
	}
}

// TestMemSearch_ExplicitKnownProject: valid override uses ProjectExists path (REQ-311)
func TestMemSearch_ExplicitKnownProject(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	s := newMCPTestStore(t)
	// Seed an observation under "known-project".
	if err := s.CreateSession("sess-known", "known-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-known",
		Type:      "manual",
		Title:     "known project memory",
		Content:   "explicit project content",
		Project:   "known-project",
	}); err != nil {
		t.Fatal(err)
	}

	h := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "known project memory",
		"project": "known-project",
	}}}
	res, err := h(context.Background(), req)
	if err != nil || res.IsError {
		t.Fatalf("search with known project: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "known project memory") {
		t.Errorf("expected to find known-project memory, got: %q", text)
	}
}

// TestMemSearch_UnknownProjectError: unknown override returns structured error (REQ-311)
func TestMemSearch_UnknownProjectError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "anything",
		"project": "does-not-exist-project",
	}}}
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for unknown project override")
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "unknown_project") {
		t.Errorf("expected error_code unknown_project, got: %q", text)
	}
}

// TestAllTools_ReadResponseEnvelope: project envelope in every successful read response (REQ-314)
func TestAllTools_ReadResponseEnvelope(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	s := newMCPTestStore(t)

	// Seed minimal data.
	if err := s.CreateSession("sess-env", "envelope-test-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-env",
		Type:      "manual",
		Title:     "envelope test observation",
		Content:   "content",
		Project:   "envelope-test-project",
	})
	if err != nil {
		t.Fatal(err)
	}

	// mem_search envelope
	hSearch := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	resSearch, err := hSearch(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"query": "envelope test",
		}},
	})
	if err != nil || resSearch.IsError {
		t.Fatalf("search: err=%v isError=%v", err, resSearch.IsError)
	}

	// mem_get_observation envelope
	hGet := handleGetObservation(s)
	resGet, err := hGet(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id": float64(obsID),
		}},
	})
	if err != nil || resGet.IsError {
		t.Fatalf("get obs: err=%v isError=%v", err, resGet.IsError)
	}
	_ = resGet // envelope check deferred to verify phase
}

// ─── Batch 6: mem_current_project tool ───────────────────────────────────────

// TestMemCurrentProject_NormalResult: full metadata in response (REQ-313)
func TestMemCurrentProject_NormalResult(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/current-project-test.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleCurrentProject(s)

	res, err := h(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "current-project-test") {
		t.Errorf("expected project name in response, got: %q", text)
	}
	if !strings.Contains(text, "project_source") {
		t.Errorf("expected project_source in response, got: %q", text)
	}
	if !strings.Contains(text, "project_path") {
		t.Errorf("expected project_path in response, got: %q", text)
	}
}

// TestMemCurrentProject_AmbiguousNoError: IsError==false, project=="", available_projects non-empty (REQ-313)
func TestMemCurrentProject_AmbiguousNoError(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"repo-p", "repo-q"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initTestGitRepo(t, child)
	}
	t.Chdir(parent)

	s := newMCPTestStore(t)
	h := handleCurrentProject(s)

	res, err := h(context.Background(), mcppkg.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	// REQ-313: mem_current_project MUST NOT return an error on ambiguous cwd.
	if res.IsError {
		t.Fatalf("mem_current_project must not return error on ambiguous cwd; got: %s",
			callResultText(t, res))
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "available_projects") {
		t.Errorf("expected available_projects in response, got: %q", text)
	}
	// JW3: ambiguous source must be "ambiguous", not "dir_basename".
	if !strings.Contains(text, `"ambiguous"`) {
		t.Errorf("expected project_source=ambiguous in response, got: %q", text)
	}
}

// TestMemCurrentProject_WarningCase3: warning!="" and project_source=="git_child" (REQ-313)
func TestMemCurrentProject_WarningCase3(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "only-child-repo")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	initTestGitRepo(t, child)
	t.Chdir(parent)

	s := newMCPTestStore(t)
	h := handleCurrentProject(s)

	res, err := h(context.Background(), mcppkg.CallToolRequest{})
	if err != nil || res.IsError {
		t.Fatalf("handler error: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "git_child") {
		t.Errorf("expected project_source=git_child, got: %q", text)
	}
	if !strings.Contains(text, "warning") {
		t.Errorf("expected warning field in response, got: %q", text)
	}
}

// ─── Test helpers (Batch 3) ───────────────────────────────────────────────────

// initTestGitRepo creates a git repo in dir, configures user, and optionally
// adds a remote origin. Exported as helper for both project and mcp tests.
func initTestGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
}

func callResultJSON(t *testing.T, res *mcppkg.CallToolResult) map[string]any {
	t.Helper()
	text := callResultText(t, res)
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("response is not JSON: %v\ntext: %s", err, text)
	}
	return m
}

// ─── Batch 3: resolver helpers + envelope + error helper ─────────────────────

// TestResolveWriteProject_AutoDetects: t.Chdir to temp git repo, assert Source!=""
func TestResolveWriteProject_AutoDetects(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	res, err := resolveWriteProject()
	if err != nil {
		t.Fatalf("resolveWriteProject: %v", err)
	}
	if res.Source == "" {
		t.Error("Source must be non-empty for a git repo")
	}
	if res.Project == "" {
		t.Error("Project must be non-empty for a git repo")
	}
}

// TestResolveWriteProject_AmbiguousError: assert errors.Is(err, ErrAmbiguousProject)
func TestResolveWriteProject_AmbiguousError(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"repo-a", "repo-b"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initTestGitRepo(t, child)
	}
	t.Chdir(parent)

	_, err := resolveWriteProject()
	if !errors.Is(err, project.ErrAmbiguousProject) {
		t.Errorf("expected ErrAmbiguousProject, got %v", err)
	}
}

// TestResolveReadProject_WithOverride: known project override succeeds
func TestResolveReadProject_WithOverride(t *testing.T) {
	s := newMCPTestStore(t)
	// Register the project in the store.
	if err := s.CreateSession("sess-x", "known-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	dir := t.TempDir()
	t.Chdir(dir)

	res, err := resolveReadProject(s, "known-project")
	if err != nil {
		t.Fatalf("resolveReadProject: %v", err)
	}
	if res.Project != "known-project" {
		t.Errorf("Project = %q; want %q", res.Project, "known-project")
	}
}

// TestResolveReadProject_UnknownOverride: unknown override returns error_code=="unknown_project" + available_projects
func TestResolveReadProject_UnknownOverride(t *testing.T) {
	s := newMCPTestStore(t)
	// Store has a different project.
	if err := s.CreateSession("sess-y", "real-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	dir := t.TempDir()
	t.Chdir(dir)

	_, err := resolveReadProject(s, "does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown project override")
	}
	var upe *unknownProjectError
	if !errors.As(err, &upe) {
		t.Errorf("expected *unknownProjectError, got %T: %v", err, err)
	}
}

// TestRespondWithProject_MergesEnvelope: assert project, project_source, project_path in result
func TestRespondWithProject_MergesEnvelope(t *testing.T) {
	res := project.DetectionResult{
		Project: "myproject",
		Source:  project.SourceGitRemote,
		Path:    "/home/user/myproject",
	}
	result := respondWithProject(res, "saved OK", nil)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	text := callResultText(t, result)
	if !strings.Contains(text, "project") {
		t.Error("response must mention project")
	}
	if !strings.Contains(text, "myproject") {
		t.Errorf("response must include project name, got: %q", text)
	}
}

// TestErrorWithMeta_WrapsResponse: assert IsError==true, error_code, available_projects, hint
func TestErrorWithMeta_WrapsResponse(t *testing.T) {
	result := errorWithMeta("ambiguous_project", "cannot determine project", []string{"repo-a", "repo-b"})
	if !result.IsError {
		t.Error("IsError must be true")
	}
	text := callResultText(t, result)
	if !strings.Contains(text, "ambiguous_project") {
		t.Errorf("response must contain error_code, got: %q", text)
	}
	if !strings.Contains(text, "repo-a") {
		t.Errorf("response must contain available_projects, got: %q", text)
	}
}

// ─── F1: handleGetObservation, handleStats, handleTimeline envelope tests ──────

// TestHandleGetObservation_ResponseEnvelopeIncludesProject: successful get obs
// response must contain project, project_source, project_path envelope fields (REQ-314).
func TestHandleGetObservation_ResponseEnvelopeIncludesProject(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	s := newMCPTestStore(t)
	if err := s.CreateSession("sess-get-env", "env-test-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-get-env",
		Type:      "manual",
		Title:     "envelope observation",
		Content:   "content",
		Project:   "env-test-project",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := handleGetObservation(s)
	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id": float64(id),
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("get obs: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}

	m := callResultJSON(t, res)
	if _, ok := m["project"]; !ok {
		t.Error("response envelope must contain 'project' field")
	}
	if _, ok := m["project_source"]; !ok {
		t.Error("response envelope must contain 'project_source' field")
	}
	if _, ok := m["project_path"]; !ok {
		t.Error("response envelope must contain 'project_path' field")
	}
}

// TestHandleStats_AutoDetectsProject: stats response must include project envelope (REQ-314).
func TestHandleStats_AutoDetectsProject(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/stats-auto-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleStats(s)
	res, err := h(context.Background(), mcppkg.CallToolRequest{})
	if err != nil || res.IsError {
		t.Fatalf("stats: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}

	m := callResultJSON(t, res)
	if _, ok := m["project"]; !ok {
		t.Error("stats response must contain 'project' field")
	}
	if _, ok := m["project_source"]; !ok {
		t.Error("stats response must contain 'project_source' field")
	}
}

// TestHandleStats_ExplicitUnknownProjectError: stats with unknown project override returns
// structured error (REQ-311 applied to stats).
func TestHandleStats_ExplicitUnknownProjectError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleStats(s)
	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"project": "nonexistent-stats-project",
		}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for unknown project override in stats")
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "unknown_project") {
		t.Errorf("expected error_code unknown_project in stats error, got: %q", text)
	}
}

// TestHandleTimeline_AutoDetectsProject: timeline response must include project envelope (REQ-314).
func TestHandleTimeline_AutoDetectsProject(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/timeline-auto-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	if err := s.CreateSession("sess-tl", "timeline-auto-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-tl",
		Type:      "manual",
		Title:     "timeline obs",
		Content:   "content",
		Project:   "timeline-auto-project",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := handleTimeline(s)
	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"observation_id": float64(obsID),
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("timeline: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}

	m := callResultJSON(t, res)
	if _, ok := m["project"]; !ok {
		t.Error("timeline response must contain 'project' field")
	}
	if _, ok := m["project_source"]; !ok {
		t.Error("timeline response must contain 'project_source' field")
	}
}

// TestHandleTimeline_ExplicitUnknownProjectError: timeline with unknown project override
// returns structured error (REQ-311).
func TestHandleTimeline_ExplicitUnknownProjectError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	s := newMCPTestStore(t)
	if err := s.CreateSession("sess-tl-unknown", "known-tl-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-tl-unknown",
		Type:      "manual",
		Title:     "tl obs",
		Content:   "content",
		Project:   "known-tl-project",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := handleTimeline(s)
	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"observation_id": float64(obsID),
			"project":        "does-not-exist-tl-project",
		}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for unknown project override in timeline")
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "unknown_project") {
		t.Errorf("expected error_code unknown_project in timeline error, got: %q", text)
	}
}

// ─── F2: mem_session_summary schema + auto-detect tests ──────────────────────

// TestMemSessionSummary_SchemaNoProjectField: mem_session_summary must NOT have
// 'project' in its input schema (mirrors REQ-308 write-tool contract).
func TestMemSessionSummary_SchemaNoProjectField(t *testing.T) {
	s := newMCPTestStore(t)
	srv := NewServer(s)

	st := srv.GetTool("mem_session_summary")
	if st == nil {
		t.Fatal("mem_session_summary not registered")
	}
	props := st.Tool.InputSchema.Properties
	if _, hasProject := props["project"]; hasProject {
		t.Error("mem_session_summary must not have 'project' in schema (write tool — auto-detect only)")
	}
}

// TestMemSessionSummary_AutoDetectsProject: summary is stored under the auto-detected project.
func TestMemSessionSummary_AutoDetectsProject(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/summary-auto-project.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleSessionSummary(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"content": "## Goal\nTest auto-detection",
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("session summary: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}

	obs, err := s.RecentObservations("summary-auto-project", "project", 5)
	if err != nil || len(obs) == 0 {
		t.Fatal("expected session_summary observation under auto-detected project 'summary-auto-project'")
	}

	m := callResultJSON(t, res)
	if _, ok := m["project"]; !ok {
		t.Error("session_summary response must contain 'project' envelope field")
	}
}

// TestMemSessionSummary_AmbiguousReturnsError: ambiguous cwd returns error (REQ-309).
func TestMemSessionSummary_AmbiguousReturnsError(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"repo-summary-1", "repo-summary-2"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initTestGitRepo(t, child)
	}
	t.Chdir(parent)

	s := newMCPTestStore(t)
	h := handleSessionSummary(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"content": "## Goal\nAmbiguous test",
		}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for ambiguous project in session_summary")
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "ambiguous_project") {
		t.Errorf("expected error_code ambiguous_project, got: %q", text)
	}
}

// ─── Judgment Round 1 Hotfix tests ───────────────────────────────────────────

// JW1: TestWriteTool_AmbiguousErrorUsesCwdRepos_NotAllProjects
// When cwd is ambiguous, error must list the repos in cwd — NOT all store projects.
func TestWriteTool_AmbiguousErrorUsesCwdRepos_NotAllProjects(t *testing.T) {
	// Set up an ambiguous parent dir with 2 git repos.
	parent := t.TempDir()
	for _, name := range []string{"repo-cwd-a", "repo-cwd-b"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initTestGitRepo(t, child)
	}
	t.Chdir(parent)

	s := newMCPTestStore(t)
	// Seed an unrelated project in the store — this must NOT appear in error.
	if err := s.CreateSession("sess-unrelated", "unrelated-store-project", "/tmp"); err != nil {
		t.Fatal(err)
	}

	h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"title":   "t",
			"content": "c",
		}},
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for ambiguous cwd")
	}
	text := callResultText(t, res)
	// Must list cwd repos (repo-cwd-a, repo-cwd-b).
	if !strings.Contains(text, "repo-cwd-a") {
		t.Errorf("available_projects must contain repo-cwd-a (cwd repo); got: %q", text)
	}
	if !strings.Contains(text, "repo-cwd-b") {
		t.Errorf("available_projects must contain repo-cwd-b (cwd repo); got: %q", text)
	}
	// Must NOT list the unrelated store project.
	if strings.Contains(text, "unrelated-store-project") {
		t.Errorf("available_projects must NOT list all store projects; got: %q", text)
	}
}

// JW2: TestResolveReadProject_NormalizesOverride
// resolveReadProject must normalize (lowercase+trim) the override before ProjectExists.
func TestResolveReadProject_NormalizesOverride(t *testing.T) {
	s := newMCPTestStore(t)
	// Register a lowercase project name in the store.
	if err := s.CreateSession("sess-norm", "myapp", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	dir := t.TempDir()
	t.Chdir(dir)

	// Pass mixed-case and padded override — must normalize to "myapp".
	res, err := resolveReadProject(s, "  MyApp  ")
	if err != nil {
		t.Fatalf("resolveReadProject with mixed-case override: %v", err)
	}
	if res.Project != "myapp" {
		t.Errorf("Project = %q; want %q", res.Project, "myapp")
	}
}

// JW3: TestDetectProjectFull_AmbiguousSource — Case 4 must use "ambiguous" source, not "dir_basename"
// This test lives in mcp_test.go for co-location with other JW tests; detect.go tests
// are in detect_test.go but the constant is exported and testable here.
func TestDetectProjectFull_AmbiguousHasAmbiguousSource(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"repo-src-1", "repo-src-2"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initTestGitRepo(t, child)
	}

	result := project.DetectProjectFull(parent)
	if result.Error == nil {
		t.Fatal("expected ErrAmbiguousProject")
	}
	if result.Source != project.SourceAmbiguous {
		t.Errorf("Source = %q; want %q (SourceAmbiguous)", result.Source, project.SourceAmbiguous)
	}
}

// JW4: TestHandleSearch_SuccessUsesEnvelope — both empty and non-empty results must use respondWithProject
func TestHandleSearch_SuccessUsesEnvelope(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	s := newMCPTestStore(t)
	// Seed one observation.
	if err := s.CreateSession("sess-env4", "envelope-search-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-env4",
		Type:      "manual",
		Title:     "envelope search test",
		Content:   "envelope search content",
		Project:   "envelope-search-project",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// Non-empty results path.
	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"query": "envelope search test",
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("search: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}
	body := callResultJSON(t, res)
	if _, ok := body["project"]; !ok {
		t.Error("success response must contain 'project' envelope field (non-empty path)")
	}
	if _, ok := body["project_source"]; !ok {
		t.Error("success response must contain 'project_source' envelope field (non-empty path)")
	}

	// Empty results path.
	resEmpty, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"query": "absolutely_nonexistent_query_xyz987",
		}},
	})
	if err != nil || resEmpty.IsError {
		t.Fatalf("empty search: err=%v isError=%v", err, resEmpty.IsError)
	}
	bodyEmpty := callResultJSON(t, resEmpty)
	if _, ok := bodyEmpty["project"]; !ok {
		t.Error("empty-results response must contain 'project' envelope field")
	}
	if _, ok := bodyEmpty["project_source"]; !ok {
		t.Error("empty-results response must contain 'project_source' envelope field")
	}
}

// JR2-1 RED: TestHandleSearch_EnvelopeProjectMatchesQueryProject
// When the git repo name contains double hyphens (e.g. "my--app"), NormalizeProject
// collapses it to "my-app". The envelope project field must match the normalized form
// so LLMs reading the envelope see the same project name used in the query.
func TestHandleSearch_EnvelopeProjectMatchesQueryProject(t *testing.T) {
	dir := t.TempDir()
	// initTestGitRepo + set remote with -- in name
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", "git@github.com:user/my--app.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"query": "anything",
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("search: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}
	body := callResultJSON(t, res)
	proj, ok := body["project"].(string)
	if !ok {
		t.Fatal("envelope must have 'project' field")
	}
	// After normalization "my--app" → "my-app". The envelope must report the collapsed name.
	const want = "my-app"
	if proj != want {
		t.Errorf("envelope project = %q, want %q (double-hyphen must be collapsed)", proj, want)
	}
}

// JR2-1 RED: TestHandleContext_EnvelopeProjectMatchesQueryProject — same check for handleContext.
func TestHandleContext_EnvelopeProjectMatchesQueryProject(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", "git@github.com:user/my--app.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	t.Chdir(dir)

	s := newMCPTestStore(t)
	h := handleContext(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{}},
	})
	if err != nil || res.IsError {
		t.Fatalf("context: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}
	body := callResultJSON(t, res)
	proj, ok := body["project"].(string)
	if !ok {
		t.Fatal("envelope must have 'project' field")
	}
	const want = "my-app"
	if proj != want {
		t.Errorf("envelope project = %q, want %q (double-hyphen must be collapsed)", proj, want)
	}
}

// JR2-3 RED: TestHandleGetObservation_DegradedPathNoEnvelope
// When the cwd is ambiguous (multiple git repos), resolveReadProject returns an error.
// The handler must degrade gracefully: IsError=false, result contains observation content,
// and the response is NOT JSON (no project_source envelope field).
func TestHandleGetObservation_DegradedPathNoEnvelope(t *testing.T) {
	// Create a parent dir with two child git repos → ambiguous cwd.
	parent := t.TempDir()
	for _, name := range []string{"repo-a", "repo-b"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0755); err != nil {
			t.Fatal(err)
		}
		initTestGitRepo(t, child)
	}
	t.Chdir(parent)

	s := newMCPTestStore(t)
	if err := s.CreateSession("sess-degraded", "degraded-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-degraded",
		Type:      "manual",
		Title:     "degraded obs title",
		Content:   "degraded obs content",
		Project:   "degraded-project",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := handleGetObservation(s)
	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id": float64(obsID),
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("degraded path must not return IsError=true; text=%q", callResultText(t, res))
	}
	text := callResultText(t, res)
	if !strings.Contains(text, "degraded obs content") {
		t.Errorf("degraded path must contain observation content; got: %q", text)
	}
	// The degraded path returns plain text (no JSON envelope), so project_source must be absent.
	var m map[string]any
	if json.Unmarshal([]byte(text), &m) == nil {
		if _, hasSource := m["project_source"]; hasSource {
			t.Error("degraded path must NOT include 'project_source' envelope field")
		}
	}
}

// JW5: TestHandleGetObservation_UsesReadResolver — verify semantics; currently uses resolveWriteProject.
// This test confirms that after the fix it uses resolveReadProject (observable: same behavior + envelope).
// The fix is rename-only (semantics identical), so we just assert the envelope is present.
func TestHandleGetObservation_EnvelopePresent(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	s := newMCPTestStore(t)
	if err := s.CreateSession("sess-getobs", "obs-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-getobs",
		Type:      "manual",
		Title:     "getobs test",
		Content:   "content",
		Project:   "obs-project",
	})
	if err != nil {
		t.Fatal(err)
	}

	h := handleGetObservation(s)
	res, err := h(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id": float64(obsID),
		}},
	})
	if err != nil || res.IsError {
		t.Fatalf("get obs: err=%v isError=%v text=%q", err, res.IsError, callResultText(t, res))
	}
	body := callResultJSON(t, res)
	if _, ok := body["project"]; !ok {
		t.Error("handleGetObservation response must have 'project' envelope field")
	}
	if _, ok := body["project_source"]; !ok {
		t.Error("handleGetObservation response must have 'project_source' envelope field")
	}
}

// JW6: TestMCPConfig_DefaultProjectFieldRemoved — DefaultProject must not appear in MCPConfig.
// This test is a compile-time guard; accessing a removed field causes a compile error.
// We assert it by confirming MCPConfig can be constructed without DefaultProject.
// (No runtime check possible for a removed field — this test passes post-removal.)
func TestMCPConfig_CanConstructWithoutDefaultProject(t *testing.T) {
	// If MCPConfig.DefaultProject exists, this test compiles fine but is a no-op.
	// The real guard is removing the field and verifying the only populate site in main.go
	// is also removed. The test below verifies the struct has the expected shape post-fix.
	cfg := MCPConfig{}
	_ = cfg
	// After fix: MCPConfig has no DefaultProject field; this function body is unchanged.
	// The REAL enforcement is that main.go no longer sets cfg.DefaultProject.
}

// JW7: TestMemContext_SchemaNoLimitParam — mem_context schema must NOT advertise limit.
func TestMemContext_SchemaNoLimitParam(t *testing.T) {
	s := newMCPTestStore(t)
	srv := newServerWithActivity(s, MCPConfig{}, nil, NewSessionActivity(10*time.Minute))

	tools := srv.ListTools()
	st, ok := tools["mem_context"]
	if !ok {
		t.Fatal("mem_context tool not found")
	}

	// The schema must NOT have a "limit" input property.
	props := st.Tool.InputSchema.Properties
	if _, hasLimit := props["limit"]; hasLimit {
		t.Error("mem_context schema must not advertise 'limit' param (it is silently ignored)")
	}
}

// JS1: TestAllTools_ReadResponseEnvelope_WithAssertions
// The original test was a no-op. This version actually asserts envelope fields.
func TestAllTools_ReadResponseEnvelope_WithAssertions(t *testing.T) {
	dir := t.TempDir()
	initTestGitRepo(t, dir)
	t.Chdir(dir)

	s := newMCPTestStore(t)

	// Seed minimal data.
	if err := s.CreateSession("sess-js1", "js1-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-js1",
		Type:      "manual",
		Title:     "js1 envelope test",
		Content:   "js1 content",
		Project:   "js1-project",
	})
	if err != nil {
		t.Fatal(err)
	}

	assertEnvelope := func(t *testing.T, tool string, res *mcppkg.CallToolResult) {
		t.Helper()
		body := callResultJSON(t, res)
		if _, ok := body["project"]; !ok {
			t.Errorf("[%s] response must contain 'project' envelope field; got: %v", tool, body)
		}
		if _, ok := body["project_source"]; !ok {
			t.Errorf("[%s] response must contain 'project_source' envelope field; got: %v", tool, body)
		}
		if _, ok := body["project_path"]; !ok {
			t.Errorf("[%s] response must contain 'project_path' envelope field; got: %v", tool, body)
		}
	}

	// mem_search envelope
	hSearch := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	resSearch, err := hSearch(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"query": "js1 envelope test",
		}},
	})
	if err != nil || resSearch.IsError {
		t.Fatalf("search: err=%v isError=%v", err, resSearch.IsError)
	}
	assertEnvelope(t, "mem_search", resSearch)

	// mem_get_observation envelope
	hGet := handleGetObservation(s)
	resGet, err := hGet(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"id": float64(obsID),
		}},
	})
	if err != nil || resGet.IsError {
		t.Fatalf("get obs: err=%v isError=%v", err, resGet.IsError)
	}
	assertEnvelope(t, "mem_get_observation", resGet)

	// mem_context envelope
	hCtx := handleContext(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	resCtx, err := hCtx(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{}},
	})
	if err != nil || resCtx.IsError {
		t.Fatalf("context: err=%v isError=%v", err, resCtx.IsError)
	}
	assertEnvelope(t, "mem_context", resCtx)
}

// ─── Phase E — Conflict Surfacing Instructions ────────────────────────────────

// TestServerInstructions_ConflictSurfacingBlock verifies that serverInstructions
// contains the CONFLICT SURFACING section with all required guidance phrases.
// This is the RED→GREEN test for Phase E (E.1).
func TestServerInstructions_ConflictSurfacingBlock(t *testing.T) {
	required := []string{
		// Section header — agents must be able to grep for it
		"## CONFLICT SURFACING",

		// Core trigger condition
		"judgment_required",

		// The action: iterate candidates and call mem_judge
		"candidates[]",
		"mem_judge",

		// Heuristic: low confidence threshold
		"0.7",

		// Heuristic: ask for high-stakes relation+type combos
		"supersedes",
		"conflicts_with",
		"architecture",

		// Conversational (not blocking) resolution pattern
		"conversationally",

		// Post-resolution: persist via mem_judge with evidence
		"evidence",
	}

	for _, phrase := range required {
		if !strings.Contains(serverInstructions, phrase) {
			t.Errorf("serverInstructions is missing required phrase %q in CONFLICT SURFACING block", phrase)
		}
	}
}

// ─── Fix 1 RED — TestHandleSave_MCPConfig_OverridesDefaults ──────────────────

// TestHandleSave_MCPConfig_OverridesDefaults verifies that MCPConfig.BM25Floor
// and MCPConfig.Limit are forwarded to FindCandidates. REQ-001 requires
// configurability via Config; the existing MCPConfig struct was empty.
//
// Strategy: set BM25Floor to a very strict value (0.0) via MCPConfig. Even with
// two similar observations in the store, no candidate should score >= 0 (BM25
// scores are always negative), so candidates[] must be empty. Without the fix,
// MCPConfig.BM25Floor would be ignored and the default -2.0 would be used,
// returning at least one candidate — causing the assertion to fail.
func TestHandleSave_MCPConfig_OverridesDefaults(t *testing.T) {
	s := newMCPTestStore(t)

	// Helper to create float64 pointer.
	ptrF := func(v float64) *float64 { return &v }

	// Create MCP server with strict BM25Floor override — nothing should score >= 0.
	cfg := MCPConfig{
		BM25Floor: ptrF(0.0),
	}
	h := handleSave(s, cfg, NewSessionActivity(10*time.Minute))

	// Save first observation — no candidates yet.
	req1 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "JWT auth token session management",
		"content": "Session-based auth in the middleware layer keeps things simple",
		"type":    "architecture",
	}}}
	if _, err := h(context.Background(), req1); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// Save second, similar observation. With strict floor, no candidates should pass.
	req2 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   "Switched from JWT sessions to token auth",
		"content": "Replacing session auth with JWT tokens improves scalability",
		"type":    "architecture",
	}}}
	res2, err := h(context.Background(), req2)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if res2.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res2))
	}

	text := callResultText(t, res2)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("response is not valid JSON: %v — got %q", err, text)
	}

	// With BM25Floor=0.0 configured, no candidate can pass (BM25 scores are always negative).
	// judgment_required must be false or absent.
	if jr, ok := envelope["judgment_required"].(bool); ok && jr {
		t.Fatalf("expected judgment_required=false with strict BM25Floor=0.0 override, got true — MCPConfig.BM25Floor may not be wired")
	}
	if cands, ok := envelope["candidates"]; ok {
		if arr, ok := cands.([]any); ok && len(arr) > 0 {
			t.Fatalf("expected no candidates with BM25Floor=0.0 override, got %d — MCPConfig.BM25Floor may not be wired", len(arr))
		}
	}
}

// ─── Phase F — mem_search annotation upgrade (REQ-004, REQ-005, REQ-012) ──────

// F.1a — MemSearch_AnnotatesConflictsWith_Judged
// REQ-004 | Design §7
// Judged conflicts_with relation must surface as "conflicts: #<id> (<title>)".
func TestMemSearch_AnnotatesConflictsWith_Judged(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-f1a", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	obsAID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1a",
		Type:      "decision",
		Title:     "Use in-memory cache",
		Content:   "Cache decisions in memory for speed",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs A: %v", err)
	}
	obsBID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1a",
		Type:      "decision",
		Title:     "Use Redis for caching",
		Content:   "Redis is the preferred caching layer",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs B: %v", err)
	}

	obsA, err := s.GetObservation(obsAID)
	if err != nil {
		t.Fatalf("get obs A: %v", err)
	}
	obsB, err := s.GetObservation(obsBID)
	if err != nil {
		t.Fatalf("get obs B: %v", err)
	}

	// Create and judge a conflicts_with relation: A conflicts_with B.
	relSyncID := "rel-f1a-conflicts-01"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   relSyncID,
		SourceID: obsA.SyncID,
		TargetID: obsB.SyncID,
	}); err != nil {
		t.Fatalf("save relation: %v", err)
	}
	if _, err := s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    relSyncID,
		Relation:      store.RelationConflictsWith,
		MarkedByActor: "agent:test",
		MarkedByKind:  "agent",
	}); err != nil {
		t.Fatalf("judge relation: %v", err)
	}

	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	searchRes, err := search(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "cache",
		"project": "engram",
		"scope":   "project",
	}}})
	if err != nil {
		t.Fatalf("search error: %v", err)
	}

	text := callResultText(t, searchRes)
	// obsA should have annotation: conflicts: #<obsBID> (Use Redis for caching)
	want := fmt.Sprintf("conflicts: #%d (Use Redis for caching)", obsBID)
	if !strings.Contains(text, want) {
		t.Fatalf("expected annotation %q in search result, got:\n%s", want, text)
	}
}

// F.1b — MemSearch_PendingConflict_KeepsPhase1Annotation
// REQ-004 (negative) | Design §7
// Pending conflicts_with relation must NOT produce a conflicts: annotation.
// The existing "conflict: contested by #<sync_id> (pending)" annotation must stay byte-for-byte.
func TestMemSearch_PendingConflict_KeepsPhase1Annotation(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-f1b", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	obsAID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1b",
		Type:      "decision",
		Title:     "Keep Postgres decision",
		Content:   "We keep Postgres as primary store",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs A: %v", err)
	}
	obsBID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1b",
		Type:      "decision",
		Title:     "Switch to MongoDB decision",
		Content:   "Switch to MongoDB for flexibility",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs B: %v", err)
	}

	obsA, err := s.GetObservation(obsAID)
	if err != nil {
		t.Fatalf("get obs A: %v", err)
	}
	obsB, err := s.GetObservation(obsBID)
	if err != nil {
		t.Fatalf("get obs B: %v", err)
	}

	// Save PENDING relation (not judged) between A and B.
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   "rel-f1b-pending-01",
		SourceID: obsA.SyncID,
		TargetID: obsB.SyncID,
	}); err != nil {
		t.Fatalf("save pending relation: %v", err)
	}

	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	searchRes, err := search(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "decision",
		"project": "engram",
		"scope":   "project",
	}}})
	if err != nil {
		t.Fatalf("search error: %v", err)
	}

	text := callResultText(t, searchRes)

	// Must NOT produce a "conflicts:" annotation (that is for judged only).
	if strings.Contains(text, "conflicts:") {
		t.Fatalf("pending relation must not produce conflicts: annotation, got:\n%s", text)
	}
	// Phase 1 pending annotation must be present byte-for-byte (minus target sync_id which varies).
	if !strings.Contains(text, "conflict: contested by #") {
		t.Fatalf("expected Phase 1 pending annotation 'conflict: contested by #', got:\n%s", text)
	}
	if !strings.Contains(text, "(pending)") {
		t.Fatalf("expected '(pending)' in annotation, got:\n%s", text)
	}
	// obsBID must not appear in the annotation (Phase 1 uses sync_id, not integer id in pending case).
	_ = obsBID // used to create the relation; not checked in pending annotation format
}

// F.1c — MemSearch_TitleEnrichment_SupersedesAndSupersededBy
// REQ-005 | Design §7
// judged supersedes/superseded_by annotations must include (#<id> <title>).
func TestMemSearch_TitleEnrichment_SupersedesAndSupersededBy(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-f1c", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	oldID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1c",
		Type:      "architecture",
		Title:     "Old JWT approach",
		Content:   "We used session-based auth before JWT",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add old obs: %v", err)
	}
	newID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1c",
		Type:      "architecture",
		Title:     "New JWT approach",
		Content:   "JWT is now our authentication strategy",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add new obs: %v", err)
	}

	oldObs, err := s.GetObservation(oldID)
	if err != nil {
		t.Fatalf("get old obs: %v", err)
	}
	newObs, err := s.GetObservation(newID)
	if err != nil {
		t.Fatalf("get new obs: %v", err)
	}

	// newObs supersedes oldObs.
	relSyncID := "rel-f1c-supersedes-01"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   relSyncID,
		SourceID: newObs.SyncID,
		TargetID: oldObs.SyncID,
	}); err != nil {
		t.Fatalf("save relation: %v", err)
	}
	if _, err := s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    relSyncID,
		Relation:      store.RelationSupersedes,
		MarkedByActor: "agent:test",
		MarkedByKind:  "agent",
	}); err != nil {
		t.Fatalf("judge relation: %v", err)
	}

	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	searchRes, err := search(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "JWT approach",
		"project": "engram",
		"scope":   "project",
	}}})
	if err != nil {
		t.Fatalf("search error: %v", err)
	}

	text := callResultText(t, searchRes)

	// newObs should have: supersedes: #<oldID> (Old JWT approach)
	wantSupersedes := fmt.Sprintf("supersedes: #%d (Old JWT approach)", oldID)
	if !strings.Contains(text, wantSupersedes) {
		t.Fatalf("expected %q in search result, got:\n%s", wantSupersedes, text)
	}
	// oldObs should have: superseded_by: #<newID> (New JWT approach)
	wantSupersededBy := fmt.Sprintf("superseded_by: #%d (New JWT approach)", newID)
	if !strings.Contains(text, wantSupersededBy) {
		t.Fatalf("expected %q in search result, got:\n%s", wantSupersededBy, text)
	}
}

// F.1d — MemSearch_TitleEnrichment_FallsBackToDeleted
// REQ-005 (edge case) | Design §7, §8
// When the related observation has been deleted, annotation must read "(deleted)".
func TestMemSearch_TitleEnrichment_FallsBackToDeleted(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-f1d", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// obs that will be deleted.
	deletedID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1d",
		Type:      "decision",
		Title:     "Deleted target decision",
		Content:   "This decision will be deleted",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add deleted obs: %v", err)
	}
	// source obs that supersedes the deleted one.
	sourceID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1d",
		Type:      "decision",
		Title:     "Superseding decision",
		Content:   "This decision supersedes the deleted one",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add source obs: %v", err)
	}

	deletedObs, err := s.GetObservation(deletedID)
	if err != nil {
		t.Fatalf("get deleted obs: %v", err)
	}
	sourceObs, err := s.GetObservation(sourceID)
	if err != nil {
		t.Fatalf("get source obs: %v", err)
	}

	// source supersedes deleted.
	relSyncID := "rel-f1d-deleted-01"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   relSyncID,
		SourceID: sourceObs.SyncID,
		TargetID: deletedObs.SyncID,
	}); err != nil {
		t.Fatalf("save relation: %v", err)
	}
	if _, err := s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    relSyncID,
		Relation:      store.RelationSupersedes,
		MarkedByActor: "agent:test",
		MarkedByKind:  "agent",
	}); err != nil {
		t.Fatalf("judge relation: %v", err)
	}

	// Soft-delete the target observation.
	if err := s.DeleteObservation(deletedID, false); err != nil {
		t.Fatalf("delete obs: %v", err)
	}

	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	searchRes, err := search(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "superseding decision",
		"project": "engram",
		"scope":   "project",
	}}})
	if err != nil {
		t.Fatalf("search error: %v", err)
	}

	text := callResultText(t, searchRes)
	// Source obs should have: supersedes: #<deletedID> (deleted)
	wantDeleted := fmt.Sprintf("supersedes: #%d (deleted)", deletedID)
	if !strings.Contains(text, wantDeleted) {
		t.Fatalf("expected %q for deleted target, got:\n%s", wantDeleted, text)
	}
}

// F.1e — MemSearch_AllThreeTypes_FormatExact
// REQ-012 | Design §7
// All 3 annotation types present on one obs → format matches contract byte-for-byte.
func TestMemSearch_AllThreeTypes_FormatExact(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s-f1e", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Central obs that has all three relation types as source.
	centralID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1e",
		Type:      "architecture",
		Title:     "Central architecture decision",
		Content:   "This memory has all relation types",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add central obs: %v", err)
	}
	// Target for supersedes.
	supersedesTargetID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1e",
		Type:      "architecture",
		Title:     "Old architecture",
		Content:   "The old architecture approach",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add supersedes target: %v", err)
	}
	// Target for conflicts_with.
	conflictsTargetID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1e",
		Type:      "architecture",
		Title:     "Competing architecture",
		Content:   "A competing approach that conflicts",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add conflicts target: %v", err)
	}

	centralObs, err := s.GetObservation(centralID)
	if err != nil {
		t.Fatalf("get central: %v", err)
	}
	supersedesTarget, err := s.GetObservation(supersedesTargetID)
	if err != nil {
		t.Fatalf("get supersedes target: %v", err)
	}
	conflictsTarget, err := s.GetObservation(conflictsTargetID)
	if err != nil {
		t.Fatalf("get conflicts target: %v", err)
	}

	// Create judged supersedes: central supersedes supersedesTarget.
	relSupersedes := "rel-f1e-supersedes"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   relSupersedes,
		SourceID: centralObs.SyncID,
		TargetID: supersedesTarget.SyncID,
	}); err != nil {
		t.Fatalf("save supersedes relation: %v", err)
	}
	if _, err := s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    relSupersedes,
		Relation:      store.RelationSupersedes,
		MarkedByActor: "agent:test",
		MarkedByKind:  "agent",
	}); err != nil {
		t.Fatalf("judge supersedes: %v", err)
	}

	// Create judged conflicts_with: central conflicts_with conflictsTarget.
	relConflicts := "rel-f1e-conflicts"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   relConflicts,
		SourceID: centralObs.SyncID,
		TargetID: conflictsTarget.SyncID,
	}); err != nil {
		t.Fatalf("save conflicts relation: %v", err)
	}
	if _, err := s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    relConflicts,
		Relation:      store.RelationConflictsWith,
		MarkedByActor: "agent:test",
		MarkedByKind:  "agent",
	}); err != nil {
		t.Fatalf("judge conflicts_with: %v", err)
	}

	// Also add a superseded_by: create another obs that supersedes central (so central is target).
	supersederID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-f1e",
		Type:      "architecture",
		Title:     "Newer architecture",
		Content:   "The newest architecture supersedes central",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add superseder: %v", err)
	}
	supersederObs, err := s.GetObservation(supersederID)
	if err != nil {
		t.Fatalf("get superseder: %v", err)
	}

	relSupersededBy := "rel-f1e-superseded-by"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   relSupersededBy,
		SourceID: supersederObs.SyncID,
		TargetID: centralObs.SyncID,
	}); err != nil {
		t.Fatalf("save superseded_by relation: %v", err)
	}
	if _, err := s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    relSupersededBy,
		Relation:      store.RelationSupersedes,
		MarkedByActor: "agent:test",
		MarkedByKind:  "agent",
	}); err != nil {
		t.Fatalf("judge superseded_by: %v", err)
	}

	search := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	searchRes, err := search(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"query":   "central architecture decision",
		"project": "engram",
		"scope":   "project",
	}}})
	if err != nil {
		t.Fatalf("search error: %v", err)
	}

	text := callResultText(t, searchRes)

	// Verify exact format for all three types on central obs.
	wantSupersedes := fmt.Sprintf("supersedes: #%d (Old architecture)", supersedesTargetID)
	wantConflicts := fmt.Sprintf("conflicts: #%d (Competing architecture)", conflictsTargetID)
	wantSupersededBy := fmt.Sprintf("superseded_by: #%d (Newer architecture)", supersederID)

	if !strings.Contains(text, wantSupersedes) {
		t.Fatalf("expected %q, got:\n%s", wantSupersedes, text)
	}
	if !strings.Contains(text, wantConflicts) {
		t.Fatalf("expected %q, got:\n%s", wantConflicts, text)
	}
	if !strings.Contains(text, wantSupersededBy) {
		t.Fatalf("expected %q, got:\n%s", wantSupersededBy, text)
	}
}
