package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Existing persisted ownership wins even when client inspection would fail.
// Ended shared roots must bind the live continuation, not resurrect the root.
func TestHookExternalProjectRoutingRegisteredClaudeOwner(t *testing.T) {
	binary := buildCompatibilityBinary(t)
	for _, ended := range []bool{false, true} {
		name := "live-owner-control"
		if ended {
			name = "ended-shared-continuation-control"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			cwd := filepath.Join(root, "absent", "feature-x")
			type call struct {
				Method, Path string
				Body         map[string]any
			}
			var mu sync.Mutex
			var calls []call
			ownerDir := "/persisted/myproj"
			effective := "host"
			if ended {
				effective = "host:resume:1"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				var body map[string]any
				if len(data) > 0 && json.Unmarshal(data, &body) != nil {
					t.Error("invalid body")
				}
				mu.Lock()
				calls = append(calls, call{r.Method, r.URL.Path, body})
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/sessions/host":
					row := map[string]any{"id": "host", "project": "myproj", "directory": ownerDir, "ownership_mode": "project_owned"}
					if ended {
						row["ended_at"] = "2025-01-01T00:00:00Z"
						row["ownership_mode"] = "shared"
					}
					_ = json.NewEncoder(w).Encode(row)
				case "/runtime-sessions/resolve":
					_, _ = io.WriteString(w, `{"id":"host:resume:1","status":"resolved","resumed_from":"host"}`)
				case "/sessions":
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]string{"id": effective, "status": "created"})
				default:
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			env := hookProjectFixtureEnvironment(t, root, server.URL)
			input, _ := json.Marshal(map[string]any{"cwd": cwd, "session_id": "host", "unrelated": "never-echo-this", "tool_name": "mcp__engram__mem_save", "tool_input": map[string]string{"session_id": "model", "project": "feature-x", "title": "keep-title"}})
			before := externalTree(t, root)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "hook", "claude-pre-tool-use")
			cmd.Dir, cmd.Env, cmd.Stdin = root, env, bytes.NewReader(input)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			t.Logf("exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			if err != nil || stderr.Len() != 0 {
				t.Errorf("want exit 0 and empty stderr")
			}
			var response struct {
				Output struct {
					Decision string         `json:"permissionDecision"`
					Input    map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			wantInput := map[string]any{"session_id": effective, "project": "feature-x", "title": "keep-title"}
			if json.Unmarshal(stdout.Bytes(), &response) != nil || response.Output.Decision != "" || !reflect.DeepEqual(response.Output.Input, wantInput) {
				t.Errorf("owner binding output=%s", stdout.String())
			}
			want := []call{{"GET", "/sessions/host", nil}}
			directory := cwd
			registration := map[string]any{"id": effective, "project": "myproj", "directory": directory, "ownership_mode": "project_owned"}
			if ended {
				want = append(want, call{"POST", "/runtime-sessions/resolve", map[string]any{"id": "host", "project": "myproj", "directory": ownerDir, "ownership_mode": "shared"}})
				registration = map[string]any{"id": effective, "project": "myproj", "directory": ownerDir}
			}
			want = append(want, call{"POST", "/sessions", registration})
			mu.Lock()
			actual := append([]call(nil), calls...)
			mu.Unlock()
			if !reflect.DeepEqual(actual, want) {
				t.Errorf("HTTP got=%#v want=%#v", actual, want)
			}
			if !reflect.DeepEqual(externalTree(t, root), before) {
				t.Error("registered-owner control changed complete fixture")
			}
		})
	}
}

// S4-S5: selecting identity is separate from runtime scope validation. Shared
// callers still need usable client inspection; a known owner is not a fallback.
func TestHookExternalProjectRoutingCodexOwners(t *testing.T) {
	binary := buildCompatibilityBinary(t)
	for _, action := range []string{"codex-register", "codex-resolve", "codex-session-end", "codex-pre-tool-use", "codex-user-prompt-submit"} {
		for _, scenario := range []string{"owned-conflicting-client", "owned-unusable-client", "shared-cross-project", "shared-unusable-client", "ended-shared-continuation"} {
			t.Run(action+"/"+scenario, func(t *testing.T) {
				root := t.TempDir()
				cwd := filepath.Join(root, "feature-x")
				if err := os.Mkdir(cwd, 0700); err != nil {
					t.Fatal(err)
				}
				hookProjectFixtureConfig(t, cwd, "feature-x")
				unusable := strings.Contains(scenario, "unusable")
				if unusable {
					cwd = filepath.Join(root, "absent", "feature-x")
				}
				owned := strings.HasPrefix(scenario, "owned")
				ended := scenario == "ended-shared-continuation"
				denied := unusable && !owned
				mode := "shared"
				selected := "feature-x"
				if owned {
					mode = "project_owned"
					selected = "myproj"
				}
				ownerDir := "/persisted/myproj"
				effective := "host"
				if ended {
					effective = "host:resume:2"
				}
				type call struct {
					Method, Path string
					Body         map[string]any
				}
				var mu sync.Mutex
				var calls []call
				runtimeCalls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, _ := io.ReadAll(r.Body)
					var body map[string]any
					if len(data) > 0 && json.Unmarshal(data, &body) != nil {
						t.Error("invalid JSON request")
					}
					mu.Lock()
					calls = append(calls, call{r.Method, r.URL.RequestURI(), body})
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/sessions/host":
						row := map[string]any{"id": "host", "project": "myproj", "directory": ownerDir, "ownership_mode": mode}
						if ended {
							row["ended_at"] = "2025-01-01T00:00:00Z"
						}
						_ = json.NewEncoder(w).Encode(row)
					case "/project/current":
						// Keep the old implementation responsive so RED names the routing/body
						// violation rather than an incidental unavailable-server failure.
						_, _ = io.WriteString(w, `{"project":"feature-x","project_source":"dir_basename"}`)
					case "/health":
						_, _ = io.WriteString(w, `{"status":"ok","service":"engram","capabilities":{"runtime_session_resolution":true}}`)
					case "/sessions":
						w.WriteHeader(http.StatusCreated)
						ack := map[string]string{"id": effective, "status": "created"}
						if ended {
							ack["resumed_from"] = "host"
						}
						_ = json.NewEncoder(w).Encode(ack)
					case "/runtime-sessions/resolve", "/runtime-sessions/end":
						runtimeCalls++
						if ended && runtimeCalls == 1 {
							w.WriteHeader(http.StatusConflict)
							_, _ = io.WriteString(w, `{"error":"runtime session scope conflict"}`)
							return
						}
						status := "resolved"
						if r.URL.Path == "/runtime-sessions/end" {
							status = "ended"
						}
						ack := map[string]string{"id": effective, "status": status}
						if ended {
							ack["resumed_from"] = "host"
						}
						_ = json.NewEncoder(w).Encode(ack)
					case "/prompts":
						w.WriteHeader(http.StatusCreated)
						_, _ = io.WriteString(w, `{}`)
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL)
						w.WriteHeader(500)
					}
				}))
				defer server.Close()
				env := hookProjectFixtureEnvironment(t, root, server.URL)
				for i, v := range env {
					if strings.HasPrefix(v, "TEMP=") || strings.HasPrefix(v, "TMP=") || strings.HasPrefix(v, "TMPDIR=") {
						env[i] = strings.SplitN(v, "=", 2)[0] + "=" + filepath.Join(root, "temp")
					}
				}
				// Model project is deliberately neither the inspected project nor the owner.
				input, _ := json.Marshal(map[string]any{"cwd": cwd, "session_id": "host", "prompt": "private-prompt", "unrelated": "never-echo-this", "tool_name": "mcp__engram__mem_save", "tool_input": map[string]any{"session_id": "model", "project": "model-project", "title": "keep-title"}})
				before := externalTree(t, root)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "hook", action)
				cmd.Dir, cmd.Env, cmd.Stdin = root, env, bytes.NewReader(input)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				t.Logf("exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				if err != nil || stderr.Len() != 0 {
					t.Error("want exit 0 and empty stderr")
				}
				want := []call{{"GET", "/sessions/host", nil}}
				if !denied {
					scope := map[string]any{"id": "host", "project": selected, "directory": cwd}
					if action == "codex-register" {
						scope["resume"] = true
						want = append(want, call{"POST", "/sessions", scope})
					} else {
						operation := "resolve"
						if action == "codex-session-end" {
							operation = "end"
						} else {
							want = append(want, call{"GET", "/health", nil})
						}
						path := "/runtime-sessions/" + operation
						want = append(want, call{"POST", path, scope})
						if ended {
							// Preserve the actual-409-only retry and its fresh authoritative read.
							// Only directory/mode change; caller project remains feature-x.
							want = append(want, call{"GET", "/sessions/host", nil}, call{"POST", path, map[string]any{"id": "host", "project": selected, "directory": ownerDir, "ownership_mode": "shared"}})
						}
						if action == "codex-user-prompt-submit" {
							want = append(want, call{"POST", "/prompts", map[string]any{"session_id": effective, "project": selected, "content": "private-prompt"}})
						}
					}
				}
				mu.Lock()
				actual := append([]call(nil), calls...)
				mu.Unlock()
				if !reflect.DeepEqual(actual, want) {
					t.Errorf("HTTP got=%#v want=%#v", actual, want)
				}
				switch action {
				case "codex-user-prompt-submit":
					expected, _ := json.Marshal(map[string]string{"systemMessage": codexToolSearchMessage})
					if stdout.String() != string(expected) {
						t.Errorf("stdout=%q want %q", stdout.String(), expected)
					}
					sum := sha256.Sum256([]byte("host"))
					marker := filepath.Join(root, "temp", "engram-codex-"+hex.EncodeToString(sum[:])+"-tools-loaded")
					info, err := os.Stat(marker)
					if err != nil || info.Size() != 0 {
						t.Errorf("expected empty tools-loaded marker: %v", err)
					} else {
						before[marker] = externalTreeEntry{info.Mode(), ""}
					}
				case "codex-pre-tool-use":
					var response struct {
						Output struct {
							Decision string         `json:"permissionDecision"`
							Input    map[string]any `json:"updatedInput"`
						} `json:"hookSpecificOutput"`
					}
					if json.Unmarshal(stdout.Bytes(), &response) != nil {
						t.Error("invalid hook JSON")
					}
					if denied {
						if response.Output.Decision != "deny" || response.Output.Input != nil {
							t.Errorf("expected denial: %s", stdout.String())
						}
					} else {
						wantInput := map[string]any{"session_id": effective, "project": "model-project", "title": "keep-title"}
						if response.Output.Decision != "allow" || !reflect.DeepEqual(response.Output.Input, wantInput) {
							t.Errorf("binding/arguments changed: %s", stdout.String())
						}
					}
				default:
					expected := effective + "\n"
					if denied || action == "codex-session-end" {
						expected = ""
					}
					if stdout.String() != expected {
						t.Errorf("stdout=%q want %q", stdout.String(), expected)
					}
				}
				if strings.Contains(stdout.String(), "private-prompt") || strings.Contains(stdout.String(), "never-echo-this") {
					t.Error("private input leaked")
				}
				if !reflect.DeepEqual(externalTree(t, root), before) {
					t.Error("complete fixture changed outside exact permitted marker effect")
				}
			})
		}
	}
}
