package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestClaudeWindowsPromptCLIProcess(t *testing.T) {
	if os.Getenv("ENGRAM_TEST_CLAUDE_WINDOWS_PROMPT_PROCESS") != "1" {
		return
	}
	// TestMain strips the ambient Pi marker before starting any tests.
	if err := os.Setenv("PI_CODING_AGENT", os.Getenv("ENGRAM_TEST_CLAUDE_WINDOWS_PROMPT_PI")); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"engram", "hook", "claude-windows-user-prompt-submit"}, os.Args[3:]...)
	main()
	os.Exit(0)
}

func newClaudeWindowsPromptCommand(t *testing.T, root, pi string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	testArgs := append([]string{"-test.run=^TestClaudeWindowsPromptCLIProcess$", "--"}, args...)
	cmd := exec.CommandContext(ctx, os.Args[0], testArgs...)
	cmd.Env = append(os.Environ(),
		"ENGRAM_TEST_CLAUDE_WINDOWS_PROMPT_PROCESS=1",
		"ENGRAM_TEST_CLAUDE_WINDOWS_PROMPT_PI="+pi,
		"ENGRAM_DATA_DIR="+filepath.Join(root, "must-not-exist"),
		"ENGRAM_URL=http://127.0.0.1:1",
		"ENGRAM_SOCKET="+filepath.Join(root, "unused.sock"),
		"TEMP="+root, "TMP="+root, "TMPDIR="+root,
	)
	return cmd
}

func TestClaudeWindowsPromptCheckCLI(t *testing.T) {
	root := t.TempDir()
	cmd := newClaudeWindowsPromptCommand(t, root, "1", "--check")
	// Leave the writer open to catch an accidental read of hook input.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	output, err := cmd.Output()
	if runtime.GOOS == "windows" {
		if err != nil || string(output) != "{\"hook\":\"claude-windows-user-prompt-submit\",\"version\":1}\n" {
			t.Fatalf("capability check: stdout=%q err=%v", output, err)
		}
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || len(output) != 0 {
			t.Fatalf("non-Windows capability check: stdout=%q err=%v", output, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("capability check wrote files: %v %v", entries, err)
	}
}

func TestClaudeWindowsPromptBootstrapCLI(t *testing.T) {
	root := t.TempDir()
	if runtime.GOOS != "windows" {
		cmd := newClaudeWindowsPromptCommand(t, root, "")
		output, err := cmd.Output()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || len(output) != 0 {
			t.Fatalf("non-Windows hook: stdout=%q err=%v", output, err)
		}
		return
	}
	run := func(t *testing.T, input, pi string) string {
		t.Helper()
		cmd := newClaudeWindowsPromptCommand(t, root, pi)
		cmd.Stdin = strings.NewReader(input)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil || stderr.Len() != 0 {
			t.Fatalf("native prompt hook: %v; stderr=%q", err, stderr.String())
		}
		return strings.TrimSpace(string(output))
	}
	assertBootstrap := func(t *testing.T, output string) {
		t.Helper()
		var reply struct {
			HookSpecificOutput struct {
				Event   string `json:"hookEventName"`
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(output), &reply); err != nil {
			t.Fatalf("expected valid bootstrap JSON: %v", err)
		}
		context := reply.HookSpecificOutput.Context
		if reply.HookSpecificOutput.Event != "UserPromptSubmit" ||
			!strings.Contains(context, "select:mcp__plugin_engram_engram__mem_save,") ||
			!strings.Contains(context, ",mcp__engram__mem_save,") ||
			strings.Contains(output, "systemMessage") {
			t.Fatalf("invalid first-prompt context: %s", output)
		}
	}

	const input = `{"session_id":"7ab8c413-5f2e-4f5f-b283-c402f0538469","prompt":"fixture"}`
	t.Run("Pi leaves first prompt available", func(t *testing.T) {
		if got := run(t, input, "1"); got != "" {
			t.Fatalf("Pi hook returned %q", got)
		}
	})
	t.Run("bootstrap then silence", func(t *testing.T) {
		assertBootstrap(t, run(t, input, ""))
		if got := run(t, input, ""); got != "{}" {
			t.Fatalf("second prompt returned %q", got)
		}
	})
	t.Run("existing Bash marker is respected", func(t *testing.T) {
		const id = "9e17feaa-6ffc-4f48-b03a-8c1ec92764ff"
		if err := os.WriteFile(filepath.Join(root, "engram-claude-"+id+"-tools-loaded"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		if got := run(t, `{"session_id":"`+id+`"}`, ""); got != "{}" {
			t.Fatalf("existing marker returned %q", got)
		}
	})
	t.Run("malformed input still gets local bootstrap", func(t *testing.T) {
		assertBootstrap(t, run(t, `{"session_id":`, ""))
	})
	if _, err := os.Stat(filepath.Join(root, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("hook touched an Engram data directory: %v", err)
	}
}
