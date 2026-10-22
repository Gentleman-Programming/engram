package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// Capture public read surfaces, including counters and queued replication effects.
// Keeping the entire values also catches changes to existing records on rejection.
func writePreparationSnapshot(t *testing.T, s *store.Store) []any {
	t.Helper()
	stats, err := handleStats(s, MCPConfig{})(context.Background(), mcppkg.CallToolRequest{})
	if err != nil || stats.IsError {
		t.Fatalf("stats: %v, %#v", err, stats)
	}
	sessions, err := s.AllSessions("", 100)
	if err != nil {
		t.Fatal(err)
	}
	observations, err := s.AllObservations("", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	prompts, err := s.RecentPrompts("", 100)
	if err != nil {
		t.Fatal(err)
	}
	mutations, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	counters, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	return []any{callResultText(t, stats), sessions, observations, prompts, mutations, counters}
}

func TestWritePreparationValidationBeforeEffects(t *testing.T) {
	for _, tool := range []string{"save", "summary"} {
		type validationCase struct {
			name  string
			patch map[string]any
			want  string
		}
		cases := []validationCase{
			{"missing-content", map[string]any{"content": nil}, "content is required for mem_session_summary"},
			{"blank-content", map[string]any{"content": " \t\n "}, "content is required for mem_session_summary"},
			{"non-string-content", map[string]any{"content": 42}, "content is required for mem_session_summary"},
		}
		if tool == "save" {
			for i := range cases {
				cases[i].want = "content is required for mem_save (use content, or observation for backward-compatible clients)"
			}
			cases = append(cases,
				validationCase{"blank-title", map[string]any{"title": " \t "}, "Failed to save: observation title is required"},
				validationCase{"invalid-capture-prompt", map[string]any{"capture_prompt": "false"}, "capture_prompt must be a boolean"},
			)
		}
		for _, tc := range cases {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				_, s, _ := newAmbiguousMCPSetup(t)
				activity := NewSessionActivity(0)
				handler := handleSave(s, MCPConfig{}, activity)
				if tool == "summary" {
					handler = handleSessionSummary(s, MCPConfig{}, activity)
				}
				// Both an unknown session and an invalid explicit project would fail if
				// preparation were moved ahead of handler-specific input validation.
				args := map[string]any{"title": "Valid title", "content": "Valid content", "project": "", "session_id": "unknown-session", "recovery_token": "invalid"}
				for k, v := range tc.patch {
					args[k] = v
				}
				if tc.name == "missing-content" {
					delete(args, "content")
				}
				before := writePreparationSnapshot(t, s)
				result, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
				if err != nil {
					t.Fatal(err)
				}
				if !result.IsError || callResultText(t, result) != tc.want {
					t.Fatalf("result = %#v, text = %q; want %q", result, callResultText(t, result), tc.want)
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("PARITY_RESULT %s %s", t.Name(), encoded)
				if after := writePreparationSnapshot(t, s); !reflect.DeepEqual(before, after) {
					t.Fatalf("rejection changed public state: before=%#v after=%#v", before, after)
				}
			})
		}
	}
}

func TestWritePreparationExplicitProjectPresence(t *testing.T) {
	for _, tool := range []string{"save", "summary"} {
		for _, tc := range []struct {
			name    string
			project any
			raw     string
		}{
			{"empty", "", ""}, {"whitespace", " \t ", " \t "}, {"non-string", 42, ""},
		} {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				s := newMCPTestStore(t)
				if err := s.CreateSession("anchored-session", "anchored-project", t.TempDir()); err != nil {
					t.Fatal(err)
				}
				activity := NewSessionActivity(0)
				handler := handleSave(s, MCPConfig{}, activity)
				if tool == "summary" {
					handler = handleSessionSummary(s, MCPConfig{}, activity)
				}
				args := map[string]any{"title": "Valid title", "content": "Valid content", "session_id": "anchored-session", "project": tc.project}
				before := writePreparationSnapshot(t, s)
				result, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
				if err != nil {
					t.Fatal(err)
				}
				body := callResultJSON(t, result)
				want := fmt.Sprintf("Project %q is invalid: project is required", tc.raw)
				if !result.IsError || body["error_code"] != "invalid_project" || body["message"] != want {
					t.Fatalf("unexpected rejection: %#v", body)
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("PARITY_RESULT %s %s", t.Name(), encoded)
				if after := writePreparationSnapshot(t, s); !reflect.DeepEqual(before, after) {
					t.Fatalf("rejection changed public state: before=%#v after=%#v", before, after)
				}
				// Omission, unlike an explicit invalid value, inherits the known session.
				delete(args, "project")
				result, err = handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
				if err != nil || result.IsError {
					t.Fatalf("omitted project: %v, %#v", err, result)
				}
				observations, err := s.SessionObservations("anchored-session", 100)
				if err != nil || len(observations) != 1 {
					t.Fatalf("observations: %#v, %v", observations, err)
				}
				obs := observations[0]
				if obs.Project == nil || *obs.Project != "anchored-project" {
					t.Fatalf("project: %#v", obs.Project)
				}
				wantType := "manual"
				if tool == "summary" {
					wantType = "session_summary"
				}
				if obs.Type != wantType {
					t.Fatalf("type = %q; want %q", obs.Type, wantType)
				}
			})
		}
	}
}
