package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

var ErrDatabaseStoreInUse = errors.New("database store is in use")

var (
	storeLeaseTimeout       = 60 * time.Second
	storeLeaseRetryInterval = 25 * time.Millisecond
	renameDatabaseFile      = os.Rename
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
	return filepath.Join(filepath.Dir(canonicalDir), "."+filepath.Base(canonicalDir)+".engram.store.lock"), nil
}

func canonicalStoreLeaseDir(dataDir string) (string, error) {
	canonicalDir, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return "", fmt.Errorf("resolve store lease directory %s: %w", dataDir, err)
	}
	return filepath.Clean(canonicalDir), nil
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

func releaseStoreLeases(leases []*storeLease) {
	for i := len(leases) - 1; i >= 0; i-- {
		_ = leases[i].Close()
	}
}

func acquireMoveLeases(sourceDir, destinationDir string) ([]*storeLease, error) {
	sourceDir, err := canonicalStoreLeaseDir(sourceDir)
	if err != nil {
		return nil, err
	}
	destinationDir, err = canonicalStoreLeaseDir(destinationDir)
	if err != nil {
		return nil, err
	}
	dirs := []string{sourceDir, destinationDir}
	sort.Strings(dirs)
	if len(dirs) == 2 && dirs[0] == dirs[1] {
		dirs = dirs[:1]
	}
	leases := make([]*storeLease, 0, len(dirs))
	for _, dir := range dirs {
		lease, err := acquireStoreLease(dir, true)
		if err != nil {
			releaseStoreLeases(leases)
			return nil, fmt.Errorf("acquire exclusive store lease for %s: %w", dir, err)
		}
		leases = append(leases, lease)
	}
	return leases, nil
}

type databaseMove struct {
	source      string
	destination string
}

func rollbackDatabaseMoves(moved []databaseMove) error {
	var rollbackErr error
	for i := len(moved) - 1; i >= 0; i-- {
		move := moved[i]
		if err := renameDatabaseFile(move.destination, move.source); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore %s: %w", filepath.Base(move.source), err))
		}
	}
	return rollbackErr
}

func moveDatabaseFailure(moveErr error, moved []databaseMove) error {
	if rollbackErr := rollbackDatabaseMoves(moved); rollbackErr != nil {
		return errors.Join(moveErr, fmt.Errorf("rollback database generation: %w", rollbackErr))
	}
	return moveErr
}

// MoveDatabaseGeneration moves a complete SQLite database generation only when
// no live Store holds either the source or destination directory.
func MoveDatabaseGeneration(sourceDB, destinationDB string) error {
	sourceDir := filepath.Dir(sourceDB)
	destinationDir := filepath.Dir(destinationDB)
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	leases, err := acquireMoveLeases(sourceDir, destinationDir)
	if err != nil {
		return err
	}
	defer releaseStoreLeases(leases)

	moved := make([]databaseMove, 0, 3)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		destination := destinationDB + suffix
		if _, err := os.Stat(destination); err == nil {
			return fmt.Errorf("destination %s already exists: %w", filepath.Base(destination), os.ErrExist)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect destination %s: %w", filepath.Base(destination), err)
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		source := sourceDB + suffix
		if _, err := os.Stat(source); err != nil {
			if suffix != "" && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return moveDatabaseFailure(fmt.Errorf("inspect %s: %w", filepath.Base(source), err), moved)
		}
		destination := destinationDB + suffix
		if err := renameDatabaseFile(source, destination); err != nil {
			moveErr := fmt.Errorf("move %s: %w", filepath.Base(source), err)
			return moveDatabaseFailure(moveErr, moved)
		}
		moved = append(moved, databaseMove{source: source, destination: destination})
	}
	return nil
}
