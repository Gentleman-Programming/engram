//go:build windows

package hostbind

import (
	"errors"
	"math"
	"strconv"

	"golang.org/x/sys/windows"
)

// ErrNoProcess reports that the PID does not name a running process.
var ErrNoProcess = errors.New("hostbind: no such process")

// ProcessStart returns the creation time of a live pid.
func ProcessStart(pid int) (string, error) {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return "", ErrNoProcess
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return "", ErrNoProcess
		}
		return "", err
	}
	defer windows.CloseHandle(handle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", err
	}
	// A handle can keep an exited process object queryable. Check liveness on
	// the very same object so PID reuse cannot split the two observations.
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return "", err
	}
	switch state {
	case windows.WAIT_OBJECT_0:
		return "", ErrNoProcess
	case uint32(windows.WAIT_TIMEOUT):
		return strconv.FormatInt(creation.Nanoseconds(), 10), nil
	default:
		return "", errors.New("hostbind: unexpected process wait result")
	}
}
