package sync_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
)

func openProvenanceStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, e := store.New(store.FallbackConfig(dir))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close provenance store: %v", err)
		}
	})
	return s
}
func createProvenanceSession(t *testing.T, s *store.Store) {
	t.Helper()
	if e := s.CreateSession("ses-verify", "proj-verify", t.TempDir()); e != nil {
		t.Fatal(e)
	}
}
func applyProvenanceSyncMutation(t *testing.T, s *store.Store, target string, seq int64, op, key, fields string) {
	t.Helper()
	body := fmt.Sprintf(`{"sync_id":%q,"session_id":"ses-verify","project":"proj-verify",%s}`, key, fields)
	if e := s.ApplyPulledMutation(target, store.SyncMutation{Seq: seq, Entity: store.SyncEntityObservation, EntityKey: key, Project: "proj-verify", Op: op, Payload: body}); e != nil {
		t.Fatal(e)
	}
}
func assertProvenanceSyncObservation(t *testing.T, s *store.Store, key, want string, live bool) store.Observation {
	t.Helper()
	d, e := s.Export()
	if e != nil {
		t.Fatal(e)
	}
	for _, o := range d.Observations {
		if o.SyncID != key {
			continue
		}
		got := ""
		if o.SupersededBySyncID != nil {
			got = *o.SupersededBySyncID
		}
		if got != want || (o.DeletedAt == nil) != live {
			t.Errorf("%s: link=%q live=%v; want link=%q live=%v", key, got, o.DeletedAt == nil, want, live)
		}
		return o
	}
	t.Fatalf("observation %q missing", key)
	return store.Observation{}
}

// Public Export/Import round trip; no legacy client, private seam or SQL.
func TestSyncStructuredProvenanceRoundTrip(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("deleted=%v", deleted), func(t *testing.T) {
			source := openProvenanceStore(t, t.TempDir())
			createProvenanceSession(t, source)
			applyProvenanceSyncMutation(t, source, store.DefaultSyncTargetKey, 1, store.SyncOpUpsert, "obs-roundtrip", `"type":"note","title":"source","content":"source","scope":"project","superseded_by_sync_id":"absent-survivor"`)
			o := assertProvenanceSyncObservation(t, source, "obs-roundtrip", "absent-survivor", true)
			if deleted {
				if e := source.DeleteObservation(o.ID, false); e != nil {
					t.Fatal(e)
				}
			}
			transport := engramsync.NewFileTransport(t.TempDir())
			result, e := engramsync.NewWithTransport(source, transport).Export("verifier", "")
			if e != nil {
				t.Fatal(e)
			}
			if result.IsEmpty {
				t.Fatal("empty export")
			}
			destination := openProvenanceStore(t, t.TempDir())
			createProvenanceSession(t, destination)
			imported, e := engramsync.NewWithTransport(destination, transport).Import()
			if e != nil {
				t.Fatal(e)
			}
			if imported.ChunksImported != 1 {
				t.Fatalf("import result=%+v", imported)
			}
			if deleted {
				applyProvenanceSyncMutation(t, destination, store.DefaultSyncTargetKey, 99, store.SyncOpUpsert, "obs-roundtrip", `"type":"note","title":"restored","content":"source"`)
			}
			assertProvenanceSyncObservation(t, destination, "obs-roundtrip", "absent-survivor", true)
		})
	}
}
func TestSyncStructuredProvenanceObservationArrayImport(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		for _, deleted := range []bool{false, true} {
			t.Run(fmt.Sprintf("cloud=%v/deleted=%v", cloud, deleted), func(t *testing.T) {
				link, project, when := "absent-survivor", "proj-verify", "2026-01-01 00:00:00"
				o := store.Observation{SyncID: "obs-array", SessionID: "ses-verify", Type: "note", Title: "source", Content: "source", Project: &project, Scope: "project", CreatedAt: when, UpdatedAt: when, SupersededBySyncID: &link}
				if deleted {
					o.DeletedAt = &when
				}
				body, e := json.Marshal(engramsync.ChunkData{Observations: []store.Observation{o}})
				if e != nil {
					t.Fatal(e)
				}
				transport := engramsync.NewFileTransport(t.TempDir())
				entry := engramsync.ChunkEntry{ID: "abc12345", Memories: 1}
				if e := transport.WriteChunk(entry.ID, body, entry); e != nil {
					t.Fatal(e)
				}
				if e := transport.WriteManifest(&engramsync.Manifest{Version: 2, Chunks: []engramsync.ChunkEntry{entry}}); e != nil {
					t.Fatal(e)
				}
				destination := openProvenanceStore(t, t.TempDir())
				createProvenanceSession(t, destination)
				sy := engramsync.NewWithTransport(destination, transport)
				if cloud {
					if e := destination.EnrollProject(project); e != nil {
						t.Fatal(e)
					}
					sy = engramsync.NewCloudWithTransport(destination, transport, project)
				}
				if _, e := sy.Import(); e != nil {
					t.Fatal(e)
				}
				if deleted {
					applyProvenanceSyncMutation(t, destination, "cloud:proj-verify", 99, store.SyncOpUpsert, "obs-array", `"type":"note","title":"restored","content":"source"`)
				}
				assertProvenanceSyncObservation(t, destination, "obs-array", link, true)
			})
		}
	}
}
