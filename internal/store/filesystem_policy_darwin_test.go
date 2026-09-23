//go:build darwin

package store

import "testing"

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
