//go:build !windows

package daemon

import (
	"errors"
	"os"
	"syscall"
)

// tryStartLock takes an exclusive non-blocking flock. Locks are per open
// file description, so this conflicts even within one process (two starts
// in one process are serialized exactly like two processes).
func tryStartLock(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrStartLockHeld
	}
	return err
}

// unlockStartLock drops the flock; the file is closed by the caller-side
// Release (closing also drops the lock, unlock keeps the intent explicit).
func unlockStartLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
