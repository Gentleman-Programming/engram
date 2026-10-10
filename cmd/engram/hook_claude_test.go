package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/mcp"
	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func claudeHookStdin(t *testing.T, input string, closed bool) *os.File {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	if _, err := writer.Write([]byte(input)); err != nil {
		t.Fatalf("write Claude hook input: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	if closed {
		if err := reader.Close(); err != nil {
			t.Fatalf("close stdin reader: %v", err)
		}
	}
	return reader
}

func TestClaudeEndedRegistrationCannotPersistBoundWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes the Claude SessionStart bash hook")
	}
	for _, binary := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("requires %s: %v", binary, err)
		}
	}
	root := t.TempDir()
	db, err := store.New(store.FallbackConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const host = "ended-claude-host"
	if err := db.CreateSession(host, "project-a", root); err != nil {
		t.Fatal(err)
	}
	if err := db.EndSession(host, "finished"); err != nil {
		t.Fatal(err)
	}
	var registrationStatus int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/ended-claude-host":
			_, _ = io.WriteString(w, `{"id":"ended-claude-host","project":"project-a","directory":"/work","started_at":"2026-01-01 00:00:00","ended_at":"2026-01-02 00:00:00"}`)
		case r.URL.Path == "/project/current":
			_, _ = w.Write([]byte(`{"project":"project-a","project_source":"config"}`))
		case r.URL.Path == "/sessions" && r.Method == http.MethodPost:
			var req struct {
				ID            string `json:"id"`
				Project       string `json:"project"`
				Directory     string `json:"directory"`
				OwnershipMode string `json:"ownership_mode"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("registration body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if req.ID != host || req.Project != "project-a" || req.OwnershipMode != "project_owned" {
				t.Errorf("registration = %+v", req)
			}
			err := db.StartSessionWithOwnershipMode(req.ID, req.Project, req.Directory, req.OwnershipMode)
			if !errors.Is(err, store.ErrSessionAlreadyEnded) {
				t.Errorf("registration error = %v, want already ended", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			registrationStatus = http.StatusConflict
			w.WriteHeader(registrationStatus)
			_, _ = w.Write([]byte(`{"code":"session_already_ended"}`))
		case r.URL.Path == "/context":
			_, _ = w.Write([]byte(`{"context":""}`))
		default:
			t.Errorf("unexpected hook request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	stubDir := filepath.Join(root, "bin")
	if err := os.Mkdir(stubDir, 0700); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nif [ \"$#\" -eq 3 ] && [ \"$1\" = setup ] && [ \"$2\" = claude-code ] && [ \"$3\" = --mcp-only ]; then exit 0; fi\nprintf 'unexpected engram invocation\\n' >&2\nexit 99\n"
	if err := os.WriteFile(filepath.Join(stubDir, "engram"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"session_id": host, "cwd": root})
	cmd := exec.Command("bash", filepath.Join("..", "..", "plugin", "claude-code", "scripts", "session-start.sh"))
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+root, "CLAUDE_CONFIG_DIR="+filepath.Join(root, "claude"), "ENGRAM_DATA_DIR="+filepath.Join(root, "data"), "ENGRAM_URL="+server.URL, "ENGRAM_SOCKET=", "ENGRAM_PROJECT=", "ENGRAM_PORT=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SessionStart: %v: %s", err, out)
	}
	if registrationStatus != http.StatusConflict {
		t.Fatalf("registration status = %d, want 409; hook output: %s", registrationStatus, out)
	}
	request, _ := json.Marshal(map[string]any{"session_id": host, "cwd": root, "tool_name": "mcp__engram__mem_save", "tool_input": map[string]any{"title": "ended host write", "content": "must not persist", "session_id": "foreign-model-session", "project": "project-a"}})
	var hook struct {
		HookSpecificOutput struct {
			PermissionDecision string         `json:"permissionDecision"`
			UpdatedInput       map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	t.Setenv("ENGRAM_URL", server.URL)
	if err := json.Unmarshal(guardClaudePreToolUse(request), &hook); err != nil {
		t.Fatal(err)
	}
	if hook.HookSpecificOutput.PermissionDecision != "deny" || hook.HookSpecificOutput.UpdatedInput != nil {
		t.Fatalf("ended host must be denied without bound input: %+v", hook)
	}
	// Claude does not dispatch denied tool calls to MCP.

	observations, err := db.AllObservations("project-a", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := db.SessionObservations(host, 100)
	if err != nil {
		t.Fatal(err)
	}
	ended, err := db.GetSession(host)
	if err != nil {
		t.Fatal(err)
	}
	if ended.EndedAt == nil {
		t.Fatal("409 registration unexpectedly reopened ended session")
	}
	if _, err := db.GetSession("foreign-model-session"); err == nil {
		t.Fatal("foreign model session was created")
	}
	if len(observations) != 0 || len(bound) != 0 {
		t.Fatalf("ended host write persisted %d observations after 409", len(observations))
	}

	// Direct/manual MCP calls bypass the Claude hook; this is not an agent-path write.
	call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_save", "arguments": map[string]any{"title": "manual write", "content": "direct MCP control", "session_id": host, "project": "project-a"}}})
	response := mcp.NewServerWithConfig(db, mcp.MCPConfig{DefaultProject: "project-a"}, nil).HandleMessage(context.Background(), call)
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"isError":true`) || !strings.Contains(string(encoded), "manual write") {
		t.Fatalf("direct/manual MCP write result: %s", encoded)
	}
	observations, err = db.AllObservations("project-a", "", 100)
	if err != nil || len(observations) != 1 {
		t.Fatalf("direct/manual MCP persistence: count=%d, err=%v, response=%s", len(observations), err, encoded)
	}
}

func TestCodexEndedSessionStart409DeniesWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes Codex SessionStart bash hook")
	}
	for _, binary := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("requires %s: %v", binary, err)
		}
	}
	root := t.TempDir()
	db, err := store.New(store.FallbackConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateSessionWithOwnershipMode("host", "foreign-project", root, store.SessionOwnershipProjectOwned); err != nil {
		t.Fatal(err)
	}
	if err := db.EndSession("host", "finished"); err != nil {
		t.Fatal(err)
	}
	production := server.New(db, 0).Handler()
	registrationStatus := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/project/current" {
			_, _ = w.Write([]byte(`{"project":"project-a","project_source":"config"}`))
			return
		}
		if r.URL.Path == "/context" {
			_, _ = w.Write([]byte(`{"context":""}`))
			return
		}
		if r.URL.Path == "/runtime-sessions/resolve" || r.URL.Path == "/health" {
			production.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/sessions" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		capture := httptest.NewRecorder()
		production.ServeHTTP(capture, r)
		registrationStatus = capture.Code
		w.WriteHeader(capture.Code)
		_, _ = w.Write(capture.Body.Bytes())
	}))
	defer endpoint.Close()
	codexTestHookBinary(t, root)
	input, _ := json.Marshal(map[string]string{"session_id": "host", "cwd": root})
	command := exec.Command("bash", filepath.Join("..", "..", "plugin", "codex", "scripts", "session-start.sh"))
	command.Stdin = strings.NewReader(string(input))
	command.Env = append(os.Environ(), "HOME="+root, "ENGRAM_DATA_DIR="+filepath.Join(root, "data"), "ENGRAM_URL="+endpoint.URL, "ENGRAM_SOCKET=", "ENGRAM_PROJECT=", "ENGRAM_PORT=")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Codex SessionStart: %v: %s", err, output)
	}
	if registrationStatus != 409 || strings.Contains(string(output), `"session_id":"host"`) || strings.Contains(string(output), "Registered runtime session") {
		t.Fatalf("409 handoff: status=%d output=%s", registrationStatus, output)
	}
	t.Setenv("ENGRAM_URL", endpoint.URL)
	response := guardCodexPreToolUse([]byte(`{"session_id":"host","cwd":"` + filepath.ToSlash(root) + `","tool_name":"mcp__engram__mem_save","tool_input":{"session_id":"model","project":"project-a","title":"blocked"}}`))
	var result struct {
		HookSpecificOutput struct {
			PermissionDecision string          `json:"permissionDecision"`
			UpdatedInput       json.RawMessage `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(response, &result); err != nil || result.HookSpecificOutput.PermissionDecision != "deny" || result.HookSpecificOutput.UpdatedInput != nil {
		t.Fatalf("response=%s err=%v", response, err)
	}
	observations, err := db.AllObservations("project-a", "", 100)
	if err != nil || len(observations) != 0 {
		t.Fatalf("observations=%d err=%v", len(observations), err)
	}
	ended, err := db.GetSession("host")
	if err != nil || ended.EndedAt == nil {
		t.Fatalf("ended=%+v err=%v", ended, err)
	}
}

func TestCodexCallConfirmsSharedHostBeforeBinding(t *testing.T) {
	root := t.TempDir()
	db, err := store.New(store.FallbackConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateSessionWithOwnershipMode("host", "project-a", root, store.SessionOwnershipShared); err != nil {
		t.Fatal(err)
	}
	production := server.New(db, 0).Handler()
	confirmations := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/project/current" {
			if r.URL.Query().Get("cwd") != filepath.ToSlash(root) {
				t.Errorf("cwd = %q", r.URL.Query().Get("cwd"))
			}
			_, _ = w.Write([]byte(`{"project":"project-b","project_source":"config"}`))
			return
		}
		if r.URL.Path == "/health" {
			production.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/runtime-sessions/resolve" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(404)
			return
		}
		var registration struct {
			ID        string `json:"id"`
			Project   string `json:"project"`
			Ownership string `json:"ownership_mode"`
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal(body, &registration); err != nil {
			t.Error(err)
		}
		if registration.ID != "host" || registration.Project != "project-b" || registration.Ownership != "" {
			t.Errorf("registration = %+v", registration)
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		capture := httptest.NewRecorder()
		production.ServeHTTP(capture, r)
		var confirmation struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(capture.Body.Bytes(), &confirmation); err != nil {
			t.Error(err)
		}
		if capture.Code != http.StatusOK || confirmation.ID != "host" || confirmation.Status != "resolved" {
			t.Errorf("registration response: %d %s", capture.Code, capture.Body.String())
		}
		confirmations++
		w.WriteHeader(capture.Code)
		_, _ = w.Write(capture.Body.Bytes())
	}))
	defer endpoint.Close()
	t.Setenv("ENGRAM_URL", endpoint.URL)
	input := []byte(`{"session_id":"host","cwd":"` + filepath.ToSlash(root) + `","tool_name":"mcp__engram__mem_save","tool_input":{"session_id":"model","project":"project-b","title":"retained","content":"cross-project observation"}}`)
	var result struct {
		HookSpecificOutput struct {
			PermissionDecision string         `json:"permissionDecision"`
			UpdatedInput       map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(guardCodexPreToolUse(input), &result); err != nil {
		t.Fatal(err)
	}
	if confirmations != 1 || result.HookSpecificOutput.PermissionDecision != "allow" || result.HookSpecificOutput.UpdatedInput["session_id"] != "host" || result.HookSpecificOutput.UpdatedInput["project"] != "project-b" {
		t.Fatalf("confirmations=%d result=%+v", confirmations, result)
	}
	call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_save", "arguments": result.HookSpecificOutput.UpdatedInput}})
	mcpResult := mcp.NewServerWithConfig(db, mcp.MCPConfig{DefaultProject: "project-a"}, nil).HandleMessage(context.Background(), call)
	encoded, err := json.Marshal(mcpResult)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"isError":true`) {
		t.Fatalf("bound MCP write failed: %s", encoded)
	}
	observations, err := db.AllObservations("project-b", "", 100)
	if err != nil || len(observations) != 1 {
		t.Fatalf("project B observations=%d err=%v result=%s", len(observations), err, encoded)
	}
	bound, err := db.SessionObservations("host", 100)
	if err != nil || len(bound) != 1 || bound[0].ID != observations[0].ID {
		t.Fatalf("host observations=%v err=%v", bound, err)
	}
	owner, err := db.GetSession("host")
	if err != nil || owner.Project != "project-a" || owner.OwnershipMode != store.SessionOwnershipShared || owner.EndedAt != nil {
		t.Fatalf("shared owner=%+v err=%v", owner, err)
	}
	if _, err := db.GetSession("model"); err == nil {
		t.Fatal("foreign model session created")
	}
}

func TestCodexInvalidExplicitPortDenies(t *testing.T) {
	for _, port := range []string{"invalid", "0", "65536"} {
		t.Run(port, func(t *testing.T) {
			t.Setenv("ENGRAM_URL", "")
			t.Setenv("ENGRAM_SOCKET", "")
			t.Setenv("ENGRAM_PORT", port)
			response := guardCodexPreToolUse([]byte(`{"session_id":"host","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{}}`))
			var result struct {
				HookSpecificOutput struct {
					PermissionDecision string          `json:"permissionDecision"`
					UpdatedInput       json.RawMessage `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(response, &result); err != nil || result.HookSpecificOutput.PermissionDecision != "deny" || result.HookSpecificOutput.UpdatedInput != nil {
				t.Fatalf("response=%s err=%v", response, err)
			}
		})
	}
}

func TestCodexUnconfirmedCallsDenyWithoutUpdatedInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"ended", 409, `{"code":"session_already_ended"}`},
		{"unavailable", 503, `{}`},
		{"wrong id", 201, `{"id":"other","status":"created"}`},
		{"wrong status", 201, `{"id":"host","status":"failed"}`},
		{"malformed", 201, `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resolverRequests atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/project/current" {
					_, _ = w.Write([]byte(`{"project":"project-a","project_source":"config"}`))
					return
				}
				if r.URL.Path == "/health" {
					codexMockHealth(w)
					return
				}
				if r.URL.Path != "/runtime-sessions/resolve" || r.Method != http.MethodPost {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				resolverRequests.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer endpoint.Close()
			t.Setenv("ENGRAM_URL", endpoint.URL)
			response := guardCodexPreToolUse([]byte(`{"session_id":"host","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{}}`))
			var result struct {
				HookSpecificOutput struct {
					PermissionDecision string          `json:"permissionDecision"`
					UpdatedInput       json.RawMessage `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(response, &result); err != nil || result.HookSpecificOutput.PermissionDecision != "deny" || result.HookSpecificOutput.UpdatedInput != nil {
				t.Fatalf("response=%s err=%v", response, err)
			}
			if got := resolverRequests.Load(); got != 1 {
				t.Fatalf("resolver requests=%d, want 1", got)
			}
		})
	}
}

func TestCodexGuardRejectsMissingHostIdentityWithoutNetwork(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unconfirmed call contacted server: %s %s", r.Method, r.URL)
	}))
	defer endpoint.Close()
	t.Setenv("ENGRAM_URL", endpoint.URL)

	for _, tc := range []struct {
		name  string
		input string
	}{
		{"missing cwd", `{"session_id":"host","tool_name":"mcp__engram__mem_save","tool_input":{}}`},
		{"blank cwd", `{"session_id":"host","cwd":"  ","tool_name":"mcp__engram__mem_save","tool_input":{}}`},
		{"missing session", `{"cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{}}`},
		{"blank session", `{"session_id":"  ","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result struct {
				HookSpecificOutput struct {
					PermissionDecision string          `json:"permissionDecision"`
					UpdatedInput       json.RawMessage `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			response := guardCodexPreToolUse([]byte(tc.input))
			if err := json.Unmarshal(response, &result); err != nil || result.HookSpecificOutput.PermissionDecision != "deny" || result.HookSpecificOutput.UpdatedInput != nil {
				t.Fatalf("want deny without updated input: %s, err=%v", response, err)
			}
		})
	}
}

func TestCodexGuardReadOnlyAndNonEngramSkipNetwork(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("read-only call contacted server: %s %s", r.Method, r.URL)
	}))
	defer endpoint.Close()
	t.Setenv("ENGRAM_URL", endpoint.URL)

	for _, tool := range []string{"mcp__engram__mem_search", "mcp__plugin_engram_engram__mem_search", "Bash"} {
		t.Run(tool, func(t *testing.T) {
			input, err := json.Marshal(map[string]string{"tool_name": tool})
			if err != nil {
				t.Fatal(err)
			}
			if got := string(guardCodexPreToolUse(input)); got != "{}" {
				t.Fatalf("want untouched read-only input, got %s", got)
			}
		})
	}
}

func TestClaudeInvalidExplicitPortDeniesWithoutDefaultServer(t *testing.T) {
	for _, port := range []string{"invalid", "0", "65536"} {
		t.Run(port, func(t *testing.T) {
			t.Setenv("ENGRAM_URL", "")
			t.Setenv("ENGRAM_SOCKET", "")
			t.Setenv("ENGRAM_PORT", port)
			response := guardClaudePreToolUse([]byte(`{"session_id":"host","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{}}`))
			var result struct {
				HookSpecificOutput struct {
					PermissionDecision string `json:"permissionDecision"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(response, &result); err != nil || result.HookSpecificOutput.PermissionDecision != "deny" {
				t.Fatalf("explicit invalid port must deny: %s, %v", response, err)
			}
		})
	}
}

func TestHookSessionConfirmationLatency(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, tc := range []struct {
			name      string
			project   time.Duration
			register  time.Duration
			wantDeny  bool
			wantPosts int32
		}{
			{"slow healthy server", 900 * time.Millisecond, 900 * time.Millisecond, false, 1},
			{"project timeout", 5 * time.Second, 0, true, 0},
			{"registration timeout", 0, 5 * time.Second, true, 1},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				var gets, posts, registrations atomic.Int32
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					delay := tc.project
					if r.URL.Path == "/health" {
						codexMockHealth(w)
						return
					}
					switch r.URL.Path {
					case "/sessions/host":
						w.WriteHeader(http.StatusNotFound)
						return
					case "/project/current":
						gets.Add(1)
					case "/sessions", "/runtime-sessions/resolve":
						if (r.URL.Path == "/sessions") != (agent == "claude") {
							t.Errorf("unexpected agent endpoint: %s", r.URL.Path)
						}
						posts.Add(1)
						delay = tc.register
						var registration map[string]string
						if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
							t.Errorf("registration body: %v", err)
						}
						if registration["id"] != "host" || registration["project"] != "project-a" || registration["directory"] != "/work" {
							t.Errorf("unexpected registration: %v", registration)
						}
						if (registration["ownership_mode"] == "project_owned") != (agent == "claude") {
							t.Errorf("ownership mode changed: %v", registration)
						}
					default:
						t.Errorf("unexpected endpoint: %s", r.URL.Path)
						return
					}
					timer := time.NewTimer(delay)
					defer timer.Stop()
					select {
					case <-r.Context().Done():
						return
					case <-timer.C:
					}
					if r.URL.Path == "/project/current" {
						_, _ = io.WriteString(w, `{"project":"project-a","project_source":"config"}`)
						return
					}
					registrations.Add(1)
					if agent == "codex" {
						_, _ = io.WriteString(w, `{"id":"host","status":"resolved"}`)
						return
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"id":"host","status":"created"}`)
				}))
				defer endpoint.Close()
				t.Setenv("ENGRAM_URL", endpoint.URL)
				oldStdin, oldOutput, oldExit := os.Stdin, claudeHookOutput, exitFunc
				t.Cleanup(func() { os.Stdin, claudeHookOutput, exitFunc = oldStdin, oldOutput, oldExit })
				os.Stdin = claudeHookStdin(t, `{"session_id":"host","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{"session_id":"model","title":"preserved"}}`, false)
				var output []byte
				claudeHookOutput = func(value []byte) error { output = append([]byte(nil), value...); return nil }
				exitFunc = func(code int) { t.Errorf("unexpected process exit: %d", code) }
				cmdHook([]string{agent + "-pre-tool-use"})
				var response struct {
					HookSpecificOutput struct {
						HookEventName            string         `json:"hookEventName"`
						PermissionDecision       string         `json:"permissionDecision"`
						PermissionDecisionReason string         `json:"permissionDecisionReason"`
						UpdatedInput             map[string]any `json:"updatedInput"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal(output, &response); err != nil {
					t.Fatalf("invalid hook output %s: %v", output, err)
				}
				got := response.HookSpecificOutput
				if got.HookEventName != "PreToolUse" {
					t.Fatalf("wrong hook event: %s", output)
				}
				if tc.wantDeny {
					prefix := "Claude"
					if agent == "codex" {
						prefix = "Codex"
					}
					wantReason := prefix + " host session confirmation timed out (server slow or unavailable)"
					if got.PermissionDecision != "deny" || got.PermissionDecisionReason != wantReason || got.UpdatedInput != nil {
						t.Errorf("want timeout denial without updated input, got %s", output)
					}
					if registrations.Load() != 0 {
						t.Errorf("timed out registration completed")
					}
				} else {
					wantDecision := ""
					if agent == "codex" {
						wantDecision = "allow"
					}
					if got.PermissionDecision != wantDecision || got.PermissionDecisionReason != "" || got.UpdatedInput["session_id"] != "host" || got.UpdatedInput["title"] != "preserved" || registrations.Load() != 1 {
						t.Errorf("want confirmed bound input, got %s", output)
					}
				}
				if gets.Load() != 1 || posts.Load() != tc.wantPosts {
					t.Errorf("unexpected requests: GET=%d POST=%d", gets.Load(), posts.Load())
				}
			})
		}
	}
}

// TestHookSessionConfirmationSharedDeadline uses virtual time and an in-memory
// transport so scheduler load cannot move the timeout from registration to lookup.
func TestHookSessionConfirmationSharedDeadline(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				t.Setenv("ENGRAM_URL", "http://hook.test")
				oldTransport := http.DefaultTransport
				oldStdin, oldOutput, oldExit := os.Stdin, claudeHookOutput, exitFunc
				t.Cleanup(func() {
					http.DefaultTransport = oldTransport
					os.Stdin, claudeHookOutput, exitFunc = oldStdin, oldOutput, oldExit
				})
				started := time.Now()
				var projectContext context.Context
				var gets, posts, registrations int
				http.DefaultTransport = codexRoundTripper(func(r *http.Request) (*http.Response, error) {
					deadline, ok := r.Context().Deadline()
					if !ok || !deadline.Equal(started.Add(4*time.Second)) {
						t.Fatalf("confirmation deadline = %v, want start + 4s", deadline)
					}
					if r.URL.Path == "/sessions/host" {
						return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
					}
					if r.URL.Path == "/health" {
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"ok","service":"engram","capabilities":{"runtime_session_resolution":true}}`))}, nil
					}
					switch r.URL.Path {
					case "/project/current":
						gets++
						projectContext = r.Context()
						if r.Method != http.MethodGet || r.URL.Query().Get("cwd") != "/work" {
							t.Fatalf("unexpected project request: %s %s", r.Method, r.URL)
						}
					case "/sessions", "/runtime-sessions/resolve":
						if (r.URL.Path == "/sessions") != (agent == "claude") {
							t.Fatalf("unexpected agent endpoint: %s", r.URL.Path)
						}
						posts++
						if r.Context() != projectContext || time.Since(started) != 2100*time.Millisecond {
							t.Fatal("registration did not start after lookup with the same context")
						}
						var registration map[string]string
						if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
							t.Fatal(err)
						}
						if r.Method != http.MethodPost || registration["id"] != "host" || registration["project"] != "project-a" || registration["directory"] != "/work" || (registration["ownership_mode"] == "project_owned") != (agent == "claude") {
							t.Fatalf("unexpected registration: %s %v", r.Method, registration)
						}
					default:
						t.Fatalf("unexpected request: %s", r.URL)
					}
					timer := time.NewTimer(2100 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-r.Context().Done():
						return nil, r.Context().Err()
					case <-timer.C:
					}
					body, status := `{"project":"project-a","project_source":"config"}`, http.StatusOK
					if r.URL.Path == "/sessions" {
						registrations++
						body, status = `{"id":"host","status":"created"}`, http.StatusCreated
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})
				os.Stdin = claudeHookStdin(t, `{"session_id":"host","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{"session_id":"model","title":"preserved"}}`, false)
				var output []byte
				var writes int
				claudeHookOutput = func(value []byte) error { output = append(output, value...); writes++; return nil }
				exitFunc = func(code int) { t.Errorf("unexpected process exit: %d", code) }
				cmdHook([]string{agent + "-pre-tool-use"})
				prefix := "Claude"
				if agent == "codex" {
					prefix = "Codex"
				}
				want := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"` + prefix + ` host session confirmation timed out (server slow or unavailable)"}}`
				if string(output) != want || writes != 1 {
					t.Fatalf("hook output = %s (%d writes), want %s", output, writes, want)
				}
				if gets != 1 || posts != 1 || registrations != 0 {
					t.Errorf("GET=%d POST=%d completed registrations=%d, want 1/1/0", gets, posts, registrations)
				}
				if elapsed := time.Since(started); elapsed != 4*time.Second {
					t.Errorf("virtual confirmation duration = %v, want 4s", elapsed)
				}
				if !errors.Is(projectContext.Err(), context.DeadlineExceeded) {
					t.Errorf("shared context error = %v, want deadline exceeded", projectContext.Err())
				}
			})
		})
	}
}

func TestClaudeRegistrationRequiresMatchingCreatedResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unavailable", http.StatusServiceUnavailable, `{}`},
		{"mismatched id", http.StatusCreated, `{"id":"other","status":"created"}`},
		{"malformed", http.StatusCreated, `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/project/current" {
					_, _ = w.Write([]byte(`{"project":"project-a","project_source":"config"}`))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			t.Setenv("ENGRAM_URL", server.URL)
			input := []byte(`{"session_id":"host","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{}}`)
			var result struct {
				HookSpecificOutput struct {
					PermissionDecision string         `json:"permissionDecision"`
					UpdatedInput       map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(guardClaudePreToolUse(input), &result); err != nil || result.HookSpecificOutput.PermissionDecision != "deny" || result.HookSpecificOutput.UpdatedInput != nil {
				t.Fatalf("must deny unconfirmed registration: %+v, %v", result, err)
			}
		})
	}
}

// Keep this fixture outside host Git ancestry without changing package-wide discovery.
func isolateClaudeGitFixture(t *testing.T, root string) {
	t.Helper()
	ceiling := root
	if inherited := os.Getenv("GIT_CEILING_DIRECTORIES"); inherited != "" {
		ceiling += string(os.PathListSeparator) + inherited
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", ceiling)
}

func TestClaudeFixtureGitCeiling(t *testing.T) {
	root := t.TempDir()
	inherited := filepath.Join(root, "existing") + string(os.PathListSeparator) + filepath.Join(root, "other")
	for _, tt := range []struct {
		name     string
		existing string
		want     string
	}{
		{"empty inherited ceiling", "", root},
		{"preserve inherited ceilings", inherited, root + string(os.PathListSeparator) + inherited},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GIT_CEILING_DIRECTORIES", tt.existing)
			t.Run("fixture", func(t *testing.T) {
				isolateClaudeGitFixture(t, root)
				if got := os.Getenv("GIT_CEILING_DIRECTORIES"); got != tt.want {
					t.Fatalf("fixture Git ceiling = %q, want %q", got, tt.want)
				}
			})
			if got := os.Getenv("GIT_CEILING_DIRECTORIES"); got != tt.existing {
				t.Fatalf("inherited Git ceiling after fixture = %q, want %q", got, tt.existing)
			}
		})
	}
}

func TestClaudeWriteGateBindsRegisteredSessionToOwnerProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("requires git: %v", err)
	}
	root := t.TempDir()
	isolateClaudeGitFixture(t, root)
	db, err := store.New(store.FallbackConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	production := server.New(db, 0).Handler()
	endpoint := httptest.NewServer(production)
	defer endpoint.Close()
	t.Setenv("ENGRAM_URL", endpoint.URL)

	gate := func(t *testing.T, session, cwd string) (decision, reason string, updatedInput map[string]any) {
		t.Helper()
		request, _ := json.Marshal(map[string]any{
			"session_id": session, "cwd": cwd, "tool_name": "mcp__engram__mem_save",
			"tool_input": map[string]any{"title": "gate write", "content": "bound", "session_id": "foreign-model-session"},
		})
		var hook struct {
			HookSpecificOutput struct {
				PermissionDecision       string         `json:"permissionDecision"`
				PermissionDecisionReason string         `json:"permissionDecisionReason"`
				UpdatedInput             map[string]any `json:"updatedInput"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(guardClaudePreToolUse(request), &hook); err != nil {
			t.Fatalf("gate output for %s from %s: %v", session, cwd, err)
		}
		bound := hook.HookSpecificOutput
		return bound.PermissionDecision, bound.PermissionDecisionReason, bound.UpdatedInput
	}
	requireAllowed := func(t *testing.T, session, cwd string) {
		t.Helper()
		decision, reason, updatedInput := gate(t, session, cwd)
		if decision == "deny" {
			t.Fatalf("registered session %s must keep writing from %s: deny %q", session, cwd, reason)
		}
		if updatedInput["session_id"] != session || updatedInput["title"] != "gate write" {
			t.Fatalf("bound input for %s = %v", session, updatedInput)
		}
		sessionRow, err := db.GetSession(session)
		if err != nil || sessionRow.EndedAt != nil {
			t.Fatalf("session %s after gate: %+v err=%v", session, sessionRow, err)
		}
	}
	resolvesTo := func(t *testing.T, cwd string, wantProject string, wantStatus int) {
		t.Helper()
		response, err := http.Get(endpoint.URL + "/project/current?cwd=" + url.QueryEscape(cwd))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != wantStatus {
			t.Fatalf("/project/current?cwd=%s = %d %s, want status %d", cwd, response.StatusCode, body, wantStatus)
		}
		if wantProject != "" && !strings.Contains(string(body), "\"project\":\""+wantProject+"\"") {
			t.Fatalf("/project/current?cwd=%s = %s, want project %q", cwd, body, wantProject)
		}
	}

	// Layout 1 (issue matrix): non-git project dir with .engram/config.json, write from a subfolder.
	clientDir := filepath.Join(root, "client")
	if err := os.MkdirAll(filepath.Join(clientDir, ".engram"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(clientDir, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clientDir, ".engram", "config.json"), []byte(`{"project_name":"client"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolvesTo(t, clientDir, "client", http.StatusOK)
	requireAllowed(t, "s1", clientDir)
	resolvesTo(t, filepath.Join(clientDir, "docs"), "docs", http.StatusOK)
	requireAllowed(t, "s1", filepath.Join(clientDir, "docs"))

	// Layout 2 (issue matrix): umbrella config dir, write from a child git repository.
	umbrellaDir := filepath.Join(root, "umbrella")
	if err := os.MkdirAll(filepath.Join(umbrellaDir, ".engram"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(umbrellaDir, ".engram", "config.json"), []byte(`{"project_name":"umbrella"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(umbrellaDir, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", filepath.Join(umbrellaDir, "child")).CombinedOutput(); err != nil {
		t.Fatalf("git init umbrella child: %v: %s", err, out)
	}
	requireAllowed(t, "s2", umbrellaDir)
	resolvesTo(t, filepath.Join(umbrellaDir, "child"), "child", http.StatusOK)
	requireAllowed(t, "s2", filepath.Join(umbrellaDir, "child"))

	// Layout 3 (issue matrix): cwd is an unrelated directory whose basename resolves elsewhere.
	strayDir := filepath.Join(root, "removed-worktree")
	if err := os.MkdirAll(strayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	resolvesTo(t, strayDir, "removed-worktree", http.StatusOK)
	requireAllowed(t, "s1", strayDir)

	// Layout 5 (gantit): clone into the session's non-git cwd; the cwd never moves.
	wsDir := filepath.Join(root, "ws")
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	requireAllowed(t, "s5", wsDir)
	bareDir := filepath.Join(root, "child.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", bareDir).CombinedOutput(); err != nil {
		t.Fatalf("git init bare: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "clone", "-q", bareDir, filepath.Join(wsDir, "child")).CombinedOutput(); err != nil {
		t.Fatalf("git clone into session cwd: %v: %s", err, out)
	}
	// Pin the remote label independently of the platform's local path separators.
	// This only changes fixture metadata; no network operation is performed.
	if out, err := exec.Command("git", "-C", filepath.Join(wsDir, "child"), "remote", "set-url", "origin", "https://example.invalid/child.git").CombinedOutput(); err != nil {
		t.Fatalf("git set fixture origin: %v: %s", err, out)
	}
	resolvesTo(t, wsDir, "", http.StatusConflict) // project_transition_conflict before the binding exists
	requireAllowed(t, "s5", wsDir)
	resolvesTo(t, filepath.Join(wsDir, "child"), "child", http.StatusOK) // one resolution writes the binding
	resolvesTo(t, wsDir, "child", http.StatusOK)                         // git_child promotion
	requireAllowed(t, "s5", wsDir)
	owner, err := db.GetSession("s5")
	if err != nil || owner.Project != "ws" {
		t.Fatalf("session s5 owner = %+v err=%v, want project ws after clone binding", owner, err)
	}

	// Layout 4 (issue matrix): ambiguous cwd must still deny an unregistered session, naming the cause.
	multiDir := filepath.Join(root, "multi")
	for _, repo := range []string{"r1", "r2"} {
		if err := os.MkdirAll(filepath.Join(multiDir, repo), 0o700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "init", "-q", filepath.Join(multiDir, repo)).CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v: %s", repo, err, out)
		}
	}
	ambiguousResponse, err := http.Get(endpoint.URL + "/project/current?cwd=" + url.QueryEscape(multiDir))
	if err != nil {
		t.Fatal(err)
	}
	ambiguousBody, _ := io.ReadAll(ambiguousResponse.Body)
	_ = ambiguousResponse.Body.Close()
	if ambiguousResponse.StatusCode != http.StatusOK || !strings.Contains(string(ambiguousBody), `"project_source":"ambiguous"`) || !strings.Contains(string(ambiguousBody), `"error_hint"`) {
		t.Fatalf("ambiguous precondition /project/current?cwd=%s = %d %s", multiDir, ambiguousResponse.StatusCode, ambiguousBody)
	}
	decision, reason, updatedInput := gate(t, "s4-unknown", multiDir)
	if decision != "deny" || updatedInput != nil {
		t.Fatalf("ambiguous cwd for unregistered session must deny: %+v", decision)
	}
	if reason == "" || strings.Contains(reason, "registration could not be confirmed") || !strings.Contains(reason, "ambiguous") {
		t.Fatalf("ambiguous denial must name the cause, got %q", reason)
	}
	if _, err := db.GetSession("s4-unknown"); err == nil {
		t.Fatal("denied ambiguous registration created a session")
	}

	// S6: an ended session denies with its own cause, distinct from the generic message.
	if response, err := http.Post(endpoint.URL+"/sessions/s1/end", "application/json", strings.NewReader(`{}`)); err != nil || response.StatusCode != http.StatusOK {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatalf("end s1: err=%v status=%v", err, response)
	}
	decision, reason, updatedInput = gate(t, "s1", clientDir)
	if decision != "deny" || updatedInput != nil {
		t.Fatalf("ended session must deny: %q", decision)
	}
	if reason == "" || strings.Contains(reason, "registration could not be confirmed") || !strings.Contains(reason, "already ended") {
		t.Fatalf("ended-session denial must name the cause, got %q", reason)
	}
}

func TestClaudeConflictDenialNamesSessionProjects(t *testing.T) {
	// Defensive naming contract: if a registration still conflicts, the denial
	// carries the server's session_project_conflict fields instead of the generic text.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/s9":
			_, _ = io.WriteString(w, `{"id":"s9","project":"client","directory":"/w","started_at":"2026-10-10 00:00:00"}`)
		case r.URL.Path == "/project/current":
			_, _ = io.WriteString(w, `{"project":"client","project_source":"config"}`)
		case r.URL.Path == "/sessions" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"session_project_conflict","error":"session ownership does not match write project: session \"s9\" belongs to \"client\", not \"docs\"","owner_project":"client","requested_project":"docs","session_id":"s9"}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ENGRAM_URL", server.URL)
	input, _ := json.Marshal(map[string]any{"session_id": "s9", "cwd": "/w/docs", "tool_name": "mcp__engram__mem_save", "tool_input": map[string]any{"title": "t", "content": "c"}})
	var hook struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(guardClaudePreToolUse(input), &hook); err != nil {
		t.Fatal(err)
	}
	reason := hook.HookSpecificOutput.PermissionDecisionReason
	if hook.HookSpecificOutput.PermissionDecision != "deny" || reason == "" || strings.Contains(reason, "registration could not be confirmed") || !strings.Contains(reason, "session_project_conflict") || !strings.Contains(reason, "client") || !strings.Contains(reason, "docs") {
		t.Fatalf("conflict denial must name code and projects, got %q", reason)
	}
}

func TestClaudeDegradedSessionLookupFailsClosed(t *testing.T) {
	// A session lookup that errors (5xx) must deny with a named cause instead
	// of silently re-deriving the write's project from the cwd: a degraded
	// answer is not "first registration" (#1717 review follow-up).
	var derived bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/s9":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"boom"}`)
		case r.URL.Path == "/project/current":
			derived = true
			_, _ = io.WriteString(w, `{"project":"docs","project_source":"dir_basename"}`)
		case r.URL.Path == "/sessions" && r.Method == http.MethodPost:
			t.Errorf("cwd-derived registration reached the server after a failed lookup")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"s9","status":"created"}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ENGRAM_URL", server.URL)
	input, _ := json.Marshal(map[string]any{"session_id": "s9", "cwd": "/w/docs", "tool_name": "mcp__engram__mem_save", "tool_input": map[string]any{"title": "t", "content": "c"}})
	var hook struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(guardClaudePreToolUse(input), &hook); err != nil {
		t.Fatal(err)
	}
	reason := hook.HookSpecificOutput.PermissionDecisionReason
	if hook.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(reason, "session lookup failed") || derived {
		t.Fatalf("degraded lookup must fail closed with a named cause, got %q derived=%v", reason, derived)
	}
}

func TestClaudeSessionLookupOutcomes(t *testing.T) {
	// Pin every session-lookup outcome (#1762 review follow-up): transport
	// failures, malformed 2xx payloads, and mismatched session IDs deny
	// without ever touching cwd resolution; a legacy blank-project row defers
	// to the cwd path and allows the write.
	for _, tc := range []struct {
		name       string
		sessionGet func(w http.ResponseWriter)
		wantAllow  bool
	}{
		{"transport failure denies without cwd resolution", nil, false},
		{"malformed 2xx session denies without cwd resolution", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{`)
		}, false},
		{"mismatched session id denies", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"id":"other","project":"docs"}`)
		}, false},
		{"missing project field denies", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"id":"s9"}`)
		}, false},
		{"null project denies", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"id":"s9","project":null}`)
		}, false},
		{"blank project defers to cwd resolution", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"id":"s9","project":""}`)
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var derived, posted bool
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/sessions/s9":
					tc.sessionGet(w)
				case r.URL.Path == "/project/current":
					derived = true
					_, _ = io.WriteString(w, `{"project":"docs","project_source":"dir_basename"}`)
				case r.URL.Path == "/sessions" && r.Method == http.MethodPost:
					posted = true
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"id":"s9","status":"created"}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Setenv("ENGRAM_URL", endpoint.URL)
			if tc.sessionGet == nil {
				endpoint.Close() // transport failure: connection refused
			} else {
				defer endpoint.Close()
			}
			input, _ := json.Marshal(map[string]any{"session_id": "s9", "cwd": "/w/docs", "tool_name": "mcp__engram__mem_save", "tool_input": map[string]any{"title": "t", "content": "c"}})
			var hook struct {
				HookSpecificOutput struct {
					PermissionDecision       string         `json:"permissionDecision"`
					PermissionDecisionReason string         `json:"permissionDecisionReason"`
					UpdatedInput             map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(guardClaudePreToolUse(input), &hook); err != nil {
				t.Fatal(err)
			}
			bound := hook.HookSpecificOutput
			if tc.wantAllow {
				if bound.PermissionDecision == "deny" || bound.UpdatedInput["session_id"] != "s9" || !derived || !posted {
					t.Fatalf("blank-project row must defer to cwd resolution and allow: decision=%q derived=%v posted=%v (%s)", bound.PermissionDecision, derived, posted, bound.PermissionDecisionReason)
				}
				return
			}
			if bound.PermissionDecision != "deny" || bound.UpdatedInput != nil || derived || posted {
				t.Fatalf("%s must deny without cwd resolution or registration: decision=%q derived=%v posted=%v (%s)", tc.name, bound.PermissionDecision, derived, posted, bound.PermissionDecisionReason)
			}
			if !strings.Contains(bound.PermissionDecisionReason, "session lookup failed") {
				t.Fatalf("%s must name the lookup cause, got %q", tc.name, bound.PermissionDecisionReason)
			}
		})
	}
}

func TestClaudeLargeSessionLookupStillConfirms(t *testing.T) {
	// GET /sessions/{id} returns the full session, including its summary; a
	// legitimate 65,536-character summary must not truncate the payload into
	// a malformed-session denial (#1762 review: only error bodies are bounded).
	hugeSummary := strings.Repeat("s", 65536)
	for _, tc := range []struct {
		name     string
		session  string
		post     string
		postCode int
		wantDeny string
	}{
		{
			name:     "active session with huge summary confirms without cwd resolution",
			session:  `{"id":"s9","project":"client","summary":"` + hugeSummary + `"}`,
			post:     `{"id":"s9","status":"created"}`,
			postCode: http.StatusCreated,
		},
		{
			name:     "ended session with huge summary keeps the already-ended cause",
			session:  `{"id":"s9","project":"client","ended_at":"2026-01-01 00:00:00","summary":"` + hugeSummary + `"}`,
			post:     `{"code":"session_already_ended","session_id":"s9"}`,
			postCode: http.StatusConflict,
			wantDeny: "already ended",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var derived bool
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/sessions/s9":
					_, _ = io.WriteString(w, tc.session)
				case r.URL.Path == "/project/current":
					derived = true
					_, _ = io.WriteString(w, `{"project":"docs","project_source":"dir_basename"}`)
				case r.URL.Path == "/sessions" && r.Method == http.MethodPost:
					w.WriteHeader(tc.postCode)
					_, _ = io.WriteString(w, tc.post)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer endpoint.Close()
			t.Setenv("ENGRAM_URL", endpoint.URL)
			input, _ := json.Marshal(map[string]any{"session_id": "s9", "cwd": "/w/docs", "tool_name": "mcp__engram__mem_save", "tool_input": map[string]any{"title": "t", "content": "c"}})
			var hook struct {
				HookSpecificOutput struct {
					PermissionDecision       string         `json:"permissionDecision"`
					PermissionDecisionReason string         `json:"permissionDecisionReason"`
					UpdatedInput             map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(guardClaudePreToolUse(input), &hook); err != nil {
				t.Fatal(err)
			}
			bound := hook.HookSpecificOutput
			if tc.wantDeny == "" {
				if bound.PermissionDecision == "deny" || bound.UpdatedInput["session_id"] != "s9" || derived {
					t.Fatalf("large session must confirm against its owner: decision=%q derived=%v (%s)", bound.PermissionDecision, derived, bound.PermissionDecisionReason)
				}
				return
			}
			if bound.PermissionDecision != "deny" || !strings.Contains(bound.PermissionDecisionReason, tc.wantDeny) || derived {
				t.Fatalf("large ended session must keep its named cause: decision=%q derived=%v (%s)", bound.PermissionDecision, derived, bound.PermissionDecisionReason)
			}
		})
	}
}

func TestShouldCheckForUpdatesSkipsInternalHook(t *testing.T) {
	if shouldCheckForUpdates([]string{"hook", "claude-pre-tool-use"}) {
		t.Fatal("internal hook must not run the update check before emitting a Claude hook response")
	}
}

func TestCmdHookWritesTransformedResponse(t *testing.T) {
	oldStdin, oldOutput := os.Stdin, claudeHookOutput
	t.Cleanup(func() { os.Stdin, claudeHookOutput = oldStdin, oldOutput })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/project/current" {
			_, _ = w.Write([]byte(`{"project":"project-a","project_source":"config"}`))
			return
		}
		if r.URL.Path == "/sessions/claude-session" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/sessions" {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"claude-session","status":"created"}`))
			return
		}
		t.Errorf("unexpected request: %s", r.URL)
	}))
	defer server.Close()
	t.Setenv("ENGRAM_URL", server.URL)
	os.Stdin = claudeHookStdin(t, `{"session_id":"claude-session","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{"title":"decision"}}`, false)
	var output []byte
	claudeHookOutput = func(response []byte) error { output = append([]byte(nil), response...); return nil }
	cmdHook([]string{"claude-pre-tool-use"})
	var response struct {
		HookSpecificOutput struct {
			UpdatedInput       map[string]any `json:"updatedInput"`
			PermissionDecision string         `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil || response.HookSpecificOutput.UpdatedInput["session_id"] != "claude-session" || response.HookSpecificOutput.PermissionDecision != "" {
		t.Fatalf("successful hook output = %s, %v", output, err)
	}
}

func TestClaudeAdapterPersistsWritesForDistinctSameWorktreeHosts(t *testing.T) {
	root := t.TempDir()
	db, err := store.New(store.FallbackConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const project = "same-worktree"
	hosts := []string{"claude-host-one", "claude-host-two"}
	for _, host := range hosts {
		if err := db.CreateSession(host, project, root); err != nil {
			t.Fatal(err)
		}
	}
	production := server.New(db, 0).Handler()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/project/current" {
			_, _ = io.WriteString(w, `{"project":"same-worktree","project_source":"config"}`)
			return
		}
		production.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	t.Setenv("ENGRAM_URL", endpoint.URL)
	oldStdin, oldOutput := os.Stdin, claudeHookOutput
	t.Cleanup(func() { os.Stdin, claudeHookOutput = oldStdin, oldOutput })
	mcpServer := mcp.NewServerWithConfig(db, mcp.MCPConfig{DefaultProject: project}, nil)
	want := map[string]map[string]int{
		hosts[0]: {"one first": 1, "one second": 1},
		hosts[1]: {"two first": 1, "two second": 1},
	}
	for _, step := range []struct{ host, title string }{
		{hosts[0], "one first"}, {hosts[1], "two first"},
		{hosts[0], "one second"}, {hosts[1], "two second"},
	} {
		request, _ := json.Marshal(map[string]any{
			"session_id": step.host, "cwd": root, "tool_name": "mcp__engram__mem_save",
			"tool_input": map[string]any{"title": step.title, "content": step.title, "project": project, "session_id": "foreign-model-session"},
		})
		os.Stdin = claudeHookStdin(t, string(request), false)
		var output []byte
		claudeHookOutput = func(data []byte) error { output = append([]byte(nil), data...); return nil }
		cmdHook([]string{"claude-pre-tool-use"})
		var hook struct {
			HookSpecificOutput struct {
				PermissionDecision string         `json:"permissionDecision"`
				UpdatedInput       map[string]any `json:"updatedInput"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(output, &hook); err != nil {
			t.Fatal(err)
		}
		bound := hook.HookSpecificOutput
		if bound.PermissionDecision == "deny" || bound.UpdatedInput["session_id"] != step.host || bound.UpdatedInput["project"] != project {
			t.Fatalf("host %s bound output = %s", step.host, output)
		}
		call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_save", "arguments": bound.UpdatedInput}})
		result := mcpServer.HandleMessage(context.Background(), call)
		encoded, err := json.Marshal(result)
		if err != nil || strings.Contains(string(encoded), `"isError":true`) || !strings.Contains(string(encoded), step.title) {
			t.Fatalf("host %s MCP result = %s, err=%v", step.host, encoded, err)
		}
	}
	all, err := db.AllObservations(project, "", 100)
	if err != nil || len(all) != 4 {
		t.Fatalf("project observations = %d, err=%v", len(all), err)
	}
	for host, expected := range want {
		observations, err := db.SessionObservations(host, 100)
		if err != nil || len(observations) != 2 {
			t.Fatalf("host %s observations = %v, err=%v", host, observations, err)
		}
		counts := make(map[string]int)
		for _, observation := range observations {
			counts[observation.Title]++
		}
		for title, count := range expected {
			if counts[title] != count {
				t.Fatalf("host %s title %q count = %d, want %d; all titles: %v", host, title, counts[title], count, counts)
			}
		}
		if len(counts) != len(expected) {
			t.Fatalf("host %s has unexpected titles: %v", host, counts)
		}
	}
	if _, err := db.GetSession("foreign-model-session"); err == nil {
		t.Fatal("foreign model session was created")
	}
}

func TestClaudeShellLifecyclePersistsOnlyLiveHostWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes the Claude SessionStart bash hook")
	}
	for _, binary := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("requires %s: %v", binary, err)
		}
	}
	root := t.TempDir()
	db, err := store.New(store.FallbackConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const project = "same-worktree"
	hosts := []string{"shell-host-one", "shell-host-two"}
	const endedHost = "shell-host-ended"
	production := server.New(db, 0).Handler()
	var registrations, conflicts atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/project/current" {
			_, _ = io.WriteString(w, `{"project":"same-worktree","project_source":"config"}`)
			return
		}
		if r.URL.Path == "/sessions" && r.Method == http.MethodPost {
			registrations.Add(1)
			capture := &statusCapture{ResponseWriter: w}
			production.ServeHTTP(capture, r)
			if capture.status == http.StatusConflict {
				conflicts.Add(1)
			}
			return
		}
		production.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	stubDir := filepath.Join(root, "bin")
	if err := os.Mkdir(stubDir, 0700); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nif [ \"$#\" -eq 3 ] && [ \"$1\" = setup ] && [ \"$2\" = claude-code ] && [ \"$3\" = --mcp-only ]; then exit 0; fi\nexit 99\n"
	if err := os.WriteFile(filepath.Join(stubDir, "engram"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_URL", endpoint.URL)
	t.Setenv("HOME", root)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("ENGRAM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("ENGRAM_SOCKET", "")
	t.Setenv("ENGRAM_PROJECT", "")
	t.Setenv("ENGRAM_PORT", "")
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	start := func(host string) {
		t.Helper()
		input, _ := json.Marshal(map[string]string{"session_id": host, "cwd": root})
		cmd := exec.Command("bash", filepath.Join(packageDir, "..", "..", "plugin", "claude-code", "scripts", "session-start.sh"))
		cmd.Dir = root
		cmd.Stdin = strings.NewReader(string(input))
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("SessionStart %s: %v: %s", host, err, out)
		}
	}
	for _, host := range hosts {
		start(host)
		row, err := db.GetSession(host)
		if err != nil || row.EndedAt != nil {
			t.Fatalf("SessionStart %s did not persist live session: %+v, %v", host, row, err)
		}
	}
	if registered, conflicted := registrations.Load(), conflicts.Load(); registered != 2 || conflicted != 0 {
		t.Fatalf("initial registrations = %d, conflicts = %d", registered, conflicted)
	}
	oldStdin, oldOutput := os.Stdin, claudeHookOutput
	t.Cleanup(func() { os.Stdin, claudeHookOutput = oldStdin, oldOutput })
	mcpServer := mcp.NewServerWithConfig(db, mcp.MCPConfig{DefaultProject: project}, nil)
	dispatches := 0
	preToolUse := func(host, title string) (string, map[string]any) {
		t.Helper()
		request, _ := json.Marshal(map[string]any{"session_id": host, "cwd": root, "tool_name": "mcp__engram__mem_save", "tool_input": map[string]any{"title": title, "content": title, "project": project, "session_id": "foreign-model-session"}})
		os.Stdin = claudeHookStdin(t, string(request), false)
		var output []byte
		claudeHookOutput = func(data []byte) error { output = append([]byte(nil), data...); return nil }
		cmdHook([]string{"claude-pre-tool-use"})
		var hook struct {
			HookSpecificOutput struct {
				PermissionDecision string         `json:"permissionDecision"`
				UpdatedInput       map[string]any `json:"updatedInput"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(output, &hook); err != nil {
			t.Fatalf("hook response %s: %v", output, err)
		}
		return hook.HookSpecificOutput.PermissionDecision, hook.HookSpecificOutput.UpdatedInput
	}
	want := map[string]map[string]int{hosts[0]: {"one first": 1, "one second": 1}, hosts[1]: {"two first": 1, "two second": 1}}
	for _, step := range []struct{ host, title string }{{hosts[0], "one first"}, {hosts[1], "two first"}, {hosts[0], "one second"}, {hosts[1], "two second"}} {
		decision, bound := preToolUse(step.host, step.title)
		if decision == "deny" || bound["session_id"] != step.host || bound["project"] != project {
			t.Fatalf("host %s decision %q, bound = %v", step.host, decision, bound)
		}
		call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_save", "arguments": bound}})
		dispatches++
		result := mcpServer.HandleMessage(context.Background(), call)
		encoded, err := json.Marshal(result)
		if err != nil || strings.Contains(string(encoded), `"isError":true`) || !strings.Contains(string(encoded), step.title) {
			t.Fatalf("MCP result = %s, err = %v", encoded, err)
		}
	}
	if err := db.CreateSession(endedHost, project, root); err != nil {
		t.Fatal(err)
	}
	if err := db.EndSession(endedHost, "finished"); err != nil {
		t.Fatal(err)
	}
	start(endedHost)
	if registered, conflicted := registrations.Load(), conflicts.Load(); registered != 7 || conflicted != 1 {
		t.Fatalf("registrations = %d, production 409s = %d", registered, conflicted)
	}
	decision, bound := preToolUse(endedHost, "must not persist")
	if decision != "deny" || bound != nil || dispatches != 4 {
		t.Fatalf("ended host decision %q, bound %v, dispatches %d", decision, bound, dispatches)
	}
	all, err := db.AllObservations(project, "", 100)
	if err != nil || len(all) != 4 {
		t.Fatalf("observations = %d, err = %v", len(all), err)
	}
	for host, expected := range want {
		observations, err := db.SessionObservations(host, 100)
		if err != nil || len(observations) != 2 {
			t.Fatalf("host %s observations = %v, err = %v", host, observations, err)
		}
		counts := map[string]int{}
		for _, observation := range observations {
			counts[observation.Title]++
		}
		if len(counts) != len(expected) {
			t.Fatalf("host %s titles = %v", host, counts)
		}
		for title, count := range expected {
			if counts[title] != count {
				t.Fatalf("host %s title %q count = %d", host, title, counts[title])
			}
		}
	}
	ended, err := db.GetSession(endedHost)
	if err != nil || ended.EndedAt == nil {
		t.Fatalf("ended row = %+v, err = %v", ended, err)
	}
	endedWrites, err := db.SessionObservations(endedHost, 100)
	if err != nil || len(endedWrites) != 0 {
		t.Fatalf("ended writes = %v, err = %v", endedWrites, err)
	}
	if _, err := db.GetSession("foreign-model-session"); err == nil {
		t.Fatal("foreign model session was created")
	}
}

type statusCapture struct {
	http.ResponseWriter
	status int
}

func (w *statusCapture) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func TestCmdHookExitsWhenClaudeResponseWriteFails(t *testing.T) {
	oldStdin, oldOutput, oldExit := os.Stdin, claudeHookOutput, exitFunc
	t.Cleanup(func() { os.Stdin, claudeHookOutput, exitFunc = oldStdin, oldOutput, oldExit })
	claudeHookOutput = func([]byte) error { return errors.New("write Claude hook response") }
	var exitCodes []int
	exitFunc = func(code int) { exitCodes = append(exitCodes, code) }
	os.Stdin = claudeHookStdin(t, `{"session_id":"claude-session","tool_name":"mcp__engram__mem_save","tool_input":{"title":"decision"}}`, false)
	cmdHook([]string{"claude-pre-tool-use"})
	os.Stdin = claudeHookStdin(t, "", true)
	cmdHook([]string{"claude-pre-tool-use"})
	if len(exitCodes) != 2 || exitCodes[0] != 1 || exitCodes[1] != 1 {
		t.Fatalf("exit codes = %v, want [1 1] after Claude hook response write failures", exitCodes)
	}
}

func TestCmdHookEmitsJSONDenialWhenClaudeInputReadFails(t *testing.T) {
	oldStdin, oldOutput := os.Stdin, claudeHookOutput
	t.Cleanup(func() { os.Stdin, claudeHookOutput = oldStdin, oldOutput })
	os.Stdin = claudeHookStdin(t, "", true)
	var output []byte
	claudeHookOutput = func(response []byte) error { output = append([]byte(nil), response...); return nil }
	cmdHook([]string{"claude-pre-tool-use"})
	var response struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("command output = %q, want JSON denial: %v", output, err)
	}
	if response.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hook event = %q, want PreToolUse", response.HookSpecificOutput.HookEventName)
	}
	if response.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("permissionDecision = %q, want deny", response.HookSpecificOutput.PermissionDecision)
	}
	if response.HookSpecificOutput.PermissionDecisionReason != "cannot read authoritative Claude hook input" {
		t.Fatalf("permissionDecisionReason = %q", response.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestCodexPreToolUseCommandDeniesUnreadableInput(t *testing.T) {
	oldStdin, oldOutput, oldExit := os.Stdin, claudeHookOutput, exitFunc
	t.Cleanup(func() { os.Stdin, claudeHookOutput, exitFunc = oldStdin, oldOutput, oldExit })
	os.Stdin = claudeHookStdin(t, "", true)
	var output []byte
	claudeHookOutput = func(response []byte) error { output = append([]byte(nil), response...); return nil }
	exitFunc = func(code int) { t.Errorf("unexpected exit code %d", code) }
	cmdHook([]string{"codex-pre-tool-use"})
	var response struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("command output = %q, want JSON denial: %v", output, err)
	}
	if got := response.HookSpecificOutput; got.HookEventName != "PreToolUse" || got.PermissionDecision != "deny" || got.PermissionDecisionReason != "cannot read authoritative Codex hook input" {
		t.Fatalf("Codex read failure response = %+v", got)
	}
}

func TestCodexPreToolUseCommandExitsWhenResponseWriteFails(t *testing.T) {
	oldStdin, oldOutput, oldExit := os.Stdin, claudeHookOutput, exitFunc
	t.Cleanup(func() { os.Stdin, claudeHookOutput, exitFunc = oldStdin, oldOutput, oldExit })
	os.Stdin = claudeHookStdin(t, `{"session_id":"host","tool_name":"mcp__engram__mem_save","tool_input":{"title":"decision"}}`, false)
	claudeHookOutput = func([]byte) error { return errors.New("write Codex hook response") }
	var exitCodes []int
	exitFunc = func(code int) { exitCodes = append(exitCodes, code) }
	cmdHook([]string{"codex-pre-tool-use"})
	if len(exitCodes) != 1 || exitCodes[0] != 1 {
		t.Fatalf("exit codes = %v, want [1] after Codex response write failure", exitCodes)
	}
}

func TestCodexPreToolUseNamespacedSaveUsesHostID(t *testing.T) {
	for _, tc := range []struct {
		name, input, decision string
	}{
		{"model ID replaced", `{"session_id":"host","tool_name":"mcp__plugin_engram_engram__mem_save","tool_input":{"session_id":"model-picked","title":"retained"}}`, "allow"},
		{"missing host ID denied", `{"tool_name":"mcp__plugin_engram_engram__mem_save","tool_input":{"session_id":"model-picked"}}`, "deny"},
		{"malformed input denied", `{"session_id":"host","tool_name":"mcp__plugin_engram_engram__mem_save","tool_input":null}`, "deny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response struct {
				HookSpecificOutput struct {
					PermissionDecision string         `json:"permissionDecision"`
					UpdatedInput       map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(transformCodexPreToolUse([]byte(tc.input)), &response); err != nil {
				t.Fatal(err)
			}
			if got := response.HookSpecificOutput.PermissionDecision; got != tc.decision {
				t.Fatalf("decision = %q, want %q", got, tc.decision)
			}
			if tc.decision == "allow" && (response.HookSpecificOutput.UpdatedInput["session_id"] != "host" || response.HookSpecificOutput.UpdatedInput["title"] != "retained") {
				t.Fatalf("unexpected bound input: %#v", response.HookSpecificOutput.UpdatedInput)
			}
		})
	}
}

func TestCodexPreToolUseBindsWrites(t *testing.T) {
	for _, tool := range claudeEngramWriteAndSessionTools {
		t.Run(tool, func(t *testing.T) {
			field := "session_id"
			if tool == "mem_session_start" || tool == "mem_session_end" {
				field = "id"
			}
			input := []byte(`{"session_id":"host","tool_name":"mcp__engram__` + tool + `","tool_input":{"` + field + `":"model-picked","other":{"keep":true}}}`)
			var response struct {
				HookSpecificOutput struct {
					PermissionDecision string         `json:"permissionDecision"`
					UpdatedInput       map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(transformCodexPreToolUse(input), &response); err != nil {
				t.Fatal(err)
			}
			if response.HookSpecificOutput.PermissionDecision != "allow" || response.HookSpecificOutput.UpdatedInput[field] != "host" || response.HookSpecificOutput.UpdatedInput["other"].(map[string]any)["keep"] != true {
				t.Fatalf("unexpected Codex rewrite: %+v", response)
			}
		})
	}
}

func TestCodexPreToolUseRejectsMalformedInputAndLeavesReadsUntouched(t *testing.T) {
	for _, input := range []string{
		`not json`,
		`{"tool_name":"mcp__engram__mem_save","tool_input":{}}`,
		`{"session_id":"host","tool_name":"mcp__engram__mem_save","tool_input":null}`,
		`{"session_id":"host","tool_name":"mcp__engram__mem_save","tool_input":[]}`,
	} {
		t.Run(input, func(t *testing.T) {
			var response struct {
				HookSpecificOutput struct {
					PermissionDecision string `json:"permissionDecision"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(transformCodexPreToolUse([]byte(input)), &response); err != nil || response.HookSpecificOutput.PermissionDecision != "deny" {
				t.Fatalf("malformed input response: %+v, %v", response, err)
			}
		})
	}
	for _, tool := range []string{"mcp__engram__mem_search", "mcp__other__mem_save"} {
		input := `{"session_id":"host","tool_name":"` + tool + `","tool_input":{"session_id":"model"}}`
		if got := string(transformCodexPreToolUse([]byte(input))); got != "{}" {
			t.Errorf("non-write response for %s = %s", tool, got)
		}
	}
}

func TestCodexPreToolUseInterleavedHostSessionsRemainDistinct(t *testing.T) {
	for _, host := range []string{"host-one", "host-two", "host-one"} {
		input := []byte(`{"session_id":"` + host + `","tool_name":"mcp__engram__mem_save","tool_input":{"session_id":"model-picked","project":"same-project","directory":"same-directory"}}`)
		var response struct {
			HookSpecificOutput struct {
				UpdatedInput map[string]any `json:"updatedInput"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(transformCodexPreToolUse(input), &response); err != nil {
			t.Fatal(err)
		}
		if got := response.HookSpecificOutput.UpdatedInput; got["session_id"] != host || got["project"] != "same-project" || got["directory"] != "same-directory" {
			t.Fatalf("host %q updated input = %#v", host, got)
		}
	}
}

func TestCodexPreToolUseReadsDoNotRequireSessionOrInput(t *testing.T) {
	for _, input := range []string{
		`{"tool_name":"mcp__engram__mem_search"}`,
		`{"session_id":42,"tool_name":"mcp__engram__mem_context","tool_input":null}`,
		`{"tool_name":"mcp__other__mem_save","tool_input":[]}`,
	} {
		if got := string(transformCodexPreToolUse([]byte(input))); got != "{}" {
			t.Errorf("read/non-Engram call %s = %s, want {}", input, got)
		}
	}
}

func TestCodexPreToolUseCommandWritesAllowAndBoundInput(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			codexMockHealth(w)
			return
		}
		if r.URL.Path == "/project/current" {
			_, _ = w.Write([]byte(`{"project":"project-a","project_source":"config"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"host","status":"resolved"}`))
	}))
	defer endpoint.Close()
	t.Setenv("ENGRAM_URL", endpoint.URL)
	oldStdin, oldOutput := os.Stdin, claudeHookOutput
	t.Cleanup(func() { os.Stdin, claudeHookOutput = oldStdin, oldOutput })
	os.Stdin = claudeHookStdin(t, `{"session_id":"host","cwd":"/work","tool_name":"mcp__engram__mem_save","tool_input":{"session_id":"model","title":"retained"}}`, false)
	var output []byte
	claudeHookOutput = func(response []byte) error { output = append([]byte(nil), response...); return nil }
	cmdHook([]string{"codex-pre-tool-use"})
	var response struct {
		HookSpecificOutput struct {
			PermissionDecision string         `json:"permissionDecision"`
			UpdatedInput       map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil || response.HookSpecificOutput.PermissionDecision != "allow" || response.HookSpecificOutput.UpdatedInput["session_id"] != "host" || response.HookSpecificOutput.UpdatedInput["title"] != "retained" {
		t.Fatalf("command response = %s, %v", output, err)
	}
}

func TestTransformClaudePreToolUseBindsEngramWritesToAuthoritativeSession(t *testing.T) {
	input := []byte(`{
		"session_id":"claude-parent-session",
		"tool_name":"mcp__engram__mem_save",
		"tool_input":{"title":"decision","content":"keep this","session_id":"model-invented","project":"engram","nested":{"keep":true}}
	}`)

	output := transformClaudePreToolUse(input)
	var response struct {
		HookSpecificOutput struct {
			HookEventName      string         `json:"hookEventName"`
			UpdatedInput       map[string]any `json:"updatedInput"`
			PermissionDecision string         `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode hook response: %v\n%s", err, output)
	}
	if response.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hook event = %q, want PreToolUse", response.HookSpecificOutput.HookEventName)
	}
	if response.HookSpecificOutput.PermissionDecision != "" {
		t.Fatalf("successful rewrite must not auto-allow, got permissionDecision=%q", response.HookSpecificOutput.PermissionDecision)
	}
	updated := response.HookSpecificOutput.UpdatedInput
	if got := updated["session_id"]; got != "claude-parent-session" {
		t.Fatalf("session_id = %#v, want authoritative Claude session", got)
	}
	if got := updated["title"]; got != "decision" {
		t.Fatalf("title = %#v, want preserved", got)
	}
	if got := updated["project"]; got != "engram" {
		t.Fatalf("project = %#v, want preserved", got)
	}
	nested, ok := updated["nested"].(map[string]any)
	if !ok || nested["keep"] != true {
		t.Fatalf("nested arguments = %#v, want preserved", updated["nested"])
	}
}

func TestTransformClaudePreToolUseBindsEveryEngramWriteAndSessionTool(t *testing.T) {
	for _, tool := range claudeEngramWriteAndSessionTools {
		for _, server := range []string{"mcp__engram__", "mcp__plugin_engram_engram__"} {
			t.Run(server+tool, func(t *testing.T) {
				bindingField := "session_id"
				if tool == "mem_session_start" || tool == "mem_session_end" {
					bindingField = "id"
				}
				input := []byte(`{"session_id":"subagent-session","tool_name":"` + server + tool + `","tool_input":{"` + bindingField + `":"wrong","value":"preserved"}}`)
				output := transformClaudePreToolUse(input)
				var response struct {
					HookSpecificOutput struct {
						UpdatedInput map[string]any `json:"updatedInput"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal(output, &response); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if got := response.HookSpecificOutput.UpdatedInput[bindingField]; got != "subagent-session" {
					t.Fatalf("%s = %#v, want subagent authoritative session", bindingField, got)
				}
				if got := response.HookSpecificOutput.UpdatedInput["value"]; got != "preserved" {
					t.Fatalf("unrelated tool input = %#v, want preserved", got)
				}
			})
		}
	}
}

func TestTransformClaudePreToolUseLeavesNonEngramAndReadToolsUntouched(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`{"session_id":"claude-session","tool_name":"mcp__other__mem_save","tool_input":{"session_id":"model"}}`),
		[]byte(`{"session_id":"claude-session","tool_name":"mcp__engram__mem_search","tool_input":{"query":"history"}}`),
	} {
		if got := strings.TrimSpace(string(transformClaudePreToolUse(input))); got != "{}" {
			t.Fatalf("non-target tool response = %s, want {}", got)
		}
	}
}

func TestTransformClaudePreToolUseFailsClosedForMalformedAuthoritativeInput(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`not json`),
		[]byte(`{"tool_name":"mcp__engram__mem_save","tool_input":{}}`),
		[]byte(`{"session_id":" ","tool_name":"mcp__engram__mem_save","tool_input":{}}`),
		[]byte(`{"session_id":"claude-session","tool_name":"mcp__engram__mem_save","tool_input":null}`),
		[]byte(`{"session_id":"claude-session","tool_name":42,"tool_input":{}}`),
	} {
		output := transformClaudePreToolUse(input)
		var response struct {
			HookSpecificOutput struct {
				HookEventName      string `json:"hookEventName"`
				PermissionDecision string `json:"permissionDecision"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(output, &response); err != nil {
			t.Fatalf("decode denial: %v\n%s", err, output)
		}
		if response.HookSpecificOutput.HookEventName != "PreToolUse" || response.HookSpecificOutput.PermissionDecision != "deny" {
			t.Fatalf("malformed authoritative input response = %#v, want PreToolUse deny", response.HookSpecificOutput)
		}
	}
}
