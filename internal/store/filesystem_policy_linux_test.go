//go:build linux

package store

import "testing"

func TestLinuxFilesystemAdapterRecognizesSigned32BitRemoteMagic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		magic int64
	}{
		{"CIFS", linuxCIFSFilesystemMagic},
		{"SMB2", linuxSMB2FilesystemMagic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded := uint32(tc.magic)
			signed := int64(int32(encoded))
			if got := classifyLinuxFilesystemMagic(signed).Support; got != filesystemRemote {
				t.Errorf("classifyLinuxFilesystemMagic(%#x) support = %q, want %q", signed, got, filesystemRemote)
			}
		})
	}
}

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
