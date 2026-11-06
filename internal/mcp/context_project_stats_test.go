package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestMemContextStatsFollowEffectiveProjectScope(t *testing.T) {
	s := newMCPTestStore(t)
	for _, project := range []string{"tiny", "other"} {
		if err := s.CreateSession(project+"-session", project, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddObservation(store.AddObservationParams{SessionID: project + "-session", Project: project, Type: "decision", Title: project + " title", Content: project + " memory belongs to this project", Scope: "personal"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"process project", map[string]any{}, "Memory stats: 1 sessions, 1 observations across projects: tiny"},
		{"explicit project", map[string]any{"project": "tiny"}, "Memory stats: 1 sessions, 1 observations across projects: tiny"},
		{"normalized project", map[string]any{"project": "TINY"}, "Memory stats: 1 sessions, 1 observations across projects: tiny"},
		{"personal explicit project", map[string]any{"project": "tiny", "scope": "personal"}, "Memory stats: 1 sessions, 1 observations across projects: tiny"},
		{"personal all projects", map[string]any{"scope": "personal"}, "Memory stats: 2 sessions, 2 observations across projects:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := handleContext(s, MCPConfig{DefaultProject: "tiny"}, NewSessionActivity(10*time.Minute))
			res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: tc.args}})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatal(callResultText(t, res))
			}
			text := callResultText(t, res)
			if !strings.Contains(text, tc.want) {
				t.Fatalf("context omitted expected scoped stats %q: %s", tc.want, text)
			}
			if tc.name != "personal all projects" {
				footer := strings.Split(text, "Memory stats:")[1]
				if strings.Contains(footer, "other") {
					t.Fatalf("unrelated project leaked into footer: %s", footer)
				}
			}
		})
	}
}
