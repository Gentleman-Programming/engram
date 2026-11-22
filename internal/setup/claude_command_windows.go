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

	// The Windows SDK's ProcThreadAttributeValue(13, FALSE, TRUE, FALSE).
	procThreadAttributeJobList = 0x0002000d
)

var claudeIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func claudeOrdinarySetupRunner(managed bool) claudeSetupRunner {
	return func(name string, args ...string) ([]byte, error) {
		timeout := claudeLocalTimeout
		if !managed && len(args) >= 3 && args[0] == "plugin" && args[1] == "marketplace" {
			timeout = claudeRemoteTimeout
		}
		return runClaudeProfileCommand(timeout, name, args...)
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
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return nil, err
	}
	// Assign the job at creation, so Claude cannot spawn before containment.
	if err := attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		return nil, fmt.Errorf("prepare Claude setup job assignment: %w", err)
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
	if err := windows.CreateProcess(app, line, nil, nil, true, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_NO_WINDOW, nil, nil, &startup.StartupInfo, &process); err != nil {
		return nil, fmt.Errorf("start Claude in its setup job: %w", err)
	}
	defer windows.CloseHandle(process.Process)
	windows.CloseHandle(process.Thread)

	runErr := waitClaudeProcess(ctx, process.Process)
	if runErr == nil {
		var status uint32
		if err := windows.GetExitCodeProcess(process.Process, &status); err != nil {
			runErr = err
		} else if status != 0 {
			runErr = fmt.Errorf("exit status %d", status)
		}
	}
	if err := drainClaudeJob(job, process.Process); err != nil {
		return nil, errors.Join(runErr, fmt.Errorf("drain Claude setup job: %w", err))
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return nil, errors.Join(runErr, err)
	}
	out, err := io.ReadAll(io.LimitReader(output, claudeSetupOutputLimit+1))
	if len(out) > claudeSetupOutputLimit {
		return nil, errors.Join(runErr, errors.New("Claude setup output exceeded 4 MiB"))
	}
	return out, errors.Join(runErr, err)
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

func drainClaudeJob(job, root windows.Handle) error {
	stopErr := configureClaudeJob(job, true)
	members, listErr := openClaudeJobMembers(job)
	listErr = errors.Join(stopErr, listErr)
	defer func() {
		for _, process := range members {
			windows.CloseHandle(process)
		}
	}()
	if err := windows.TerminateJobObject(job, 1); err != nil {
		return errors.Join(listErr, err)
	}
	// Accounting can reach zero while process objects are still terminating.
	// Keep handles for the root and known children through their final exit.
	for _, process := range append(members, root) {
		if _, err := windows.WaitForSingleObject(process, windows.INFINITE); err != nil {
			listErr = errors.Join(listErr, err)
		}
	}
	var accounting struct {
		totalUserTime, totalKernelTime, periodUserTime, periodKernelTime int64
		pageFaults, totalProcesses, activeProcesses, terminatedProcesses uint32
	}
	for {
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
			return errors.Join(listErr, err)
		}
		if accounting.activeProcesses == 0 {
			return listErr
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func openClaudeJobMembers(job windows.Handle) ([]windows.Handle, error) {
	// JOBOBJECT_BASIC_PROCESS_ID_LIST has two DWORDs followed by a variable
	// ULONG_PTR array. An aligned uintptr buffer works on either target.
	const headerWords = int(8 / unsafe.Sizeof(uintptr(0)))
	var pids []uintptr
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
		pids = buffer[headerWords : headerWords+int(header[1])]
		break
	}
	var members []windows.Handle
	var openErr error
	for _, pid := range pids {
		process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue // The PID already exited.
		}
		if err != nil {
			openErr = errors.Join(openErr, err)
			continue
		}
		var belongs uint32
		ok, _, err := claudeIsProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&belongs)))
		if ok == 0 || belongs == 0 {
			windows.CloseHandle(process)
			if ok == 0 {
				openErr = errors.Join(openErr, err)
			}
			continue // The PID may have been recycled after the snapshot.
		}
		members = append(members, process)
	}
	return members, openErr
}
