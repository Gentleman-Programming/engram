//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package store

import (
	"errors"
	"os"
	"syscall"
)

// tryLockMigrationFile attempts a non-blocking exclusive flock(2) on f.
// It reports (false, nil) when another process (or file description) holds
// the lock, so the caller can retry with backoff.
func tryLockMigrationFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	// EWOULDBLOCK/EAGAIN: lock is held elsewhere. EINTR: interrupted by a
	// signal. Both are retryable, not failures.
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
		return false, nil
	}
	return false, err
}

// unlockMigrationFile releases the flock taken by tryLockMigrationFile.
func unlockMigrationFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
