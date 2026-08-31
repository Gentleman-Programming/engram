//go:build unix

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreLifetimesCoordinateThroughSymlink(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	aliasDir := filepath.Join(base, "alias")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("create real directory: %v", err)
	}
	if err := os.Symlink(realDir, aliasDir); err != nil {
		t.Fatalf("create store alias: %v", err)
	}

	first, err := New(FallbackConfig(realDir))
	if err != nil {
		t.Fatalf("open real store: %v", err)
	}
	second, err := New(FallbackConfig(aliasDir))
	if err != nil {
		_ = first.Close()
		t.Fatalf("open aliased store: %v", err)
	}
	if exclusiveStoreLeaseAvailable(t, realDir) {
		_ = second.Close()
		_ = first.Close()
		t.Fatal("exclusive lease acquired while real and aliased stores were live")
	}
	if err := second.Close(); err != nil {
		_ = first.Close()
		t.Fatalf("close aliased store: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close real store: %v", err)
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
