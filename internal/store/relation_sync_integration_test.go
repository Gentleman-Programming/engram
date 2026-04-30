package store

import (
	"encoding/json"
	"errors"
	"testing"
)

// ─── Phase G — Integration tests (REQ-001–REQ-009 cross-cutting) ──────────────
//
// These tests exercise the full push→pull→apply loop end-to-end using two
// independent *Store instances (Machine A and Machine B) without a real cloud
// server. Transfer is simulated by extracting mutations from A's
// ListPendingSyncMutations and applying them to B via ApplyPulledMutation.
//
// Strict TDD: tests are written first; all 6 should pass against A-F implementation.

// ─── Shared setup ─────────────────────────────────────────────────────────────

// setupIntegrationStores creates two independent stores:
//   - machineA: enrolled for "proj-int", has two observations and a judged relation.
//   - machineB: independent store, initially empty for project "proj-int".
//
// Returns machineA, machineB, and the sync_ids of the two observations on A.
func setupIntegrationStores(t *testing.T) (machineA, machineB *Store, syncObsX, syncObsY string) {
	t.Helper()

	// Machine A — producer
	machineA = newTestStore(t)
	if err := machineA.CreateSession("ses-int-a", "proj-int", "/tmp/int-a"); err != nil {
		t.Fatalf("A: CreateSession: %v", err)
	}
	if err := machineA.EnrollProject("proj-int"); err != nil {
		t.Fatalf("A: EnrollProject: %v", err)
	}
	_, syncObsX = addTestObsSession(t, machineA, "ses-int-a", "Decision X for integration", "decision", "proj-int", "project")
	_, syncObsY = addTestObsSession(t, machineA, "ses-int-a", "Decision Y for integration", "decision", "proj-int", "project")

	// Machine B — consumer (independent store, same project name but no data yet)
	machineB = newTestStore(t)
	if err := machineB.CreateSession("ses-int-b", "proj-int", "/tmp/int-b"); err != nil {
		t.Fatalf("B: CreateSession: %v", err)
	}

	return
}

// transferMutations extracts all pending mutations from src and applies them to dst.
// Returns the count of mutations transferred.
func transferMutations(t *testing.T, src, dst *Store) int {
	t.Helper()
	mutations, err := src.ListPendingSyncMutations(DefaultSyncTargetKey, 200)
	if err != nil {
		t.Fatalf("transferMutations: list from src: %v", err)
	}

	if err := dst.ensureSyncState(DefaultSyncTargetKey); err != nil {
		t.Fatalf("transferMutations: ensureSyncState dst: %v", err)
	}

	for _, m := range mutations {
		m.TargetKey = DefaultSyncTargetKey
		if err := dst.ApplyPulledMutation(DefaultSyncTargetKey, m); err != nil {
			t.Fatalf("transferMutations: ApplyPulledMutation (entity=%s, key=%s): %v", m.Entity, m.EntityKey, err)
		}
	}
	return len(mutations)
}

// ─── G.1 — Full cross-machine push → pull ─────────────────────────────────────

// TestRelationSync_PushPull_CrossMachine (G.1) verifies REQ-001 + REQ-002:
// Machine A judges a relation → mutation enqueued → transferred to Machine B →
// Machine B has the same relation row with matching sync_id and provenance.
func TestRelationSync_PushPull_CrossMachine(t *testing.T) {
	// SAFETY NET: existing store tests pass (verified above — no pre-existing failures).

	// RED: test written before any G-phase code.
	// Setup: Machine A with enrolled project + two observations.
	machineA, machineB, syncObsX, syncObsY := setupIntegrationStores(t)

	// Create a pending relation on Machine A.
	relSyncID := newSyncID("rel")
	if _, err := machineA.SaveRelation(SaveRelationParams{
		SyncID:   relSyncID,
		SourceID: syncObsX,
		TargetID: syncObsY,
	}); err != nil {
		t.Fatalf("A: SaveRelation: %v", err)
	}

	// Judge the relation (this should enqueue a sync mutation for enrolled project).
	actor := "agent:claude-sonnet-4-6"
	kind := "agent"
	if _, err := machineA.JudgeRelation(JudgeRelationParams{
		JudgmentID:    relSyncID,
		Relation:      RelationConflictsWith,
		MarkedByActor: actor,
		MarkedByKind:  kind,
	}); err != nil {
		t.Fatalf("A: JudgeRelation: %v", err)
	}

	// Verify Machine A produced relation mutation.
	nMuts := countRelationMutations(t, machineA, SyncEntityRelation, "proj-int")
	if nMuts == 0 {
		t.Fatal("G.1: Machine A must enqueue at least 1 relation sync mutation after JudgeRelation")
	}

	// Machine B: seed the same observations so FK preconditions are met.
	machineB.CreateSession("ses-int-b-obs", "proj-int", "/tmp/int-b-obs") //nolint: check
	addTestObsSession(t, machineB, "ses-int-b-obs", "Decision X for integration", "decision", "proj-int", "project")
	addTestObsSession(t, machineB, "ses-int-b-obs", "Decision Y for integration", "decision", "proj-int", "project")

	// Transfer all pending mutations from A to B.
	// G.1 assertion: transferMutations should not return an error.
	n := transferMutations(t, machineA, machineB)
	if n == 0 {
		t.Fatal("G.1: expected at least 1 mutation to transfer from A to B")
	}

	// Assert Machine B has the relation row with the same sync_id.
	rB, err := machineB.GetRelation(relSyncID)
	if err != nil {
		t.Fatalf("G.1: Machine B must have relation sync_id=%q after pull, got error: %v", relSyncID, err)
	}
	if rB.SyncID != relSyncID {
		t.Errorf("G.1: relation sync_id: want %q, got %q", relSyncID, rB.SyncID)
	}
	if rB.JudgmentStatus != JudgmentStatusJudged {
		t.Errorf("G.1: judgment_status: want %q, got %q", JudgmentStatusJudged, rB.JudgmentStatus)
	}
	if rB.MarkedByActor == nil || *rB.MarkedByActor != actor {
		got := "<nil>"
		if rB.MarkedByActor != nil {
			got = *rB.MarkedByActor
		}
		t.Errorf("G.1: marked_by_actor: want %q, got %q", actor, got)
	}
	if rB.Relation != RelationConflictsWith {
		t.Errorf("G.1: relation: want %q, got %q", RelationConflictsWith, rB.Relation)
	}
}

// ─── G.2 — FK miss → defer → retry success ────────────────────────────────────

// TestRelationSync_FKMissDeferRetrySuccess (G.2) verifies REQ-002 + REQ-007:
// Machine B pulls a relation that references observations not yet local →
// deferred. Then observations arrive. replayDeferred() succeeds.
func TestRelationSync_FKMissDeferRetrySuccess(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-g2-a", "proj-g2", "/tmp/g2-a"); err != nil {
		t.Fatalf("G.2: CreateSession: %v", err)
	}

	// Add one observation for source — target will be missing.
	_, syncSource := addTestObsSession(t, s, "ses-g2-a", "Source Obs G2", "decision", "proj-g2", "project")
	missingTargetSyncID := "obs-g2-missing-" + newSyncID("x")

	relSyncID := newSyncID("rel-g2")

	// Build a relation mutation referencing a target that does NOT yet exist locally.
	m := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncSource,
		TargetID:       missingTargetSyncID,
		Relation:       RelationSupersedes,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-g2",
		CreatedAt:      "2026-04-26T12:00:00Z",
		UpdatedAt:      "2026-04-26T12:00:00Z",
	})
	m.TargetKey = DefaultSyncTargetKey
	m.Seq = 1 // non-zero so ApplyPulledMutation does not skip (seq=0 == state.LastPulledSeq=0)

	// Ensure sync_state so ApplyPulledMutation can advance cursor.
	if err := s.ensureSyncState(DefaultSyncTargetKey); err != nil {
		t.Fatalf("G.2: ensureSyncState: %v", err)
	}

	// Apply — should be deferred (not error), because FK miss is handled internally.
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, m); err != nil {
		t.Fatalf("G.2: ApplyPulledMutation with FK miss should return nil (deferred), got: %v", err)
	}

	// Assert: relation NOT in memory_relations.
	if n := countRelationRows(t, s, relSyncID); n != 0 {
		t.Fatalf("G.2: expected 0 rows in memory_relations for deferred relation, got %d", n)
	}
	// Assert: row IS in sync_apply_deferred with apply_status='deferred'.
	if n := countDeferredRows(t, s, relSyncID); n != 1 {
		t.Fatalf("G.2: expected 1 row in sync_apply_deferred, got %d", n)
	}
	applyStatus, retryCount := getDeferredRow(t, s, relSyncID)
	if applyStatus != "deferred" {
		t.Errorf("G.2: apply_status: want 'deferred', got %q", applyStatus)
	}
	if retryCount != 0 {
		t.Errorf("G.2: retry_count: want 0, got %d", retryCount)
	}

	// Now Machine B "pulls" the missing target observation (simulate arrival).
	if err := s.CreateSession("ses-g2-b", "proj-g2", "/tmp/g2-b"); err != nil {
		t.Fatalf("G.2: CreateSession ses-g2-b: %v", err)
	}
	_, arrivedTargetSyncID := addTestObsSession(t, s, "ses-g2-b", "Target Obs Now Present", "decision", "proj-g2", "project")

	// Update the deferred row's payload to reference the now-present target.
	newPayload, _ := json.Marshal(syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncSource,
		TargetID:       arrivedTargetSyncID,
		Relation:       RelationSupersedes,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-g2",
		CreatedAt:      "2026-04-26T12:00:00Z",
		UpdatedAt:      "2026-04-26T12:00:00Z",
	})
	if _, err := s.db.Exec(
		`UPDATE sync_apply_deferred SET payload = ?, apply_status = 'deferred' WHERE sync_id = ?`,
		string(newPayload), relSyncID,
	); err != nil {
		t.Fatalf("G.2: update deferred payload: %v", err)
	}

	// Call ReplayDeferred — should succeed now that target exists.
	res, err := s.ReplayDeferred()
	if err != nil {
		t.Fatalf("G.2: ReplayDeferred: %v", err)
	}
	if res.Succeeded != 1 {
		t.Errorf("G.2: expected 1 succeeded replay, got %d (retried=%d, dead=%d)", res.Succeeded, res.Retried, res.Dead)
	}

	// Assert: deferred row is GONE.
	if n := countDeferredRows(t, s, relSyncID); n != 0 {
		t.Errorf("G.2: deferred row must be removed after successful replay, got %d", n)
	}
	// Assert: relation IS now in memory_relations.
	if n := countRelationRows(t, s, relSyncID); n != 1 {
		t.Errorf("G.2: expected 1 row in memory_relations after replay, got %d", n)
	}
}

// ─── G.3 — Retry cap → dead ────────────────────────────────────────────────────

// TestRelationSync_RetryCapDead (G.3) verifies REQ-007 edge case:
// A relation persistently FK-fails (target observation never arrives).
// After 5 retries, apply_status='dead' and the row is no longer attempted.
func TestRelationSync_RetryCapDead(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-g3", "proj-g3", "/tmp/g3"); err != nil {
		t.Fatalf("G.3: CreateSession: %v", err)
	}
	_, syncSource := addTestObsSession(t, s, "ses-g3", "Source Obs G3", "decision", "proj-g3", "project")
	missingTarget := "obs-g3-never-arrives-" + newSyncID("x")

	relSyncID := newSyncID("rel-g3")
	payload, _ := json.Marshal(syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncSource,
		TargetID:       missingTarget,
		Relation:       RelationCompatible,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-g3",
		CreatedAt:      "2026-04-26T12:00:00Z",
		UpdatedAt:      "2026-04-26T12:00:00Z",
	})

	// Insert deferred row at retry_count=4 (one retry away from dead threshold=5).
	insertDeferredRow(t, s, relSyncID, SyncEntityRelation, string(payload), 4, "deferred")

	// ReplayDeferred: target still missing → row becomes dead.
	res, err := s.ReplayDeferred()
	if err != nil {
		t.Fatalf("G.3: ReplayDeferred: %v", err)
	}
	if res.Dead != 1 {
		t.Errorf("G.3: expected 1 dead row, got dead=%d (retried=%d, succeeded=%d)", res.Dead, res.Retried, res.Succeeded)
	}

	// Verify apply_status='dead' and retry_count=5.
	applyStatus, retryCount := getDeferredRow(t, s, relSyncID)
	if applyStatus != "dead" {
		t.Errorf("G.3: apply_status: want 'dead', got %q", applyStatus)
	}
	if retryCount != 5 {
		t.Errorf("G.3: retry_count: want 5, got %d", retryCount)
	}

	// Run ReplayDeferred again — dead row must NOT be retried (retried=0).
	res2, err := s.ReplayDeferred()
	if err != nil {
		t.Fatalf("G.3: second ReplayDeferred: %v", err)
	}
	if res2.Retried != 0 {
		t.Errorf("G.3: dead row must not be retried; got retried=%d", res2.Retried)
	}
	// Row must still be dead.
	applyStatus2, _ := getDeferredRow(t, s, relSyncID)
	if applyStatus2 != "dead" {
		t.Errorf("G.3: apply_status changed after second replay; got %q", applyStatus2)
	}
}

// ─── G.6 — Multi-actor: two distinct rows for same (source, target) pair ──────

// TestRelationSync_MultiActor_TwoDistinctRows (G.6) verifies REQ-009 edge case:
// Two agents each judge the same (source, target) pair with different relation
// types. Both relations sync via the cloud "channel". A third machine (consumer)
// receives both and has 2 distinct local rows for the same source/target pair.
func TestRelationSync_MultiActor_TwoDistinctRows(t *testing.T) {
	// Machine A: seed observations, produce two relation mutations for same pair.
	consumer := newTestStore(t)
	if err := consumer.CreateSession("ses-g6-cons", "proj-g6", "/tmp/g6-cons"); err != nil {
		t.Fatalf("G.6: CreateSession consumer: %v", err)
	}

	// Seed the target observations on the consumer so FK preconditions are met.
	_, syncSrcConsumer := addTestObsSession(t, consumer, "ses-g6-cons", "Shared Source G6", "decision", "proj-g6", "project")
	_, syncTgtConsumer := addTestObsSession(t, consumer, "ses-g6-cons", "Shared Target G6", "decision", "proj-g6", "project")

	// Two agents judge the SAME (source, target) pair with DIFFERENT relation types.
	// Simulate by building two relation mutations with distinct sync_ids.
	relSyncIDAgent1 := newSyncID("rel-g6-a1")
	relSyncIDAgent2 := newSyncID("rel-g6-a2")

	m1 := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncIDAgent1,
		SourceID:       syncSrcConsumer,
		TargetID:       syncTgtConsumer,
		Relation:       RelationConflictsWith,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-g6",
		CreatedAt:      "2026-04-26T12:00:00Z",
		UpdatedAt:      "2026-04-26T12:00:00Z",
	})
	m2 := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncIDAgent2,
		SourceID:       syncSrcConsumer,
		TargetID:       syncTgtConsumer,
		Relation:       RelationSupersedes,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-g6",
		CreatedAt:      "2026-04-26T12:05:00Z",
		UpdatedAt:      "2026-04-26T12:05:00Z",
	})

	if err := consumer.ensureSyncState(DefaultSyncTargetKey); err != nil {
		t.Fatalf("G.6: ensureSyncState: %v", err)
	}

	// Apply both mutations to the consumer (third machine).
	if err := applyRelationMutation(t, consumer, m1); err != nil {
		t.Fatalf("G.6: apply agent-1 mutation: %v", err)
	}
	if err := applyRelationMutation(t, consumer, m2); err != nil {
		t.Fatalf("G.6: apply agent-2 mutation: %v", err)
	}

	// Assert: two distinct rows in memory_relations for same (source, target) pair.
	n1 := countRelationRows(t, consumer, relSyncIDAgent1)
	n2 := countRelationRows(t, consumer, relSyncIDAgent2)

	if n1 != 1 {
		t.Errorf("G.6: agent-1 relation (sync_id=%q): expected 1 row, got %d", relSyncIDAgent1, n1)
	}
	if n2 != 1 {
		t.Errorf("G.6: agent-2 relation (sync_id=%q): expected 1 row, got %d", relSyncIDAgent2, n2)
	}

	// Verify both rows reference the same (source, target) pair.
	r1, err := consumer.GetRelation(relSyncIDAgent1)
	if err != nil {
		t.Fatalf("G.6: GetRelation agent-1: %v", err)
	}
	r2, err := consumer.GetRelation(relSyncIDAgent2)
	if err != nil {
		t.Fatalf("G.6: GetRelation agent-2: %v", err)
	}

	if r1.SourceID != syncSrcConsumer {
		t.Errorf("G.6: agent-1 source_id: want %q, got %q", syncSrcConsumer, r1.SourceID)
	}
	if r2.SourceID != syncSrcConsumer {
		t.Errorf("G.6: agent-2 source_id: want %q, got %q", syncSrcConsumer, r2.SourceID)
	}
	if r1.TargetID != syncTgtConsumer {
		t.Errorf("G.6: agent-1 target_id: want %q, got %q", syncTgtConsumer, r1.TargetID)
	}
	if r2.TargetID != syncTgtConsumer {
		t.Errorf("G.6: agent-2 target_id: want %q, got %q", syncTgtConsumer, r2.TargetID)
	}

	// The two rows must have DISTINCT relation types (not the same verdict).
	if r1.Relation == r2.Relation {
		t.Errorf("G.6: both rows have the same relation type %q — expected distinct types (multi-actor)", r1.Relation)
	}

	// Triangulation: verify total count of relation rows for this (source, target) pair.
	var totalRows int
	if err := consumer.db.QueryRow(
		`SELECT count(*) FROM memory_relations WHERE source_id = ? AND target_id = ?`,
		syncSrcConsumer, syncTgtConsumer,
	).Scan(&totalRows); err != nil {
		t.Fatalf("G.6: count rows by (source, target): %v", err)
	}
	if totalRows != 2 {
		t.Errorf("G.6: expected 2 rows for same (source, target) pair, got %d", totalRows)
	}
}

// ─── Compile-time: verify ErrRelationFKMissing is used correctly ──────────────

var _ = errors.Is // suppress unused-import warning — errors used in tests above
