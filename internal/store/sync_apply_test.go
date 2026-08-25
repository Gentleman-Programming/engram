package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// buildRelationMutation builds a SyncMutation for entity='relation' from a
// syncRelationPayload.
func buildRelationMutation(t *testing.T, p syncRelationPayload) SyncMutation {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("buildRelationMutation: marshal: %v", err)
	}
	return SyncMutation{
		Entity:    SyncEntityRelation,
		EntityKey: p.SyncID,
		Op:        SyncOpUpsert,
		Payload:   string(raw),
		Source:    SyncSourceRemote,
		Project:   p.Project,
	}
}

// applyRelationMutation calls applyPulledMutationTx inside a transaction.
func applyRelationMutation(t *testing.T, s *Store, m SyncMutation) error {
	t.Helper()
	return s.withTx(func(tx *sql.Tx) error {
		return s.applyPulledMutationTx(tx, m)
	})
}

// countRelationRows returns the count of rows in memory_relations with the
// given sync_id.
func countRelationRows(t *testing.T, s *Store, syncID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM memory_relations WHERE sync_id = ?`, syncID,
	).Scan(&n); err != nil {
		t.Fatalf("countRelationRows: %v", err)
	}
	return n
}

// countDeferredRows returns the count of rows in sync_apply_deferred for the
// given sync_id.
func countDeferredRows(t *testing.T, s *Store, syncID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sync_apply_deferred WHERE sync_id = ?`, syncID,
	).Scan(&n); err != nil {
		t.Fatalf("countDeferredRows: %v", err)
	}
	return n
}

// setupSyncApplyStore creates a fresh store with two sessions and two
// observations suitable for relation apply tests.
func setupSyncApplyStore(t *testing.T) (s *Store, syncObsA, syncObsB string) {
	t.Helper()
	s = newTestStore(t)
	if err := s.CreateSession("ses-apply-test", "proj-apply", "/tmp/apply"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, syncObsA = addTestObsSession(t, s, "ses-apply-test", "Obs A for apply tests", "decision", "proj-apply", "project")
	_, syncObsB = addTestObsSession(t, s, "ses-apply-test", "Obs B for apply tests", "decision", "proj-apply", "project")
	return
}

// ─── Phase C.3 — Pull-side RED tests (REQ-002, REQ-009) ──────────────────────

// C.3a — ApplyPulledRelation_InsertsWhenObsExist: both source and target
// observations exist locally → relation is upserted into memory_relations.
func TestApplyPulledRelation_InsertsWhenObsExist(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	relSyncID := newSyncID("rel")
	m := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationConflictsWith,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	if err := applyRelationMutation(t, s, m); err != nil {
		t.Fatalf("applyPulledMutationTx: %v", err)
	}

	n := countRelationRows(t, s, relSyncID)
	if n != 1 {
		t.Errorf("expected 1 row in memory_relations for sync_id=%q; got %d", relSyncID, n)
	}

	// Verify the deferred table is empty for this sync_id.
	d := countDeferredRows(t, s, relSyncID)
	if d != 0 {
		t.Errorf("expected 0 deferred rows after successful apply; got %d", d)
	}
}

// C.3b — ApplyPulledRelation_DefersOnFKMiss: target observation absent →
// row written to sync_apply_deferred; no halt; seq is ACK-able.
func TestApplyPulledRelation_DefersOnFKMiss(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	// Target does NOT exist locally.
	missingTarget := "obs-ghost-" + newSyncID("x")

	relSyncID := newSyncID("rel")
	m := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTarget,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	// applyPulledMutationTx must return ErrRelationFKMissing.
	err := applyRelationMutation(t, s, m)
	if err == nil {
		t.Fatal("expected ErrRelationFKMissing when target is absent; got nil")
	}
	if !errors.Is(err, ErrRelationFKMissing) {
		t.Errorf("expected ErrRelationFKMissing; got %v", err)
	}

	// Caller writes to sync_apply_deferred on ErrRelationFKMissing.
	// The test simulates the caller: write deferred row and verify.
	if _, werr := s.db.Exec(`
		INSERT INTO sync_apply_deferred (sync_id, entity, payload, apply_status, retry_count, first_seen_at)
		VALUES (?, 'relation', ?, 'deferred', 0, datetime('now'))
		ON CONFLICT(sync_id) DO UPDATE SET payload=excluded.payload, last_attempted_at=datetime('now')
	`, relSyncID, m.Payload); werr != nil {
		t.Fatalf("write deferred row: %v", werr)
	}

	d := countDeferredRows(t, s, relSyncID)
	if d != 1 {
		t.Errorf("expected 1 deferred row; got %d", d)
	}

	// Verify apply_status and retry_count.
	var applyStatus string
	var retryCount int
	if err2 := s.db.QueryRow(
		`SELECT apply_status, retry_count FROM sync_apply_deferred WHERE sync_id = ?`, relSyncID,
	).Scan(&applyStatus, &retryCount); err2 != nil {
		t.Fatalf("scan deferred row: %v", err2)
	}
	if applyStatus != "deferred" {
		t.Errorf("apply_status: want %q, got %q", "deferred", applyStatus)
	}
	if retryCount != 0 {
		t.Errorf("retry_count: want 0, got %d", retryCount)
	}

	// memory_relations must NOT have a row for this sync_id.
	r := countRelationRows(t, s, relSyncID)
	if r != 0 {
		t.Errorf("expected 0 rows in memory_relations for deferred relation; got %d", r)
	}
}

// C.3c — ApplyPulledRelation_IdempotentOnSyncID: pulling the same relation
// twice yields exactly one row (REQ-009, INSERT OR REPLACE on sync_id).
func TestApplyPulledRelation_IdempotentOnSyncID(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	relSyncID := newSyncID("rel")
	p := syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationCompatible,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	}
	m := buildRelationMutation(t, p)

	// First apply.
	if err := applyRelationMutation(t, s, m); err != nil {
		t.Fatalf("first applyPulledMutationTx: %v", err)
	}

	// Second apply with same sync_id.
	if err := applyRelationMutation(t, s, m); err != nil {
		t.Fatalf("second applyPulledMutationTx: %v", err)
	}

	n := countRelationRows(t, s, relSyncID)
	if n != 1 {
		t.Errorf("expected exactly 1 row after two pulls with same sync_id; got %d", n)
	}
}

// ─── Phase E store-layer helpers (REQ-007) ────────────────────────────────────

// insertDeferredRow inserts a row directly into sync_apply_deferred for test setup.
func insertDeferredRow(t *testing.T, s *Store, syncID, entity, payload string, retryCount int, applyStatus string) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred (sync_id, entity, payload, retry_count, apply_status, first_seen_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'))
	`, syncID, entity, payload, retryCount, applyStatus); err != nil {
		t.Fatalf("insertDeferredRow: %v", err)
	}
}

// getDeferredRow fetches a single deferred row's status fields.
func getDeferredRow(t *testing.T, s *Store, syncID string) (applyStatus string, retryCount int) {
	t.Helper()
	if err := s.db.QueryRow(
		`SELECT apply_status, retry_count FROM sync_apply_deferred WHERE sync_id = ?`, syncID,
	).Scan(&applyStatus, &retryCount); err != nil {
		t.Fatalf("getDeferredRow sync_id=%s: %v", syncID, err)
	}
	return applyStatus, retryCount
}

// TestReplayDeferred_RetrySucceeds: A deferred row; after the missing obs arrives
// and ReplayDeferred runs, the row is applied and removed.
func TestReplayDeferred_RetrySucceeds(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	// Missing target obs.
	missingTarget := "obs-missing-" + newSyncID("x")

	relSyncID := newSyncID("rel")
	payload, _ := json.Marshal(syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTarget,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	// Insert deferred row.
	insertDeferredRow(t, s, relSyncID, SyncEntityRelation, string(payload), 0, "deferred")

	// ReplayDeferred with missing obs → still FK miss → still deferred.
	res, err := s.ReplayDeferred()
	if err != nil {
		t.Fatalf("ReplayDeferred (first): %v", err)
	}
	if res.Retried != 1 {
		t.Errorf("retried: want 1, got %d", res.Retried)
	}
	if res.Succeeded != 0 {
		t.Errorf("succeeded: want 0 (obs still missing), got %d", res.Succeeded)
	}

	// Now add the missing observation so the FK precondition is met.
	if err := s.CreateSession("ses-b", "proj-apply", "/tmp/b"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, missingTargetSync := addTestObsSession(t, s, "ses-b", "Now Present Obs", "decision", "proj-apply", "project")
	// Update the deferred row to use the now-present sync_id.
	newPayload, _ := json.Marshal(syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTargetSync,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})
	// Update the payload in the deferred row (simulates the obs arriving).
	if _, err := s.db.Exec(
		`UPDATE sync_apply_deferred SET payload = ?, apply_status = 'deferred' WHERE sync_id = ?`,
		string(newPayload), relSyncID,
	); err != nil {
		t.Fatalf("update deferred payload: %v", err)
	}

	// ReplayDeferred now — should succeed.
	res2, err := s.ReplayDeferred()
	if err != nil {
		t.Fatalf("ReplayDeferred (second): %v", err)
	}
	if res2.Succeeded != 1 {
		t.Errorf("succeeded: want 1, got %d", res2.Succeeded)
	}

	// Row must be gone from sync_apply_deferred.
	d := countDeferredRows(t, s, relSyncID)
	if d != 0 {
		t.Errorf("deferred row still exists after successful replay; got %d rows", d)
	}
	// Row must be in memory_relations.
	r := countRelationRows(t, s, relSyncID)
	if r != 1 {
		t.Errorf("expected 1 row in memory_relations after replay; got %d", r)
	}
}

// TestReplayDeferred_DeadAtFiveRetries: retry_count=4; FK still missing → row becomes dead.
func TestReplayDeferred_DeadAtFiveRetries(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	missingTarget := "obs-ghost-" + newSyncID("x")
	relSyncID := newSyncID("rel-dead")
	payload, _ := json.Marshal(syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTarget,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	// Insert at retry_count=4 (one away from dead threshold of 5).
	insertDeferredRow(t, s, relSyncID, SyncEntityRelation, string(payload), 4, "deferred")

	res, err := s.ReplayDeferred()
	if err != nil {
		t.Fatalf("ReplayDeferred: %v", err)
	}
	if res.Dead != 1 {
		t.Errorf("dead: want 1, got %d", res.Dead)
	}

	applyStatus, retryCount := getDeferredRow(t, s, relSyncID)
	if applyStatus != "dead" {
		t.Errorf("apply_status: want 'dead', got %q", applyStatus)
	}
	if retryCount != 5 {
		t.Errorf("retry_count: want 5, got %d", retryCount)
	}
}

// TestReplayDeferred_DeadRowSkipped: a dead row must not be retried.
func TestReplayDeferred_DeadRowSkipped(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	missingTarget := "obs-ghost-dead-" + newSyncID("x")
	relSyncID := newSyncID("rel-already-dead")
	payload, _ := json.Marshal(syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTarget,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})

	insertDeferredRow(t, s, relSyncID, SyncEntityRelation, string(payload), 5, "dead")

	res, err := s.ReplayDeferred()
	if err != nil {
		t.Fatalf("ReplayDeferred: %v", err)
	}
	// Dead row must not be retried at all.
	if res.Retried != 0 {
		t.Errorf("retried: want 0 (dead row skipped), got %d", res.Retried)
	}

	// Row must still be dead.
	applyStatus, _ := getDeferredRow(t, s, relSyncID)
	if applyStatus != "dead" {
		t.Errorf("apply_status changed unexpectedly; got %q", applyStatus)
	}
}

// TestCountDeferredAndDead: 3 deferred + 1 dead → counts correct.
func TestCountDeferredAndDead(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)
	missingTarget := "obs-missing-count-" + newSyncID("x")
	makePayload := func(id string) string {
		p, _ := json.Marshal(syncRelationPayload{
			SyncID:         id,
			SourceID:       syncA,
			TargetID:       missingTarget,
			Relation:       RelationRelated,
			JudgmentStatus: JudgmentStatusJudged,
			Project:        "proj-apply",
			CreatedAt:      "2026-04-26T10:00:00Z",
			UpdatedAt:      "2026-04-26T10:00:00Z",
		})
		return string(p)
	}

	insertDeferredRow(t, s, newSyncID("r1"), SyncEntityRelation, makePayload("r1"), 0, "deferred")
	insertDeferredRow(t, s, newSyncID("r2"), SyncEntityRelation, makePayload("r2"), 1, "deferred")
	insertDeferredRow(t, s, newSyncID("r3"), SyncEntityRelation, makePayload("r3"), 2, "deferred")
	insertDeferredRow(t, s, newSyncID("r4"), SyncEntityRelation, makePayload("r4"), 5, "dead")

	deferred, dead, err := s.CountDeferredAndDead()
	if err != nil {
		t.Fatalf("CountDeferredAndDead: %v", err)
	}
	if deferred != 3 {
		t.Errorf("deferred: want 3, got %d", deferred)
	}
	if dead != 1 {
		t.Errorf("dead: want 1, got %d", dead)
	}
}

// TestApplyPulledMutation_DeferredOnFKMiss: ApplyPulledMutation for relation FK miss
// writes to sync_apply_deferred and returns nil (cursor can advance).
func TestApplyPulledMutation_DeferredOnFKMiss(t *testing.T) {
	s, syncA, _ := setupSyncApplyStore(t)

	missingTarget := "obs-missing-apply-" + newSyncID("x")
	relSyncID := newSyncID("rel-deferred-apply")

	m := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID,
		SourceID:       syncA,
		TargetID:       missingTarget,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})
	m.Seq = 42
	m.TargetKey = DefaultSyncTargetKey

	// Ensure sync_state exists so ApplyPulledMutation can advance the cursor.
	if err := s.ensureSyncState(DefaultSyncTargetKey); err != nil {
		t.Fatalf("ensureSyncState: %v", err)
	}

	// ApplyPulledMutation must return nil (deferred internally).
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, m); err != nil {
		t.Fatalf("ApplyPulledMutation: expected nil for FK miss (deferred), got %v", err)
	}

	// Deferred row must exist.
	d := countDeferredRows(t, s, relSyncID)
	if d != 1 {
		t.Errorf("expected 1 deferred row; got %d", d)
	}

	// memory_relations must NOT have the row.
	r := countRelationRows(t, s, relSyncID)
	if r != 0 {
		t.Errorf("expected 0 rows in memory_relations for deferred; got %d", r)
	}
}

// F.1 — TestApplyPulledRelation_MalformedPayload_StraightToDead: a relation
// mutation with a malformed (or incomplete) payload must go directly to
// apply_status='dead' without retries. Decode errors are not retryable.
//
// Case (a): payload is not valid JSON.
// Case (b): payload is valid JSON but missing required source_id / target_id.
func TestApplyPulledRelation_MalformedPayload_StraightToDead(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "invalid JSON",
			payload: "not valid json",
		},
		{
			name:    "missing source_id and target_id",
			payload: `{"relation_type":"conflicts"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.ensureSyncState(DefaultSyncTargetKey); err != nil {
				t.Fatalf("ensureSyncState: %v", err)
			}

			relSyncID := newSyncID("rel-malformed")
			m := SyncMutation{
				Entity:    SyncEntityRelation,
				EntityKey: relSyncID,
				Op:        SyncOpUpsert,
				Payload:   tc.payload,
				Source:    SyncSourceRemote,
				Seq:       1,
				TargetKey: DefaultSyncTargetKey,
			}

			// ApplyPulledMutation must return nil — malformed payloads are
			// ACK-ed (cursor advances) but written as dead to sync_apply_deferred.
			if err := s.ApplyPulledMutation(DefaultSyncTargetKey, m); err != nil {
				t.Fatalf("ApplyPulledMutation: expected nil for dead payload, got %v", err)
			}

			// The row must be in sync_apply_deferred with apply_status='dead'.
			var applyStatus string
			var retryCount int
			if err := s.db.QueryRow(
				`SELECT apply_status, retry_count FROM sync_apply_deferred WHERE sync_id = ?`, relSyncID,
			).Scan(&applyStatus, &retryCount); err != nil {
				t.Fatalf("scan deferred row: %v", err)
			}
			if applyStatus != "dead" {
				t.Errorf("apply_status: want %q, got %q", "dead", applyStatus)
			}
			if retryCount != 0 {
				t.Errorf("retry_count: want 0, got %d", retryCount)
			}

			// memory_relations must NOT have a row.
			r := countRelationRows(t, s, relSyncID)
			if r != 0 {
				t.Errorf("expected 0 rows in memory_relations for dead payload; got %d", r)
			}
		})
	}
}

// C.3d — ApplyPulledRelation_MultiActorSamePair: two mutations, same
// (source, target) pair but different sync_id → two distinct rows (REQ-009).
func TestApplyPulledRelation_MultiActorSamePair(t *testing.T) {
	s, syncA, syncB := setupSyncApplyStore(t)

	relSyncID1 := newSyncID("rel")
	relSyncID2 := newSyncID("rel")

	m1 := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID1,
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationConflictsWith,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:00:00Z",
		UpdatedAt:      "2026-04-26T10:00:00Z",
	})
	m2 := buildRelationMutation(t, syncRelationPayload{
		SyncID:         relSyncID2,
		SourceID:       syncA,
		TargetID:       syncB,
		Relation:       RelationRelated,
		JudgmentStatus: JudgmentStatusJudged,
		Project:        "proj-apply",
		CreatedAt:      "2026-04-26T10:05:00Z",
		UpdatedAt:      "2026-04-26T10:05:00Z",
	})

	if err := applyRelationMutation(t, s, m1); err != nil {
		t.Fatalf("apply actor-1 mutation: %v", err)
	}
	if err := applyRelationMutation(t, s, m2); err != nil {
		t.Fatalf("apply actor-2 mutation: %v", err)
	}

	n1 := countRelationRows(t, s, relSyncID1)
	n2 := countRelationRows(t, s, relSyncID2)
	if n1 != 1 {
		t.Errorf("actor-1 sync_id: expected 1 row, got %d", n1)
	}
	if n2 != 1 {
		t.Errorf("actor-2 sync_id: expected 1 row, got %d", n2)
	}
}
