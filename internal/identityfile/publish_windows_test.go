//go:build windows

package identityfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishWindowsDoesNotRequireHardLinksAndConsumesSource(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "temporary"), filepath.Join(dir, "identity")
	if err := os.WriteFile(source, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Publish(source, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source after publication = %v, want consumed", err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "complete" {
		t.Fatalf("destination = %q, %v", data, err)
	}
}

func TestPublishWindowsKeepsSourceWhenWinnerExists(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "temporary"), filepath.Join(dir, "identity")
	os.WriteFile(source, []byte("loser"), 0o600)
	os.WriteFile(destination, []byte("winner"), 0o600)
	if err := Publish(source, destination); !errors.Is(err, fs.ErrExist) || !os.IsExist(err) {
		t.Fatalf("error = %v, want exist", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("loser source must remain for caller cleanup: %v", err)
	}
}
