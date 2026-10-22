package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestReviewProjectContractUnresolvedRPC(t *testing.T) {
	mcpHomeSelectionFixture(t)
	s := newMCPTestStore(t)
	id := seedHomeSelectionMCP(t, s)
	before := mcpHomeSelectionState(t, s)
	failed, body := homeSelectionRPC(t, NewServer(s), "mem_review", map[string]any{"action": "mark_reviewed", "id": id})
	if !failed || body["error_code"] != "explicit_project_required" || body["message"] != homeSelectionGuidance {
		t.Fatalf("unresolved project refusal: failed=%v body=%v", failed, body)
	}
	if after := mcpHomeSelectionState(t, s); !reflect.DeepEqual(before, after) {
		t.Fatalf("unresolved project changed observations/counters/sync: before=%v after=%v", before, after)
	}
}

func TestReviewProjectContractAmbiguousRPC(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("ENGRAM_PROJECT", "")
	for _, name := range []string{"read-a", "read-b"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initTestGitRepo(t, child)
	}
	t.Chdir(parent)
	s := newMCPTestStore(t)
	id := seedHomeSelectionMCP(t, s)
	before := mcpHomeSelectionState(t, s)
	failed, body := homeSelectionRPC(t, NewServer(s), "mem_review", map[string]any{"action": "mark_reviewed", "id": id})
	if !failed || body["error_code"] != "ambiguous_project" || body["recovery_token"] == nil || body["project_source"] != "ambiguous" || body["project_path"] != parent {
		t.Errorf("missing structured ambiguity: failed=%v body=%v", failed, body)
	}
	if !reflect.DeepEqual(body["available_projects"], []any{"read-a", "read-b"}) {
		t.Errorf("missing ambiguity candidates: %v", body)
	}
	if after := mcpHomeSelectionState(t, s); !reflect.DeepEqual(before, after) {
		t.Errorf("ambiguity changed observations/counters/sync: before=%v after=%v", before, after)
	}
	if body["hint"] != "Ask the user to choose a project, then retry mem_review action=mark_reviewed with an explicit project." {
		t.Errorf("incorrect mark_reviewed recovery hint: %v", body)
	}
}

func TestReviewProjectContractRPC(t *testing.T) {
	for _, tc := range []struct {
		name, project, owner, key string
		reject                    bool
	}{
		{"explicit overrides process positive", " ALPHA ", "alpha", "observation_id", false},
		{"explicit overrides process negative", "alpha", "beta", "observation_id", true},
		{"omitted current and alias legacy control", "", "beta", "id", false},
		{"omitted current rejects other project", "", "alpha", "observation_id", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_PROJECT", "beta")
			s := newMCPTestStore(t)
			for _, p := range []string{"alpha", "beta"} {
				if err := s.CreateSession(p, p, ""); err != nil {
					t.Fatal(err)
				}
				if err := s.EnrollProject(p); err != nil {
					t.Fatal(err)
				}
			}
			id, err := s.AddObservation(store.AddObservationParams{SessionID: tc.owner, Project: tc.owner, Type: "decision", Title: "review ownership", Content: "preserve unrelated evidence"})
			if err != nil {
				t.Fatal(err)
			}
			past := "2000-01-01 00:00:00"
			if _, err := s.DB().Exec(`UPDATE observations SET review_after = ? WHERE id = ?`, past, id); err != nil {
				t.Fatal(err)
			}
			before := mcpHomeSelectionState(t, s)
			srv := NewServerWithConfig(s, MCPConfig{DefaultProject: "beta"}, nil)
			args := map[string]any{"action": "mark_reviewed", tc.key: id}
			if tc.project != "" {
				args["project"] = tc.project
			}
			wire, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_review", "arguments": args}})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(srv.HandleMessage(context.Background(), wire))
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Error  any                   `json:"error"`
				Result mcppkg.CallToolResult `json:"result"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if response.Error != nil {
				t.Fatalf("RPC error: %s", raw)
			}
			if tc.reject {
				if !response.Result.IsError || callResultText(t, &response.Result) != "Failed to mark reviewed: observation not found in resolved project" {
					t.Fatalf("refusal: %s", raw)
				}
				if after := mcpHomeSelectionState(t, s); !reflect.DeepEqual(before, after) {
					t.Fatalf("refusal changed observations/counters/sync: before=%v after=%v", before, after)
				}
			} else {
				if response.Result.IsError {
					t.Fatalf("mark failed: %s", raw)
				}
				body := callResultJSON(t, &response.Result)
				if body["id"] != float64(id) || body["state"] != store.ObservationStateActive {
					t.Fatalf("mark result: %v", body)
				}
				obs, err := s.GetObservation(id)
				if err != nil || obs.ReviewAfter == nil || *obs.ReviewAfter == past {
					t.Fatalf("mark had no effect: %+v, %v", obs, err)
				}
			}
			// Existing global list semantics remain independent of the process project.
			isError, listed := homeSelectionRPC(t, srv, "mem_review", map[string]any{"action": "list"})
			if isError {
				t.Fatalf("list failed: %v", listed)
			}
			rows := listed["observations"].([]any)
			want := 0
			if tc.reject {
				want = 1
			}
			if len(rows) != want {
				t.Fatalf("global review list: %v, want %d", rows, want)
			}
		})
	}
}
