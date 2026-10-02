package diagnostic

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDiagnosticUnboundGitIsNotRepairAuthority(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	dir := t.TempDir()
	s := newDiagnosticTestStore(t)
	if err := s.CreateSession("history", "local-app", dir); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://example.test/team/remote-app.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	scope := Scope{Store: s, Project: "local-app"}
	report, err := NewRunner().RunOne(context.Background(), scope, CheckSessionProjectDirectoryMismatch)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Checks[0].Findings) != 0 {
		t.Fatalf("unbound Git became authority: %+v", report)
	}
	plan, err := BuildRepairPlan(context.Background(), scope, report, CheckSessionProjectDirectoryMismatch, RepairModePlan)
	if err != nil || len(plan.Actions) != 0 {
		t.Fatalf("repair=%+v err=%v", plan, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
		t.Fatalf("diagnostic initialized binding: %v", err)
	}
}
