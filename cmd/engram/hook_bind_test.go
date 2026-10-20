package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/hostbind"
)

func isolatedHostBindingDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := hostBindingDataDir
	hostBindingDataDir = func() string { return dir }
	t.Cleanup(func() { hostBindingDataDir = old })
	return dir
}

func TestClaudeBindSessionPublishesHostSession(t *testing.T) {
	dir := isolatedHostBindingDir(t)
	t.Setenv("PI_CODING_AGENT", "")
	t.Setenv("ENGRAM_HOST_PID", strconv.Itoa(os.Getpid()))
	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })

	for _, id := range []string{"claude-a", "claude-b"} { // /clear rebinds the same host
		os.Stdin = claudeHookStdin(t, `{"session_id":"`+id+`","cwd":"/work"}`, false)
		cmdHook([]string{"claude-bind-session"})
		if got, ok := hostbind.Lookup(dir, os.Getpid()); !ok || got != id {
			t.Fatalf("binding = %q, %v; want %q", got, ok, id)
		}
	}
}

func TestClaudeBindSessionNeverFailsOrBindsOnBadInput(t *testing.T) {
	dir := isolatedHostBindingDir(t)
	oldStdin, oldExit := os.Stdin, exitFunc
	t.Cleanup(func() { os.Stdin, exitFunc = oldStdin, oldExit })
	exitFunc = func(code int) { t.Fatalf("bind-session must never exit non-zero (code %d)", code) }

	cases := map[string]struct{ pid, stdin, pi string }{
		"missing host pid": {"", `{"session_id":"s"}`, ""},
		"invalid host pid": {"abc", `{"session_id":"s"}`, ""},
		"init pid":         {"1", `{"session_id":"s"}`, ""},
		"missing session":  {strconv.Itoa(os.Getpid()), `{"cwd":"/w"}`, ""},
		"malformed json":   {strconv.Itoa(os.Getpid()), `{nope`, ""},
		"under pi":         {strconv.Itoa(os.Getpid()), `{"session_id":"s"}`, "1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("ENGRAM_HOST_PID", tc.pid)
			t.Setenv("PI_CODING_AGENT", tc.pi)
			os.Stdin = claudeHookStdin(t, tc.stdin, false)
			cmdHook([]string{"claude-bind-session"})
			if got, ok := hostbind.Lookup(dir, os.Getpid()); ok {
				t.Fatalf("unexpected binding %q", got)
			}
		})
	}
}

func TestClaudePreToolUsePublishesBindingOnlyAfterConfirmation(t *testing.T) {
	dir := isolatedHostBindingDir(t)
	t.Setenv("PI_CODING_AGENT", "")
	const host = "claude-host"
	confirm := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/"+host:
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/project/current":
			_, _ = w.Write([]byte(`{"project":"project-a","project_source":"config"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/sessions":
			if !confirm {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + host + `","status":"created"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ENGRAM_URL", server.URL)
	request, _ := json.Marshal(map[string]any{"session_id": host, "cwd": t.TempDir(), "tool_name": "mcp__engram__mem_save", "tool_input": map[string]any{"title": "t", "content": "c", "project": "project-a"}})

	confirm = false
	if out := guardClaudePreToolUse(request); !strings.Contains(string(out), `"deny"`) {
		t.Fatalf("unconfirmed session must be denied, got %s", out)
	}
	if got, ok := hostbind.Lookup(dir, os.Getppid()); ok {
		t.Fatalf("denied call must not publish a binding, got %q", got)
	}

	confirm = true
	if out := guardClaudePreToolUse(request); strings.Contains(string(out), `"deny"`) {
		t.Fatalf("confirmed session must be allowed, got %s", out)
	}
	if got, ok := hostbind.Lookup(dir, os.Getppid()); !ok || got != host {
		t.Fatalf("binding = %q, %v; want %q for the host parent process", got, ok, host)
	}
}

// The hook and `engram mcp` must agree on the data directory, so the default
// resolver honors the same ENGRAM_DATA_DIR override main() gives the store.
func TestHostBindingDataDirHonorsEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ENGRAM_DATA_DIR", dir)
	// Restore the production resolver, which TestMain replaces.
	resolver := hostBindingDataDirFromEnvOrConfig
	if got := resolver(); got != dir {
		t.Fatalf("data dir = %q; want ENGRAM_DATA_DIR %q", got, dir)
	}
	t.Setenv("ENGRAM_DATA_DIR", "   ")
	if got := resolver(); got == "   " {
		t.Fatalf("blank ENGRAM_DATA_DIR must fall back to the default, got %q", got)
	}
}
