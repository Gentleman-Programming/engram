//go:build windows

package store

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsFilesystemResolverRequestsOpenedName(t *testing.T) {
	dataDir := t.TempDir()
	original := windowsFinalPathNameByHandle
	t.Cleanup(func() { windowsFinalPathNameByHandle = original })
	var calls int
	windowsFinalPathNameByHandle = func(handle windows.Handle, buffer *uint16, size, flags uint32) (uint32, error) {
		if flags != 0x8 { // FILE_NAME_OPENED
			t.Fatalf("GetFinalPathNameByHandle flags = %#x, want FILE_NAME_OPENED", flags)
		}
		calls++
		return original(handle, buffer, size, flags)
	}
	resolved, err := resolveWindowsFinalPath(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 || filepath.VolumeName(resolved) == "" {
		t.Fatalf("resolved path = %q, calls = %d; want local volume", resolved, calls)
	}
}

func TestWindowsFilesystemAdapterRejectsResolvedRemotePath(t *testing.T) {
	dataDir := t.TempDir()
	var gotRoot string
	setWindowsFilesystemAdapter(t,
		func(string) (string, error) { return `\\server\share\data`, nil },
		func(root string) (uint32, error) {
			gotRoot = root
			return windowsDriveRemote, nil
		},
	)

	var rejection *NetworkFilesystemError
	if err := checkDataDirectoryFilesystem(dataDir); !errors.As(err, &rejection) {
		t.Fatalf("checkDataDirectoryFilesystem error = %v, want NetworkFilesystemError", err)
	}
	if gotRoot != `\\server\share\` {
		t.Errorf("drive root = %q, want \\server\\share\\", gotRoot)
	}
}

func TestWindowsFilesystemAdapterAllowsTemporaryDirectory(t *testing.T) {
	dataDir := t.TempDir()
	setWindowsFilesystemAdapter(t, resolveWindowsFinalPath, func(string) (uint32, error) { return windowsDriveFixed, nil })
	if err := checkDataDirectoryFilesystem(dataDir); err != nil {
		t.Fatalf("checkDataDirectoryFilesystem(%q): %v", dataDir, err)
	}
}

func TestWindowsFilesystemAdapterPropagatesResolverError(t *testing.T) {
	want := errors.New("resolve path")
	original := resolveWindowsPath
	resolveWindowsPath = func(string) (string, error) { return "", want }
	t.Cleanup(func() { resolveWindowsPath = original })
	if _, err := detectFilesystem("data"); !errors.Is(err, want) {
		t.Fatalf("detectFilesystem error = %v, want %v", err, want)
	}
}

func setWindowsFilesystemAdapter(t *testing.T, resolve func(string) (string, error), driveType func(string) (uint32, error)) {
	t.Helper()
	originalResolve := resolveWindowsPath
	originalDriveType := windowsDriveType
	resolveWindowsPath = resolve
	windowsDriveType = driveType
	t.Cleanup(func() {
		resolveWindowsPath = originalResolve
		windowsDriveType = originalDriveType
	})
}
