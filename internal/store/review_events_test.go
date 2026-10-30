package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReviewClearEventsLegacyMigration(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			_ = s.Close()
		}
	})
	if err := s.CreateSession("migration", "demo", "/tmp/demo"); err != nil {
		t.Fatal(err)
	}
	payload := `{"sync_id":"migration-obs","session_id":"migration","type":"manual","title":"preserved","content":"content","project":"demo","scope":"project","review_after":"2020-01-01 00:00:00"}`
	if err := s.ApplyPulledMutation(LocalChunkTargetKey, SyncMutation{Seq: 1, Entity: SyncEntityObservation, EntityKey: "migration-obs", Op: SyncOpUpsert, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetObservationBySyncID("migration-obs")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fixture, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	// This additive migration's relevant prior schema differs only by this table.
	_, setupErr := fixture.Exec(`DROP TABLE review_clear_events`)
	closeErr := fixture.Close()
	if setupErr != nil || closeErr != nil {
		t.Fatalf("legacy fixture setup: %v; close: %v", setupErr, closeErr)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.GetObservationBySyncID(before.SyncID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed observation: before=%+v after=%+v err=%v", before, after, err)
	}
	if err := s.MarkReviewed(before.ID); err != nil {
		t.Fatal(err)
	}
	cleared, err := s.GetObservation(before.ID)
	if err != nil || cleared.ReviewAfter != nil {
		t.Fatalf("migrated clear: %+v, %v", cleared, err)
	}
	events, err := s.ExportReviewClearEvents()
	if err != nil || len(events) != 1 || events[0].SyncID != before.SyncID || events[0].ID <= 0 || events[0].Key == "" {
		t.Fatalf("migrated event: %+v, %v", events, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := s.ExportReviewClearEvents()
	if err != nil || !reflect.DeepEqual(events, persisted) {
		t.Fatalf("event lost across restart: %+v, %v", persisted, err)
	}
}

func TestReviewClearEventsLocalOwnership(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		t.Run(map[bool]string{false: "unenrolled", true: "cloud ack"}[enrolled], func(t *testing.T) {
			s := newTestStore(t)
			if enrolled {
				if err := s.EnrollProject("demo"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.CreateSession("events", "demo", "/tmp/demo"); err != nil {
				t.Fatal(err)
			}
			id, err := s.AddObservation(AddObservationParams{SessionID: "events", Type: "manual", Title: "manual", Content: "content", Project: "demo"})
			if err != nil {
				t.Fatal(err)
			}
			obs, err := s.GetObservation(id)
			if err != nil {
				t.Fatal(err)
			}
			check := func(want int) {
				t.Helper()
				events, err := s.ExportReviewClearEvents()
				if err != nil || len(events) != want {
					t.Fatalf("events = %+v, %v, want %d", events, err, want)
				}
			}
			apply := func(field string, seq int64) {
				t.Helper()
				payload := `{"sync_id":"` + obs.SyncID + `","session_id":"events","type":"manual","title":"manual","content":"content","project":"demo","scope":"project","review_after":` + field + `}`
				if err := s.ApplyPulledMutation(LocalChunkTargetKey, SyncMutation{Seq: seq, Entity: SyncEntityObservation, EntityKey: obs.SyncID, Op: SyncOpUpsert, Payload: payload}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.MarkReviewed(id); err != nil {
				t.Fatal(err)
			}
			check(0)
			apply(`null`, 1)
			check(0)
			apply(`"2020-01-01 00:00:00"`, 2)
			if err := s.MarkReviewed(id); err != nil {
				t.Fatal(err)
			}
			check(1)
			if enrolled {
				pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
				if err != nil {
					t.Fatal(err)
				}
				var seqs []int64
				for _, mutation := range pending {
					seqs = append(seqs, mutation.Seq)
				}
				if err := s.AckSyncMutationSeqs(DefaultSyncTargetKey, seqs); err != nil {
					t.Fatal(err)
				}
				check(1)
			}
			apply(`"2020-01-01 00:00:00"`, 3)
			check(0)
			if err := s.MarkReviewed(id); err != nil {
				t.Fatal(err)
			}
			check(2)
			events, err := s.ExportReviewClearEvents()
			if err != nil {
				t.Fatal(err)
			}
			if events[0].ID >= events[1].ID || events[0].Key == events[1].Key {
				t.Fatalf("non-distinct events: %+v", events)
			}
			if err := s.DeleteObservation(id, false); err != nil {
				t.Fatal(err)
			}
			check(0)
		})
	}
}
