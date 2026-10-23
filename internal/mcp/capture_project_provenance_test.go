package mcp

import (
	"context"
	"strconv"
	"testing"
	"time"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestCaptureProjectProvenanceMCPSessionAndObservation(t *testing.T) {
	st := newMCPTestStore(t)
	t.Chdir(t.TempDir())
	activity := NewSessionActivity(10 * time.Minute)
	cfg := MCPConfig{DefaultProject: "example"}
	result, err := handleSessionStart(st, cfg, activity)(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{"id": "origin"}},
	})
	if err != nil || result.IsError {
		t.Fatalf("start=%+v err=%v", result, err)
	}
	p, err := st.CaptureProjectProvenance("session", "origin")
	if err != nil || p == nil || p.Source != "process_override" || p.RawProject != "example" {
		t.Fatalf("session=%+v err=%v", p, err)
	}
	result, err = handleSave(st, cfg, activity)(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"session_id": "origin", "title": "Inherited evidence", "content": "Preserve known source through session",
		}},
	})
	if err != nil || result.IsError {
		t.Fatalf("save=%+v err=%v", result, err)
	}
	observations, err := st.RecentObservations("example", "project", 10)
	if err != nil || len(observations) != 1 {
		t.Fatalf("observations=%+v err=%v", observations, err)
	}
	p, err = st.CaptureProjectProvenance("observation", strconv.FormatInt(observations[0].ID, 10))
	if err != nil || p == nil || p.Source != "process_override" || p.RawProject != "example" {
		t.Fatalf("observation=%+v err=%v", p, err)
	}
}

func TestCaptureProjectProvenanceMCPImplicitSession(t *testing.T) {
	st := newMCPTestStore(t)
	t.Chdir(t.TempDir())
	if err := st.EnrollProject("example"); err != nil {
		t.Fatal(err)
	}
	result, err := handleSavePrompt(st, MCPConfig{DefaultProject: "example"}, nil)(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{"content": "Implicit prompt evidence"}},
	})
	if err != nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	prompts, err := st.RecentPrompts("example", 10)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("prompts=%+v err=%v", prompts, err)
	}
	p, err := st.CaptureProjectProvenance("session", prompts[0].SessionID)
	if err != nil || p == nil || p.Source != "process_override" {
		t.Fatalf("implicit session=%+v err=%v", p, err)
	}
}

func TestCaptureProjectProvenanceMCPPrompt(t *testing.T) {
	st := newMCPTestStore(t)
	t.Chdir(t.TempDir())
	if err := st.CreateSession("evidence", "example", ""); err != nil {
		t.Fatal(err)
	}
	result, err := handleSavePrompt(st, MCPConfig{}, nil)(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"session_id": "evidence", "project": "example", "content": "Preserve prompt evidence",
		}},
	})
	if err != nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	prompts, err := st.RecentPrompts("example", 10)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("prompts=%+v err=%v", prompts, err)
	}
	p, err := st.CaptureProjectProvenance("prompt", strconv.FormatInt(prompts[0].ID, 10))
	if err != nil || p == nil || p.Source != "unknown" || p.RawProject != "" {
		t.Fatalf("evidence=%+v err=%v", p, err)
	}
}
