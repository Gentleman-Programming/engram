package main

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

// Pi exports PI_CODING_AGENT to every process it starts, and the Claude hook
// is a no-op when it is set. Clear it so the hook tests behave the same from a
// Pi shell as from CI. The Pi guard itself is covered in package plugin_test.
// Hook tests also publish host bindings; keep those out of the developer's real
// Engram data directory.
func TestMain(m *testing.M) {
	if err := os.Unsetenv("PI_CODING_AGENT"); err != nil {
		fmt.Fprintf(os.Stderr, "clear PI_CODING_AGENT: %v\n", err)
		os.Exit(1)
	}
	// Test subprocesses must never recover a developer's drive-root database.
	// Migration dispatch assertions install their own seam.
	migrateOrphanedDatabase = func(string) {}

	// Unrelated CLI subprocesses may deliberately have no writable temp root.
	// Retain the suite's temp root before individual tests change their env.
	tempRoot := os.TempDir()
	var dir string
	var once sync.Once
	hostBindingDataDir = func() string {
		once.Do(func() {
			var err error
			dir, err = os.MkdirTemp(tempRoot, "engram-hostbind-test-")
			if err != nil {
				fmt.Fprintf(os.Stderr, "create host binding dir: %v\n", err)
				os.Exit(1)
			}
		})
		return dir
	}
	code := m.Run()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}
