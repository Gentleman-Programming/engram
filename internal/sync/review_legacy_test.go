package sync

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestLegacyChunkReviewDateCompatibility(t *testing.T) {
	for _, field := range []string{"", `,"review_after":"2019-12-01 00:00:00"`} {
		t.Run(field, func(t *testing.T) {
			dst := newTestStore(t)
			dir := filepath.Join(t.TempDir(), ".engram")
			raw := []byte(`{"sessions":[{"id":"legacy-review","project":"demo","directory":"/tmp/demo","started_at":"2019-01-01 00:00:00"}],"observations":[{"sync_id":"legacy-review-obs","session_id":"legacy-review","type":"decision","title":"old decision","content":"old content","project":"demo","scope":"project","created_at":"2019-01-01 00:00:00"` + field + `}]}`)
			if err := New(dst, dir).transport.WriteChunk("legacy-review", raw, ChunkEntry{ID: "legacy-review"}); err != nil {
				t.Fatal(err)
			}
			writeManifestFile(t, dir, &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: "legacy-review", CreatedBy: "old", CreatedAt: "2019-01-01T00:00:00Z"}}})
			result, err := New(dst, dir).Import()
			if err != nil {
				t.Fatal(err)
			}
			if result.ChunksImported != 1 || result.ObservationsImported != 1 {
				t.Fatalf("import result = %+v", result)
			}
			obs, err := dst.GetObservationBySyncID("legacy-review-obs")
			if err != nil {
				t.Fatal(err)
			}
			want := "2019-07-01 00:00:00"
			if field != "" {
				want = "2019-12-01 00:00:00"
			}
			if obs.ReviewAfter == nil || *obs.ReviewAfter != want {
				t.Fatalf("legacy review_after = %v, want %s", obs.ReviewAfter, want)
			}
		})
	}
}

func TestGitSyncClearsReviewDate(t *testing.T) {
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	a, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b := newTestStore(t)
	if err := a.CreateSession("clear-review", "demo", "/tmp/demo"); err != nil {
		t.Fatal(err)
	}
	id, err := a.AddObservation(store.AddObservationParams{SessionID: "clear-review", Type: "manual", Title: "manual", Content: "content", Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	obs, err := a.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"sync_id": obs.SyncID, "session_id": obs.SessionID, "type": "manual", "title": "manual", "content": "content", "scope": "project", "project": "demo", "review_after": "2020-01-01 00:00:00"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApplyPulledMutation(store.LocalChunkTargetKey, store.SyncMutation{Seq: 1, Entity: store.SyncEntityObservation, EntityKey: obs.SyncID, Op: store.SyncOpUpsert, Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), ".engram")
	if _, err := New(a, dir).Export("a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := New(b, dir).Import(); err != nil {
		t.Fatal(err)
	}
	imported, err := b.GetObservationBySyncID(obs.SyncID)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ReviewAfter == nil {
		t.Fatal("initial manual date lost")
	}
	if err := a.MarkReviewed(id); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(a, dir).Export("a", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.IsEmpty {
		t.Fatal("null reset was not exported")
	}
	if _, err := New(b, dir).Import(); err != nil {
		t.Fatal(err)
	}
	imported, err = b.GetObservationBySyncID(obs.SyncID)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ReviewAfter != nil {
		t.Fatalf("review date not cleared: %s", *imported.ReviewAfter)
	}
	if repeat, err := New(a, dir).Export("a", ""); err != nil || !repeat.IsEmpty {
		t.Fatalf("repeat export = %+v, %v", repeat, err)
	}
	// A second clear has its own receipt even when every write is in one second.
	if err := a.ApplyPulledMutation(store.LocalChunkTargetKey, store.SyncMutation{Seq: 2, Entity: store.SyncEntityObservation, EntityKey: obs.SyncID, Op: store.SyncOpUpsert, Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(a, dir).Export("a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := New(b, dir).Import(); err != nil {
		t.Fatal(err)
	}
	if err := a.MarkReviewed(id); err != nil {
		t.Fatal(err)
	}
	if result, err := New(a, dir).Export("a", ""); err != nil || result.IsEmpty {
		t.Fatalf("second clear = %+v, %v", result, err)
	}
	if _, err := New(b, dir).Import(); err != nil {
		t.Fatal(err)
	}
	imported, err = b.GetObservationBySyncID(obs.SyncID)
	if err != nil || imported.ReviewAfter != nil {
		t.Fatalf("second clear not propagated: %+v, %v", imported, err)
	}
}
