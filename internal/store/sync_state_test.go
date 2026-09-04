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
}
