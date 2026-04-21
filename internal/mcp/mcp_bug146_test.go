package mcp

import (
	"context"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestHandleSearchNoProjectFilter tests that mem_search without an explicit
// project searches across ALL projects, not just DefaultProject.
// This is the fix for GitHub issue #146.
func TestHandleSearchNoProjectFilter(t *testing.T) {
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

	// Save an observation under project "other-project"
	s.CreateSession("sess-1", "other-project", "")
	s.AddObservation(store.AddObservationParams{
		SessionID: "sess-1",
		Type:      "decision",
		Title:     "Important decision",
		Content:   "This is a test about homelab-fastmcp content",
		Project:   "other-project",
	})

	// MCP config with DefaultProject set to something ELSE
	mcpCfg := MCPConfig{DefaultProject: "current-project"}
	activity := NewSessionActivity(10)
	handler := handleSearch(s, mcpCfg, activity)

	// Call mem_search WITHOUT specifying project (LLM didn't send it)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"query": "homelab-fastmcp",
		// "project" is intentionally omitted
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	text, _ := result.Content[0].(mcp.TextContent)
	if text.Text == `No memories found for: "homelab-fastmcp"` {
		t.Fatalf("mem_search returned empty when project was omitted — bug #146 not fixed. Got: %s", text.Text)
	}

	if text.Text == "" || len(result.Content) == 0 {
		t.Fatal("mem_search returned empty content")
	}

	t.Logf("Result: %s", text.Text)
}

// TestHandleContextNoProjectFilter tests that mem_context without project
// shows observations from ALL projects, not just DefaultProject.
func TestHandleContextNoProjectFilter(t *testing.T) {
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

	// Save under "other-project"
	s.CreateSession("sess-1", "other-project", "")
	s.AddObservation(store.AddObservationParams{
		SessionID: "sess-1",
		Type:      "decision",
		Title:     "Important decision",
		Content:   "Some content here",
		Project:   "other-project",
	})

	mcpCfg := MCPConfig{DefaultProject: "current-project"}
	activity := NewSessionActivity(10)
	handler := handleContext(s, mcpCfg, activity)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		// "project" omitted
	}

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	text, _ := result.Content[0].(mcp.TextContent)
	if text.Text == "No previous session memories found." {
		t.Fatalf("mem_context returned empty when project was omitted — bug #146 not fixed")
	}

	t.Logf("Result: %s", text.Text)
}
