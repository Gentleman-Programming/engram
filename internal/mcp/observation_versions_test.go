package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestGetObservationWithoutIncludeHistoryIsUnchanged(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Use middleware for JWT validation.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Build history so the default path must prove it stays hidden.
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Move auth to gateway.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	}); err != nil {
		t.Fatalf("upsert observation: %v", err)
	}

	handler := handleGetObservation(s, MCPConfig{})
	res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id": float64(id),
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	envelope := callResultJSON(t, res)
	if _, ok := envelope["version_count"]; ok {
		t.Fatalf("version_count must be absent when include_history is omitted, got %v", envelope["version_count"])
	}
	if _, ok := envelope["history"]; ok {
		t.Fatalf("history must be absent when include_history is omitted")
	}
	text, ok := envelope["result"].(string)
	if !ok {
		t.Fatalf("result is not a string: %T", envelope["result"])
	}
	if strings.Contains(text, "History") || strings.Contains(text, "Version 1") {
		t.Fatalf("default output leaked history: %q", text)
	}
	if !strings.Contains(text, "gateway") {
		t.Fatalf("expected current content in default output, got %q", text)
	}
}

func TestGetObservationWithIncludeHistoryRendersVersions(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "JWT only.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	for _, content := range []string{"JWT + refresh rotation.", "OAuth2 via gateway."} {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "s1",
			Type:      "architecture",
			Title:     "Auth architecture",
			Content:   content,
			Project:   "engram",
			Scope:     "project",
			TopicKey:  "architecture/auth-model",
		}); err != nil {
			t.Fatalf("upsert %q: %v", content, err)
		}
	}

	handler := handleGetObservation(s, MCPConfig{})
	res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":              float64(id),
		"include_history": true,
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	envelope := callResultJSON(t, res)
	if envelope["version_count"] != float64(2) {
		t.Fatalf("version_count = %v, want 2", envelope["version_count"])
	}
	history, ok := envelope["history"].([]any)
	if !ok {
		t.Fatalf("history is not an array: %T", envelope["history"])
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}

	text, ok := envelope["result"].(string)
	if !ok {
		t.Fatalf("result is not a string: %T", envelope["result"])
	}
	if !strings.Contains(text, "Version 1") || !strings.Contains(text, "Version 2") {
		t.Fatalf("history section headers missing from output: %q", text)
	}
	if !strings.Contains(text, "JWT only.") || !strings.Contains(text, "JWT + refresh rotation.") {
		t.Fatalf("previous contents missing from history section: %q", text)
	}
	if !strings.Contains(text, "OAuth2 via gateway.") {
		t.Fatalf("current content missing from output: %q", text)
	}
}

func TestGetObservationIncludeHistoryEmptyHistoryStillReportsCount(t *testing.T) {
	s := newMCPTestStore(t)
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s1",
		Type:      "note",
		Title:     "Touched once",
		Content:   "No evolution yet.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	handler := handleGetObservation(s, MCPConfig{})
	res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":              float64(id),
		"include_history": true,
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", callResultText(t, res))
	}

	envelope := callResultJSON(t, res)
	if envelope["version_count"] != float64(0) {
		t.Fatalf("version_count = %v, want 0", envelope["version_count"])
	}
	if history, ok := envelope["history"].([]any); ok && len(history) != 0 {
		t.Fatalf("history = %v, want empty", history)
	}
	text, _ := envelope["result"].(string)
	if strings.Contains(text, "History") {
		t.Fatalf("empty history should not render a history section, got %q", text)
	}
}