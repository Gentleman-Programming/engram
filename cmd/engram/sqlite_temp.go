package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// prepareSQLiteTemp configures only this CLI process, before any normal startup
// path can open SQLite. Store constructors remain environment-neutral.
func prepareSQLiteTemp(dataDir string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if err := store.ValidateDataDirectory(dataDir); err != nil {
		return err
	}
	dir := filepath.Join(dataDir, "tmp", "sqlite")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("engram: create SQLite temporary directory %q: %w", dir, err)
	}
	// Probe a uniquely owned file; never clean the shared directory or SQLite's
	// files, which may belong to another running process.
	f, err := os.CreateTemp(dir, ".engram-write-check-")
	if err != nil {
		return fmt.Errorf("engram: check SQLite temporary directory %q: %w", dir, err)
	}
	closeErr := f.Close()
	removeErr := os.Remove(f.Name())
	if closeErr != nil {
		return fmt.Errorf("engram: close SQLite temporary directory probe: %w", closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("engram: remove SQLite temporary directory probe: %w", removeErr)
	}
	for _, key := range []string{"TEMP", "TMP"} {
		if err := os.Setenv(key, dir); err != nil {
			return fmt.Errorf("engram: set process %s for SQLite temporaries: %w", key, err)
		}
	}
	return nil
}
