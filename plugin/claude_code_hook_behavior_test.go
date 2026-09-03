package plugin_test

import (
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
	"strings"
	"sync"
	"testing"
	"time"
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

// requireHookBinaries skips only when the host cannot run the Bash hooks.
// hooks.json and the PowerShell fallback still have interpreter-free backstops.
func requireHookBinaries(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH - skipping Bash hook behavior tests", bin)
		}
		if err := exec.Command(bin, "--version").Run(); err != nil {
			t.Skipf("%s is not runnable - skipping Bash hook behavior tests: %v", bin, err)
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

// newSessionID derives a unique, deterministic UUID from the test name. This
// matches the hook's unencoded session-key contract. It clears state left by an
// interrupted earlier run so the first-message path is reachable, and registers
// the same cleanup on exit.
func newSessionID(t *testing.T) string {
	t.Helper()
	hash := sha256.Sum256([]byte(t.Name()))
	id := hex.EncodeToString(hash[:16])
	id = id[:12] + "4" + id[13:16] + "8" + id[17:]
	id = id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]

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
//
// Unlike the PowerShell source parser in claude_code_hook_enforcement_test.go,
// this parses runtime-emitted additionalContext, where a single select: anchor
// has no comment-marker ambiguity.
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

var claudeCodeBootstrapTools = []string{
	"mem_save", "mem_search", "mem_context", "mem_session_summary",
	"mem_session_start", "mem_session_end", "mem_get_observation",
	"mem_suggest_topic_key", "mem_capture_passive", "mem_save_prompt",
	"mem_update", "mem_current_project", "mem_judge",
}

func assertToolSearchNames(t *testing.T, listed map[string]bool) {
	t.Helper()
	want := make(map[string]bool, len(claudeCodeBootstrapTools)*2)
	for _, prefix := range []string{"mcp__engram__", "mcp__plugin_engram_engram__"} {
		for _, tool := range claudeCodeBootstrapTools {
			want[prefix+tool] = true
		}
	}
	for name := range want {
		if !listed[name] {
			t.Errorf("emitted select: list is missing %q", name)
		}
	}
	for name := range listed {
		if !want[name] {
			t.Errorf("emitted select: list contains unexpected name %q", name)
		}
	}
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

// Defects 1 and 2: the first-message bootstrap must reach the model through
// additionalContext and load the exact ToolSearch names for both install modes.
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
	assertToolSearchNames(t, selectNames(t, payload.HookSpecificOutput.AdditionalContext))
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
	stdout := runHook(t, "user-prompt-submit.sh", stdin, env)
	if got := strings.TrimSpace(stdout); got != "{}" {
		t.Errorf("second message response = %q, want {}", got)
	}
	payload := decodeHookPayload(t, stdout)

	if payload.HookSpecificOutput.AdditionalContext != "" {
		t.Errorf("bootstrap fired twice for one session: %q", payload.HookSpecificOutput.AdditionalContext)
	}
}

// observationsServer impersonates the engram server for the nudge path. It
// answers /observations with a single observation saved lastSaveAge ago, and
// 404s /sessions/<id> so the hook skips its session-age gate (user-prompt-
// submit.sh:223 only applies that gate when a start time was returned).
func observationsServer(t *testing.T, lastSaveAge time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/project/current" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"project":"engram","project_source":"config"}`)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/observations") {
			http.NotFound(w, r)
			return
		}
		createdAt := time.Now().Add(-lastSaveAge).UTC().Format("2006-01-02T15:04:05Z")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"created_at":%q}]`, createdAt)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// markSessionBootstrapped creates the marker so the hook takes the subsequent-
// message path instead of the first-message bootstrap.
func markSessionBootstrapped(t *testing.T, sessionID string) {
	t.Helper()
	if err := os.WriteFile(stateFilePath(sessionID), nil, 0o600); err != nil {
		t.Fatalf("create session marker: %v", err)
	}
}

func TestNudgeBehavior(t *testing.T) {
	for _, tt := range []struct {
		name        string
		lastSaveAge time.Duration
		runs        int
		wantNudge   bool
	}{
		{"TestNudgeEmitsMemoryReminder", 20 * time.Minute, 1, true},
		{"TestNoNudgeWhenSaveIsRecent", time.Minute, 1, false},
		// Back-to-back runs are inside the 900-second default cooldown. This is
		// the regression proof for the nudge timestamp's trailing newline.
		{"TestNudgeIsDebouncedWithinCooldown", 20 * time.Minute, 2, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requireHookBinaries(t)
			sessionID := newSessionID(t)
			markSessionBootstrapped(t, sessionID)
			srv := observationsServer(t, tt.lastSaveAge)
			stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, sessionID, t.TempDir())
			env := map[string]string{"ENGRAM_PORT": serverPort(t, srv)}

			first := decodeHookPayload(t, runHook(t, "user-prompt-submit.sh", stdin, env))
			gotNudge := strings.Contains(first.HookSpecificOutput.AdditionalContext, "MEMORY REMINDER")
			if gotNudge != tt.wantNudge {
				t.Errorf("first nudge = %q, want nudge %t", first.HookSpecificOutput.AdditionalContext, tt.wantNudge)
			}
			if tt.wantNudge {
				if first.SystemMessage != "" {
					t.Errorf("nudge emitted systemMessage %q - it never reaches the model (issue #145)", first.SystemMessage)
				}
				if got := first.HookSpecificOutput.HookEventName; got != "UserPromptSubmit" {
					t.Errorf("nudge hookSpecificOutput.hookEventName = %q, want %q", got, "UserPromptSubmit")
				}
			}
			if tt.runs == 2 {
				second := decodeHookPayload(t, runHook(t, "user-prompt-submit.sh", stdin, env))
				if second.HookSpecificOutput.AdditionalContext != "" {
					t.Errorf("nudge repeated inside the cooldown window: %q", second.HookSpecificOutput.AdditionalContext)
				}
			}
		})
	}
}

// passiveCapture is the body subagent-stop.sh POSTs to /observations/passive.
type passiveCapture struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Project   string `json:"project"`
	Source    string `json:"source"`
}

// captureServer records every passive-capture POST. subagent-stop.sh issues its
// curl synchronously, so by the time the process exits the request has landed
// and the returned slice is complete.
func captureServer(t *testing.T) (*httptest.Server, func() []passiveCapture) {
	t.Helper()
	var mu sync.Mutex
	var got []passiveCapture

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/project/current" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"project":"engram","project_source":"config"}`)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read passive capture body: %v", err)
			return
		}
		var capture passiveCapture
		if err := json.Unmarshal(body, &capture); err != nil {
			t.Errorf("passive capture body is not valid JSON: %v (body: %q)", err, body)
			return
		}
		mu.Lock()
		got = append(got, capture)
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)

	return srv, func() []passiveCapture {
		mu.Lock()
		defer mu.Unlock()
		return append([]passiveCapture(nil), got...)
	}
}

func TestSubagentStopPayloadHandling(t *testing.T) {
	const tricky = "line one\nline \"two\" $HOME `id` 'quoted' \\backslash"
	for _, tt := range []struct {
		name, sessionID, lastAssistantMessage, stdout, want string
	}{
		{"TestSubagentStopPrefersLastAssistantMessage", "sess-primary", "from last_assistant_message", "from stdout", "from last_assistant_message"},
		{"TestSubagentStopFallsBackToStdout", "sess-fallback", "", "from stdout", "from stdout"},
		{"TestSubagentStopSkipsEmptyPayload", "sess-empty", "", "", ""},
		{"TestSubagentStopPreservesShellMetacharacters", "sess-quoting", tricky, "", tricky},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requireHookBinaries(t)
			srv, captured := captureServer(t)
			input := map[string]string{"session_id": tt.sessionID, "cwd": t.TempDir()}
			if tt.lastAssistantMessage != "" {
				input["last_assistant_message"] = tt.lastAssistantMessage
			}
			if tt.stdout != "" {
				input["stdout"] = tt.stdout
			}
			payload, err := json.Marshal(input)
			if err != nil {
				t.Fatalf("marshal fixture: %v", err)
			}
			runHook(t, "subagent-stop.sh", string(payload), map[string]string{"ENGRAM_PORT": serverPort(t, srv)})

			got := captured()
			if tt.want == "" {
				if len(got) != 0 {
					t.Errorf("posted %d captures for an empty payload, want 0: %+v", len(got), got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %d passive captures, want 1: %+v", len(got), got)
			}
			if got[0].Content != tt.want {
				t.Errorf("content = %q, want %q", got[0].Content, tt.want)
			}
			if got[0].SessionID != tt.sessionID || got[0].Source != "subagent-stop" {
				t.Errorf("passive capture = %+v, want session_id %q and source subagent-stop", got[0], tt.sessionID)
			}
		})
	}
}
