package store

import (
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
