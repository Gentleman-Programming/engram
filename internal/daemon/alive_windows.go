//go:build windows

package daemon

import (
	"golang.org/x/sys/windows"
)

// stillActive is the Win32 STILL_ACTIVE sentinel (259) returned by
// GetExitCodeProcess for a running process. It is a heuristic: a process can
// legitimately exit with 259, which callers accept as a rare false positive.
const stillActive = 259

// ProcessAlive reports whether a pid belongs to a live process. Windows
// reuses pids aggressively and STILL_ACTIVE is a heuristic exit code, so
// callers must confirm with a health probe before treating the daemon as
// running.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == stillActive
}
