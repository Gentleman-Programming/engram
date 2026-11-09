package plugin_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Use the actual decision handler and prompt store: successful skip cases must
// reach Go, preserve historical captures, and leave bootstrap output intact.
func TestClaudeCodeDesktopSyntheticPromptPersistence(t *testing.T) {
	t.Setenv("ENGRAM_PROJECT", "")
	for _, route := range []string{"jq", "no-jq", "powershell"} {
		t.Run(route, func(t *testing.T) {
			db, err := store.New(store.FallbackConfig(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			cwd := t.TempDir()
			if err := os.Mkdir(filepath.Join(cwd, ".engram"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cwd, ".engram", "config.json"), []byte(`{"project_name":"desktop-project"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateSession("historical", "desktop-project", cwd); err != nil {
				t.Fatal(err)
			}
			const historical = "<system-reminder>existing capture</system-reminder>"
			if _, err := db.AddPrompt(store.AddPromptParams{SessionID: "historical", Project: "desktop-project", Content: historical}); err != nil {
				t.Fatal(err)
			}
			handler := server.New(db, 0).Handler()
			var decisions, persists atomic.Int32
			port := claudeCodeWindowsPromptServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/prompts/capture-decision" {
					decisions.Add(1)
				}
				if r.URL.Path == "/prompts" && r.Method == http.MethodPost {
					persists.Add(1)
				}
				handler.ServeHTTP(w, r)
			}))
			for _, tc := range []struct {
				name, prompt string
				want         int
			}{
				{"system reminder", " \t\r\n<system-reminder>side session finished</system-reminder>", 0},
				{"CI monitor", "\n<ci-monitor-event>checks finished</ci-monitor-event>", 0},
				{"task notification", "<task-notification>done</task-notification>", 0},
				{"agent message", "<agent-message from=\"side\">done</agent-message>", 0},
				{"embedded reminder", "Explain <system-reminder> in the docs", 1},
				{"embedded CI monitor", "Explain <ci-monitor-event> in the docs", 1},
				{"similar reminder tag", "<system-reminder-example> is an example", 1},
				{"similar CI tag", "<ci-monitor-events> is an example", 1},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before, err := db.RecentPrompts("desktop-project", 100)
					if err != nil {
						t.Fatal(err)
					}
					decisionBefore, persistBefore := decisions.Load(), persists.Load()
					session := newSessionID(t)
					if err := db.CreateSession(session, "desktop-project", cwd); err != nil {
						t.Fatal(err)
					}
					var stdout string
					if route == "powershell" {
						stdout = runClaudeCodeWindowsPromptHook(t, claudeCodePowerShell(t), filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "user-prompt-submit.ps1"), port, session, cwd, tc.prompt, true)
					} else {
						requireHookBinaries(t)
						env := map[string]string{"ENGRAM_PORT": port, "TMPDIR": t.TempDir()}
						if route == "no-jq" {
							bashEnv := filepath.Join(t.TempDir(), "without-jq.sh")
							if err := os.WriteFile(bashEnv, []byte(`command() { if [[ "$1" == "-v" && "$2" == "jq" ]]; then return 1; fi; builtin command "$@"; }`), 0600); err != nil {
								t.Fatal(err)
							}
							env["BASH_ENV"] = bashScriptPath(t, bashEnv)
						}
						input, err := json.Marshal(map[string]string{"session_id": session, "cwd": cwd, "prompt": tc.prompt})
						if err != nil {
							t.Fatal(err)
						}
						var stderr string
						stdout, stderr = runHookWithStderr(t, "user-prompt-submit.sh", string(input), env)
						if stderr != "" {
							t.Fatalf("hook stderr=%q", stderr)
						}
					}
					payload := decodeHookPayload(t, stdout)
					if payload.SystemMessage != "" || payload.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
						t.Fatalf("hook envelope=%+v", payload)
					}
					assertToolSearchNames(t, selectNames(t, payload.HookSpecificOutput.AdditionalContext))
					if decisions.Load()-decisionBefore != 1 || persists.Load()-persistBefore != int32(tc.want) {
						t.Fatalf("decision/persist calls=%d/%d want1/%d", decisions.Load()-decisionBefore, persists.Load()-persistBefore, tc.want)
					}
					after, err := db.RecentPrompts("desktop-project", 100)
					if err != nil {
						t.Fatal(err)
					}
					if len(after) != len(before)+tc.want {
						t.Fatalf("prompt rows=%d want%d", len(after), len(before)+tc.want)
					}
					foundHistorical, foundHuman := false, false
					for _, row := range after {
						foundHistorical = foundHistorical || row.SessionID == "historical" && row.Content == historical
						foundHuman = foundHuman || row.SessionID == session && row.Content == strings.TrimSpace(tc.prompt)
					}
					if !foundHistorical || tc.want == 1 && !foundHuman {
						t.Fatalf("historical/human rows missing: %+v", after)
					}
				})
			}
		})
	}
}
