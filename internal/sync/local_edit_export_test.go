package sync

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestLocalExportComparesObservationVersions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		history    []string
		current    string
		wantExport bool
	}{
		{"overtaken SQLite revision", []string{"2025-01-01 00:00:00"}, "2025-01-02 00:00:00", true},
		{"overtaken RFC3339 revision", []string{"2025-01-01T01:00:00+01:00"}, "2025-01-02 00:00:00", true},
		{"fractional edit", []string{"2025-01-02 00:00:00.100"}, "2025-01-02 00:00:00.200", true},
		{"equal version", []string{"2025-01-02T00:00:00Z"}, "2025-01-02 00:00:00", false},
		{"older snapshot after newer", []string{"2025-01-03 00:00:00", "2025-01-01 00:00:00"}, "2025-01-02 00:00:00", false},
		{"legacy identity uses watermark", []string{""}, "2025-01-02 00:00:00", false},
		{"unreadable legacy version uses watermark", []string{"zzzz"}, "2025-07-02 00:00:00", true},
	} {
		for _, mutations := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mutations=%t", tc.name, mutations), func(t *testing.T) {
				s := newTestStore(t)
				if err := s.CreateSession("version-host", "demo", "/synthetic/demo"); err != nil {
					t.Fatal(err)
				}
				id, err := s.AddObservation(store.AddObservationParams{SessionID: "version-host", Project: "demo", Type: "decision", Title: "Version", Content: "current"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.DB().Exec("UPDATE sessions SET started_at='2025-01-01 00:00:00'; UPDATE observations SET created_at='2025-01-01 00:00:00',updated_at=? WHERE id=?", tc.current, id); err != nil {
					t.Fatal(err)
				}
				obs, err := s.GetObservation(id)
				if err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(t.TempDir(), ".engram")
				manifest := &Manifest{Version: 1}
				for i, stamp := range tc.history {
					history := *obs
					history.CreatedAt, history.UpdatedAt = "", stamp
					chunk := ChunkData{Observations: []store.Observation{history}}
					if mutations {
						payload, err := json.Marshal(history)
						if err != nil {
							t.Fatal(err)
						}
						chunk = ChunkData{Mutations: []store.SyncMutation{{Entity: store.SyncEntityObservation, EntityKey: obs.SyncID, Op: store.SyncOpUpsert, Payload: string(payload)}}}
					}
					key := fmt.Sprintf("version-%d", i)
					writeLocalChunkFile(t, dir, key, chunk)
					manifest.Chunks = append(manifest.Chunks, ChunkEntry{ID: key, CreatedAt: "2025-06-01T00:00:00Z"})
				}
				writeManifestFile(t, dir, manifest)
				result, err := New(s, dir).Export("A", "demo")
				if err != nil {
					t.Fatal(err)
				}
				if result.IsEmpty == tc.wantExport {
					t.Fatalf("export = %+v, want observation=%t", result, tc.wantExport)
				}
			})
		}
	}
}

func TestLocalEditOvertakenByRemoteChunk(t *testing.T) {
	a, b := newTestStore(t), newTestStore(t)
	dir := filepath.Join(t.TempDir(), ".engram")
	sa, sb := New(a, dir), New(b, dir)
	if err := a.CreateSession("a", "demo", "/synthetic/demo"); err != nil {
		t.Fatal(err)
	}
	params := store.AddObservationParams{SessionID: "a", Project: "demo", Scope: "project", Type: "decision", Title: "Deploy target", Content: "original", TopicKey: "demo/deploy"}
	id, err := a.AddObservation(params)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB().Exec("UPDATE observations SET created_at='2025-01-01 00:00:00',updated_at='2025-01-01 00:00:00' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if _, err = sa.Export("A", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err = sb.Import(); err != nil {
		t.Fatal(err)
	}
	params.Content = "corrected"
	if _, err = a.AddObservation(params); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB().Exec("UPDATE observations SET updated_at='2025-01-02 00:00:00' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if err = b.CreateSession("b", "demo", "/synthetic/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.AddObservation(store.AddObservationParams{SessionID: "b", Project: "demo", Scope: "project", Type: "decision", Title: "Unrelated", Content: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err = sb.Export("B", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err = sa.Import(); err != nil {
		t.Fatal(err)
	}
	result, err := sa.Export("A", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if result.IsEmpty {
		t.Fatal("local revision2 was lost behind unrelated remote manifest timestamp")
	}
	if _, err = sb.Import(); err != nil {
		t.Fatal(err)
	}
	obs, err := a.GetObservation(id)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := b.GetObservationBySyncID(obs.SyncID)
	if err != nil || remote.Content != "corrected" || remote.RevisionCount != 2 {
		t.Fatalf("remote edit not delivered: %+v %v", remote, err)
	}
	again, err := sa.Export("A", "demo")
	if err != nil || !again.IsEmpty {
		t.Fatalf("unchanged edit exported again: %+v %v", again, err)
	}
}
