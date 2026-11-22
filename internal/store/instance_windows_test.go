package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRepairInstanceIDWithPublishedReaderOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".instance-id")
	if err := os.WriteFile(path, []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := openPublishedInstanceID(dir)
	switch {
	case errors.Is(err, errors.ErrUnsupported):
		if _, ensureErr := EnsureInstanceID(dir); ensureErr != nil {
			t.Fatal(ensureErr)
		}
		t.Skip("the Windows build or volume uses locked identity reads")
	case err != nil:
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()

	id, err := EnsureInstanceID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ReadPublishedInstanceID(dir); err != nil || got != id || id == "" {
		t.Fatalf("repaired identity = %q, %v, want %q", got, err, id)
	}
}
