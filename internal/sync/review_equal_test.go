package sync

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestReviewRestoreAtExactWatermark(t *testing.T) {
	a, b := newTestStore(t), newTestStore(t)
	if err := a.CreateSession("equal-review", "demo", "/tmp/demo"); err != nil {
		t.Fatal(err)
	}
	id, err := a.AddObservation(store.AddObservationParams{SessionID: "equal-review", Type: "decision", Title: "decision", Content: "content", Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	obs, err := a.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	data, err := a.Export()
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	encoded, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	fields["review_after"] = nil
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), ".engram")
	sy := New(a, dir)
	raw, err := json.Marshal(ChunkData{Sessions: data.Sessions, Mutations: []store.SyncMutation{{Entity: store.SyncEntityObservation, EntityKey: obs.SyncID, Op: store.SyncOpUpsert, Payload: string(payload)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := sy.transport.WriteChunk("equal-null", raw, ChunkEntry{ID: "equal-null"}); err != nil {
		t.Fatal(err)
	}
	cutoff := obs.UpdatedAt // Exact persisted string also freezes normalized equality.
	if normalizeTime(cutoff) != normalizeTime(obs.UpdatedAt) {
		t.Fatal("fixture does not freeze exact watermark equality")
	}
	writeManifestFile(t, dir, &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: "equal-null", CreatedAt: cutoff}}})
	if _, err := New(b, dir).Import(); err != nil {
		t.Fatal(err)
	}
	before, err := b.GetObservationBySyncID(obs.SyncID)
	if err != nil || before.ReviewAfter != nil {
		t.Fatalf("null fixture: %+v, %v", before, err)
	}
	if result, err := sy.Export("a", ""); err != nil || result.IsEmpty {
		t.Fatalf("equal-time restore: %+v, %v", result, err)
	}
	if _, err := New(b, dir).Import(); err != nil {
		t.Fatal(err)
	}
	got, err := b.GetObservationBySyncID(obs.SyncID)
	if err != nil || got.ReviewAfter == nil || *got.ReviewAfter != *obs.ReviewAfter {
		t.Fatalf("receiver restore: %+v, %v", got, err)
	}
	if result, err := sy.Export("a", ""); err != nil || !result.IsEmpty {
		t.Fatalf("repeat restore: %+v, %v", result, err)
	}
}
