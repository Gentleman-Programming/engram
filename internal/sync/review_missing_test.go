package sync

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestGitSyncMissingReviewDatePreservesReceiver(t *testing.T) {
	for _, reviewed := range []bool{false, true} {
		t.Run(map[bool]string{false: "untouched", true: "reviewed without prior date"}[reviewed], func(t *testing.T) {
			a, b := newTestStore(t), newTestStore(t)
			if err := a.CreateSession("missing-review", "demo", "/tmp/demo"); err != nil {
				t.Fatal(err)
			}
			id, err := a.AddObservation(store.AddObservationParams{SessionID: "missing-review", Type: "manual", Title: "manual", Content: "content", Project: "demo"})
			if err != nil {
				t.Fatal(err)
			}
			obs, err := a.GetObservation(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.CreateSession(obs.SessionID, "demo", "/tmp/demo"); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(map[string]any{"sync_id": obs.SyncID, "session_id": obs.SessionID, "type": "manual", "title": "manual", "content": "content", "project": "demo", "scope": "project", "review_after": "2020-01-01 00:00:00"})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.ApplyPulledMutation(store.LocalChunkTargetKey, store.SyncMutation{Seq: 1, Entity: store.SyncEntityObservation, EntityKey: obs.SyncID, Op: store.SyncOpUpsert, Payload: string(payload)}); err != nil {
				t.Fatal(err)
			}
			if reviewed {
				if err := a.MarkReviewed(id); err != nil {
					t.Fatal(err)
				}
			}
			dir := filepath.Join(t.TempDir(), ".engram")
			if _, err := New(a, dir).Export("a", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := New(b, dir).Import(); err != nil {
				t.Fatal(err)
			}
			got, err := b.GetObservationBySyncID(obs.SyncID)
			if err != nil {
				t.Fatal(err)
			}
			if got.ReviewAfter == nil || *got.ReviewAfter != "2020-01-01 00:00:00" {
				t.Fatalf("missing date erased receiver lifecycle: %v", got.ReviewAfter)
			}
		})
	}
}
