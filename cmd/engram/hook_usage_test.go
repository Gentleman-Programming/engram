package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHookUsageCLIProcess(t *testing.T) {
	if os.Getenv("ENGRAM_TEST_HOOK_USAGE_PROCESS") != "1" {
		return
	}
	os.Args = []string{"engram", "hook", "unknown-action"}
	main()
	os.Exit(0)
}

func TestHookUsagePreservesMergedCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("CLI subprocess integration test")
	}
	root := t.TempDir()
	// A config-free command must not need a writable temporary directory.
	missingTemp := filepath.Join(root, "missing-temp")
	command := exec.Command(os.Args[0], "-test.run=^TestHookUsageCLIProcess$")
	command.Env = append(os.Environ(),
		"ENGRAM_TEST_HOOK_USAGE_PROCESS=1",
		"HOME="+root,
		"USERPROFILE="+root,
		"APPDATA="+root,
		"LOCALAPPDATA="+root,
		"XDG_CONFIG_HOME="+root,
		"TEMP="+missingTemp,
		"TMP="+missingTemp,
		"TMPDIR="+missingTemp,
		"ENGRAM_DATA_DIR="+filepath.Join(root, "data"),
	)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("exit = %v, want 1; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	want := "usage: engram hook claude-pre-tool-use|claude-bind-session|codex-pre-tool-use|codex-user-prompt-submit|resolve-hook-project\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("rejected hook command wrote configuration or data: %v", entries)
	}
}
