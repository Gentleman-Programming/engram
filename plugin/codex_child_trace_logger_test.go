package plugin_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCodexChildTraceLoggerMapping(t *testing.T) {
	testCodexChildTraceLoggerMapping(t, false)
}

func TestCodexChildTraceLoggerWithoutLimiter(t *testing.T) {
	testCodexChildTraceLoggerMapping(t, true)
}

func testCodexChildTraceLoggerMapping(t *testing.T, withoutLimiter bool) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("requires native Windows and Git Bash")
	}
	bash := codexIntentTool(t, "bash", "/usr/bin")
	limiter := filepath.Join(filepath.Dir(bash), "timeout.exe")
	if withoutLimiter {
		limiter = ""
	}
	root := t.TempDir()
	source := filepath.Join(root, "converter.go")
	converter := filepath.Join(root, "converter with spaces.exe")
	program := `package main
import("fmt";"os";"os/exec";"time")
func main(){
 if len(os.Args)>3 && os.Args[1]=="--signal=KILL" {
  if os.Args[2]!="0.2s" { os.Exit(4) }
  cmd:=exec.Command(os.Args[3],os.Args[4:]...)
  cmd.Stdout,cmd.Stderr=os.Stdout,os.Stderr
  if cmd.Run()!=nil { os.Exit(5) }
  return
 }
 if os.Getenv("CODEX_TRACE_HANG")=="1" { time.Sleep(2*time.Second) }
 if err:=os.WriteFile(os.Getenv("CODEX_TRACE_CALLED"),nil,0600);err!=nil { os.Exit(3) }
 fmt.Print("C:/native/tmp")
}`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("go", "build", "-o", converter, source).CombinedOutput(); err != nil {
		t.Fatalf("build controlled mapping converter: %v: %s", err, output)
	}
	for _, tc := range []struct {
		name, limiter, hang, mapping string
		called, success, realLimiter bool
	}{
		// The controlled limiter allows a slow successful conversion. Only the
		// timeout case depends on real scheduling under the diagnostic budget.
		{name: "successful mapping", limiter: converter, hang: "1", mapping: "C:/native/tmp", called: true, success: true},
		{name: "stalled converter", limiter: limiter, hang: "1", realLimiter: true},
		{name: "missing limiter skips conversion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.realLimiter {
				if info, err := os.Stat(tc.limiter); err != nil || !info.Mode().IsRegular() {
					t.Skip("optional Git Bash timeout.exe unavailable")
				}
			}
			trace := filepath.Join(t.TempDir(), "trace")
			called := filepath.Join(t.TempDir(), "called")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-p", "-c",
				string(codexChildTraceLogger("gate", converter, tc.limiter, filepath.ToSlash(trace)))+"printf '{}\\n'")
			cmd.WaitDelay = time.Second
			cmd.Env = []string{"PATH=", "LC_ALL=C", "MSYS_NO_PATHCONV=1", "SystemRoot=" + os.Getenv("SystemRoot"),
				"TEMP=" + root, "TMP=" + root, "CODEX_TRACE_HANG=" + tc.hang, "CODEX_TRACE_CALLED=" + called}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil || stdout.String() != "{}\n" || stderr.Len() != 0 {
				t.Fatalf("logger changed original oracle: %v stdout=%q stderr=%q", err, &stdout, &stderr)
			}
			data, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			fields := map[string]string{}
			for _, field := range strings.Split(strings.TrimSpace(string(data)), "\t") {
				key, value, _ := strings.Cut(field, "=")
				fields[key] = value
			}
			if fields["stage"] != "gate" || fields["native_tmp"] != tc.mapping || (fields["map_exit"] == "0") != tc.success {
				t.Fatalf("unexpected mapping evidence: %q", data)
			}
			if tc.limiter == "" && fields["map_exit"] != "125" {
				t.Fatalf("missing limiter must record unavailable mapping: %q", data)
			}
			_, err = os.Stat(called)
			if tc.called && err != nil || !tc.called && !os.IsNotExist(err) {
				t.Fatalf("converter completion marker: %v, want completion=%v", err, tc.called)
			}
		})
	}
}
