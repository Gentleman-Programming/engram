package sync

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func TestLocalExportDeleteTombstonesIgnoreManifestTimestampAndRemainIdempotent(t *testing.T) {
	src := newTestStore(t)
	const manifestTime = "2099-01-02T03:04:05Z"
	const tombstoneTime = "2000-01-02T03:04:05Z"
	for _, project := range []string{"proj-a", "proj-b"} {
		if err := src.CreateSession("session-"+project, project, "/tmp/"+project); err != nil {
			t.Fatalf("create session for %s: %v", project, err)
		}
		if err := src.DeleteSession("session-" + project); err != nil {
			t.Fatalf("delete session for %s: %v", project, err)
		}
	}
	if _, err := src.DB().Exec(`UPDATE sync_delete_tombstones SET deleted_at = ?`, tombstoneTime); err != nil {
		t.Fatalf("set tombstone timestamp: %v", err)
	}
	syncDir := filepath.Join(t.TempDir(), ".engram")
	writeLocalChunkFile(t, syncDir, "historical", ChunkData{})
	writeManifestFile(t, syncDir, &Manifest{Chunks: []ChunkEntry{{ID: "historical", CreatedAt: manifestTime}}})

	result, err := NewLocalWithProject(src, syncDir, "proj-a").Export("alice", "proj-a")
	if err != nil || result.IsEmpty {
		t.Fatalf("historical tombstone project export = %+v, %v", result, err)
	}
	payload, err := readGzip(filepath.Join(syncDir, "chunks", result.ChunkID+".jsonl.gz"))
	if err != nil {
		t.Fatalf("read delete chunk: %v", err)
	}
	var chunk ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatalf("decode delete chunk: %v", err)
	}
	if len(chunk.Mutations) != 1 || chunk.Mutations[0].EntityKey != "session-proj-a" || chunk.Mutations[0].Project != "proj-a" {
		t.Fatalf("project-scoped historical deletes = %+v", chunk.Mutations)
	}
	if replay, err := NewLocalWithProject(src, syncDir, "proj-a").Export("alice", "proj-a"); err != nil || !replay.IsEmpty {
		t.Fatalf("replayed local export = %+v, %v; want idempotent empty result", replay, err)
	}
}

func TestLocalExportFutureManifestDoesNotSuppressLaterHardDelete(t *testing.T) {
	src := newTestStore(t)
	const project, sessionID = "proj-future", "session-future"
	if err := src.CreateSession(sessionID, project, "/tmp/proj-future"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := src.DeleteSession(sessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	syncDir := filepath.Join(t.TempDir(), ".engram")
	writeLocalChunkFile(t, syncDir, "historical", ChunkData{})
	writeManifestFile(t, syncDir, &Manifest{Chunks: []ChunkEntry{{ID: "historical", CreatedAt: "2099-01-02T03:04:05Z"}}})
	result, err := NewLocalWithProject(src, syncDir, project).Export("alice", project)
	if err != nil || result.IsEmpty {
		t.Fatalf("future-manifest delete export = %+v, %v", result, err)
	}
	payload, err := readGzip(filepath.Join(syncDir, "chunks", result.ChunkID+".jsonl.gz"))
	if err != nil {
		t.Fatalf("read delete chunk: %v", err)
	}
	var chunk ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatalf("decode delete chunk: %v", err)
	}
	if len(chunk.Mutations) != 1 || chunk.Mutations[0].EntityKey != sessionID {
		t.Fatalf("future-manifest deletes = %+v", chunk.Mutations)
	}
}

func TestLocalExportKeepsSoftDeletesOnSnapshotPath(t *testing.T) {
	src := newTestStore(t)
	const project, sessionID = "proj-soft", "session-soft"
	if err := src.CreateSession(sessionID, project, "/tmp/proj-soft"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	observationID, err := src.AddObservation(store.AddObservationParams{SessionID: sessionID, Type: "decision", Title: "soft", Content: "soft delete", Project: project, Scope: "project"})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	syncDir := filepath.Join(t.TempDir(), ".engram")
	exporter := NewLocalWithProject(src, syncDir, project)
	if result, err := exporter.Export("alice", project); err != nil || result.IsEmpty {
		t.Fatalf("initial export = %+v, %v", result, err)
	}
	if err := src.DeleteObservation(observationID, false); err != nil {
		t.Fatalf("soft delete observation: %v", err)
	}
	if _, err := src.DB().Exec(`UPDATE observations SET deleted_at = ?, updated_at = ? WHERE id = ?`, "2099-01-02 03:04:05", "2099-01-02 03:04:05", observationID); err != nil {
		t.Fatalf("set soft-delete timestamp: %v", err)
	}

	result, err := exporter.Export("alice", project)
	if err != nil || result.IsEmpty {
		t.Fatalf("soft-delete export = %+v, %v", result, err)
	}
	payload, err := readGzip(filepath.Join(syncDir, "chunks", result.ChunkID+".jsonl.gz"))
	if err != nil {
		t.Fatalf("read soft-delete chunk: %v", err)
	}
	var chunk ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatalf("decode soft-delete chunk: %v", err)
	}
	if len(chunk.Observations) != 1 || chunk.Observations[0].DeletedAt == nil || len(chunk.Mutations) != 0 {
		t.Fatalf("soft delete must stay on snapshot path, chunk=%+v", chunk)
	}
}

func TestLocalExportImportsHardDeletesAfterInitialSnapshot(t *testing.T) {
	src := newTestStore(t)
	const project = "proj-delete"
	const sessionID = "session-delete"
	if err := src.CreateSession(sessionID, project, "/tmp/proj-delete"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if enrolled, err := src.IsProjectEnrolled(project); err != nil || enrolled {
		t.Fatalf("source enrollment = %t, %v; want unenrolled", enrolled, err)
	}
	observationID, err := src.AddObservation(store.AddObservationParams{
		SessionID: sessionID, Type: "decision", Title: "delete", Content: "delete me", Project: project, Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	promptID, err := src.AddPrompt(store.AddPromptParams{SessionID: sessionID, Content: "delete me", Project: project})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	syncDir := filepath.Join(t.TempDir(), ".engram")
	exporter := NewLocalWithProject(src, syncDir, project)
	if result, err := exporter.Export("alice", project); err != nil || result.IsEmpty {
		t.Fatalf("initial local export = %+v, %v", result, err)
	}

	dst := newTestStore(t)
	if _, err := NewLocalWithProject(dst, syncDir, project).Import(); err != nil {
		t.Fatalf("initial local import: %v", err)
	}
	observation, err := src.GetObservation(observationID)
	if err != nil {
		t.Fatalf("load observation identity: %v", err)
	}
	var promptSyncID string
	if err := src.DB().QueryRow(`SELECT sync_id FROM user_prompts WHERE id = ?`, promptID).Scan(&promptSyncID); err != nil {
		t.Fatalf("load prompt identity: %v", err)
	}

	if err := src.DeletePrompt(promptID); err != nil {
		t.Fatalf("delete prompt: %v", err)
	}
	if err := src.DeleteObservation(observationID, true); err != nil {
		t.Fatalf("hard delete observation: %v", err)
	}
	if err := src.DeleteSession(sessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	second, err := exporter.Export("alice", project)
	if err != nil || second.IsEmpty {
		t.Fatalf("delete local export = %+v, %v", second, err)
	}
	payload, err := readGzip(filepath.Join(syncDir, "chunks", second.ChunkID+".jsonl.gz"))
	if err != nil {
		t.Fatalf("read delete chunk: %v", err)
	}
	var chunk ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatalf("decode delete chunk: %v", err)
	}
	var entities []string
	for _, mutation := range chunk.Mutations {
		entities = append(entities, mutation.Entity)
	}
	if want := []string{store.SyncEntityObservation, store.SyncEntityPrompt, store.SyncEntitySession}; !reflect.DeepEqual(entities, want) {
		t.Fatalf("delete mutation order = %v, want children before session %v", entities, want)
	}
	if _, err := NewLocalWithProject(dst, syncDir, project).Import(); err != nil {
		t.Fatalf("delete local import: %v", err)
	}

	if _, err := dst.GetSession(sessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("destination session after delete = %v, want missing", err)
	}
	if _, err := dst.GetObservation(observation.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("destination observation after delete = %v, want missing", err)
	}
	var prompts int
	if err := dst.DB().QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE sync_id = ?`, promptSyncID).Scan(&prompts); err != nil {
		t.Fatalf("count destination prompts: %v", err)
	}
	if prompts != 0 {
		t.Fatalf("destination prompts after delete = %d, want 0", prompts)
	}
	if replay, err := exporter.Export("alice", project); err != nil || !replay.IsEmpty {
		t.Fatalf("replayed local export = %+v, %v; want idempotent empty result", replay, err)
	}
}
