//go:build windows

package daemon

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryStartLock takes an exclusive byte-range lock with LockFileEx. The lock
// is per file handle, so this conflicts even within one process, and the
// kernel releases it when the owning process dies.
func tryStartLock(file *os.File) error {
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	var overlapped windows.Overlapped
	overlapped.HEvent = event
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0,
		&overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrStartLockHeld
	}
	return err
}

// unlockStartLock drops the byte-range lock; the file is closed by the
// caller-side Release (closing also drops the lock, unlock keeps the intent
// explicit).
func unlockStartLock(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
