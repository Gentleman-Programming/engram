package plugin_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

func buildEngramBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "engram")
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

	hookInput := `{"session_id":"pi-guard-session","cwd":"` + t.TempDir() + `","prompt":"hello from pi","source":"startup","tool_name":"mcp__engram__mem_save","tool_input":{"title":"x"}}`

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
			srv, requests := piGuardServer(t)
			cmd := exec.Command(engramBin, "hook", "claude-pre-tool-use")
			cmd.Stdin = strings.NewReader(hookInput)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "ENGRAM_URL=" + srv.URL, "ENGRAM_DATA_DIR=" + t.TempDir()}
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
			if !strings.Contains(stdout.String(), `"updatedInput"`) {
				t.Errorf("stdout = %q, want bound updatedInput without PI_CODING_AGENT", stdout.String())
			}
			if len(got) == 0 {
				t.Errorf("expected session registration without PI_CODING_AGENT")
			}
		})
	}
}
