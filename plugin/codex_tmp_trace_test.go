package plugin_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Metadata is emitted by builtins after startup. Stderr is opened by the
// calling shell BEFORE each child starts; it includes that child's startup.
const codexTraceMetadata = `printf "%s\n" "${BASHPID:-$$}" "$PPID" "$BASH_VERSION" "${TMPDIR-}" "${TMP-}" "${TEMP-}" > "$INTENT_TRACE/$INTENT_TRACE_CALL.meta"`

func codexIntentTraceLauncher() string {
	s := codexIntentLauncher
	// This diagnostic copy leaves the ordinary launcher constant byte-identical.
	s = strings.Replace(s, "set -euo pipefail", "set -euo pipefail\nexport INTENT_TRACE=\"$2/trace\" INTENT_TRACE_CALL=initial-bash\n"+codexTraceMetadata, 1)
	// Same shebang/process chain: no additional Bash process or selector.
	s = strings.Replace(s, `printf '#!%s -p\nexec %q "$@"\n' "$bash_alias" "$target"`, `printf '#!%s -p\n%s\nexec %q "$@"\n' "$bash_alias" '`+codexTraceMetadata+`' "$target"`, 1)
	s = strings.ReplaceAll(s, `reject() { printf`, codexTraceMetadata+"\n"+`reject() { printf`)
	s = strings.Replace(s, `jq -es --arg cwd`, `INTENT_TRACE_CALL=nested-jq jq -es --arg cwd`, 1)
	s = strings.Replace(s, `>/dev/null || reject`, `2> "$INTENT_TRACE/nested-jq.stderr" >/dev/null || reject`, 1)
	for _, call := range []struct{ command, boundary string }{
		{"engram instance-id", "instance-id"},
		{"curl -sf http://127.0.0.1:19437/health --max-time 1", "health"},
		{"engram hook codex-register", "register"},
		{"engram serve", "serve"},
		{"curl -sf https://example.invalid --max-time 1", "external-curl"},
	} {
		s = strings.Replace(s, call.command, "INTENT_TRACE_CALL="+call.boundary+" "+call.command+` 2> "$INTENT_TRACE/`+call.boundary+`.stderr"`, 1)
	}
	return s
}

func codexIntentTraceReport(t *testing.T, f codexIntentFixture, cmd *exec.Cmd, initial string) bool {
	t.Helper()
	ok := true
	for _, boundary := range []string{"initial-bash", "instance-id", "health", "register", "nested-jq", "serve", "external-curl"} {
		r := map[string]any{"boundary": boundary, "selected_executable": f.bash,
			"attribution_limit": "stderr belongs to invocation boundary, not proof of emitting PID; Bash/MSYS IDs are not native Windows PIDs; absent startup metadata is unknown"}
		if cmd.Process != nil && boundary == "initial-bash" {
			key := "go_child_pid"
			if runtime.GOOS == "windows" {
				key = "native_windows_pid"
			}
			r[key] = cmd.Process.Pid
		}
		data, err := os.ReadFile(filepath.Join(f.root, "trace", boundary+".meta"))
		fields := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if err != nil || len(fields) != 6 {
			r["metadata_status"] = "unknown: missing or malformed startup record"
			ok = false
		} else {
			for i, key := range []string{"bash_pid", "bash_ppid", "bash_version", "TMPDIR", "TMP", "TEMP"} {
				r[key] = fields[i]
			}
		}
		stderr := initial
		if boundary != "initial-bash" {
			data, err = os.ReadFile(filepath.Join(f.root, "trace", boundary+".stderr"))
			stderr = string(data)
			if err != nil {
				r["stderr_status"] = "unknown: missing record"
				ok = false
			}
		}
		if stderr != "" {
			ok = false
		}
		// Bound verbose output; overflow is explicitly incomplete and still fails.
		if len(stderr) > 16384 {
			stderr = stderr[:16384]
			r["stderr_truncated"] = true
			ok = false
		}
		r["raw_stderr"] = stderr
		encoded, err := json.Marshal(r)
		if err != nil {
			t.Error(err)
			ok = false
		} else {
			t.Logf("codex-tmp-trace %s", encoded)
		}
	}
	if !ok {
		t.Error("Codex trace has stderr or incomplete required records")
	}
	return ok
}

// Invoke the public fixture in a test subprocess so a fatal remains observable.
// This regression also compiles on the base: no diagnostic-only Go API is used.
func TestCodexShellTraceContract(t *testing.T) {
	if child := os.Getenv("CODEX_TRACE_CONTRACT_CHILD"); child != "" {
		f := newCodexIntentFixture(t)
		if child == "redirected-stderr" {
			if err := os.Mkdir(filepath.Join(f.root, "trace"), 0700); err != nil {
				t.Fatal(err)
			}
			for _, boundary := range []string{"initial-bash", "instance-id", "health", "register", "nested-jq", "serve", "external-curl"} {
				if err := os.WriteFile(filepath.Join(f.root, "trace", boundary+".meta"), []byte("1\n2\nfixture-version\n/private\n/private\n/private\n"), 0600); err != nil {
					t.Fatal(err)
				}
				stderr := ""
				if boundary == "nested-jq" {
					stderr = "trace-negative-stderr\n"
				}
				if err := os.WriteFile(filepath.Join(f.root, "trace", boundary+".stderr"), []byte(stderr), 0600); err != nil {
					t.Fatal(err)
				}
			}
			codexIntentTraceReport(t, f, &exec.Cmd{}, "")
			return
		}
		if child == "pid-fallback" {
			f.tools += "\nunset BASHPID\n"
		}
		if child == "stderr" {
			f.tools += "\nprintf 'trace-negative-stderr\\n' >&2\n"
		}
		f.run(t, "preflight", "", "unset")
		return
	}
	for _, child := range []string{"clean", "pid-fallback", "stderr", "redirected-stderr"} {
		t.Run(child, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestCodexShellTraceContract$", "-test.v")
			cmd.Env = append(os.Environ(), "CODEX_TEST_TMP_TRACE=1", "CODEX_TRACE_CONTRACT_CHILD="+child)
			out, err := cmd.CombinedOutput()
			text := string(out)
			for _, boundary := range []string{"initial-bash", "instance-id", "health", "register", "nested-jq", "serve", "external-curl"} {
				if !strings.Contains(text, `"boundary":"`+boundary+`"`) {
					t.Fatalf("missing %s record: %s", boundary, text)
				}
			}
			for _, field := range []string{`"bash_pid":`, `"bash_ppid":`, `"bash_version":`, `"TMPDIR":`, `"TMP":`, `"TEMP":`, `"raw_stderr":`, `"selected_executable":`} {
				if !strings.Contains(text, field) {
					t.Fatalf("missing %s: %s", field, text)
				}
			}
			if child == "stderr" || child == "redirected-stderr" {
				if err == nil || !strings.Contains(text, "trace-negative-stderr") {
					t.Fatalf("stderr must fail and remain visible: exit=%v %s", err, text)
				}
			} else if err != nil {
				t.Fatalf("clean trace failed: %v %s", err, text)
			}
		})
	}
}
