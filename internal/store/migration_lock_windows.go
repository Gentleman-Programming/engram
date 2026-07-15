//go:build windows

package store

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// acquireMigrationLock takes an exclusive LockFileEx lock on path, blocking
// until it is granted, and returns a function that releases the lock. It
// serializes whole processes around the migration suite so that the
// destructive check-then-act rebuilds inside migrate() can never run twice
// concurrently against the same database.
//
// The lock file is deliberately left in place after unlock: deleting it
// would open a race where a third process re-creates the path and locks a
// different file object, defeating the exclusion.
func acquireMigrationLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open migration lock file %q: %w", path, err)
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock migration lock file %q: %w", path, err)
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
		_ = f.Close()
	}, nil
}
