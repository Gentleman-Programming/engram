package store

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"testing"
)

// Only the known Windows prerequisite failure skips a symlink-dependent case.
// Other fixture errors must still fail so broken tests are not hidden.
func requireFixtureSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
			t.Skipf("symlink fixture requires Windows symlink privilege: %v", err)
		}
		t.Fatalf("create symlink fixture: %v", err)
	}
}
