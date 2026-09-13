package store

import (
	"errors"
	"testing"
)

func TestObservationVersionsMigrationBaseline(t *testing.T) {
	s := newTestStoreWithLegacySchema(t, migrationFixtureRows())

	versions, err := s.ObservationVersions("obs-legacy-001", 10)
	if err != nil {
		t.Fatalf("read migrated versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions = %d, want 1 baseline", len(versions))
	}
	got := versions[0]
	if !got.IsBaseline || got.HistoryComplete || got.RevisionCount != 1 || got.VersionID == "" {
		t.Fatalf("migrated version = %#v, want incomplete baseline with an ID at legacy revision 1", got)
	}
	if err := s.migrate(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	again, err := s.ObservationVersions("obs-legacy-001", 10)
	if err != nil || len(again) != 1 || again[0].VersionID != got.VersionID {
		t.Fatalf("baseline after repeat migration = %#v, %v; want one stable UUID", again, err)
	}
	if got.Title != "Use sessions for auth" || got.Content != "We chose session-based auth" {
		t.Fatalf("migrated snapshot = %#v, want the legacy observation state", got)
	}
}

func TestObservationVersionsRecordSemanticChangesInBoundedOrder(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("versions", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "versions", Type: "decision", Title: "original", Content: "original content", Project: "engram", TopicKey: "versions/key"})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	first, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "versions", Type: "decision", Title: "topic update", Content: "topic content", Project: "engram", TopicKey: "versions/key"}); err != nil {
		t.Fatalf("topic-key update: %v", err)
	}
	updatedTitle := "manual update"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Title: &updatedTitle}); err != nil {
		t.Fatalf("update observation: %v", err)
	}

	versions, err := s.ObservationVersions(first.SyncID, 2)
	if err != nil {
		t.Fatalf("read versions: %v", err)
	}
	if len(versions) != 2 || versions[0].RevisionCount != 3 || versions[1].RevisionCount != 2 {
		t.Fatalf("bounded versions = %#v, want revisions 3 then 2", versions)
	}
	if versions[0].Title != updatedTitle || versions[1].Content != "topic content" {
		t.Fatalf("versions did not preserve semantic states: %#v", versions)
	}
	all, err := s.ObservationVersions(first.SyncID, 99)
	if err != nil {
		t.Fatalf("read all versions: %v", err)
	}
	if len(all) != 3 || all[2].Content != "original content" || all[2].IsBaseline || !all[2].HistoryComplete {
		t.Fatalf("versions = %#v, want complete initial snapshot and two revisions", all)
	}
	seen := map[string]bool{}
	for _, version := range all {
		if version.VersionID == "" || seen[version.VersionID] || version.ObservationSyncID != first.SyncID {
			t.Fatalf("portable identity = %#v, want unique version IDs for %q", all, first.SyncID)
		}
		seen[version.VersionID] = true
	}
	next, err := s.ObservationVersionsPage(first.SyncID, versions[1].RevisionCount, versions[1].VersionID, 2)
	if err != nil || len(next) != 1 || next[0].RevisionCount != 1 {
		t.Fatalf("next page = %#v, %v; want deterministic remaining revision", next, err)
	}
}

func TestObservationVersionsIgnoreDedupeAndCleanUpOnHardDelete(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("versions", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	params := AddObservationParams{SessionID: "versions", Type: "note", Title: "dedupe", Content: "same", Project: "engram"}
	id, err := s.AddObservation(params)
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if duplicateID, err := s.AddObservation(params); err != nil || duplicateID != id {
		t.Fatalf("dedupe = (%d, %v), want existing id %d", duplicateID, err, id)
	}
	latest, err := s.GetObservation(id)
	if err != nil || latest.DuplicateCount != 2 {
		t.Fatalf("latest observation after dedupe = %#v, %v; want duplicate count 2", latest, err)
	}
	versions, err := s.ObservationVersions(obs.SyncID, 10)
	if err != nil || len(versions) != 1 {
		t.Fatalf("versions after dedupe = %#v, %v; want one", versions, err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO observation_versions (version_id, observation_id, observation_sync_id, session_id, type, title, content, scope, revision_count)
		SELECT ?, id, sync_id, session_id, type, title, content, scope, revision_count FROM observations WHERE id = ?`,
		"00000000-0000-4000-8000-000000000001", id); err != nil {
		t.Fatalf("insert concurrent revision: %v", err)
	}
	versions, err = s.ObservationVersions(obs.SyncID, 10)
	if err != nil || len(versions) != 2 || versions[0].RevisionCount != versions[1].RevisionCount || versions[0].VersionID == versions[1].VersionID {
		t.Fatalf("same-revision versions = %#v, %v; want distinct IDs", versions, err)
	}
	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	if _, err := s.ObservationVersions(obs.SyncID, 10); !errors.Is(err, ErrObservationNotFound) {
		t.Fatalf("versions after hard delete error = %v, want ErrObservationNotFound", err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM observation_versions WHERE observation_id = ?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("sidecar rows after hard delete = %d, %v; want 0", count, err)
	}
}
