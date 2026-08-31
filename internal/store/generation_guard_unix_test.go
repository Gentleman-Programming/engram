//go:build unix

package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// This uses a real modernc-backed Store and an open SQLite connection. POSIX
// permits replacing an open sidecar name, so the guard can prove it rejects the
// stale generation before another SQLite read or write reaches that connection.
func TestStoreGenerationGuardRejectsLiveSidecarReplacement(t *testing.T) {
	dir := t.TempDir()
	s, err := New(FallbackConfig(dir))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	disableDatabaseGenerationCheckThrottle(s.generation)
	if err := s.CreateSession("before-replacement", "engram", "/work/engram"); err != nil {
		t.Fatalf("create session before replacement: %v", err)
	}

	shmPath := filepath.Join(dir, "engram.db-shm")
	if _, err := os.Stat(shmPath); err != nil {
		t.Fatalf("SQLite did not create shared-memory sidecar: %v", err)
	}
	replacement := shmPath + ".replacement"
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement sidecar: %v", err)
	}
	if err := os.Rename(replacement, shmPath); err != nil {
		t.Fatalf("replace live shared-memory sidecar: %v", err)
	}

	if _, err := s.GetSession("before-replacement"); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("read after sidecar replacement = %v, want ErrDatabaseGenerationChanged", err)
	}
	if err := s.CreateSession("after-replacement", "engram", "/work/engram"); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("write after sidecar replacement = %v, want ErrDatabaseGenerationChanged", err)
	}
}
