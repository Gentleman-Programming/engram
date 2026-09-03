package store

import (
	"fmt"
	"os"
	"time"
)

// migrationLockTimeout bounds how long a process waits for the migration
// lock. A hung holder must produce a loud, actionable error instead of
// silently blocking every engram process on the machine forever. It is a
// variable (not a constant) so tests can shorten the timeout path.
var migrationLockTimeout = 60 * time.Second

// acquireMigrationLock takes an exclusive advisory lock on path and returns
// a function that releases it. It serializes whole processes around the
// migration suite and the startup repair so that the destructive
// check-then-act rebuilds inside migrate() can never run twice concurrently
// against the same database.
//
// Acquisition is non-blocking with a bounded growing backoff (up to
// migrationLockTimeout total) rather than a blocking lock: a stuck holder
// then surfaces as a clear error naming the lock file instead of a silent
// machine-wide hang.
//
// The lock file is deliberately left in place after unlock: unlinking it
// would open a race where a third process re-creates the path and locks a
// different inode/file object, defeating the exclusion.
func acquireMigrationLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open migration lock file %s: %w", path, err)
	}

	deadline := time.Now().Add(migrationLockTimeout)
	backoff := 10 * time.Millisecond
	for {
		acquired, err := tryLockMigrationFile(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock migration lock file %s: %w", path, err)
		}
		if acquired {
			return func() {
				_ = unlockMigrationFile(f)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf(
				"timed out after %s waiting for migration lock %s — another engram process appears to be holding it; check for a stuck engram process (and terminate it) before retrying",
				migrationLockTimeout, path,
			)
		}
		time.Sleep(backoff)
		// Grow the poll interval but cap it low: healthy holders release the
		// lock within milliseconds (the startup repair fast path is read-only),
		// and every engram subcommand acquires this lock, so an aggressive cap
		// keeps contended cold starts snappy.
		if backoff < 100*time.Millisecond {
			backoff *= 2
			if backoff > 100*time.Millisecond {
				backoff = 100 * time.Millisecond
			}
		}
	}
}
