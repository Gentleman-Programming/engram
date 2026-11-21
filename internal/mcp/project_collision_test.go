package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestMCPImplicitGitProjectCollisionAdvisory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "empty-git-config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CEILING_DIRECTORIES", root)
	t.Setenv("ENGRAM_PROJECT", "")
	git := func(dir string, args ...string) {
		t.Helper()
		if data, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s (%v)", args, data, err)
		}
	}
	checkout := func(org string) string {
		t.Helper()
		dir := filepath.Join(root, org, "workspace")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		git(dir, "init")
		git(dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test",
			"commit", "--allow-empty", "-m", "test")
		git(dir, "remote", "add", "origin", "https://example.test/"+org+"/customer-solution.git")
		return dir
	}
	first, second := checkout("team-a"), checkout("team-b")
	linked := filepath.Join(root, "linked")
	git(first, "worktree", "add", "--detach", linked)
	const label = "customer-solution"

	configured := checkout("configured")
	config := filepath.Join(configured, ".engram")
	if err := os.Mkdir(config, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "config.json"), []byte("{\"project_name\":\"customer-solution\"}"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		tool func(*store.Store, MCPConfig, *SessionActivity) server.ToolHandlerFunc
	}{
		{"mem_save", handleSave},
		{"mem_save_prompt", handleSavePrompt},
		{"mem_session_summary", handleSessionSummary},
		{"mem_capture_passive", handleCapturePassive},
		{"mem_session_start", handleSessionStart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newMCPTestStore(t)
			activity := NewSessionActivity(10 * time.Minute)
			tool := tc.tool(db, MCPConfig{}, activity)
			call := func(handler server.ToolHandlerFunc, dir string, args map[string]any, wantWarning bool) string {
				t.Helper()
				t.Chdir(dir)
				result, err := handler(context.Background(), mcppkg.CallToolRequest{
					Params: mcppkg.CallToolParams{Arguments: args},
				})
				if err != nil || result == nil || result.IsError {
					t.Fatalf("capture failed: result=%+v error=%v", result, err)
				}
				var response struct {
					Project string `json:"project"`
					Warning string `json:"warning"`
				}
				if err := json.Unmarshal([]byte(callResultText(t, result)), &response); err != nil {
					t.Fatal(err)
				}
				if response.Project != label || strings.Contains(response.Warning, "another Git checkout") != wantWarning ||
					(wantWarning && !strings.Contains(response.Warning, "engram init <distinct-name>")) {
					t.Fatalf("unexpected response %+v, want collision advisory=%v", response, wantWarning)
				}
				return response.Warning
			}
			args := func(id string) map[string]any {
				return map[string]any{"id": id, "title": id,
					"content":        "## Key Learnings:\n- This synthetic capture is unique to the " + id + " request.",
					"capture_prompt": false}
			}
			call(tool, first, args("initial"), false)
			call(tool, linked, args("linked"), false)

			const existingID = "existing-session"
			if err := db.CreateSession(existingID, label, second); err != nil {
				t.Fatal(err)
			}
			old := args(existingID)
			old["session_id"] = existingID
			call(tool, second, old, false)
			explicit := args("explicit")
			explicit["project"] = label
			call(handleSave(db, MCPConfig{}, activity), second, explicit, false)
			call(handleSave(db, MCPConfig{DefaultProject: label}, activity), second, args("process override"), false)

			call(handleSave(db, MCPConfig{}, activity), configured, args("configured name"), false)

			collisionDirectory := second
			if tc.name == "mem_save" {
				collisionDirectory = filepath.Dir(second)
			}
			if tc.name == "mem_capture_passive" {
				empty := args("nothing extracted")
				empty["content"] = "There is no learning section in this input."
				call(tool, second, empty, false)
			}
			var rejectWrite string
			switch tc.name {
			case "mem_save":
				rejectWrite = `CREATE TRIGGER reject_fixture_collision BEFORE INSERT ON observations
					WHEN NEW.title = 'rejected-collision'
					BEGIN SELECT RAISE(ABORT, 'fixture rejected collision'); END`
			case "mem_session_start":
				rejectWrite = `CREATE TRIGGER reject_fixture_collision BEFORE INSERT ON sessions
					WHEN NEW.id = 'rejected-collision'
					BEGIN SELECT RAISE(ABORT, 'fixture rejected collision'); END`
			}
			if rejectWrite != "" {
				if _, err := db.DB().Exec(rejectWrite); err != nil {
					t.Fatal(err)
				}
				t.Chdir(collisionDirectory)
				result, err := tool(context.Background(), mcppkg.CallToolRequest{
					Params: mcppkg.CallToolParams{Arguments: args("rejected-collision")},
				})
				if err != nil || result == nil || !result.IsError {
					t.Fatalf("fixture write unexpectedly succeeded: result=%+v error=%v", result, err)
				}
				if message := callResultText(t, result); !strings.Contains(message, "fixture rejected collision") ||
					strings.Contains(message, "another Git checkout") {
					t.Fatalf("expected the fixture failure without an advisory, got %q", message)
				}
			}
			warning := call(tool, collisionDirectory, args("collision"), true)
			if tc.name == "mem_save" && !strings.Contains(warning, "auto-promoted child repository:") {
				t.Fatalf("collision advisory replaced the project discovery warning: %q", warning)
			}
			call(tool, second, args("repeat"), false)
		})
	}
}
