//go:build darwin

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinFilesystemAdapterMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := detectFilesystem(missing); !os.IsNotExist(err) {
		t.Fatalf("detectFilesystem(%q) error = %v, want not exist", missing, err)
	}
	if err := checkDataDirectoryFilesystem(missing); err != nil {
		t.Fatalf("missing path must retain ancestor inspection compatibility: %v", err)
	}
}

func TestDarwinFilesystemAdapterClassifiesTypes(t *testing.T) {
	tests := []struct {
		name       string
		filesystem string
		want       filesystemSupport
	}{
		{name: "known local", filesystem: "ext4", want: filesystemLocal},
		{name: "NFS", filesystem: "nfs", want: filesystemRemote},
		{name: "NFS4", filesystem: "nfs4", want: filesystemRemote},
		{name: "SMB", filesystem: "SMBFS", want: filesystemRemote},
		{name: "unknown", filesystem: "mysteryfs", want: filesystemUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDarwinFilesystemType(tt.filesystem).Support; got != tt.want {
				t.Errorf("classifyDarwinFilesystemType(%q) support = %q, want %q", tt.filesystem, got, tt.want)
			}
		})
	}
}
