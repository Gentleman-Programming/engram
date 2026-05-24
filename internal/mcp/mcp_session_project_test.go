package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestHandleCurrentProjectWithCwd(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleCurrentProject(s, MCPConfig{})

	// Create a temp directory that mimics a project structure
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "custom-project-name")
	if err := os.Mkdir(projDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// We pass cwd as argument
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"cwd": projDir,
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected handler error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "custom-project-name") {
		t.Fatalf("expected project name 'custom-project-name' in output, got: %q", text)
	}
}

func TestHandleCurrentProjectWithDirectory(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleCurrentProject(s, MCPConfig{})

	// Create a temp directory that mimics a project structure
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "another-custom-project")
	if err := os.Mkdir(projDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// We pass directory as argument
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"directory": projDir,
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected handler error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	if !strings.Contains(text, "another-custom-project") {
		t.Fatalf("expected project name 'another-custom-project' in output, got: %q", text)
	}
}

func TestHandleSessionSummaryResolvesFromSession(t *testing.T) {
	s := newMCPTestStore(t)
	// Create session for "my-real-project"
	sessionID := "sess-xyz-123"
	if err := s.CreateSession(sessionID, "my-real-project", "/tmp/my-real-project"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	activity := NewSessionActivity(10 * time.Minute)
	h := handleSessionSummary(s, MCPConfig{}, activity)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"session_id": sessionID,
		"content":    "## Goal\nTest\n## Accomplished\nDone\n",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected handler error: %s", callResultText(t, res))
	}

	// Verify that summary observation was created under project "my-real-project"
	obs, err := s.RecentObservations("my-real-project", "project", 5)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("expected 1 summary observation under project 'my-real-project', got: %d", len(obs))
	}
	if obs[0].SessionID != sessionID {
		t.Fatalf("expected observation session ID to be %q, got: %q", sessionID, obs[0].SessionID)
	}
}

func TestHandleSessionEndResolvesFromSession(t *testing.T) {
	s := newMCPTestStore(t)
	// Create session for "my-ended-project"
	sessionID := "sess-end-999"
	if err := s.CreateSession(sessionID, "my-ended-project", "/tmp/my-ended-project"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	activity := NewSessionActivity(10 * time.Minute)
	h := handleSessionEnd(s, MCPConfig{}, activity)

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"id":      sessionID,
		"summary": "This session is done",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected handler error: %s", callResultText(t, res))
	}

	// Verify the session is ended in store
	sess, err := s.GetSession(sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.EndedAt == nil {
		t.Fatalf("expected session to be marked as ended")
	}
	if sess.Summary == nil || *sess.Summary != "This session is done" {
		t.Fatalf("expected session summary 'This session is done', got: %v", sess.Summary)
	}
}
