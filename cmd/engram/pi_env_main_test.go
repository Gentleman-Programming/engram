package main

import (
	"fmt"
	"os"
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
	dir, err := os.MkdirTemp("", "engram-hostbind-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create host binding dir: %v\n", err)
		os.Exit(1)
	}
	hostBindingDataDir = func() string { return dir }
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
