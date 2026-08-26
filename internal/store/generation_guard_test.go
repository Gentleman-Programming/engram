package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseGenerationDetectsReplacedSidecar(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatalf("write %s: %v", filepath.Base(path), err)
		}
	}

	generation := newDatabaseGeneration(dbPath)
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}
	replacement := dbPath + "-wal.replacement"
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement WAL: %v", err)
	}
	if err := os.Remove(dbPath + "-wal"); err != nil {
		t.Fatalf("remove original WAL: %v", err)
	}
	if err := os.Rename(replacement, dbPath+"-wal"); err != nil {
		t.Fatalf("replace WAL: %v", err)
	}

	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("generation check error = %v, want ErrDatabaseGenerationChanged", err)
	}
}

// This cross-platform test proves that WAL and SHM are optional at startup and
// become tracked on their first legitimate appearance. It does not model SQLite mmap behavior.
func TestDatabaseGenerationAllowsLateSidecars(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "engram.db")
	if err := os.WriteFile(dbPath, []byte("database"), 0o600); err != nil {
		t.Fatalf("write database: %v", err)
	}
	generation := newDatabaseGeneration(dbPath)
	if err := generation.capture(); err != nil {
		t.Fatalf("capture without sidecars: %v", err)
	}
	if err := generation.check(); err != nil {
		t.Fatalf("check without sidecars: %v", err)
	}
	if err := os.WriteFile(dbPath+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatalf("create WAL: %v", err)
	}
	if err := generation.check(); err != nil {
		t.Fatalf("accept first WAL appearance: %v", err)
	}
	if err := os.WriteFile(dbPath+"-shm", []byte("shm"), 0o600); err != nil {
		t.Fatalf("create SHM: %v", err)
	}
	if err := generation.check(); err != nil {
		t.Fatalf("accept first SHM appearance: %v", err)
	}
}

func TestDatabaseGenerationRejectsSidecarDisappearance(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatalf("write %s: %v", filepath.Base(path), err)
		}
	}
	generation := newDatabaseGeneration(dbPath)
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}
	if err := os.Remove(dbPath + "-shm"); err != nil {
		t.Fatalf("remove SHM: %v", err)
	}
	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("disappearance error = %v, want ErrDatabaseGenerationChanged", err)
	}
	if err := os.WriteFile(dbPath+"-shm", []byte("replacement"), 0o600); err != nil {
		t.Fatalf("recreate SHM: %v", err)
	}
	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("reappearance error = %v, want ErrDatabaseGenerationChanged", err)
	}
}

func TestDatabaseGenerationRejectsDatabaseDisappearance(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "engram.db")
	if err := os.WriteFile(dbPath, []byte("database"), 0o600); err != nil {
		t.Fatalf("write database: %v", err)
	}
	generation := newDatabaseGeneration(dbPath)
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove database: %v", err)
	}

	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("disappearance error = %v, want ErrDatabaseGenerationChanged", err)
	}
	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("sticky disappearance error = %v, want ErrDatabaseGenerationChanged", err)
	}
}

func TestDatabaseGenerationPreservesIdentityReadErrors(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "engram.db")
	if err := os.WriteFile(dbPath, []byte("database"), 0o600); err != nil {
		t.Fatalf("write database: %v", err)
	}
	generation := newDatabaseGeneration(dbPath)
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}
	readErr := errors.New("permission denied")
	original := readDatabaseFileID
	t.Cleanup(func() { readDatabaseFileID = original })
	readDatabaseFileID = func(path string) (databaseFileIdentity, error) {
		if path == dbPath+"-wal" {
			return "", readErr
		}
		return original(path)
	}

	err := generation.check()
	if !errors.Is(err, readErr) {
		t.Fatalf("check error = %v, want wrapped read error", err)
	}
	if errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("check error = %v, must not be a generation replacement", err)
	}
}
