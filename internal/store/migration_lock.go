package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
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
	return acquireDatabaseFileLock(path, "migration", tryLockMigrationFile, unlockMigrationFile)
}

// acquireStoreGenerationLease holds a shared advisory lock for the complete
// Store lifetime. The orphaned-database mover takes the matching exclusive
// lock, so an Engram process never replaces a database generation that this
// process still has open.
func acquireStoreGenerationLease(path string) (func(), error) {
	return acquireDatabaseFileLock(path, "database generation", tryLockSharedDatabaseFile, unlockDatabaseFile)
}

// AcquireDatabaseGenerationMoveLock serializes an Engram-owned database move
// with live Store instances. Callers must defer the returned release function.
func AcquireDatabaseGenerationMoveLock(dataDir string) (func(), error) {
	return acquireDatabaseFileLock(filepath.Join(dataDir, ".generation.lock"), "database generation", tryLockMigrationFile, unlockDatabaseFile)
}

// AcquireDatabaseGenerationMoveLocks serializes a database-generation move with
// Store instances that own either the source or destination generation. Locks are
// acquired in canonical lexical order so independently ordered callers cannot
// deadlock. The returned release function unlocks in reverse acquisition order.
func AcquireDatabaseGenerationMoveLocks(sourceDir, destinationDir string) (func(), error) {
	paths := make([]string, 0, 2)
	for _, dir := range []string{sourceDir, destinationDir} {
		canonical, err := canonicalDatabaseGenerationLockPath(dir)
		if err != nil {
			return nil, err
		}
		duplicate := false
		for _, existing := range paths {
			if sameDatabaseGenerationLockPath(existing, canonical) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			paths = append(paths, canonical)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		return databaseGenerationLockPathSortKey(paths[i]) < databaseGenerationLockPathSortKey(paths[j])
	})

	releases := make([]func(), 0, len(paths))
	for _, path := range paths {
		release, err := AcquireDatabaseGenerationMoveLock(path)
		if err != nil {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			return nil, err
		}
		releases = append(releases, release)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
		})
	}, nil
}

func canonicalDatabaseGenerationLockPath(dataDir string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(dataDir))
	if err != nil {
		return "", fmt.Errorf("canonicalize database generation lock path %q: %w", dataDir, err)
	}
	return abs, nil
}

func sameDatabaseGenerationLockPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func databaseGenerationLockPathSortKey(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func acquireDatabaseFileLock(path, purpose string, tryLock func(*os.File) (bool, error), unlockFile func(*os.File) error) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s lock file %q: %w", purpose, path, err)
	}

	deadline := time.Now().Add(migrationLockTimeout)
	backoff := 10 * time.Millisecond
	for {
		acquired, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s lock file %q: %w", purpose, path, err)
		}
		if acquired {
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = unlockFile(f)
					_ = f.Close()
				})
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf(
				"timed out after %s waiting for %s lock %q — another engram process appears to be holding it; check for a stuck engram process (and terminate it) before retrying",
				migrationLockTimeout, purpose, path,
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
