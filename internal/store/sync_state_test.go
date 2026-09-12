package store

import (
	"database/sql"
	"testing"
	"time"
)

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
