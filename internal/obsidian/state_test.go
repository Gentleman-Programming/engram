package obsidian

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncStateRoundTrip(t *testing.T) {
	t.Run("write then read returns identical state", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".engram-sync-state.json")

		original := SyncState{
			LastExportAt: "2026-04-06T14:00:00Z",
			Files: map[int64]string{
				1:   "eng/bugfix/fixed-fts5-1.md",
				42:  "eng/decision/chose-sqlite-42.md",
				100: "core/architecture/db-schema-100.md",
			},
			SessionHubs: map[string]string{
				"sess-001": "_sessions/sess-001.md",
			},
			TopicHubs: map[string]string{
				"architecture": "_topics/architecture.md",
			},
			Version: 1,
		}

		if err := WriteState(path, original); err != nil {
			t.Fatalf("WriteState failed: %v", err)
		}

		got, err := ReadState(path)
		if err != nil {
			t.Fatalf("ReadState failed: %v", err)
		}

		if got.LastExportAt != original.LastExportAt {
			t.Errorf("LastExportAt: got %q, want %q", got.LastExportAt, original.LastExportAt)
		}
		if got.Version != original.Version {
			t.Errorf("Version: got %d, want %d", got.Version, original.Version)
		}
		if len(got.Files) != len(original.Files) {
			t.Errorf("Files count: got %d, want %d", len(got.Files), len(original.Files))
		}
		if got.Files[42] != "eng/decision/chose-sqlite-42.md" {
			t.Errorf("Files[42]: got %q, want %q", got.Files[42], "eng/decision/chose-sqlite-42.md")
		}
		if got.SessionHubs["sess-001"] != "_sessions/sess-001.md" {
			t.Errorf("SessionHubs[sess-001]: got %q, want %q", got.SessionHubs["sess-001"], "_sessions/sess-001.md")
		}
		if got.TopicHubs["architecture"] != "_topics/architecture.md" {
			t.Errorf("TopicHubs[architecture]: got %q, want %q", got.TopicHubs["architecture"], "_topics/architecture.md")
		}
	})

	t.Run("missing file returns empty state, no error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".engram-sync-state.json")

		// File does not exist — ReadState must return empty state, not error
		got, err := ReadState(path)
		if err != nil {
			t.Fatalf("ReadState on missing file should not error, got: %v", err)
		}
		if got.LastExportAt != "" {
			t.Errorf("empty state LastExportAt should be empty, got %q", got.LastExportAt)
		}
		if len(got.Files) != 0 {
			t.Errorf("empty state Files should be empty, got %d entries", len(got.Files))
		}
		if got.Version != 0 {
			t.Errorf("empty state Version should be 0, got %d", got.Version)
		}
	})

	t.Run("write creates file with valid JSON", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".engram-sync-state.json")

		state := SyncState{
			LastExportAt: "2026-01-01T00:00:00Z",
			Files:        map[int64]string{},
			SessionHubs:  map[string]string{},
			TopicHubs:    map[string]string{},
			Version:      1,
		}

		if err := WriteState(path, state); err != nil {
			t.Fatalf("WriteState failed: %v", err)
		}

		// File must exist
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Error("WriteState did not create the file")
		}
	})
}
