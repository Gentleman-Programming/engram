package sync

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Only the chunk write fails; manifest reads and real payload reads remain intact.
type recoveryWriteFailure struct{ Transport }

func (recoveryWriteFailure) WriteChunk(string, []byte, ChunkEntry) error {
	return errors.New("recovery write denied")
}

// ExportedAt describes the snapshot request, not a stored source record.
func sameRecoveryRecords(a, b *store.ExportData) bool {
	return a != nil && b != nil && reflect.DeepEqual(a.Sessions, b.Sessions) && reflect.DeepEqual(a.Observations, b.Observations) && reflect.DeepEqual(a.Prompts, b.Prompts) && reflect.DeepEqual(a.Relations, b.Relations) && reflect.DeepEqual(a.PromptTombstones, b.PromptTombstones)
}

func TestLocalExportMissingPayloadRecovery(t *testing.T) {
	for _, prompts := range []bool{false, true} {
		for _, tracked := range []bool{false, true} {
			name := "default"
			if prompts {
				name = "opt-in"
			}
			if tracked {
				name += "/tracked"
			} else {
				name += "/manifest-only"
			}
			t.Run(name, func(t *testing.T) {
				source := newTestStore(t)
				seedStoreForSync(t, source)
				original := New(source, t.TempDir())
				export := func(sy *Syncer) (*SyncResult, error) {
					if prompts {
						return sy.ExportWithOptions("test", "proj-a", GitExportOptions{IncludePrompts: true})
					}
					return sy.Export("test", "proj-a")
				}
				first, err := export(original)
				if err != nil || first.IsEmpty {
					t.Fatalf("initial export: %+v %v", first, err)
				}
				payload, err := original.transport.ReadChunk(first.ChunkID)
				if err != nil {
					t.Fatal(err)
				}
				manifest, err := original.transport.ReadManifest()
				if err != nil {
					t.Fatal(err)
				}
				if !tracked {
					snapshot, err := source.Export()
					if err != nil {
						t.Fatal(err)
					}
					source = newTestStore(t)
					if _, err := source.Import(snapshot); err != nil {
						t.Fatal(err)
					}
				}
				before, err := source.Export()
				if err != nil {
					t.Fatal(err)
				}
				// A new transport contains the existing manifest, but no payload bytes.
				dir := t.TempDir()
				transport := NewFileTransport(dir)
				if err := transport.WriteManifest(manifest); err != nil {
					t.Fatal(err)
				}
				sy := New(source, dir)
				recovered, err := export(sy)
				if err != nil {
					t.Fatal(err)
				}
				if recovered.IsEmpty || recovered.ChunkID != first.ChunkID || recovered.ObservationsExported != 1 || recovered.SessionsExported != 1 || recovered.PromptsExported != first.PromptsExported {
					t.Fatalf("missing current payload must be restored, not skipped: got %+v; initial %+v", recovered, first)
				}
				restored, err := transport.ReadChunk(first.ChunkID)
				if err != nil || !bytes.Equal(payload, restored) {
					t.Fatalf("restored bytes differ: %v", err)
				}
				gotManifest, err := transport.ReadManifest()
				if err != nil || !reflect.DeepEqual(manifest, gotManifest) {
					t.Fatalf("recovery changed/duplicated manifest entries: %v", err)
				}
				after, err := source.Export()
				if err != nil || !sameRecoveryRecords(before, after) {
					t.Fatalf("source records changed: %v", err)
				}
				for i := 0; i < 2; i++ {
					again, err := export(sy)
					if err != nil || !again.IsEmpty || again.ChunkID != "" {
						t.Fatalf("healthy repeat: %+v %v", again, err)
					}
					raw, err := transport.ReadChunk(first.ChunkID)
					if err != nil || !bytes.Equal(raw, payload) {
						t.Fatalf("healthy repeat rewrote payload: %v", err)
					}
				}
				peer := newTestStore(t)
				receiver := New(peer, dir)
				imported, err := receiver.Import()
				if err != nil || imported.ChunksImported != 1 || imported.ObservationsImported != 1 || imported.SessionsImported != 1 || imported.PromptsImported != first.PromptsExported {
					t.Fatalf("fresh peer: %+v %v", imported, err)
				}
				records, err := peer.ExportProject("proj-a")
				if err != nil {
					t.Fatal(err)
				}
				expected, err := source.ExportProject("proj-a")
				if err != nil {
					t.Fatal(err)
				}
				if len(records.Observations) != 1 || records.Observations[0].SyncID != expected.Observations[0].SyncID || records.Observations[0].Content != expected.Observations[0].Content || len(records.Prompts) != first.PromptsExported {
					t.Fatal("fresh peer lost record identity/content or prompt consent")
				}
				if prompts && records.Prompts[0].Content != expected.Prompts[0].Content {
					t.Fatal("shared prompt content changed")
				}
				repeated, err := receiver.Import()
				if err != nil || repeated.ChunksImported != 0 || repeated.ChunksSkipped != 1 {
					t.Fatalf("repeat import: %+v %v", repeated, err)
				}
				tracking, err := source.GetSyncedChunksForTarget(store.LocalChunkTargetKey)
				if err != nil || !tracking[first.ChunkID] {
					t.Fatalf("successful recovery not tracked: %v", err)
				}
				if _, err := source.AddObservation(store.AddObservationParams{SessionID: "s-proj", Type: "decision", Title: "later", Content: "changed content", Project: "proj-a", Scope: "project"}); err != nil {
					t.Fatal(err)
				}
				changed, err := export(sy)
				if err != nil || changed.IsEmpty || changed.ChunkID == first.ChunkID || changed.ObservationsExported != 1 || changed.PromptsExported != 0 {
					t.Fatalf("changed-content export: %+v %v", changed, err)
				}
				updated, err := transport.ReadManifest()
				if err != nil || len(updated.Chunks) != 2 || updated.Chunks[0] != manifest.Chunks[0] || updated.Chunks[1].ID != changed.ChunkID {
					t.Fatalf("manifest duplicated or pruned history: %v", err)
				}
				delta, err := receiver.Import()
				if err != nil || delta.ChunksImported != 1 || delta.ObservationsImported != 1 {
					t.Fatalf("changed-content import: %+v %v", delta, err)
				}
			})
		}
	}
}

func TestLocalExportMissingPayloadRecoveryTrackedWithoutManifest(t *testing.T) {
	source := newTestStore(t)
	seedStoreForSync(t, source)
	original := New(source, t.TempDir())
	first, err := original.Export("test", "proj-a")
	if err != nil || first.IsEmpty {
		t.Fatalf("initial export: %+v %v", first, err)
	}
	payload, err := original.transport.ReadChunk(first.ChunkID)
	if err != nil {
		t.Fatal(err)
	}
	tracking, err := source.GetSyncedChunksForTarget(store.LocalChunkTargetKey)
	if err != nil || !tracking[first.ChunkID] {
		t.Fatalf("fixture must retain local tracking: %v", err)
	}
	before, err := source.Export()
	if err != nil {
		t.Fatal(err)
	}
	// Keep local tracking, but do not copy either the manifest or payload.
	dir := t.TempDir()
	transport := NewFileTransport(dir)
	manifest, err := transport.ReadManifest()
	if err != nil || len(manifest.Chunks) != 0 {
		t.Fatalf("fixture must have an empty manifest: %+v %v", manifest, err)
	}
	if _, err := transport.ReadChunk(first.ChunkID); !errors.Is(err, ErrChunkNotFound) {
		t.Fatalf("fixture must have no payload: %v", err)
	}
	sy := New(source, dir)
	recovered, err := sy.Export("test", "proj-a")
	if err != nil || recovered.IsEmpty || recovered.ChunkID != first.ChunkID || recovered.SessionsExported != 1 || recovered.ObservationsExported != 1 || recovered.PromptsExported != 0 {
		t.Fatalf("tracked payload recovery: %+v %v", recovered, err)
	}
	restored, err := transport.ReadChunk(first.ChunkID)
	if err != nil || !bytes.Equal(payload, restored) {
		t.Fatalf("restored payload differs: %v", err)
	}
	manifest, err = transport.ReadManifest()
	if err != nil || len(manifest.Chunks) != 1 || manifest.Chunks[0].ID != first.ChunkID {
		t.Fatalf("recovery must create exactly one manifest entry: %+v %v", manifest, err)
	}
	again, err := sy.Export("test", "proj-a")
	if err != nil || !again.IsEmpty || again.ChunkID != "" {
		t.Fatalf("healthy repeat: %+v %v", again, err)
	}
	afterManifest, err := transport.ReadManifest()
	if err != nil || !reflect.DeepEqual(manifest, afterManifest) {
		t.Fatalf("repeat changed or duplicated manifest: %v", err)
	}
	afterTracking, err := source.GetSyncedChunksForTarget(store.LocalChunkTargetKey)
	if err != nil || !reflect.DeepEqual(tracking, afterTracking) {
		t.Fatalf("recovery changed existing tracking: %v", err)
	}
	after, err := source.Export()
	if err != nil || !sameRecoveryRecords(before, after) {
		t.Fatalf("recovery changed source records: %v", err)
	}
	peer := newTestStore(t)
	imported, err := New(peer, dir).Import()
	if err != nil || imported.ChunksImported != 1 || imported.SessionsImported != 1 || imported.ObservationsImported != 1 || imported.PromptsImported != 0 {
		t.Fatalf("fresh peer import: %+v %v", imported, err)
	}
	records, err := peer.ExportProject("proj-a")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := source.ExportProject("proj-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Sessions) != 1 || records.Sessions[0].ID != "s-proj" || len(records.Observations) != 1 || records.Observations[0].SyncID != expected.Observations[0].SyncID || records.Observations[0].Content != expected.Observations[0].Content || len(records.Prompts) != 0 {
		t.Fatal("fresh peer lost record identity/content or default prompt consent")
	}
}

func TestLocalExportMissingPayloadRecoveryErrors(t *testing.T) {
	for _, failure := range []string{"write", "corrupt"} {
		t.Run(failure, func(t *testing.T) {
			source := newTestStore(t)
			seedStoreForSync(t, source)
			original := New(source, t.TempDir())
			first, err := original.Export("test", "proj-a")
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := original.transport.ReadManifest()
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := source.Export()
			if err != nil {
				t.Fatal(err)
			}
			// Manifest knowledge must not acknowledge a failed restoration locally.
			source = newTestStore(t)
			if _, err := source.Import(snapshot); err != nil {
				t.Fatal(err)
			}
			before, err := source.Export()
			if err != nil {
				t.Fatal(err)
			}
			tracking, err := source.GetSyncedChunksForTarget(store.LocalChunkTargetKey)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			transport := NewFileTransport(dir)
			if err := transport.WriteManifest(manifest); err != nil {
				t.Fatal(err)
			}
			sy := New(source, dir)
			if failure == "write" {
				sy.transport = recoveryWriteFailure{transport}
			} else {
				if err := os.MkdirAll(filepath.Join(dir, "chunks"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "chunks", first.ChunkID+".jsonl.gz"), []byte("not gzip"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			result, err := sy.Export("test", "proj-a")
			if err == nil || result != nil {
				t.Fatalf("%s must return explicit error without success: %+v %v", failure, result, err)
			}
			if failure == "write" && !strings.Contains(err.Error(), "write chunk: recovery write denied") {
				t.Fatalf("write error wrapper: %v", err)
			}
			afterTracking, err := source.GetSyncedChunksForTarget(store.LocalChunkTargetKey)
			if err != nil || !reflect.DeepEqual(tracking, afterTracking) {
				t.Fatalf("failed export acknowledged tracking: %v", err)
			}
			after, err := source.Export()
			if err != nil || !sameRecoveryRecords(before, after) {
				t.Fatalf("failed export changed source: %v", err)
			}
			gotManifest, err := transport.ReadManifest()
			if err != nil || !reflect.DeepEqual(manifest, gotManifest) {
				t.Fatalf("failed export changed manifest: %v", err)
			}
			if failure == "write" {
				if _, err := transport.ReadChunk(first.ChunkID); !errors.Is(err, ErrChunkNotFound) {
					t.Fatalf("failed write created payload: %v", err)
				}
				sy.transport = transport
				retry, err := sy.Export("test", "proj-a")
				if err != nil || retry.IsEmpty || retry.ChunkID != first.ChunkID {
					t.Fatalf("retry: %+v %v", retry, err)
				}
			} else {
				raw, err := os.ReadFile(filepath.Join(dir, "chunks", first.ChunkID+".jsonl.gz"))
				if err != nil || string(raw) != "not gzip" {
					t.Fatalf("corrupt payload overwritten: %v", err)
				}
			}
		})
	}
}
