package mcp

import (
	"context"
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
