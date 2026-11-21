package project

import (
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const lockedRepositoryBindingJSON = `{"version":1,"id":"00000000000000000000000000000001","project":"project"}`

func TestReadRepositoryBindingFileWindowsSharingViolation(t *testing.T) {
	t.Run("recovers after release", func(t *testing.T) {
		_, path, release := lockRepositoryBindingFile(t)
		retries := 0
		data, err := readRepositoryBindingFileWithRetry(path, func(time.Duration) {
			retries++
			release()
		})
		if err != nil || string(data) != lockedRepositoryBindingJSON || retries != 1 {
			t.Fatalf("read = %q, %v after %d retries, want original contents after one retry", data, err, retries)
		}
	})

	t.Run("exhausts retries while locked", func(t *testing.T) {
		commonDir, path, _ := lockRepositoryBindingFile(t)
		var waits []time.Duration
		data, err := readRepositoryBindingFileWithRetry(path, func(delay time.Duration) {
			waits = append(waits, delay)
		})
		if data != nil || !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || len(waits) != 4 {
			t.Fatalf("read = %q, %v after waits %v, want sharing violation after four retries", data, err, waits)
		}
		if binding, err := readRepositoryBinding(commonDir); binding != (repositoryBinding{}) || !errors.Is(err, ErrRepositoryBinding) {
			t.Fatalf("readRepositoryBinding = %+v, %v, want repository binding error", binding, err)
		}
	})

	t.Run("returns missing file immediately", func(t *testing.T) {
		data, err := readRepositoryBindingFileWithRetry(repositoryBindingPath(t.TempDir()), func(time.Duration) {
			t.Fatal("missing file should not be retried")
		})
		if data != nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read = %q, %v, want missing file", data, err)
		}
	})
}

func lockRepositoryBindingFile(t *testing.T) (commonDir, path string, release func()) {
	t.Helper()
	commonDir = t.TempDir()
	path = repositoryBindingPath(commonDir)
	if err := os.WriteFile(path, []byte(lockedRepositoryBindingJSON), 0o600); err != nil {
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
	return commonDir, path, release
}
