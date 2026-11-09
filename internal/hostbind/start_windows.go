//go:build windows

package hostbind

import (
	"errors"
	"strconv"

	"golang.org/x/sys/windows"
)

// ErrNoProcess reports that the PID does not name a running process.
var ErrNoProcess = errors.New("hostbind: no such process")

// ProcessStart returns the creation time of pid.
func ProcessStart(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
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
	return strconv.FormatInt(creation.Nanoseconds(), 10), nil
}
