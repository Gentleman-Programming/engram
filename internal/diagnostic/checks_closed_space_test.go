package diagnostic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// TestSyncTargetClosedSpaceCheck pins the closed-set invariant: every sync_state
// target must be one of cloud, cloud:inbox, local, or cloud:<enrolled project>.
// Foreign target rows are reported as error findings with human-decision
// guidance; the reserved inbox row and the local chunk target never flag.
func TestSyncTargetClosedSpaceCheck(t *testing.T) {
	tests := []struct {
		name              string
		seed              func(t *testing.T, s *store.Store)
		wantStatus        string
		wantReason        string
		wantFinding       bool
		wantNextStepHas   []string
		wantNextStepLacks []string
	}{
		{
			name: "closed set is ok",
			seed: func(t *testing.T, s *store.Store) {
				t.Helper()
				if err := s.CreateSession("closed-space-session", "engram", "/work/engram"); err != nil {
					t.Fatalf("CreateSession: %v", err)
				}
				if err := s.EnrollProject("engram"); err != nil {
					t.Fatalf("EnrollProject: %v", err)
				}
				if _, err := s.AddObservation(store.AddObservationParams{SessionID: "closed-space-session", Type: "decision", Title: "closed", Content: "Journals the cloud:engram target.", Project: "engram", Scope: "project"}); err != nil {
					t.Fatalf("AddObservation: %v", err)
				}
				// The local chunk target is a legitimate sync_state row.
				if _, err := s.GetSyncState(store.LocalChunkTargetKey); err != nil {
					t.Fatalf("ensure local target row: %v", err)
				}
			},
			wantStatus: StatusOK,
			wantReason: CheckSyncTargetClosedSpace + "_ok",
		},
		{
			name: "phantom project target errors",
			seed: func(t *testing.T, s *store.Store) {
				t.Helper()
				if _, err := s.DB().Exec(`INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES ('cloud:unenrolled', 'idle', datetime('now'))`); err != nil {
					t.Fatalf("seed phantom project target: %v", err)
				}
				// A pending journal row attributed to the phantom project makes
				// the unacked evidence non-trivial.
				if _, err := s.DB().Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, 'obs-phantom', ?, '{}', ?, 'unenrolled')`,
					store.DefaultSyncTargetKey, store.SyncEntityObservation, store.SyncOpUpsert, store.SyncSourceLocal); err != nil {
					t.Fatalf("seed phantom journal row: %v", err)
				}
			},
			wantStatus:      StatusError,
			wantReason:      ReasonForeignSyncTarget,
			wantFinding:     true,
			wantNextStepHas: []string{"Review the 1 unacknowledged mutation(s)", "cloud enroll"},
		},
		{
			name: "totally foreign key errors",
			seed: func(t *testing.T, s *store.Store) {
				t.Helper()
				if _, err := s.DB().Exec(`INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES ('satellite:foo', 'degraded', datetime('now'))`); err != nil {
					t.Fatalf("seed foreign target: %v", err)
				}
				if _, err := s.DB().Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES ('satellite:foo', ?, 'obs-foreign', ?, '{}', ?, '')`,
					store.SyncEntityObservation, store.SyncOpUpsert, store.SyncSourceLocal); err != nil {
					t.Fatalf("seed foreign journal row: %v", err)
				}
			},
			wantStatus:      StatusError,
			wantReason:      ReasonForeignSyncTarget,
			wantFinding:     true,
			wantNextStepHas: []string{"Review the 1 unacknowledged mutation(s)", "repair workflow"},
		},
		{
			name: "inert foreign row with zero pending mutations keeps the no-action guidance",
			seed: func(t *testing.T, s *store.Store) {
				t.Helper()
				// No journal rows at all: the foreign row is inert drift.
				if _, err := s.DB().Exec(`INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES ('satellite:dormant', 'idle', datetime('now'))`); err != nil {
					t.Fatalf("seed dormant foreign target: %v", err)
				}
			},
			wantStatus:        StatusError,
			wantReason:        ReasonForeignSyncTarget,
			wantFinding:       true,
			wantNextStepHas:   []string{"inert drift", "no action is required"},
			wantNextStepLacks: []string{"Review the", "repair workflow"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newDiagnosticTestStore(t)
			if tc.seed != nil {
				tc.seed(t, s)
			}

			report, err := NewRunner().RunOne(context.Background(), Scope{Store: s}, CheckSyncTargetClosedSpace)
			if err != nil {
				t.Fatalf("RunOne: %v", err)
			}
			if len(report.Checks) != 1 {
				t.Fatalf("expected exactly one check result, got %+v", report.Checks)
			}
			check := report.Checks[0]
			if report.Status != tc.wantStatus || check.Result != tc.wantStatus || check.ReasonCode != tc.wantReason {
				t.Fatalf("status=%s reason=%s, want status=%s reason=%s", report.Status, check.ReasonCode, tc.wantStatus, tc.wantReason)
			}
			if tc.wantFinding {
				if len(check.Findings) != 1 {
					t.Fatalf("expected one finding, got %+v", check.Findings)
				}
				finding := check.Findings[0]
				if finding.Severity != SeverityError || finding.ReasonCode != ReasonForeignSyncTarget || !finding.RequiresConfirmation {
					t.Fatalf("finding=%+v", finding)
				}
				var evidence map[string]any
				if err := json.Unmarshal(finding.Evidence, &evidence); err != nil {
					t.Fatalf("decode evidence: %v", err)
				}
				if _, ok := evidence["target_key"]; !ok {
					t.Fatalf("evidence missing target_key: %s", finding.Evidence)
				}
				if _, ok := evidence["lifecycle"]; !ok {
					t.Fatalf("evidence missing lifecycle: %s", finding.Evidence)
				}
				if _, ok := evidence["unacked_mutations"]; !ok {
					t.Fatalf("evidence missing unacked_mutations: %s", finding.Evidence)
				}
				for _, want := range tc.wantNextStepHas {
					if !strings.Contains(finding.SafeNextStep, want) {
						t.Fatalf("safe_next_step missing %q: %s", want, finding.SafeNextStep)
					}
				}
				for _, banned := range tc.wantNextStepLacks {
					if strings.Contains(finding.SafeNextStep, banned) {
						t.Fatalf("safe_next_step must not contain %q: %s", banned, finding.SafeNextStep)
					}
				}
			} else if len(check.Findings) != 0 {
				t.Fatalf("expected no findings, got %+v", check.Findings)
			}
		})
	}
}

// TestSyncTargetClosedSpaceCheckNeverFlagsReservedTargets proves the seeded
// cloud:inbox row and the local chunk target are members of the closed set even
// when no enrollment exists at all.
func TestSyncTargetClosedSpaceCheckNeverFlagsReservedTargets(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if _, err := s.GetSyncState(store.LocalChunkTargetKey); err != nil {
		t.Fatalf("ensure local target row: %v", err)
	}
	states, err := s.ListSyncStates()
	if err != nil {
		t.Fatalf("ListSyncStates: %v", err)
	}
	var sawInbox, sawLocal bool
	for _, state := range states {
		switch state.TargetKey {
		case store.SyncInboxTargetKey:
			sawInbox = true
			if state.Lifecycle != store.SyncLifecycleInbox {
				t.Fatalf("inbox lifecycle = %q, want %q", state.Lifecycle, store.SyncLifecycleInbox)
			}
		case store.LocalChunkTargetKey:
			sawLocal = true
		}
	}
	if !sawInbox || !sawLocal {
		t.Fatalf("expected seeded inbox and local rows, got %+v", states)
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s}, CheckSyncTargetClosedSpace)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if report.Status != StatusOK || len(report.Checks[0].Findings) != 0 {
		t.Fatalf("reserved targets must never flag, got %+v", report)
	}
}

// TestSyncTargetClosedSpaceReportsEveryForeignTarget pins that each foreign
// sync_state row produces its own finding, not a single collapsed report.
func TestSyncTargetClosedSpaceReportsEveryForeignTarget(t *testing.T) {
	s := newDiagnosticTestStore(t)
	for _, key := range []string{"cloud:unenrolled", "satellite:foo"} {
		if _, err := s.DB().Exec(`INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, 'idle', datetime('now'))`, key); err != nil {
			t.Fatalf("seed foreign target %s: %v", key, err)
		}
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s}, CheckSyncTargetClosedSpace)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	check := report.Checks[0]
	if len(check.Findings) != 2 {
		t.Fatalf("expected two findings, got %+v", check.Findings)
	}
	flagged := map[string]bool{}
	for _, finding := range check.Findings {
		var evidence struct {
			TargetKey string `json:"target_key"`
		}
		if err := json.Unmarshal(finding.Evidence, &evidence); err != nil {
			t.Fatalf("decode finding evidence: %v", err)
		}
		flagged[evidence.TargetKey] = true
	}
	for _, key := range []string{"cloud:unenrolled", "satellite:foo"} {
		if !flagged[key] {
			t.Fatalf("expected finding for %q, got %+v", key, flagged)
		}
	}
}

// TestSyncTargetClosedSpaceReturnsStoreReadError pins the error path: a store
// whose database no longer reads surfaces the error instead of reporting ok.
func TestSyncTargetClosedSpaceReturnsStoreReadError(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s}, CheckSyncTargetClosedSpace)
	if err == nil {
		t.Fatalf("expected a store read error, got report %+v", report)
	}
}
