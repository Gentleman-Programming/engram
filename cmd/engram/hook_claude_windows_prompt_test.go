package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	if path := os.Getenv("ENGRAM_TEST_CLAUDE_PROMPT_RESERVATION"); path != "" {
		release, busy := reserveClaudeWindowsPrompt(path)
		if release == nil || busy {
			t.Fatal("fixture could not acquire prompt reservation")
		}
		defer release()
		if _, err := os.Stdout.WriteString("held\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatal(err)
		}
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
		"ENGRAM_TEST_CLAUDE_PROMPT_RESERVATION=",
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
	t.Run("closed stdout leaves first prompt available", func(t *testing.T) {
		const failedInput = `{"session_id":"5bc10c2d-54bf-472d-a1d5-ea0ebb3673d2"}`
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = writer.Close() }()
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		cmd := newClaudeWindowsPromptCommand(t, root, "")
		cmd.Stdin = strings.NewReader(failedInput)
		cmd.Stdout = writer
		if err := cmd.Run(); err != nil {
			t.Fatalf("native prompt hook with closed stdout: %v", err)
		}
		assertBootstrap(t, run(t, failedInput, ""))
		if got := run(t, failedInput, ""); got != "{}" {
			t.Fatalf("subsequent prompt returned %q", got)
		}
	})
	t.Run("malformed input still gets local bootstrap", func(t *testing.T) {
		assertBootstrap(t, run(t, `{"session_id":`, ""))
	})
	t.Run("unlocked reservation leaves first prompt available", func(t *testing.T) {
		const id = "a3a6310c-b54c-47f2-b963-39490785abba"
		pending := filepath.Join(root, "engram-claude-"+id+"-tools-loaded.pending")
		if err := os.WriteFile(pending, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		input := `{"session_id":"` + id + `"}`
		assertBootstrap(t, run(t, input, ""))
		if content, err := os.ReadFile(pending); err != nil || string(content) != "fixture" {
			t.Fatalf("unowned reservation = %q, %v", content, err)
		}
		if got := run(t, input, ""); got != "{}" {
			t.Fatalf("subsequent prompt returned %q", got)
		}
	})
	t.Run("terminated reservation holder leaves first prompt available", func(t *testing.T) {
		const id = "e188e4f2-6c40-4b7f-8c68-efc06336e2a4"
		marker := claudeWindowsPromptMarkerPath(root, id)
		pending := marker + ".pending"
		holder := newClaudeWindowsPromptCommand(t, root, "")
		holder.Env = append(holder.Env, "ENGRAM_TEST_CLAUDE_PROMPT_RESERVATION="+pending)
		stdout, err := holder.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdin, err := holder.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = stdin.Close() }()
		if err := holder.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if holder.ProcessState == nil {
				_ = holder.Process.Kill()
				_ = holder.Wait()
			}
		})
		// Keep the holder's input open until we terminate the process.
		if ready, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || ready != "held\n" {
			t.Fatalf("reservation holder readiness = %q, %v", ready, err)
		}
		input := `{"session_id":"` + id + `"}`
		if got := run(t, input, ""); got != "{}" {
			t.Fatalf("prompt while another process holds the reservation = %q", got)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("reservation produced a completed marker: %v", err)
		}
		if err := holder.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		var exitErr *exec.ExitError
		if err := holder.Wait(); !errors.As(err, &exitErr) {
			t.Fatalf("expected terminated reservation holder: %v", err)
		}
		if _, err := os.Stat(pending); !os.IsNotExist(err) {
			t.Fatalf("reservation survived process termination: %v", err)
		}
		assertBootstrap(t, run(t, input, ""))
		if got := run(t, input, ""); got != "{}" {
			t.Fatalf("subsequent prompt = %q", got)
		}
	})
	if _, err := os.Stat(filepath.Join(root, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("hook touched an Engram data directory: %v", err)
	}
}
