//go:build windows

package plugin_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func runCodexWindowsManifestCommand(t *testing.T, command, input, port string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return runCodexWindowsManifestCommandContext(t, ctx, command, input, port)
}

func runCodexWindowsManifestCommandContext(t *testing.T, ctx context.Context, command, input, port string) (string, string, int) {
	t.Helper()
	run := exec.CommandContext(ctx, "cmd.exe")
	run.SysProcAttr = &syscall.SysProcAttr{CmdLine: "/D /S /C " + command}
	run.Env = append(os.Environ(), "ENGRAM_PORT="+port)
	run.Stdin = strings.NewReader(input)
	var stdout, stderr strings.Builder
	run.Stdout = &stdout
	run.Stderr = &stderr
	err := run.Run()
	if ctx.Err() != nil {
		t.Fatalf("run manifest command canceled: %v (process error: %v), stdout=%q stderr=%q", ctx.Err(), err, stdout.String(), stderr.String())
	}
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	t.Fatalf("run manifest command: %v", err)
	return "", "", -1
}

func TestCodexWindowsManifestCommandCancellation(t *testing.T) {
	const modeKey = "ENGRAM_TEST_MANIFEST_CANCELLATION"
	if mode := os.Getenv(modeKey); mode != "" {
		ctx, cancel := context.WithCancel(context.Background())
		if mode == "deadline" {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Unix(0, 0))
		} else {
			cancel()
		}
		defer cancel()
		runCodexWindowsManifestCommandContext(t, ctx, "exit 23", "", "")
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"canceled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, self, "-test.run=^TestCodexWindowsManifestCommandCancellation$")
			cmd.Env = append(os.Environ(), modeKey+"="+mode)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("diagnostic subprocess timed out: %v", ctx.Err())
			}
			want := context.Canceled.Error()
			if mode == "deadline" {
				want = context.DeadlineExceeded.Error()
			}
			if err == nil || !strings.Contains(string(output), "run manifest command canceled: "+want) {
				t.Fatalf("error=%v output=%s, want explicit %s diagnostic", err, output, want)
			}
		})
	}
}
