package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/mcp"
	"github.com/Gentleman-Programming/engram/v2/internal/server"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
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
	if err := db.CreateSession("host", "project-a", root); err != nil {
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
	registrations := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/project/current" {
			if r.URL.Query().Get("cwd") != filepath.ToSlash(root) {
				t.Errorf("cwd = %q", r.URL.Query().Get("cwd"))
			}
			_, _ = w.Write([]byte(`{"project":"project-b","project_source":"config"}`))
			return
		}
		if r.URL.Path != "/sessions" || r.Method != http.MethodPost {
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
		if capture.Code != http.StatusCreated || confirmation.ID != "host" || confirmation.Status != "created" {
			t.Errorf("registration response: %d %s", capture.Code, capture.Body.String())
		}
		registrations++
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
	if registrations != 1 || result.HookSpecificOutput.PermissionDecision != "allow" || result.HookSpecificOutput.UpdatedInput["session_id"] != "host" || result.HookSpecificOutput.UpdatedInput["project"] != "project-b" {
		t.Fatalf("registrations=%d result=%+v", registrations, result)
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
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/project/current" {
					_, _ = w.Write([]byte(`{"project":"project-a","project_source":"config"}`))
					return
				}
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
		if r.URL.Path == "/project/current" {
			_, _ = w.Write([]byte(`{"project":"project-a","project_source":"config"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"host","status":"created"}`))
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
