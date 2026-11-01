package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// These are public CLI probes. The foreign filesystem answer below is a model,
// not evidence of an SSH connection or a second host.
func TestHookExternalProjectRouting(t *testing.T) {
	binary := buildCompatibilityBinary(t)
	actions := []string{"claude-pre-tool-use", "codex-pre-tool-use", "codex-register", "codex-resolve", "codex-session-end", "codex-user-prompt-submit"}
	for _, action := range actions {
		for _, mode := range []string{"external", "external-invalid", "local-port-control", "local-blank-url-control"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				cwd := filepath.Join(root, "feature-x")
				if err := os.Mkdir(cwd, 0700); err != nil {
					t.Fatal(err)
				}
				hookProjectFixtureConfig(t, cwd, "myproj")
				if mode == "external-invalid" {
					cwd = filepath.Join(root, "absent", "feature-x")
				}
				external := strings.HasPrefix(mode, "external")
				env := hookProjectFixtureEnvironment(t, root, "")
				var instanceID string
				project := "feature-x"
				if external {
					project = "myproj"
				}
				type request struct {
					Method, Path string
					Body         map[string]any
				}
				var mu sync.Mutex
				var got []request
				if !external {
					var err error
					instanceID, err = store.EnsureInstanceID(filepath.Join(root, "engram_data_dir"))
					if err != nil {
						t.Fatal(err)
					}
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					var fields map[string]any
					if len(body) > 0 && json.Unmarshal(body, &fields) != nil {
						t.Error("invalid request JSON")
					}
					mu.Lock()
					got = append(got, request{r.Method, r.URL.RequestURI(), fields})
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/project/current":
						// Server interprets a client path as feature-x. Never consult this in explicit URL mode.
						_, _ = io.WriteString(w, `{"project":"feature-x","project_source":"dir_basename"}`)
					case "/sessions/host":
						w.WriteHeader(http.StatusNotFound)
						_, _ = io.WriteString(w, `{}`)
					case "/health":
						_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "service": "engram", "instance_id": instanceID, "capabilities": map[string]bool{"runtime_session_resolution": true}})
					case "/sessions":
						w.WriteHeader(http.StatusCreated)
						_, _ = io.WriteString(w, `{"id":"host","status":"created"}`)
					case "/runtime-sessions/resolve":
						_, _ = io.WriteString(w, `{"id":"host","status":"resolved"}`)
					case "/runtime-sessions/end":
						_, _ = io.WriteString(w, `{"id":"host","status":"ended"}`)
					case "/prompts":
						w.WriteHeader(http.StatusCreated)
						_, _ = io.WriteString(w, `{}`)
					default:
						t.Errorf("unexpected HTTP %s %s", r.Method, r.URL)
						w.WriteHeader(500)
					}
				}))
				defer server.Close()
				for i, v := range env {
					if strings.HasPrefix(v, "ENGRAM_URL=") {
						env[i] = "ENGRAM_URL=" + server.URL
					}
					if strings.HasPrefix(v, "TEMP=") || strings.HasPrefix(v, "TMP=") || strings.HasPrefix(v, "TMPDIR=") {
						env[i] = strings.SplitN(v, "=", 2)[0] + "=" + filepath.Join(root, "temp")
					}
				}
				if !external {
					u, _ := url.Parse(server.URL)
					for i, v := range env {
						if strings.HasPrefix(v, "ENGRAM_URL=") {
							env[i] = "ENGRAM_URL="
							if mode == "local-blank-url-control" {
								env[i] = "ENGRAM_URL=   "
							}
						}
					}
					env = append(env, "ENGRAM_PORT="+u.Port())
				}
				input, _ := json.Marshal(map[string]any{"cwd": cwd, "session_id": "host", "prompt": "private-prompt", "unrelated": "never-echo-this", "tool_name": "mcp__engram__mem_save", "tool_input": map[string]any{"session_id": "model", "title": "keep-title", "project": "myproj"}})
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
					t.Errorf("want exit 0 and empty stderr")
				}
				invalid := mode == "external-invalid"
				var want []request
				add := func(method, path string, body map[string]any) { want = append(want, request{method, path, body}) }
				if action == "claude-pre-tool-use" && !external {
					add("GET", "/health", nil)
				}
				// Explicit external Codex selection must distinguish persisted owners
				// from unknown/shared sessions before deciding whether inspection is needed.
				if action == "claude-pre-tool-use" || external {
					add("GET", "/sessions/host", nil)
				}
				if !invalid {
					if !external {
						add("GET", "/project/current?cwd="+url.QueryEscape(cwd), nil)
					}
					scope := map[string]any{"id": "host", "directory": cwd, "project": project}
					switch action {
					case "claude-pre-tool-use":
						scope["ownership_mode"] = "project_owned"
						add("POST", "/sessions", scope)
					case "codex-register":
						scope["resume"] = true
						add("POST", "/sessions", scope)
					case "codex-session-end":
						add("POST", "/runtime-sessions/end", scope)
					default:
						add("GET", "/health", nil)
						add("POST", "/runtime-sessions/resolve", scope)
						if action == "codex-user-prompt-submit" {
							add("POST", "/prompts", map[string]any{"session_id": "host", "project": project, "content": "private-prompt"})
						}
					}
				}
				mu.Lock()
				actual := append([]request(nil), got...)
				mu.Unlock()
				if !reflect.DeepEqual(actual, want) {
					t.Errorf("HTTP transcript\ngot  %#v\nwant %#v", actual, want)
				}
				switch action {
				case "codex-user-prompt-submit":
					expected, _ := json.Marshal(map[string]string{"systemMessage": codexToolSearchMessage})
					if stdout.String() != string(expected) {
						t.Errorf("prompt output=%q want %q", stdout.String(), expected)
					}
					// Only the exact empty tools-loaded marker may be added, including on failure.
					sum := sha256.Sum256([]byte("host"))
					marker := filepath.Join(root, "temp", "engram-codex-"+hex.EncodeToString(sum[:])+"-tools-loaded")
					info, statErr := os.Stat(marker)
					if statErr != nil || info.Size() != 0 {
						t.Errorf("marker stat=%v info=%v", statErr, info)
					} else {
						before[marker] = externalTreeEntry{info.Mode(), ""}
					}
				case "claude-pre-tool-use", "codex-pre-tool-use":
					var response struct {
						Output struct {
							Decision string         `json:"permissionDecision"`
							Input    map[string]any `json:"updatedInput"`
						} `json:"hookSpecificOutput"`
					}
					if json.Unmarshal(stdout.Bytes(), &response) != nil {
						t.Errorf("invalid hook output")
					}
					if invalid {
						if response.Output.Decision != "deny" || response.Output.Input != nil {
							t.Errorf("inspection failure did not deny: %s", stdout.String())
						}
					} else {
						wantInput := map[string]any{"session_id": "host", "title": "keep-title", "project": "myproj"}
						if !reflect.DeepEqual(response.Output.Input, wantInput) {
							t.Errorf("updated input=%v want %v", response.Output.Input, wantInput)
						}
						decision := ""
						if action == "codex-pre-tool-use" {
							decision = "allow"
						}
						if response.Output.Decision != decision {
							t.Errorf("permission changed: %s", stdout.String())
						}
					}
				default:
					expected := "host\n"
					if invalid || action == "codex-session-end" {
						expected = ""
					}
					if stdout.String() != expected {
						t.Errorf("stdout=%q want %q", stdout.String(), expected)
					}
				}
				if strings.Contains(stdout.String(), "never-echo-this") || strings.Contains(stdout.String(), "private-prompt") {
					t.Error("private input echoed")
				}
				if after := externalTree(t, root); !reflect.DeepEqual(after, before) {
					t.Errorf("complete fixture changed\nbefore=%v\nafter=%v", before, after)
				}
			})
		}
	}
}

type externalTreeEntry struct {
	Mode    os.FileMode
	Content string
}

// Includes every directory/file mode and byte (or symlink target), without exclusions.
func externalTree(t *testing.T, root string) map[string]externalTreeEntry {
	t.Helper()
	result := map[string]externalTreeEntry{}
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		content := ""
		if info.Mode()&os.ModeSymlink != 0 {
			content, err = os.Readlink(path)
		} else if info.Mode().IsRegular() {
			var b []byte
			b, err = os.ReadFile(path)
			content = string(b)
		}
		if err != nil {
			return err
		}
		result[path] = externalTreeEntry{info.Mode(), content}
		return nil
	})
	if err != nil {
		t.Fatal(fmt.Errorf("snapshot: %w", err))
	}
	return result
}
