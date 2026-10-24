package diagnostic

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestProjectAliasGuard(t *testing.T) {
	for _, check := range []string{CheckSessionProjectDirectoryMismatch, CheckManualSessionNameProjectMismatch} {
		for _, target := range []string{"", "my-name", "upstream-name", "other"} {
			t.Run(check+"/"+target, func(t *testing.T) {
				s := newDiagnosticTestStore(t)
				dir := t.TempDir()
				if err := s.CreateSession("manual-save-upstream-name", "my-name", dir); err != nil {
					t.Fatal(err)
				}
				if err := s.CreateSession("known", "upstream-name", ""); err != nil {
					t.Fatal(err)
				}
				scope := Scope{Store: s, Project: "my-name", DetectProject: func(string) (DetectedProject, bool) {
					if check == CheckSessionProjectDirectoryMismatch {
						return DetectedProject{Project: "upstream-name", Source: "git_remote"}, true
					}
					return DetectedProject{}, false
				}}
				ctx := context.Background()
				report, err := NewRunner().RunOne(ctx, scope, check)
				if err != nil {
					t.Fatal(err)
				}
				// Add after diagnosis: repair must not trust stale alias evidence.
				if target != "" {
					if _, err := s.SetProjectAlias(store.ProjectAliasKindCWD, dir, target); err != nil {
						t.Fatal(err)
					}
				}
				plan, err := BuildRepairPlan(ctx, scope, report, check, RepairModeApply)
				if err != nil {
					t.Fatal(err)
				}
				veto := target != "" && target != "upstream-name"
				if veto && (len(plan.Actions) != 0 || len(plan.Skipped) != 1 || plan.Skipped[0].ReasonCode != "project_alias_contradicts_reclassification") {
					t.Fatalf("alias veto missing: %+v", plan)
				}
				if !veto && len(plan.Actions) != 1 {
					t.Fatalf("existing repair lost: %+v", plan)
				}
				report, err = NewRunner().RunOne(ctx, scope, check)
				if err != nil {
					t.Fatal(err)
				}
				if target != "" {
					var ev map[string]json.RawMessage
					if err := json.Unmarshal(report.Checks[0].Findings[0].Evidence, &ev); err != nil {
						t.Fatal(err)
					}
					var aliases store.ProjectAliasResolution
					if err := json.Unmarshal(ev["project_aliases"], &aliases); err != nil {
						t.Fatal(err)
					}
					if aliases.Project != target || aliases.Conflict || aliases.CwdAlias == nil ||
						aliases.CwdAlias.Kind != store.ProjectAliasKindCWD ||
						aliases.CwdAlias.Key != dir || aliases.CwdAlias.Project != target {
						t.Fatalf("unexpected alias diagnostic evidence: %+v", aliases)
					}
				}
			})
		}
	}
}

func TestProjectAliasReadFailure(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionProjectAliases(Scope{Store: s}, t.TempDir()); err == nil {
		t.Fatal("alias store failure suppressed")
	}
}

func TestProjectAliasesNeverAuthorizeMove(t *testing.T) {
	s := newDiagnosticTestStore(t)
	dir := t.TempDir()
	if err := s.CreateSession("ordinary", "my-name", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProjectAlias(store.ProjectAliasKindCWD, dir, "upstream-name"); err != nil {
		t.Fatal(err)
	}
	scope := Scope{Store: s, Project: "my-name", DetectProject: func(string) (DetectedProject, bool) { return DetectedProject{}, false }}
	for _, check := range []string{CheckSessionProjectDirectoryMismatch, CheckManualSessionNameProjectMismatch} {
		report, err := NewRunner().RunOne(context.Background(), scope, check)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := BuildRepairPlan(context.Background(), scope, report, check, RepairModeApply)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Status != "noop" || len(plan.Actions) != 0 {
			t.Fatalf("alias authorized move: %+v", plan)
		}
	}
}
