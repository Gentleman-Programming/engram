//go:build linux

package store

import "testing"

func TestLinuxFilesystemAdapterRecognizesRemoteMagic(t *testing.T) {
	for _, magic := range []int64{linuxNFSFilesystemMagic, linuxCIFSFilesystemMagic, linuxSMB2FilesystemMagic} {
		if got := classifyLinuxFilesystemMagic(magic).Support; got != filesystemRemote {
			t.Errorf("classifyLinuxFilesystemMagic(%#x) support = %q, want %q", magic, got, filesystemRemote)
		}
	}
}
