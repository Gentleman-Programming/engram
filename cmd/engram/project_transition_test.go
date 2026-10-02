package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCLISaveFirstBindingTransition(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	dir := filepath.Join(t.TempDir(), "local-app")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	withCwd(t, dir)
	t.Setenv("ENGRAM_PROJECT", "")
	cfg := testConfig(t)
	withArgs(t, "engram", "save", "historical marker", "keep memory")
	if _, stderr := captureOutput(t, func() { cmdSave(cfg) }); stderr != "" {
		t.Fatal(stderr)
	}
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://example.test/team/remote-app.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	stubExitWithPanic(t)
	withArgs(t, "engram", "save", "rejected marker", "must not save")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSave(cfg) })
	if recovered == nil || !strings.Contains(stderr, "history under local-app") {
		t.Fatalf("expected transition refusal: %v %s", recovered, stderr)
	}
	withArgs(t, "engram", "save", "explicit marker", "keep explicit access", "--project", "local-app")
	if _, stderr := captureOutput(t, func() { cmdSave(cfg) }); stderr != "" {
		t.Fatal(stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected binding: %v", err)
	}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if count, err := s.CountObservationsForProject("local-app"); err != nil || count != 2 {
		t.Fatalf("local memory count=%d err=%v", count, err)
	}
	if count, err := s.CountObservationsForProject("remote-app"); err != nil || count != 0 {
		t.Fatalf("remote memory count=%d err=%v", count, err)
	}
}
