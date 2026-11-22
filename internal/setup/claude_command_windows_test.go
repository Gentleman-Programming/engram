package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestClaudeProfileTimeoutDrainsBeforeReleasingLock(t *testing.T) {
	profile := t.TempDir()
	root := filepath.Join(profile, managedClaudeDirectory)
	t.Setenv("ENGRAM_TEST_CLAUDE_JOB", profile)
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		defer close(done)
		release, err := lockManagedClaude(root)
		if err != nil {
			done <- result{err: err}
			return
		}
		out, err := runClaudeProfileProcess(3*time.Second, os.Args[0], "-test.run=^TestClaudeProfileJobWorker$", "--", "parent")
		release()
		done <- result{out, err}
	}()
	defer func() {
		// Always wait for the fixture to exit before t.TempDir is removed.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("timed-out fixture still running")
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	child := openClaudeTestProcess(t, filepath.Join(profile, "child.pid"), deadline)
	defer windows.CloseHandle(child)
	parent := openClaudeTestProcess(t, filepath.Join(profile, "parent.pid"), deadline)
	defer windows.CloseHandle(parent)

	release, err := lockManagedClaude(root)
	if err == nil {
		release()
		t.Fatal("competing setup acquired lock during Claude command")
	}
	finished := <-done
	if !errors.Is(finished.err, context.DeadlineExceeded) ||
		!strings.Contains(string(finished.output), "Claude parent ready") ||
		!strings.Contains(string(finished.output), "Claude writer ready") {
		t.Fatalf("deadline result = %v, output = %q", finished.err, finished.output)
	}
	release, err = lockManagedClaude(root)
	if err != nil {
		t.Fatalf("profile still locked after timeout: %v", err)
	}
	defer release()
	for _, process := range []windows.Handle{parent, child} {
		state, err := windows.WaitForSingleObject(process, 0)
		if err != nil || state != windows.WAIT_OBJECT_0 {
			t.Fatalf("profile lock reopened before writer exited: state %d, error %v", state, err)
		}
	}
	out, err := runClaudeProfileProcess(3*time.Second, os.Args[0], "-test.run=^TestClaudeProfileJobWorker$", "--", "retry")
	if err != nil || !strings.Contains(string(out), "retry ready") {
		t.Fatalf("retry = %v, output %q", err, out)
	}
}

func openClaudeTestProcess(t *testing.T, pidFile string, deadline time.Time) windows.Handle {
	t.Helper()
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			pid, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32)
			if err == nil {
				process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
				if err != nil {
					t.Fatal(err)
				}
				state, err := windows.WaitForSingleObject(process, 0)
				if state != uint32(windows.WAIT_TIMEOUT) || err != nil {
					windows.CloseHandle(process)
					t.Fatalf("fixture writer was not running: state %d, error %v", state, err)
				}
				return process
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no subprocess at %s", pidFile)
	return 0
}

func TestClaudeProfileJobWorker(t *testing.T) {
	profile := os.Getenv("ENGRAM_TEST_CLAUDE_JOB")
	if profile == "" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "retry" {
		fmt.Println("retry ready")
		return
	}
	if mode == "parent" {
		grandchild := exec.Command(os.Args[0], "-test.run=^TestClaudeProfileJobWorker$", "--", "child")
		grandchild.Stdout, grandchild.Stderr = os.Stdout, os.Stderr
		if err := grandchild.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Println("Claude parent ready")
	} else if mode == "child" {
		fmt.Fprintln(os.Stderr, "Claude writer ready")
	} else {
		t.Fatalf("unexpected child mode %q", mode)
	}
	if err := os.WriteFile(filepath.Join(profile, mode+".pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}
