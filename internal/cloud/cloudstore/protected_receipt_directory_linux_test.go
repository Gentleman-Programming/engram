package cloudstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPrivateReceiptDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := openPrivateReceiptDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, filepath.Join(dir, "absent"), "relative"} {
		root, err := openPrivateReceiptDirectory(path)
		if err == nil {
			_ = root.Close()
			t.Errorf("accepted %q", path)
		}
	}
}
