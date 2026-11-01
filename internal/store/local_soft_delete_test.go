package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestLocalSoftDeleteRestoreAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, updated, created string
		restore                bool
	}{
		{"older", "2026-01-01 00:00:00", "", false},
		{"equal", "2026-02-01 00:00:00", "", false},
		{"normalized equal", "2026-02-01T00:00:00Z", "", false},
		{"fractional older", "2026-01-31T23:59:59.999999999Z", "", false},
		{"fractional newer", "2026-02-01T00:00:00.000000001Z", "", true},
		{"newer", "2026-03-01 00:00:00", "", true},
		{"missing", "", "", false},
		{"malformed", "not-a-date", "2026-03-01 00:00:00", false},
		{"legacy older", "", "2026-01-01 00:00:00", false},
		{"legacy newer", "", "2026-03-01 00:00:00", true},
		{"whitespace fallback", "  ", "2026-03-01 00:00:00", true},
		{"invalid deletion time", "2026-03-01 00:00:00", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("S", "p", "/p"); err != nil {
				t.Fatal(err)
			}
			id, err := s.AddObservation(AddObservationParams{SessionID: "S", Project: "p", Scope: "project", Type: "note", Title: "original", Content: "original"})
			if err != nil {
				t.Fatal(err)
			}
			original, err := s.GetObservation(id)
			if err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{"sync_id": original.SyncID, "session_id": "S", "project": "p", "deleted_at": "2026-02-01 00:00:00", "updated_at": "2026-02-01 00:00:00"}
			if tc.name == "invalid deletion time" {
				payload["deleted_at"] = "not-a-date"
			}
			apply := func(chunk, op string) {
				t.Helper()
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.ApplyPulledChunkForDomain("local", chunk, []SyncMutation{{Entity: SyncEntityObservation, EntityKey: original.SyncID, Op: op, Payload: string(raw)}}, false); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func() (*Observation, error) {
				var o Observation
				err := scanObservationRow(s.db.QueryRow(`SELECT `+observationSelectColumns+` FROM observations WHERE sync_id = ?`, original.SyncID), &o)
				return &o, err
			}
			apply("delete", SyncOpDelete)
			before, err := snapshot()
			if err != nil {
				t.Fatal(err)
			}
			payload = map[string]any{"sync_id": original.SyncID, "session_id": "S", "project": "p", "type": "note", "title": "restored", "content": "restored", "updated_at": tc.updated, "created_at": tc.created}
			apply("upsert", SyncOpUpsert)
			after, err := snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if tc.restore {
				if after.DeletedAt != nil || after.Title != "restored" {
					t.Fatalf("restore rejected: %+v", after)
				}
			} else if !reflect.DeepEqual(before, after) {
				t.Fatalf("stale upsert changed deleted row: before=%+v after=%+v", before, after)
			}
			state, err := s.GetSyncState("local")
			if err != nil || state.LastPulledSeq != 2 {
				t.Fatalf("cursor: %+v %v", state, err)
			}
		})
	}
}

func TestLocalObservationWritesRetainGenerationPrecision(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("S", "p", "/p"); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "S", Project: "p", Type: "note", Title: "original", Content: "original", TopicKey: "precision"})
	if err != nil {
		t.Fatal(err)
	}
	assertPrecision := func(value string) {
		t.Helper()
		if _, err := time.Parse("2006-01-02 15:04:05.000000000", value); err != nil {
			t.Fatalf("generation loses fractional precision: %q: %v", value, err)
		}
	}
	title := "edited"
	updated, err := s.UpdateObservation(id, UpdateObservationParams{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	assertPrecision(updated.UpdatedAt)
	topicID, err := s.AddObservation(AddObservationParams{SessionID: "S", Project: "p", Type: "note", Title: "topic edit", Content: "topic edit", TopicKey: "precision"})
	if err != nil || topicID != id {
		t.Fatalf("topic update id=%d want=%d err=%v", topicID, id, err)
	}
	topic, err := s.GetObservation(topicID)
	if err != nil {
		t.Fatal(err)
	}
	assertPrecision(topic.UpdatedAt)
	if err := s.DeleteObservation(id, false); err != nil {
		t.Fatal(err)
	}
	var deletedAt, deletedUpdatedAt string
	if err := s.db.QueryRow(`SELECT deleted_at, updated_at FROM observations WHERE id = ?`, id).Scan(&deletedAt, &deletedUpdatedAt); err != nil {
		t.Fatal(err)
	}
	assertPrecision(deletedAt)
	assertPrecision(deletedUpdatedAt)
	if deletedAt != deletedUpdatedAt {
		t.Fatalf("delete generations differ: %s/%s", deletedAt, deletedUpdatedAt)
	}
}

func TestCloudSoftDeleteRestoreKeepsSequenceOrdering(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("S", "p", "/p"); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "S", Project: "p", Type: "note", Title: "original", Content: "original"})
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(chunk, op string, payload map[string]any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ApplyPulledChunkForDomain("cloud:p", chunk, []SyncMutation{{Entity: SyncEntityObservation, EntityKey: o.SyncID, Op: op, Payload: string(raw)}}, true); err != nil {
			t.Fatal(err)
		}
	}
	apply("delete", SyncOpDelete, map[string]any{"sync_id": o.SyncID, "session_id": "S", "project": "p", "deleted_at": "2026-02-01 00:00:00"})
	// Cloud's later sequence is authoritative even with an older wall clock.
	apply("restore", SyncOpUpsert, map[string]any{"sync_id": o.SyncID, "session_id": "S", "project": "p", "type": "note", "title": "restored", "content": "restored", "updated_at": "2026-01-01 00:00:00"})
	after, err := s.GetObservation(id)
	if err != nil || after.Title != "restored" {
		t.Fatalf("cloud restore=%+v %v", after, err)
	}
}

func TestDeferredLocalSnapshotCannotRestoreSoftDeletedObservation(t *testing.T) {
	s := newTestStore(t)
	raw := `{"sync_id":"obs-deferred","session_id":"S","project":"p","type":"note","title":"stale","content":"stale","updated_at":"2020-01-01 00:00:00"}`
	if err := s.ApplyPulledChunk(LocalChunkTargetKey, "missing-parent", []SyncMutation{{Entity: SyncEntityObservation, EntityKey: "obs-deferred", Op: SyncOpUpsert, Payload: raw}}); err != nil {
		t.Fatal(err)
	}
	deferred, dead, err := s.CountDeferredAndDead()
	if err != nil || deferred != 1 || dead != 0 {
		t.Fatalf("deferred=%d dead=%d err=%v", deferred, dead, err)
	}
	if err := s.CreateSession("S", "p", "/p"); err != nil {
		t.Fatal(err)
	}
	p := "p"
	if _, err := s.Import(&ExportData{Observations: []Observation{{SyncID: "obs-deferred", SessionID: "S", Project: &p, Type: "note", Title: "current", Content: "current", CreatedAt: "2020-01-01 00:00:00", UpdatedAt: "2020-01-01 00:00:00"}}}); err != nil {
		t.Fatal(err)
	}
	o, err := s.GetObservationBySyncID("obs-deferred")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservation(o.ID, false); err != nil {
		t.Fatal(err)
	}
	result, err := s.ReplayDeferredForScope(LocalChunkTargetKey, "p")
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("replay=%+v %v", result, err)
	}
	observations, err := s.AllObservations("p", "", 100)
	if err != nil || len(observations) != 0 {
		t.Fatalf("stale replay resurrected row: %+v %v", observations, err)
	}
}
