package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/mcp"
	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Run the public hook command in a subprocess, including its stdout/exit contract.
func TestRootScopedResumeHookProcess(t *testing.T) {
	action := os.Getenv("ENGRAM_RESUME_TEST_ACTION")
	if action == "" {
		return
	}
	cmdHook([]string{action})
	os.Exit(0)
}

func TestRootScopedResumeLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("runs public hook subprocesses and MCP lifecycle")
	}
	for _, tc := range []struct {
		name, agent, mode, project string
		changedDirectory           bool
	}{
		{"Claude changed cwd", "claude", "project_owned", "owner", true},
		{"Claude same cwd", "claude", "project_owned", "owner", false},
		{"Claude shared cross-project", "claude", "shared", "other", true},
		{"Codex changed cwd", "codex", "shared", "owner", true},
		{"Codex same cwd", "codex", "shared", "owner", false},
		{"Codex project-owned changed cwd", "codex", "project_owned", "owner", true},
		{"Codex shared cross-project", "codex", "shared", "other", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("GIT_CEILING_DIRECTORIES", root)
			t.Setenv("PI_CODING_AGENT", "")
			a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
			for _, d := range []string{a, b} {
				if err := os.Mkdir(d, 0700); err != nil {
					t.Fatal(err)
				}
			}
			cwd := a
			if tc.changedDirectory {
				cwd = b
			}
			db, err := store.New(store.FallbackConfig(filepath.Join(root, "data")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			production := server.New(db, 0).Handler()
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/project/current" {
					_ = json.NewEncoder(w).Encode(map[string]string{"project": tc.project, "project_source": "config"})
					return
				}
				production.ServeHTTP(w, r)
			}))
			defer endpoint.Close()
			call := func(method, path string, body any, want int) map[string]any {
				t.Helper()
				var data io.Reader
				if body != nil {
					raw, _ := json.Marshal(body)
					data = bytes.NewReader(raw)
				}
				request, _ := http.NewRequest(method, endpoint.URL+path, data)
				request.Header.Set("Content-Type", "application/json")
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = response.Body.Close() }()
				raw, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != want {
					t.Fatalf("%s %s = %d %s, want %d", method, path, response.StatusCode, raw, want)
				}
				var value map[string]any
				if err := json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			scope := func(id, project, directory, mode string) map[string]any {
				return map[string]any{"id": id, "project": project, "directory": directory, "ownership_mode": mode}
			}
			call("POST", "/sessions", scope("host", "owner", a, tc.mode), 201)
			call("POST", "/sessions/host/end", map[string]any{}, 200)
			original := call("GET", "/sessions/host", nil, 200)
			cli := func(action string, payload any) []byte {
				t.Helper()
				raw, _ := json.Marshal(payload)
				command := exec.Command(os.Args[0], "-test.run=^TestRootScopedResumeHookProcess$")
				command.Env = append(os.Environ(), "ENGRAM_RESUME_TEST_ACTION="+action, "ENGRAM_URL="+endpoint.URL, "ENGRAM_SOCKET=", "ENGRAM_PROJECT=", "HOME="+root, "ENGRAM_DATA_DIR="+filepath.Join(root, "child-data"), "ENGRAM_HOOK_OUTPUT=json")
				command.Stdin = bytes.NewReader(raw)
				var stdout, stderr bytes.Buffer
				command.Stdout = &stdout
				command.Stderr = &stderr
				if err := command.Run(); err != nil {
					t.Fatalf("%s exit=%v stdout=%q stderr=%q", action, err, stdout.String(), stderr.String())
				}
				if stderr.Len() != 0 {
					t.Fatalf("%s stderr=%q", action, stderr.String())
				}
				return stdout.Bytes()
			}
			if tc.agent == "codex" {
				var ack map[string]string
				if err := json.Unmarshal(cli("codex-register", map[string]string{"session_id": "host", "cwd": cwd}), &ack); err != nil || ack["session_id"] != "host:resume:2" {
					t.Fatalf("registration=%v err=%v", ack, err)
				}
			} else {
				registration := scope("host", tc.project, cwd, tc.mode)
				registration["resume"] = true
				if got := call("POST", "/sessions", registration, 201)["id"]; got != "host:resume:2" {
					t.Fatalf("registration=%v", got)
				}
			}
			continuation := call("GET", "/sessions/host:resume:2", nil, 200)
			for _, field := range []string{"project", "directory", "ownership_mode"} {
				if continuation[field] != original[field] {
					t.Fatalf("continuation %s=%v, root=%v", field, continuation[field], original[field])
				}
			}
			input := map[string]any{"session_id": "host", "cwd": cwd, "tool_name": "mcp__engram__mem_save", "tool_input": map[string]any{"session_id": "forged", "project": tc.project, "title": "resumed", "content": "scope proof"}}
			var envelope struct {
				Output struct {
					Decision string         `json:"permissionDecision"`
					Input    map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			output := cli(tc.agent+"-pre-tool-use", input)
			if err := json.Unmarshal(output, &envelope); err != nil || envelope.Output.Decision == "deny" || envelope.Output.Input["session_id"] != "host:resume:2" {
				t.Fatalf("gate=%s err=%v", output, err)
			}
			request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_save", "arguments": envelope.Output.Input}})
			result, _ := json.Marshal(mcp.NewServerWithConfig(db, mcp.MCPConfig{DefaultProject: tc.project}, nil).HandleMessage(context.Background(), request))
			if bytes.Contains(result, []byte(`"isError":true`)) {
				t.Fatalf("MCP save=%s", result)
			}
			observations, err := db.SessionObservations("host:resume:2", 10)
			if err != nil || len(observations) != 1 || observations[0].Project == nil || *observations[0].Project != tc.project {
				t.Fatalf("observations=%+v err=%v", observations, err)
			}
			if rootObservations, err := db.SessionObservations("host", 10); err != nil || len(rootObservations) != 0 {
				t.Fatalf("root observations=%v err=%v", rootObservations, err)
			}
			if tc.agent == "codex" {
				cli("codex-session-end", map[string]string{"session_id": "host", "cwd": cwd})
			} else {
				call("POST", "/runtime-sessions/end", scope("host", "owner", a, tc.mode), 200)
			}
			if ended := call("GET", "/sessions/host:resume:2", nil, 200)["ended_at"]; ended == nil {
				t.Fatal("continuation remains open")
			}
			if after := call("GET", "/sessions/host", nil, 200); !reflect.DeepEqual(after, original) {
				t.Fatalf("root changed: before=%v after=%v", original, after)
			}
			call("GET", "/sessions/forged", nil, 404)
		})
	}
}
