package diagnostic

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	projectpkg "github.com/Gentleman-Programming/engram/v2/internal/project"
)

func TestBuildRepairPlanManualSessionNameKeepsPersistedProjectWhenBasenameCorroboratesIt(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("manual-save-engram", "sias-app", "/work/sias-app"); err != nil {
		t.Fatalf("CreateSession manual: %v", err)
	}
	if err := s.CreateSession("known-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession known: %v", err)
	}

	scope := Scope{
		Store:   s,
		Project: "sias-app",
		DetectProject: func(directory string) (DetectedProject, bool) {
			if directory == "/work/sias-app" {
				return DetectedProject{Project: "sias-app", Source: projectpkg.SourceDirBasename, Path: directory}, true
			}
			return DetectedProject{}, false
		},
	}
	plan, err := BuildRepairPlan(context.Background(), scope, Report{}, CheckManualSessionNameProjectMismatch, RepairModePlan)
	if err != nil {
		t.Fatalf("BuildRepairPlan: %v", err)
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("actions=%+v, want no move when basename corroborates persisted ownership", plan.Actions)
	}
}

func TestManualSessionNameCheckSuppressesBasenameCorroboratedMismatch(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("manual-save-engram", "sias-app", "/work/sias-app"); err != nil {
		t.Fatalf("CreateSession manual: %v", err)
	}
	if err := s.CreateSession("known-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession known: %v", err)
	}

	scope := Scope{
		Store:   s,
		Project: "sias-app",
		DetectProject: func(directory string) (DetectedProject, bool) {
			if directory == "/work/sias-app" {
				return DetectedProject{Project: "sias-app", Source: projectpkg.SourceDirBasename, Path: directory}, true
			}
			return DetectedProject{}, false
		},
	}
	for _, check := range []string{CheckSessionProjectDirectoryMismatch, CheckManualSessionNameProjectMismatch} {
		report, err := NewRunner().RunOne(context.Background(), scope, check)
		if err != nil {
			t.Fatalf("RunOne(%s): %v", check, err)
		}
		if len(report.Checks[0].Findings) != 0 {
			t.Fatalf("%s findings=%+v, want no competing ownership finding", check, report.Checks[0].Findings)
		}
	}

	plan, err := BuildRepairPlan(context.Background(), scope, Report{}, CheckManualSessionNameProjectMismatch, RepairModePlan)
	if err != nil {
		t.Fatalf("BuildRepairPlan: %v", err)
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("actions=%+v, want no move", plan.Actions)
	}
}

func TestProductionDirectoryBasenameCorroborationSuppressesCompetingOwnershipPaths(t *testing.T) {
	s := newDiagnosticTestStore(t)
	temp := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", temp)
	directory := filepath.Join(temp, "tasks", "sias-app")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := s.CreateSession("manual-save-engram", "sias-app", directory); err != nil {
		t.Fatalf("CreateSession manual: %v", err)
	}
	if err := s.CreateSession("known-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession known: %v", err)
	}

	scope := Scope{Store: s, Project: "sias-app"}
	for _, check := range []string{CheckSessionProjectDirectoryMismatch, CheckManualSessionNameProjectMismatch} {
		report, err := NewRunner().RunOne(context.Background(), scope, check)
		if err != nil {
			t.Fatalf("RunOne(%s): %v", check, err)
		}
		if len(report.Checks[0].Findings) != 0 {
			t.Fatalf("%s findings=%+v, want no competing ownership finding", check, report.Checks[0].Findings)
		}
	}

	plan, err := BuildRepairPlan(context.Background(), scope, Report{}, CheckManualSessionNameProjectMismatch, RepairModePlan)
	if err != nil {
		t.Fatalf("BuildRepairPlan: %v", err)
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("actions=%+v, want no move", plan.Actions)
	}
}

func TestTrustedGitDirectoryEvidenceWinsAcrossCheckAndRepair(t *testing.T) {
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("manual-save-engram", "sias-app", "/work/third-project"); err != nil {
		t.Fatalf("CreateSession manual: %v", err)
	}
	if err := s.CreateSession("known-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession known: %v", err)
	}

	scope := Scope{
		Store:   s,
		Project: "sias-app",
		DetectProject: func(directory string) (DetectedProject, bool) {
			if directory == "/work/third-project" {
				return DetectedProject{Project: "third-project", Source: "git_root", Path: directory}, true
			}
			return DetectedProject{}, false
		},
	}
	directoryReport, err := NewRunner().RunOne(context.Background(), scope, CheckSessionProjectDirectoryMismatch)
	if err != nil {
		t.Fatalf("RunOne directory mismatch: %v", err)
	}
	if len(directoryReport.Checks[0].Findings) != 1 {
		t.Fatalf("directory findings=%+v, want trusted Git finding", directoryReport.Checks[0].Findings)
	}
	manualReport, err := NewRunner().RunOne(context.Background(), scope, CheckManualSessionNameProjectMismatch)
	if err != nil {
		t.Fatalf("RunOne manual mismatch: %v", err)
	}
	if len(manualReport.Checks[0].Findings) != 0 {
		t.Fatalf("manual findings=%+v, want trusted Git evidence to suppress the competing manual finding", manualReport.Checks[0].Findings)
	}

	directoryPlan, err := BuildRepairPlan(context.Background(), scope, directoryReport, CheckSessionProjectDirectoryMismatch, RepairModePlan)
	if err != nil {
		t.Fatalf("BuildRepairPlan directory: %v", err)
	}
	if len(directoryPlan.Actions) != 1 || directoryPlan.Actions[0].ToProject != "third-project" || directoryPlan.Actions[0].EvidenceSource != "git_root" {
		t.Fatalf("directory actions=%+v, want trusted Git target", directoryPlan.Actions)
	}
	manualPlan, err := BuildRepairPlan(context.Background(), scope, manualReport, CheckManualSessionNameProjectMismatch, RepairModePlan)
	if err != nil {
		t.Fatalf("BuildRepairPlan manual: %v", err)
	}
	if len(manualPlan.Actions) != 0 {
		t.Fatalf("manual actions=%+v, want no competing manual repair", manualPlan.Actions)
	}
}

func TestSessionProjectAuthorityDecision(t *testing.T) {
	tests := []struct {
		name                 string
		persistedProject     string
		manualTarget         string
		knownManualTarget    bool
		directory            DetectedProject
		wantTarget               string
		wantEvidenceSource       string
		wantBasenameVeto         bool
		wantNoCompetingBehavior  bool
	}{
		{
			name:              "uncorroborated known manual target remains repairable",
			persistedProject:  "sias-app",
			manualTarget:      "engram",
			knownManualTarget: true,
			wantTarget:        "engram",
		},
		{
			name:              "trusted git directory takes precedence",
			persistedProject:  "sias-app",
			manualTarget:      "engram",
			knownManualTarget: true,
			directory:         DetectedProject{Project: "third-project", Source: "git_remote", Path: "/work/third-project"},
			wantTarget:        "third-project",
			wantEvidenceSource: "git_remote",
		},
		{
			name:              "basename corroborates persisted ownership",
			persistedProject:  "sias-app",
			manualTarget:      "engram",
			knownManualTarget: true,
			directory:         DetectedProject{Project: "sias-app", Source: projectpkg.SourceDirBasename, Path: "/work/sias-app"},
			wantBasenameVeto:  true,
		},
		{
			name:                    "conflicting basename vetoes competing behavior",
			persistedProject:        "sias-app",
			manualTarget:            "engram",
			knownManualTarget:       true,
			directory:               DetectedProject{Project: "third-project", Source: projectpkg.SourceDirBasename, Path: "/work/third-project"},
			wantNoCompetingBehavior: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decision := decideSessionProjectAuthority(tc.persistedProject, tc.manualTarget, tc.knownManualTarget, tc.directory)
			if decision.repairTarget != tc.wantTarget || decision.repairEvidenceSource != tc.wantEvidenceSource || decision.directoryBasenameCorroboratesPersisted != tc.wantBasenameVeto {
				t.Fatalf("decision=%+v", decision)
			}
			if tc.wantNoCompetingBehavior && (decision.shouldReportDirectoryMismatch() || decision.shouldReportManualNameMismatch() || decision.shouldRepairFromTrustedDirectory() || decision.shouldRepairFromManualName()) {
				t.Fatalf("decision=%+v, want no competing finding or repair", decision)
			}
		})
	}
}
