package plugin_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCodexHandoffToolGuard(t *testing.T) {
	tools := []string{"engram", "curl", "cat", "dirname", "jq", "date", "touch", "bash"}
	cases := []struct{ name, remove, inject string }{
		{name: "owned files use one validation subprocess"},
		{name: "function shadow", inject: "jq() { :; }\n"},
		{name: "alias shadow", inject: "shopt -s expand_aliases\nalias jq=true\n"},
		{name: "wrong resolved path", inject: `command() {
  if [ "$1" = -v ] && [ "$2" = jq ]; then printf '/outside/jq\n'; else builtin command "$@"; fi
}
`},
	}
	for _, name := range tools {
		cases = append(cases, struct{ name, remove, inject string }{name: "missing " + name, remove: name})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cwd := filepath.Join(t.TempDir(), "fixture root with spaces")
			if err := os.Mkdir(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			f := newCodexIntentFixture(t)
			env := codexHandoffEnv(t, cwd, "http://127.0.0.1:19437")
			if tc.remove != "" {
				if err := os.Remove(filepath.Join(cwd, "bin", tc.remove)); err != nil {
					t.Fatal(err)
				}
			}
			trace := filepath.Join(cwd, "validation-pids")
			bin := codexIntentQuote(filepath.ToSlash(filepath.Join(cwd, "bin")))
			if f.cygpath != "" {
				bin = "$(" + codexIntentQuote(f.cygpath) + " -u -- " + bin + ")"
			}
			script := "bin=" + bin + "\nexport PATH=\"$bin\"\n" + tc.inject +
				"type() { builtin printf '%s\\n' \"${BASHPID-unavailable}\" >> " + codexIntentQuote(filepath.ToSlash(trace)) + "; builtin type \"$@\"; }\n" +
				codexHandoffToolGuard() + "printf 'accepted\\n'\n"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, f.bash, "--noprofile", "--norc", "-p", "-c", script)
			cmd.WaitDelay = time.Second
			cmd.Env, cmd.Dir = env, cwd
			output, err := cmd.CombinedOutput()
			if tc.remove != "" || tc.inject != "" {
				if err == nil || cmd.ProcessState.ExitCode() != 92 || len(output) != 0 {
					t.Fatalf("guard accepted invalid lookup: err=%v output=%q", err, output)
				}
				return
			}
			if err != nil || string(output) != "accepted\n" {
				t.Fatalf("owned tools rejected: %v %q", err, output)
			}
			data, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			pids := map[string]bool{}
			calls := strings.Fields(string(data))
			for _, pid := range calls {
				pids[pid] = true
			}
			if len(calls) != len(tools) {
				t.Fatalf("validated %d tools, want %d", len(calls), len(tools))
			}
			// Legacy macOS Bash lacks BASHPID. All lookup/rejection behavior
			// still runs there; native Windows must expose process identity
			// for the MSYS subprocess-budget regression.
			if pids["unavailable"] && runtime.GOOS != "windows" {
				return
			}
			if pids["unavailable"] || len(pids) != 1 {
				t.Fatalf("validation used %d subprocesses for %d tool checks; want one subprocess and %d checks", len(pids), len(calls), len(tools))
			}
		})
	}
}
