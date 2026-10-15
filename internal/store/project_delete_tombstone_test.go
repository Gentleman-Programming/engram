package store

import (
	"fmt"
	"testing"
	"time"
)

// TestDeleteProjectHardWritesProjectTombstone pins the regression behind
// issue #1851: `delete project --hard` must record delete evidence for the
// project itself, so the project's queued upsert mutations have evidence to
// supersede against instead of staying pending (and blocking doctor on
// sync_mutation_required_fields) forever.
func TestDeleteProjectHardWritesProjectTombstone(t *testing.T) {
	s := newTestStore(t)

	// Local upserts are only journalled while the project is enrolled (the
	// enrollment backfill materializes the rest), so the fixture mirrors the
	// issue scenario: enroll, queue mutations, then leave the project
	// unenrolled before the hard delete.
	enrollTestProject(t, s, "alpha")
	if err := s.CreateSession("s-tomb-proj-1", "alpha", "/tmp/alpha"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i, title := range []string{"obs-one", "obs-two"} {
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "s-tomb-proj-1",
			Type:      "decision",
			Title:     title,
			Content:   fmt.Sprintf("content %d", i),
			Project:   "alpha",
			Scope:     "project",
		}); err != nil {
			t.Fatalf("add observation %q: %v", title, err)
		}
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s-tomb-proj-1", Content: "prompt one", Project: "alpha"}); err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	if err := s.UnenrollProject("alpha"); err != nil {
		t.Fatalf("unenroll alpha: %v", err)
	}

	// Fixture sanity: the project has queued local upserts and no tombstones.
	var pending int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = 'alpha' AND source = 'local' AND op = 'upsert' AND disposition = 'pending' AND acked_at IS NULL`).Scan(&pending); err != nil {
		t.Fatalf("count pending mutations: %v", err)
	}
	if pending != 4 {
		t.Fatalf("fixture: expected 4 pending upsert mutations for alpha (session+2 observations+prompt), got %d", pending)
	}
	var projectTombstones int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_delete_tombstones WHERE entity = 'project'`).Scan(&projectTombstones); err != nil {
		t.Fatalf("count project tombstones: %v", err)
	}
	if projectTombstones != 0 {
		t.Fatalf("fixture: expected no project tombstones before delete, got %d", projectTombstones)
	}

	if _, err := s.DeleteProject("alpha", true); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	// The hard delete must record a project delete tombstone.
	var deletedAt string
	var hardDelete, active bool
	err := s.db.QueryRow(`SELECT deleted_at, hard_delete, active FROM sync_delete_tombstones WHERE entity = 'project' AND entity_key = 'alpha'`).Scan(&deletedAt, &hardDelete, &active)
	if err != nil {
		t.Fatalf("project tombstone after hard delete: %v", err)
	}
	if !hardDelete || !active {
		t.Fatalf("project tombstone hard_delete=%v active=%v, want both true", hardDelete, active)
	}
	if deletedAt == "" {
		t.Fatal("project tombstone has empty deleted_at")
	}

	// The documented repair path must now supersede every queued mutation.
	report, err := s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, "alpha", true)
	if err != nil {
		t.Fatalf("SupersedeUnenrolledLegacyMutations: %v", err)
	}
	if len(report.Actions) != pending {
		t.Fatalf("expected %d supersede actions, got %d (%+v)", pending, len(report.Actions), report.Actions)
	}
	var stillPending int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = 'alpha' AND source = 'local' AND op = 'upsert' AND disposition = 'pending' AND acked_at IS NULL`).Scan(&stillPending); err != nil {
		t.Fatalf("count remaining pending mutations: %v", err)
	}
	if stillPending != 0 {
		t.Fatalf("expected 0 pending mutations after repair, got %d", stillPending)
	}
}

// TestDeleteProjectTombstoneSupersedesOnlyPreDeleteMutations guards the time
// fence: a project tombstone supersedes mutations recorded before the delete,
// but a project recreated under the same name keeps journaling fresh upserts
// that must still reach the cloud.
func TestDeleteProjectTombstoneSupersedesOnlyPreDeleteMutations(t *testing.T) {
	s := newTestStore(t)

	enrollTestProject(t, s, "bravo")
	if err := s.CreateSession("s-tomb-fence-1", "bravo", "/tmp/bravo"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s-tomb-fence-1",
		Type:      "decision",
		Title:     "pre-delete observation",
		Content:   "pre-delete content",
		Project:   "bravo",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if err := s.UnenrollProject("bravo"); err != nil {
		t.Fatalf("unenroll bravo: %v", err)
	}
	if _, err := s.DeleteProject("bravo", true); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	// The project is recreated under the same name and journals a fresh upsert
	// stamped after the tombstone's deleted_at.
	recreatedAt := time.Now().UTC().Add(time.Second).Format("2006-01-02 15:04:05.000000000")
	if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		DefaultSyncTargetKey, SyncEntityObservation, "obs-recreated", SyncOpUpsert,
		`{"sync_id":"obs-recreated","type":"decision","title":"recreated","content":"recreated content","project":"bravo"}`,
		SyncSourceLocal, "bravo", recreatedAt); err != nil {
		t.Fatalf("seed recreated mutation: %v", err)
	}

	report, err := s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, "", true)
	if err != nil {
		t.Fatalf("SupersedeUnenrolledLegacyMutations: %v", err)
	}
	if len(report.Actions) != 2 {
		t.Fatalf("expected the 2 pre-delete mutations to be superseded, got %d (%+v)", len(report.Actions), report.Actions)
	}
	var disposition string
	if err := s.db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity = 'session' AND entity_key = 's-tomb-fence-1'`).Scan(&disposition); err != nil {
		t.Fatalf("read session disposition: %v", err)
	}
	if disposition != SyncMutationDispositionSuperseded {
		t.Fatalf("pre-delete session disposition = %q, want %q", disposition, SyncMutationDispositionSuperseded)
	}
	if err := s.db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity_key = 'obs-recreated'`).Scan(&disposition); err != nil {
		t.Fatalf("read recreated disposition: %v", err)
	}
	if disposition != SyncMutationDispositionPending {
		t.Fatalf("post-delete mutation disposition = %q, want pending (recreated project content must still sync)", disposition)
	}
}

// TestEnrollProjectAfterHardDeleteIgnoresProjectTombstone proves project
// tombstones are invisible to the mutation backfill: backfill scans select
// tombstones by project only, and only 'observation'/'session' rows can be
// journalled, so a 'project' row must neither fail nor loop enrollment.
func TestEnrollProjectAfterHardDeleteIgnoresProjectTombstone(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-tomb-enroll-1", "charlie", "/tmp/charlie"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s-tomb-enroll-1",
		Type:      "decision",
		Title:     "charlie observation",
		Content:   "charlie content",
		Project:   "charlie",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if _, err := s.DeleteProject("charlie", true); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	var projectTombstones int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_delete_tombstones WHERE entity = 'project' AND entity_key = 'charlie'`).Scan(&projectTombstones); err != nil {
		t.Fatalf("count project tombstones: %v", err)
	}
	if projectTombstones != 1 {
		t.Fatalf("expected exactly 1 project tombstone for charlie, got %d", projectTombstones)
	}

	if err := s.EnrollProject("charlie"); err != nil {
		t.Fatalf("EnrollProject after hard delete: %v", err)
	}
}
