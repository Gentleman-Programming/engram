//go:build windows

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockStoreLease(file *os.File, exclusive, nonBlocking bool) error {
	flags := uint32(0)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if nonBlocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	return windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &windows.Overlapped{})
}

func unlockStoreLease(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}

func isStoreLeaseBusy(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
