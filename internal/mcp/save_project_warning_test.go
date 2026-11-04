package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestSaveProjectWarningMembership(t *testing.T) {
	for _, kind := range []string{"live", "session-only", "prompt-only", "enrolled-only", "deleted-only", "legacy-case", "listing-error", "probe-error"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			s := newMCPTestStore(t)
			for _, project := range []string{"engram", "engam"} {
				if err := s.CreateSession(project, project, dir); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.AddObservation(store.AddObservationParams{SessionID: "engram", Project: "engram", Type: "decision", Title: "nearby project", Content: "synthetic nearby project"}); err != nil {
				t.Fatal(err)
			}
			if kind == "live" || kind == "deleted-only" || kind == "probe-error" {
				id, err := s.AddObservation(store.AddObservationParams{SessionID: "engam", Project: "engam", Type: "decision", Title: "target project", Content: "synthetic target"})
				if err != nil {
					t.Fatal(err)
				}
				if kind == "deleted-only" {
					if err := s.DeleteObservation(id, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind == "prompt-only" {
				if _, err := s.DB().Exec(`INSERT INTO user_prompts(session_id,project,content) VALUES('engam','engam','synthetic prompt')`); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "enrolled-only" {
				if err := s.EnrollProject("engam"); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "legacy-case" {
				if _, err := s.DB().Exec(`INSERT INTO observations(session_id,project,type,title,content) VALUES('engam','Engam','decision','legacy','synthetic legacy')`); err != nil {
					t.Fatal(err)
				}
			}
			original := listSaveProjectNames
			originalProbe := hasSaveProjectObservations
			if kind == "probe-error" {
				hasSaveProjectObservations = func(*store.Store, string) (bool, error) {
					return false, errors.New("synthetic point lookup failure")
				}
			}
			calls := 0
			listSaveProjectNames = func(st *store.Store) ([]string, error) {
				calls++
				if kind == "listing-error" {
					return nil, errors.New("synthetic listing failure")
				}
				return original(st)
			}
			t.Cleanup(func() { listSaveProjectNames, hasSaveProjectObservations = original, originalProbe })
			h := handleSave(s, MCPConfig{DefaultProject: "engam"}, NewSessionActivity(0))
			res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"title": "membership save", "content": "synthetic saved content", "session_id": "engam", "capture_prompt": false}}})
			if err != nil || res == nil || res.IsError {
				t.Fatalf("save err=%v result=%+v", err, res)
			}
			wantCalls := 1
			if kind == "live" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Errorf("enumerations=%d want%d", calls, wantCalls)
			}
			wantWarning := kind != "live" && kind != "listing-error" && kind != "probe-error"
			if got := strings.Contains(callResultText(t, res), "Similar project found"); got != wantWarning {
				t.Errorf("warning=%t want%t: %s", got, wantWarning, callResultText(t, res))
			}
			observations, err := s.RecentObservations("engam", "project", 10)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, obs := range observations {
				if obs.Title == "membership save" {
					found = true
					if obs.Project == nil || *obs.Project != "engam" || obs.SessionID != "engam" {
						t.Errorf("save attribution changed: %+v", obs)
					}
				}
			}
			if !found {
				t.Error("successful save not persisted")
			}
		})
	}
}
