package store

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckDataDirectoryFilesystemRejectsKnownRemote(t *testing.T) {
	setFilesystemInspector(t, func(string) (filesystemInfo, error) {
		return filesystemInfo{Type: "NFS", Support: filesystemRemote}, nil
	})

	err := checkDataDirectoryFilesystem(t.TempDir())
	var rejection *NetworkFilesystemError
	if !errors.As(err, &rejection) {
		t.Fatalf("checkDataDirectoryFilesystem error = %v, want NetworkFilesystemError", err)
	}
	if rejection.Filesystem != "NFS" {
		t.Errorf("rejection filesystem = %q, want NFS", rejection.Filesystem)
	}
	if !strings.Contains(err.Error(), "ENGRAM_DATA_DIR") {
		t.Errorf("rejection does not require ENGRAM_DATA_DIR: %v", err)
	}
}

func TestCheckDataDirectoryFilesystemPreservesUnknownCompatibility(t *testing.T) {
	setFilesystemInspector(t, func(string) (filesystemInfo, error) {
		return filesystemInfo{Type: "mysteryfs", Support: filesystemUnknown}, nil
	})

	if err := checkDataDirectoryFilesystem(t.TempDir()); err != nil {
		t.Fatalf("checkDataDirectoryFilesystem unknown filesystem: %v", err)
	}
}

func setFilesystemInspector(t *testing.T, inspector func(string) (filesystemInfo, error)) {
	t.Helper()
	original := filesystemInspector
	filesystemInspector = inspector
	t.Cleanup(func() { filesystemInspector = original })
}
