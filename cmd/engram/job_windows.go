//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// mcpJobOperations isolates the Windows lifetime protocol so its ownership and
// rollback ordering can be tested without depending on process timing.
type mcpJobOperations struct {
	currentProcess       func() windows.Handle
	parentProcessID      func() (uint32, error)
	createJob            func() (windows.Handle, error)
	configureKillOnClose func(windows.Handle) error
	openParent           func(uint32) (windows.Handle, error)
	parentPredatesChild  func(parent, child windows.Handle) error
	parentIsRunning      func(windows.Handle) (bool, error)
	duplicateToParent    func(job, parent windows.Handle) (windows.Handle, error)
	assignCurrent        func(windows.Handle) error
	revokeParentCopy     func(parent, parentJob windows.Handle) error
	close                func(windows.Handle) error
}

var mcpJobOps = mcpJobOperations{
	currentProcess:       windows.CurrentProcess,
	parentProcessID:      mcpParentProcessID,
	createJob:            func() (windows.Handle, error) { return windows.CreateJobObject(nil, nil) },
	configureKillOnClose: configureMCPJobKillOnClose,
	openParent: func(pid uint32) (windows.Handle, error) {
		return windows.OpenProcess(windows.PROCESS_DUP_HANDLE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	},
	parentPredatesChild: mcpJobParentPredatesChild,
	parentIsRunning:     mcpJobParentIsRunning,
	duplicateToParent:   duplicateMCPJobToParent,
	assignCurrent: func(job windows.Handle) error {
		return windows.AssignProcessToJobObject(job, windows.CurrentProcess())
	},
	revokeParentCopy: revokeMCPJobFromParent,
	close:            windows.CloseHandle,
}

// retainMCPProcessUntilParentExit puts the MCP process in a kill-on-close job
// whose only long-lived handle belongs to its immediate parent. It is called at
// the first line of cmdMCP, before store, autosync, or stdio setup.
func retainMCPProcessUntilParentExit() (err error) {
	ops := mcpJobOps
	job, err := ops.createJob()
	if err != nil {
		return fmt.Errorf("create MCP lifetime job: %w", err)
	}
	childOwnsJob := true
	defer func() {
		if !childOwnsJob {
			return
		}
		if closeErr := ops.close(job); closeErr != nil && err == nil {
			err = fmt.Errorf("close child MCP lifetime job handle: %w", closeErr)
		}
	}()

	if err := ops.configureKillOnClose(job); err != nil {
		return fmt.Errorf("configure MCP lifetime job: %w", err)
	}
	parentPID, err := ops.parentProcessID()
	if err != nil {
		return fmt.Errorf("find MCP parent process: %w", err)
	}
	parent, err := ops.openParent(parentPID)
	if err != nil {
		// PROCESS_DUP_HANDLE denial is a compatibility failure: no job has been
		// assigned, and the child-owned handle is closed by the defer above.
		return fmt.Errorf("open MCP parent process %d for handle duplication: %w", parentPID, err)
	}
	defer func() {
		if closeErr := ops.close(parent); closeErr != nil && err == nil {
			err = fmt.Errorf("close MCP parent process handle: %w", closeErr)
		}
	}()

	// Toolhelp's PPID is a snapshot. Confirm the opened process existed before
	// this child to reject the meaningful PID-reuse case. The immediate parent is
	// intentional: wrapper processes own the lifetime even when they are not the
	// original MCP client.
	if err := ops.parentPredatesChild(parent, ops.currentProcess()); err != nil {
		return fmt.Errorf("validate MCP parent process %d: %w", parentPID, err)
	}
	if running, runningErr := ops.parentIsRunning(parent); runningErr != nil {
		return fmt.Errorf("check MCP parent process %d: %w", parentPID, runningErr)
	} else if !running {
		return fmt.Errorf("MCP parent process %d exited before job assignment", parentPID)
	}

	parentJob, err := ops.duplicateToParent(job, parent)
	if err != nil {
		return fmt.Errorf("duplicate MCP lifetime job into parent process %d: %w", parentPID, err)
	}
	parentOwnsJob := false
	defer func() {
		if parentOwnsJob {
			return
		}
		if revokeErr := ops.revokeParentCopy(parent, parentJob); revokeErr != nil {
			if err == nil {
				err = fmt.Errorf("revoke MCP lifetime job from parent process %d: %w", parentPID, revokeErr)
			} else {
				err = fmt.Errorf("%w; revoke MCP lifetime job from parent process %d: %v", err, parentPID, revokeErr)
			}
		}
	}()

	// Duplicating before assignment prevents an assignment failure from leaving
	// the current process as the only kill-on-close holder.
	if err := ops.assignCurrent(job); err != nil {
		return fmt.Errorf("assign MCP process to lifetime job: %w", err)
	}
	parentOwnsJob = true

	// A parent can terminate between the pre-assignment check and here. Keeping
	// the child handle in that narrow race is safer than closing it and allowing
	// KILL_ON_JOB_CLOSE to terminate the MCP process itself. It becomes a normal
	// process-lifetime handle and the caller logs the compatibility fallback.
	if running, runningErr := ops.parentIsRunning(parent); runningErr != nil {
		childOwnsJob = false
		return fmt.Errorf("recheck MCP parent process %d after job assignment: %w", parentPID, runningErr)
	} else if !running {
		childOwnsJob = false
		return fmt.Errorf("MCP parent process %d exited during job assignment", parentPID)
	}
	return nil
}

func mcpParentProcessID() (uint32, error) {
	ppid := windows.Getppid()
	if ppid <= 0 {
		return 0, fmt.Errorf("current process has no parent")
	}
	return uint32(ppid), nil
}

func configureMCPJobKillOnClose(job windows.Handle) error {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	return err
}

func mcpJobParentPredatesChild(parent, child windows.Handle) error {
	var parentCreated, parentExited, parentKernel, parentUser windows.Filetime
	if err := windows.GetProcessTimes(parent, &parentCreated, &parentExited, &parentKernel, &parentUser); err != nil {
		return err
	}
	var childCreated, childExited, childKernel, childUser windows.Filetime
	if err := windows.GetProcessTimes(child, &childCreated, &childExited, &childKernel, &childUser); err != nil {
		return err
	}
	if parentCreated.Nanoseconds() > childCreated.Nanoseconds() {
		return fmt.Errorf("parent was created after this process (possible PID reuse)")
	}
	return nil
}

func mcpJobParentIsRunning(parent windows.Handle) (bool, error) {
	const waitTimeout = 0x00000102
	result, err := windows.WaitForSingleObject(parent, 0)
	if err != nil {
		return false, err
	}
	if result == waitTimeout {
		return true, nil
	}
	if result == windows.WAIT_OBJECT_0 {
		return false, nil
	}
	return false, fmt.Errorf("unexpected wait result %#x", result)
}

func duplicateMCPJobToParent(job, parent windows.Handle) (windows.Handle, error) {
	var parentJob windows.Handle
	err := windows.DuplicateHandle(
		windows.CurrentProcess(), job, parent, &parentJob,
		0, false, windows.DUPLICATE_SAME_ACCESS,
	)
	return parentJob, err
}

func revokeMCPJobFromParent(parent, parentJob windows.Handle) error {
	var localJob windows.Handle
	if err := windows.DuplicateHandle(
		parent, parentJob, windows.CurrentProcess(), &localJob,
		0, false, windows.DUPLICATE_SAME_ACCESS|windows.DUPLICATE_CLOSE_SOURCE,
	); err != nil {
		return err
	}
	return windows.CloseHandle(localJob)
}
