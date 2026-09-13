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
