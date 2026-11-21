package identityfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const lockedIdentityContents = "complete identity\n"

func TestReadFileWindowsSharingViolation(t *testing.T) {
	t.Run("recovers after release", func(t *testing.T) {
		path, release := lockIdentityFile(t)
		retries := 0
		data, err := readFileWithRetry(path, func(time.Duration) {
			retries++
			release()
		})
		if err != nil || string(data) != lockedIdentityContents || retries != 1 {
			t.Fatalf("read = %q, %v after %d retries, want original contents after one retry", data, err, retries)
		}
	})

	t.Run("exhausts retries while locked", func(t *testing.T) {
		path, _ := lockIdentityFile(t)
		var waits []time.Duration
		data, err := readFileWithRetry(path, func(delay time.Duration) {
			waits = append(waits, delay)
		})
		if data != nil || !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || len(waits) != 4 {
			t.Fatalf("read = %q, %v after waits %v, want sharing violation after four retries", data, err, waits)
		}
	})

	t.Run("returns missing file immediately", func(t *testing.T) {
		data, err := readFileWithRetry(filepath.Join(t.TempDir(), "identity"), func(time.Duration) {
			t.Fatal("missing file should not be retried")
		})
		if data != nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read = %q, %v, want missing file", data, err)
		}
	})
}

func lockIdentityFile(t *testing.T) (path string, release func()) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(path, []byte(lockedIdentityContents), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	release = func() {
		t.Helper()
		if handle == windows.InvalidHandle {
			return
		}
		if err := windows.CloseHandle(handle); err != nil {
			t.Fatal(err)
		}
		handle = windows.InvalidHandle
	}
	t.Cleanup(release)
	return path, release
}
