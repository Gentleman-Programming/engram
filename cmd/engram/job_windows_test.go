//go:build windows

package main

import (
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

const mcpJobHelperEnv = "ENGRAM_MCP_JOB_HELPER"

// TestMCPJobObjectParentExitTerminatesWorker reproduces issue #1190 without
// relying on stdio EOF: the worker keeps running after its immediate parent
// exits unless the worker's lifetime is retained by a parent-owned job handle.
func TestMCPJobObjectParentExitTerminatesWorker(t *testing.T) {
	if helper := os.Getenv(mcpJobHelperEnv); helper != "" {
		runMCPJobHelper(t, helper)
		return
	}

	readyPath := filepath.Join(t.TempDir(), "worker-ready")
	wrapper := exec.Command(os.Args[0], "-test.run=^TestMCPJobObjectParentExitTerminatesWorker$")
	wrapper.Env = append(os.Environ(), mcpJobHelperEnv+"=wrapper", "ENGRAM_MCP_JOB_READY="+readyPath)
	wrapper.WaitDelay = time.Second
	output, err := wrapper.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper failed: %v\n%s", err, output)
	}

	workerPID, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 32)
	if err != nil {
		t.Fatalf("wrapper returned worker PID %q: %v", output, err)
	}
	worker, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(workerPID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		// The job can kill the worker before this test opens its handle, which
		// is the same required outcome.
		return
	}
	if err != nil {
		t.Fatalf("open worker process %d: %v", workerPID, err)
	}
	defer windows.CloseHandle(worker)

	result, err := windows.WaitForSingleObject(worker, uint32((5*time.Second)/time.Millisecond))
	if err != nil {
		t.Fatalf("wait for worker termination: %v", err)
	}
	if result != windows.WAIT_OBJECT_0 {
		t.Fatalf("worker %d survived after its parent exited (wait result %#x)", workerPID, result)
	}
}

func TestMCPJobObjectSetupDegradesWithoutLeakingChildOwnership(t *testing.T) {
	createFailed := errors.New("create failed")
	configureFailed := errors.New("configure failed")
	permissionDenied := errors.New("access denied")
	pidReused := errors.New("parent was created after this process")
	duplicateFailed := errors.New("duplicate failed")
	assignmentFailed := errors.New("nested jobs are not supported")
	revokeFailed := errors.New("revoke failed")
	parentRecheckFailed := errors.New("parent wait failed")

	tests := []struct {
		name                    string
		fixture                 mcpJobFixture
		wantError               string
		wantAssignmentAttempted bool
		wantAssignmentSucceeded bool
		wantChildJobClosed      bool
		wantParentProcessClosed bool
		wantParentCopyRetained  bool
		wantParentCopyRevoked   bool
	}{
		{
			name:      "create job failure leaves no owned handles",
			fixture:   mcpJobFixture{createJobErr: createFailed},
			wantError: "create MCP lifetime job",
		},
		{
			name:               "configure job failure closes only child handle",
			fixture:            mcpJobFixture{configureErr: configureFailed},
			wantError:          "configure MCP lifetime job",
			wantChildJobClosed: true,
		},
		{
			name:                    "parent open permission denied",
			fixture:                 mcpJobFixture{openParentErr: permissionDenied},
			wantError:               "open MCP parent process",
			wantChildJobClosed:      true,
			wantParentProcessClosed: false,
		},
		{
			name:                    "parent PID was reused",
			fixture:                 mcpJobFixture{parentPredatesErr: pidReused},
			wantError:               "validate MCP parent process",
			wantChildJobClosed:      true,
			wantParentProcessClosed: true,
		},
		{
			name:                    "parent exits before duplicate",
			fixture:                 mcpJobFixture{parentRunning: []bool{false}},
			wantError:               "exited before job assignment",
			wantChildJobClosed:      true,
			wantParentProcessClosed: true,
		},
		{
			name:                    "duplicate into parent fails",
			fixture:                 mcpJobFixture{duplicateErr: duplicateFailed},
			wantError:               "duplicate MCP lifetime job",
			wantChildJobClosed:      true,
			wantParentProcessClosed: true,
		},
		{
			name:                    "assignment failure revokes parent copy",
			fixture:                 mcpJobFixture{assignErr: assignmentFailed},
			wantError:               "assign MCP process",
			wantAssignmentAttempted: true,
			wantChildJobClosed:      true,
			wantParentProcessClosed: true,
			wantParentCopyRevoked:   true,
		},
		{
			name:                    "rollback revoke failure is reported",
			fixture:                 mcpJobFixture{assignErr: assignmentFailed, revokeErr: revokeFailed},
			wantError:               "revoke MCP lifetime job",
			wantAssignmentAttempted: true,
			wantChildJobClosed:      true,
			wantParentProcessClosed: true,
			wantParentCopyRetained:  true,
		},
		{
			name:                    "parent exits after duplication without self termination",
			fixture:                 mcpJobFixture{parentRunning: []bool{true, false}},
			wantError:               "exited during job assignment",
			wantAssignmentAttempted: true,
			wantAssignmentSucceeded: true,
			wantChildJobClosed:      false,
			wantParentProcessClosed: true,
		},
		{
			name:                    "post assignment parent recheck error retains child job handle",
			fixture:                 mcpJobFixture{parentRunning: []bool{true, true}, parentRunningErr: []error{nil, parentRecheckFailed}},
			wantError:               "recheck MCP parent process",
			wantAssignmentAttempted: true,
			wantAssignmentSucceeded: true,
			wantParentProcessClosed: true,
			wantParentCopyRetained:  true,
		},
		{
			name:                    "parent retains the successful job",
			fixture:                 mcpJobFixture{parentRunning: []bool{true, true}},
			wantAssignmentAttempted: true,
			wantAssignmentSucceeded: true,
			wantChildJobClosed:      true,
			wantParentProcessClosed: true,
			wantParentCopyRetained:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := tt.fixture
			stubMCPJobOperations(t, &fixture)

			err := retainMCPProcessUntilParentExit()
			if tt.wantError == "" && err != nil {
				t.Fatalf("setup error = %v", err)
			}
			if tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError)) {
				t.Fatalf("setup error = %v, want containing %q", err, tt.wantError)
			}
			if fixture.assignmentAttempted != tt.wantAssignmentAttempted {
				t.Fatalf("assignment attempted = %t, want %t", fixture.assignmentAttempted, tt.wantAssignmentAttempted)
			}
			if fixture.assignmentSucceeded != tt.wantAssignmentSucceeded {
				t.Fatalf("assignment succeeded = %t, want %t", fixture.assignmentSucceeded, tt.wantAssignmentSucceeded)
			}
			if fixture.childJobClosed != tt.wantChildJobClosed {
				t.Fatalf("child job handle closed = %t, want %t", fixture.childJobClosed, tt.wantChildJobClosed)
			}
			if fixture.parentProcessClosed != tt.wantParentProcessClosed {
				t.Fatalf("parent process handle closed = %t, want %t", fixture.parentProcessClosed, tt.wantParentProcessClosed)
			}
			if fixture.parentCopyRetained != tt.wantParentCopyRetained {
				t.Fatalf("parent job copy retained = %t, want %t", fixture.parentCopyRetained, tt.wantParentCopyRetained)
			}
			if fixture.parentCopyRevoked != tt.wantParentCopyRevoked {
				t.Fatalf("parent job copy revoked = %t, want %t", fixture.parentCopyRevoked, tt.wantParentCopyRevoked)
			}
			if fixture.assignmentAttempted && !fixture.parentCopyExistedBeforeAssignment {
				t.Fatal("job must be duplicated into the parent before assignment")
			}
		})
	}
}

type mcpJobFixture struct {
	createJobErr      error
	configureErr      error
	openParentErr     error
	parentPredatesErr error
	duplicateErr      error
	assignErr         error
	revokeErr         error
	parentRunning     []bool
	parentRunningErr  []error

	parentCopyExistedBeforeAssignment bool
	assignmentAttempted               bool
	assignmentSucceeded               bool
	childJobClosed                    bool
	parentProcessClosed               bool
	parentCopyRetained                bool
	parentCopyRevoked                 bool
}

func stubMCPJobOperations(t *testing.T, fixture *mcpJobFixture) {
	t.Helper()
	old := mcpJobOps
	mcpJobOps = mcpJobOperations{
		currentProcess:  func() windows.Handle { return 1 },
		parentProcessID: func() (uint32, error) { return 42, nil },
		createJob:       func() (windows.Handle, error) { return 2, fixture.createJobErr },
		configureKillOnClose: func(windows.Handle) error {
			return fixture.configureErr
		},
		openParent: func(uint32) (windows.Handle, error) {
			return 3, fixture.openParentErr
		},
		parentPredatesChild: func(windows.Handle, windows.Handle) error {
			return fixture.parentPredatesErr
		},
		parentIsRunning: func(windows.Handle) (bool, error) {
			running := true
			if len(fixture.parentRunning) > 0 {
				running = fixture.parentRunning[0]
				fixture.parentRunning = fixture.parentRunning[1:]
			}
			var runningErr error
			if len(fixture.parentRunningErr) > 0 {
				runningErr = fixture.parentRunningErr[0]
				fixture.parentRunningErr = fixture.parentRunningErr[1:]
			}
			if !running {
				fixture.parentCopyRetained = false
			}
			return running, runningErr
		},
		duplicateToParent: func(windows.Handle, windows.Handle) (windows.Handle, error) {
			if fixture.duplicateErr == nil {
				fixture.parentCopyExistedBeforeAssignment = true
				fixture.parentCopyRetained = true
			}
			return 4, fixture.duplicateErr
		},
		assignCurrent: func(windows.Handle) error {
			fixture.assignmentAttempted = true
			if fixture.assignErr == nil {
				fixture.assignmentSucceeded = true
			}
			return fixture.assignErr
		},
		revokeParentCopy: func(windows.Handle, windows.Handle) error {
			if fixture.revokeErr == nil {
				fixture.parentCopyRetained = false
				fixture.parentCopyRevoked = true
			}
			return fixture.revokeErr
		},
		close: func(handle windows.Handle) error {
			switch handle {
			case 2:
				fixture.childJobClosed = true
			case 3:
				fixture.parentProcessClosed = true
			default:
				t.Fatalf("unexpected handle close: %d", handle)
			}
			return nil
		},
	}
	t.Cleanup(func() { mcpJobOps = old })
}

func runMCPJobHelper(t *testing.T, helper string) {
	t.Helper()
	switch helper {
	case "wrapper":
		readyPath := os.Getenv("ENGRAM_MCP_JOB_READY")
		workerLogPath := filepath.Join(filepath.Dir(readyPath), "worker.log")
		workerLog, err := os.OpenFile(workerLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		worker := exec.Command(os.Args[0], "-test.run=^TestMCPJobObjectParentExitTerminatesWorker$")
		worker.Env = append(os.Environ(), mcpJobHelperEnv+"=worker", "ENGRAM_MCP_JOB_READY="+readyPath)
		worker.Stdout = workerLog
		worker.Stderr = workerLog
		if err := worker.Start(); err != nil {
			_ = workerLog.Close()
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		if err := workerLog.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(4)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(readyPath); err == nil {
				fmt.Fprint(os.Stdout, worker.Process.Pid)
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
		diagnostics, _ := os.ReadFile(workerLogPath)
		fmt.Fprintf(os.Stderr, "worker did not become ready:\n%s", diagnostics)
		os.Exit(5)
	case "worker":
		if err := retainMCPProcessUntilParentExit(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(4)
		}
		if err := os.WriteFile(os.Getenv("ENGRAM_MCP_JOB_READY"), []byte("ready"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(5)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		t.Fatalf("unknown MCP job helper %q", helper)
	}
}
