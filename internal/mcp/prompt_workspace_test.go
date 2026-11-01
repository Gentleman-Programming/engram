package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/hostbind"
	"github.com/Gentleman-Programming/engram/v3/internal/project"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestSavePromptUsesPerCallWorkspaceAndRuntimeSession(t *testing.T) {
	s := newMCPTestStore(t)
	root := t.TempDir()
	neutral := filepath.Join(root, "neutral")
	if err := os.Mkdir(neutral, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(neutral)
	t.Setenv("ENGRAM_PROJECT", "")
	activity := NewSessionActivity(0)
	handler := handleSavePrompt(s, MCPConfig{}, activity)
	for _, tc := range []struct{ name, owner, key string }{
		{"alpha-first", "alpha", "directory"},
		{"alpha-second", "alpha", "cwd"},
		{"beta", "beta", "directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(root, tc.name)
			if err := os.MkdirAll(filepath.Join(dir, ".engram"), 0700); err != nil {
				t.Fatal(err)
			}
			config, err := json.Marshal(map[string]string{"project_name": tc.owner})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".engram", "config.json"), config, 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateSession(tc.name, tc.owner, project.RuntimeWorktreeDirectory(dir)); err != nil {
				t.Fatal(err)
			}
			// Discovery remains read-only; this write must carry its own workspace.
			_, err = handleCurrentProject(s, MCPConfig{})(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"directory": dir}}})
			if err != nil {
				t.Fatal(err)
			}
			res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"content": tc.name, tc.key: dir}}})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError {
				t.Fatal(callResultText(t, res))
			}
			var envelope map[string]any
			if err := json.Unmarshal([]byte(callResultText(t, res)), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope["project"] != tc.owner {
				t.Fatalf("prompt attributed to %v, want %s: %s", envelope["project"], tc.owner, callResultText(t, res))
			}
			prompts, err := s.RecentPrompts(tc.owner, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range prompts {
				if p.Content == tc.name && p.SessionID == tc.name {
					return
				}
			}
			t.Fatalf("prompt did not attach to the workspace's exact runtime session: %+v", prompts)
		})
	}
}

func TestPromptWorkspacePreservesAmbiguityRecoveryPath(t *testing.T) {
	s := newMCPTestStore(t)
	t.Chdir(t.TempDir())
	t.Setenv("ENGRAM_PROJECT", "")
	parents := []string{t.TempDir(), t.TempDir()}
	for _, parent := range parents {
		for _, name := range []string{"repo-a", "repo-b"} {
			dir := filepath.Join(parent, name)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			initTestGitRepo(t, dir)
		}
	}
	h := handleSavePrompt(s, MCPConfig{}, NewSessionActivity(0))
	args := map[string]any{"directory": parents[0], "content": "chosen workspace prompt"}
	call := func() *mcppkg.CallToolResult {
		t.Helper()
		res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	first := call()
	if !first.IsError {
		t.Fatal("expected directory ambiguity")
	}
	token, ok := callResultJSON(t, first)["recovery_token"].(string)
	if !ok || token == "" {
		t.Fatal("missing recovery token")
	}
	args["project"], args["project_choice_reason"] = "repo-a", "user_selected_after_ambiguous_project"
	if res := call(); !res.IsError {
		t.Fatal("recovery admitted without token")
	}
	args["recovery_token"], args["directory"] = token, parents[1]
	if res := call(); !res.IsError {
		t.Fatal("token from another workspace was admitted")
	}
	args["directory"] = parents[0]
	res := call()
	if res.IsError {
		t.Fatal(callResultText(t, res))
	}
	prompts, err := s.RecentPrompts("repo-a", 10)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("recovery prompts=%+v error=%v", prompts, err)
	}
}

func TestPromptWorkspaceDoesNotGuessOrReviveEndedSessions(t *testing.T) {
	s := newMCPTestStore(t)
	t.Chdir(t.TempDir())
	t.Setenv("ENGRAM_PROJECT", "")
	dir := promptWorkspaceFixture(t, t.TempDir(), "workspace", "alpha")
	for _, id := range []string{"first", "second", "ended"} {
		if err := s.CreateSession(id, "alpha", project.RuntimeWorktreeDirectory(dir)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EndSession("ended", "done"); err != nil {
		t.Fatal(err)
	}
	res, err := handleSavePrompt(s, MCPConfig{}, NewSessionActivity(0))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"directory": dir, "content": "must not guess"}}})
	if err != nil || !res.IsError || !strings.Contains(callResultText(t, res), "multiple active") {
		t.Fatalf("ambiguous sessions result=%+v error=%v", res, err)
	}

	prompts, err := s.RecentPrompts("alpha", 10)
	if err != nil || len(prompts) != 0 {
		t.Fatalf("failed identity wrote prompts=%+v error=%v", prompts, err)
	}
	ended, err := s.GetSession("ended")
	if err != nil || ended.EndedAt == nil {
		t.Fatalf("ended session was revived: %+v %v", ended, err)
	}
}

func TestPromptWorkspaceCannotReplaceHostBindingAuthority(t *testing.T) {
	s, dataDir, _ := hostBindingFixture(t)
	t.Setenv("ENGRAM_PROJECT", "")
	if err := hostbind.Publish(dataDir, os.Getpid(), "uuid-first"); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"engram", "other"} {
		dir := promptWorkspaceFixture(t, t.TempDir(), "workspace", owner)
		res, err := handleSavePrompt(s, MCPConfig{}, NewSessionActivity(0))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"directory": dir, "content": "host-bound " + owner}}})
		if err != nil || res.IsError != (owner == "other") {
			t.Fatalf("owner %s result=%+v error=%v", owner, res, err)
		}
	}
	prompts, err := s.RecentPrompts("engram", 10)
	if err != nil || len(prompts) != 1 || prompts[0].SessionID != "uuid-first" {
		t.Fatalf("host binding changed: %+v %v", prompts, err)
	}
	other, err := s.RecentPrompts("other", 10)
	if err != nil || len(other) != 0 {
		t.Fatalf("mismatched binding wrote elsewhere: %+v %v", other, err)
	}
}

func promptWorkspaceFixture(t *testing.T, root, name, owner string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, ".engram"), 0700); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]string{"project_name": owner})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".engram", "config.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPromptWorkspaceValidationAuthorityAndManualEvidence(t *testing.T) {
	s := newMCPTestStore(t)
	root := t.TempDir()
	neutral := filepath.Join(root, "neutral")
	if err := os.Mkdir(neutral, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(neutral)
	alpha := promptWorkspaceFixture(t, root, "alpha", "alpha")
	beta := promptWorkspaceFixture(t, root, "beta", "beta")
	badConfig := promptWorkspaceFixture(t, root, "bad-config", "bad")
	if err := os.WriteFile(filepath.Join(badConfig, ".engram", "config.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("owner-session", "alpha", project.RuntimeWorktreeDirectory(alpha)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, process, env, owner string
		args                      map[string]any
		fail                      bool
	}{
		{"alias", "", "", "beta", map[string]any{"cwd": beta}, false},
		{"directory wins", "", "", "alpha", map[string]any{"directory": alpha, "cwd": beta}, false},
		{"blank primary", "", "", "beta", map[string]any{"directory": "  ", "cwd": beta}, false},
		{"explicit session", "", "", "alpha", map[string]any{"directory": beta, "session_id": "owner-session"}, false},
		{"process authority", "fixed", "env-owner", "fixed", map[string]any{"directory": beta, "project": "model-guess"}, false},
		{"environment authority", "", "env-owner", "env-owner", map[string]any{"directory": beta}, false},
		{"legacy omitted", "", "", "neutral", map[string]any{}, false},
		{"invalid type", "", "", "", map[string]any{"directory": 42.0}, true},
		{"null", "", "", "", map[string]any{"directory": nil}, true},
		{"relative", "", "", "", map[string]any{"directory": "../alpha"}, true},
		{"missing", "", "", "", map[string]any{"directory": filepath.Join(root, "missing")}, true},
		{"file", "", "", "", map[string]any{"directory": file}, true},
		{"invalid config", "", "", "", map[string]any{"directory": badConfig}, true},
		{"unknown session", "", "", "", map[string]any{"directory": alpha, "session_id": "unknown"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_PROJECT", tc.env)
			tc.args["content"] = tc.name
			var before int
			if err := s.DB().QueryRow(`SELECT COUNT(*) FROM user_prompts`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			res, err := handleSavePrompt(s, MCPConfig{DefaultProject: tc.process}, NewSessionActivity(0))(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: tc.args}})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError != tc.fail {
				t.Fatalf("error=%v want=%v: %s", res.IsError, tc.fail, callResultText(t, res))
			}
			if tc.fail {
				var after int
				if err := s.DB().QueryRow(`SELECT COUNT(*) FROM user_prompts`).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if after != before {
					t.Fatalf("failed request persisted a prompt: %d -> %d", before, after)
				}
				return
			}
			prompts, err := s.RecentPrompts(tc.owner, 20)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range prompts {
				if p.Content != tc.name {
					continue
				}
				if tc.name == "explicit session" && p.SessionID != "owner-session" {
					t.Fatalf("session authority changed: %+v", p)
				}
				if tc.name == "alias" {
					session, err := s.GetSession(p.SessionID)
					if err != nil || project.DirectoryIdentity(session.Directory) != project.DirectoryIdentity(beta) {
						t.Fatalf("manual session directory=%+v error=%v", session, err)
					}
					evidence, err := s.CaptureProjectProvenance("prompt", strconv.FormatInt(p.ID, 10))
					if err != nil || evidence == nil || evidence.Directory != beta {
						t.Fatalf("prompt evidence=%+v error=%v", evidence, err)
					}
				}
				return
			}
			t.Fatalf("prompt not found under %s: %+v", tc.owner, prompts)
		})
	}
	owner, err := s.GetSession("owner-session")
	if err != nil || owner.Project != "alpha" || project.DirectoryIdentity(owner.Directory) != project.DirectoryIdentity(alpha) {
		t.Fatalf("session was reassigned: %+v %v", owner, err)
	}
}

// Pin the registered public schema, not just direct-handler acceptance.
func TestPromptWorkspaceSchema(t *testing.T) {
	s := newMCPTestStore(t)
	tool := NewServerWithConfig(s, MCPConfig{}, nil).GetTool("mem_save_prompt")
	if tool == nil {
		t.Fatal("mem_save_prompt missing from tool catalog")
	}
	for _, key := range []string{"directory", "cwd"} {
		if _, ok := tool.Tool.InputSchema.Properties[key]; !ok {
			t.Fatalf("schema is missing %s", key)
		}
	}
}
