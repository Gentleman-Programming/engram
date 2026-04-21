package mcp

import (
	"context"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestMemSearchDateFilter validates that since/until parameters filter results by date.
func TestMemSearchDateFilter(t *testing.T) {
	cfg := store.Config{
		DataDir:              t.TempDir(),
		MaxObservationLength: 50000,
		MaxContextResults:    20,
		MaxSearchResults:     20,
	}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	s.CreateSession("sess-1", "proj", "")
	s.AddObservation(store.AddObservationParams{
		SessionID: "sess-1",
		Type:      "decision",
		Title:     "Old decision",
		Content:   "content old",
		Project:   "proj",
	})

	s.CreateSession("sess-2", "proj", "")
	s.AddObservation(store.AddObservationParams{
		SessionID: "sess-2",
		Type:      "decision",
		Title:     "New decision",
		Content:   "content new",
		Project:   "proj",
	})

	// Update created_at manually is hard; instead rely on natural ordering.
	// We'll test that since="2999-01-01" returns nothing and since="2000-01-01" returns both.
	mcpCfg := MCPConfig{DefaultProject: ""}
	activity := NewSessionActivity(10)
	handler := handleSearch(s, mcpCfg, activity)

	// Query with since far in the future → no results
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"query":   "decision",
		"since":   "2999-01-01",
		"project": "proj",
	}
	result, _ := handler(context.Background(), req)
	text, _ := result.Content[0].(mcp.TextContent)
	if text.Text != `No memories found for: "decision"` {
		t.Fatalf("expected no results for future since, got: %s", text.Text)
	}

	// Query with since in the past → results found
	req2 := mcp.CallToolRequest{}
	req2.Params.Arguments = map[string]interface{}{
		"query":   "decision",
		"since":   "2000-01-01",
		"project": "proj",
	}
	result, _ = handler(context.Background(), req2)
	text, _ = result.Content[0].(mcp.TextContent)
	if text.Text == `No memories found for: "decision"` {
		t.Fatal("expected results for past since, got none")
	}
}

// TestMemSearchUntilFilter validates that until parameter filters results by date.
func TestMemSearchUntilFilter(t *testing.T) {
	cfg := store.Config{
		DataDir:              t.TempDir(),
		MaxObservationLength: 50000,
		MaxContextResults:    20,
		MaxSearchResults:     20,
	}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	s.CreateSession("sess-1", "proj", "")
	s.AddObservation(store.AddObservationParams{
		SessionID: "sess-1",
		Type:      "decision",
		Title:     "Some decision",
		Content:   "content here",
		Project:   "proj",
	})

	mcpCfg := MCPConfig{DefaultProject: ""}
	activity := NewSessionActivity(10)
	handler := handleSearch(s, mcpCfg, activity)

	// Query with until far in the past → no results
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"query":   "decision",
		"until":   "2000-01-01",
		"project": "proj",
	}
	result, _ := handler(context.Background(), req)
	text, _ := result.Content[0].(mcp.TextContent)
	if text.Text != `No memories found for: "decision"` {
		t.Fatalf("expected no results for past until, got: %s", text.Text)
	}
}

// TestMemContextDateFilter validates that mem_context respects since/until.
func TestMemContextDateFilter(t *testing.T) {
	cfg := store.Config{
		DataDir:              t.TempDir(),
		MaxObservationLength: 50000,
		MaxContextResults:    20,
		MaxSearchResults:     20,
	}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	s.CreateSession("sess-1", "proj", "")
	s.AddObservation(store.AddObservationParams{
		SessionID: "sess-1",
		Type:      "decision",
		Title:     "Decision",
		Content:   "content",
		Project:   "proj",
	})

	mcpCfg := MCPConfig{DefaultProject: ""}
	activity := NewSessionActivity(10)
	handler := handleContext(s, mcpCfg, activity)

	// Future since → no results
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"since": "2999-01-01",
	}
	result, _ := handler(context.Background(), req)
	text, _ := result.Content[0].(mcp.TextContent)
	if text.Text != "No previous session memories found." {
		t.Fatalf("expected empty context for future since, got: %s", text.Text)
	}

	// Past since → results found
	req2 := mcp.CallToolRequest{}
	req2.Params.Arguments = map[string]interface{}{
		"since": "2000-01-01",
	}
	result, _ = handler(context.Background(), req2)
	text, _ = result.Content[0].(mcp.TextContent)
	if text.Text == "No previous session memories found." {
		t.Fatal("expected context for past since, got none")
	}
}

// TestMemSessions validates the new mem_sessions tool.
func TestMemSessions(t *testing.T) {
	cfg := store.Config{
		DataDir:              t.TempDir(),
		MaxObservationLength: 50000,
		MaxContextResults:    20,
		MaxSearchResults:     20,
	}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	s.CreateSession("sess-1", "proj", "")
	s.AddObservation(store.AddObservationParams{
		SessionID: "sess-1",
		Type:      "decision",
		Title:     "Old",
		Content:   "content",
		Project:   "proj",
	})

	mcpCfg := MCPConfig{DefaultProject: ""}
	activity := NewSessionActivity(10)
	handler := handleSessions(s, mcpCfg, activity)

	// Wide range → results
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"since": "2000-01-01",
		"until": "2999-01-01",
	}
	result, _ := handler(context.Background(), req)
	text, _ := result.Content[0].(mcp.TextContent)
	if text.Text == "No sessions found for the given filters." {
		t.Fatal("expected sessions, got none")
	}

	// Future range → no results
	req2 := mcp.CallToolRequest{}
	req2.Params.Arguments = map[string]interface{}{
		"since": "2999-01-01",
		"until": "2999-12-31",
	}
	result, _ = handler(context.Background(), req2)
	text, _ = result.Content[0].(mcp.TextContent)
	if text.Text != "No sessions found for the given filters." {
		t.Fatalf("expected no sessions for future range, got: %s", text.Text)
	}
}
