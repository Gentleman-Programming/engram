//go:build windows

package daemon

import (
	"math"

	"golang.org/x/sys/windows"
)

// waitTimeout is the Win32 WAIT_TIMEOUT status (258). Declared locally
// because x/sys/windows exports it only as a syscall.Errno (same pattern as
// mcpJobParentIsRunning in cmd/engram).
const waitTimeout = 0x00000102

// ProcessAlive reports whether a pid belongs to a live process. Liveness is
// a wait-based check, not the GetExitCodeProcess STILL_ACTIVE heuristic (a
// process that really exits with code 259 is indistinguishable from a live
// one): the process is opened with SYNCHRONIZE and polled with a zero
// timeout — WAIT_TIMEOUT means alive, WAIT_OBJECT_0 means dead. A wait error
// is indeterminate and fails safe as alive; callers must still confirm
// daemon identity through the health probe before signaling. Pids above the
// Win32 DWORD range cannot be addressed by OpenProcess and are rejected
// before the uint32 conversion.
func ProcessAlive(pid int) bool {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return true // indeterminate: fail safe as alive
	}
	switch event {
	case windows.WAIT_OBJECT_0:
		return false
	case waitTimeout:
		return true
	default:
		return true // unexpected wait status: fail safe as alive
	}
}
