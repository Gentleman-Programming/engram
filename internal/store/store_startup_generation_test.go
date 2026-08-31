package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreStartupDetectsReplacementBeforeFirstDriverUse(t *testing.T) {
	dir := t.TempDir()
	originalOpen := openGuardedDB
	t.Cleanup(func() { openGuardedDB = originalOpen })
	openGuardedDB = func(dbPath string, generation *databaseGeneration) (*sql.DB, error) {
		replacement := dbPath + ".replacement"
		if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
			return nil, err
		}
		if err := os.Remove(dbPath); err != nil {
			return nil, err
		}
		if err := os.Rename(replacement, dbPath); err != nil {
			return nil, err
		}
		return originalOpen(dbPath, generation)
	}

	_, err := New(FallbackConfig(dir))
	if !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("New() error = %v, want ErrDatabaseGenerationChanged", err)
	}
}

func TestStoreStartupCleansUpCreatedDatabaseAfterOpenFailure(t *testing.T) {
	dir := t.TempDir()
	originalOpen := openGuardedDB
	t.Cleanup(func() { openGuardedDB = originalOpen })
	openGuardedDB = func(string, *databaseGeneration) (*sql.DB, error) {
		return nil, errors.New("open failed")
	}

	_, err := New(FallbackConfig(dir))
	if err == nil {
		t.Fatal("New() succeeded after open failure")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "engram.db")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("created database remains after startup failure: %v", statErr)
	}
}
