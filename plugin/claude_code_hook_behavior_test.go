package plugin_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Layer 1 of the Claude Code hook test strategy: run the hooks and assert on
// what they actually emit.
//
// Layer 2 (claude_code_hook_enforcement_test.go) asserts on script source text.
// That is cheap and always runs, but every such assertion has an unbounded
// false-negative surface — four rounds of review on PR #654 each found a
// different substring that satisfied an assertion without satisfying the
// contract. Parsing real output has no such surface: a payload emitted at the
// wrong nesting level, or under the wrong key, fails on its own.

// hookPayload is Claude Code's UserPromptSubmit hook response shape. Only
// hookSpecificOutput.additionalContext reaches the model; a systemMessage
// renders to the terminal as "UserPromptSubmit says: ..." and is never
// delivered (issue #145), which is why it is decoded here and asserted empty.
type hookPayload struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
	SystemMessage string `json:"systemMessage"`
}

// requireHookBinaries skips when an interpreter the hooks need is missing.
// Skipping is acceptable only because Layer 2 still guards these contracts
// statically; without that backstop a skip would leave them unchecked.
func requireHookBinaries(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH - skipping hook behavior tests (claude_code_hook_enforcement_test.go still covers these contracts statically)", bin)
		}
	}
}

// user-prompt-submit.sh hardcodes /tmp for its session markers (line 188 uses
// /tmp, not TMPDIR), so tests clean up by absolute path rather than t.TempDir.
func stateFilePath(sessionID string) string {
	return filepath.Join("/tmp", "engram-claude-"+sessionID+"-tools-loaded")
}

func nudgeFilePath(sessionID string) string {
	return filepath.Join("/tmp", "engram-claude-"+sessionID+"-last-nudge")
}

// newSessionID derives a unique, deterministic id from the test name. It clears
// state left by an interrupted earlier run so the first-message path is
// reachable, and registers the same cleanup on exit.
func newSessionID(t *testing.T) string {
	t.Helper()
	id := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}, t.Name())

	clean := func() {
		os.Remove(stateFilePath(id))
		os.Remove(nudgeFilePath(id))
	}
	clean()
	t.Cleanup(clean)
	return id
}

// runHook executes a hook script under bash with the given stdin and returns
// its stdout. The hooks must always exit 0: a non-zero exit makes Claude Code
// block the user's message.
func runHook(t *testing.T, scriptName, stdin string, env map[string]string) string {
	t.Helper()
	script := filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", scriptName)

	cmd := exec.Command("bash", script)
	cmd.Stdin = strings.NewReader(stdin)
	// Force the POSIX path: the Windows-safe branch short-circuits before the
	// logic under test, and OSTYPE/MSYSTEM could otherwise leak in from the env.
	cmd.Env = append(os.Environ(), "ENGRAM_CLAUDE_WINDOWS_BASH_SAFE_MODE=0")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s must always exit 0, got %v\nstdout: %q\nstderr: %q", scriptName, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// serverPort extracts the port of a test server for ENGRAM_PORT. The hooks
// build their URL as http://127.0.0.1:${ENGRAM_PORT}, which is the interface
// httptest listens on.
func serverPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL %q: %v", srv.URL, err)
	}
	return u.Port()
}

// selectNames returns the exact tool names in a runtime additionalContext. The
// scan stops at the newline terminating the list, so no name can be satisfied
// by a longer name that merely contains it.
func selectNames(t *testing.T, additionalContext string) map[string]bool {
	t.Helper()
	idx := strings.Index(additionalContext, "select:")
	if idx < 0 {
		t.Fatalf("additionalContext carries no ToolSearch select: list: %q", additionalContext)
	}
	rest := additionalContext[idx+len("select:"):]
	if nl := strings.IndexAny(rest, "\r\n"); nl >= 0 {
		rest = rest[:nl]
	}
	set := make(map[string]bool)
	for _, name := range strings.Split(rest, ",") {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = true
		}
	}
	return set
}

// decodeHookPayload parses hook stdout and fails loudly on malformed JSON: the
// hook contract requires valid JSON on every path.
func decodeHookPayload(t *testing.T, stdout string) hookPayload {
	t.Helper()
	var payload hookPayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("hook stdout is not valid JSON: %v\nstdout: %q", err, stdout)
	}
	return payload
}

// deadServer stands in for the engram server on paths that must not depend on
// it. It answers 404 to everything, which is what the hooks' `curl -sf` treats
// as "no data".
func deadServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	return srv
}

// Defect 1: the first-message bootstrap must reach the model, which means
// additionalContext nested inside hookSpecificOutput — not systemMessage, and
// not additionalContext at the top level.
func TestBootstrapEmitsToolSearchPayload(t *testing.T) {
	requireHookBinaries(t)
	sessionID := newSessionID(t)
	srv := deadServer(t)

	stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, sessionID, t.TempDir())
	payload := decodeHookPayload(t, runHook(t, "user-prompt-submit.sh", stdin,
		map[string]string{"ENGRAM_PORT": serverPort(t, srv)}))

	if payload.SystemMessage != "" {
		t.Errorf("hook emitted systemMessage %q - it renders to the terminal and never reaches the model (issue #145)", payload.SystemMessage)
	}
	if got := payload.HookSpecificOutput.HookEventName; got != "UserPromptSubmit" {
		t.Errorf("hookSpecificOutput.hookEventName = %q, want %q", got, "UserPromptSubmit")
	}
	if payload.HookSpecificOutput.AdditionalContext == "" {
		t.Error("hookSpecificOutput.additionalContext is empty - the bootstrap delivers nothing")
	}
}

// Defect 2: one select: list must serve both install modes. ToolSearch resolves
// whichever names exist and ignores the rest, so listing both prefixes is safe.
func TestBootstrapCoversBothInstallPrefixes(t *testing.T) {
	requireHookBinaries(t)
	sessionID := newSessionID(t)
	srv := deadServer(t)

	stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, sessionID, t.TempDir())
	payload := decodeHookPayload(t, runHook(t, "user-prompt-submit.sh", stdin,
		map[string]string{"ENGRAM_PORT": serverPort(t, srv)}))

	listed := selectNames(t, payload.HookSpecificOutput.AdditionalContext)
	tools := []string{
		"mem_save", "mem_search", "mem_context", "mem_session_summary",
		"mem_session_start", "mem_session_end", "mem_get_observation",
		"mem_suggest_topic_key", "mem_capture_passive", "mem_save_prompt",
		"mem_update", "mem_current_project", "mem_judge",
	}
	for _, prefix := range []string{"mcp__engram__", "mcp__plugin_engram_engram__"} {
		for _, tool := range tools {
			if want := prefix + tool; !listed[want] {
				t.Errorf("emitted select: list is missing %q", want)
			}
		}
	}
}

// The marker file makes the bootstrap fire exactly once per session; a repeat
// injection on every message would flood the model's context.
func TestSecondMessageEmitsNoContext(t *testing.T) {
	requireHookBinaries(t)
	sessionID := newSessionID(t)
	srv := deadServer(t)
	env := map[string]string{"ENGRAM_PORT": serverPort(t, srv)}
	stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, sessionID, t.TempDir())

	runHook(t, "user-prompt-submit.sh", stdin, env)
	payload := decodeHookPayload(t, runHook(t, "user-prompt-submit.sh", stdin, env))

	if payload.HookSpecificOutput.AdditionalContext != "" {
		t.Errorf("bootstrap fired twice for one session: %q", payload.HookSpecificOutput.AdditionalContext)
	}
}
