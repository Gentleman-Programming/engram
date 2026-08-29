package tasks

import "testing"

func TestDeriveState_LiteralStatus(t *testing.T) {
	cases := map[string]string{
		"To Do":                 StateOpen,
		"Backlog":               StateOpen,
		"Reopened":              StateOpen,
		"in Analysis":           StateAnalysis,
		"In Develop":            StateInProgress,
		"In Progress":           StateInProgress,
		"In Code Review":        StateReview,
		"Ready for Deployment":  StateReview,
		"Verified":              StateVerified,
		"Done":                  StateDone,
		"Closed":                StateDone,
		"Pending External Data": StateBlocked,
		"Blocked":               StateBlocked,
		"Cancelled":             StateCancelled,
		"Won't Do":              StateCancelled,
	}
	for status, want := range cases {
		got, ok := DeriveState(status, "")
		if !ok {
			t.Errorf("DeriveState(%q, \"\"): expected ok=true", status)
			continue
		}
		if got != want {
			t.Errorf("DeriveState(%q, \"\") = %q, want %q", status, got, want)
		}
	}
}

func TestDeriveState_FallsBackToCategory(t *testing.T) {
	cases := map[string]string{
		"new":           StateOpen,
		"indeterminate": StateInProgress,
		"done":          StateDone,
	}
	for category, want := range cases {
		got, ok := DeriveState("Some Custom Status", category)
		if !ok || got != want {
			t.Errorf("DeriveState(unknown, %q) = (%q, %v), want (%q, true)", category, got, ok, want)
		}
	}
}

func TestDeriveState_UnrecognizedReturnsFalse(t *testing.T) {
	if _, ok := DeriveState("Some Custom Status", ""); ok {
		t.Error("expected ok=false when neither status nor category is recognized")
	}
}

func TestClosedStates(t *testing.T) {
	if !ClosedStates[StateDone] || !ClosedStates[StateCancelled] {
		t.Error("expected done and cancelled to be closed states")
	}
	if ClosedStates[StateOpen] || ClosedStates[StateInProgress] {
		t.Error("expected open/in_progress to not be closed states")
	}
}
