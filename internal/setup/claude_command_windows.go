package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	claudeInventoryTimeout = 10 * time.Second // Local CLI startup and inventory readback.
	claudeLocalTimeout     = 30 * time.Second // Local catalog, plugin cache and MCP writes.
	claudeRemoteTimeout    = 2 * time.Minute  // Ordinary GitHub marketplace clone or refresh.
	claudeSetupOutputLimit = 4 << 20
)

var (
	claudeIsProcessInJob          = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
	assignClaudeSetupJob          = windows.AssignProcessToJobObject
	openClaudeSetupProcess        = windows.OpenProcess
	errClaudeNestedJobUnavailable = errors.New("Windows rejected the nested Claude setup job")
)

func claudeOrdinarySetupRunner(managed bool) claudeSetupRunner {
	return func(name string, args ...string) ([]byte, error) {
		timeout := claudeLocalTimeout
		if !managed && len(args) >= 3 && args[0] == "plugin" && args[1] == "marketplace" {
			timeout = claudeRemoteTimeout
		}
		out, err := runClaudeProfileCommand(timeout, name, args...)
		if !managed && errors.Is(err, errClaudeNestedJobUnavailable) {
			// Preserve legacy ordinary setup in shells that cannot start nested
			// jobs. The containment attempt failed before Claude could run.
			return runCommand(name, args...)
		}
		return out, err
	}
}

func runClaudeProfileProcess(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runClaudeJob(ctx, name, args...)
}

// runClaudeJob returns only after the subprocess and its Windows job have
// drained. Callers can then release the profile mutex and allow another setup.
func runClaudeJob(ctx context.Context, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	app, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{name}, args...)))
	if err != nil {
		return nil, err
	}

	// File redirection gives Claude closed stdin and keeps a child's inherited
	// output handles from keeping Go waiting on a pipe after timeout.
	input, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	output, err := os.CreateTemp("", "engram-claude-setup-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(output.Name())
	defer output.Close()

	handles := [2]windows.Handle{}
	for i, file := range []*os.File{input, output} {
		self := windows.CurrentProcess()
		if err := windows.DuplicateHandle(self, windows.Handle(file.Fd()), self, &handles[i], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return nil, err
		}
		defer windows.CloseHandle(handles[i])
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create Claude setup job: %w", err)
	}
	defer windows.CloseHandle(job)
	if err := configureClaudeJob(job, false); err != nil {
		return nil, fmt.Errorf("configure Claude setup job: %w", err)
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return nil, err
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	startup.ShowWindow = windows.SW_HIDE
	startup.StdInput, startup.StdOutput, startup.StdErr = handles[0], handles[1], handles[1]
	var process windows.ProcessInformation
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED)
	if err := windows.CreateProcess(app, line, nil, nil, true, flags, nil, nil, &startup.StartupInfo, &process); err != nil {
		return nil, fmt.Errorf("start suspended Claude setup command: %w", err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	// The process inherits the Windows environment's normal parent job, if
	// present. Its thread stays suspended until the additional job is set.
	if err := assignClaudeSetupJob(job, process.Process); err != nil {
		if stopErr := stopSuspendedClaude(process.Process); stopErr != nil {
			return nil, errors.Join(err, stopErr)
		}
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			if nested, checkErr := claudeProcessInJob(windows.CurrentProcess(), 0); checkErr == nil && nested {
				return nil, fmt.Errorf("%w: %w", errClaudeNestedJobUnavailable, err)
			}
		}
		return nil, fmt.Errorf("assign suspended Claude command to the setup job: %w", err)
	}
	runErr := ctx.Err()
	if runErr == nil {
		_, runErr = windows.ResumeThread(process.Thread)
	}
	if runErr == nil {
		runErr = waitClaudeProcess(ctx, process.Process)
	}
	if runErr == nil {
		var status uint32
		if err := windows.GetExitCodeProcess(process.Process, &status); err != nil {
			runErr = err
		} else if status != 0 {
			runErr = fmt.Errorf("exit status %d", status)
		}
	}
	drainClaudeJob(job, process)
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return nil, errors.Join(runErr, err)
	}
	out, err := io.ReadAll(io.LimitReader(output, claudeSetupOutputLimit+1))
	if len(out) > claudeSetupOutputLimit {
		return nil, errors.Join(runErr, errors.New("Claude setup output exceeded 4 MiB"))
	}
	return out, errors.Join(runErr, err)
}

func stopSuspendedClaude(process windows.Handle) error {
	if err := windows.TerminateProcess(process, 1); err != nil {
		state, waitErr := windows.WaitForSingleObject(process, 0)
		if waitErr != nil || state != windows.WAIT_OBJECT_0 {
			return errors.Join(err, waitErr)
		}
		return nil
	}
	_, err := windows.WaitForSingleObject(process, windows.INFINITE)
	return err
}

func waitClaudeProcess(ctx context.Context, process windows.Handle) error {
	for {
		state, err := windows.WaitForSingleObject(process, 25)
		if err != nil {
			return err
		}
		if state == windows.WAIT_OBJECT_0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func configureClaudeJob(job windows.Handle, draining bool) error {
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if draining {
		// No living member can start another child during teardown. The
		// creator itself still occupies the only allowed process slot.
		limits.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
		limits.BasicLimitInformation.ActiveProcessLimit = 1
	}
	_, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	return err
}

func drainClaudeJob(job windows.Handle, root windows.ProcessInformation) {
	slow := time.AfterFunc(5*time.Second, func() {
		fmt.Fprintln(os.Stderr, "Windows is still stopping Claude. Setup will hold the profile lock until process cleanup finishes.")
	})
	defer slow.Stop()

	// Preserve the PID census before termination can remove members from it.
	// Keep the profile locked if Windows cannot establish the cleanup state.
	retryClaudeCleanup(func() error { return configureClaudeJob(job, true) })
	var pids []uintptr
	retryClaudeCleanup(func() error {
		var err error
		pids, err = claudeJobProcessIDs(job)
		return err
	})
	type member struct {
		pid     uint32
		process windows.Handle
	}
	var members []member
	for _, pid := range pids {
		if uint32(pid) == root.ProcessId {
			continue
		}
		process, err := openClaudeJobMember(job, uint32(pid))
		if err != nil || process != 0 {
			members = append(members, member{pid: uint32(pid), process: process})
		}
	}
	retryClaudeCleanup(func() error { return windows.TerminateJobObject(job, 1) })
	waitClaudeJobExit(root.Process)
	for _, child := range members {
		if child.process == 0 {
			// If the initial open was inconclusive, retain the PID even
			// when job accounting drops to zero. An exit must be observed
			// or the identity must be confirmed gone before we can unlock.
			retryClaudeCleanup(func() error {
				var err error
				child.process, err = openClaudeJobMember(job, child.pid)
				return err
			})
		}
		if child.process != 0 {
			waitClaudeJobExit(child.process)
			windows.CloseHandle(child.process)
		}
	}

	var accounting struct {
		totalUserTime, totalKernelTime, periodUserTime, periodKernelTime int64
		pageFaults, totalProcesses, activeProcesses, terminatedProcesses uint32
	}
	retryClaudeCleanup(func() error {
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
			return err
		}
		if accounting.activeProcesses != 0 {
			return errors.New("Claude setup job still has active processes")
		}
		return nil
	})
}

func waitClaudeJobExit(process windows.Handle) {
	retryClaudeCleanup(func() error {
		_, err := windows.WaitForSingleObject(process, windows.INFINITE)
		return err
	})
}

func retryClaudeCleanup(step func() error) {
	for step() != nil {
		time.Sleep(10 * time.Millisecond)
	}
}

func claudeJobProcessIDs(job windows.Handle) ([]uintptr, error) {
	// JOBOBJECT_BASIC_PROCESS_ID_LIST has two DWORDs followed by a variable
	// ULONG_PTR array. An aligned uintptr buffer works on either target.
	const headerWords = int(8 / unsafe.Sizeof(uintptr(0)))
	for count := 8; ; count *= 2 {
		buffer := make([]uintptr, headerWords+count)
		err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(&buffer[0])), uint32(uintptr(len(buffer))*unsafe.Sizeof(buffer[0])), nil)
		if errors.Is(err, windows.ERROR_MORE_DATA) {
			continue
		}
		if err != nil {
			return nil, err
		}
		header := (*[2]uint32)(unsafe.Pointer(&buffer[0]))
		if header[1] < header[0] {
			continue // Windows requires a larger buffer for a complete census.
		}
		return buffer[headerWords : headerWords+int(header[1])], nil
	}
}

func openClaudeJobMember(job windows.Handle, pid uint32) (windows.Handle, error) {
	process, err := openClaudeSetupProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		// The census contains valid, nonzero PIDs. This one is now gone.
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	belongs, err := claudeProcessInJob(process, job)
	if err != nil || !belongs {
		windows.CloseHandle(process)
		return 0, err // Confirmed non-member means the original PID was recycled.
	}
	return process, nil
}

func claudeProcessInJob(process, job windows.Handle) (bool, error) {
	var belongs uint32
	ok, _, err := claudeIsProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&belongs)))
	if ok == 0 {
		return false, err
	}
	return belongs != 0, nil
}
