package diagnostic

import (
	"context"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func TestBuildRepairPlanForeignSyncTargetUsesStoreClassification(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if _, err := s.DB().Exec(`
		INSERT INTO sync_enrolled_projects (project) VALUES ('valid'); INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES ('satellite:empty', 'idle', datetime('now')), ('satellite:terminal', 'idle', datetime('now'));
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project, disposition) VALUES ('satellite:terminal', 'observation', 'pending', 'upsert', '{}', 'local', 'valid', 'pending');
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, acked_at, disposition, disposition_reason, disposition_evidence, disposition_at) VALUES ('satellite:terminal', 'observation', 'terminal', 'upsert', '{}', 'local', datetime('now'), 'quarantined', 'kept', 'evidence', datetime('now'));`); err != nil {
		t.Fatalf("seed foreign targets: %v", err)
	}
	report, err := NewRunner().RunOne(context.Background(), Scope{Store: s}, CheckSyncTargetClosedSpace)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	plan, err := BuildRepairPlan(context.Background(), Scope{Store: s}, report, CheckSyncTargetClosedSpace, RepairModeDryRun)
	if err != nil {
		t.Fatalf("BuildRepairPlan: %v", err)
	}
	if plan.Status != "dry_run" || len(plan.Actions) != 0 || len(plan.TargetActions) != 2 {
		t.Fatalf("plan=%+v", plan)
	}
	if empty, terminal := plan.TargetActions[0], plan.TargetActions[1]; empty.TargetKey != "satellite:empty" || !empty.StateRemoved || empty.RetainedMutations != 0 || terminal.TargetKey != "satellite:terminal" || terminal.RetargetedMutations != 1 || terminal.StateRemoved || terminal.RetainedMutations != 1 {
		t.Fatalf("actions=%+v", plan.TargetActions)
	}
	var states, mutations int
	if err := s.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM sync_state WHERE target_key LIKE 'satellite:%'), (SELECT COUNT(*) FROM sync_mutations WHERE target_key = 'satellite:terminal')`).Scan(&states, &mutations); err != nil || states != 2 || mutations != 2 {
		t.Fatalf("plan mutated states=%d mutations=%d err=%v", states, mutations, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := BuildRepairPlan(context.Background(), Scope{Store: s}, report, CheckSyncTargetClosedSpace, RepairModeDryRun); err == nil {
		t.Fatal("expected cleanup classification error")
	}
}

func TestBuildRepairPlanDirectoryMismatchUsesTrustedEvidence(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("s-engram", "sias-app", "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.CreateSession("s-ignored", "sias-app", "/work/ignored"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	scope := Scope{Store: s, Project: "sias-app", DetectProject: func(dir string) (DetectedProject, bool) {
		switch dir {
		case "/work/engram":
			return DetectedProject{Project: "engram", Source: "git_remote", Path: dir}, true
		case "/work/ignored":
			return DetectedProject{Project: "ignored", Source: "basename", Path: dir}, true
		default:
			return DetectedProject{}, false
		}
	}}
	report, err := NewRunner().RunOne(context.Background(), scope, CheckSessionProjectDirectoryMismatch)
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}

	plan, err := BuildRepairPlan(context.Background(), scope, report, CheckSessionProjectDirectoryMismatch, RepairModePlan)
	if err != nil {
		t.Fatalf("BuildRepairPlan: %v", err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("actions=%+v skipped=%+v", plan.Actions, plan.Skipped)
	}
	got := plan.Actions[0]
	if got.SessionID != "s-engram" || got.FromProject != "sias-app" || got.ToProject != "engram" || got.EvidenceSource != "git_remote" {
		t.Fatalf("action=%+v", got)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].ReasonCode != "untrusted_directory_evidence" {
		t.Fatalf("skipped=%+v", plan.Skipped)
	}
}

func TestBuildRepairPlanManualSessionNameRules(t *testing.T) {
	tests := []struct {
		name       string
		sessions   []store.DiagnosticSessionEvidence
		detect     func(string) (DetectedProject, bool)
		wantAction bool
		wantSkip   string
	}{
		{
			name: "exact manual save known project",
			sessions: []store.DiagnosticSessionEvidence{
				{ID: "manual-save-engram", Name: "manual-save-engram", Project: "sias-app", Directory: "/work/engram"},
				{ID: "known", Name: "known", Project: "engram", Directory: "/work/engram"},
			},
			wantAction: true,
		},
		{
			name: "unknown manual target skipped",
			sessions: []store.DiagnosticSessionEvidence{
				{ID: "manual-save-engram", Name: "manual-save-engram", Project: "sias-app", Directory: "/work/engram"},
			},
			wantSkip: "manual_name_unknown_project",
		},
		{
			name: "trusted directory contradiction skipped",
			sessions: []store.DiagnosticSessionEvidence{
				{ID: "manual-save-engram", Name: "manual-save-engram", Project: "sias-app", Directory: "/work/engram"},
				{ID: "known", Name: "known", Project: "engram", Directory: "/work/engram"},
			},
			detect: func(string) (DetectedProject, bool) {
				return DetectedProject{Project: "other", Source: "git_root", Path: "/work/other"}, true
			},
			wantSkip: "trusted_directory_contradicts_manual_name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newDiagnosticTestStore(t)
			for _, session := range tc.sessions {
				if err := s.CreateSession(session.ID, session.Project, session.Directory); err != nil {
					t.Fatalf("CreateSession(%s): %v", session.ID, err)
				}
			}
			scope := Scope{Store: s, Project: "sias-app", DetectProject: tc.detect}
			plan, err := BuildRepairPlan(context.Background(), scope, Report{}, CheckManualSessionNameProjectMismatch, RepairModePlan)
			if err != nil {
				t.Fatalf("BuildRepairPlan: %v", err)
			}
			if tc.wantAction && (len(plan.Actions) != 1 || plan.Actions[0].ToProject != "engram") {
				t.Fatalf("actions=%+v skipped=%+v", plan.Actions, plan.Skipped)
			}
			if tc.wantSkip != "" && (len(plan.Skipped) != 1 || plan.Skipped[0].ReasonCode != tc.wantSkip) {
				t.Fatalf("skipped=%+v actions=%+v", plan.Skipped, plan.Actions)
			}
		})
	}
}
