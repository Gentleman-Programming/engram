package diagnostic

import (
	"context"
	"fmt"
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
