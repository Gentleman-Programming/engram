package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StartLockName is the start-lock basename under the engram data dir: an
// empty file whose platform lock (flock / LockFileEx) serializes concurrent
// `engram run -d` starts per data directory.
const StartLockName = "serve.lock"

// ErrStartLockHeld reports that the per-data-dir start lock is held by
// another process: another `engram run -d` is mid-start on the same data
// directory.
var ErrStartLockHeld = errors.New("daemon: another start is in progress on this data directory")

// startLockRetryInterval is the acquisition retry cadence while waiting for
// a concurrent starter to finish.
const startLockRetryInterval = 50 * time.Millisecond

// StartLockPath returns the start-lock location for an engram data dir.
func StartLockPath(dataDir string) string {
	return filepath.Join(dataDir, StartLockName)
}

// StartLock holds the per-data-dir start lock acquired by AcquireStartLock.
// The lock is released by Release and by process death, so a crashed starter
// can never wedge the data directory.
type StartLock struct {
	file *os.File
}

// AcquireStartLock takes the exclusive per-data-dir start lock, waiting up
// to `wait` for a concurrent starter to finish before failing with
// ErrStartLockHeld. The caller must hold it across admission, readiness
// publication, and cleanup so two parents can never overwrite each other's
// pidfile and readiness records.
func AcquireStartLock(dataDir string, wait time.Duration) (*StartLock, error) {
	path := StartLockPath(dataDir)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: open start lock %s: %w", path, err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := tryStartLock(file)
		if err == nil {
			return &StartLock{file: file}, nil
		}
		if !errors.Is(err, ErrStartLockHeld) {
			_ = file.Close()
			return nil, fmt.Errorf("daemon: lock %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("%w (lock file %s); wait for the other start to finish or remove the file if it is stale", ErrStartLockHeld, path)
		}
		time.Sleep(startLockRetryInterval)
	}
}

// Release drops the start lock. It is safe to call more than once.
func (l *StartLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	if err := unlockStartLock(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("daemon: unlock start lock: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("daemon: close start lock: %w", err)
	}
	return nil
}
