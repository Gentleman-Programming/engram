package store_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestRepairBackupReservationCollisionLeavesRelationPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		data := t.TempDir()
		s, err := store.New(store.FallbackConfig(data))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		before, err := s.SaveRelation(store.SaveRelationParams{SyncID: "rel-0000000000000001", SourceID: "obs-0000000000000001", TargetID: "obs-0000000000000002"})
		if err != nil || before.JudgmentStatus != "pending" {
			t.Fatalf("seed pending relation: %+v, %v", before, err)
		}
		counts, err := s.Stats()
		if err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(data, "backups")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		// The bubble clock cannot race the production helper's time.Now call.
		path := filepath.Join(directory, "engram-repair-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".db")
		// SQLite accepts an existing empty destination, so removing O_EXCL would
		// overwrite this file and allow the relation repair to proceed.
		contents := []byte{}
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
		original, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.ReclassifyOrphanedPendingRelations()
		if !errors.Is(err, os.ErrExist) {
			t.Errorf("reservation error=%v, want os.ErrExist (not a later SQLite error)", err)
		}
		if result != (store.OrphanedPendingRelationReclassificationResult{}) {
			t.Errorf("failed reservation reported applied state: %+v", result)
		}
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, contents) {
			t.Errorf("colliding snapshot changed: got %d bytes, want %d; read error=%v", len(actual), len(contents), err)
		}
		current, err := os.Stat(path)
		if err != nil || !os.SameFile(original, current) || original.Mode() != current.Mode() {
			t.Errorf("colliding snapshot identity or permissions changed: %v", err)
		}
		after, err := s.GetRelation(before.SyncID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Errorf("pending relation changed: %+v, %v", after, err)
		}
		afterCounts, err := s.Stats()
		if err != nil || !reflect.DeepEqual(counts, afterCounts) {
			t.Errorf("source counters changed: %+v, %v", afterCounts, err)
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
			t.Errorf("failed reservation created another backup: %+v, %v", entries, err)
		}
	})
}
