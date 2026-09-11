package plugin_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

// requireHookBinaries skips only when the host cannot run the Bash hooks from this workspace.
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
	_ = bashScriptPath(t, filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "_helpers.sh"))
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
	stdout, _ := runHookWithStderr(t, scriptName, stdin, env)
	return stdout
}

// runHookInDir is runHook with an explicit working directory for the hook
// process, used to exercise CLAUDE_CONFIG_DIR resolution against a relative
// path (issue #1081). An empty dir behaves exactly like runHook.
func runHookInDir(t *testing.T, scriptName, stdin string, env map[string]string, dir string) string {
	t.Helper()
	stdout, _ := runHookWithStderrInDir(t, scriptName, stdin, env, dir)
	return stdout
}

func runHookWithStderr(t *testing.T, scriptName, stdin string, env map[string]string) (string, string) {
	t.Helper()
	return runHookWithStderrInDir(t, scriptName, stdin, env, "")
}

// runHookWithStderrInDir is runHookWithStderr with an explicit working
// directory for the hook process; an empty dir uses the test process's own
// working directory (the exec.Cmd default).
func runHookWithStderrInDir(t *testing.T, scriptName, stdin string, env map[string]string, dir string) (string, string) {
	t.Helper()
	script := filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", scriptName)

	cmd := exec.Command("bash", bashScriptPath(t, script))
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	// Force the POSIX path: the Windows-safe branch short-circuits before the
	// logic under test, and OSTYPE/MSYSTEM could otherwise leak in from the env.
	cmd.Env = make([]string, 0, len(os.Environ())+len(env)+1)
	for _, entry := range os.Environ() {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "ENGRAM_URL=") || strings.HasPrefix(upper, "ENGRAM_PORT=") || strings.HasPrefix(upper, "ENGRAM_SOCKET=") {
			continue
		}
		// Skip any other entry the caller's env map overrides, so the
		// override is never shadowed by an earlier duplicate key in envp.
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		// Keep hook tests hermetic: CLAUDE_CONFIG_DIR is unset unless the
		// caller explicitly supplies it in env.
		if strings.EqualFold(key, "CLAUDE_CONFIG_DIR") {
			continue
		}
		if _, override := env[key]; override {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "ENGRAM_CLAUDE_WINDOWS_BASH_SAFE_MODE=0")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s must always exit 0, got %v\nstdout: %q\nstderr: %q", scriptName, err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func resolveClaudeHookURL(t *testing.T, env map[string]string) string {
	t.Helper()
	helper := filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "_helpers.sh")
	cmd := exec.Command("bash", "-c", `source "$1"; printf '%s' "$ENGRAM_URL"`, "bash", bashScriptPath(t, helper))
	cmd.Env = make([]string, 0, len(os.Environ())+len(env))
	for _, entry := range os.Environ() {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "ENGRAM_URL=") || strings.HasPrefix(upper, "ENGRAM_PORT=") || strings.HasPrefix(upper, "ENGRAM_SOCKET=") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve Claude hook URL: %v", err)
	}
	return string(output)
}

func bashScriptPath(t *testing.T, path string) string {
	t.Helper()
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return path
	}
	remainder := strings.ReplaceAll(strings.TrimLeft(path[len(volume):], `\\/`), `\`, "/")
	for _, candidate := range []string{
		"/" + strings.ToLower(volume[:1]) + "/" + remainder,
		"/mnt/" + strings.ToLower(volume[:1]) + "/" + remainder,
	} {
		if exec.Command("bash", "-c", `[ -f "$1" ]`, "bash", candidate).Run() == nil {
			return candidate
		}
	}
	t.Skipf("bash cannot access hook script %q", path)
	return ""
}

func normalizedClaudeHookMaxTime(t *testing.T, configured, callerDefault string) string {
	t.Helper()
	helper := filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "_helpers.sh")
	cmd := exec.Command("bash", "-c", `source "$1" "__engram_hook_default_max_time=$2"; printf '%s' "$ENGRAM_HOOK_MAX_TIME"`, "bash", bashScriptPath(t, helper), callerDefault)
	cmd.Env = make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "ENGRAM_HOOK_MAX_TIME=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	if configured != "" {
		cmd.Env = append(cmd.Env, "ENGRAM_HOOK_MAX_TIME="+configured)
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("normalize Claude hook max time: %v", err)
	}
	return string(output)
}

func resolveClaudeConfigRoot(t *testing.T, configured string) string {
	t.Helper()
	helper := filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "_helpers.sh")
	cmd := exec.Command("bash", "-c", `source "$1"; claude_config_root`, "bash", bashScriptPath(t, helper))
	cmd.Dir = t.TempDir()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "CLAUDE_CONFIG_DIR") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+configured)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve Claude config root: %v", err)
	}
	return string(output)
}

func capturedUserPromptMaxTimes(t *testing.T, configured string) []string {
	t.Helper()
	requireHookBinaries(t)
	argsPath := filepath.Join(t.TempDir(), "curl-args")
	bashEnv := filepath.Join(t.TempDir(), "fake-curl.sh")
	const fakeCurlScript = `curl() {
  printf '%s\n' "$@" >> "$ENGRAM_TEST_CURL_ARGS"
  for arg in "$@"; do
    case "$arg" in
      */project/current*) printf '%s' '{"project":"engram","project_source":"config"}' ;;
      */sessions/*) printf '%s' '{}' ;;
      */observations*) printf '%s' '[]' ;;
    esac
  done
}
`
	if err := os.WriteFile(bashEnv, []byte(fakeCurlScript), 0o600); err != nil {
		t.Fatalf("write fake curl environment: %v", err)
	}

	sessionID := newSessionID(t)
	stateDir := t.TempDir()
	env := map[string]string{
		"ENGRAM_HOOK_MAX_TIME":  configured,
		"ENGRAM_TEST_CURL_ARGS": argsPath,
		"BASH_ENV":              bashEnv,
		"TMPDIR":                stateDir,
	}
	stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, sessionID, t.TempDir())
	runHook(t, "user-prompt-submit.sh", stdin, env)
	runHook(t, "user-prompt-submit.sh", stdin, env)

	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read fake curl arguments: %v", err)
	}
	var maxTimes []string
	args := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--max-time" {
			maxTimes = append(maxTimes, args[i+1])
		}
	}
	return maxTimes
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

func TestNudgeFailsClosedForIncompleteObservations(t *testing.T) {
	requireHookBinaries(t)
	for _, tt := range []struct {
		name         string
		observations string
	}{
		{name: "empty object", observations: `[{}]`},
		{name: "null timestamp", observations: `[{"created_at":null}]`},
		{name: "non-string timestamp", observations: `[{"created_at":42}]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := newSessionID(t)
			markSessionBootstrapped(t, sessionID)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/project/current":
					_, _ = io.WriteString(w, `{"project":"engram","project_source":"config"}`)
				case "/sessions/" + sessionID:
					_, _ = fmt.Fprintf(w, `{"started_at":%q}`, time.Now().Add(-20*time.Minute).UTC().Format(time.RFC3339))
				case "/observations":
					_, _ = io.WriteString(w, tt.observations)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, sessionID, t.TempDir())
			payload := decodeHookPayload(t, runHook(t, "user-prompt-submit.sh", stdin,
				map[string]string{"ENGRAM_PORT": serverPort(t, srv)}))
			if strings.Contains(payload.HookSpecificOutput.AdditionalContext, "MEMORY REMINDER") {
				t.Fatalf("nudge emitted for incomplete observations: %q", payload.HookSpecificOutput.AdditionalContext)
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

func TestSubagentStopUsesUnixSocketTransport(t *testing.T) {
	requireHookBinaries(t)
	requireUnixSocketHooks(t)

	socketPath := filepath.Join(t.TempDir(), "engram.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}
	var mu sync.Mutex
	var captures []passiveCapture
	var host string
	httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/project/current":
			_, _ = io.WriteString(w, `{"project":"engram","project_source":"config"}`)
		case "/observations/passive":
			host = r.Host
			var capture passiveCapture
			if err := json.NewDecoder(r.Body).Decode(&capture); err != nil {
				t.Errorf("decode passive capture: %v", err)
				return
			}
			captures = append(captures, capture)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() {
		_ = httpServer.Close()
		_ = listener.Close()
		_ = os.Remove(socketPath)
	})

	input := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"last_assistant_message":"captured over UDS"}`, "uds-session", t.TempDir())
	runHook(t, "subagent-stop.sh", input, map[string]string{"ENGRAM_SOCKET": "  " + socketPath + "  ", "ENGRAM_PORT": " 7437 "})

	mu.Lock()
	defer mu.Unlock()
	if host != "localhost" {
		t.Fatalf("Unix socket request host = %q, want localhost", host)
	}
	if len(captures) != 1 || captures[0].Content != "captured over UDS" {
		t.Fatalf("captures = %+v, want one UDS passive capture", captures)
	}
}

func TestUserPromptSocketFailureWarnsAndReturnsValidJSON(t *testing.T) {
	requireHookBinaries(t)
	requireUnixSocketHooks(t)
	sessionID := newSessionID(t)
	markSessionBootstrapped(t, sessionID)
	socketPath := filepath.Join(t.TempDir(), "unreachable.sock")
	stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, sessionID, t.TempDir())

	stdout, stderr := runHookWithStderr(t, "user-prompt-submit.sh", stdin, map[string]string{"ENGRAM_SOCKET": socketPath})
	if got := strings.TrimSpace(stdout); got != "{}" {
		t.Fatalf("socket failure stdout = %q, want valid empty JSON", got)
	}
	if !strings.Contains(stderr, "warning: Engram") {
		t.Fatalf("socket failure stderr = %q, want actionable Engram warning", stderr)
	}
}

func TestClaudeHookWhitespacePortFallsBackToDefault(t *testing.T) {
	requireHookBinaries(t)
	for _, tt := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "whitespace port uses default", env: map[string]string{"ENGRAM_PORT": " \t "}, want: "http://127.0.0.1:7437"},
		{name: "nonnumeric port uses default", env: map[string]string{"ENGRAM_PORT": "invalid"}, want: "http://127.0.0.1:7437"},
		{name: "zero port uses default", env: map[string]string{"ENGRAM_PORT": "0"}, want: "http://127.0.0.1:7437"},
		{name: "all-zero port uses default", env: map[string]string{"ENGRAM_PORT": "0000"}, want: "http://127.0.0.1:7437"},
		{name: "leading-zero port is valid", env: map[string]string{"ENGRAM_PORT": "00080"}, want: "http://127.0.0.1:80"},
		{name: "negative port uses default", env: map[string]string{"ENGRAM_PORT": "-1"}, want: "http://127.0.0.1:7437"},
		{name: "plus-signed port uses default", env: map[string]string{"ENGRAM_PORT": "+7437"}, want: "http://127.0.0.1:7437"},
		{name: "maximum port is valid", env: map[string]string{"ENGRAM_PORT": "65535"}, want: "http://127.0.0.1:65535"},
		{name: "out-of-range port uses default", env: map[string]string{"ENGRAM_PORT": "65536"}, want: "http://127.0.0.1:7437"},
		{name: "very long port uses default", env: map[string]string{"ENGRAM_PORT": strings.Repeat("9", 1_000)}, want: "http://127.0.0.1:7437"},
		{name: "non-empty port is trimmed", env: map[string]string{"ENGRAM_PORT": " 8123 "}, want: "http://127.0.0.1:8123"},
		{name: "socket takes precedence", env: map[string]string{"ENGRAM_PORT": " \t ", "ENGRAM_SOCKET": " /tmp/engram.sock "}, want: "http://localhost"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveClaudeHookURL(t, tt.env); got != tt.want {
				t.Fatalf("ENGRAM_URL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClaudeHookEnvironmentFiltersInheritedEngramURL(t *testing.T) {
	t.Setenv("ENGRAM_URL", "http://127.0.0.1:1")
	if got, want := resolveClaudeHookURL(t, map[string]string{"ENGRAM_PORT": "8123"}), "http://127.0.0.1:8123"; got != want {
		t.Fatalf("inherited ENGRAM_URL leaked into helper: got %q, want %q", got, want)
	}
	if got, want := resolveClaudeHookURL(t, map[string]string{"ENGRAM_URL": "http://127.0.0.1:9999"}), "http://127.0.0.1:9999"; got != want {
		t.Fatalf("explicit ENGRAM_URL = %q, want %q", got, want)
	}
}

func TestCodexSessionStartWhitespaceURLUsesManagedLocalMode(t *testing.T) {
	requireHookBinaries(t)
	srv := healthyServer(t)
	stubDir := writeRecordingEngramStub(t)
	logPath := filepath.Join(t.TempDir(), "engram-invocations.log")
	script := bashScriptPath(t, filepath.Join(repoRoot(t), "plugin", "codex", "scripts", "session-start.sh"))
	cmd := exec.Command("bash", script)
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, "codex-session", t.TempDir()))
	for _, entry := range os.Environ() {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "ENGRAM_URL=") || strings.HasPrefix(upper, "ENGRAM_PORT=") || strings.HasPrefix(upper, "ENGRAM_SOCKET=") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env,
		"ENGRAM_URL= \t ",
		"ENGRAM_PORT="+serverPort(t, srv),
		"PATH="+stubDir+":"+os.Getenv("PATH"),
		"ENGRAM_TEST_ENGRAM_LOG="+logPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Codex session-start must exit successfully: %v\n%s", err, output)
	}

	if got := readEngramInvocations(t, logPath); len(got) != 1 || got[0] != "instance-id" {
		t.Fatalf("whitespace ENGRAM_URL selected external mode; invocations = %v, want [instance-id]", got)
	}
}

func TestClaudeHookMaxTimeNormalization(t *testing.T) {
	for _, tt := range []struct {
		name, configured, callerDefault, want string
	}{
		{name: "other hooks default to three seconds", callerDefault: "3", want: "3"},
		{name: "user prompt default is fractional", callerDefault: "0.2", want: "0.2"},
		{name: "valid fractional override is preserved", configured: "1.5", callerDefault: "0.2", want: "1.5"},
		{name: "largest curl-safe decimal is preserved", configured: "2147483.647", callerDefault: "3", want: "2147483.647"},
		{name: "zero falls back to user prompt default", configured: "0", callerDefault: "0.2", want: "0.2"},
		{name: "zero decimal falls back to user prompt default", configured: "0.0", callerDefault: "0.2", want: "0.2"},
		{name: "signed value falls back to other hook default", configured: "+1", callerDefault: "3", want: "3"},
		{name: "negative value falls back to other hook default", configured: "-1", callerDefault: "3", want: "3"},
		{name: "invalid value falls back to other hook default", configured: "not-a-number", callerDefault: "3", want: "3"},
		{name: "NaN falls back to other hook default", configured: "NaN", callerDefault: "3", want: "3"},
		{name: "infinity falls back to other hook default", configured: "inf", callerDefault: "3", want: "3"},
		{name: "curl overflow falls back to other hook default", configured: "2147484", callerDefault: "3", want: "3"},
		{name: "invalid caller default fails safely", callerDefault: "0", want: "3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedClaudeHookMaxTime(t, tt.configured, tt.callerDefault); got != tt.want {
				t.Fatalf("ENGRAM_HOOK_MAX_TIME = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClaudeConfigRootRecognizesWindowsAbsolutePaths(t *testing.T) {
	requireHookBinaries(t)
	for _, tt := range []struct {
		name, configured, want string
	}{
		{name: "drive with forward slashes", configured: " \tC:/Users/test/.claude\t ", want: "C:/Users/test/.claude"},
		{name: "drive with backslashes", configured: `C:\Users\test\.claude`, want: `C:\Users\test\.claude`},
		{name: "UNC", configured: `\\server\share\.claude`, want: `\\server\share\.claude`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveClaudeConfigRoot(t, tt.configured); got != tt.want {
				t.Fatalf("claude_config_root() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUserPromptMaxTimeFallbackReachesDirectCurlCalls(t *testing.T) {
	if got, want := capturedUserPromptMaxTimes(t, "0"), []string{"0.2", "2", "0.2", "0.2", "0.2", "0.2"}; !equalStrings(got, want) {
		t.Fatalf("curl --max-time values = %q, want %q", got, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// writeRecordingEngramStub writes a fake `engram` binary that logs each invocation's args to $ENGRAM_TEST_ENGRAM_LOG and exits 0.
func writeRecordingEngramStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "engram")
	content := "#!/bin/bash\nprintf '%s\\n' \"$*\" >> \"$ENGRAM_TEST_ENGRAM_LOG\"\nif [ \"$*\" = \"instance-id\" ]; then printf '00000000000000000000000000000000\\n'; fi\nexit 0\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write recording engram stub: %v", err)
	}
	return dir
}

func writeMigratingEngramStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "engram")
	content := "#!/bin/bash\nprintf '%s\\n' \"$*\" >> \"$ENGRAM_TEST_ENGRAM_LOG\"\nif [ \"$*\" = \"instance-id\" ]; then printf '00000000000000000000000000000000\\n'; fi\nif [ \"$*\" = \"setup claude-code --mcp-only\" ]; then\n  mkdir -p \"$(dirname \"$ENGRAM_TEST_MCP_CONFIG\")\"\n  printf '{}' > \"$ENGRAM_TEST_MCP_CONFIG\"\nfi\nexit 0\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write migrating engram stub: %v", err)
	}
	return dir
}

// readEngramInvocations returns the argument lists recorded by writeRecordingEngramStub, in call order.
func readEngramInvocations(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read engram invocation log: %v", err)
	}
	trimmed := strings.TrimSuffix(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// healthyServer answers 200 to /health and 404 to everything else.
func healthyServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"instance_id":"00000000000000000000000000000000"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSessionStartHonorsClaudeConfigDirForMCPMigrationGuard verifies session-start.sh's MCP-migration guard honors CLAUDE_CONFIG_DIR (issue #1081).
func TestSessionStartHonorsClaudeConfigDirForMCPMigrationGuard(t *testing.T) {
	requireHookBinaries(t)

	runWithEngramStub := func(t *testing.T, configDir string) []string {
		t.Helper()
		srv := healthyServer(t)
		stubDir := writeRecordingEngramStub(t)
		logPath := filepath.Join(t.TempDir(), "engram-invocations.log")
		// bash's PATH is always colon-separated, even under Git Bash on Windows.
		env := map[string]string{
			"ENGRAM_PORT":            serverPort(t, srv),
			"CLAUDE_CONFIG_DIR":      configDir,
			"PATH":                   stubDir + ":" + os.Getenv("PATH"),
			"ENGRAM_TEST_ENGRAM_LOG": logPath,
		}
		stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, newSessionID(t), t.TempDir())
		runHook(t, "session-start.sh", stdin, env)
		return readEngramInvocations(t, logPath)
	}

	t.Run("existing regular file under CLAUDE_CONFIG_DIR skips migration", func(t *testing.T) {
		configDir := t.TempDir()
		mcpDir := filepath.Join(configDir, "mcp")
		if err := os.MkdirAll(mcpDir, 0o755); err != nil {
			t.Fatalf("create mcp dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(mcpDir, "engram.json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("write existing mcp config: %v", err)
		}

		for _, args := range runWithEngramStub(t, configDir) {
			if args == "setup claude-code --mcp-only" {
				t.Fatalf("migration invoked even though a regular MCP config already exists under CLAUDE_CONFIG_DIR")
			}
		}
	})

	t.Run("missing config under CLAUDE_CONFIG_DIR triggers migration", func(t *testing.T) {
		configDir := t.TempDir() // mcp/engram.json intentionally absent

		invocations := runWithEngramStub(t, configDir)
		found := false
		for _, args := range invocations {
			if args == "setup claude-code --mcp-only" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("migration was not invoked for a missing MCP config under CLAUDE_CONFIG_DIR; invocations: %v", invocations)
		}
	})

	t.Run("symlink under CLAUDE_CONFIG_DIR triggers migration once", func(t *testing.T) {
		configDir := t.TempDir()
		mcpDir := filepath.Join(configDir, "mcp")
		if err := os.MkdirAll(mcpDir, 0o755); err != nil {
			t.Fatalf("create mcp dir: %v", err)
		}
		target := filepath.Join(t.TempDir(), "existing-engram.json")
		if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
			t.Fatalf("write symlink target: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(mcpDir, "engram.json")); err != nil {
			t.Skipf("symlink creation is unavailable: %v", err)
		}

		count := 0
		for _, args := range runWithEngramStub(t, configDir) {
			if args == "setup claude-code --mcp-only" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("migration invocations for symlink MCP config = %d, want 1", count)
		}
	})

	t.Run("migration creates the resolved config and runs once", func(t *testing.T) {
		configDir := t.TempDir()
		mcpConfig := filepath.Join(configDir, "mcp", "engram.json")
		srv := healthyServer(t)
		stubDir := writeMigratingEngramStub(t)
		logPath := filepath.Join(t.TempDir(), "engram-invocations.log")
		env := map[string]string{
			"ENGRAM_PORT":            serverPort(t, srv),
			"CLAUDE_CONFIG_DIR":      configDir,
			"PATH":                   stubDir + ":" + os.Getenv("PATH"),
			"ENGRAM_TEST_ENGRAM_LOG": logPath,
			"ENGRAM_TEST_MCP_CONFIG": mcpConfig,
		}
		stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, newSessionID(t), t.TempDir())
		runHook(t, "session-start.sh", stdin, env)
		info, err := os.Stat(mcpConfig)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("first migration must create regular MCP config at %q: info=%v, err=%v", mcpConfig, info, err)
		}
		runHook(t, "session-start.sh", stdin, env)
		if got := strings.Count(strings.Join(readEngramInvocations(t, logPath), "\n"), "setup claude-code --mcp-only"); got != 1 {
			t.Fatalf("migration invocations across two SessionStart runs = %d, want 1", got)
		}
	})

	// migrationWasInvoked reports whether "setup claude-code --mcp-only"
	// appears among the recorded engram invocations.
	migrationWasInvoked := func(invocations []string) bool {
		for _, args := range invocations {
			if args == "setup claude-code --mcp-only" {
				return true
			}
		}
		return false
	}

	t.Run("unset or blank CLAUDE_CONFIG_DIR falls back to $HOME/.claude", func(t *testing.T) {
		cases := []struct {
			name          string
			setConfig     bool
			configDir     string
			createConfig  bool
			wantMigration bool
		}{
			{name: "unset/config present", createConfig: true},
			{name: "unset/config absent", wantMigration: true},
			{name: "empty/config present", setConfig: true, createConfig: true},
			{name: "empty/config absent", setConfig: true, wantMigration: true},
			{name: "whitespace-only/config present", setConfig: true, configDir: "   \t  ", createConfig: true},
			{name: "whitespace-only/config absent", setConfig: true, configDir: "   \t  ", wantMigration: true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				home := t.TempDir()
				if tc.createConfig {
					mcpDir := filepath.Join(home, ".claude", "mcp")
					if err := os.MkdirAll(mcpDir, 0o755); err != nil {
						t.Fatalf("create mcp dir: %v", err)
					}
					if err := os.WriteFile(filepath.Join(mcpDir, "engram.json"), []byte("{}"), 0o644); err != nil {
						t.Fatalf("write existing mcp config: %v", err)
					}
				}

				srv := healthyServer(t)
				stubDir := writeRecordingEngramStub(t)
				logPath := filepath.Join(t.TempDir(), "engram-invocations.log")
				env := map[string]string{
					"ENGRAM_PORT":            serverPort(t, srv),
					"HOME":                   home,
					"PATH":                   stubDir + ":" + os.Getenv("PATH"),
					"ENGRAM_TEST_ENGRAM_LOG": logPath,
				}
				if tc.setConfig {
					env["CLAUDE_CONFIG_DIR"] = tc.configDir
				}
				stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, newSessionID(t), t.TempDir())
				runHook(t, "session-start.sh", stdin, env)

				if got := migrationWasInvoked(readEngramInvocations(t, logPath)); got != tc.wantMigration {
					t.Fatalf("migration invoked=%v, want %v", got, tc.wantMigration)
				}
			})
		}
	})

	t.Run("relative CLAUDE_CONFIG_DIR resolved against hook's working directory", func(t *testing.T) {
		const rel = "relative-claude-config"
		cases := []struct {
			name          string
			createConfig  bool
			wantMigration bool
		}{
			{name: "config present", createConfig: true, wantMigration: false},
			{name: "config absent", createConfig: false, wantMigration: true},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				workDir := t.TempDir()
				if tc.createConfig {
					mcpDir := filepath.Join(workDir, rel, "mcp")
					if err := os.MkdirAll(mcpDir, 0o755); err != nil {
						t.Fatalf("create mcp dir: %v", err)
					}
					if err := os.WriteFile(filepath.Join(mcpDir, "engram.json"), []byte("{}"), 0o644); err != nil {
						t.Fatalf("write existing mcp config: %v", err)
					}
				}

				srv := healthyServer(t)
				stubDir := writeRecordingEngramStub(t)
				logPath := filepath.Join(t.TempDir(), "engram-invocations.log")
				env := map[string]string{
					"ENGRAM_PORT":            serverPort(t, srv),
					"CLAUDE_CONFIG_DIR":      rel,
					"PATH":                   stubDir + ":" + os.Getenv("PATH"),
					"ENGRAM_TEST_ENGRAM_LOG": logPath,
				}
				stdin := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, newSessionID(t), t.TempDir())
				runHookInDir(t, "session-start.sh", stdin, env, workDir)

				if got := migrationWasInvoked(readEngramInvocations(t, logPath)); got != tc.wantMigration {
					t.Fatalf("migration invoked=%v, want %v", got, tc.wantMigration)
				}
			})
		}
	})
}
