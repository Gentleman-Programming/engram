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

// TestDeleteProjectTombstoneUsesNormalizedProjectKey pins the tombstone
// contract: the recorded project column must carry the same normalized key as
// entity_key, because projectTombstoneSupersedesMutationTx looks the tombstone
// up with the mutation's normalized project on both columns. A raw name that
// changes under NormalizeProject would otherwise strand the tombstone and
// leave pre-delete mutations pending forever.
func TestDeleteProjectTombstoneUsesNormalizedProjectKey(t *testing.T) {
	s := newTestStore(t)

	rawProject := "Foxtxt__Proj" // canonicalizes to "foxtxt_proj"
	normalized, _ := NormalizeProject(rawProject)
	if normalized == rawProject {
		t.Fatalf("fixture: %q must change under NormalizeProject, got %q", rawProject, normalized)
	}
	if err := s.CreateSession("s-tomb-norm-1", "foxtxt_proj", "/tmp/foxtxt_proj"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s-tomb-norm-1",
		Type:      "decision",
		Title:     "norm observation",
		Content:   "norm content",
		Project:   "foxtxt_proj",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Simulate legacy rows stored under a non-canonical project name so the
	// hard delete runs with a raw name that differs from its normalized key.
	if _, err := s.db.Exec(`UPDATE sessions SET project = ? WHERE id = ?`, rawProject, "s-tomb-norm-1"); err != nil {
		t.Fatalf("relabel session to raw name: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET project = ? WHERE session_id = ?`, rawProject, "s-tomb-norm-1"); err != nil {
		t.Fatalf("relabel observation to raw name: %v", err)
	}

	if _, err := s.DeleteProject(rawProject, true); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	var tombstoneProject string
	err := s.db.QueryRow(`SELECT project FROM sync_delete_tombstones WHERE entity = 'project' AND entity_key = ?`, normalized).Scan(&tombstoneProject)
	if err != nil {
		t.Fatalf("read project tombstone: %v", err)
	}
	if tombstoneProject != normalized {
		t.Fatalf("tombstone project = %q, want normalized key %q (entity_key and project must match)", tombstoneProject, normalized)
	}

	// End to end: a queued pre-delete mutation must now find the tombstone and
	// be superseded by the documented repair path.
	if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		DefaultSyncTargetKey, SyncEntityObservation, "obs-norm-1", SyncOpUpsert,
		`{"sync_id":"obs-norm-1","type":"decision","title":"norm","content":"norm content","project":"foxtxt_proj"}`,
		SyncSourceLocal, normalized, "2026-01-02 15:04:05.000000000"); err != nil {
		t.Fatalf("seed pre-delete mutation: %v", err)
	}
	report, err := s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, normalized, true)
	if err != nil {
		t.Fatalf("SupersedeUnenrolledLegacyMutations: %v", err)
	}
	if len(report.Actions) != 1 {
		t.Fatalf("expected the pre-delete mutation to be superseded via the normalized tombstone, got %d actions (%+v)", len(report.Actions), report.Actions)
	}
}

// TestDeleteProjectTombstoneSupersedesMutationOccurredAtSameInstant pins the
// less-than-or-equal fence: a mutation stamped at exactly the tombstone's
// deleted_at (the same-instant edge within one second) carries delete evidence
// and must be superseded.
func TestDeleteProjectTombstoneSupersedesMutationOccurredAtSameInstant(t *testing.T) {
	s := newTestStore(t)

	enrollTestProject(t, s, "golf")
	if err := s.CreateSession("s-tomb-same-1", "golf", "/tmp/golf"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s-tomb-same-1",
		Type:      "decision",
		Title:     "same-instant observation",
		Content:   "same-instant content",
		Project:   "golf",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if err := s.UnenrollProject("golf"); err != nil {
		t.Fatalf("unenroll golf: %v", err)
	}
	if _, err := s.DeleteProject("golf", true); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	var deletedAt string
	if err := s.db.QueryRow(`SELECT deleted_at FROM sync_delete_tombstones WHERE entity = 'project' AND entity_key = 'golf'`).Scan(&deletedAt); err != nil {
		t.Fatalf("read tombstone deleted_at: %v", err)
	}
	// Stamp the queued mutations at the tombstone's exact instant: this is the
	// boundary a strict less-than comparison would leave pending.
	if _, err := s.db.Exec(`UPDATE sync_mutations SET occurred_at = ? WHERE project = 'golf' AND acked_at IS NULL AND disposition = 'pending'`, deletedAt); err != nil {
		t.Fatalf("stamp mutations at deleted_at: %v", err)
	}

	report, err := s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, "golf", true)
	if err != nil {
		t.Fatalf("SupersedeUnenrolledLegacyMutations: %v", err)
	}
	if len(report.Actions) != 2 {
		t.Fatalf("expected both same-instant mutations to be superseded, got %d actions (%+v)", len(report.Actions), report.Actions)
	}
}

// TestDeleteProjectTombstoneKeepsSameSecondPostDeleteMutationPending pins the
// sub-second fence: a mutation journalled through the real enqueue path after a
// hard delete must stay pending even when its occurred_at shares a wall-clock
// second with the tombstone's deleted_at. enqueueSyncMutationWithSourceTx stamps
// occurred_at with sub-second precision; the old second-precision column default
// made such a mutation compare occurred_at <= deleted_at and lose its content.
//
// Real enqueue precision is checked before constructing temporal fixtures.
// Both a same-second collision and a valid zero-fraction timestamp are exercised
// without relying on wall-clock progress between delete and recreate.
func TestDeleteProjectTombstoneKeepsSameSecondPostDeleteMutationPending(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fraction     time.Duration
		sharesSecond bool
	}{
		{name: "same-second", fraction: time.Nanosecond, sharesSecond: true},
		{name: "zero-fraction", fraction: 0, sharesSecond: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)

			enrollTestProject(t, s, "hotel")
			if err := s.CreateSession("s-tomb-pre-1", "hotel", "/tmp/hotel"); err != nil {
				t.Fatalf("create pre-delete session: %v", err)
			}
			if _, err := s.AddObservation(AddObservationParams{
				SessionID: "s-tomb-pre-1",
				Type:      "decision",
				Title:     "pre-delete observation",
				Content:   "pre-delete content",
				Project:   "hotel",
				Scope:     "project",
			}); err != nil {
				t.Fatalf("add pre-delete observation: %v", err)
			}
			if err := s.UnenrollProject("hotel"); err != nil {
				t.Fatalf("unenroll hotel: %v", err)
			}
			if _, err := s.DeleteProject("hotel", true); err != nil {
				t.Fatalf("DeleteProject: %v", err)
			}

			// Recreate the project under the same name and journal fresh upserts right
			// after the delete through the real enqueue path.
			if err := s.EnrollProject("hotel"); err != nil {
				t.Fatalf("re-enroll hotel: %v", err)
			}
			if err := s.CreateSession("s-tomb-post-1", "hotel", "/tmp/hotel"); err != nil {
				t.Fatalf("create post-delete session: %v", err)
			}
			if _, err := s.AddObservation(AddObservationParams{
				SessionID: "s-tomb-post-1",
				Type:      "decision",
				Title:     "post-delete observation",
				Content:   "post-delete content",
				Project:   "hotel",
				Scope:     "project",
			}); err != nil {
				t.Fatalf("add post-delete observation: %v", err)
			}
			if err := s.UnenrollProject("hotel"); err != nil {
				t.Fatalf("unenroll hotel again: %v", err)
			}

			var postObservationSyncID string
			if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE session_id = 's-tomb-post-1'`).Scan(&postObservationSyncID); err != nil {
				t.Fatalf("read post-delete observation sync_id: %v", err)
			}
			postDelete := []struct {
				entity    string
				entityKey string
				occurred  string
			}{
				{entity: "session", entityKey: "s-tomb-post-1"},
				{entity: "observation", entityKey: postObservationSyncID},
			}
			for i := range postDelete {
				if err := s.db.QueryRow(`SELECT occurred_at FROM sync_mutations WHERE entity = ? AND entity_key = ?`, postDelete[i].entity, postDelete[i].entityKey).Scan(&postDelete[i].occurred); err != nil {
					t.Fatalf("read post-delete %s occurred_at: %v", postDelete[i].entity, err)
				}
				stamp, err := time.Parse("2006-01-02 15:04:05.000000000", postDelete[i].occurred)
				if err != nil || stamp.Format("2006-01-02 15:04:05.000000000") != postDelete[i].occurred {
					t.Fatalf("post-delete %s occurred_at = %q, want nine fractional digits from real enqueue (including .000000000): %v", postDelete[i].entity, postDelete[i].occurred, err)
				}
			}

			// Place fresh mutations strictly after the original delete, independent of
			// the real clock. The zero-fraction case starts exactly on a second.
			var originalDeletedAt string
			if err := s.db.QueryRow(`SELECT deleted_at FROM sync_delete_tombstones WHERE entity = 'project' AND entity_key = 'hotel'`).Scan(&originalDeletedAt); err != nil {
				t.Fatal(err)
			}
			originalDelete, err := time.Parse("2006-01-02 15:04:05.999999999", originalDeletedAt)
			if err != nil {
				t.Fatal(err)
			}
			fresh := originalDelete.Truncate(time.Second).Add(time.Second).Add(tc.fraction)
			if !fresh.After(originalDelete) {
				t.Fatal("fixture: fresh mutation must follow original delete")
			}
			for i := range postDelete {
				postDelete[i].occurred = fresh.Format("2006-01-02 15:04:05.000000000")
				if _, err := s.db.Exec(`UPDATE sync_mutations SET occurred_at = ? WHERE entity = ? AND entity_key = ?`, postDelete[i].occurred, postDelete[i].entity, postDelete[i].entityKey); err != nil {
					t.Fatal(err)
				}
			}

			// Dispositions must hold after the repair: the delete supersedes the
			// pre-delete mutation, and the recreated project's fresh upserts stay
			// pending so they can still reach the cloud.
			assertDispositions := func() {
				var disposition string
				if err := s.db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity = 'session' AND entity_key = 's-tomb-pre-1'`).Scan(&disposition); err != nil {
					t.Fatalf("read pre-delete session disposition: %v", err)
				}
				if disposition != SyncMutationDispositionSuperseded {
					t.Fatalf("pre-delete session disposition = %q, want %q", disposition, SyncMutationDispositionSuperseded)
				}
				for _, key := range postDelete {
					if err := s.db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity = ? AND entity_key = ?`, key.entity, key.entityKey).Scan(&disposition); err != nil {
						t.Fatalf("read post-delete %s disposition: %v", key.entity, err)
					}
					if disposition != SyncMutationDispositionPending {
						t.Fatalf("post-delete %s disposition = %q, want %q (recreated project content must still sync)", key.entity, disposition, SyncMutationDispositionPending)
					}
				}
			}
			assertNoPostDeleteAction := func(report SyncMutationSupersedeReport, pass string) {
				for _, action := range report.Actions {
					for _, key := range postDelete {
						if action.EntityKey == key.entityKey {
							t.Fatalf("post-delete mutation %q was superseded in the %s pass (%+v); it must stay pending", action.EntityKey, pass, action)
						}
					}
				}
			}

			// First pass: controlled post-delete timestamps.
			report, err := s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, "hotel", true)
			if err != nil {
				t.Fatalf("SupersedeUnenrolledLegacyMutations: %v", err)
			}
			assertNoPostDeleteAction(report, "controlled post-delete")
			assertDispositions()

			// Second pass: put the fence one nanosecond before the earliest fresh
			// instant. At zero fraction this must be in the preceding second, not
			// equal to fresh: the production fence deliberately uses <=.
			earliest := fresh
			for _, key := range postDelete {
				instant, err := time.Parse("2006-01-02 15:04:05.999999999", key.occurred)
				if err != nil {
					t.Fatal(err)
				}
				if instant.Before(earliest) {
					earliest = instant
				}
			}
			boundary := earliest.Add(-time.Nanosecond).Format("2006-01-02 15:04:05.000000000")
			if _, err := s.db.Exec(`UPDATE sync_delete_tombstones SET deleted_at = ? WHERE entity = 'project' AND entity_key = 'hotel'`, boundary); err != nil {
				t.Fatalf("fold project tombstone deleted_at into the mutation second: %v", err)
			}
			var deletedAt string
			if err := s.db.QueryRow(`SELECT deleted_at FROM sync_delete_tombstones WHERE entity = 'project' AND entity_key = 'hotel'`).Scan(&deletedAt); err != nil {
				t.Fatalf("read folded project tombstone deleted_at: %v", err)
			}
			fence, err := time.Parse("2006-01-02 15:04:05.999999999", deletedAt)
			if err != nil {
				t.Fatal(err)
			}
			if !fence.Equal(earliest.Add(-time.Nanosecond)) {
				t.Fatalf("project tombstone instant = %v, want %v", fence, earliest.Add(-time.Nanosecond))
			}
			for _, key := range postDelete {
				instant, err := time.Parse("2006-01-02 15:04:05.999999999", key.occurred)
				if err != nil {
					t.Fatal(err)
				}
				if !instant.After(fence) {
					t.Fatalf("post-delete %s instant %v must be strictly after fence %v", key.entity, instant, fence)
				}
				if shares := instant.Truncate(time.Second).Equal(fence.Truncate(time.Second)); shares != tc.sharesSecond {
					t.Fatalf("fixture: shares second = %v, want %v", shares, tc.sharesSecond)
				}
			}

			report, err = s.SupersedeUnenrolledLegacyMutations(DefaultSyncTargetKey, "hotel", true)
			if err != nil {
				t.Fatalf("SupersedeUnenrolledLegacyMutations after folding: %v", err)
			}
			assertNoPostDeleteAction(report, tc.name)
			assertDispositions()
		})
	}
}
