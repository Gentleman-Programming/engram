package sync

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestReviewAfterSyncRoundTrip(t *testing.T) {
	a, b := newTestStore(t), newTestStore(t)
	if err := a.CreateSession("review-roundtrip", "demo", "/tmp/demo"); err != nil {
		t.Fatal(err)
	}
	id, err := a.AddObservation(store.AddObservationParams{SessionID: "review-roundtrip", Type: "decision", Title: "decision", Content: "shared decision", Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := a.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	// Seed an overdue date through the public pull API so reviewing on B must
	// produce a genuinely different date, even when both syncs run immediately.
	payload, err := json.Marshal(map[string]any{"sync_id": original.SyncID, "session_id": original.SessionID, "type": original.Type, "title": original.Title, "content": original.Content, "project": "demo", "scope": "project", "review_after": "2020-07-01 00:00:00"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyPulledMutation(store.LocalChunkTargetKey, store.SyncMutation{Seq: 1, Entity: store.SyncEntityObservation, EntityKey: original.SyncID, Op: store.SyncOpUpsert, Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}
	original, err = a.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), ".engram")
	if _, err := New(a, dir).Export("a", ""); err != nil {
		t.Fatal(err)
	}
	imported, err := New(b, dir).Import()
	if err != nil {
		t.Fatal(err)
	}
	if imported.ChunksImported != 1 || imported.ObservationsImported != 1 {
		t.Fatalf("import = %+v", imported)
	}
	copy, err := b.GetObservationBySyncID(original.SyncID)
	if err != nil {
		t.Fatal(err)
	}
	if copy.ReviewAfter == nil || original.ReviewAfter == nil || *copy.ReviewAfter != *original.ReviewAfter {
		t.Fatalf("sync lost review date: A=%v B=%v", original.ReviewAfter, copy.ReviewAfter)
	}
	due, err := b.ObservationsNeedingReview("demo", 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("imported review list = %#v, %v", due, err)
	}
	if err := b.MarkReviewed(copy.ID); err != nil {
		t.Fatal(err)
	}
	reviewed, err := b.GetObservation(copy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.ReviewAfter == nil || *reviewed.ReviewAfter == *original.ReviewAfter {
		t.Fatal("review did not change the date")
	}
	due, err = b.ObservationsNeedingReview("demo", 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("reviewed list = %#v, %v", due, err)
	}
	result, err := New(b, dir).Export("b", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.IsEmpty {
		t.Fatal("review reset was not exported")
	}
	if _, err := New(a, dir).Import(); err != nil {
		t.Fatal(err)
	}
	back, err := a.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	if back.ReviewAfter == nil || reviewed.ReviewAfter == nil || *back.ReviewAfter != *reviewed.ReviewAfter {
		t.Fatalf("review reset lost on return sync: A=%v B=%v", back.ReviewAfter, reviewed.ReviewAfter)
	}
	again, err := New(a, dir).Import()
	if err != nil {
		t.Fatal(err)
	}
	if again.ChunksImported != 0 {
		t.Fatalf("reimport = %+v", again)
	}
}
