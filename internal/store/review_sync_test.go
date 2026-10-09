package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPulledObservationReviewAfter(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        *string
	}{
		{"preserve date", `,"review_after":"2020-07-01 00:00:00"`, reviewSyncString("2020-07-01 00:00:00")},
		{"derive legacy date", "", reviewSyncString("2020-07-01 00:00:00")},
		{"explicit null", `,"review_after":null`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("review-sync", "demo", "/tmp/demo"); err != nil {
				t.Fatal(err)
			}
			payload := `{"sync_id":"review-sync-obs","session_id":"review-sync","type":"decision","title":"decision","content":"content","project":"demo","scope":"project","created_at":"2020-01-01 00:00:00"` + tc.field + `}`
			mutation := SyncMutation{Seq: 1, Entity: SyncEntityObservation, EntityKey: "review-sync-obs", Op: SyncOpUpsert, Payload: payload}
			if err := s.ApplyPulledMutation(LocalChunkTargetKey, mutation); err != nil {
				t.Fatal(err)
			}
			obs, err := s.GetObservationBySyncID(mutation.EntityKey)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(obs.ReviewAfter, tc.want) {
				t.Fatalf("review_after = %v, want %v", obs.ReviewAfter, tc.want)
			}
			due, err := s.ObservationsNeedingReview("demo", 10)
			if err != nil {
				t.Fatal(err)
			}
			if (len(due) == 1) != (tc.want != nil) {
				t.Fatalf("review list = %#v", due)
			}
			// A legacy update must not erase or restart the existing lifecycle.
			mutation.Seq = 2
			mutation.Payload = `{"sync_id":"review-sync-obs","session_id":"review-sync","type":"decision","title":"updated","content":"updated content","project":"demo","scope":"project"}`
			if err := s.ApplyPulledMutation(LocalChunkTargetKey, mutation); err != nil {
				t.Fatal(err)
			}
			obs, err = s.GetObservationBySyncID(mutation.EntityKey)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(obs.ReviewAfter, tc.want) || obs.Title != "updated" {
				t.Fatalf("legacy update = %#v", obs)
			}
		})
	}
}

func reviewSyncString(s string) *string { return &s }

func TestReviewMutationRejectsInvalidDateWithoutChanges(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("review-invalid", "demo", "/tmp/demo"); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "review-invalid", Type: "decision", Title: "original", Content: "original content", Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingSyncMutations(LocalChunkTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"sync_id": before.SyncID, "session_id": before.SessionID, "type": "decision", "title": "changed", "content": "changed", "project": "demo", "review_after": "not-a-date"})
	if err != nil {
		t.Fatal(err)
	}
	err = s.ApplyPulledMutation(LocalChunkTargetKey, SyncMutation{Seq: 999, Entity: SyncEntityObservation, EntityKey: before.SyncID, Op: SyncOpUpsert, Payload: string(payload)})
	if err == nil {
		t.Fatal("invalid review_after accepted")
	}
	after, err := s.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := s.ListPendingSyncMutations(LocalChunkTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pending, remaining) {
		t.Fatal("rejected mutation changed observation or pending mutations")
	}
}
