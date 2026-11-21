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

func TestCLISaveWarnsOnceBeforeImplicitProjectCollision(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "empty-git-config"))
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CEILING_DIRECTORIES", root)
	t.Setenv("ENGRAM_PROJECT", "")
	cfg := testConfig(t)
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	checkout := func(name, org string) string {
		t.Helper()
		path := filepath.Join(root, name, "workspace")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		git(path, "init")
		git(path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test",
			"commit", "--allow-empty", "-m", "fixture")
		git(path, "remote", "add", "origin", "https://example.test/"+org+"/customer-solution.git")
		return path
	}
	save := func(dir, title string, flags ...string) string {
		t.Helper()
		withCwd(t, dir)
		withArgs(t, append([]string{"engram", "save", title, "fixture content"}, flags...)...)
		stdout, stderr := captureOutput(t, func() { cmdSave(cfg) })
		if !strings.Contains(stdout, "Memory saved:") {
			t.Fatalf("save %q failed: %s %s", title, stdout, stderr)
		}
		return stderr
	}

	first := checkout("first", "team-a")
	second := checkout("second", "team-b")
	linked := filepath.Join(root, "linked")
	git(first, "worktree", "add", "--detach", linked)

	if stderr := save(first, "first"); stderr != "" {
		t.Fatalf("first capture warned: %s", stderr)
	}
	if stderr := save(linked, "linked"); stderr != "" {
		t.Fatalf("linked worktree warned: %s", stderr)
	}
	if stderr := save(second, "explicit", "--project", "customer-solution"); stderr != "" {
		t.Fatalf("explicit capture warned: %s", stderr)
	}
	if stderr := save(second, "colliding"); !strings.Contains(stderr, "another Git checkout") || !strings.Contains(stderr, "engram init <distinct-name>") {
		t.Fatalf("missing collision advisory: %s", stderr)
	}
	if stderr := save(second, "repeat"); stderr != "" {
		t.Fatalf("repeated capture warned: %s", stderr)
	}

	withCwd(t, second)
	withArgs(t, "engram", "init", "client-b")
	captureOutput(t, cmdInit)
	if stderr := save(second, "configured"); stderr != "" {
		t.Fatalf("configured project warned: %s", stderr)
	}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for project, want := range map[string]int{"customer-solution": 5, "client-b": 1} {
		if count, err := s.CountObservationsForProject(project); err != nil || count != want {
			t.Fatalf("project %q count=%d want=%d err=%v", project, count, want, err)
		}
	}

	t.Run("unavailable advisory metadata still permits save", func(t *testing.T) {
		cfg := testConfig(t)
		if err := os.WriteFile(filepath.Join(cfg.DataDir, "git-project-advisories-v1"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		withCwd(t, first)
		withArgs(t, "engram", "save", "advisory unavailable", "fixture content")
		stdout, stderr := captureOutput(t, func() { cmdSave(cfg) })
		if !strings.Contains(stdout, "Memory saved:") || stderr != "" {
			t.Fatalf("expected an ordinary save, got stdout=%q stderr=%q", stdout, stderr)
		}
	})
}
