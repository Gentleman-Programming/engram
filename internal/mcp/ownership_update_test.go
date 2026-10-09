package mcp

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestHandleUpdateOwnerAssertionIndependentOfProcessProject(t *testing.T) {
	for _, processProject := range []string{"", "owner-project", "other-project", "unknown", "../invalid"} {
		t.Run(processProject, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("ENGRAM_PROJECT", "")
			if processProject == "" {
				t.Setenv("ENGRAM_PROJECT", "unrelated-env-project")
			}
			s := newMCPTestStore(t)
			if err := s.CreateSession("owner-session", "owner-project", ""); err != nil {
				t.Fatal(err)
			}
			id, err := s.AddObservation(store.AddObservationParams{SessionID: "owner-session", Project: "owner-project", Type: "note", Title: "Original", Content: "Original content", Scope: "project"})
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.GetObservation(id)
			if err != nil {
				t.Fatal(err)
			}
			queueBefore, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
			if err != nil {
				t.Fatal(err)
			}
			h := handleUpdate(s, MCPConfig{DefaultProject: processProject})
			call := func(expected string) *mcppkg.CallToolResult {
				t.Helper()
				res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"id": float64(id), "expected_project": expected, "content": "Updated content"}}})
				if err != nil {
					t.Fatal(err)
				}
				return res
			}
			rejected := call("other-project")
			if !rejected.IsError || !strings.Contains(callResultText(t, rejected), store.ErrObservationProjectMismatch.Error()) {
				t.Fatalf("wrong owner result: %s", callResultText(t, rejected))
			}
			after, err := s.GetObservation(id)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected update changed observation: %#v, %v", after, err)
			}
			queueAfter, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
			if err != nil || !reflect.DeepEqual(queueBefore, queueAfter) {
				t.Fatalf("rejected update changed sync queue: %v", err)
			}
			accepted := call(" OWNER--PROJECT ")
			if accepted.IsError {
				t.Fatalf("matching owner rejected: %s", callResultText(t, accepted))
			}
			body := callResultJSON(t, accepted)
			if body["project"] != "owner-project" || body["project_source"] != "explicit_override" || body["project_path"] != "" || !strings.Contains(body["result"].(string), "Memory updated:") {
				t.Fatalf("update response: %#v", body)
			}
			after, err = s.GetObservation(id)
			if err != nil || after.Content != "Updated content" || after.Project == nil || *after.Project != "owner-project" || after.RevisionCount != before.RevisionCount+1 {
				t.Fatalf("accepted update: %#v, %v", after, err)
			}
		})
	}
}
