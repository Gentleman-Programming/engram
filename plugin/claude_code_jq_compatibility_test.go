package plugin_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// jq 1.6 exits successfully on an empty input stream even with -e. Emulate
// only that difference, delegating all JSON processing to the installed jq.
func jq16CompatibilityPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("jq compatibility wrapper requires POSIX bash and /usr/bin/jq")
	}
	if _, err := os.Stat("/usr/bin/jq"); err != nil {
		t.Skipf("jq compatibility delegate unavailable: %v", err)
	}
	dir := t.TempDir()
	wrapper := `#!/bin/bash
exit_status=0
for arg in "$@"; do
  case "$arg" in
    -n|--null-input|--version|--help) exec /usr/bin/jq "$@" ;;
    --exit-status) exit_status=1 ;;
    --*) ;;
    -*) case "$arg" in *e*) exit_status=1 ;; esac ;;
  esac
done
input_file=$(mktemp) || exit 1
trap 'rm -f "$input_file"' EXIT
cat > "$input_file"
if [ "$exit_status" = 1 ] && [ ! -s "$input_file" ]; then
  exit 0
fi
/usr/bin/jq "$@" < "$input_file"
`
	if err := os.WriteFile(filepath.Join(dir, "jq"), []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write jq compatibility wrapper: %v", err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func TestClaudeHooksJQ16Compatibility(t *testing.T) {
	requireHookBinaries(t)
	t.Setenv("PATH", jq16CompatibilityPath(t))
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"TestSessionStartRegistersProjectOwnedClaudeSession", TestSessionStartRegistersProjectOwnedClaudeSession},
		{"TestSessionStartResumeKeepsLegacySharedOwnership", TestSessionStartResumeKeepsLegacySharedOwnership},
		{"TestClaudeCodeSessionEndHook", TestClaudeCodeSessionEndHook},
	} {
		t.Run(tc.name, tc.run)
	}
}

func TestClaudeHookResponseValidation(t *testing.T) {
	requireHookBinaries(t)
	for _, emulated := range []bool{false, true} {
		t.Run(fmt.Sprintf("jq16=%t", emulated), func(t *testing.T) {
			if emulated {
				t.Setenv("PATH", jq16CompatibilityPath(t))
			}
			for _, tc := range []struct {
				name, response, call, want string
				wantExit                   int
			}{
				{"empty health", "", "engram_health_matches_instance expected", "", 1},
				{"valid health", `{"instance_id":"expected"}`, "engram_health_matches_instance expected", "", 0},
				{"empty project", "", "resolve_project /work", "", 1},
				{"valid project", `{"project":"client","project_source":"config"}`, "resolve_project /work", "client\n", 0},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var requests atomic.Int64
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						requests.Add(1)
						wantPath := "/health"
						if strings.HasPrefix(tc.call, "resolve_project") {
							wantPath = "/project/current"
							if r.URL.Query().Get("cwd") != "/work" {
								t.Errorf("cwd = %q, want /work", r.URL.Query().Get("cwd"))
							}
						}
						if r.Method != http.MethodGet || r.URL.Path != wantPath {
							t.Errorf("request = %s %s, want GET %s", r.Method, r.URL.Path, wantPath)
						}
						_, _ = io.WriteString(w, tc.response)
					}))
					t.Cleanup(srv.Close)
					helper := filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "_helpers.sh")
					cmd := exec.Command("bash", "-c", `source "$1"; `+tc.call, "bash", bashScriptPath(t, helper))
					for _, entry := range os.Environ() {
						key := strings.SplitN(entry, "=", 2)[0]
						if key != "ENGRAM_URL" && key != "ENGRAM_SOCKET" && key != "ENGRAM_PORT" {
							cmd.Env = append(cmd.Env, entry)
						}
					}
					cmd.Env = append(cmd.Env, "ENGRAM_URL="+srv.URL, "ENGRAM_SOCKET=")
					var stdout, stderr strings.Builder
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					err := cmd.Run()
					gotExit := 0
					if err != nil {
						if exit, ok := err.(*exec.ExitError); ok {
							gotExit = exit.ExitCode()
						} else {
							t.Fatalf("run helper: %v", err)
						}
					}
					if gotExit != tc.wantExit || stdout.String() != tc.want || stderr.String() != "" || requests.Load() != 1 {
						t.Fatalf("helper exit=%d stdout=%q stderr=%q requests=%d, want exit=%d stdout=%q stderr empty requests=1",
							gotExit, stdout.String(), stderr.String(), requests.Load(), tc.wantExit, tc.want)
					}
				})
			}
		})
	}
}
