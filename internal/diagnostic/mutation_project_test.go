package diagnostic

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCompleteMutationInvalidProject(t *testing.T) {
	for _, tc := range []struct {
		project string
		cloud   bool
		invalid bool
	}{{"", true, true}, {" ", true, true}, {" demo ", true, true}, {"", false, false}, {"demo", true, false}} {
		t.Run(fmt.Sprintf("project=%q/cloud=%t", tc.project, tc.cloud), func(t *testing.T) {
			s, cfg := newDiagnosticTestStoreWithConfig(t)
			if tc.cloud {
				if err := s.EnrollProject("demo"); err != nil {
					t.Fatal(err)
				}
			}
			payload := `{"id":"manual-save","project":"demo","directory":"/synthetic","started_at":"2026-01-01 00:00:00","ownership_mode":""}`
			seedDiagnosticPendingMutation(t, cfg.DataDir, tc.project, store.SyncEntitySession, "manual-save", store.SyncOpUpsert, payload)
			t.Run("check", func(t *testing.T) {
				report, err := NewRunner().RunOne(context.Background(), Scope{Store: s}, CheckSyncMutationRequiredFields)
				if err != nil {
					t.Fatal(err)
				}
				if !tc.invalid {
					if report.Status != StatusOK {
						t.Fatalf("ordinary or valid project incorrectly blocked: %+v", report)
					}
					return
				}
				if report.Status != StatusBlocked || len(report.Checks[0].Findings) != 1 || report.Checks[0].ReasonCode != "sync_mutation_project_invalid" {
					t.Fatalf("complete payload with invalid project must block doctor: %+v", report)
				}
			})
			rows, err := s.ListPendingProjectMutations("")
			if err != nil || len(rows) != 1 || rows[0].Disposition != store.SyncMutationDispositionPending || rows[0].Payload != payload || rows[0].Project != tc.project {
				t.Fatalf("read-only diagnostic changed the journal: %+v %v", rows, err)
			}
			if !tc.invalid {
				return
			}
			t.Run("repair", func(t *testing.T) {
				dry, err := RepairSyncMutationRequiredFields(s, "", RepairModeDryRun)
				if err != nil {
					t.Fatal(err)
				}
				if len(dry.Quarantine.Actions) != 1 {
					t.Fatalf("dry repair missed invalid project: %+v", dry)
				}
				pending, err := s.ListPendingProjectMutations("")
				if err != nil || len(pending) != 1 || pending[0].Disposition != store.SyncMutationDispositionPending {
					t.Fatalf("dry-run changed the journal: %+v %v", pending, err)
				}
				applied, err := RepairSyncMutationRequiredFields(s, "", RepairModeApply)
				if err != nil {
					t.Fatal(err)
				}
				if len(applied.Quarantine.Actions) != 1 {
					t.Fatalf("repair missed invalid project: %+v", applied)
				}
				rows, err := s.ListPendingProjectMutations("")
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 || rows[0].Disposition != store.SyncMutationDispositionQuarantined || rows[0].Payload != payload || rows[0].AckedAt != nil || rows[0].DispositionEvidence == "" || rows[0].DispositionAt == nil {
					t.Fatalf("repair contract: %+v", rows)
				}
				again, err := RepairSyncMutationRequiredFields(s, "", RepairModeApply)
				if err != nil || len(again.Quarantine.Actions) != 0 {
					t.Fatalf("repair is not idempotent: %+v %v", again, err)
				}
			})
		})
	}
}

func TestCompleteMutationWithRecoverableProject(t *testing.T) {
	s, cfg := newDiagnosticTestStoreWithConfig(t)
	if err := s.CreateSession("manual-save", "demo", "/synthetic"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM sync_mutations`); err != nil {
		t.Fatal(err)
	}
	payload := `{"id":"manual-save","project":"demo","directory":"/synthetic","started_at":"2026-01-01 00:00:00","ownership_mode":""}`
	seedDiagnosticPendingMutation(t, cfg.DataDir, "", store.SyncEntitySession, "manual-save", store.SyncOpUpsert, payload)
	dry, err := RepairSyncMutationRequiredFields(s, "", RepairModeDryRun)
	if err != nil || len(dry.ProjectRepairs) != 1 {
		t.Fatalf("dry-run: %+v %v", dry, err)
	}
	before, err := s.ListPendingProjectMutations("")
	if err != nil || len(before) != 1 || before[0].Project != "" {
		t.Fatalf("dry-run wrote: %+v %v", before, err)
	}
	if _, err := RepairSyncMutationRequiredFields(s, "", RepairModeApply); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListPendingProjectMutations("")
	if err != nil || len(rows) != 1 || rows[0].Project != "demo" || rows[0].Payload != payload || rows[0].Disposition != store.SyncMutationDispositionPending || rows[0].AckedAt != nil {
		t.Fatalf("recoverable project not restored safely: %+v %v", rows, err)
	}
	again, err := RepairSyncMutationRequiredFields(s, "", RepairModeApply)
	if err != nil || len(again.ProjectRepairs) != 0 {
		t.Fatalf("repeat repair: %+v %v", again, err)
	}
}

func TestInvalidProjectScopeAndPayloadPriority(t *testing.T) {
	for _, payload := range []string{`{"id":"manual-save","project":"demo","directory":"/synthetic","ownership_mode":""}`, `{}`} {
		s, cfg := newDiagnosticTestStoreWithConfig(t)
		if err := s.EnrollProject("demo"); err != nil {
			t.Fatal(err)
		}
		seedDiagnosticPendingMutation(t, cfg.DataDir, " demo ", store.SyncEntitySession, "manual-save", store.SyncOpUpsert, payload)
		report, err := NewRunner().RunOne(context.Background(), Scope{Store: s, Project: "demo"}, CheckSyncMutationRequiredFields)
		if err != nil {
			t.Fatal(err)
		}
		want := store.ValidateSyncMutationPayload(store.SyncEntitySession, store.SyncOpUpsert, payload, "manual-save")
		if want.ReasonCode == "" {
			want.ReasonCode = "sync_mutation_project_invalid"
		}
		if report.Status != StatusBlocked || len(report.Checks[0].Findings) != 1 || report.Checks[0].ReasonCode != want.ReasonCode {
			t.Fatalf("scoped reason: %+v want %s", report, want.ReasonCode)
		}
		next := report.Checks[0].Findings[0].SafeNextStep
		if !strings.Contains(next, "engram doctor repair --check sync_mutation_required_fields --dry-run") || strings.Contains(next, "--project") {
			t.Fatalf("unscoped repair hint: %q", next)
		}
	}
}
