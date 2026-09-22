package sync

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func mustExportLocalChunk(t *testing.T, sy *Syncer, project string) (*SyncResult, ChunkData) {
	t.Helper()
	result, err := sy.Export("alice", project)
	if err != nil || result.IsEmpty {
		t.Fatalf("export = %+v, %v", result, err)
	}
	payload, err := sy.transport.ReadChunk(result.ChunkID)
	if err != nil {
		t.Fatal(err)
	}
	var chunk ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatal(err)
	}
	return result, chunk
}

func mustImportLocal(t *testing.T, sy *Syncer) {
	t.Helper()
	if _, err := sy.Import(); err != nil {
		t.Fatal(err)
	}
}

func mustNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLocalExportDeleteTombstonesIgnoreManifestTimestampAndRemainIdempotent(t *testing.T) {
	s := newTestStore(t)
	for _, project := range []string{"proj-a", "proj-b"} {
		mustNoError(t, s.CreateSession("session-"+project, project, "/tmp/"+project))
		mustNoError(t, s.DeleteSession("session-"+project))
	}
	if _, err := s.DB().Exec(`UPDATE sync_delete_tombstones SET deleted_at = '2000-01-02 03:04:05'`); err != nil {
		t.Fatal(err)
	}
	mustNoError(t, s.CreateSession("legacy-parent", "proj-a", "/tmp/legacy"))
	if _, err := s.DB().Exec(`INSERT INTO prompt_tombstones (sync_id, session_id, project, deleted_at) VALUES ('prompt-legacy', 'legacy-parent', '', '2000-01-02 03:04:05')`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), ".engram")
	writeLocalChunkFile(t, dir, "historical", ChunkData{})
	writeManifestFile(t, dir, &Manifest{Chunks: []ChunkEntry{{ID: "historical", CreatedAt: "2099-01-02T03:04:05Z"}}})
	sy := NewLocalWithProject(s, dir, "proj-a")
	_, chunk := mustExportLocalChunk(t, sy, "proj-a")
	if len(chunk.Mutations) != 2 || chunk.Mutations[0].EntityKey != "prompt-legacy" || chunk.Mutations[0].Project != "proj-a" || chunk.Mutations[1].EntityKey != "session-proj-a" {
		t.Fatalf("project-scoped historical deletes = %+v", chunk.Mutations)
	}
	if replay, err := sy.Export("alice", "proj-a"); err != nil || !replay.IsEmpty {
		t.Fatalf("replayed export = %+v, %v", replay, err)
	}
}

func TestLocalExportImportsHardDeletesAfterInitialSnapshot(t *testing.T) {
	src := newTestStore(t)
	const project, sessionID = "proj-delete", "session-delete"
	mustNoError(t, src.CreateSession(sessionID, project, "/tmp/delete"))
	observationID, err := src.AddObservation(store.AddObservationParams{SessionID: sessionID, Type: "decision", Title: "delete", Content: "delete", Project: project, Scope: "project"})
	if err != nil {
		t.Fatal(err)
	}
	promptID, err := src.AddPrompt(store.AddPromptParams{SessionID: sessionID, Content: "delete", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), ".engram")
	exporter := NewLocalWithProject(src, dir, project)
	mustExportLocalChunk(t, exporter, project)
	dst := newTestStore(t)
	importer := NewLocalWithProject(dst, dir, project)
	mustImportLocal(t, importer)
	observation, err := src.GetObservation(observationID)
	if err != nil {
		t.Fatal(err)
	}
	var promptSyncID string
	if err := src.DB().QueryRow(`SELECT sync_id FROM user_prompts WHERE id = ?`, promptID).Scan(&promptSyncID); err != nil {
		t.Fatal(err)
	}
	mustNoError(t, src.DeletePrompt(promptID))
	mustNoError(t, src.DeleteObservation(observationID, true))
	mustNoError(t, src.DeleteSession(sessionID))
	_, chunk := mustExportLocalChunk(t, exporter, project)
	var entities []string
	for _, mutation := range chunk.Mutations {
		entities = append(entities, mutation.Entity)
	}
	if strings.Join(entities, ",") != "observation,prompt,session" {
		t.Fatalf("delete order = %v", entities)
	}
	mustImportLocal(t, importer)
	if _, err := dst.GetSession(sessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("destination session survived: %v", err)
	}
	if _, err := dst.GetObservation(observation.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("destination observation survived: %v", err)
	}
	var prompts int
	if err := dst.DB().QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE sync_id = ?`, promptSyncID).Scan(&prompts); err != nil || prompts != 0 {
		t.Fatalf("destination prompts = %d, %v", prompts, err)
	}
}

func TestLocalImportHardDeleteGenerationSurvivesOutOfOrderChunks(t *testing.T) {
	dst := newTestStore(t)
	const project, sessionID, parentID, observationID = "proj-order", "session-order", "parent-order", "observation-order"
	mustNoError(t, dst.CreateSession(parentID, project, "/tmp/parent"))
	deleteChunk := func(at string) ChunkData {
		return ChunkData{Mutations: []store.SyncMutation{
			{Entity: store.SyncEntityObservation, EntityKey: observationID, Op: store.SyncOpDelete, Payload: `{"sync_id":"` + observationID + `","session_id":"` + parentID + `","project":"` + project + `","deleted_at":"` + at + `","hard_delete":true}`},
			{Entity: store.SyncEntitySession, EntityKey: sessionID, Op: store.SyncOpDelete, Payload: `{"id":"` + sessionID + `","project":"` + project + `","deleted_at":"` + at + `","hard_delete":true}`},
		}}
	}
	projectValue := project
	dir := filepath.Join(t.TempDir(), ".engram")
	writeLocalChunkFile(t, dir, "delete", deleteChunk("2026-02-01 00:00:00"))
	writeLocalChunkFile(t, dir, "stale", ChunkData{
		Sessions:     []store.Session{{ID: sessionID, Project: project, Directory: "/tmp/stale", StartedAt: "2026-01-01 00:00:00"}},
		Observations: []store.Observation{{SyncID: observationID, SessionID: parentID, Project: &projectValue, Type: "note", Content: "stale", Scope: "project", CreatedAt: "2026-01-01 00:00:00", UpdatedAt: "2026-01-01 00:00:00"}},
	})
	entries := []ChunkEntry{{ID: "delete"}, {ID: "stale"}}
	writeManifestFile(t, dir, &Manifest{Version: ownershipModeManifestVersion, Chunks: entries})
	importer := NewLocalWithProject(dst, dir, project)
	mustImportLocal(t, importer)
	if _, err := dst.GetSession(sessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale session resurrected: %v", err)
	}
	if _, err := dst.GetObservationBySyncID(observationID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale observation resurrected: %v", err)
	}
	writeLocalChunkFile(t, dir, "new", ChunkData{
		Sessions:     []store.Session{{ID: sessionID, Project: project, Directory: "/tmp/new", StartedAt: "2026-03-01 00:00:00"}},
		Observations: []store.Observation{{SyncID: observationID, SessionID: parentID, Project: &projectValue, Type: "note", Content: "new", Scope: "project", CreatedAt: "2026-03-01 00:00:00", UpdatedAt: "2026-03-01 00:00:00"}},
	})
	entries = append(entries, ChunkEntry{ID: "new"})
	writeManifestFile(t, dir, &Manifest{Version: ownershipModeManifestVersion, Chunks: entries})
	mustImportLocal(t, importer)
	if session, err := dst.GetSession(sessionID); err != nil || session.Directory != "/tmp/new" {
		t.Fatalf("newer session = %+v, %v", session, err)
	}
	if observation, err := dst.GetObservationBySyncID(observationID); err != nil || observation.Content != "new" {
		t.Fatalf("newer observation = %+v, %v", observation, err)
	}
}

func TestLocalExportHardDeleteAfterRecreatedSessionSnapshot(t *testing.T) {
	s := newTestStore(t)
	const project, sessionID = "proj-recreate", "session-recreate"
	sy := NewLocalWithProject(s, filepath.Join(t.TempDir(), ".engram"), project)
	mustNoError(t, s.CreateSession(sessionID, project, "/tmp/recreate"))
	mustNoError(t, s.DeleteSession(sessionID))
	if _, err := s.DB().Exec(`UPDATE sync_delete_tombstones SET deleted_at = '2000-01-02 03:04:05' WHERE entity = ? AND entity_key = ?`, store.SyncEntitySession, sessionID); err != nil {
		t.Fatal(err)
	}
	first, _ := mustExportLocalChunk(t, sy, project)
	if first.MutationsExported != 1 {
		t.Fatalf("initial delete export = %+v", first)
	}
	mustNoError(t, s.CreateSession(sessionID, project, "/tmp/recreate"))
	if _, err := s.DB().Exec(`UPDATE sessions SET started_at = '2099-01-02 03:04:05' WHERE id = ?`, sessionID); err != nil {
		t.Fatal(err)
	}
	second, _ := mustExportLocalChunk(t, sy, project)
	if second.SessionsExported != 1 {
		t.Fatalf("recreated export = %+v", second)
	}
	mustNoError(t, s.DeleteSession(sessionID))
	if _, err := s.DB().Exec(`UPDATE sync_delete_tombstones SET deleted_at = '2100-01-02 03:04:05' WHERE entity = ? AND entity_key = ?`, store.SyncEntitySession, sessionID); err != nil {
		t.Fatal(err)
	}
	third, chunk := mustExportLocalChunk(t, sy, project)
	if third.MutationsExported != 1 || len(chunk.Mutations) != 1 || chunk.Mutations[0].EntityKey != sessionID {
		t.Fatalf("second delete = %+v, %+v", third, chunk.Mutations)
	}
	dst := newTestStore(t)
	mustImportLocal(t, NewLocalWithProject(dst, sy.syncDir, project))
	if _, err := dst.GetSession(sessionID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("imported session survived: %v", err)
	}
	if replay, err := sy.Export("alice", project); err != nil || !replay.IsEmpty {
		t.Fatalf("replayed export = %+v, %v", replay, err)
	}
}
