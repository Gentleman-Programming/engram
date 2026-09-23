//go:build darwin

package store

import "testing"

func TestDarwinFilesystemAdapterRecognizesRemoteTypes(t *testing.T) {
	for _, filesystem := range []string{"nfs", "smbfs"} {
		if got := classifyDarwinFilesystemType(filesystem).Support; got != filesystemRemote {
			t.Errorf("classifyDarwinFilesystemType(%q) support = %q, want %q", filesystem, got, filesystemRemote)
		}
	}
}
