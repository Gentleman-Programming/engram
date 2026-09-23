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

func TestLinuxFilesystemAdapterRecognizesRemoteMagic(t *testing.T) {
	for _, magic := range []int64{linuxNFSFilesystemMagic, linuxCIFSFilesystemMagic, linuxSMB2FilesystemMagic} {
		if got := classifyLinuxFilesystemMagic(magic).Support; got != filesystemRemote {
			t.Errorf("classifyLinuxFilesystemMagic(%#x) support = %q, want %q", magic, got, filesystemRemote)
		}
	}
}
