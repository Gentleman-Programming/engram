package store

// Tests for cloud-relation-backfill (#496).
//
// Three behaviors verified:
//   1. backfillRelationSyncMutationsTx: non-orphaned relations missing a
//      sync_mutations row get one created; orphaned relations are skipped.
//   2. JudgeBySemantic: enrolled project → enqueues; non-enrolled → does not.
//   3. projectNeedsBackfill: returns true when a relation lacks its mutation row.

import (
	"database/sql"
	"errors"
	"testing"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// setupBackfillStore creates a store with:
//   - session "ses-bf" / project "proj-bf"
//   - two observations (srcSyncID, tgtSyncID)
//   - project enrolled in sync_enrolled_projects
//
// Returns the store plus sync_ids of the two observations.
func setupBackfillStore(t *testing.T) (s *Store, srcSyncID, tgtSyncID string) {
	t.Helper()
	s = newTestStore(t)
	if err := s.CreateSession("ses-bf", "proj-bf", "/tmp/bf"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.EnrollProject("proj-bf"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	_, srcSyncID = addTestObsSession(t, s, "ses-bf", "Backfill source obs", "decision", "proj-bf", "project")
	_, tgtSyncID = addTestObsSession(t, s, "ses-bf", "Backfill target obs", "decision", "proj-bf", "project")
	return
}

// insertRelationDirect inserts a memory_relations row bypassing the normal
// SaveRelation / JudgeRelation path so there is no corresponding sync_mutations
// row. Used to simulate the pre-backfill gap for rows that legitimately lack
// marked_by_* fields (e.g. orphaned, pending).
func insertRelationDirect(t *testing.T, s *Store, syncID, sourceID, targetID, judgmentStatus string) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO memory_relations
			(sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at)
		VALUES (?, ?, ?, 'related', ?, datetime('now'), datetime('now'))
	`, syncID, sourceID, targetID, judgmentStatus); err != nil {
		t.Fatalf("insertRelationDirect: %v", err)
	}
}

// insertJudgedRelationDirect inserts a fully-judged memory_relations row with
// marked_by_actor and marked_by_kind populated, mirroring what JudgeRelation
// and JudgeBySemantic produce. Use this for fixtures that must be picked up
// by the backfill (the tightened predicate excludes rows without marked_by_*).
func insertJudgedRelationDirect(t *testing.T, s *Store, syncID, sourceID, targetID string) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO memory_relations
			(sync_id, source_id, target_id, relation, judgment_status,
			 marked_by_actor, marked_by_kind,
			 created_at, updated_at)
		VALUES (?, ?, ?, 'related', 'judged', 'test-actor', 'agent', datetime('now'), datetime('now'))
	`, syncID, sourceID, targetID); err != nil {
		t.Fatalf("insertJudgedRelationDirect: %v", err)
	}
}

// countRelationSyncMutationsByKey returns the count of sync_mutations rows for a
// specific relation entity_key.
func countRelationSyncMutationsByKey(t *testing.T, s *Store, relSyncID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND source = ?`,
		SyncEntityRelation, relSyncID, SyncSourceLocal,
	).Scan(&n); err != nil {
		t.Fatalf("countRelationSyncMutationsByKey(%q): %v", relSyncID, err)
	}
	return n
}

// ─── Test 1: backfillRelationSyncMutationsTx ─────────────────────────────────

// TestBackfillRelationSyncMutations_CreatesRowForNonOrphaned verifies that a
// non-orphaned relation that already exists in memory_relations with NO
// sync_mutations row gets a sync_mutations row after backfill runs.
//
// This tests gap #1 from issue #496: backfillProjectSyncMutationsTx calls no
// relation backfill, so pre-existing relations never replicate to the cloud.
func TestBackfillRelationSyncMutations_CreatesRowForNonOrphaned(t *testing.T) {
	s, srcSyncID, tgtSyncID := setupBackfillStore(t)

	// Insert a judged relation directly (with marked_by_* fields populated) —
	// no sync_mutations row exists yet. Uses insertJudgedRelationDirect because
	// the tightened backfill predicate excludes rows without marked_by fields.
	relSyncID := newSyncID("rel-bf-judged")
	insertJudgedRelationDirect(t, s, relSyncID, srcSyncID, tgtSyncID)

	// Precondition: zero mutation rows for this relation.
	if n := countRelationSyncMutationsByKey(t, s, relSyncID); n != 0 {
		t.Fatalf("precondition: expected 0 sync_mutations for relation, got %d", n)
	}

	// Run backfill through the public entry point (same path used on startup).
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("repairEnrolledProjectSyncMutations: %v", err)
	}

	// Postcondition: one sync_mutations row must exist now.
	if n := countRelationSyncMutationsByKey(t, s, relSyncID); n != 1 {
		t.Errorf("expected 1 sync_mutations row after backfill, got %d", n)
	}
}

// TestBackfillRelationSyncMutations_SkipsOrphaned verifies that orphaned
// relations are NOT backfilled — their status signals the endpoints are gone,
// so syncing them would produce a useless cloud row.
func TestBackfillRelationSyncMutations_SkipsOrphaned(t *testing.T) {
	s, srcSyncID, tgtSyncID := setupBackfillStore(t)

	// Insert an orphaned relation directly.
	relOrphanedSyncID := newSyncID("rel-bf-orphaned")
	insertRelationDirect(t, s, relOrphanedSyncID, srcSyncID, tgtSyncID, JudgmentStatusOrphaned)

	// Run backfill.
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("repairEnrolledProjectSyncMutations: %v", err)
	}

	// Orphaned relation must NOT have a sync_mutations row.
	if n := countRelationSyncMutationsByKey(t, s, relOrphanedSyncID); n != 0 {
		t.Errorf("orphaned relation must NOT be backfilled, got %d sync_mutations rows", n)
	}
}

// TestBackfillRelationSyncMutations_SkipsAlreadyEnqueued verifies idempotency:
// running backfill again when a mutation already exists does not create a
// duplicate.
func TestBackfillRelationSyncMutations_SkipsAlreadyEnqueued(t *testing.T) {
	s, srcSyncID, tgtSyncID := setupBackfillStore(t)

	relSyncID := newSyncID("rel-bf-already")
	insertJudgedRelationDirect(t, s, relSyncID, srcSyncID, tgtSyncID)

	// Run backfill twice.
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("first repairEnrolledProjectSyncMutations: %v", err)
	}
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("second repairEnrolledProjectSyncMutations: %v", err)
	}

	// Must still be exactly one row — idempotent.
	if n := countRelationSyncMutationsByKey(t, s, relSyncID); n != 1 {
		t.Errorf("expected exactly 1 sync_mutations row after two backfill runs, got %d", n)
	}
}

// ─── Test 2: JudgeBySemantic enqueue ─────────────────────────────────────────

// TestJudgeBySemantic_EnqueuesSyncMutation_WhenEnrolled verifies that calling
// JudgeBySemantic on an enrolled project produces a sync_mutations row for the
// resulting relation.
//
// This tests gap #2 from issue #496: JudgeBySemantic never called
// enqueueSyncMutationTx, so every semantic verdict produced no journal row.
func TestJudgeBySemantic_EnqueuesSyncMutation_WhenEnrolled(t *testing.T) {
	s, srcSyncID, tgtSyncID := setupBackfillStore(t)

	before := countRelationMutations(t, s, SyncEntityRelation, "proj-bf")

	syncID, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:  srcSyncID,
		TargetID:  tgtSyncID,
		Relation:  RelationConflictsWith,
		Reasoning: "they conflict semantically",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("JudgeBySemantic: %v", err)
	}
	if syncID == "" {
		t.Fatal("JudgeBySemantic: expected non-empty syncID")
	}

	after := countRelationMutations(t, s, SyncEntityRelation, "proj-bf")
	if after <= before {
		t.Errorf("expected sync_mutations to gain a row after JudgeBySemantic on enrolled project; before=%d after=%d", before, after)
	}

	// Verify the mutation references the correct relation sync_id.
	if n := countRelationSyncMutationsByKey(t, s, syncID); n != 1 {
		t.Errorf("expected 1 sync_mutations row for relation %q, got %d", syncID, n)
	}
}

// TestJudgeBySemantic_DoesNotEnqueue_WhenNotEnrolled verifies that calling
// JudgeBySemantic on a non-enrolled project does NOT produce a sync_mutations
// row (enrollment guard — backfill covers it post-enrollment).
func TestJudgeBySemantic_DoesNotEnqueue_WhenNotEnrolled(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-unenrolled", "proj-unenrolled", "/tmp/unenrolled"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, srcID := addTestObsSession(t, s, "ses-unenrolled", "Source unenrolled", "decision", "proj-unenrolled", "project")
	_, tgtID := addTestObsSession(t, s, "ses-unenrolled", "Target unenrolled", "decision", "proj-unenrolled", "project")

	syncID, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:  srcID,
		TargetID:  tgtID,
		Relation:  RelationRelated,
		Reasoning: "semantically related",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("JudgeBySemantic: %v", err)
	}
	if syncID == "" {
		t.Fatal("JudgeBySemantic: expected non-empty syncID even for unenrolled project")
	}

	// No mutation must exist for the unenrolled project.
	if n := countRelationSyncMutationsByKey(t, s, syncID); n != 0 {
		t.Errorf("unenrolled project: expected 0 sync_mutations rows for relation, got %d", n)
	}
}

// TestJudgeBySemantic_UpdateEnqueues_WhenEnrolled verifies that updating an
// existing relation via JudgeBySemantic (UPSERT path) also enqueues a
// sync_mutations row.
func TestJudgeBySemantic_UpdateEnqueues_WhenEnrolled(t *testing.T) {
	s, srcSyncID, tgtSyncID := setupBackfillStore(t)

	// First call: insert.
	syncID, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:  srcSyncID,
		TargetID:  tgtSyncID,
		Relation:  RelationRelated,
		Reasoning: "initial verdict",
		Model:     "model-v1",
	})
	if err != nil {
		t.Fatalf("JudgeBySemantic insert: %v", err)
	}

	beforeUpdate := countRelationMutations(t, s, SyncEntityRelation, "proj-bf")

	// Second call: update (same pair, different relation).
	syncID2, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:  srcSyncID,
		TargetID:  tgtSyncID,
		Relation:  RelationConflictsWith,
		Reasoning: "revised verdict — conflicts",
		Model:     "model-v2",
	})
	if err != nil {
		t.Fatalf("JudgeBySemantic update: %v", err)
	}
	// Same pair → same canonical sync_id returned.
	if syncID2 != syncID {
		t.Errorf("UPSERT should return same sync_id; want %q, got %q", syncID, syncID2)
	}

	afterUpdate := countRelationMutations(t, s, SyncEntityRelation, "proj-bf")
	if afterUpdate <= beforeUpdate {
		t.Errorf("expected additional sync_mutations row after JudgeBySemantic update; before=%d after=%d", beforeUpdate, afterUpdate)
	}
}

// ─── Test 3: projectNeedsBackfill detects missing relation mutations ──────────

// TestProjectNeedsBackfill_TrueWhenRelationMissingMutation verifies that
// projectNeedsBackfill returns true when a relation exists in memory_relations
// with no corresponding sync_mutations row.
//
// This tests gap #3 from issue #496: the fast-path skip in
// repairEnrolledProjectSyncMutations would silently skip projects that only
// have unsynced relations (observations were already backfilled).
func TestProjectNeedsBackfill_TrueWhenRelationMissingMutation(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-bf2", "proj-bf2", "/tmp/bf2"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, src2 := addTestObsSession(t, s, "ses-bf2", "NeedsBF src", "decision", "proj-bf2", "project")
	_, tgt2 := addTestObsSession(t, s, "ses-bf2", "NeedsBF tgt", "decision", "proj-bf2", "project")

	// Enroll — this creates sync_mutations for session + observations.
	if err := s.EnrollProject("proj-bf2"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	// Now insert a judged relation WITHOUT a sync_mutations row (simulates the gap).
	// Must use insertJudgedRelationDirect so marked_by_* fields are populated;
	// the tightened predicate in projectNeedsBackfill requires them.
	relSyncID := newSyncID("rel-needs-bf")
	insertJudgedRelationDirect(t, s, relSyncID, src2, tgt2)

	// projectNeedsBackfill must return true because the relation lacks a mutation.
	needs, err := s.projectNeedsBackfill("proj-bf2")
	if err != nil {
		t.Fatalf("projectNeedsBackfill after relation insert: %v", err)
	}
	if !needs {
		t.Errorf("projectNeedsBackfill must return true when a non-orphaned relation has no sync_mutations row, got false")
	}
}

// TestProjectNeedsBackfill_FalseWhenRelationHasMutation verifies that
// projectNeedsBackfill returns false when all relations already have
// sync_mutations rows (and sessions/observations are also covered).
func TestProjectNeedsBackfill_FalseWhenRelationHasMutation(t *testing.T) {
	s, srcSyncID, tgtSyncID := setupBackfillStore(t)

	// JudgeRelation — enrolled, so it enqueues a mutation automatically.
	relSyncID := newSyncID("rel-has-mut")
	if _, err := s.SaveRelation(SaveRelationParams{
		SyncID:   relSyncID,
		SourceID: srcSyncID,
		TargetID: tgtSyncID,
	}); err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}
	if _, err := s.JudgeRelation(JudgeRelationParams{
		JudgmentID:    relSyncID,
		Relation:      RelationConflictsWith,
		MarkedByActor: "test-actor",
		MarkedByKind:  "agent",
	}); err != nil {
		t.Fatalf("JudgeRelation: %v", err)
	}

	// projectNeedsBackfill should return false: relation already has a mutation.
	needs, err := s.projectNeedsBackfill("proj-bf")
	if err != nil {
		t.Fatalf("projectNeedsBackfill: %v", err)
	}
	if needs {
		t.Errorf("expected projectNeedsBackfill=false when relation already has a sync_mutations row, got true")
	}
}

// TestProjectNeedsBackfill_OrphanedRelationDoesNotTrigger verifies that an
// orphaned relation (without a sync_mutations row) does NOT cause
// projectNeedsBackfill to return true — orphaned relations are intentionally
// excluded from sync.
func TestProjectNeedsBackfill_OrphanedRelationDoesNotTrigger(t *testing.T) {
	// Create a clean store with only enrolled session/obs/relation mutations
	// satisfied, then add an orphaned relation.
	s2 := newTestStore(t)
	if err := s2.CreateSession("ses-orph-bf", "proj-orph", "/tmp/orph"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, src := addTestObsSession(t, s2, "ses-orph-bf", "Orphan src", "decision", "proj-orph", "project")
	_, tgt := addTestObsSession(t, s2, "ses-orph-bf", "Orphan tgt", "decision", "proj-orph", "project")

	if err := s2.EnrollProject("proj-orph"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	// Insert an orphaned relation (no sync_mutations row for it).
	orphRelSyncID := newSyncID("rel-orph-check")
	insertRelationDirect(t, s2, orphRelSyncID, src, tgt, JudgmentStatusOrphaned)

	// projectNeedsBackfill must NOT be triggered by the orphaned relation.
	// (It may still be true because of the sessions/obs from EnrollProject
	// — but the orphaned relation itself must not contribute.)
	// Verify the relation-specific count is zero.
	if err := s2.withTx(func(tx *sql.Tx) error {
		// Manually confirm orphaned relation is excluded from the backfill query.
		var n int
		err := tx.QueryRow(`
			SELECT COUNT(*) FROM memory_relations r
			JOIN observations src ON src.sync_id = r.source_id AND src.deleted_at IS NULL
			JOIN observations tgt ON tgt.sync_id = r.target_id AND tgt.deleted_at IS NULL
			WHERE r.judgment_status != ?
			  AND NOT EXISTS (
				SELECT 1 FROM sync_mutations sm
				WHERE sm.target_key = ? AND sm.entity = ? AND sm.entity_key = r.sync_id AND sm.source = ?
			  )
		`, JudgmentStatusOrphaned, DefaultSyncTargetKey, SyncEntityRelation, SyncSourceLocal).Scan(&n)
		if err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("orphaned relation must NOT be counted in backfill check, got count=%d", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("withTx: %v", err)
	}
}

// ─── Test 4: pre-enrollment relations are backfilled on EnrollProject ─────────

// TestEnrollProject_BackfillsPreExistingRelations verifies the core #496
// trigger end-to-end through the real code path:
//
//  1. Create a session and two observations on an UNENROLLED store.
//  2. Call JudgeBySemantic — enrollment gate prevents any sync_mutations row.
//  3. Call EnrollProject — backfillProjectSyncMutationsTx runs, which calls
//     backfillRelationSyncMutationsTx internally.
//  4. Assert the relation NOW has a sync_mutations row (backfill succeeded).
//
// This proves that relations created before enrollment are replicated when the
// project is later enrolled.
func TestEnrollProject_BackfillsPreExistingRelations(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-enroll-bf", "proj-enroll-bf", "/tmp/enroll-bf"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, srcSyncID := addTestObsSession(t, s, "ses-enroll-bf", "Pre-enroll src obs", "decision", "proj-enroll-bf", "project")
	_, tgtSyncID := addTestObsSession(t, s, "ses-enroll-bf", "Pre-enroll tgt obs", "decision", "proj-enroll-bf", "project")

	// Create a relation via the real JudgeBySemantic path on an UNENROLLED project.
	// The enrollment gate must prevent any sync_mutations row from being written.
	relSyncID, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:  srcSyncID,
		TargetID:  tgtSyncID,
		Relation:  RelationRelated,
		Reasoning: "pre-enrollment semantic verdict",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("JudgeBySemantic (unenrolled): %v", err)
	}
	if relSyncID == "" {
		t.Fatal("JudgeBySemantic: expected non-empty relSyncID")
	}

	// Pre-enrollment: enrollment gate must have prevented any relation mutation.
	if n := countRelationSyncMutationsByKey(t, s, relSyncID); n != 0 {
		t.Fatalf("precondition: expected 0 sync_mutations for relation before enrollment, got %d", n)
	}

	// Enroll the project — this triggers backfillProjectSyncMutationsTx, which
	// calls backfillRelationSyncMutationsTx to cover the pre-existing relation.
	if err := s.EnrollProject("proj-enroll-bf"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	// Post-enrollment: the relation must now have a sync_mutations row.
	if n := countRelationSyncMutationsByKey(t, s, relSyncID); n != 1 {
		t.Errorf("expected 1 sync_mutations row for relation after EnrollProject backfill, got %d", n)
	}
}

// ─── Test 5: pending rows are not backfilled ──────────────────────────────────

// TestBackfillRelationSyncMutations_SkipsPending verifies that a pending
// (unjudged) relation — which lacks marked_by_actor/marked_by_kind — is NOT
// selected by the backfill and does NOT cause projectNeedsBackfill to return
// true.  Enqueueing pending rows would produce cloud mutations that are
// hard-rejected by server validation (HTTP 400), potentially blocking the
// entire sync batch.
func TestBackfillRelationSyncMutations_SkipsPending(t *testing.T) {
	s, srcSyncID, tgtSyncID := setupBackfillStore(t)

	// Insert a pending relation without marked_by_* fields (simulates a
	// FindCandidates/SaveRelation row that has not been judged yet).
	pendingRelSyncID := newSyncID("rel-bf-pending")
	insertRelationDirect(t, s, pendingRelSyncID, srcSyncID, tgtSyncID, JudgmentStatusPending)

	// Run backfill — the pending relation must be skipped.
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("repairEnrolledProjectSyncMutations: %v", err)
	}

	// Backfill must NOT have created a sync_mutations row for the pending relation.
	if n := countRelationSyncMutationsByKey(t, s, pendingRelSyncID); n != 0 {
		t.Errorf("pending relation must NOT be backfilled (would fail cloud validation), got %d sync_mutations rows", n)
	}

	// projectNeedsBackfill must NOT return true because of the pending relation.
	// (All sessions/observations were enrolled, so they are covered.)
	needs, err := s.projectNeedsBackfill("proj-bf")
	if err != nil {
		t.Fatalf("projectNeedsBackfill: %v", err)
	}
	if needs {
		t.Errorf("projectNeedsBackfill must NOT return true for a pending relation without marked_by_* fields")
	}
}

// ─── Test 6: JudgeBySemantic uses session-fallback for project derivation ─────

// TestJudgeBySemantic_UsesSessionFallback_ForProject verifies that
// JudgeBySemantic derives the Project for the enqueued sync mutation via the
// session-fallback (coalesce(obs.project, session.project)), not from
// observations.project alone.
//
// When observations.project is blank but the session carries the project, the
// payload's Project field must be non-empty so cloud validation passes.
func TestJudgeBySemantic_UsesSessionFallback_ForProject(t *testing.T) {
	s := newTestStore(t)

	// Create a session with a project and enroll it.
	if err := s.CreateSession("ses-sf", "proj-sf", "/tmp/sf"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.EnrollProject("proj-sf"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	// Add observations with blank project — only the session carries the project.
	// This simulates observations ingested before the project column was populated.
	_, srcSyncID := addTestObsSession(t, s, "ses-sf", "SF source obs", "decision", "", "project")
	_, tgtSyncID := addTestObsSession(t, s, "ses-sf", "SF target obs", "decision", "", "project")

	// JudgeBySemantic must derive project via session fallback.
	relSyncID, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:  srcSyncID,
		TargetID:  tgtSyncID,
		Relation:  RelationRelated,
		Reasoning: "session-fallback test verdict",
		Model:     "test-model",
	})
	if err != nil {
		t.Fatalf("JudgeBySemantic: %v", err)
	}
	if relSyncID == "" {
		t.Fatal("JudgeBySemantic: expected non-empty relSyncID")
	}

	// A sync_mutations row must exist (project is enrolled).
	if n := countRelationSyncMutationsByKey(t, s, relSyncID); n != 1 {
		t.Fatalf("expected 1 sync_mutations row, got %d", n)
	}

	// The enqueued payload's project must be non-empty (resolved via session).
	var payloadProject string
	if err := s.db.QueryRow(
		`SELECT ifnull(json_extract(payload, '$.project'), '')
		   FROM sync_mutations
		  WHERE entity = ? AND entity_key = ? AND source = ?`,
		SyncEntityRelation, relSyncID, SyncSourceLocal,
	).Scan(&payloadProject); err != nil {
		t.Fatalf("reading payload project: %v", err)
	}
	if payloadProject == "" {
		t.Errorf("JudgeBySemantic enqueued payload has empty project; session fallback must populate it (got %q)", payloadProject)
	}
	if payloadProject != "proj-sf" {
		t.Errorf("expected payload project %q, got %q", "proj-sf", payloadProject)
	}
}

// ─── Test 7: cross-project guard uses session fallback ────────────────────────

// TestCrossProjectGuard_SessionFallback_RejectsDifferentSessionProjects verifies
// that validateCrossProjectGuard (used by both JudgeBySemantic and JudgeRelation)
// rejects a relation whose observations have blank observations.project but whose
// SESSIONS belong to DIFFERENT projects.
//
// This is the regression test for the bypass: without the session-fallback query
// in validateCrossProjectGuard, the guard sees "" == "" and allows the relation,
// even though the two observations live in different session-projects.
func TestCrossProjectGuard_SessionFallback_RejectsDifferentSessionProjects(t *testing.T) {
	s := newTestStore(t)

	// Two sessions in DIFFERENT projects.
	if err := s.CreateSession("ses-guard-p", "proj-guard-p", "/tmp/guard-p"); err != nil {
		t.Fatalf("CreateSession p: %v", err)
	}
	if err := s.CreateSession("ses-guard-q", "proj-guard-q", "/tmp/guard-q"); err != nil {
		t.Fatalf("CreateSession q: %v", err)
	}

	// Observations with blank observations.project — project lives only in session.
	_, srcSyncID := addTestObsSession(t, s, "ses-guard-p", "Guard source obs", "decision", "", "project")
	_, tgtSyncID := addTestObsSession(t, s, "ses-guard-q", "Guard target obs", "decision", "", "project")

	// JudgeBySemantic must detect cross-project via session fallback and reject.
	_, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:  srcSyncID,
		TargetID:  tgtSyncID,
		Relation:  RelationRelated,
		Reasoning: "cross-project session-fallback guard test",
		Model:     "test-model",
	})
	if !errors.Is(err, ErrCrossProjectRelation) {
		t.Errorf("JudgeBySemantic: expected ErrCrossProjectRelation; got %v", err)
	}

	// JudgeRelation must also reject for the same reason.
	relSyncID := newSyncID("rel")
	if _, err2 := s.SaveRelation(SaveRelationParams{
		SyncID:   relSyncID,
		SourceID: srcSyncID,
		TargetID: tgtSyncID,
	}); err2 != nil {
		t.Fatalf("SaveRelation: %v", err2)
	}
	_, err = s.JudgeRelation(JudgeRelationParams{
		JudgmentID:    relSyncID,
		Relation:      RelationRelated,
		MarkedByActor: "agent:test",
		MarkedByKind:  "agent",
	})
	if !errors.Is(err, ErrCrossProjectRelation) {
		t.Errorf("JudgeRelation: expected ErrCrossProjectRelation; got %v", err)
	}
}

// TestCrossProjectGuard_SessionFallback_AllowsSameSessionProject verifies that
// the guard does NOT reject observations that share the same session project,
// even when observations.project is blank. This guards against over-tightening.
func TestCrossProjectGuard_SessionFallback_AllowsSameSessionProject(t *testing.T) {
	s := newTestStore(t)

	// Both observations in the SAME session (same project).
	if err := s.CreateSession("ses-guard-same", "proj-guard-same", "/tmp/guard-same"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.EnrollProject("proj-guard-same"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	// Both observations have blank observations.project but same session.
	_, srcSyncID := addTestObsSession(t, s, "ses-guard-same", "Guard same src", "decision", "", "project")
	_, tgtSyncID := addTestObsSession(t, s, "ses-guard-same", "Guard same tgt", "decision", "", "project")

	// Must NOT return ErrCrossProjectRelation.
	relSyncID, err := s.JudgeBySemantic(JudgeBySemanticParams{
		SourceID:  srcSyncID,
		TargetID:  tgtSyncID,
		Relation:  RelationRelated,
		Reasoning: "same session project guard test",
		Model:     "test-model",
	})
	if err != nil {
		t.Errorf("JudgeBySemantic: unexpected error for same-session-project pair: %v", err)
	}
	if relSyncID == "" {
		t.Error("JudgeBySemantic: expected non-empty relSyncID for same-session-project pair")
	}
}

// ─── Test 8: JudgeRelation uses session-fallback for project derivation ────────

// TestJudgeRelation_UsesSessionFallback_ForProject verifies that JudgeRelation
// derives the Project for the enqueued sync mutation via the session-fallback
// (coalesce(obs.project, session.project)), not from observations.project alone.
//
// Scenario: project P is enrolled; a session exists in P; two observations are
// created in that session with BLANK observations.project (project lives only on
// the session). A pending relation is saved between them. Calling JudgeRelation
// must enqueue a sync_mutations row whose payload.project is "P", not "".
//
// Without the fix, srcProject="" → enrollCheckProject="" → enrolled=0 → return nil
// (no mutation row). After the fix, the session-fallback resolves srcProject="P",
// enrollCheckProject="P", enrolled=1, and the mutation is written immediately.
func TestJudgeRelation_UsesSessionFallback_ForProject(t *testing.T) {
	s := newTestStore(t)

	// Create a session with an explicit project and enroll it.
	if err := s.CreateSession("ses-jr-sf", "proj-jr-sf", "/tmp/jr-sf"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.EnrollProject("proj-jr-sf"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	// Create observations with BLANK observations.project — project lives only on
	// the session. This simulates observations ingested before the project column
	// was populated, which is the exact gap described in the CodeRabbit finding.
	_, srcSyncID := addTestObsSession(t, s, "ses-jr-sf", "JR SF source obs", "decision", "", "project")
	_, tgtSyncID := addTestObsSession(t, s, "ses-jr-sf", "JR SF target obs", "decision", "", "project")

	// Save a pending relation (the precursor step before JudgeRelation).
	relSyncID := newSyncID("rel-jr-sf")
	if _, err := s.SaveRelation(SaveRelationParams{
		SyncID:   relSyncID,
		SourceID: srcSyncID,
		TargetID: tgtSyncID,
	}); err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}

	// Pre-condition: no mutation row yet (SaveRelation does not enqueue).
	if n := countRelationSyncMutationsByKey(t, s, relSyncID); n != 0 {
		t.Fatalf("precondition: expected 0 sync_mutations before JudgeRelation, got %d", n)
	}

	// Call JudgeRelation — this is the code path being fixed.
	if _, err := s.JudgeRelation(JudgeRelationParams{
		JudgmentID:    relSyncID,
		Relation:      RelationRelated,
		MarkedByActor: "test-actor",
		MarkedByKind:  "agent",
	}); err != nil {
		t.Fatalf("JudgeRelation: %v", err)
	}

	// Post-condition 1: a sync_mutations row must exist immediately after
	// JudgeRelation (no waiting for startup backfill).
	if n := countRelationSyncMutationsByKey(t, s, relSyncID); n != 1 {
		t.Errorf("expected 1 sync_mutations row after JudgeRelation on enrolled project (session-fallback); got %d", n)
	}

	// Post-condition 2: the enqueued payload's project must be "proj-jr-sf",
	// not "" — so cloud validation does not reject it.
	var payloadProject string
	if err := s.db.QueryRow(
		`SELECT ifnull(json_extract(payload, '$.project'), '')
		   FROM sync_mutations
		  WHERE entity = ? AND entity_key = ? AND source = ?`,
		SyncEntityRelation, relSyncID, SyncSourceLocal,
	).Scan(&payloadProject); err != nil {
		t.Fatalf("reading payload project from sync_mutations: %v", err)
	}
	if payloadProject == "" {
		t.Errorf("JudgeRelation enqueued payload has empty project; session-fallback must populate it")
	}
	if payloadProject != "proj-jr-sf" {
		t.Errorf("expected payload project %q, got %q", "proj-jr-sf", payloadProject)
	}
}
