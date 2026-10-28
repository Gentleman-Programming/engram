package mcp

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestManualFallbackAvoidsForeignOwner(t *testing.T) {
	for _, tool := range []string{"save", "prompt", "summary"} {
		t.Run(tool, func(t *testing.T) {
			t.Setenv("ENGRAM_PROJECT", "dotfiles")
			s := newMCPTestStore(t)
			if err := s.EnrollProject("dotfiles"); err != nil {
				t.Fatal(err)
			}
			const legacy = "manual-save-dotfiles"
			const owner = "stale-wsl-project"
			if err := s.CreateSessionWithOwnershipMode(legacy, owner, "/old/dotfiles", store.SessionOwnershipProjectOwned); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddObservation(store.AddObservationParams{SessionID: legacy, Project: owner, Type: "manual", Title: "Existing history", Content: "Preserve this", Scope: "project"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddPrompt(store.AddPromptParams{SessionID: legacy, Project: owner, Content: "Existing prompt"}); err != nil {
				t.Fatal(err)
			}
			before, _ := s.GetSession(legacy)
			history, _ := s.RecentObservations(owner, "project", 10)
			prompts, _ := s.RecentPrompts(owner, 10)
			activity := NewSessionActivity(10 * time.Minute)
			h := handleSave(s, MCPConfig{}, activity)
			if tool == "prompt" {
				h = handleSavePrompt(s, MCPConfig{}, activity)
			}
			if tool == "summary" {
				h = handleSessionSummary(s, MCPConfig{}, activity)
			}
			for i := 0; i < 2; i++ {
				args := map[string]any{"title": "New memory", "content": "New content"}
				if tool != "prompt" {
					args["project"] = "dotfiles"
				}
				result, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
				if err != nil {
					t.Fatal(err)
				}
				if result.IsError {
					t.Fatalf("implicit %s failed: %s", tool, callResultText(t, result))
				}
			}
			after, _ := s.GetSession(legacy)
			preserved, _ := s.RecentObservations(owner, "project", 10)
			preservedPrompts, _ := s.RecentPrompts(owner, 10)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, preserved) || !reflect.DeepEqual(prompts, preservedPrompts) {
				t.Fatal("foreign data changed")
			}
			var ids []string
			if tool == "prompt" {
				rows, err := s.RecentPrompts("dotfiles", 10)
				if err != nil || len(rows) != 2 {
					t.Fatalf("prompts=%v err=%v", rows, err)
				}
				for _, row := range rows {
					ids = append(ids, row.SessionID)
				}
			} else {
				rows, err := s.RecentObservations("dotfiles", "project", 10)
				if err != nil || len(rows) == 0 {
					t.Fatalf("observations=%v err=%v", rows, err)
				}
				for _, row := range rows {
					ids = append(ids, row.SessionID)
				}
			}
			for _, id := range ids {
				if id == legacy || id != ids[0] {
					t.Fatalf("unstable or foreign fallback: %v", ids)
				}
				session, err := s.GetSession(id)
				if err != nil || session.Project != "dotfiles" || session.OwnershipMode != store.SessionOwnershipProjectOwned {
					t.Fatalf("session=%v err=%v", session, err)
				}
			}
			rejected, err := handleSave(s, MCPConfig{}, activity)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"project": "dotfiles", "session_id": legacy, "title": "Rejected", "content": "Must not transfer"}}})
			if err != nil || !rejected.IsError {
				t.Fatalf("explicit foreign session must fail: result=%v err=%v", rejected, err)
			}
			final, _ := s.GetSession(legacy)
			if !reflect.DeepEqual(before, final) {
				t.Fatal("explicit rejection changed owner")
			}
		})
	}
}
