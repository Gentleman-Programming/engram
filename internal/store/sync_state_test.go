package store

import (
	"database/sql"
	"testing"
	"time"
)

func TestCleanupForeignSyncTargetsPreservesJournalAndValidTargets(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("valid"); err != nil {
		t.Fatalf("enroll valid project: %v", err)
	}
	if err := s.EnrollProject("isolate"); err != nil {
		t.Fatalf("enroll isolated project: %v", err)
	}
	if _, err := s.DB().Exec(`INSERT INTO sync_state (target_key, lifecycle, last_enqueued_seq, reason_code, updated_at) VALUES (?, 'degraded', 77, 'isolated', datetime('now'))`, syncTargetKeyForProject("isolate")); err != nil {
		t.Fatalf("seed isolated target: %v", err)
	}
	if err := s.CreateSession("cleanup-session", "valid", "/work/valid"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	observationID, err := s.AddObservation(AddObservationParams{SessionID: "cleanup-session", Type: "decision", Title: "preserve", Content: "local observation", Project: "valid", Scope: "project"})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	for _, target := range []string{"satellite:inert", "satellite:pending"} {
		if _, err := s.DB().Exec(`INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, 'idle', datetime('now'))`, target); err != nil {
			t.Fatalf("seed foreign target %q: %v", target, err)
		}
	}
	pendingResult, err := s.DB().Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"satellite:pending", SyncEntityObservation, "preserved-mutation", SyncOpUpsert, `{"sync_id":"preserved-mutation","project":"valid"}`, SyncSourceLocal, "valid")
	if err != nil {
		t.Fatalf("seed pending mutation: %v", err)
	}
	if _, err := s.DB().Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project, acked_at, disposition, disposition_reason, disposition_evidence, disposition_at) VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'), 'quarantined', 'kept_reason', 'kept_evidence', datetime('now')), (?, ?, ?, ?, ?, ?, ?, NULL, 'pending', NULL, NULL, NULL)`, "satellite:pending", SyncEntityObservation, "preserved-metadata", SyncOpUpsert, `{}`, SyncSourceLocal, "valid", "satellite:pending", SyncEntityObservation, "local-only", SyncOpUpsert, `{"sync_id":"local-only","project":"local-only"}`, SyncSourceLocal, "local-only"); err != nil {
		t.Fatalf("seed metadata mutation: %v", err)
	}
	pendingSeq, err := pendingResult.LastInsertId()
	if err != nil {
		t.Fatalf("pending sequence: %v", err)
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER abort_foreign_cleanup BEFORE UPDATE ON sync_mutations WHEN NEW.target_key = 'cloud' BEGIN SELECT RAISE(ABORT, 'forced cleanup failure'); END`); err != nil {
		t.Fatalf("create rollback trigger: %v", err)
	}
	if _, err := s.CleanupForeignSyncTargets(true); err == nil || scalarInt(t, s, `SELECT COUNT(*) FROM sync_state WHERE target_key LIKE 'satellite:%'`) != 2 || scalarInt(t, s, `SELECT COUNT(*) FROM sync_mutations WHERE target_key = 'satellite:pending'`) != 3 {
		t.Fatal("failed cleanup persisted a partial change")
	}
	if _, err := s.DB().Exec(`DROP TRIGGER abort_foreign_cleanup`); err != nil {
		t.Fatalf("drop rollback trigger: %v", err)
	}

	planned, err := s.CleanupForeignSyncTargets(false)
	if err != nil {
		t.Fatalf("plan cleanup: %v", err)
	}
	if planned.Applied || len(planned.Actions) != 2 || planned.Actions[1].RetargetedMutations != 1 || planned.Actions[1].RetainedMutations != 2 {
		t.Fatalf("plan=%+v", planned)
	}
	if got := scalarInt(t, s, `SELECT COUNT(*) FROM sync_state WHERE target_key LIKE 'satellite:%'`); got != 2 {
		t.Fatalf("plan changed foreign targets: %d", got)
	}

	applied, err := s.CleanupForeignSyncTargets(true)
	if err != nil {
		t.Fatalf("apply cleanup: %v", err)
	}
	if !applied.Applied || len(applied.Actions) != 2 || applied.Actions[1].RetargetedMutations != 1 || applied.Actions[1].RetainedMutations != 2 || applied.Actions[1].StateRemoved {
		t.Fatalf("applied=%+v", applied)
	}
	if got := scalarInt(t, s, `SELECT COUNT(*) FROM sync_state WHERE target_key LIKE 'satellite:%'`); got != 1 {
		t.Fatalf("foreign targets after mixed cleanup: %d", got)
	}
	var target, payload, localTarget string
	if err := s.DB().QueryRow(`SELECT target_key, payload, (SELECT target_key FROM sync_mutations WHERE entity_key = 'local-only') FROM sync_mutations WHERE entity_key = 'preserved-mutation'`).Scan(&target, &payload, &localTarget); err != nil {
		t.Fatalf("read preserved mutation: %v", err)
	}
	if target != DefaultSyncTargetKey || payload != `{"sync_id":"preserved-mutation","project":"valid"}` || localTarget != "satellite:pending" {
		t.Fatalf("mutation targets=%q,%q payload=%q", target, localTarget, payload)
	}
	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil || len(pending) != 3 || pending[len(pending)-1].Seq != pendingSeq {
		t.Fatalf("retargeted mutation is not queued: %+v, %v", pending, err)
	}
	cloud, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil || cloud.LastEnqueuedSeq != pendingSeq || cloud.LastAckedSeq != 0 {
		t.Fatalf("cloud cursor=%+v, want enqueued=%d and acknowledged=0: %v", cloud, pendingSeq, err)
	}
	var metadataTarget, disposition, reason, evidence string
	var ackedAt, dispositionAt sql.NullString
	if err := s.DB().QueryRow(`SELECT target_key, acked_at, disposition, disposition_reason, disposition_evidence, disposition_at FROM sync_mutations WHERE entity_key = 'preserved-metadata'`).Scan(&metadataTarget, &ackedAt, &disposition, &reason, &evidence, &dispositionAt); err != nil || metadataTarget != "satellite:pending" || !ackedAt.Valid || disposition != SyncMutationDispositionQuarantined || reason != "kept_reason" || evidence != "kept_evidence" || !dispositionAt.Valid {
		t.Fatalf("metadata changed target=%q ack=%v disposition=%q reason=%q evidence=%q at=%v err=%v", metadataTarget, ackedAt, disposition, reason, evidence, dispositionAt, err)
	}
	if _, err := s.GetObservation(observationID); err != nil {
		t.Fatalf("cleanup removed local observation: %v", err)
	}
	isolated, err := s.GetSyncState(syncTargetKeyForProject("isolate"))
	if err != nil || isolated.Lifecycle != SyncLifecycleDegraded || isolated.LastEnqueuedSeq != 77 || derefString(isolated.ReasonCode) != "isolated" {
		t.Fatalf("cleanup changed isolated target: %+v, %v", isolated, err)
	}

	repeated, err := s.CleanupForeignSyncTargets(true)
	if err != nil {
		t.Fatalf("repeat cleanup: %v", err)
	}
	if repeated.Applied || len(repeated.Actions) != 1 || repeated.Actions[0].RetargetedMutations != 0 || repeated.Actions[0].RetainedMutations != 2 || repeated.Actions[0].StateRemoved {
		t.Fatalf("repeat cleanup=%+v", repeated)
	}
}

func TestPolicyFailureReasonAndCloudSummaryUseProjectState(t *testing.T) {
	s := newTestStore(t)
	message := "policy denial guidance"
	if err := s.MarkSyncFailureWithReason("cloud:policy-project", "policy_forbidden", message, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("mark policy failure: %v", err)
	}
	if err := s.MarkSyncFailure("cloud", "legacy global failure", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("mark legacy failure: %v", err)
	}

	state, err := s.GetSyncState("cloud:policy-project")
	if err != nil {
		t.Fatalf("get policy state: %v", err)
	}
	if state.ReasonCode == nil || *state.ReasonCode != "policy_forbidden" {
		t.Fatalf("reason code = %v, want policy_forbidden", state.ReasonCode)
	}
	summary, err := s.CloudSyncSummary()
	if err != nil {
		t.Fatalf("cloud sync summary: %v", err)
	}
	if summary.LastError != message {
		t.Fatalf("summary error = %q, want project-scoped %q", summary.LastError, message)
	}
	if summary.ReasonCode != "policy_forbidden" {
		t.Fatalf("summary reason code = %q, want policy_forbidden", summary.ReasonCode)
	}
}

func TestCloudSyncSummaryUsesStableTargetKeyForEqualTimestamps(t *testing.T) {
	s := newTestStore(t)
	fixedTime := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := s.MarkSyncFailureWithReason("cloud:bravo", "transport_failed", "bravo failure", fixedTime); err != nil {
		t.Fatalf("mark bravo failure: %v", err)
	}
	if err := s.MarkSyncFailureWithReason("cloud:alpha", "policy_forbidden", "alpha failure", fixedTime); err != nil {
		t.Fatalf("mark alpha failure: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE sync_state SET updated_at = ? WHERE target_key LIKE 'cloud:%'`, "2026-01-02 03:04:05"); err != nil {
		t.Fatalf("set equal timestamps: %v", err)
	}

	summary, err := s.CloudSyncSummary()
	if err != nil {
		t.Fatalf("cloud sync summary: %v", err)
	}
	if summary.LastError != "alpha failure" {
		t.Fatalf("summary error = %q, want alpha failure", summary.LastError)
	}
	if summary.ReasonCode != "policy_forbidden" {
		t.Fatalf("summary reason code = %q, want policy_forbidden", summary.ReasonCode)
	}
}

func TestMarkSyncFailureWithReasonDefaultsWhitespaceReasonCode(t *testing.T) {
	s := newTestStore(t)
	fixedTime := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	if err := s.MarkSyncFailureWithReason("cloud:policy-project", " \t ", "sync failed", fixedTime); err != nil {
		t.Fatalf("mark sync failure: %v", err)
	}

	state, err := s.GetSyncState("cloud:policy-project")
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.ReasonCode == nil || *state.ReasonCode != "transport_failed" {
		t.Fatalf("reason code = %v, want transport_failed", state.ReasonCode)
	}
}

// TestInboxLifecycleSettersAreNoOps pins the reserved cloud inbox target against
// every Mark* lifecycle setter: none of them may transition the row away from
// the fixed inbox lifecycle or record failure state on it.
func TestInboxLifecycleSettersAreNoOps(t *testing.T) {
	s := newTestStore(t)
	backoff := time.Now().Add(time.Minute)
	setters := []struct {
		name string
		call func() error
	}{
		{name: "healthy", call: func() error { return s.MarkSyncHealthy(SyncInboxTargetKey) }},
		{name: "pending", call: func() error { return s.MarkSyncPending(SyncInboxTargetKey) }},
		{name: "failure", call: func() error { return s.MarkSyncFailure(SyncInboxTargetKey, "inbox boom", backoff) }},
		{name: "failure with reason", call: func() error {
			return s.MarkSyncFailureWithReason(SyncInboxTargetKey, "transport_failed", "inbox boom", backoff)
		}},
		{name: "blocked", call: func() error { return s.MarkSyncBlocked(SyncInboxTargetKey, "paused", "inbox paused") }},
		{name: "paused", call: func() error { return s.MarkSyncPaused(SyncInboxTargetKey, "inbox paused") }},
		{name: "auth required", call: func() error { return s.MarkSyncAuthRequired(SyncInboxTargetKey, "inbox auth") }},
		{name: "uppercase variant", call: func() error { return s.MarkSyncHealthy("Cloud:Inbox") }},
	}
	for _, setter := range setters {
		if err := setter.call(); err != nil {
			t.Fatalf("%s on inbox target: %v", setter.name, err)
		}
	}

	var lifecycle string
	var consecutiveFailures int
	var reasonCode, lastError, lastSuccess sql.NullString
	if err := s.db.QueryRow(`
		SELECT lifecycle, consecutive_failures, reason_code, last_error, last_success_at
		FROM sync_state WHERE target_key = ?`, SyncInboxTargetKey).
		Scan(&lifecycle, &consecutiveFailures, &reasonCode, &lastError, &lastSuccess); err != nil {
		t.Fatalf("load inbox state: %v", err)
	}
	if lifecycle != SyncLifecycleInbox || consecutiveFailures != 0 || reasonCode.Valid || lastError.Valid || lastSuccess.Valid {
		t.Fatalf("inbox row drifted: lifecycle=%q consecutive_failures=%d reason=%v last_error=%v last_success=%v",
			lifecycle, consecutiveFailures, reasonCode, lastError, lastSuccess)
	}
}

// TestInboxRefreshHelpersNeverTransitionLifecycle pins the refresh helpers:
// even pending journal rows attributed to the inbox target must not flip its
// lifecycle, because the row is pinned to the fixed inbox state.
func TestInboxRefreshHelpersNeverTransitionLifecycle(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		VALUES (?, ?, 'inbox-drift', ?, '{}', ?, ?)`,
		SyncInboxTargetKey, SyncEntityObservation, SyncOpUpsert, SyncSourceLocal, ReservedInboxProjectName); err != nil {
		t.Fatalf("seed inbox journal row: %v", err)
	}
	if err := s.withTx(func(tx *sql.Tx) error {
		if err := s.applySyncLifecycleTx(tx, SyncInboxTargetKey, 3); err != nil {
			return err
		}
		if err := s.refreshSyncLifecycleTx(tx, SyncInboxTargetKey); err != nil {
			return err
		}
		return s.refreshProjectSyncStateTx(tx, ReservedInboxProjectName)
	}); err != nil {
		t.Fatalf("refresh inbox lifecycle: %v", err)
	}

	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM sync_state WHERE target_key = ?`, SyncInboxTargetKey).Scan(&lifecycle); err != nil {
		t.Fatalf("load inbox state: %v", err)
	}
	if lifecycle != SyncLifecycleInbox {
		t.Fatalf("inbox lifecycle = %q after refresh, want %q", lifecycle, SyncLifecycleInbox)
	}
}
