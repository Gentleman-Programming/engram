package plugin_test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionMarkerCleanupKeepsOriginalDirectory(t *testing.T) {
	requireHookBinaries(t)
	first, second := t.TempDir(), t.TempDir()
	var pathsA, pathsB []string
	unrelated := filepath.Join(first, "unrelated-marker")
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Run("owner", func(t *testing.T) {
		t.Setenv("TMPDIR", first)
		id := newSessionID(t)
		for _, suffix := range []string{"-tools-loaded", "-last-nudge"} {
			a := filepath.Join(first, "engram-claude-"+id+suffix)
			b := filepath.Join(second, "engram-claude-"+id+suffix)
			for _, path := range []string{a, b} {
				if err := os.WriteFile(path, []byte("owned fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			pathsA = append(pathsA, a)
			pathsB = append(pathsB, b)
		}
		// Restore registration precedes newSessionID: its cleanup must observe B.
		if err := os.Setenv("TMPDIR", second); err != nil {
			t.Fatal(err)
		}
	})
	for _, p := range pathsA {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("original marker remains: %v", err)
		}
	}
	for _, p := range append(pathsB, unrelated) {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("unrelated marker removed: %v", err)
		}
	}
	t.Run("new owner uses new directory", func(t *testing.T) {
		t.Setenv("TMPDIR", second)
		id := newSessionID(t)
		p := filepath.Join(second, "engram-claude-"+id+"-tools-loaded")
		if err := os.WriteFile(p, []byte("new owner"), 0600); err != nil {
			t.Fatal(err)
		}
		pathsA = []string{p}
	})
	for _, p := range pathsA {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("new owner marker remains: %v", err)
		}
	}
}
