//go:build unix

package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveDatabaseGenerationRefusesLiveStoreThroughSymlink(t *testing.T) {
	useShortStoreLeaseTimeout(t)
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	aliasDir := filepath.Join(base, "alias")
	destinationDir := filepath.Join(base, "destination")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("create real directory: %v", err)
	}
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Fatalf("create store alias: %v", err)
	}

	s, err := New(FallbackConfig(realDir))
	if err != nil {
		t.Fatalf("open real store: %v", err)
	}
	defer s.Close()
	realLeasePath, err := storeLeasePath(realDir)
	if err != nil {
		t.Fatalf("derive real lease path: %v", err)
	}
	aliasLeasePath, err := storeLeasePath(aliasDir)
	if err != nil {
		t.Fatalf("derive alias lease path: %v", err)
	}
	if aliasLeasePath != realLeasePath {
		t.Fatalf("alias lease path = %q, want %q", aliasLeasePath, realLeasePath)
	}
	realMissingLeasePath, err := storeLeasePath(filepath.Join(realDir, "missing"))
	if err != nil {
		t.Fatalf("derive missing real lease path: %v", err)
	}
	aliasMissingLeasePath, err := storeLeasePath(filepath.Join(aliasDir, "missing"))
	if err != nil {
		t.Fatalf("derive missing alias lease path: %v", err)
	}
	if aliasMissingLeasePath != realMissingLeasePath {
		t.Fatalf("missing alias lease path = %q, want %q", aliasMissingLeasePath, realMissingLeasePath)
	}

	err = MoveDatabaseGeneration(filepath.Join(aliasDir, "engram.db"), filepath.Join(destinationDir, "engram.db"))
	if !errors.Is(err, ErrDatabaseStoreInUse) {
		t.Fatalf("move through alias error = %v, want ErrDatabaseStoreInUse", err)
	}
}

func TestStoreLeaseUsesWritableDataDirectoryWhenParentIsReadOnly(t *testing.T) {
	parent := t.TempDir()
	dataDir := filepath.Join(parent, "data")
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("make parent read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Errorf("restore parent permissions: %v", err)
		}
	})
	if err := os.WriteFile(filepath.Join(parent, "write-probe"), []byte("probe"), 0o600); err == nil {
		_ = os.Remove(filepath.Join(parent, "write-probe"))
		t.Skip("filesystem does not enforce read-only parent permissions")
	}

	s, err := New(FallbackConfig(dataDir))
	if err != nil {
		t.Fatalf("open store with read-only parent: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".engram.store.lock")); err != nil {
		t.Fatalf("lease inside data directory: %v", err)
	}
}
