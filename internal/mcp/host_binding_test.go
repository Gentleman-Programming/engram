package mcp

import (
	"context"
	"os"
	"reflect"
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

// Absent bindings retain ambiguity refusal; verified but unusable bindings
// reject independently of the fallback candidates.
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
			want := "host session binding cannot resolve a live session for the requested project; provide session_id explicitly or restart the host session to refresh its binding"
			if tt.name == "no binding" || tt.name == "binding published for a different host process" {
				want = "multiple active runtime sessions"
			}
			if !res.IsError || !strings.Contains(callResultText(t, res), want) {
				t.Fatalf("want refusal containing %q, got error=%v %q", want, res.IsError, callResultText(t, res))
			}
			if got := observationSessions(t, s, "engram"); len(got) != 0 {
				t.Fatalf("no write may be attributed, got %v", got)
			}
		})
	}
}

// A valid binding is authoritative even after its session ends: a different
// sole active session in the same project/directory must not receive the write.
func TestEndedHostBindingRejectsOmittedSessionIDWithSoleActiveSession(t *testing.T) {
	for _, tool := range []string{"mem_save", "mem_session_summary"} {
		t.Run(tool, func(t *testing.T) {
			s, dataDir, directory := hostBindingFixture(t)
			if err := hostbind.Publish(dataDir, os.Getpid(), "uuid-first"); err != nil {
				t.Fatal(err)
			}
			if err := s.EndSession("uuid-first", "Original ended summary"); err != nil {
				t.Fatal(err)
			}
			activeBefore, err := s.ActiveRuntimeSessions("engram", directory)
			if err != nil || len(activeBefore) != 1 || activeBefore[0] != "uuid-second" {
				t.Fatalf("active sessions = %v, %v; want only uuid-second", activeBefore, err)
			}
			statsBefore, err := s.Stats()
			if err != nil {
				t.Fatal(err)
			}
			sessionsBefore := make(map[string]*store.Session)
			for _, id := range []string{"uuid-first", "uuid-second"} {
				session, err := s.GetSession(id)
				if err != nil {
					t.Fatal(err)
				}
				sessionsBefore[id] = session
			}
			if got := observationSessions(t, s, "engram"); len(got) != 0 {
				t.Fatalf("fixture already has observations: %v", got)
			}

			var res *mcppkg.CallToolResult
			if tool == "mem_save" {
				res = saveWithoutSessionID(t, s, "engram")
			} else {
				res, err = handleSessionSummary(s, MCPConfig{}, NewSessionActivity(10*time.Minute))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
					"project": "engram", "content": "Summary must not be attributed to the other active session",
				}}})
				if err != nil {
					t.Fatalf("handler error: %v", err)
				}
			}
			if !res.IsError {
				t.Errorf("ended host binding must reject %s, got %q", tool, callResultText(t, res))
			}
			if got := observationSessions(t, s, "engram"); len(got) != 0 {
				t.Errorf("rejected %s must not write any observation (including session_summary), got sessions %v", tool, got)
			}
			statsAfter, err := s.Stats()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(statsAfter, statsBefore) {
				t.Errorf("store counters changed: before=%+v after=%+v", statsBefore, statsAfter)
			}
			for id, before := range sessionsBefore {
				after, err := s.GetSession(id)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(after, before) {
					t.Errorf("session %s changed: before=%+v after=%+v", id, before, after)
				}
			}
			activeAfter, err := s.ActiveRuntimeSessions("engram", directory)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(activeAfter, activeBefore) {
				t.Errorf("active sessions changed: before=%v after=%v", activeBefore, activeAfter)
			}
		})
	}
}

// All omitted-ID writers share the same binding rule. With exactly one live
// fallback candidate, absence permits attribution but an invalid binding cannot.
func TestHostBindingRuleAcrossOmittedSessionWriters(t *testing.T) {
	for _, tool := range []string{"mem_save", "mem_session_summary", "mem_save_prompt", "mem_capture_passive"} {
		for _, binding := range []string{"absent", "ended", "unknown", "foreign project"} {
			t.Run(tool+"/"+binding, func(t *testing.T) {
				s, dataDir, directory := hostBindingFixture(t)
				if err := s.EndSession("uuid-first", "Original summary"); err != nil {
					t.Fatal(err)
				}
				root := "uuid-first"
				if binding == "unknown" {
					root = "uuid-missing"
				}
				if binding == "foreign project" {
					root = "uuid-other"
					if err := s.StartSessionWithOwnershipMode(root, "other-project", directory, store.SessionOwnershipProjectOwned); err != nil {
						t.Fatal(err)
					}
				}
				if binding != "absent" {
					if err := hostbind.Publish(dataDir, os.Getpid(), root); err != nil {
						t.Fatal(err)
					}
				}
				before, err := s.Stats()
				if err != nil {
					t.Fatal(err)
				}
				sessions := make(map[string]*store.Session)
				ids := []string{"uuid-first", "uuid-second"}
				if binding == "foreign project" {
					ids = append(ids, root)
				}
				for _, id := range ids {
					sessions[id], err = s.GetSession(id)
					if err != nil {
						t.Fatal(err)
					}
				}
				activity := NewSessionActivity(10 * time.Minute)
				cfg := MCPConfig{DefaultProject: "engram"}
				handler := handleSave(s, cfg, activity)
				args := map[string]any{"title": "Binding rule", "type": "bugfix", "project": "engram", "content": "## Key Learnings:\n\n1. Always validate the authoritative host binding before persisting a runtime memory."}
				switch tool {
				case "mem_session_summary":
					handler = handleSessionSummary(s, cfg, activity)
				case "mem_save_prompt":
					handler = handleSavePrompt(s, cfg, activity)
				case "mem_capture_passive":
					handler = handleCapturePassive(s, cfg, activity)
				}
				res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
				if err != nil {
					t.Fatal(err)
				}
				observations := observationSessions(t, s, "engram")
				prompts, err := s.RecentPrompts("engram", 10)
				if err != nil {
					t.Fatal(err)
				}
				if binding == "absent" {
					if res.IsError {
						t.Fatalf("no-binding fallback failed: %s", callResultText(t, res))
					}
					if tool == "mem_save_prompt" {
						if len(prompts) != 1 || prompts[0].SessionID != "uuid-second" {
							t.Fatalf("fallback prompts = %+v", prompts)
						}
					} else if len(observations) != 1 || observations[0] != "uuid-second" {
						t.Fatalf("fallback observation sessions = %v", observations)
					}
					return
				}
				want := "host session binding cannot resolve a live session for the requested project; provide session_id explicitly or restart the host session to refresh its binding"
				if !res.IsError || !strings.Contains(callResultText(t, res), want) {
					t.Errorf("want binding refusal, got error=%v %q", res.IsError, callResultText(t, res))
				}
				if len(observations) != 0 || len(prompts) != 0 {
					t.Errorf("rejection wrote observations=%v prompts=%+v", observations, prompts)
				}
				after, err := s.Stats()
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Errorf("counters changed: before=%+v after=%+v", before, after)
				}
				for id, before := range sessions {
					after, err := s.GetSession(id)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(before, after) {
						t.Errorf("session %s changed: before=%+v after=%+v", id, before, after)
					}
				}
			})
		}
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
