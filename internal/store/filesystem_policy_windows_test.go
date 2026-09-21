//go:build windows

package store

import (
	"errors"
	"testing"
)

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
