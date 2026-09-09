package store

import (
	"strings"
	"testing"
	"time"
)

func TestInboxSyncStateRowIsSeededWithInboxLifecycle(t *testing.T) {
	s := newTestStore(t)
	var lifecycle string
	if err := s.db.QueryRow(`SELECT lifecycle FROM sync_state WHERE target_key = ?`, SyncInboxTargetKey).Scan(&lifecycle); err != nil {
		t.Fatalf("load inbox sync_state row: %v", err)
	}
	if lifecycle != SyncLifecycleInbox {
		t.Fatalf("inbox lifecycle = %q, want %q", lifecycle, SyncLifecycleInbox)
	}
}

// TestInboxSyncStateRowIsReseededOnLegacyDatabase proves the seed runs on every
// open, not only on fresh databases, so a store created before the inbox existed
// still receives the reserved row.
func TestInboxSyncStateRowIsReseededOnLegacyDatabase(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM sync_state WHERE target_key = ?`, SyncInboxTargetKey); err != nil {
		t.Fatalf("drop inbox row: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := New(cfg)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	var lifecycle string
	if err := reopened.db.QueryRow(`SELECT lifecycle FROM sync_state WHERE target_key = ?`, SyncInboxTargetKey).Scan(&lifecycle); err != nil {
		t.Fatalf("load reseeded inbox row: %v", err)
	}
	if lifecycle != SyncLifecycleInbox {
		t.Fatalf("reseeded inbox lifecycle = %q, want %q", lifecycle, SyncLifecycleInbox)
	}
}

func TestCloudSyncSummaryExcludesReservedInboxTarget(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("summary-project"); err != nil {
		t.Fatalf("enroll summary project: %v", err)
	}
	if err := s.MarkSyncHealthy("cloud:summary-project"); err != nil {
		t.Fatalf("mark project healthy: %v", err)
	}
	if err := s.MarkSyncFailureWithReason("cloud:summary-project", "transport_failed", "project failure", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("mark project failure: %v", err)
	}
	// The inbox row is written with raw SQL because the Mark* setters are no-ops
	// for the reserved target; the row is made maximally attractive to the
	// cloud:% aggregation to prove the exclusion, not the guards.
	if _, err := s.db.Exec(`UPDATE sync_state
		SET last_error = 'inbox failure', reason_code = 'inbox_reason', last_success_at = '2999-01-01 00:00:00', updated_at = '2999-01-01 00:00:01'
		WHERE target_key = ?`, SyncInboxTargetKey); err != nil {
		t.Fatalf("stage inbox row: %v", err)
	}

	summary, err := s.CloudSyncSummary()
	if err != nil {
		t.Fatalf("cloud sync summary: %v", err)
	}
	if summary.LastError != "project failure" {
		t.Fatalf("summary last error = %q, want project failure", summary.LastError)
	}
	if summary.ReasonCode != "transport_failed" {
		t.Fatalf("summary reason code = %q, want transport_failed", summary.ReasonCode)
	}
	if summary.LastSuccessAt == "" || strings.HasPrefix(summary.LastSuccessAt, "2999-") {
		t.Fatalf("summary last success = %q, want the project row, never the inbox row", summary.LastSuccessAt)
	}
}

func TestEnrollProjectRejectsReservedInboxProjectName(t *testing.T) {
	s := newTestStore(t)
	err := s.EnrollProject(ReservedInboxProjectName)
	if err == nil || !strings.Contains(err.Error(), "reserved for the cloud inbox") {
		t.Fatalf("EnrollProject(inbox) error = %v, want reserved-for-cloud-inbox rejection", err)
	}
	var enrolled int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_enrolled_projects`).Scan(&enrolled); err != nil || enrolled != 0 {
		t.Fatalf("enrolled projects = %d (err %v), want none after rejection", enrolled, err)
	}

	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("EnrollProject(engram): %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_enrolled_projects`).Scan(&enrolled); err != nil || enrolled != 1 {
		t.Fatalf("enrolled projects = %d (err %v), want the normal project only", enrolled, err)
	}
}

func TestObservationAndRelationMutationsStillEnqueue(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("obs-session", "obs-project", "/tmp/obs"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	// Relation delivery is enrollment-gated, so the project must be enrolled for
	// the relation mutation to enqueue at all.
	if err := s.EnrollProject("obs-project"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	_, sourceSyncID := addTestObsSession(t, s, "obs-session", "source", "decision", "obs-project", "project")
	_, targetSyncID := addTestObsSession(t, s, "obs-session", "target", "decision", "obs-project", "project")

	var observations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND target_key = ? AND project = ? AND acked_at IS NULL`, SyncEntityObservation, DefaultSyncTargetKey, "obs-project").Scan(&observations); err != nil || observations != 2 {
		t.Fatalf("observation mutations = %d (err %v), want 2", observations, err)
	}
	cloudState, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get cloud state: %v", err)
	}
	if cloudState.Lifecycle != SyncLifecyclePending {
		t.Fatalf("cloud lifecycle = %q, want pending after observation enqueue", cloudState.Lifecycle)
	}
	projectState, err := s.GetSyncState(syncTargetKeyForProject("obs-project"))
	if err != nil {
		t.Fatalf("get project state: %v", err)
	}
	if projectState.Lifecycle != SyncLifecyclePending {
		t.Fatalf("project lifecycle = %q, want pending after observation enqueue", projectState.Lifecycle)
	}

	if _, err := s.SaveRelation(SaveRelationParams{SyncID: "obs-relation", SourceID: sourceSyncID, TargetID: targetSyncID}); err != nil {
		t.Fatalf("save relation: %v", err)
	}
	if _, err := s.JudgeRelation(JudgeRelationParams{JudgmentID: "obs-relation", Relation: RelationRelated, MarkedByActor: "test", MarkedByKind: "agent"}); err != nil {
		t.Fatalf("judge relation: %v", err)
	}
	var relations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND target_key = ?`, SyncEntityRelation, DefaultSyncTargetKey).Scan(&relations); err != nil || relations != 1 {
		t.Fatalf("relation mutations = %d (err %v), want 1", relations, err)
	}
}

func TestTelemetryStillJournalsWhileProtocolRequiresSessions(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSessionWithOwnershipMode("protocol-session", "protocol-project", "/tmp/protocol", SessionOwnershipProjectOwned); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "protocol-session", Content: "protocol prompt", Project: "protocol-project"}); err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	var sessionMutations, promptMutations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE target_key = ? AND entity = ? AND entity_key = ? AND acked_at IS NULL`, DefaultSyncTargetKey, SyncEntitySession, "protocol-session").Scan(&sessionMutations); err != nil || sessionMutations != 1 {
		t.Fatalf("session mutations = %d (err %v), want 1: sessions are load-bearing sync telemetry", sessionMutations, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE target_key = ? AND entity = ? AND acked_at IS NULL`, DefaultSyncTargetKey, SyncEntityPrompt).Scan(&promptMutations); err != nil || promptMutations != 1 {
		t.Fatalf("prompt mutations = %d (err %v), want 1: prompts are load-bearing sync telemetry", promptMutations, err)
	}
	for _, targetKey := range []string{DefaultSyncTargetKey, syncTargetKeyForProject("protocol-project")} {
		state, err := s.GetSyncState(targetKey)
		if err != nil {
			t.Fatalf("get %s state: %v", targetKey, err)
		}
		if state.Lifecycle != SyncLifecyclePending {
			t.Fatalf("%s lifecycle = %q, want pending after telemetry enqueue", targetKey, state.Lifecycle)
		}
	}
}
func TestListSyncStatesReportsLifecycleAndUnackedCounts(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("list-session", "list-project", "/tmp/list"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.EnrollProject("list-project"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	addTestObsSession(t, s, "list-session", "listed", "decision", "list-project", "project")
	if _, err := s.GetSyncState(LocalChunkTargetKey); err != nil {
		t.Fatalf("ensure local target row: %v", err)
	}

	states, err := s.ListSyncStates()
	if err != nil {
		t.Fatalf("list sync states: %v", err)
	}
	byKey := make(map[string]SyncTargetState, len(states))
	for _, state := range states {
		byKey[state.TargetKey] = state
	}
	for _, key := range []string{DefaultSyncTargetKey, SyncInboxTargetKey, LocalChunkTargetKey, "cloud:list-project"} {
		if _, ok := byKey[key]; !ok {
			t.Fatalf("sync state for %q missing from %+v", key, states)
		}
	}
	if lifecycle := byKey[SyncInboxTargetKey].Lifecycle; lifecycle != SyncLifecycleInbox {
		t.Fatalf("inbox lifecycle = %q, want %q", lifecycle, SyncLifecycleInbox)
	}
	// Sessions are load-bearing telemetry (see
	// TestTelemetryStillJournalsWhileProtocolRequiresSessions), so each target
	// tracks the session + observation pair.
	if unacked := byKey[DefaultSyncTargetKey].UnackedMutations; unacked != 2 {
		t.Fatalf("cloud unacked = %d, want 2", unacked)
	}
	if unacked := byKey["cloud:list-project"].UnackedMutations; unacked != 2 {
		t.Fatalf("cloud:list-project unacked = %d, want 2 project-scoped rows", unacked)
	}
}
