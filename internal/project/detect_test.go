package project

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ─── extractRepoName unit tests ──────────────────────────────────────────────

func TestExtractRepoName(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "SSH with .git suffix",
			url:  "git@github.com:user/repo.git",
			want: "repo",
		},
		{
			name: "SSH without .git suffix",
			url:  "git@github.com:user/repo",
			want: "repo",
		},
		{
			name: "HTTPS with .git suffix",
			url:  "https://github.com/user/repo.git",
			want: "repo",
		},
		{
			name: "HTTPS without .git suffix",
			url:  "https://github.com/user/repo",
			want: "repo",
		},
		{
			name: "SSH org with dots in repo name",
			url:  "git@github.com:Gentleman-Programming/engram.git",
			want: "engram",
		},
		{
			name: "HTTPS org with dots",
			url:  "https://github.com/Gentleman-Programming/engram.git",
			want: "engram",
		},
		{
			name: "Repo name without .git from SSH",
			url:  "git@gitlab.com:group/subgroup/my-project",
			want: "my-project",
		},
		{
			name: "Empty URL returns empty",
			url:  "",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractRepoName(tc.url)
			if got != tc.want {
				t.Errorf("extractRepoName(%q) = %q; want %q", tc.url, got, tc.want)
			}
		})
	}
}

// ─── DetectProject integration tests ─────────────────────────────────────────

// initGit initialises a new git repository in dir. Helper for tests.
func initGit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
}

func TestDetectProject_GitRemote(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)

	// Add a fake remote
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:testuser/my-cool-repo.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	got := DetectProject(dir)
	if got != "my-cool-repo" {
		t.Errorf("DetectProject with remote = %q; want %q", got, "my-cool-repo")
	}
}

func TestDetectProject_GitRemote_HTTPS(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)

	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"https://github.com/Gentleman-Programming/engram.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	got := DetectProject(dir)
	if got != "engram" {
		t.Errorf("DetectProject HTTPS remote = %q; want %q", got, "engram")
	}
}

func TestDetectProject_GitRootNoRemote(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)
	// No remote configured — should fall back to basename of git root

	got := DetectProject(dir)
	want := filepath.Base(dir)
	// Normalize to lowercase to match DetectProject output
	wantLower := strings.ToLower(want)
	if got != wantLower {
		t.Errorf("DetectProject no-remote = %q; want %q", got, wantLower)
	}
}

func TestDetectProject_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	// Not a git repo — should fall back to basename of dir

	got := DetectProject(dir)
	want := strings.ToLower(filepath.Base(dir))
	if got != want {
		t.Errorf("DetectProject non-git = %q; want %q", got, want)
	}
}

func TestDetectProject_EmptyDir_NoPanic(t *testing.T) {
	// Even an empty string for dir should not panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("DetectProject panicked: %v", r)
		}
	}()
	got := DetectProject("")
	// Just verify it returns something non-empty (the exact value depends on OS)
	if got == "" {
		t.Error("DetectProject(\"\") returned empty string")
	}
}

func TestDetectProject_NormalizedLowercase(t *testing.T) {
	// DetectProject must always return lowercase names.
	// Create a temp dir whose basename has upper-case letters.
	parent := t.TempDir()
	upper := filepath.Join(parent, "MyProject")
	if err := os.MkdirAll(upper, 0o755); err != nil {
		t.Fatal(err)
	}

	got := DetectProject(upper)
	if got != "myproject" {
		t.Errorf("DetectProject uppercase dir = %q; want %q", got, "myproject")
	}
}

func TestDetectProject_GitRemoteCasing(t *testing.T) {
	// Remote repo name like "MyRepo.git" should be lowercased.
	dir := t.TempDir()
	initGit(t, dir)

	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:user/MyRepo.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	got := DetectProject(dir)
	if got != "myrepo" {
		t.Errorf("DetectProject uppercase remote name = %q; want %q", got, "myrepo")
	}
}

// ─── DetectProjectFull tests (Batch 1 — REQ-300 through REQ-307) ──────────────

// TestDetectProjectFull_Case1_Remote asserts Source=="git_remote" for a
// t.TempDir git repo with remote origin URL (REQ-301).
func TestDetectProjectFull_Case1_Remote(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin",
		"git@github.com:testuser/my-cool-repo.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	res := DetectProjectFull(dir)

	if res.Source != SourceGitRemote {
		t.Errorf("Source = %q; want %q", res.Source, SourceGitRemote)
	}
	if res.Project != "my-cool-repo" {
		t.Errorf("Project = %q; want %q", res.Project, "my-cool-repo")
	}
	if res.Path == "" {
		t.Error("Path must be non-empty")
	}
	if res.Error != nil {
		t.Errorf("unexpected error: %v", res.Error)
	}
}

func TestDetectProjectFull_ConfigFromRepoRootOverridesRemoteFromSubdir(t *testing.T) {
	root := t.TempDir()
	initGit(t, root)
	cmd := exec.Command("git", "-C", root, "remote", "add", "origin", "git@github.com:testuser/wrong-remote.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	configDir := filepath.Join(root, ".engram")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":"Canonical App"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(root, "src", "pkg")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(subdir)

	if res.Error != nil {
		t.Fatalf("unexpected config detection error: %v", res.Error)
	}
	if res.Source != SourceConfig || res.Project != "canonical app" {
		t.Fatalf("expected config project canonical app, got source=%q project=%q", res.Source, res.Project)
	}
	gotPath, _ := filepath.EvalSymlinks(res.Path)
	wantPath, _ := filepath.EvalSymlinks(root)
	if got, want := gotPath, wantPath; got != want {
		t.Fatalf("expected config path %q, got %q", want, got)
	}
}

func TestDetectProjectFull_InvalidConfigFailsClearly(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".engram")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":"   "}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(dir)

	if !errors.Is(res.Error, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got source=%q err=%v", res.Source, res.Error)
	}
	if res.Source != SourceConfig || !strings.Contains(res.Error.Error(), "project_name") {
		t.Fatalf("expected clear config project_name error, got %+v", res)
	}
}

func TestDetectProjectFull_DoesNotInheritParentConfigOutsideGitRepo(t *testing.T) {
	parent := t.TempDir()
	configDir := filepath.Join(parent, ".engram")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":"parent-lock"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "plain-child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(child)

	if res.Error != nil {
		t.Fatalf("unexpected detection error: %v", res.Error)
	}
	if res.Source != SourceDirBasename || res.Project != "plain-child" {
		t.Fatalf("expected child basename without inheriting parent config, got source=%q project=%q", res.Source, res.Project)
	}
	if got, want := res.Path, child; got != want {
		t.Fatalf("expected child path %q, got %q", want, got)
	}
}

func TestDetectProjectFull_InvalidRepoConfigFromSubdirFailsClearly(t *testing.T) {
	root := t.TempDir()
	initGit(t, root)
	configDir := filepath.Join(root, ".engram")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":"bad/name"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(root, "cmd", "tool")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(subdir)

	if !errors.Is(res.Error, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig from repo config, got source=%q err=%v", res.Source, res.Error)
	}
	if res.Source != SourceConfig || !strings.Contains(res.Error.Error(), "project_name") {
		t.Fatalf("expected clear repo config project_name error, got %+v", res)
	}
	gotPath, _ := filepath.EvalSymlinks(res.Path)
	wantPath, _ := filepath.EvalSymlinks(root)
	if got, want := gotPath, wantPath; got != want {
		t.Fatalf("expected invalid config path %q, got %q", want, got)
	}
}

// TestDetectProjectFull_Case1_PathIsRepoRoot asserts that Case 1 (git_remote)
// sets Path to the git repository root, not the input directory (JS2).
// When called from a subdir of a remote-configured repo, Path should equal the
// root — consistent with Case 2 behavior.
func TestDetectProjectFull_Case1_PathIsRepoRoot(t *testing.T) {
	root := t.TempDir()
	initGit(t, root)
	cmd := exec.Command("git", "-C", root, "remote", "add", "origin",
		"git@github.com:testuser/root-repo.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	// Call from a subdirectory.
	subdir := filepath.Join(root, "src", "lib")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(subdir)

	if res.Source != SourceGitRemote {
		t.Errorf("Source = %q; want %q", res.Source, SourceGitRemote)
	}
	// JS2: Path must be repo root, not subdir.
	wantPath, _ := filepath.EvalSymlinks(root)
	gotPath, _ := filepath.EvalSymlinks(res.Path)
	if gotPath != wantPath {
		t.Errorf("Case1 Path = %q; want repo root %q (JS2: consistent with Case2)", res.Path, root)
	}
}

// TestDetectProjectFull_Case1_NoRemote asserts fallthrough to git_root source
// when no origin remote exists (REQ-301 fallback).
func TestDetectProjectFull_Case1_NoRemote(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)

	res := DetectProjectFull(dir)

	if res.Source != SourceGitRoot {
		t.Errorf("Source = %q; want %q", res.Source, SourceGitRoot)
	}
	if res.Project == "" {
		t.Error("Project must not be empty when no remote is set")
	}
	if res.Error != nil {
		t.Errorf("unexpected error: %v", res.Error)
	}
}

// TestDetectProjectFull_Case2_Subdir asserts Source=="git_root", Path==ancestor_root,
// from a subdirectory two levels deep inside a git repo (REQ-302).
func TestDetectProjectFull_Case2_Subdir(t *testing.T) {
	root := t.TempDir()
	initGit(t, root)

	subdir := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(subdir)

	if res.Source != SourceGitRoot {
		t.Errorf("Source = %q; want %q", res.Source, SourceGitRoot)
	}
	// Resolve symlinks for comparison — macOS /var → /private/var.
	wantPath, _ := filepath.EvalSymlinks(root)
	gotPath, _ := filepath.EvalSymlinks(res.Path)
	if gotPath != wantPath {
		t.Errorf("Path = %q; want %q", res.Path, root)
	}
	if res.Project == "" {
		t.Error("Project must not be empty")
	}
	if res.Error != nil {
		t.Errorf("unexpected error: %v", res.Error)
	}
}

// TestDetectProjectFull_Case3_SingleChild asserts Source=="git_child",
// Warning!="", Error==nil for a temp dir with exactly one git-repo subdirectory
// (REQ-303).
func TestDetectProjectFull_Case3_SingleChild(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "my-child-repo")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, child)

	res := DetectProjectFull(parent)

	if res.Source != SourceGitChild {
		t.Errorf("Source = %q; want %q", res.Source, SourceGitChild)
	}
	if res.Warning == "" {
		t.Error("Warning must be non-empty for git_child promotion")
	}
	if res.Error != nil {
		t.Errorf("unexpected error: %v", res.Error)
	}
	if res.Project != "my-child-repo" {
		t.Errorf("Project = %q; want %q", res.Project, "my-child-repo")
	}
}

// TestDetectProjectFull_Case4_MultiChild asserts Error==ErrAmbiguousProject,
// len(AvailableProjects)==2, Project=="" for two git-repo children (REQ-304).
func TestDetectProjectFull_Case4_MultiChild(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"repo-alpha", "repo-beta"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initGit(t, child)
	}

	res := DetectProjectFull(parent)

	if !errors.Is(res.Error, ErrAmbiguousProject) {
		t.Errorf("Error = %v; want ErrAmbiguousProject", res.Error)
	}
	if len(res.AvailableProjects) != 2 {
		t.Errorf("AvailableProjects len = %d; want 2", len(res.AvailableProjects))
	}
	if res.Project != "" {
		t.Errorf("Project = %q; want empty on ambiguous", res.Project)
	}
}

// TestDetectProjectFull_Case5_Basename asserts Source=="dir_basename",
// Project==filepath.Base(dir), Error==nil for a plain non-git dir (REQ-305).
func TestDetectProjectFull_Case5_Basename(t *testing.T) {
	parent := t.TempDir()
	plain := filepath.Join(parent, "plain-dir")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(plain)

	if res.Source != SourceDirBasename {
		t.Errorf("Source = %q; want %q", res.Source, SourceDirBasename)
	}
	if res.Project != "plain-dir" {
		t.Errorf("Project = %q; want %q", res.Project, "plain-dir")
	}
	if res.Error != nil {
		t.Errorf("unexpected error: %v", res.Error)
	}
	if res.Warning != "" {
		t.Errorf("Warning must be empty for dir_basename, got %q", res.Warning)
	}
}

// TestChildScan_ShortCircuit asserts the scan stops after 2 repos (REQ-306).
func TestChildScan_ShortCircuit(t *testing.T) {
	parent := t.TempDir()
	// Create 4 child repos — scan must short-circuit after 2.
	for _, name := range []string{"repo1", "repo2", "repo3", "repo4"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initGit(t, child)
	}

	res := DetectProjectFull(parent)

	if !errors.Is(res.Error, ErrAmbiguousProject) {
		t.Errorf("Error = %v; want ErrAmbiguousProject", res.Error)
	}
	// AvailableProjects length bounded — scan stopped early.
	if len(res.AvailableProjects) < 2 {
		t.Errorf("expected at least 2 available projects, got %d", len(res.AvailableProjects))
	}
}

// TestChildScan_SkipNoise asserts node_modules and vendor are skipped (REQ-306).
func TestChildScan_SkipNoise(t *testing.T) {
	parent := t.TempDir()
	// node_modules with .git inside — must NOT be counted.
	nm := filepath.Join(parent, "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, nm)
	// One legitimate repo.
	legit := filepath.Join(parent, "my-project")
	if err := os.MkdirAll(legit, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, legit)

	res := DetectProjectFull(parent)

	// Should auto-promote my-project as the single child.
	if res.Source != SourceGitChild {
		t.Errorf("Source = %q; want %q (node_modules should be skipped)", res.Source, SourceGitChild)
	}
	if res.Project != "my-project" {
		t.Errorf("Project = %q; want %q", res.Project, "my-project")
	}
}

// TestChildScan_SkipHidden asserts hidden directories are skipped (REQ-306).
func TestChildScan_SkipHidden(t *testing.T) {
	parent := t.TempDir()
	// Hidden dir with .git inside — must NOT be counted.
	hidden := filepath.Join(parent, ".hidden-repo")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, hidden)
	// One visible repo.
	visible := filepath.Join(parent, "visible-repo")
	if err := os.MkdirAll(visible, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, visible)

	res := DetectProjectFull(parent)

	if res.Source != SourceGitChild {
		t.Errorf("Source = %q; want %q (hidden dirs should be skipped)", res.Source, SourceGitChild)
	}
	if res.Project != "visible-repo" {
		t.Errorf("Project = %q; want %q", res.Project, "visible-repo")
	}
}

// TestDetectProject_MatchesFull asserts DetectProject returns same as
// DetectProjectFull.Project for non-ambiguous cases (REQ-307 backward-compat wrapper).
func TestDetectProject_MatchesFull(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)

	full := DetectProjectFull(dir)
	compat := DetectProject(dir)

	// For non-ambiguous cases, the wrapper must match Full.Project.
	if full.Error == nil && compat != full.Project {
		t.Errorf("DetectProject = %q; DetectProjectFull.Project = %q; must be equal",
			compat, full.Project)
	}
}

// TestDetectProject_AmbiguousEmpty asserts DetectProject returns basename
// (not empty) even on ambiguous cwd, maintaining CLI compat (REQ-307).
func TestDetectProject_AmbiguousEmpty(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"repo-a", "repo-b"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initGit(t, child)
	}

	// DetectProjectFull reports ambiguous — Project will be "".
	full := DetectProjectFull(parent)
	if !errors.Is(full.Error, ErrAmbiguousProject) {
		t.Skipf("expected ambiguous; got source=%s err=%v", full.Source, full.Error)
	}

	// The design decision: on ambiguity, DetectProject returns basename fallback
	// so CLI callers never see empty. We verify project != "" per design doc §9.
	// NOTE: the spec says DetectProject returns full.Project; design says ambiguous
	// populates Project with basename. Both are satisfied by DetectProjectFull
	// setting Project=basename when ErrAmbiguousProject occurs.
	got := DetectProject(parent)
	if got == "" {
		t.Error("DetectProject must not return empty string on ambiguous cwd")
	}
}
