package store_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// exerciseRepairBackup uses only public Store APIs for records and counters.
func exerciseRepairBackup(t *testing.T, locked, existing bool) (string, string) {
	t.Helper()
	data := t.TempDir()
	s, err := store.New(store.FallbackConfig(data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession("backup-fixture", "backup-project", data); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(store.AddObservationParams{SessionID: "backup-fixture", Project: "backup-project", Scope: "project", Type: "discovery", Title: "Synthetic backup", Content: "Synthetic contents only."})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(data, "backups")
	old := filepath.Join(backupDir, "previous.db")
	var oldMode, directoryMode os.FileMode
	if existing {
		if err := os.Mkdir(backupDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(old, []byte("existing backup fixture"), 0644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(old)
		if err != nil {
			t.Fatal(err)
		}
		oldMode = info.Mode().Perm()
		info, err = os.Stat(backupDir)
		if err != nil {
			t.Fatal(err)
		}
		directoryMode = info.Mode().Perm()
	}
	var backup string
	if locked {
		result, err := s.ReclassifyOrphanedPendingRelations()
		if err != nil {
			t.Fatal(err)
		}
		if result.Reclassified != 0 || result.CandidatesAtApply != 0 || result.OneEndpointMissing != 0 || result.LivePending != 0 {
			t.Fatalf("unexpected relation changes: %+v", result)
		}
		backup = result.BackupPath
	} else {
		backup, err = s.BackupSQLite()
		if err != nil {
			t.Fatal(err)
		}
	}
	if filepath.Dir(backup) != backupDir || backup == old {
		t.Fatalf("unexpected backup destination %q", backup)
	}
	after, err := s.GetObservation(id)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("source observation changed: %+v, %v", after, err)
	}
	afterCounts, err := s.Stats()
	if err != nil || !reflect.DeepEqual(counts, afterCounts) {
		t.Fatalf("source counters changed: %+v, %v", afterCounts, err)
	}
	if existing {
		contents, err := os.ReadFile(old)
		if err != nil || string(contents) != "existing backup fixture" {
			t.Fatalf("existing backup changed: %q, %v", contents, err)
		}
		info, err := os.Stat(old)
		if err != nil || info.Mode().Perm() != oldMode {
			t.Fatalf("existing backup permissions changed: %v", err)
		}
		info, err = os.Stat(backupDir)
		if err != nil || info.Mode().Perm() != directoryMode {
			t.Fatalf("existing directory permissions changed: %v", err)
		}
	}
	// Reopen a copy through Store, rather than inspecting private tables.
	contents, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	restoredDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(restoredDir, "engram.db"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := store.New(store.FallbackConfig(restoredDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	copied, err := restored.GetObservation(id)
	if err != nil || !reflect.DeepEqual(before, copied) {
		t.Fatalf("backup observation differs: %+v, %v", copied, err)
	}
	copiedCounts, err := restored.Stats()
	if err != nil || !reflect.DeepEqual(counts, copiedCounts) {
		t.Fatalf("backup counters differ: %+v, %v", copiedCounts, err)
	}
	return backup, data
}

func TestRepairBackupPreservesData(t *testing.T) {
	for _, locked := range []bool{false, true} {
		name := "ordinary"
		if locked {
			name = "locked_repair"
		}
		t.Run(name, func(t *testing.T) { exerciseRepairBackup(t, locked, false) })
	}
}
