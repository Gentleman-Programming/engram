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
	run := exec.CommandContext(ctx, "cmd.exe")
	run.SysProcAttr = &syscall.SysProcAttr{CmdLine: "/D /S /C " + command}
	run.Env = append(os.Environ(), "ENGRAM_PORT="+port)
	run.Stdin = strings.NewReader(input)
	var stdout, stderr strings.Builder
	run.Stdout = &stdout
	run.Stderr = &stderr
	err := run.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	t.Fatalf("run manifest command: %v", err)
	return "", "", -1
}
