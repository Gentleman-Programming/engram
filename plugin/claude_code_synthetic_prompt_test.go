package plugin_test

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestClaudeCodeSyntheticPromptCapture verifies server-owned persistence decisions across
// Bash and PowerShell while preserving human text and the ToolSearch bootstrap.
func TestClaudeCodeSyntheticPromptCapture(t *testing.T) {
	for _, route := range []string{"jq", "no-jq", "powershell"} {
		t.Run(route, func(t *testing.T) {
			for _, test := range []struct {
				name, prompt string
				want         int
				response     string
				status       int
				delay        time.Duration
			}{
				{name: "human", prompt: "Explain this function", want: 1},
				{name: "human whitespace preserved", prompt: " \tExplain café 😀\r\n\"quoted\" \\path\r\n", want: 1},
				{name: "task notification", prompt: "<task-notification>done</task-notification>"},
				{name: "agent message", prompt: "<agent-message from=\"x\">done</agent-message>"},
				{name: "trimmed task notification", prompt: " \t\r\n<task-notification>done</task-notification>"},
				{name: "trimmed agent message", prompt: " \t\r\n<agent-message from=\"x\">done</agent-message>"},
				{name: "embedded tag is human", prompt: "Explain <task-notification> syntax", want: 1},
				{name: "server owns classification", prompt: "<agent-message human override", want: 1},
				{name: "server skips human", prompt: "Explain this function"},
				{name: "malformed", prompt: "human", response: `not-json`},
				{name: "old server", prompt: "human", response: `{}`, status: http.StatusNotFound},
				{name: "unavailable", prompt: "human", status: http.StatusServiceUnavailable},
				{name: "timeout", prompt: "human", response: `{"decision":"capture","project":"canonical-project","project_source":"config"}`, delay: 2200 * time.Millisecond},
				{name: "redirect capture", prompt: "human", status: http.StatusFound, response: `{"decision":"capture","project":"canonical-project","project_source":"config"}`},
				{name: "project slash path", prompt: "human", response: `{"decision":"capture","project":"folder/project","project_source":"config"}`},
				{name: "project backslash path", prompt: "human", response: `{"decision":"capture","project":"folder\\project","project_source":"config"}`},
				{name: "project controls", prompt: "human", response: `{"decision":"capture","project":"project\u0001\u007f","project_source":"config"}`},
				{name: "project leading space", prompt: "human", response: `{"decision":"capture","project":" canonical-project","project_source":"config"}`},
				{name: "project trailing space", prompt: "human", response: `{"decision":"capture","project":"canonical-project ","project_source":"config"}`},
				{name: "missing decision", prompt: "human", response: `{"project":"canonical-project","project_source":"config"}`},
				{name: "wrong decision type", prompt: "human", response: `{"decision":true,"project":"canonical-project","project_source":"config"}`},
				{name: "wrong decision casing", prompt: "human", response: `{"decision":"Capture","project":"canonical-project","project_source":"config"}`},
				{name: "wrong property casing", prompt: "human", response: `{"Decision":"capture","project":"canonical-project","project_source":"config"}`},
				{name: "nested metadata", prompt: "human", response: `{"decision":"capture","nested":{"project":"canonical-project","project_source":"config"}}`},
				{name: "wrong project type", prompt: "human", response: `{"decision":"capture","project":42,"project_source":"config"}`},
				{name: "blank project", prompt: "human", response: `{"decision":"capture","project":" \t","project_source":"config"}`},
				{name: "unknown source", prompt: "human", response: `{"decision":"capture","project":"canonical-project","project_source":"CONFIG"}`},
				{name: "error hint", prompt: "human", response: `{"decision":"capture","project":"canonical-project","project_source":"config","error_hint":"ambiguous"}`},
				{name: "array response", prompt: "human", response: `[{"decision":"capture","project":"canonical-project","project_source":"config"}]`},
			} {
				t.Run(test.name, func(t *testing.T) {
					var mu sync.Mutex
					var contents []string
					decisions := 0
					cwd := t.TempDir()
					port := claudeCodeWindowsPromptServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/prompts/capture-decision":
							var request struct{ Source, Cwd, Content string }
							if r.Method != http.MethodPost {
								t.Errorf("decision method = %s", r.Method)
							}
							if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
								t.Errorf("decode decision: %v", err)
							}
							if request.Source != "claude-code" || request.Cwd != cwd || request.Content != test.prompt {
								t.Errorf("decision input changed: %+v", request)
							}
							mu.Lock()
							decisions++
							mu.Unlock()
							if test.delay != 0 {
								time.Sleep(test.delay)
							}
							status := test.status
							if status == 0 {
								status = http.StatusOK
							}
							if status == http.StatusFound {
								w.Header().Set("Location", "/redirect-target")
							}
							w.WriteHeader(status)
							response := test.response
							if response == "" {
								response = `{"decision":"skip"}`
								if test.want == 1 {
									response = `{"decision":"capture","project":"canonical-project","project_source":"config"}`
								}
							}
							_, _ = w.Write([]byte(response))
						case "/redirect-target":
							t.Error("decision request must not follow redirects")
							_, _ = w.Write([]byte(`{"decision":"capture","project":"canonical-project","project_source":"config"}`))
						case "/project/current":
							t.Error("capture must replace, not add to, current-project read")
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
						stdout = runClaudeCodeWindowsPromptHook(t, claudeCodePowerShell(t), filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "user-prompt-submit.ps1"), port, newSessionID(t), cwd, test.prompt, true)
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
						input, err := json.Marshal(map[string]string{"session_id": newSessionID(t), "cwd": cwd, "prompt": test.prompt})
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
					if decisions != 1 {
						t.Errorf("decision requests = %d, want exactly 1", decisions)
					}
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

// TestClaudeCodeWindowsSafePromptRoute keeps bootstrap builtin-only and performs
// no capture decision, persistence, reminder reads, or external helper calls.
func TestClaudeCodeWindowsSafePromptRoute(t *testing.T) {
	bashEnv := filepath.Join(t.TempDir(), "forbidden-helpers.sh")
	if err := os.WriteFile(bashEnv, []byte(`for helper in jq git curl date dirname cat touch; do
  eval "$helper() { printf 'unexpected helper: $helper\\n' >&2; return 1; }"
done`), 0600); err != nil {
		t.Fatal(err)
	}
	port := claudeCodeWindowsPromptServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("Windows safe route made API request: %s", r.URL.Path)
	}))
	session := newSessionID(t)
	input, err := json.Marshal(map[string]string{"session_id": session, "cwd": t.TempDir(), "prompt": "human"})
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	for turn := 0; turn < 2; turn++ {
		run := exec.Command("bash", bashScriptPath(t, filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "user-prompt-submit.sh")))
		run.Env = append(os.Environ(), "MSYSTEM=MINGW64", "ENGRAM_CLAUDE_WINDOWS_BASH_SAFE_MODE=auto", "ENGRAM_PORT="+port, "TMPDIR="+filepath.ToSlash(stateDir), "BASH_ENV="+bashScriptPath(t, bashEnv))
		run.Stdin = strings.NewReader(string(input))
		output, err := run.CombinedOutput()
		if err != nil {
			t.Fatalf("safe hook failed: %v: %s", err, output)
		}
		payload := decodeHookPayload(t, string(output))
		if turn == 0 {
			assertToolSearchNames(t, selectNames(t, payload.HookSpecificOutput.AdditionalContext))
		} else if strings.TrimSpace(string(output)) != "{}" {
			t.Fatalf("second safe turn = %s", output)
		}
	}
}
