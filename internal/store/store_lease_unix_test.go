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
