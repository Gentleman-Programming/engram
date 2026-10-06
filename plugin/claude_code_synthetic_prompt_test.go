package plugin_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestClaudeCodeSyntheticPromptCapture(t *testing.T) {
	for _, route := range []string{"jq", "no-jq", "powershell"} {
		t.Run(route, func(t *testing.T) {
			for _, test := range []struct {
				name, prompt string
				want         int
			}{
				{"human", "Explain this function", 1},
				{"human whitespace preserved", " \tExplain this function", 1},
				{"task notification", "<task-notification>done</task-notification>", 0},
				{"agent message", "<agent-message from=\"x\">done</agent-message>", 0},
				{"trimmed task notification", " \t\r\n<task-notification>done</task-notification>", 0},
				{"trimmed agent message", " \t\r\n<agent-message from=\"x\">done</agent-message>", 0},
				{"embedded tag is human", "Explain <task-notification> syntax", 1},
			} {
				t.Run(test.name, func(t *testing.T) {
					var mu sync.Mutex
					var contents []string
					port := claudeCodeWindowsPromptServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/project/current":
							_, _ = w.Write([]byte(`{"project":"canonical-project","project_source":"config"}`))
						case "/prompts":
							if r.Method != http.MethodPost {
								t.Errorf("method = %s, want POST", r.Method)
							}
							var payload struct {
								SessionID string `json:"session_id"`
								Project   string `json:"project"`
								Content   string `json:"content"`
							}
							if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
								t.Errorf("decode prompt: %v", err)
							}
							if payload.Project != "canonical-project" || payload.SessionID == "" {
								t.Errorf("invalid prompt identity: %+v", payload)
							}
							mu.Lock()
							contents = append(contents, payload.Content)
							mu.Unlock()
							w.WriteHeader(http.StatusNoContent)
						default:
							_, _ = w.Write([]byte(`{}`))
						}
					}))
					var stdout string
					if route == "powershell" {
						stdout = runClaudeCodeWindowsPromptHook(t, claudeCodePowerShell(t), filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "user-prompt-submit.ps1"), port, newSessionID(t), t.TempDir(), test.prompt, true)
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
						input, err := json.Marshal(map[string]string{"session_id": newSessionID(t), "cwd": t.TempDir(), "prompt": test.prompt})
						if err != nil {
							t.Fatal(err)
						}
						var stderr string
						stdout, stderr = runHookWithStderr(t, "user-prompt-submit.sh", string(input), env)
						if stderr != "" {
							t.Errorf("stderr = %q, want empty", stderr)
						}
					}
					payload := decodeHookPayload(t, stdout)
					if payload.SystemMessage != "" || payload.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
						t.Errorf("unexpected hook response: %+v", payload)
					}
					assertToolSearchNames(t, selectNames(t, payload.HookSpecificOutput.AdditionalContext))
					mu.Lock()
					defer mu.Unlock()
					if len(contents) != test.want {
						t.Fatalf("prompt requests = %d, want %d; contents = %q", len(contents), test.want, contents)
					}
					if test.want == 1 && contents[0] != test.prompt {
						t.Errorf("stored content = %q, want %q", contents[0], test.prompt)
					}
				})
			}
		})
	}
}
