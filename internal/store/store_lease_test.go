package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMoveDatabaseGenerationRefusesLiveStore(t *testing.T) {
	useShortStoreLeaseTimeout(t)
	base := t.TempDir()
	sourceDir := filepath.Join(base, "source")
	destinationDir := filepath.Join(base, "destination")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}

	s, err := New(FallbackConfig(sourceDir))
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	sourceDB := filepath.Join(sourceDir, "engram.db")
	destinationDB := filepath.Join(destinationDir, "engram.db")
	if err := MoveDatabaseGeneration(sourceDB, destinationDB); !errors.Is(err, ErrDatabaseStoreInUse) {
		_ = s.Close()
		t.Fatalf("move with live store error = %v, want ErrDatabaseStoreInUse", err)
	}
	if _, err := os.Stat(destinationDB); !errors.Is(err, os.ErrNotExist) {
		_ = s.Close()
		t.Fatalf("destination database exists after refused move: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close source store: %v", err)
	}

	if err := MoveDatabaseGeneration(sourceDB, destinationDB); err != nil {
		t.Fatalf("move after store close: %v", err)
	}
	if _, err := os.Stat(destinationDB); err != nil {
		t.Fatalf("destination database missing after move: %v", err)
	}
	if _, err := os.Stat(sourceDB); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source database still exists after move: %v", err)
	}
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		t.Fatalf("read source directory after move: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("source directory retained entries after move: %v", entries)
	}
}

func TestMoveDatabaseGenerationRollsBackPartialMove(t *testing.T) {
	base := t.TempDir()
	sourceDir := filepath.Join(base, "source")
	destinationDir := filepath.Join(base, "destination")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	sourceDB := filepath.Join(sourceDir, "engram.db")
	destinationDB := filepath.Join(destinationDir, "engram.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(sourceDB+suffix, []byte("source"+suffix), 0o600); err != nil {
			t.Fatalf("write source %q: %v", suffix, err)
		}
	}

	originalRename := renameDatabaseFile
	t.Cleanup(func() { renameDatabaseFile = originalRename })
	failure := errors.New("forced WAL move failure")
	renameDatabaseFile = func(source, destination string) error {
		if source == sourceDB+"-wal" {
			return failure
		}
		return originalRename(source, destination)
	}

	err := MoveDatabaseGeneration(sourceDB, destinationDB)
	if !errors.Is(err, failure) {
		t.Fatalf("move error = %v, want wrapped forced failure", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		content, readErr := os.ReadFile(sourceDB + suffix)
		if readErr != nil {
			t.Fatalf("read restored source %q: %v", suffix, readErr)
		}
		if string(content) != "source"+suffix {
			t.Fatalf("source %q content = %q", suffix, content)
		}
		if _, statErr := os.Stat(destinationDB + suffix); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("destination %q exists after rollback: %v", suffix, statErr)
		}
	}
}

func TestAcquireStoreLeaseTimesOutWithGuidance(t *testing.T) {
	useShortStoreLeaseTimeout(t)

	dir := t.TempDir()
	held, err := acquireStoreLease(dir, true)
	if err != nil {
		t.Fatalf("acquire held lease: %v", err)
	}
	defer held.Close()
	_, err = acquireStoreLease(dir, false)
	if !errors.Is(err, ErrDatabaseStoreInUse) {
		t.Fatalf("acquire error = %v, want ErrDatabaseStoreInUse", err)
	}
	if !strings.Contains(err.Error(), "restart") {
		t.Fatalf("timeout diagnostic = %q, want restart guidance", err)
	}
}

func useShortStoreLeaseTimeout(t *testing.T) {
	t.Helper()
	originalTimeout := storeLeaseTimeout
	originalRetry := storeLeaseRetryInterval
	storeLeaseTimeout = 20 * time.Millisecond
	storeLeaseRetryInterval = time.Millisecond
	t.Cleanup(func() {
		storeLeaseTimeout = originalTimeout
		storeLeaseRetryInterval = originalRetry
	})
}
