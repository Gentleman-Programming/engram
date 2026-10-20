package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSQLiteTempFailurePaths(t *testing.T) {
	if testing.Short() {
		t.Skip("isolated test-binary subprocesses")
	}
	if runtime.GOOS != "windows" {
		t.Skip("Windows startup failures")
	}
	if mode := os.Getenv("ENGRAM_TEST_SQLITE_TEMP_FAILURE"); mode != "" {
		if strings.HasPrefix(mode, "isolation-") {
			runSQLiteTempIsolation(t, strings.TrimPrefix(mode, "isolation-"))
		} else {
			runSQLiteTempStartup(t, mode)
		}
		return
	}
	for _, mode := range []string{"preparation", "assertion", "isolation-preparation", "isolation-assertion"} {
		t.Run(mode, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestSQLiteTempFailurePaths$", "-test.count=1")
			cmd.Env = append(os.Environ(), "ENGRAM_TEST_SQLITE_TEMP_FAILURE="+mode)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			output := stdout.String() + stderr.String()
			if err == nil {
				t.Fatalf("forced failure silently passed: %s", output)
			}
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				t.Fatalf("not a normal test failure: %v\n%s", err, output)
			}
			text := string(output)
			if !strings.Contains(text, "--- FAIL: TestSQLiteTempFailurePaths") || !strings.HasSuffix(strings.TrimSpace(stdout.String()), "FAIL") || strings.Contains(text, "panic:") {
				t.Fatalf("startup failure escaped the test harness:\n%s", text)
			}
			if strings.HasSuffix(mode, "preparation") && !strings.Contains(text, "unexpected exit 1") {
				t.Fatalf("missing exit diagnostic: %s", text)
			}
			if strings.HasSuffix(mode, "assertion") && !strings.Contains(text, "forced store assertion") {
				t.Fatalf("missing assertion diagnostic: %s", text)
			}
			wantStderr := ""
			if strings.HasSuffix(mode, "preparation") {
				wantStderr = "engram: engram: data directory must be an absolute path, got \"relative-data\" — set ENGRAM_DATA_DIR or ensure your home directory is resolvable\n"
				if strings.HasPrefix(mode, "isolation-") {
					wantStderr += wantStderr
				}
			}
			if stderr.String() != wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), wantStderr)
			}
			t.Logf("isolated %s: exit 1\nstdout:\n%sstderr:\n%s", mode, stdout.String(), stderr.String())
		})
	}
}
