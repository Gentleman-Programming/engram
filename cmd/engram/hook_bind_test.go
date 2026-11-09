package main

import (
	"encoding/json"
	"io"
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

// withHostProcessPID makes the bind command believe pid launched it.
func withHostProcessPID(t *testing.T, pid int) {
	t.Helper()
	old := hostProcessPID
	hostProcessPID = func() int { return pid }
	t.Cleanup(func() { hostProcessPID = old })
}

func TestClaudeBindSessionPublishesHostSession(t *testing.T) {
	dir := isolatedHostBindingDir(t)
	t.Setenv("PI_CODING_AGENT", "")
	// The binding must not depend on any env var naming the host.
	t.Setenv("ENGRAM_HOST_PID", "1")
	withHostProcessPID(t, os.Getpid())
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

// Without the seam the command binds its real parent process, which is the
// Claude process because the hook is registered in exec form.
func TestClaudeBindSessionUsesRealParentProcess(t *testing.T) {
	dir := isolatedHostBindingDir(t)
	t.Setenv("PI_CODING_AGENT", "")
	t.Setenv("ENGRAM_HOST_PID", strconv.Itoa(os.Getpid()))
	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })

	os.Stdin = claudeHookStdin(t, `{"session_id":"claude-parent"}`, false)
	cmdHook([]string{"claude-bind-session"})
	if got, ok := hostbind.Lookup(dir, os.Getppid()); !ok || got != "claude-parent" {
		t.Fatalf("binding for parent = %q, %v; want claude-parent", got, ok)
	}
	if got, ok := hostbind.Lookup(dir, os.Getpid()); ok {
		t.Fatalf("ENGRAM_HOST_PID must be ignored, got binding %q for it", got)
	}
}

func TestClaudeBindSessionPrintsNothing(t *testing.T) {
	isolatedHostBindingDir(t)
	t.Setenv("PI_CODING_AGENT", "")
	withHostProcessPID(t, os.Getpid())
	oldStdin, oldStdout := os.Stdin, os.Stdout
	t.Cleanup(func() { os.Stdin, os.Stdout = oldStdin, oldStdout })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	os.Stdin = claudeHookStdin(t, `{"session_id":"quiet"}`, false)
	cmdHook([]string{"claude-bind-session"})
	_ = w.Close()
	os.Stdout = oldStdout
	out, _ := io.ReadAll(r)
	if len(out) != 0 {
		t.Fatalf("bind-session must print nothing (SessionStart stdout becomes context), got %q", out)
	}
}

func TestClaudeBindSessionNeverFailsOrBindsOnBadInput(t *testing.T) {
	dir := isolatedHostBindingDir(t)
	oldStdin, oldExit := os.Stdin, exitFunc
	t.Cleanup(func() { os.Stdin, exitFunc = oldStdin, oldExit })
	exitFunc = func(code int) { t.Fatalf("bind-session must never exit non-zero (code %d)", code) }

	cases := map[string]struct {
		pid       int
		stdin, pi string
	}{
		"init pid":        {1, `{"session_id":"s"}`, ""},
		"zero pid":        {0, `{"session_id":"s"}`, ""},
		"missing session": {os.Getpid(), `{"cwd":"/w"}`, ""},
		"malformed json":  {os.Getpid(), `{nope`, ""},
		"under pi":        {os.Getpid(), `{"session_id":"s"}`, "1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PI_CODING_AGENT", tc.pi)
			withHostProcessPID(t, tc.pid)
			os.Stdin = claudeHookStdin(t, tc.stdin, false)
			cmdHook([]string{"claude-bind-session"})
			if got, ok := hostbind.Lookup(dir, os.Getpid()); ok {
				t.Fatalf("unexpected binding %q", got)
			}
		})
	}
}

// The PreToolUse guard runs in shell form, so its parent is not reliably the
// Claude process (dash on Linux, Git Bash on Windows): it must never publish.
func TestClaudePreToolUseDoesNotPublishBinding(t *testing.T) {
	dir := isolatedHostBindingDir(t)
	t.Setenv("PI_CODING_AGENT", "")
	const host = "claude-host"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sessions/"+host:
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/project/current":
			_, _ = w.Write([]byte(`{"project":"project-a","project_source":"config"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + host + `","status":"created"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ENGRAM_URL", server.URL)
	request, _ := json.Marshal(map[string]any{"session_id": host, "cwd": t.TempDir(), "tool_name": "mcp__engram__mem_save", "tool_input": map[string]any{"title": "t", "content": "c", "project": "project-a"}})

	if out := guardClaudePreToolUse(request); strings.Contains(string(out), `"deny"`) {
		t.Fatalf("confirmed session must be allowed, got %s", out)
	}
	for _, pid := range []int{os.Getppid(), os.Getpid()} {
		if got, ok := hostbind.Lookup(dir, pid); ok {
			t.Fatalf("PreToolUse must not publish a binding, got %q for pid %d", got, pid)
		}
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
