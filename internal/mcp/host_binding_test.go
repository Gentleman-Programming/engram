package mcp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/hostbind"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// hostBindingFixture creates two live runtime sessions in one directory, which
// makes an omitted session_id ambiguous (#1693), and makes this test process
// play the host agent that launched the MCP server.
func hostBindingFixture(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	originalWorkingDirectory := currentWorkingDirectory
	currentWorkingDirectory = func() string { return "/work/engram" }
	t.Cleanup(func() { currentWorkingDirectory = originalWorkingDirectory })
	originalHost := hostParentPID
	hostParentPID = os.Getpid
	t.Cleanup(func() { hostParentPID = originalHost })

	s, dataDir := newMCPTestStoreWithDataDir(t)
	directory := runtimeSessionDirectory("/work/engram")
	for _, id := range []string{"uuid-first", "uuid-second"} {
		if err := s.StartSessionWithOwnershipMode(id, "engram", directory, store.SessionOwnershipProjectOwned); err != nil {
			t.Fatalf("start session %q: %v", id, err)
		}
	}
	return s, dataDir, directory
}

func saveWithoutSessionID(t *testing.T, s *store.Store, project string) *mcppkg.CallToolResult {
	t.Helper()
	return saveTitledWithoutSessionID(t, s, project, "Host bound save")
}

func saveTitledWithoutSessionID(t *testing.T, s *store.Store, project, title string) *mcppkg.CallToolResult {
	t.Helper()
	res, err := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title":   title,
		"content": "**What**: " + title + " saved without session_id\n**Why**: host binding must pick the session",
		"type":    "bugfix",
		"project": project,
	}}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return res
}

func observationSessions(t *testing.T, s *store.Store, project string) []string {
	t.Helper()
	obs, err := s.RecentObservations(project, "project", 10)
	if err != nil {
		t.Fatalf("recent observations: %v", err)
	}
	ids := make([]string, 0, len(obs))
	for _, o := range obs {
		ids = append(ids, o.SessionID)
	}
	return ids
}

func TestOmittedSessionIDUsesHostBoundSessionWhenAmbiguous(t *testing.T) {
	s, dataDir, _ := hostBindingFixture(t)
	if err := hostbind.Publish(dataDir, os.Getpid(), "uuid-second"); err != nil {
		t.Fatal(err)
	}

	res := saveWithoutSessionID(t, s, "engram")
	if res.IsError {
		t.Fatalf("host-bound save must succeed, got %q", callResultText(t, res))
	}
	if got := observationSessions(t, s, "engram"); len(got) != 1 || got[0] != "uuid-second" {
		t.Fatalf("observation sessions = %v; want [uuid-second]", got)
	}
}

func TestHostBindingFollowsRebindAfterClear(t *testing.T) {
	s, dataDir, _ := hostBindingFixture(t)
	for _, id := range []string{"uuid-first", "uuid-second"} {
		if err := hostbind.Publish(dataDir, os.Getpid(), id); err != nil {
			t.Fatal(err)
		}
		if res := saveTitledWithoutSessionID(t, s, "engram", "Bound to "+id); res.IsError {
			t.Fatalf("save bound to %s failed: %q", id, callResultText(t, res))
		}
	}
	got := observationSessions(t, s, "engram")
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("each binding must receive its own write, got %v", got)
	}
}

func TestHostBindingFollowsResumeToLiveContinuation(t *testing.T) {
	s, dataDir, directory := hostBindingFixture(t)
	if err := s.EndSession("uuid-first", ""); err != nil {
		t.Fatal(err)
	}
	continuation, err := s.ResumeSessionWithOwnershipMode("uuid-first", "engram", directory, store.SessionOwnershipProjectOwned)
	if err != nil || continuation == "uuid-first" {
		t.Fatalf("resume continuation = %q, %v", continuation, err)
	}
	if err := hostbind.Publish(dataDir, os.Getpid(), "uuid-first"); err != nil {
		t.Fatal(err)
	}

	if res := saveWithoutSessionID(t, s, "engram"); res.IsError {
		t.Fatalf("resumed host save must succeed, got %q", callResultText(t, res))
	}
	if got := observationSessions(t, s, "engram"); len(got) != 1 || got[0] != continuation {
		t.Fatalf("observation sessions = %v; want [%s]", got, continuation)
	}
}

// Every case below must keep the pre-existing fail-closed ambiguity refusal.
func TestHostBindingNeverOverridesAmbiguityWhenUnusable(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, s *store.Store, dataDir string)
	}{
		{"no binding", func(t *testing.T, s *store.Store, dataDir string) {}},
		{"binding names unknown session", func(t *testing.T, s *store.Store, dataDir string) {
			if err := hostbind.Publish(dataDir, os.Getpid(), "uuid-missing"); err != nil {
				t.Fatal(err)
			}
		}},
		{"bound session ended without continuation", func(t *testing.T, s *store.Store, dataDir string) {
			if err := hostbind.Publish(dataDir, os.Getpid(), "uuid-second"); err != nil {
				t.Fatal(err)
			}
			// A third live session keeps the omitted-session write ambiguous
			// once the bound one has ended.
			if err := s.StartSessionWithOwnershipMode("uuid-third", "engram", runtimeSessionDirectory("/work/engram"), store.SessionOwnershipProjectOwned); err != nil {
				t.Fatal(err)
			}
			if err := s.EndSession("uuid-second", ""); err != nil {
				t.Fatal(err)
			}
		}},
		{"bound session belongs to another project", func(t *testing.T, s *store.Store, dataDir string) {
			if err := s.StartSessionWithOwnershipMode("uuid-other", "other-project", runtimeSessionDirectory("/work/engram"), store.SessionOwnershipProjectOwned); err != nil {
				t.Fatal(err)
			}
			if err := hostbind.Publish(dataDir, os.Getpid(), "uuid-other"); err != nil {
				t.Fatal(err)
			}
		}},
		{"binding published for a different host process", func(t *testing.T, s *store.Store, dataDir string) {
			if err := hostbind.Publish(dataDir, os.Getppid(), "uuid-second"); err != nil {
				t.Skipf("parent process identity unavailable: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, dataDir, _ := hostBindingFixture(t)
			tt.setup(t, s, dataDir)

			res := saveWithoutSessionID(t, s, "engram")
			if !res.IsError || !strings.Contains(callResultText(t, res), "multiple active runtime sessions") {
				t.Fatalf("unusable binding must keep the ambiguity refusal, got error=%v %q", res.IsError, callResultText(t, res))
			}
			if got := observationSessions(t, s, "engram"); len(got) != 0 {
				t.Fatalf("no write may be attributed, got %v", got)
			}
		})
	}
}

func TestExplicitSessionIDIgnoresHostBinding(t *testing.T) {
	s, dataDir, _ := hostBindingFixture(t)
	if err := hostbind.Publish(dataDir, os.Getpid(), "uuid-second"); err != nil {
		t.Fatal(err)
	}
	res, err := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"title": "Explicit", "content": "**What**: explicit session wins", "type": "bugfix",
		"project": "engram", "session_id": "uuid-first",
	}}})
	if err != nil || res.IsError {
		t.Fatalf("explicit save failed: %v %v", err, res)
	}
	if got := observationSessions(t, s, "engram"); len(got) != 1 || got[0] != "uuid-first" {
		t.Fatalf("observation sessions = %v; want [uuid-first]", got)
	}
}
