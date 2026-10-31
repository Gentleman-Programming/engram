package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// usesLocalSQLite mirrors the existing dispatch spelling: main and cmdCloud
// are case-sensitive; only cmdCloudUpgrade normalizes its nested command.
// It scopes the new bootstrap, not existing validation or orphan migration.
func usesLocalSQLite(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "embed", "serve", "mcp", "tui", "search", "save", "delete", "timeline", "conflicts", "doctor", "context", "stats", "export", "import", "sync", "obsidian-export", "projects":
		return true
	case "cloud":
		if len(args) < 2 {
			return false
		}
		switch args[1] {
		case "status", "enroll", "unenroll", "attest-prompt-source":
			return true
		case "upgrade":
			if len(args) >= 3 {
				switch strings.ToLower(strings.TrimSpace(args[2])) {
				case "doctor", "repair", "bootstrap", "remirror", "status", "rollback":
					return true
				}
			}
		}
	}
	return false
}

// prepareSQLiteTemp configures only this CLI process, before a local-SQLite
// command opens its store. Store constructors remain environment-neutral.
func prepareSQLiteTemp(dataDir string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if err := store.ValidateDataDirectory(dataDir); err != nil {
		return fmt.Errorf("prepare SQLite temp directory: %s", strings.TrimPrefix(err.Error(), "engram: "))
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
