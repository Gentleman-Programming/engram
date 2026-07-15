//go:build unix

package store

import (
	"fmt"
	"os"
	"syscall"
)

// acquireMigrationLock takes an exclusive advisory flock(2) on path, blocking
// until it is granted, and returns a function that releases the lock. It
// serializes whole processes around the migration suite so that the
// destructive check-then-act rebuilds inside migrate() can never run twice
// concurrently against the same database.
//
// The lock file is deliberately left in place after unlock: unlinking it
// would open a race where a third process re-creates the path and flocks a
// different inode, defeating the exclusion.
func acquireMigrationLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open migration lock file %q: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock migration lock file %q: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
