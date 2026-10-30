package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const homeSelectionGuidance = "explicit project required: use --project, ENGRAM_PROJECT, or .engram/config.json project_name"

func mcpHomeSelectionFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(home, "new-project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_TEMPLATE_DIR", "GIT_SHALLOW_FILE", "GIT_NAMESPACE", "GIT_REPLACE_REF_BASE", "GIT_PREFIX", "ENGRAM_PROJECT", "ENGRAM_HTTP_TOKEN"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"APPDATA": filepath.Join(root, "appdata"), "LOCALAPPDATA": filepath.Join(root, "localappdata"), "GIT_ATTR_NOSYSTEM": "1",
		"ENGRAM_DATA_DIR": filepath.Join(root, "data"), "GIT_CONFIG_COUNT": "0",
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": filepath.Join(root, "global.gitconfig"),
		"GIT_CONFIG_SYSTEM": filepath.Join(root, "system.gitconfig"), "GIT_CEILING_DIRECTORIES": root,
	} {
		t.Setenv(key, value)
	}
	initTestGitRepo(t, home)
	t.Chdir(cwd)
	return home, cwd
}

// Unlike the existing tool-specific RPC helpers, this exercises every public tool name.
func homeSelectionRPC(t *testing.T, srv *mcpserver.MCPServer, name string, args map[string]any) (bool, map[string]any) {
	t.Helper()
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(srv.HandleMessage(context.Background(), request))
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Error  any `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Error != nil || len(wire.Result.Content) != 1 {
		t.Fatalf("unexpected RPC response: %s", raw)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(wire.Result.Content[0].Text), &body); err != nil {
		t.Fatalf("non-JSON tool response: %s: %v", raw, err)
	}
	return wire.Result.IsError, body
}

func mcpHomeSelectionState(t *testing.T, s *store.Store) []any {
	t.Helper()
	stats, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	seq, err := s.MaxPendingSyncMutationSeq("")
	if err != nil {
		t.Fatal(err)
	}
	// consolidateState preserves all exported fields except ExportedAt, and all pending mutations.
	return []any{consolidateState(t, s), stats, seq}
}

func seedHomeSelectionMCP(t *testing.T, s *store.Store) int64 {
	t.Helper()
	if err := s.CreateSession("seed", "home", ""); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(store.AddObservationParams{SessionID: "seed", Project: "home", Type: "decision", Title: "preserved marker", Content: "honest preexisting evidence"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPrompt(store.AddPromptParams{SessionID: "seed", Project: "home", Content: "original prompt"}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHomeProjectSelectionMCPRejectsImplicitCapture(t *testing.T) {
	for _, operation := range []string{"save", "prompt", "summary", "start", "start-directory", "end", "passive", "current", "new-dotfiles"} {
		t.Run(operation, func(t *testing.T) {
			home, cwd := mcpHomeSelectionFixture(t)
			if operation == "new-dotfiles" {
				cwd = filepath.Join(home, ".config")
				if err := os.Mkdir(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				t.Chdir(cwd)
			}
			s := newMCPTestStore(t)
			seedHomeSelectionMCP(t, s)
			srv := NewServerWithConfig(s, MCPConfig{}, nil)
			if operation == "save" || operation == "new-dotfiles" {
				// Prime the same server's automatic-prompt context using an explicit override.
				t.Setenv("ENGRAM_PROJECT", "home")
				failed, body := homeSelectionRPC(t, srv, "mem_save_prompt", map[string]any{"content": "automatic prompt context"})
				if failed {
					t.Fatalf("prime prompt context: %v", body)
				}
				t.Setenv("ENGRAM_PROJECT", "")
			}
			before := mcpHomeSelectionState(t, s)
			name := "mem_save"
			args := map[string]any{"title": "rejected marker", "content": "must not persist", "capture_prompt": true, "type": "decision"}
			switch operation {
			case "prompt":
				name, args = "mem_save_prompt", map[string]any{"content": "must not persist"}
			case "summary":
				name, args = "mem_session_summary", map[string]any{"content": "must not persist"}
			case "start", "start-directory":
				name, args = "mem_session_start", map[string]any{"id": "rejected-session"}
				if operation == "start-directory" {
					args["directory"] = cwd
				}
			case "end":
				name, args = "mem_session_end", map[string]any{"id": "seed", "summary": "must not replace summary"}
			case "passive":
				name, args = "mem_capture_passive", map[string]any{"content": "## Key Learnings:\n- Preserve this rejected learning", "source": "test"}
			case "current":
				name, args = "mem_current_project", map[string]any{"directory": cwd}
			}
			failed, body := homeSelectionRPC(t, srv, name, args)
			if operation == "current" {
				if failed || body["project"] != "" || body["error_hint"] != homeSelectionGuidance {
					t.Errorf("context must not promote HOME identity: failed=%v body=%v", failed, body)
				}
			} else if !failed || body["error_code"] != "explicit_project_required" || body["message"] != homeSelectionGuidance {
				t.Errorf("expected actionable explicit-project error: failed=%v body=%v", failed, body)
			}
			if _, ok := body["recovery_token"]; ok {
				t.Error("explicit designation must not issue an ambiguity recovery token")
			}
			if !reflect.DeepEqual(before, mcpHomeSelectionState(t, s)) {
				t.Error("rejection changed persistent records, stats, pending mutations or sequence")
			}
			if _, err := os.Stat(filepath.Join(home, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
				t.Errorf("rejection published HOME binding: %v", err)
			}
		})
	}
}

func TestHomeProjectSelectionMCPAuthorityAndReads(t *testing.T) {
	for _, operation := range []string{"request", "environment", "process", "config", "inherited-config", "bound-save", "bound-prompt", "bound-summary", "bound-passive", "read", "list", "stored-id", "home-root", "tracked-dotfiles", "nested", "non-home"} {
		t.Run(operation, func(t *testing.T) {
			home, cwd := mcpHomeSelectionFixture(t)
			if operation == "home-root" {
				t.Chdir(home)
			}
			if operation == "tracked-dotfiles" {
				cwd = filepath.Join(home, ".config")
				if err := os.Mkdir(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cwd, "settings"), []byte("tracked"), 0o600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("git", "-C", home, "add", "--", ".config/settings")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git add: %v: %s", err, out)
				}
				t.Chdir(cwd)
			}
			if operation == "nested" {
				initTestGitRepo(t, cwd)
			}
			if operation == "non-home" {
				repo := filepath.Join(filepath.Dir(home), "monorepo")
				cwd = filepath.Join(repo, "untracked")
				if err := os.MkdirAll(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				initTestGitRepo(t, repo)
				t.Chdir(cwd)
			}
			s := newMCPTestStore(t)
			id := seedHomeSelectionMCP(t, s)
			cfg := MCPConfig{}
			args := map[string]any{"title": "accepted marker", "content": "accepted evidence", "type": "decision"}
			name := "mem_save"
			wantProject := "home"
			if operation == "request" || operation == "environment" || operation == "process" || operation == "config" || operation == "inherited-config" {
				if err := os.Mkdir(filepath.Join(cwd, ".engram"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cwd, ".engram", "config.json"), []byte(`{"project_name":"configured"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				wantProject = "configured"
				if operation == "inherited-config" {
					child := filepath.Join(cwd, "child")
					if err := os.Mkdir(child, 0o700); err != nil {
						t.Fatal(err)
					}
					t.Chdir(child)
				}
			}
			switch operation {
			case "request":
				t.Setenv("ENGRAM_PROJECT", "environment")
				args["project"] = "home"
				wantProject = "home"
			case "environment":
				t.Setenv("ENGRAM_PROJECT", "environment")
				wantProject = "environment"
			case "process":
				t.Setenv("ENGRAM_PROJECT", "environment")
				cfg.DefaultProject = "process"
				wantProject = "process"
			case "bound-save", "bound-prompt", "bound-summary", "bound-passive":
				args["session_id"] = "seed"
				switch operation {
				case "bound-prompt":
					name = "mem_save_prompt"
				case "bound-summary":
					name = "mem_session_summary"
				case "bound-passive":
					name = "mem_capture_passive"
					args["content"] = "## Key Learnings:\n- Bound session authority remains intact"
				}
			case "read":
				name, args = "mem_search", map[string]any{"query": "preserved marker"}
			case "list":
				name, args = "mem_list_projects", map[string]any{}
			case "stored-id":
				name, args = "mem_get_observation", map[string]any{"id": id}
			case "home-root", "tracked-dotfiles", "nested", "non-home":
				name, args = "mem_current_project", map[string]any{}
				if operation == "nested" {
					wantProject = "new-project"
				}
				if operation == "non-home" {
					wantProject = "monorepo"
				}
			}
			srv := NewServerWithConfig(s, cfg, nil)
			before := mcpHomeSelectionState(t, s)
			failed, body := homeSelectionRPC(t, srv, name, args)
			if failed {
				t.Fatalf("preserved public contract rejected: %v", body)
			}
			if operation == "read" || operation == "list" || operation == "stored-id" {
				if !reflect.DeepEqual(before, mcpHomeSelectionState(t, s)) {
					t.Error("read changed persistent state")
				}
				if operation == "list" {
					if body["count"] != float64(1) {
						t.Errorf("project list=%v", body)
					}
				} else {
					text, _ := body["result"].(string)
					if !strings.Contains(text, "preserved marker") {
						t.Errorf("read lost stored evidence: %v", body)
					}
				}
				return
			}
			if body["project"] != wantProject {
				t.Fatalf("project=%v want=%s body=%v", body["project"], wantProject, body)
			}
			session, err := s.GetSession("seed")
			if err != nil || session.Project != "home" {
				t.Fatalf("original session rebound: %+v %v", session, err)
			}
			if operation == "bound-prompt" {
				prompts, err := s.RecentPrompts("home", 10)
				if err != nil || len(prompts) != 2 || prompts[0].SessionID != "seed" {
					t.Fatalf("bound prompt attribution=%v err=%v", prompts, err)
				}
			}
			if operation == "bound-save" || operation == "bound-prompt" || operation == "bound-summary" || operation == "bound-passive" {
				if _, err := os.Stat(filepath.Join(home, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
					t.Errorf("session-authoritative capture initialized binding: %v", err)
				}
			}
			if operation == "request" || operation == "environment" || operation == "process" || operation == "config" || operation == "inherited-config" || operation == "bound-save" {
				rows, err := s.RecentObservations(wantProject, "project", 10)
				if err != nil || len(rows) == 0 || rows[0].Title != "accepted marker" {
					t.Fatalf("capture attribution rows=%v err=%v", rows, err)
				}
			}
		})
	}
}
