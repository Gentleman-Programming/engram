package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataDirectoryAdmission(t *testing.T) {
	t.Run("relative path rejected", func(t *testing.T) {
		err := ValidateDataDirectory("relative")
		if err == nil || !strings.Contains(err.Error(), "data directory must be an absolute path") {
			t.Fatalf("unexpected admission: %v", err)
		}
	})
	t.Run("validation does not create directory or change environment", func(t *testing.T) {
		root := t.TempDir()
		data := filepath.Join(root, "new-data")
		t.Setenv("TEMP", root)
		t.Setenv("TMP", root)
		if err := ValidateDataDirectory(data); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(data); !os.IsNotExist(err) {
			t.Fatalf("validation created path: %v", err)
		}
		if os.Getenv("TEMP") != root || os.Getenv("TMP") != root {
			t.Fatal("validation changed process environment")
		}
	})
}
