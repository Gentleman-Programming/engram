package plugin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Pi runs Claude models through pi-claude-bridge, which loads the user's Claude
// Code settings. Pi already owns its Engram session through gentle-engram, so
// every Claude Code plugin hook must be a silent no-op when Pi marks its child
// processes with PI_CODING_AGENT.

type piGuardRequests struct {
	mu   sync.Mutex
	seen []string
}

func (r *piGuardRequests) add(entry string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, entry)
}

func (r *piGuardRequests) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func piGuardServer(t *testing.T) (*httptest.Server, *piGuardRequests) {
	t.Helper()
	requests := &piGuardRequests{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r.Method + " " + r.URL.Path)
		switch r.URL.Path {
		case "/sessions/pi-guard-session":
			// Model first registration rather than a malformed persisted session.
			w.WriteHeader(http.StatusNotFound)
		case "/project/current":
			_, _ = w.Write([]byte(`{"project":"pi-guard","project_source":"config"}`))
		case "/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"pi-guard-session","status":"created"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, requests
}

// piGuardHookInput encodes a hook payload as JSON, so paths such as Windows
// temp directories are escaped and every case receives valid input.
func piGuardHookInput(t *testing.T, payload map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode hook input: %v", err)
	}
	return string(encoded)
}

func buildEngramBinary(t *testing.T) string {
	t.Helper()
	name := "engram"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/engram")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build engram: %v\n%s", err, out)
	}
	return bin
}

func TestClaudeCodeHooksAreNoOpUnderPi(t *testing.T) {
	requireHookBinaries(t)
	engramBin := buildEngramBinary(t)

	hookInput := piGuardHookInput(t, map[string]any{
		"session_id": "pi-guard-session",
		"cwd":        t.TempDir(),
		"prompt":     "hello from pi",
		"source":     "startup",
		"tool_name":  "mcp__engram__mem_save",
		"tool_input": map[string]string{"title": "x"},
	})

	shellHooks := []string{"session-start.sh", "post-compaction.sh", "user-prompt-submit.sh", "subagent-stop.sh", "session-end.sh"}

	for _, tc := range []struct {
		name string
		env  string // value of PI_CODING_AGENT; "" means unset
	}{
		{"unset", ""},
		{"empty", "empty"}, // set to the empty string
		{"true", "true"},
	} {
		piSet := tc.env != "" && tc.env != "empty"

		for _, script := range shellHooks {
			t.Run(tc.name+"/"+script, func(t *testing.T) {
				srv, requests := piGuardServer(t)
				env := map[string]string{"ENGRAM_URL": srv.URL, "HOME": t.TempDir(), "TMPDIR": t.TempDir()}
				switch {
				case piSet:
					env["PI_CODING_AGENT"] = tc.env
				case tc.env == "empty":
					env["PI_CODING_AGENT"] = ""
				}
				stdout := runHook(t, script, hookInput, env)
				got := requests.list()
				if piSet {
					if stdout != "" {
						t.Errorf("stdout = %q, want empty under Pi", stdout)
					}
					if len(got) != 0 {
						t.Errorf("requests = %v, want none under Pi", got)
					}
					return
				}
				// Without the Pi marker every hook keeps contacting Engram.
				if len(got) == 0 {
					t.Errorf("%s sent no requests without PI_CODING_AGENT", script)
				}
			})
		}

		t.Run(tc.name+"/claude-pre-tool-use", func(t *testing.T) {
			for _, fixture := range []string{"plain", "unrelated-git-ancestor"} {
				t.Run(fixture, func(t *testing.T) {
					root := t.TempDir()
					ancestor := filepath.Join(root, "ancestor")
					cwd := filepath.Join(ancestor, "fixture")
					if err := os.MkdirAll(cwd, 0o700); err != nil {
						t.Fatal(err)
					}
					if fixture == "unrelated-git-ancestor" {
						// Model a snapshot below a broken worktree without relying on WSL.
						if err := os.WriteFile(filepath.Join(ancestor, ".git"), []byte("gitdir: "+filepath.Join(root, "missing-gitdir")+"\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					// Bound discovery above this non-Git cwd, not package-wide or in
					// production. Explicit repositories inside the fixture stay visible.
					ceiling := ancestor
					if inherited := os.Getenv("GIT_CEILING_DIRECTORIES"); inherited != "" {
						ceiling += string(os.PathListSeparator) + inherited
					}
					srv, requests := piGuardServer(t)
					cmd := exec.Command(engramBin, "hook", "claude-pre-tool-use")
					cmd.Stdin = strings.NewReader(piGuardHookInput(t, map[string]any{
						"session_id": "pi-guard-session",
						"cwd":        cwd,
						"tool_name":  "mcp__engram__mem_save",
						"tool_input": map[string]string{"title": "x"},
					}))
					cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "ENGRAM_URL=" + srv.URL, "ENGRAM_DATA_DIR=" + t.TempDir(), "GIT_CEILING_DIRECTORIES=" + ceiling}
					switch {
					case piSet:
						cmd.Env = append(cmd.Env, "PI_CODING_AGENT="+tc.env)
					case tc.env == "empty":
						cmd.Env = append(cmd.Env, "PI_CODING_AGENT=")
					}
					var stdout, stderr bytes.Buffer
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					if err := cmd.Run(); err != nil {
						t.Fatalf("engram hook claude-pre-tool-use must exit 0: %v\nstderr: %s", err, stderr.String())
					}
					if stderr.Len() != 0 {
						t.Errorf("stderr = %q, want empty", stderr.String())
					}
					got := requests.list()
					if piSet {
						if stdout.Len() != 0 {
							t.Errorf("stdout = %q, want empty under Pi", stdout.String())
						}
						if len(got) != 0 {
							t.Errorf("requests = %v, want none under Pi", got)
						}
						return
					}
					var response struct {
						HookSpecificOutput struct {
							HookEventName      string            `json:"hookEventName"`
							PermissionDecision string            `json:"permissionDecision"`
							UpdatedInput       map[string]string `json:"updatedInput"`
						} `json:"hookSpecificOutput"`
					}
					if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
						t.Fatalf("decode hook response %q: %v", stdout.String(), err)
					}
					output := response.HookSpecificOutput
					if output.HookEventName != "PreToolUse" || output.PermissionDecision != "" || len(output.UpdatedInput) != 2 || output.UpdatedInput["title"] != "x" || output.UpdatedInput["session_id"] != "pi-guard-session" {
						t.Errorf("stdout = %q, want bound updatedInput without PI_CODING_AGENT", stdout.String())
					}
					if want := "GET /sessions/pi-guard-session,POST /sessions"; strings.Join(got, ",") != want {
						t.Errorf("requests = %v, want %s", got, want)
					}
				})
			}
		})
	}
}

// TestClaudeCodeWindowsSafePromptRouteIsNoOpUnderPi covers the guard that
// user-prompt-submit.sh runs before its Windows-safe fast path. That path exits
// before _helpers.sh is sourced, so the helpers guard cannot mask a missing
// script-level guard here. The control run proves the fast path was reached.
func TestClaudeCodeWindowsSafePromptRouteIsNoOpUnderPi(t *testing.T) {
	script := bashScriptPath(t, filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "user-prompt-submit.sh"))
	input := piGuardHookInput(t, map[string]any{
		"session_id": "pi-guard-session",
		"cwd":        t.TempDir(),
		"prompt":     "hello from pi",
	})

	run := func(t *testing.T, piValue string) string {
		t.Helper()
		srv, requests := piGuardServer(t)
		cmd := exec.Command("bash", script)
		cmd.Env = append(os.Environ(),
			"MSYSTEM=MINGW64",
			"ENGRAM_CLAUDE_WINDOWS_BASH_SAFE_MODE=auto",
			"ENGRAM_URL="+srv.URL,
			"TMPDIR="+filepath.ToSlash(t.TempDir()),
		)
		if piValue != "" {
			cmd.Env = append(cmd.Env, "PI_CODING_AGENT="+piValue)
		}
		cmd.Stdin = strings.NewReader(input)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("user-prompt-submit.sh must exit 0: %v\nstderr: %s", err, stderr.String())
		}
		if got := requests.list(); len(got) != 0 {
			t.Errorf("Windows-safe route made API requests: %v", got)
		}
		return stdout.String()
	}

	t.Run("control/unset", func(t *testing.T) {
		if out := run(t, ""); !strings.Contains(out, "ToolSearch") {
			t.Fatalf("stdout = %q, want the ToolSearch bootstrap from the Windows-safe route", out)
		}
	})
	t.Run("true", func(t *testing.T) {
		if out := run(t, "true"); out != "" {
			t.Errorf("stdout = %q, want empty under Pi", out)
		}
	})
}

// TestClaudeCodePowerShellPromptHookIsNoOpUnderPi covers the early exit in
// user-prompt-submit.ps1, the PowerShell adapter that the shell tests above do
// not run. The control run proves the adapter executed and printed its bootstrap.
func TestClaudeCodePowerShellPromptHookIsNoOpUnderPi(t *testing.T) {
	powershellPath := claudeCodePowerShell(t)
	adapter := filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "user-prompt-submit.ps1")

	run := func(t *testing.T, piValue string) string {
		t.Helper()
		srv, requests := piGuardServer(t)
		serverURL, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatalf("parse server URL: %v", err)
		}
		sessionID := newSessionID(t)
		stateFile := filepath.Join(os.TempDir(), "engram-claude-"+sessionID+"-tools-loaded")
		t.Cleanup(func() { _ = os.Remove(stateFile) })

		cmd := exec.Command(powershellPath, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", adapter)
		cmd.Env = append(withoutEngramPort(os.Environ()), "ENGRAM_PORT="+serverURL.Port())
		if piValue != "" {
			cmd.Env = append(cmd.Env, "PI_CODING_AGENT="+piValue)
		}
		cmd.Stdin = strings.NewReader(piGuardHookInput(t, map[string]any{
			"session_id": sessionID,
			"cwd":        t.TempDir(),
			"prompt":     "hello from pi",
		}))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("user-prompt-submit.ps1 must exit 0: %v\nstderr: %s", err, stderr.String())
		}
		if piValue != "" {
			if got := requests.list(); len(got) != 0 {
				t.Errorf("requests = %v, want none under Pi", got)
			}
		}
		return stdout.String()
	}

	t.Run("control/unset", func(t *testing.T) {
		if out := run(t, ""); !strings.Contains(out, "ToolSearch") {
			t.Fatalf("stdout = %q, want the ToolSearch bootstrap from the PowerShell adapter", out)
		}
	})
	t.Run("true", func(t *testing.T) {
		if out := run(t, "true"); out != "" {
			t.Errorf("stdout = %q, want empty under Pi", out)
		}
	})
}
