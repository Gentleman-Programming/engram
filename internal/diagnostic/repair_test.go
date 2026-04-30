package diagnostic

import (
	"context"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

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
