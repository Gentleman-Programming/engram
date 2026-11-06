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
		created    string
	}{
		{"overtaken SQLite revision", []string{"2025-01-01 00:00:00"}, "2025-01-02 00:00:00", true, ""},
		{"overtaken RFC3339 revision", []string{"2025-01-01T01:00:00+01:00"}, "2025-01-02 00:00:00", true, ""},
		{"fractional edit", []string{"2025-01-02 00:00:00.100"}, "2025-01-02 00:00:00.200", true, ""},
		{"equal version", []string{"2025-01-02T00:00:00Z"}, "2025-01-02 00:00:00", false, ""},
		{"older snapshot after newer", []string{"2025-01-03 00:00:00", "2025-01-01 00:00:00"}, "2025-01-02 00:00:00", false, ""},
		{"legacy identity cannot hide an edit", []string{""}, "2025-01-02 00:00:00", true, ""},
		{"unreadable legacy version uses watermark", []string{"zzzz"}, "2025-07-02 00:00:00", true, ""},
		{"malformed update with readable creation", []string{"zzzz"}, "2025-01-02 00:00:00", true, "2025-01-01 00:00:00"},
		{"missing update with readable creation", []string{""}, "2025-01-02 00:00:00", true, "2025-01-01 00:00:00"},
		{"creation newer than update is already exported", []string{"2024-12-01 00:00:00"}, "2025-01-01 00:00:00", false, "2025-01-02 00:00:00"},
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
				if tc.name == "creation newer than update is already exported" {
					if _, err := s.DB().Exec("UPDATE observations SET created_at=?,updated_at=? WHERE id=?", tc.created, tc.history[0], id); err != nil {
						t.Fatal(err)
					}
				}
				dir := filepath.Join(t.TempDir(), ".engram")
				manifest := &Manifest{Version: 1}
				for i, stamp := range tc.history {
					history := *obs
					history.CreatedAt, history.UpdatedAt = tc.created, stamp
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

func TestLocalEditAfterLegacyOrDeletedHistoryRoundTrip(t *testing.T) {
	for _, kind := range []string{"identity-only", "unreadable", "delete-then-stale"} {
		t.Run(kind, func(t *testing.T) {
			a, b := newTestStore(t), newTestStore(t)
			if err := a.CreateSession("host", "demo", "/synthetic"); err != nil {
				t.Fatal(err)
			}
			params := store.AddObservationParams{SessionID: "host", Project: "demo", Type: "decision", Title: "History", Content: "original", TopicKey: "demo/history"}
			id, err := a.AddObservation(params)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.DB().Exec("UPDATE sessions SET started_at='2025-01-01 00:00:00'; UPDATE observations SET created_at='2025-01-01 00:00:00',updated_at='2025-01-01 00:00:00' WHERE id=?", id); err != nil {
				t.Fatal(err)
			}
			original, err := a.GetObservation(id)
			if err != nil {
				t.Fatal(err)
			}
			session, err := a.GetSession("host")
			if err != nil {
				t.Fatal(err)
			}
			history := *original
			if kind != "delete-then-stale" {
				history.CreatedAt, history.UpdatedAt = "", ""
				if kind == "unreadable" {
					history.UpdatedAt = "zzzz"
				}
			}
			dir := filepath.Join(t.TempDir(), ".engram")
			writeLocalChunkFile(t, dir, "initial", ChunkData{Sessions: []store.Session{*session}, Observations: []store.Observation{history}})
			m := &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: "initial", CreatedAt: "2025-01-01T00:00:00Z"}}}
			if kind == "delete-then-stale" {
				payload, err := json.Marshal(map[string]any{"sync_id": original.SyncID, "hard_delete": true, "deleted": true, "deleted_at": "2025-01-02 00:00:00", "updated_at": "2025-01-02 00:00:00"})
				if err != nil {
					t.Fatal(err)
				}
				writeLocalChunkFile(t, dir, "delete", ChunkData{Mutations: []store.SyncMutation{{Entity: store.SyncEntityObservation, EntityKey: original.SyncID, Op: store.SyncOpDelete, OccurredAt: "2025-01-02 00:00:00", Payload: string(payload)}}})
				writeLocalChunkFile(t, dir, "stale", ChunkData{Observations: []store.Observation{*original}})
				m.Chunks = append(m.Chunks, ChunkEntry{ID: "delete", CreatedAt: "2025-01-02T00:00:00Z"}, ChunkEntry{ID: "stale", CreatedAt: "2025-01-04T00:00:00Z"})
			}
			writeLocalChunkFile(t, dir, "unrelated", ChunkData{})
			m.Chunks = append(m.Chunks, ChunkEntry{ID: "unrelated", CreatedAt: "2025-06-01T00:00:00Z"})
			writeManifestFile(t, dir, m)
			sa, sb := New(a, dir), New(b, dir)
			if _, err := sb.Import(); err != nil {
				t.Fatal(err)
			}
			if kind == "delete-then-stale" {
				if obs, err := b.GetObservationBySyncID(original.SyncID); err == nil {
					t.Fatalf("stale upsert recreated deleted observation: %+v", obs)
				}
				if _, err := a.DB().Exec("UPDATE observations SET updated_at='2025-01-01 12:00:00' WHERE id=?", id); err != nil {
					t.Fatal(err)
				}
				stale, err := sa.Export("A", "demo")
				if err != nil || !stale.IsEmpty {
					t.Fatalf("stale upsert lowered delete floor: %+v %v", stale, err)
				}
			} else {
				// Freeze the legacy import's default local timestamps independently
				// of the host clock; the export-selection assertion uses the history.
				if _, err := b.DB().Exec("UPDATE observations SET created_at='2025-01-01 00:00:00',updated_at='2025-01-01 00:00:00'"); err != nil {
					t.Fatal(err)
				}
			}
			params.Content = "corrected"
			if _, err := a.AddObservation(params); err != nil {
				t.Fatal(err)
			}
			if _, err := a.DB().Exec("UPDATE observations SET updated_at='2025-01-03 00:00:00' WHERE id=?", id); err != nil {
				t.Fatal(err)
			}
			result, err := sa.Export("A", "demo")
			if err != nil || result.IsEmpty {
				t.Fatalf("overtaken edit not exported: %+v %v", result, err)
			}
			if _, err := sb.Import(); err != nil {
				t.Fatal(err)
			}
			remote, err := b.GetObservationBySyncID(original.SyncID)
			if err != nil || remote.Content != "corrected" {
				t.Fatalf("peer did not receive later edit: %+v %v", remote, err)
			}
			again, err := sa.Export("A", "demo")
			if err != nil || !again.IsEmpty {
				t.Fatalf("unchanged edit re-exported: %+v %v", again, err)
			}
		})
	}
}
