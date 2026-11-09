package store

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// Read back through public APIs so a refusal must preserve observations,
// sessions, counters and pending sync payloads, not just row counts.
type consolidationSnapshot struct {
	data      *ExportData
	mutations []SyncMutation
}

func snapshotConsolidation(t *testing.T, s *Store) consolidationSnapshot {
	t.Helper()
	data, err := s.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	data.ExportedAt = ""
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 1000)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}
	return consolidationSnapshot{data, mutations}
}

func assertConsolidationUnchanged(t *testing.T, s *Store, before consolidationSnapshot) {
	t.Helper()
	if after := snapshotConsolidation(t, s); !reflect.DeepEqual(before, after) {
		t.Fatalf("refusal or rollback changed exported data/counters or pending sync mutations")
	}
}

func TestConsolidateObservations_SourceDriftWithoutCounters(t *testing.T) {
	for _, change := range []string{"project migration", "title repair"} {
		t.Run(change, func(t *testing.T) {
			s, ids, _ := consolidateTestStore(t)
			preview, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			if change == "project migration" {
				if _, err := s.MigrateProject("proj-cons", "proj-renamed"); err != nil {
					t.Fatalf("MigrateProject: %v", err)
				}
			} else {
				// Seed the same counter-preserving edit used by diagnostic title repair.
				if _, err := s.db.Exec(`UPDATE observations SET title = ? WHERE id = ?`, "Repaired source title", ids[1]); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotConsolidation(t, s)
			res, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids, Confirm: true, Fingerprint: preview.Plan.Fingerprint})
			if !errors.Is(err, ErrConsolidateFingerprintMismatch) {
				t.Fatalf("confirm: got %v, want fingerprint mismatch", err)
			}
			if res == nil || res.Plan == nil || res.Survivor != nil || res.Plan.Fingerprint == preview.Plan.Fingerprint {
				t.Fatal("drift must return a fresh plan, without a survivor")
			}
			if change == "project migration" && res.Plan.Project != "proj-renamed" {
				t.Fatalf("fresh project = %q", res.Plan.Project)
			}
			assertConsolidationUnchanged(t, s, before)
		})
	}
}

func TestConsolidateObservations_ConfirmationBindsSurvivor(t *testing.T) {
	for _, field := range []string{"title", "content", "type", "topic_key"} {
		t.Run(field, func(t *testing.T) {
			s, ids, _ := consolidateTestStore(t)
			preview, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
			if err != nil {
				t.Fatalf("preview: %v", err)
			}
			var overrides ConsolidateOverrides
			switch field {
			case "title":
				overrides.Title = "Changed survivor title"
			case "content":
				overrides.Content = "Changed survivor content"
			case "type":
				overrides.Type = "architecture"
			case "topic_key":
				overrides.TopicKey = "architecture/consolidation"
			}
			before := snapshotConsolidation(t, s)
			res, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids, Overrides: overrides, Confirm: true, Fingerprint: preview.Plan.Fingerprint})
			if !errors.Is(err, ErrConsolidateFingerprintMismatch) {
				t.Fatalf("changed %s: got %v, want fingerprint mismatch", field, err)
			}
			if res == nil || res.Plan == nil || res.Survivor != nil || res.Plan.Fingerprint == preview.Plan.Fingerprint {
				t.Fatal("override drift must return a fresh plan, without a survivor")
			}
			assertConsolidationUnchanged(t, s, before)
			// Explicitly reconfirm the replacement preview before any destructive write.
			confirmed, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids, Overrides: overrides, Confirm: true, Fingerprint: res.Plan.Fingerprint})
			if err != nil {
				t.Fatalf("reconfirm: %v", err)
			}
			if confirmed.Survivor == nil || confirmed.Survivor.Title != res.Plan.Survivor.Title || confirmed.Survivor.Content != res.Plan.Survivor.Content || confirmed.Survivor.Type != res.Plan.Survivor.Type || derefString(confirmed.Survivor.TopicKey) != res.Plan.Survivor.TopicKey {
				t.Fatal("reconfirmed survivor differs from replacement preview")
			}
		})
	}
}

func TestConsolidateObservations_RefusesUnresolvedOwnership(t *testing.T) {
	for _, unowned := range []string{"newest", "oldest", "all"} {
		t.Run(unowned, func(t *testing.T) {
			s, ids, _ := consolidateTestStore(t)
			// Legacy databases can contain both blank observation and session ownership.
			if _, err := s.db.Exec(`UPDATE sessions SET project = '' WHERE id = 'ses-cons'`); err != nil {
				t.Fatal(err)
			}
			switch unowned {
			case "newest":
				_, err := s.db.Exec(`UPDATE observations SET project = '' WHERE id = ?`, ids[2])
				if err != nil {
					t.Fatal(err)
				}
			case "oldest":
				_, err := s.db.Exec(`UPDATE observations SET project = '' WHERE id = ?`, ids[0])
				if err != nil {
					t.Fatal(err)
				}
			case "all":
				_, err := s.db.Exec(`UPDATE observations SET project = '' WHERE session_id = 'ses-cons'`)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotConsolidation(t, s)
			for _, confirm := range []bool{false, true} {
				res, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids, Confirm: confirm, Fingerprint: "stale"})
				if !errors.Is(err, ErrProjectRequired) || res != nil {
					t.Fatalf("confirm=%v: result=%+v error=%v, want project required without result", confirm, res, err)
				}
				assertConsolidationUnchanged(t, s, before)
			}
		})
	}
}

func TestConsolidateObservations_ResolvedOwnershipAndIDOrder(t *testing.T) {
	s, ids, _ := consolidateTestStore(t)
	// Missing observation ownership still inherits the existing session project.
	if _, err := s.db.Exec(`UPDATE observations SET project = NULL WHERE id = ?`, ids[2]); err != nil {
		t.Fatal(err)
	}
	preview, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	reversed := []int64{ids[2], ids[1], ids[0]}
	other, err := s.ConsolidateObservations(ConsolidateParams{IDs: reversed})
	if err != nil {
		t.Fatalf("reordered preview: %v", err)
	}
	if preview.Plan.Fingerprint != other.Plan.Fingerprint {
		t.Fatal("input ID order changed the fingerprint")
	}
	res, err := s.ConsolidateObservations(ConsolidateParams{IDs: reversed, Confirm: true, Fingerprint: preview.Plan.Fingerprint})
	if err != nil {
		t.Fatalf("confirm reordered IDs: %v", err)
	}
	if res.Survivor == nil || derefString(res.Survivor.Project) != "proj-cons" || res.Survivor.Title != preview.Plan.Survivor.Title || res.Survivor.Content != preview.Plan.Survivor.Content {
		t.Fatal("confirmation did not preserve the resolved preview")
	}
	for _, id := range ids {
		if _, err := s.GetObservation(id); err == nil {
			t.Fatalf("source %d remains live", id)
		}
	}
}

func TestConsolidateObservations_AtomicRollbackAfterWrites(t *testing.T) {
	for _, seam := range []string{"second source delete", "delete journal"} {
		t.Run(seam, func(t *testing.T) {
			s, ids, _ := consolidateTestStore(t)
			preview, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
			if err != nil {
				t.Fatal(err)
			}
			trigger := fmt.Sprintf(`CREATE TRIGGER fail_consolidation_late BEFORE UPDATE OF deleted_at ON observations WHEN NEW.id = %d AND NEW.deleted_at IS NOT NULL BEGIN SELECT RAISE(ABORT, 'forced late consolidation failure'); END`, ids[1])
			if seam == "delete journal" {
				trigger = `CREATE TRIGGER fail_consolidation_late BEFORE INSERT ON sync_mutations WHEN NEW.op = 'delete' BEGIN SELECT RAISE(ABORT, 'forced late consolidation failure'); END`
			}
			if _, err := s.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			before := snapshotConsolidation(t, s)
			res, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids, Confirm: true, Fingerprint: preview.Plan.Fingerprint})
			if err == nil || res != nil {
				t.Fatalf("late failure: result=%+v error=%v", res, err)
			}
			assertConsolidationUnchanged(t, s, before)
		})
	}
}
