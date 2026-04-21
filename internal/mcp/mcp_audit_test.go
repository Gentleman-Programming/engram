package mcp

// mcp_audit_test.go — tests derived from the 10-test audit (2026-04-22).
//
// Covers:
//   T6 — handleSave must reject empty title or empty content.
//   T7 — SessionActivity must be safe under concurrent access (run with -race).

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
)

// ─── T6: handleSave validation ────────────────────────────────────────────────

func TestHandleSaveRejectsEmptyTitle(t *testing.T) {
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

	activity := NewSessionActivity(10 * time.Minute)
	handler := handleSave(s, MCPConfig{}, activity)

	emptyTitles := []struct {
		name  string
		title string
	}{
		{"empty string", ""},
		{"only spaces", "   "},
		{"only tab", "\t"},
	}

	for _, tc := range emptyTitles {
		t.Run(tc.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = map[string]interface{}{
				"title":   tc.title,
				"content": "valid content",
			}
			result, err := handler(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			// Must be an error result (isError=true), not a success
			if !result.IsError {
				t.Fatalf("expected IsError=true for empty title %q, got success: %v", tc.title, result.Content)
			}
			text, _ := result.Content[0].(mcp.TextContent)
			if !strings.Contains(text.Text, "title") {
				t.Errorf("error message should mention 'title', got: %q", text.Text)
			}
		})
	}
}

func TestHandleSaveRejectsEmptyContent(t *testing.T) {
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

	activity := NewSessionActivity(10 * time.Minute)
	handler := handleSave(s, MCPConfig{}, activity)

	emptyContents := []struct {
		name    string
		content string
	}{
		{"empty string", ""},
		{"only spaces", "   "},
		{"only newline", "\n"},
	}

	for _, tc := range emptyContents {
		t.Run(tc.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = map[string]interface{}{
				"title":   "valid title",
				"content": tc.content,
			}
			result, err := handler(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if !result.IsError {
				t.Fatalf("expected IsError=true for empty content %q, got success", tc.content)
			}
			text, _ := result.Content[0].(mcp.TextContent)
			if !strings.Contains(text.Text, "content") {
				t.Errorf("error message should mention 'content', got: %q", text.Text)
			}
		})
	}
}

func TestHandleSaveAcceptsValidTitleAndContent(t *testing.T) {
	// Camino feliz: título y contenido válidos deben guardarse sin error.
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

	activity := NewSessionActivity(10 * time.Minute)
	handler := handleSave(s, MCPConfig{DefaultProject: "test-proj"}, activity)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"title":   "My decision",
		"content": "Full content here",
		"type":    "decision",
	}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.IsError {
		text, _ := result.Content[0].(mcp.TextContent)
		t.Fatalf("expected success, got error: %s", text.Text)
	}
}

// ─── T7: SessionActivity concurrency ─────────────────────────────────────────

// Run with: go test -race ./internal/mcp/... -run TestSessionActivityConcurrency
func TestSessionActivityConcurrency(t *testing.T) {
	activity := NewSessionActivity(10 * time.Minute)
	const goroutines = 50
	const iterations = 20

	var wg sync.WaitGroup
	sessions := []string{"sess-a", "sess-b", "sess-c"}

	for i := 0; i < goroutines; i++ {
		sess := sessions[i%len(sessions)]
		wg.Add(4)

		go func(s string) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				activity.RecordToolCall(s)
			}
		}(sess)

		go func(s string) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				activity.RecordSave(s)
			}
		}(sess)

		go func(s string) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				activity.NudgeIfNeeded(s)
			}
		}(sess)

		go func(s string) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				activity.ActivityScore(s)
			}
		}(sess)
	}

	wg.Wait()

	// Verify final counts are plausible (>0, no negative wrapping)
	for _, sess := range sessions {
		score := activity.ActivityScore(sess)
		if score == "" {
			// sess may have been cleared by another goroutine — acceptable
			continue
		}
		if strings.Contains(score, "-") && !strings.Contains(score, "consider") {
			t.Errorf("session %q has unexpected negative count in score: %q", sess, score)
		}
	}
}

func TestSessionActivityClearResetsState(t *testing.T) {
	activity := NewSessionActivity(10 * time.Minute)
	activity.RecordToolCall("sess-x")
	activity.RecordSave("sess-x")

	// Score should be non-empty before clear
	if activity.ActivityScore("sess-x") == "" {
		t.Fatal("expected non-empty score before clear")
	}

	activity.ClearSession("sess-x")

	// After clear, NudgeIfNeeded should return ""
	if nudge := activity.NudgeIfNeeded("sess-x"); nudge != "" {
		t.Errorf("expected no nudge after clear, got: %q", nudge)
	}
	// ActivityScore should also return ""
	if score := activity.ActivityScore("sess-x"); score != "" {
		t.Errorf("expected empty score after clear, got: %q", score)
	}
}

// ─── T2 (MCP layer): handleSearch with invalid date must return tool error ───

func TestHandleSearchInvalidDateReturnsToolError(t *testing.T) {
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

	s.CreateSession("s1", "proj", "")
	s.AddObservation(store.AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "something", Content: "content", Project: "proj",
	})

	activity := NewSessionActivity(10 * time.Minute)
	handler := handleSearch(s, MCPConfig{}, activity)

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]interface{}{
		"query": "something",
		"since": "not-a-date",
	}
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	// Must surface as a tool error, not silently ignore the bad date
	if !result.IsError {
		text, _ := result.Content[0].(mcp.TextContent)
		t.Fatalf("expected tool error for invalid since, got success: %s", text.Text)
	}
}

// ─── T8 (MCP layer): handleSearch with empty query must return tool error ────

func TestHandleSearchEmptyQueryReturnsToolError(t *testing.T) {
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

	activity := NewSessionActivity(10 * time.Minute)
	handler := handleSearch(s, MCPConfig{}, activity)

	emptyQueries := []string{"", "   "}
	for _, q := range emptyQueries {
		t.Run("query=«"+q+"»", func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = map[string]interface{}{
				"query": q,
			}
			result, err := handler(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if !result.IsError {
				text, _ := result.Content[0].(mcp.TextContent)
				t.Fatalf("expected tool error for empty query %q, got success: %s", q, text.Text)
			}
		})
	}
}
