//go:build windows

package plugin_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexWindowsPowerShellFileCancellation(t *testing.T) {
	const modeKey = "ENGRAM_TEST_POWERSHELL_FILE_CANCEL_MODE"
	if mode := os.Getenv(modeKey); mode != "" {
		script := filepath.Join(t.TempDir(), "owned-deadline.ps1")
		// Sleep inside this process: killing PowerShell leaves no fixture child.
		source := `[Console]::Out.Write('owned-started'); [Console]::Error.Write('owned-stderr'); [System.Threading.Thread]::Sleep(20000)`
		if err := os.WriteFile(script, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		if mode == "default-deadline" {
			_, _, code := runCodexWindowsPowerShellFile(t, script, "", nil)
			t.Fatalf("deadline helper returned ordinary exit=%d", code)
		}
		ctx, cancel := context.WithCancel(context.Background())
		if mode == "deadline" {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Unix(0, 0))
		} else {
			cancel()
		}
		defer cancel()
		runCodexWindowsPowerShellFileContext(t, ctx, script, "", nil)
		t.Fatal("canceled helper returned normally")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"default-deadline", "deadline", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, self, "-test.run=^TestCodexWindowsPowerShellFileCancellation$", "-test.v")
			command.Env = append(os.Environ(), modeKey+"="+mode)
			output, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("diagnostic subprocess canceled: %v", ctx.Err())
			}
			cause := context.DeadlineExceeded.Error()
			if mode == "canceled" {
				cause = context.Canceled.Error()
			}
			want := "run PowerShell command canceled: " + cause
			if err == nil || !strings.Contains(string(output), want) || !strings.Contains(string(output), "stdout=") || !strings.Contains(string(output), "stderr=") {
				t.Fatalf("error=%v output=%s; want explicit %s diagnostic", err, output, cause)
			}
			t.Logf("observed %s diagnostic:\n%s", mode, output)
		})
	}
}

func TestCodexWindowsPowerShellFileExitCodes(t *testing.T) {
	for _, want := range []int{0, 1, 23} {
		t.Run(fmt.Sprint(want), func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "owned-exit.ps1")
			source := fmt.Sprintf(`[Console]::Out.Write('owned-stdout'); [Console]::Error.Write('owned-stderr'); exit %d`, want)
			if err := os.WriteFile(script, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, code := runCodexWindowsPowerShellFile(t, script, "", nil)
			if code != want || string(stdout) != "owned-stdout" || string(stderr) != "owned-stderr" {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want%d and exact streams", code, stdout, stderr, want)
			}
		})
	}
}
