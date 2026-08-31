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
	if _, err := os.Stat(filepath.Join(sourceDir, ".engram.store.lock")); err != nil {
		t.Fatalf("source lease missing after move: %v", err)
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

func TestMoveDatabaseGenerationRequiresSourceDatabase(t *testing.T) {
	base := t.TempDir()
	sourceDir := filepath.Join(base, "source")
	destinationDir := filepath.Join(base, "destination")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	sourceDB := filepath.Join(sourceDir, "engram.db")
	destinationDB := filepath.Join(destinationDir, "engram.db")
	if err := os.WriteFile(sourceDB+"-wal", []byte("orphaned WAL"), 0o600); err != nil {
		t.Fatalf("write source WAL: %v", err)
	}

	err := MoveDatabaseGeneration(sourceDB, destinationDB)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("move error = %v, want missing source database", err)
	}
	if _, err := os.Stat(sourceDB + "-wal"); err != nil {
		t.Fatalf("source WAL after failed move: %v", err)
	}
	if _, err := os.Stat(destinationDB + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination WAL after failed move: %v", err)
	}
}

func TestMoveDatabaseGenerationPreservesExistingDestination(t *testing.T) {
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
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		t.Fatalf("create destination directory: %v", err)
	}
	if err := os.WriteFile(destinationDB+"-wal", []byte("destination WAL"), 0o600); err != nil {
		t.Fatalf("write destination WAL: %v", err)
	}

	originalRename := renameDatabaseFile
	t.Cleanup(func() { renameDatabaseFile = originalRename })
	renameCalls := 0
	renameDatabaseFile = func(source, destination string) error {
		renameCalls++
		return errors.New("rename must not be called")
	}

	err := MoveDatabaseGeneration(sourceDB, destinationDB)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("move error = %v, want existing destination", err)
	}
	if renameCalls != 0 {
		t.Fatalf("rename calls = %d, want 0", renameCalls)
	}
	content, err := os.ReadFile(destinationDB + "-wal")
	if err != nil {
		t.Fatalf("read destination WAL: %v", err)
	}
	if string(content) != "destination WAL" {
		t.Fatalf("destination WAL content = %q, want preserved content", content)
	}
}

func TestStoreLeasePathAllowsMissingDataDirectory(t *testing.T) {
	parent := t.TempDir()
	dataDir := filepath.Join(parent, "missing")

	lockPath, err := storeLeasePath(dataDir)
	if err != nil {
		t.Fatalf("derive lease path: %v", err)
	}
	if want := filepath.Join(dataDir, ".engram.store.lock"); lockPath != want {
		t.Fatalf("lease path = %q, want %q", lockPath, want)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing data directory after lease derivation: %v", err)
	}
}

func TestStoreLeasePathIsInsideCanonicalDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	canonicalDir, err := canonicalStoreLeaseDir(dataDir)
	if err != nil {
		t.Fatalf("resolve data directory: %v", err)
	}
	lockPath, err := storeLeasePath(dataDir)
	if err != nil {
		t.Fatalf("derive lease path: %v", err)
	}
	if want := filepath.Join(canonicalDir, ".engram.store.lock"); lockPath != want {
		t.Fatalf("lease path = %q, want %q", lockPath, want)
	}
}

func TestMoveDatabaseGenerationCreatesDirectoriesBeforeLeasing(t *testing.T) {
	base := t.TempDir()
	sourceDir := filepath.Join(base, "source")
	destinationDir := filepath.Join(base, "destination")

	err := MoveDatabaseGeneration(filepath.Join(sourceDir, "engram.db"), filepath.Join(destinationDir, "engram.db"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("move error = %v, want missing source database", err)
	}
	for _, dir := range []string{sourceDir, destinationDir} {
		if _, err := os.Stat(filepath.Join(dir, ".engram.store.lock")); err != nil {
			t.Fatalf("lease in %s after directory creation: %v", dir, err)
		}
	}
}

func TestStoreLeasePathPreservesResolutionErrors(t *testing.T) {
	_, err := storeLeasePath(filepath.Join(t.TempDir(), "\x00"))
	if err == nil {
		t.Fatal("derive lease path with invalid path succeeded")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid path error = %v, must not be treated as missing", err)
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
