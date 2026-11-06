package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Two independent SQLite stores exchange real file chunks, including a stale
// snapshot exported offline into another sync directory.
func TestOfflineReplicaDeleteReplay(t *testing.T) {
	for _, hard := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("hard=%v/reverse=%v", hard, reverse), func(t *testing.T) {
				a, b := newTestStore(t), newTestStore(t)
				project, otherProject := "proj-a", "proj-b"
				if _, err := a.Import(&store.ExportData{
					Sessions:     []store.Session{{ID: "s-proj", Project: "proj-a", Directory: "/proj-a", StartedAt: "2020-01-01 00:00:00"}, {ID: "s-other", Project: "proj-b", Directory: "/proj-b", StartedAt: "2020-01-01 00:00:00"}},
					Observations: []store.Observation{{SyncID: "obs-old", SessionID: "s-proj", Type: "note", Title: "project observation", Content: "old snapshot", Scope: "project", Project: &project, CreatedAt: "2020-01-01 00:00:00", UpdatedAt: "2020-01-01 00:00:00"}, {SyncID: "obs-foreign", SessionID: "s-other", Type: "note", Title: "foreign", Content: "foreign content", Scope: "project", Project: &otherProject, CreatedAt: "2020-01-01 00:00:00", UpdatedAt: "2020-01-01 00:00:00"}},
				}); err != nil {
					t.Fatal(err)
				}
				dir := t.TempDir()
				sa, sb := New(a, dir), New(b, dir)
				if _, err := sa.Export("A", "proj-a"); err != nil {
					t.Fatal(err)
				}
				if _, err := sb.Import(); err != nil {
					t.Fatal(err)
				}
				// B remains offline while A removes the observation.
				// Local export's time cutoff has second precision; ensure the
				// deletion is exported separately from the initial snapshot.
				time.Sleep(1100 * time.Millisecond)
				original, err := a.AllObservations("proj-a", "project", 100)
				if err != nil || len(original) != 1 {
					t.Fatalf("seed: %v %v", original, err)
				}
				if err := a.DeleteObservation(original[0].ID, hard); err != nil {
					t.Fatal(err)
				}
				if result, err := sa.Export("A", "proj-a"); err != nil || result.IsEmpty {
					t.Fatal(err)
				}
				// An offline new write must survive reconciliation; it must not revive the old one.
				if _, err := b.AddObservation(store.AddObservationParams{SessionID: "s-proj", Project: "proj-a", Scope: "project", Type: "note", Title: "offline new", Content: "independent offline write"}); err != nil {
					t.Fatal(err)
				}
				offlineDir := t.TempDir()
				offline := New(b, offlineDir)
				if _, err := offline.Export("B", "proj-a"); err != nil {
					t.Fatal(err)
				}
				// Merge actual offline exports, as independent Git branches would.
				m, err := sa.readManifest()
				if err != nil {
					t.Fatal(err)
				}
				om, err := offline.readManifest()
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range om.Chunks {
					raw, err := os.ReadFile(filepath.Join(offlineDir, "chunks", entry.ID+".jsonl.gz"))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "chunks", entry.ID+".jsonl.gz"), raw, 0600); err != nil {
						t.Fatal(err)
					}
					m.Chunks = append(m.Chunks, entry)
				}
				if reverse {
					for i, j := 0, len(m.Chunks)-1; i < j; i, j = i+1, j-1 {
						m.Chunks[i], m.Chunks[j] = m.Chunks[j], m.Chunks[i]
					}
				}
				if err := sa.writeManifest(m); err != nil {
					t.Fatal(err)
				}
				for round := 0; round < 3; round++ {
					for _, s := range []*Syncer{sa, sb} {
						if _, err := s.Import(); err != nil {
							t.Fatal(err)
						}
					}
					for index, s := range []*store.Store{a, b} {
						obs, err := s.AllObservations("proj-a", "project", 100)
						if err != nil {
							t.Fatal(err)
						}
						if len(obs) != 1 || obs[0].Title != "offline new" {
							t.Fatalf("round%d replica%d live=%+v", round, index, obs)
						}
					}
				}
				// Export scope must not publish the other project's seed session or observation.
				if _, err := b.GetSession("s-other"); err == nil {
					t.Fatal("foreign session leaked")
				}
				other, err := b.AllObservations("proj-b", "project", 100)
				if err != nil || len(other) != 0 {
					t.Fatalf("foreign observations: %+v %v", other, err)
				}
			})
		}
	}
}

func TestIsolatedSessionTwoReplicaScope(t *testing.T) {
	a, b := newTestStore(t), newTestStore(t)
	for _, p := range []string{"alpha", "beta"} {
		id, err := a.RegisterSessionWithProjectProvenance("host-"+p, p, "/"+p, store.SessionOwnershipProjectOwned, false, true, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.AddObservation(store.AddObservationParams{SessionID: id, Project: p, Type: "note", Title: p, Content: "isolated " + p, Scope: "project"}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	if _, err := New(a, dir).Export("A", "alpha"); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		if _, err := New(b, dir).Import(); err != nil {
			t.Fatal(err)
		}
		data, err := b.ExportProject("alpha")
		if err != nil {
			t.Fatal(err)
		}
		if len(data.Sessions) != 1 || len(data.Observations) != 1 {
			t.Fatalf("alpha snapshot=%+v", data)
		}
		if data.Sessions[0].OwnershipMode != store.SessionOwnershipProjectOwned || data.Observations[0].SessionID != data.Sessions[0].ID {
			t.Fatal("isolated ownership changed")
		}
		other, err := b.ExportProject("beta")
		if err != nil {
			t.Fatal(err)
		}
		if len(other.Sessions) != 0 || len(other.Observations) != 0 {
			t.Fatal("beta leaked into alpha export")
		}
	}
}
