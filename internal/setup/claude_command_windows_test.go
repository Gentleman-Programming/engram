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
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestClaudeOrdinarySetupFallbackRequiresStartupRejection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed bool
		jobErr  error
		retry   bool
	}{
		{"unmanaged nested job rejection", false, errClaudeNestedJobUnavailable, true},
		{"managed nested job rejection", true, errClaudeNestedJobUnavailable, false},
		{"unmanaged timed out", false, context.DeadlineExceeded, false},
		{"unmanaged CLI failure", false, errors.New("Claude failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetSetupSeams(t)
			calls := 0
			runClaudeProfileCommand = func(_ time.Duration, _ string, _ ...string) ([]byte, error) { return nil, tc.jobErr }
			runCommand = func(_ string, _ ...string) ([]byte, error) {
				calls++
				return []byte("legacy"), nil
			}
			out, err := claudeOrdinarySetupRunner(tc.managed)("claude.exe", "plugin", "install", "engram")
			if tc.retry {
				if calls != 1 || err != nil || string(out) != "legacy" {
					t.Fatalf("legacy fallback calls %d, error %v, output %q", calls, err, out)
				}
			} else if calls != 0 || !errors.Is(err, tc.jobErr) {
				t.Fatalf("unexpected legacy fallback calls %d, error %v", calls, err)
			}
		})
	}
}

func TestClaudeOrdinarySetupAfterSuspendedAssignmentDenial(t *testing.T) {
	t.Setenv("ENGRAM_TEST_CLAUDE_JOB", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestClaudeProfileJobWorker$", "-test.v", "--", "deny-assignment").CombinedOutput()
	if err != nil {
		t.Fatalf("restricted parent fixture failed: %v, output %s", err, out)
	}
	if strings.Contains(string(out), "--- SKIP:") {
		t.Skipf("fixture cannot claim a restricted parent job in this host: %s", out)
	}
	if !strings.Contains(string(out), "managed child rejected; ordinary fallback ran") {
		t.Fatalf("fixture did not exercise both routes: %s", out)
	}
}

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
	if mode == "deny-assignment" {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(job)
		limits := uint32(windows.JOB_OBJECT_UILIMIT_HANDLES)
		_, err = windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions,
			uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
			t.Skipf("assign restricted parent job: %v", err)
		}
		var suspended []windows.Handle
		defer func() {
			for _, process := range suspended {
				windows.CloseHandle(process)
			}
		}()
		args := []string{"-test.run=^TestClaudeProfileJobWorker$", "--", "retry"}
		ctx, cancel := context.WithCancel(context.Background())
		assignClaudeSetupJob = func(job, process windows.Handle) error {
			err := windows.AssignProcessToJobObject(job, process)
			cancel()
			return err
		}
		if out, err := runClaudeJob(ctx, os.Args[0], args...); len(out) != 0 ||
			(!errors.Is(err, context.Canceled) && !errors.Is(err, errClaudeNestedJobUnavailable)) {
			t.Fatalf("canceled before resume: %v, output %q", err, out)
		}
		// This host accepts the real constrained Job combination. Inject the
		// possible Windows denial after creating an actual suspended process.
		assignClaudeSetupJob = func(_ windows.Handle, process windows.Handle) error {
			self := windows.CurrentProcess()
			var observer windows.Handle
			if err := windows.DuplicateHandle(self, process, self, &observer, windows.SYNCHRONIZE, false, 0); err != nil {
				t.Fatal(err)
			}
			suspended = append(suspended, observer)
			return windows.ERROR_ACCESS_DENIED
		}
		if out, err := claudeOrdinarySetupRunner(true)(os.Args[0], args...); !errors.Is(err, errClaudeNestedJobUnavailable) || len(out) != 0 {
			t.Fatalf("contained launch in restricted parent job: %v, output %q", err, out)
		}
		if _, err := os.Stat(filepath.Join(profile, "retry-started")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a managed child executed before containment: %v", err)
		}
		if out, err := claudeOrdinarySetupRunner(false)(os.Args[0], args...); err != nil || !strings.Contains(string(out), "retry ready") {
			t.Fatalf("ordinary fallback in restricted parent job: %v, output %q", err, out)
		}
		if len(suspended) != 2 {
			t.Fatalf("contained startup attempts = %d", len(suspended))
		}
		for _, process := range suspended {
			if state, err := windows.WaitForSingleObject(process, 0); err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("denied suspended process survived: state %d, error %v", state, err)
			}
		}
		fmt.Println("managed child rejected; ordinary fallback ran")
		return
	}
	if mode == "retry" {
		if err := os.WriteFile(filepath.Join(profile, "retry-started"), []byte("yes"), 0600); err != nil {
			t.Fatal(err)
		}
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
