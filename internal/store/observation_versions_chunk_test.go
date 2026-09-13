package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

func pulledObservationForVersionTest(t *testing.T, syncID, title string) (SyncMutation, ObservationVersion) {
	t.Helper()
	project := "proj"
	at := "2026-01-01 00:00:00"
	payload, err := json.Marshal(syncObservationPayload{SyncID: syncID, SessionID: "chunk-session", Type: "note", Title: title, Content: title, Project: &project, Scope: "project", RevisionCount: 1, DuplicateCount: 1, CreatedAt: at, UpdatedAt: at})
	if err != nil {
		t.Fatalf("marshal observation payload: %v", err)
	}
	return SyncMutation{Entity: SyncEntityObservation, EntityKey: syncID, Op: SyncOpUpsert, Payload: string(payload)}, ObservationVersion{VersionID: "00000000-0000-4000-8000-000000000401", ObservationSyncID: syncID, SessionID: "chunk-session", Type: "note", Title: title, Content: title, Project: &project, Scope: "project", RevisionCount: 1, CapturedAt: at}
}

func newVersionChunkStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := s.CreateSession("chunk-session", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return s
}

func TestApplyPulledChunkWithVersionsReceiptedChunkDoesNotInitializeState(t *testing.T) {
	s := newTestStore(t)
	target, chunkID := "uninitialized-target", "already-receipted"
	if _, err := s.DB().Exec(`INSERT INTO sync_chunks (target_key, chunk_id) VALUES (?, ?)`, target, chunkID); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	if err := s.ApplyPulledChunkWithVersions(target, chunkID, []SyncMutation{{Entity: SyncEntityObservation}}, []ObservationVersion{{VersionID: "invalid"}}); err != nil {
		t.Fatalf("receipted chunk: %v", err)
	}
	var states, observations int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sync_state WHERE target_key = ?`, target).Scan(&states); err != nil {
		t.Fatalf("count sync state: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&observations); err != nil || states != 0 || observations != 0 {
		t.Fatalf("receipted no-op states=%d observations=%d err=%v", states, observations, err)
	}
}

func TestApplyPulledChunkWithVersionsAtomicAndIdempotent(t *testing.T) {
	s := newVersionChunkStore(t)
	mutation, version := pulledObservationForVersionTest(t, "chunk-version-observation", "remote")
	if err := s.ApplyPulledChunkWithVersions(DefaultSyncTargetKey, "versions-success", []SyncMutation{mutation}, []ObservationVersion{version}); err != nil {
		t.Fatalf("apply chunk with version: %v", err)
	}
	observation, err := s.GetObservationBySyncID(version.ObservationSyncID)
	if err != nil || observation.Title != "remote" {
		t.Fatalf("parent observation = %#v, %v", observation, err)
	}
	versions, err := s.ObservationVersions(version.ObservationSyncID, 10)
	if err != nil || len(versions) != 1 || versions[0].VersionID != version.VersionID {
		t.Fatalf("imported versions = %#v, %v", versions, err)
	}
	conflict := version
	conflict.Content = "conflict"
	if err := s.ApplyPulledChunkWithVersions(DefaultSyncTargetKey, "versions-success", []SyncMutation{mutation}, []ObservationVersion{conflict}); err != nil {
		t.Fatalf("receipted chunk retry: %v", err)
	}
	versions, _ = s.ObservationVersions(version.ObservationSyncID, 10)
	if len(versions) != 1 {
		t.Fatalf("versions after retry = %#v, want one", versions)
	}
}

func TestApplyPulledChunkWithVersionsRollsBackInvalidAndSupportsNil(t *testing.T) {
	t.Run("invalid version rolls back parent and receipt", func(t *testing.T) {
		s := newVersionChunkStore(t)
		mutation, version := pulledObservationForVersionTest(t, "chunk-invalid-observation", "remote")
		version.VersionID = "invalid"
		if err := s.ApplyPulledChunkWithVersions(DefaultSyncTargetKey, "versions-invalid", []SyncMutation{mutation}, []ObservationVersion{version}); err == nil {
			t.Fatal("invalid version chunk succeeded")
		}
		if _, err := s.GetObservationBySyncID(mutation.EntityKey); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("parent persisted after rollback: %v", err)
		}
		chunks, err := s.GetSyncedChunksForTarget(DefaultSyncTargetKey)
		if err != nil || chunks["versions-invalid"] {
			t.Fatalf("chunk receipt after rollback = %#v, %v", chunks, err)
		}
	})
	t.Run("missing parent version rolls back receipt", func(t *testing.T) {
		s := newVersionChunkStore(t)
		_, version := pulledObservationForVersionTest(t, "missing-parent", "remote")
		if err := s.ApplyPulledChunkWithVersions(DefaultSyncTargetKey, "versions-missing-parent", nil, []ObservationVersion{version}); err == nil {
			t.Fatal("missing parent version chunk succeeded")
		}
		chunks, err := s.GetSyncedChunksForTarget(DefaultSyncTargetKey)
		if err != nil || chunks["versions-missing-parent"] {
			t.Fatalf("missing-parent receipt = %#v, %v", chunks, err)
		}
	})
	t.Run("nil versions preserve incomplete pulled history", func(t *testing.T) {
		s := newVersionChunkStore(t)
		mutation, _ := pulledObservationForVersionTest(t, "chunk-nil-observation", "remote")
		if err := s.ApplyPulledChunkWithVersions(DefaultSyncTargetKey, "versions-nil", []SyncMutation{mutation}, nil); err != nil {
			t.Fatalf("nil version chunk: %v", err)
		}
		observation, err := s.GetObservationBySyncID(mutation.EntityKey)
		if err != nil {
			t.Fatalf("legacy parent import: %v", err)
		}
		updated := "local"
		if _, err := s.UpdateObservation(observation.ID, UpdateObservationParams{Content: &updated}); err != nil {
			t.Fatalf("update pulled observation: %v", err)
		}
		versions, err := s.ObservationVersions(mutation.EntityKey, 10)
		if err != nil || len(versions) != 2 || versions[1].Content != "remote" || versions[0].HistoryComplete {
			t.Fatalf("legacy pulled history = %#v, %v; want preserved incomplete baseline", versions, err)
		}
	})
}
