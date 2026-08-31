package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var ErrDatabaseStoreInUse = errors.New("database store is in use")

var (
	storeLeaseTimeout       = 60 * time.Second
	storeLeaseRetryInterval = 25 * time.Millisecond
)

type storeLease struct {
	file *os.File
}

func acquireStoreLease(dataDir string, exclusive bool) (*storeLease, error) {
	lockPath, err := storeLeasePath(dataDir)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open store lease %s: %w", lockPath, err)
	}
	deadline := time.Now().Add(storeLeaseTimeout)
	for {
		err = lockStoreLease(file, exclusive, true)
		if err == nil {
			return &storeLease{file: file}, nil
		}
		if !isStoreLeaseBusy(err) {
			_ = file.Close()
			return nil, fmt.Errorf("lock store lease %s: %w", lockPath, err)
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("%w: timed out waiting for %s; wait for the active Engram process to exit or restart it", ErrDatabaseStoreInUse, lockPath)
		}
		time.Sleep(storeLeaseRetryInterval)
	}
}

func storeLeasePath(dataDir string) (string, error) {
	canonicalDir, err := canonicalStoreLeaseDir(dataDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(canonicalDir, ".engram.store.lock"), nil
}

func canonicalStoreLeaseDir(dataDir string) (string, error) {
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve store lease directory %s: %w", dataDir, err)
	}
	dir = filepath.Clean(dir)
	missing := make([]string, 0)
	for {
		canonicalDir, err := filepath.EvalSymlinks(dir)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				canonicalDir = filepath.Join(canonicalDir, missing[i])
			}
			return filepath.Clean(canonicalDir), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("resolve store lease directory %s: %w", dataDir, err)
		}
		if _, statErr := os.Lstat(dir); statErr == nil {
			return "", fmt.Errorf("resolve store lease directory %s: %w", dataDir, err)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect store lease directory %s: %w", dir, statErr)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("resolve store lease directory %s: %w", dataDir, err)
		}
		missing = append(missing, filepath.Base(dir))
		dir = parent
	}
}

func (l *storeLease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockStoreLease(l.file)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(err, closeErr)
}
