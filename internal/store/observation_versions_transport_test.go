package store

import (
	"database/sql"
	"errors"
	"testing"
)

func TestExportImportObservationVersions(t *testing.T) {
	source := newTestStore(t)
	for _, session := range []struct{ id, project string }{{"versions-a", "proj-a"}, {"versions-b", "proj-b"}} {
		if err := source.CreateSession(session.id, session.project, "/tmp"); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	id, err := source.AddObservation(AddObservationParams{SessionID: "versions-a", Type: "note", Title: "first", Content: "first", Project: "proj-a"})
	if err != nil {
		t.Fatalf("add source observation: %v", err)
	}
	updated := "updated"
	if _, err := source.UpdateObservation(id, UpdateObservationParams{Title: &updated}); err != nil {
		t.Fatalf("update source observation: %v", err)
	}
	a, err := source.GetObservation(id)
	if err != nil {
		t.Fatalf("get source observation: %v", err)
	}
	if _, err := source.DB().Exec(`
		INSERT INTO observation_versions (version_id, observation_id, observation_sync_id, session_id, type, title, content, project, scope, revision_count)
		SELECT ?, id, sync_id, session_id, type, title, content, project, scope, revision_count FROM observations WHERE id = ?`,
		"00000000-0000-4000-8000-000000000099", id); err != nil {
		t.Fatalf("add concurrent version: %v", err)
	}
	if _, err := source.AddObservation(AddObservationParams{SessionID: "versions-b", Type: "note", Title: "other", Content: "other", Project: "proj-b"}); err != nil {
		t.Fatalf("add other-project observation: %v", err)
	}

	exported, err := source.ExportProject("proj-a")
	if err != nil {
		t.Fatalf("export project: %v", err)
	}
	if len(exported.Observations) != 1 || len(exported.ObservationVersions) != 3 {
		t.Fatalf("project export = %d observations / %d versions, want 1 / 3", len(exported.Observations), len(exported.ObservationVersions))
	}
	for _, version := range exported.ObservationVersions {
		if version.VersionID == "" || version.ObservationSyncID != a.SyncID {
			t.Fatalf("exported version = %#v, want portable proj-a identity", version)
		}
	}

	destination := newTestStore(t)
	result, err := destination.Import(exported)
	if err != nil || result.VersionsImported != 3 {
		t.Fatalf("first import = %#v, %v; want three versions", result, err)
	}
	versions, err := destination.ObservationVersions(a.SyncID, 10)
	if err != nil || len(versions) != 3 || versions[0].RevisionCount != versions[1].RevisionCount {
		t.Fatalf("imported versions = %#v, %v; want concurrent same-revision history", versions, err)
	}
	if result, err := destination.Import(exported); err != nil || result.VersionsImported != 0 {
		t.Fatalf("idempotent import = %#v, %v; want no new versions", result, err)
	}
	if _, err := destination.DB().Exec(`UPDATE observations SET title = 'local projection', updated_at = '2099-01-01 00:00:00' WHERE sync_id = ?`, a.SyncID); err != nil {
		t.Fatalf("seed local projection: %v", err)
	}
	if _, err := destination.Import(exported); err != nil {
		t.Fatalf("reimport history: %v", err)
	}
	projection, err := destination.GetObservationBySyncID(a.SyncID)
	if err != nil || projection.Title != "local projection" {
		t.Fatalf("projection after history import = %#v, %v; want local projection unchanged", projection, err)
	}
}

func TestImportObservationVersionsRejectsInvalidIdentityAndParentMetadata(t *testing.T) {
	project := "proj"
	valid := ObservationVersion{VersionID: "00000000-0000-4000-8000-000000000201", ObservationSyncID: "atomic-observation", SessionID: "atomic-session", Type: "note", Title: "atomic", Content: "atomic", Project: &project, Scope: "project", RevisionCount: 1, CapturedAt: Now()}
	for _, test := range []struct {
		name    string
		version ObservationVersion
	}{
		{"malformed ID", func() ObservationVersion { v := valid; v.VersionID = "not-a-uuid"; return v }()},
		{"non-v4 ID", func() ObservationVersion { v := valid; v.VersionID = "00000000-0000-3000-8000-000000000201"; return v }()},
		{"session mismatch", func() ObservationVersion { v := valid; v.SessionID = "other-session"; return v }()},
		{"project mismatch", func() ObservationVersion { v := valid; other := "other"; v.Project = &other; return v }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newTestStore(t)
			data := &ExportData{Sessions: []Session{{ID: "atomic-session", Project: project, Directory: "/tmp", StartedAt: Now()}}, Observations: []Observation{{SyncID: "atomic-observation", SessionID: "atomic-session", Type: "note", Title: "atomic", Content: "atomic", Project: &project, Scope: "project", CreatedAt: Now(), UpdatedAt: Now()}}, ObservationVersions: []ObservationVersion{test.version}}
			if _, err := s.Import(data); err == nil {
				t.Fatal("invalid version import succeeded")
			}
			if _, err := s.GetObservationBySyncID("atomic-observation"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("observation persisted after rollback: %v", err)
			}
		})
	}
}

func TestImportObservationVersionsRejectsConflictsAndAllowsIdenticalDuplicates(t *testing.T) {
	project := "proj"
	version := ObservationVersion{VersionID: "00000000-0000-4000-8000-000000000202", ObservationSyncID: "duplicate-observation", SessionID: "duplicate-session", Type: "note", Title: "immutable", Content: "immutable", Project: &project, Scope: "project", RevisionCount: 1, CapturedAt: Now()}
	data := &ExportData{Sessions: []Session{{ID: "duplicate-session", Project: project, Directory: "/tmp", StartedAt: Now()}}, Observations: []Observation{{SyncID: "duplicate-observation", SessionID: "duplicate-session", Type: "note", Title: "immutable", Content: "immutable", Project: &project, Scope: "project", CreatedAt: Now(), UpdatedAt: Now()}}, ObservationVersions: []ObservationVersion{version}}
	s := newTestStore(t)
	if result, err := s.Import(data); err != nil || result.VersionsImported != 1 {
		t.Fatalf("initial import = %#v, %v", result, err)
	}
	if result, err := s.Import(data); err != nil || result.VersionsImported != 0 {
		t.Fatalf("identical duplicate = %#v, %v; want no-op", result, err)
	}
	conflict := version
	conflict.Content = "conflicting content"
	data.Observations = append(data.Observations, Observation{SyncID: "rolled-back-observation", SessionID: "duplicate-session", Type: "note", Title: "rollback", Content: "rollback", Project: &project, Scope: "project", CreatedAt: Now(), UpdatedAt: Now()})
	data.ObservationVersions = []ObservationVersion{conflict}
	if _, err := s.Import(data); err == nil {
		t.Fatal("conflicting duplicate import succeeded")
	}
	if _, err := s.GetObservationBySyncID("rolled-back-observation"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("observation persisted after conflicting rollback: %v", err)
	}
}

func TestImportLegacyObservationCreatesIncompleteBaseline(t *testing.T) {
	s := newTestStore(t)
	project := "proj"
	data := &ExportData{Sessions: []Session{{ID: "legacy-baseline-session", Project: project, Directory: "/tmp", StartedAt: Now()}}, Observations: []Observation{{SyncID: "legacy-baseline-observation", SessionID: "legacy-baseline-session", Type: "note", Title: "legacy", Content: "legacy", Project: &project, Scope: "project", CreatedAt: Now(), UpdatedAt: Now()}}}
	if _, err := s.Import(data); err != nil {
		t.Fatalf("legacy import: %v", err)
	}
	versions, err := s.ObservationVersions("legacy-baseline-observation", 10)
	if err != nil || len(versions) != 1 || !versions[0].IsBaseline || versions[0].HistoryComplete {
		t.Fatalf("legacy baseline = %#v, %v; want one incomplete baseline", versions, err)
	}
	updated := "updated"
	observation, err := s.GetObservationBySyncID("legacy-baseline-observation")
	if err != nil {
		t.Fatalf("get imported observation: %v", err)
	}
	if _, err := s.UpdateObservation(observation.ID, UpdateObservationParams{Title: &updated}); err != nil {
		t.Fatalf("update imported observation: %v", err)
	}
	versions, err = s.ObservationVersions("legacy-baseline-observation", 10)
	if err != nil || len(versions) != 2 || versions[0].HistoryComplete {
		t.Fatalf("history after update = %#v, %v; want incomplete history retained", versions, err)
	}
}

func TestImportLegacyUpdateExtendsExistingHistoryAsIncomplete(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("legacy-gap-session", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "legacy-gap-session", Type: "note", Title: "before", Content: "before", Project: "proj"})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	current, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	incoming := *current
	incoming.Title, incoming.Content, incoming.UpdatedAt = "legacy", "legacy", "2099-01-01 00:00:00"
	data := &ExportData{Observations: []Observation{incoming}}
	if _, err := s.Import(data); err != nil {
		t.Fatalf("newer legacy import: %v", err)
	}
	versions, err := s.ObservationVersions(current.SyncID, 10)
	gap := false
	for _, version := range versions {
		gap = gap || (version.Title == "legacy" && version.IsBaseline && !version.HistoryComplete)
	}
	if err != nil || len(versions) != 2 || !gap {
		t.Fatalf("history after legacy update = %#v, %v; want incomplete gap snapshot", versions, err)
	}
	if result, err := s.Import(data); err != nil || result.ObservationsSkippedStale != 1 {
		t.Fatalf("identical legacy replay = %#v, %v; want stale no-op", result, err)
	}
	if versions, err = s.ObservationVersions(current.SyncID, 10); err != nil || len(versions) != 2 {
		t.Fatalf("history after stale replay = %#v, %v; want no duplicate", versions, err)
	}
	local := "local"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Title: &local}); err != nil {
		t.Fatalf("local update: %v", err)
	}
	if versions, err = s.ObservationVersions(current.SyncID, 10); err != nil || len(versions) != 3 || versions[0].HistoryComplete {
		t.Fatalf("history after local update = %#v, %v; want incomplete retained", versions, err)
	}
}

func TestImportLegacyBlankSyncIDCreatesBaseline(t *testing.T) {
	s := newTestStore(t)
	project := "proj"
	data := &ExportData{Sessions: []Session{{ID: "blank-sync-session", Project: project, Directory: "/tmp", StartedAt: Now()}}, Observations: []Observation{{SessionID: "blank-sync-session", Type: "note", Title: "before", Content: "before", Project: &project, Scope: "project", CreatedAt: Now(), UpdatedAt: Now()}}}
	if _, err := s.Import(data); err != nil {
		t.Fatalf("blank SyncID import: %v", err)
	}
	observations, err := s.RecentObservations("", "project", 1)
	if err != nil || len(observations) != 1 || observations[0].SyncID == "" {
		t.Fatalf("generated observation = %#v, %v", observations, err)
	}
	after := "after"
	if _, err := s.UpdateObservation(observations[0].ID, UpdateObservationParams{Content: &after}); err != nil {
		t.Fatalf("update imported observation: %v", err)
	}
	versions, err := s.ObservationVersions(observations[0].SyncID, 10)
	if err != nil || len(versions) != 2 || versions[1].Content != "before" || versions[0].HistoryComplete {
		t.Fatalf("generated-ID history = %#v, %v", versions, err)
	}
}

func TestImportObservationVersionsPreservesWhitespaceAndMigrationIndex(t *testing.T) {
	s := newTestStore(t)
	project := "proj"
	version := ObservationVersion{VersionID: "00000000-0000-4000-8000-000000000301", ObservationSyncID: "whitespace-observation", SessionID: "whitespace-session", Type: "note", Title: " title \n", Content: " content \n", Project: &project, Scope: "project", RevisionCount: 1, CapturedAt: Now()}
	data := &ExportData{Sessions: []Session{{ID: "whitespace-session", Project: project, Directory: "/tmp", StartedAt: Now()}}, Observations: []Observation{{SyncID: version.ObservationSyncID, SessionID: version.SessionID, Type: version.Type, Title: version.Title, Content: version.Content, Project: &project, Scope: version.Scope, CreatedAt: Now(), UpdatedAt: Now()}}, ObservationVersions: []ObservationVersion{version}}
	if _, err := s.Import(data); err != nil {
		t.Fatalf("whitespace import: %v", err)
	}
	versions, err := s.ObservationVersions(version.ObservationSyncID, 10)
	if err != nil || len(versions) != 1 || versions[0].Title != version.Title || versions[0].Content != version.Content {
		t.Fatalf("whitespace version = %#v, %v", versions, err)
	}
	var name string
	if err := s.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_observation_versions_observation_complete'`).Scan(&name); err != nil || name == "" {
		t.Fatalf("history completeness index = %q, %v", name, err)
	}
}

func TestImportObservationVersionsLegacyAndAtomic(t *testing.T) {
	legacy := &ExportData{Sessions: []Session{{ID: "legacy-session", Project: "proj", Directory: "/tmp", StartedAt: Now()}}, Observations: []Observation{{SyncID: "legacy-observation", SessionID: "legacy-session", Type: "note", Title: "legacy", Content: "legacy", Scope: "project", CreatedAt: Now(), UpdatedAt: Now()}}}
	s := newTestStore(t)
	if result, err := s.Import(legacy); err != nil || result.VersionsImported != 0 {
		t.Fatalf("legacy import = %#v, %v; want unchanged compatibility", result, err)
	}

	for _, version := range []ObservationVersion{
		{VersionID: "00000000-0000-4000-8000-000000000101", ObservationSyncID: "missing-parent", SessionID: "atomic-session", Type: "note", Title: "valid", Content: "valid", Scope: "project", RevisionCount: 1, CapturedAt: Now()},
		{VersionID: "00000000-0000-4000-8000-000000000102", ObservationSyncID: "atomic-observation", SessionID: "atomic-session", Type: "note", Title: "invalid", Scope: "project", RevisionCount: 1, CapturedAt: Now()},
	} {
		data := &ExportData{Sessions: []Session{{ID: "atomic-session", Project: "proj", Directory: "/tmp", StartedAt: Now()}}, Observations: []Observation{{SyncID: "atomic-observation", SessionID: "atomic-session", Type: "note", Title: "atomic", Content: "atomic", Scope: "project", CreatedAt: Now(), UpdatedAt: Now()}}, ObservationVersions: []ObservationVersion{version}}
		if _, err := s.Import(data); err == nil {
			t.Fatal("invalid version import succeeded")
		}
		if _, err := s.GetObservationBySyncID("atomic-observation"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("atomic observation persisted after rollback: %v", err)
		}
	}
}

func TestImportMixedVersionsCreatesOnlyLegacyBaseline(t *testing.T) {
	project, at := "proj", "2099-01-01 00:00:00"
	versioned := ObservationVersion{VersionID: "00000000-0000-4000-8000-000000000501", ObservationSyncID: "mixed-versioned", SessionID: "mixed-session", Type: "note", Title: "versioned", Content: "versioned", Project: &project, Scope: "project", RevisionCount: 1, CapturedAt: at}
	data := &ExportData{Sessions: []Session{{ID: "mixed-session", Project: project, Directory: "/tmp", StartedAt: at}}, Observations: []Observation{{SyncID: versioned.ObservationSyncID, SessionID: versioned.SessionID, Type: "note", Title: "versioned", Content: "versioned", Project: &project, Scope: "project", CreatedAt: at, UpdatedAt: at}, {SyncID: "mixed-legacy", SessionID: versioned.SessionID, Type: "note", Title: "legacy", Content: "legacy", Project: &project, Scope: "project", CreatedAt: at, UpdatedAt: at}}, ObservationVersions: []ObservationVersion{versioned}}
	s := newTestStore(t)
	if _, err := s.Import(data); err != nil {
		t.Fatalf("mixed import: %v", err)
	}
	for _, syncID := range []string{versioned.ObservationSyncID, "mixed-legacy"} {
		observation, err := s.GetObservationBySyncID(syncID)
		if err != nil {
			t.Fatalf("get %s: %v", syncID, err)
		}
		title := "local " + syncID
		if _, err := s.UpdateObservation(observation.ID, UpdateObservationParams{Title: &title}); err != nil {
			t.Fatalf("update %s: %v", syncID, err)
		}
	}
	versionedHistory, _ := s.ObservationVersions(versioned.ObservationSyncID, 10)
	legacyHistory, _ := s.ObservationVersions("mixed-legacy", 10)
	if len(versionedHistory) != 2 || versionedHistory[1].IsBaseline || len(legacyHistory) != 2 || !legacyHistory[1].IsBaseline || legacyHistory[0].HistoryComplete {
		t.Fatalf("mixed histories = versioned %#v legacy %#v", versionedHistory, legacyHistory)
	}
}
