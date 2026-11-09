package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Release the owned handle after observing the real deletion failure, without
// depending on a timer or leaving an executable process behind.
func TestCompatibilityFixtureTransientDeleteDenial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "engram.exe")
	if err := os.WriteFile(path, []byte("owned synthetic executable"), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = windows.CloseHandle(handle)
		}
	})
	calls, sleeps := 0, 0
	cleanupErr := removeCompatibilityFixture(dir, "windows", func(target string) error {
		calls++
		if target != dir {
			t.Fatalf("cleanup escaped owned fixture: %q", target)
		}
		err := os.RemoveAll(target)
		if calls == 1 {
			if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Fatalf("expected native delete denial: %v", err)
			}
			if err := windows.CloseHandle(handle); err != nil {
				t.Fatal(err)
			}
			closed = true
		}
		return err
	}, func(delay time.Duration) {
		sleeps++
		if delay != 50*time.Millisecond {
			t.Fatalf("unexpected retry delay: %v", delay)
		}
	})
	if cleanupErr != nil {
		t.Fatalf("fixture cleanup failed despite transient delete denial: %v", cleanupErr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("fixture directory still exists: %v", err)
	}
	if calls != 2 || sleeps != 1 {
		t.Fatalf("calls=%d sleeps=%d, want 2/1", calls, sleeps)
	}
}
