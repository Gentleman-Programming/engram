//go:build linux

package store

import "testing"

func TestLinuxFilesystemAdapterClassifiesMagic(t *testing.T) {
	tests := []struct {
		name  string
		magic int64
		want  filesystemSupport
	}{
		{name: "NFS", magic: linuxNFSFilesystemMagic, want: filesystemRemote},
		{name: "CIFS", magic: linuxCIFSFilesystemMagic, want: filesystemRemote},
		{name: "SMB2", magic: linuxSMB2FilesystemMagic, want: filesystemRemote},
		{name: "unknown", magic: 0, want: filesystemUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyLinuxFilesystemMagic(tt.magic).Support; got != tt.want {
				t.Errorf("classifyLinuxFilesystemMagic(%#x) support = %q, want %q", tt.magic, got, tt.want)
			}
		})
	}
}
