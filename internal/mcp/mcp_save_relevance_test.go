package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestHandleSave_RelevanceGate(t *testing.T) {
	for _, tc := range []struct {
		name, previousTitle, previousContent, title string
		want                                        bool
	}{
		{"incidental term", "Updated database backup retention", "Backup retention configuration", "Updated browser keyboard shortcuts", false},
		{"content only", "Database backup retention", "Browser keyboard shortcuts are unrelated to backups", "Browser keyboard shortcuts", false},
		{"real conflict different titles", "Keep sessions for auth middleware", "Session authentication policy", "Replace sessions with JWT for auth", true},
		{"short equivalent titles", "Redis", "Redis cache policy", "REDIS", true},
		{"technical singleton titles", "C++", "Compiler language policy", "C++", true},
		{"technical near miss", "C#", "Compare this language with C++", "C++", false},
		{"repeated incidental term", "Browser database retention", "Backup retention configuration", "Browser browser browser shortcuts", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newMCPTestStore(t)
			h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
			save := func(title, content string) map[string]any {
				t.Helper()
				res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
					"title": title, "content": content, "type": "decision",
				}}})
				if err != nil {
					t.Fatal(err)
				}
				if res.IsError {
					t.Fatalf("save error: %s", callResultText(t, res))
				}
				var envelope map[string]any
				if err := json.Unmarshal([]byte(callResultText(t, res)), &envelope); err != nil {
					t.Fatal(err)
				}
				return envelope
			}
			save(tc.previousTitle, tc.previousContent)
			got := save(tc.title, "Independent policy for "+tc.title)
			if got["judgment_required"] != tc.want {
				t.Errorf("judgment_required = %v, want %v", got["judgment_required"], tc.want)
			}
			candidates, _ := got["candidates"].([]any)
			wantCount := 0
			if tc.want {
				wantCount = 1
			}
			if len(candidates) != wantCount {
				t.Errorf("candidates = %d, want %d", len(candidates), wantCount)
			}
			message, _ := got["result"].(string)
			if strings.Contains(message, "CONFLICT REVIEW PENDING") != tc.want {
				t.Errorf("unexpected result: %q", message)
			}
			// Observe relation creation through the store's public interface, not SQL.
			count, err := s.CountRelations(store.ListRelationsOptions{Status: "pending"})
			if err != nil {
				t.Fatal(err)
			}
			if count != wantCount {
				t.Errorf("relations = %d, want %d", count, wantCount)
			}
		})
	}
}
