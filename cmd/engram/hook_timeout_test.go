package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestHookSessionConfirmationConfiguredTimeout(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, tc := range []struct {
			name, value                  string
			budget, lookup, registration time.Duration
			deny                         bool
		}{
			{"default", "", 4 * time.Second, 0, 5 * time.Second, true},
			{"blank", " \t ", 4 * time.Second, 0, 5 * time.Second, true},
			{"extended", "7s", 7 * time.Second, 0, 6 * time.Second, false},
			{"trimmed", " 7s ", 7 * time.Second, 0, 6 * time.Second, false},
			{"reduced", "250ms", 250 * time.Millisecond, 0, 300 * time.Millisecond, true},
			{"invalid", "slow", 4 * time.Second, 0, 5 * time.Second, true},
			{"unit required", "8", 4 * time.Second, 0, 5 * time.Second, true},
			{"zero", "0s", 4 * time.Second, 0, 5 * time.Second, true},
			{"negative", "-1s", 4 * time.Second, 0, 5 * time.Second, true},
			{"overflow", "999999999999999999999s", 4 * time.Second, 0, 5 * time.Second, true},
			{"configured shared deadline", "6s", 6 * time.Second, 3200 * time.Millisecond, 3200 * time.Millisecond, true},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				// Pipe readers must live outside the virtual-time bubble: Windows file
				// reads are not durably blocked and would prevent its clock advancing.
				stdout, stderr := captureOutput(t, func() {
					synctest.Test(t, func(t *testing.T) {
						t.Setenv("ENGRAM_HOOK_SESSION_CONFIRMATION_TIMEOUT", tc.value)
						useLegacyLocalHookEndpoint(t, "http://127.0.0.1:7437")
						t.Setenv("PI_CODING_AGENT", "")
						oldTransport, oldStdin, oldOutput, oldExit := http.DefaultTransport, os.Stdin, claudeHookOutput, exitFunc
						t.Cleanup(func() {
							http.DefaultTransport, os.Stdin, claudeHookOutput, exitFunc = oldTransport, oldStdin, oldOutput, oldExit
						})
						started := time.Now()
						var shared context.Context
						lookups, posts, completed, health, owners := 0, 0, 0, 0, 0
						http.DefaultTransport = codexRoundTripper(func(r *http.Request) (*http.Response, error) {
							deadline, ok := r.Context().Deadline()
							if !ok || !deadline.Equal(started.Add(tc.budget)) {
								t.Fatalf("deadline = %v, want start + %v", deadline, tc.budget)
							}
							if shared == nil {
								shared = r.Context()
							} else if r.Context() != shared {
								t.Fatal("HTTP steps did not share one context")
							}
							body, status, delay := "", http.StatusOK, time.Duration(0)
							switch r.URL.Path {
							case "/health":
								health++
								body = hookLocalMockHealthBody(t)
							case "/sessions/host":
								owners++
								status = http.StatusNotFound
							case "/project/current":
								lookups++
								delay = tc.lookup
								body = `{"project":"project-a","project_source":"config"}`
								if r.Method != http.MethodGet || r.URL.Query().Get("cwd") != "/work" {
									t.Fatalf("unexpected lookup: %s %s", r.Method, r.URL)
								}
							case "/sessions", "/runtime-sessions/resolve":
								posts++
								delay = tc.registration
								if (r.URL.Path == "/sessions") != (agent == "claude") {
									t.Fatalf("wrong agent endpoint: %s", r.URL.Path)
								}
								var input map[string]string
								if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
									t.Fatal(err)
								}
								if r.Method != http.MethodPost || input["id"] != "host" || input["project"] != "project-a" || input["directory"] != "/work" || (input["ownership_mode"] == "project_owned") != (agent == "claude") {
									t.Fatalf("wrong confirmation request: %s %v", r.Method, input)
								}
								body = `{"id":"host","status":"resolved"}`
								if agent == "claude" {
									body = `{"id":"host","status":"created"}`
									status = http.StatusCreated
								}
							default:
								t.Fatalf("unexpected request: %s", r.URL)
							}
							timer := time.NewTimer(delay)
							defer timer.Stop()
							select {
							case <-r.Context().Done():
								return nil, r.Context().Err()
							case <-timer.C:
							}
							if r.Method == http.MethodPost {
								completed++
							}
							return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
						})
						os.Stdin = claudeHookStdin(t, `{"session_id":"host","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{"session_id":"model","title":"preserved"}}`, false)
						var output []byte
						writes, exits := 0, 0
						claudeHookOutput = func(b []byte) error { writes++; output = append(output, b...); return nil }
						exitFunc = func(code int) { exits++; t.Errorf("unexpected exit %d", code) }
						cmdHook([]string{agent + "-pre-tool-use"})
						if writes != 1 || exits != 0 {
							t.Fatalf("unexpected output/exit: writes=%d exits=%d", writes, exits)
						}
						if lookups != 1 || posts != 1 || (agent == "claude" && (owners != 1 || health != 1)) || (agent == "codex" && (owners != 0 || health != 1)) {
							t.Fatalf("request counters lookup/posts/owners/health=%d/%d/%d/%d", lookups, posts, owners, health)
						}
						if tc.deny {
							prefix := "Claude"
							if agent == "codex" {
								prefix = "Codex"
							}
							want := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"` + prefix + ` host session confirmation timed out (server slow or unavailable)"}}`
							if string(output) != want || completed != 0 || time.Since(started) != tc.budget {
								t.Fatalf("denial output=%s completed=%d elapsed=%v, want %s and %v", output, completed, time.Since(started), want, tc.budget)
							}
						} else {
							var response struct {
								HookSpecificOutput struct {
									HookEventName, PermissionDecision, PermissionDecisionReason string
									UpdatedInput                                                map[string]any
								}
							}
							if err := json.Unmarshal(output, &response); err != nil {
								t.Fatal(err)
							}
							got := response.HookSpecificOutput
							wantDecision := ""
							if agent == "codex" {
								wantDecision = "allow"
							}
							if got.HookEventName != "PreToolUse" || got.PermissionDecision != wantDecision || got.PermissionDecisionReason != "" || got.UpdatedInput["session_id"] != "host" || got.UpdatedInput["title"] != "preserved" || len(got.UpdatedInput) != 2 || completed != 1 || time.Since(started) != tc.lookup+tc.registration {
								t.Fatalf("confirmation output=%s completed=%d elapsed=%v", output, completed, time.Since(started))
							}
						}
					})
				})
				if stdout != "" || stderr != "" {
					t.Fatalf("unexpected stdout=%q stderr=%q", stdout, stderr)
				}
			})
		}
	}
}

func TestHookSessionConfirmationTimeoutHelp(t *testing.T) {
	stdout, stderr := captureOutput(t, printUsage)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	for _, want := range []string{
		"ENGRAM_HOOK_SESSION_CONFIRMATION_TIMEOUT",
		"Claude/Codex PreToolUse (default: 4s)",
		"positive Go duration, e.g. 8s or 500ms",
		"invalid, or nonpositive values use 4s",
		"Increase the host hook timeout",
		"Does not change Codex prompt capture or bypass session confirmation",
		"Mitigates slow confirmation; does not fix autosync latency",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help missing %q", want)
		}
	}
}
