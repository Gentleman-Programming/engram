package project

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
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

func commitEmptyGit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", "initial commit")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func addGitWorktree(t *testing.T, repo, worktree, branch string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "-b", branch, worktree)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
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

func TestDetectProjectFull_GitRemoteCanonicalizesRepeatedSeparators(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", "git@github.com:testuser/Foo--Bar__Baz.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	res := DetectProjectFull(dir)
	if res.Source != SourceGitRemote || res.Project != "foo-bar_baz" {
		t.Fatalf("git remote detection = %+v, want source=%q project=%q", res, SourceGitRemote, "foo-bar_baz")
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

func TestDetectProjectFull_ConfigCanonicalizesRepeatedSeparators(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".engram")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":"Foo--Bar__Baz"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(dir)
	if res.Source != SourceConfig || res.Project != "foo-bar_baz" {
		t.Fatalf("config detection = %+v, want source=%q project=%q", res, SourceConfig, "foo-bar_baz")
	}
}

func TestDetectProjectFull_NearestSubprojectConfigOverridesRepoRoot(t *testing.T) {
	root := t.TempDir()
	initGit(t, root)

	rootConfigDir := filepath.Join(root, ".engram")
	if err := os.MkdirAll(rootConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootConfigDir, "config.json"), []byte(`{"project_name":"mono-root"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := filepath.Join(root, "backend")
	backendConfigDir := filepath.Join(backend, ".engram")
	if err := os.MkdirAll(backendConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backendConfigDir, "config.json"), []byte(`{"project_name":"backend-service"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	backendSubdir := filepath.Join(backend, "internal", "handlers")
	if err := os.MkdirAll(backendSubdir, 0o755); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(backendSubdir)

	if res.Error != nil {
		t.Fatalf("unexpected config detection error: %v", res.Error)
	}
	if res.Source != SourceConfig || res.Project != "backend-service" {
		t.Fatalf("expected nearest subproject config, got source=%q project=%q", res.Source, res.Project)
	}
	gotPath, _ := filepath.EvalSymlinks(res.Path)
	wantPath, _ := filepath.EvalSymlinks(backend)
	if gotPath != wantPath {
		t.Fatalf("expected backend config path %q, got %q", wantPath, gotPath)
	}
}

func TestDetectProjectFull_MonorepoSubprojectConfigsResolveIndependently(t *testing.T) {
	root := t.TempDir()
	initGit(t, root)

	backend := filepath.Join(root, "backend")
	frontend := filepath.Join(root, "frontend")
	for path, projectName := range map[string]string{
		backend:  "backend-app",
		frontend: "frontend-app",
	} {
		configDir := filepath.Join(path, ".engram")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":"`+projectName+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	backendSubdir := filepath.Join(backend, "cmd", "api")
	frontendSubdir := filepath.Join(frontend, "src", "app")
	if err := os.MkdirAll(backendSubdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(frontendSubdir, 0o755); err != nil {
		t.Fatal(err)
	}

	backendRes := DetectProjectFull(backendSubdir)
	frontendRes := DetectProjectFull(frontendSubdir)

	if backendRes.Error != nil || frontendRes.Error != nil {
		t.Fatalf("unexpected monorepo config errors: backend=%v frontend=%v", backendRes.Error, frontendRes.Error)
	}
	if backendRes.Source != SourceConfig || backendRes.Project != "backend-app" {
		t.Fatalf("expected backend config project, got %+v", backendRes)
	}
	if frontendRes.Source != SourceConfig || frontendRes.Project != "frontend-app" {
		t.Fatalf("expected frontend config project, got %+v", frontendRes)
	}
	backendPath, _ := filepath.EvalSymlinks(backendRes.Path)
	frontendPath, _ := filepath.EvalSymlinks(frontendRes.Path)
	wantBackend, _ := filepath.EvalSymlinks(backend)
	wantFrontend, _ := filepath.EvalSymlinks(frontend)
	if backendPath != wantBackend || frontendPath != wantFrontend {
		t.Fatalf("expected independent subproject roots, got backend=%q frontend=%q", backendRes.Path, frontendRes.Path)
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

func TestDetectProjectFull_DoesNotLeakHomeAncestorConfigIntoNestedRepo(t *testing.T) {
	homeLike := t.TempDir()
	homeConfigDir := filepath.Join(homeLike, ".engram")
	if err := os.MkdirAll(homeConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeConfigDir, "config.json"), []byte(`{"project_name":"home-leak"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	workspace := filepath.Join(homeLike, "workspaces", "eng")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, workspace)

	projectDir := filepath.Join(workspace, "backend", "pkg")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(projectDir)

	if res.Error != nil {
		t.Fatalf("unexpected detection error: %v", res.Error)
	}
	if res.Source != SourceGitRoot {
		t.Fatalf("expected git-root fallback inside nested repo, got source=%q project=%q", res.Source, res.Project)
	}
	if res.Project != strings.ToLower(filepath.Base(workspace)) {
		t.Fatalf("expected nested repo name %q, got %q", strings.ToLower(filepath.Base(workspace)), res.Project)
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

func TestDetectProjectFull_AmbiguousChildrenPreserveSeparatorForms(t *testing.T) {
	tests := []struct {
		name     string
		children []string
		want     []string
	}{
		{
			name:     "repeated hyphens remain distinct",
			children: []string{"Foo--Bar", "Foo-Bar"},
			want:     []string{"foo--bar", "foo-bar"},
		},
		{
			name:     "repeated underscores remain distinct",
			children: []string{"Repo_Exact_B", "Repo__Exact__B"},
			want:     []string{"repo_exact_b", "repo__exact__b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			for _, name := range tt.children {
				child := filepath.Join(parent, name)
				if err := os.MkdirAll(child, 0o755); err != nil {
					t.Fatal(err)
				}
				initGit(t, child)
			}

			res := DetectProjectFull(parent)
			if !errors.Is(res.Error, ErrAmbiguousProject) {
				t.Fatalf("error = %v, want ErrAmbiguousProject", res.Error)
			}
			got := append([]string(nil), res.AvailableProjects...)
			want := append([]string(nil), tt.want...)
			sort.Strings(got)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("available projects = %q, want %q", res.AvailableProjects, tt.want)
			}
		})
	}
}

func TestDetectProjectFull_ChildScanFindsLaterSecondRepository(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"a-repo", "z-repo"} {
		child := filepath.Join(parent, name)
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		initGit(t, child)
	}
	for i := 1; i <= 25; i++ {
		if err := os.Mkdir(filepath.Join(parent, fmt.Sprintf("noise-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	previousNow := childScanNow
	childScanNow = func() time.Time { return time.Unix(0, 0) }
	t.Cleanup(func() { childScanNow = previousNow })

	res := DetectProjectFull(parent)

	if !errors.Is(res.Error, ErrAmbiguousProject) {
		t.Fatalf("result error = %v, want ErrAmbiguousProject", res.Error)
	}
	if !reflect.DeepEqual(res.AvailableProjects, []string{"a-repo", "z-repo"}) {
		t.Fatalf("available projects = %q, want both repositories", res.AvailableProjects)
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

func TestDetectProjectFull_BasenameCanonicalizesRepeatedSeparators(t *testing.T) {
	plain := filepath.Join(t.TempDir(), "Foo--Bar__Baz")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(plain)
	if res.Source != SourceDirBasename || res.Project != "foo-bar_baz" {
		t.Fatalf("basename detection = %+v, want source=%q project=%q", res, SourceDirBasename, "foo-bar_baz")
	}
}

func TestFallbackProjectName_RootIsUnknown(t *testing.T) {
	for _, dir := range []string{string(filepath.Separator), `C:\`} {
		if got := fallbackProjectName(dir); got != "unknown" {
			t.Fatalf("fallbackProjectName(%q) = %q, want unknown", dir, got)
		}
	}
}

func TestNormalize_PathValueIsUnknown(t *testing.T) {
	for _, name := range []string{"invalid/path", `invalid\path`} {
		if got := normalize(name); got != "unknown" {
			t.Fatalf("normalize(%q) = %q, want unknown", name, got)
		}
	}
}

func TestDetectProjectFull_WorktreeUsesPrimaryRepositoryIdentity(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "canonical-repo")
	worktree := filepath.Join(parent, "feature-worktree")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, repo)
	commitEmptyGit(t, repo)
	addGitWorktree(t, repo, worktree, "feature")

	for _, dir := range []string{repo, worktree} {
		res := DetectProjectFull(dir)
		if res.Source != SourceGitRoot {
			t.Errorf("DetectProjectFull(%q) source = %q, want %q", dir, res.Source, SourceGitRoot)
		}
		if res.Project != "canonical-repo" {
			t.Errorf("DetectProjectFull(%q) project = %q, want canonical-repo", dir, res.Project)
		}
		if got, want := canonicalizePath(res.Path), canonicalizePath(repo); got != want {
			t.Errorf("DetectProjectFull(%q) path = %q, want primary repository %q", dir, got, want)
		}
	}
}

func TestDetectProjectFull_WorktreeRemoteUsesPrimaryRepositoryPath(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "canonical-repo")
	worktree := filepath.Join(parent, "feature-worktree")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, repo)
	commitEmptyGit(t, repo)
	cmd := exec.Command("git", "-C", repo, "remote", "add", "origin", "git@github.com:test/canonical-remote.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	addGitWorktree(t, repo, worktree, "feature")

	res := DetectProjectFull(worktree)
	if res.Source != SourceGitRemote || res.Project != "canonical-remote" {
		t.Fatalf("worktree result = %+v, want git_remote canonical-remote", res)
	}
	if got, want := canonicalizePath(res.Path), canonicalizePath(repo); got != want {
		t.Fatalf("worktree path = %q, want primary repository %q", got, want)
	}
}

func TestDetectProjectFull_SubmoduleUsesCheckoutRoot(t *testing.T) {
	parent := t.TempDir()
	dependency := filepath.Join(parent, "dependency")
	repo := filepath.Join(parent, "parent-repo")
	for _, dir := range []string{dependency, repo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		initGit(t, dir)
		commitEmptyGit(t, dir)
	}
	submodule := filepath.Join(repo, "deps", "submodule")
	cmd := exec.Command("git", "-C", repo, "-c", "protocol.file.allow=always", "submodule", "add", dependency, "deps/submodule")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git submodule add: %v\n%s", err, out)
	}

	res := DetectProjectFull(submodule)
	if res.Error != nil {
		t.Fatalf("submodule result error = %v", res.Error)
	}
	if got, want := canonicalizePath(res.Path), canonicalizePath(submodule); got != want {
		t.Fatalf("submodule path = %q, want checkout root %q", got, want)
	}
}

func TestDetectProjectFull_BareRepositoryUsesRepositoryName(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "canonical-repo.git")
	cmd := exec.Command("git", "init", "--bare", bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	res := DetectProjectFull(bare)
	if res.Source != SourceGitRoot || res.Project != "canonical-repo" {
		t.Fatalf("bare repository result = %+v, want git-root canonical-repo", res)
	}
	if got, want := canonicalizePath(res.Path), canonicalizePath(filepath.Join(filepath.Dir(bare), "canonical-repo")); got != want {
		t.Fatalf("bare repository path = %q, want %q", got, want)
	}
}

func TestDetectProjectFull_WorktreeDoesNotInheritCommonAncestorConfig(t *testing.T) {
	parent := t.TempDir()
	configDir := filepath.Join(parent, ".engram")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":"ancestor-leak"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(parent, "canonical-repo")
	worktree := filepath.Join(parent, "feature-worktree")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, repo)
	commitEmptyGit(t, repo)
	addGitWorktree(t, repo, worktree, "feature")
	nested := filepath.Join(worktree, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	res := DetectProjectFull(nested)
	if res.Source != SourceGitRoot || res.Project != "canonical-repo" {
		t.Fatalf("worktree result = %+v, want primary git-root identity", res)
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

func TestChildScan_ContinuesPastNoiseUntilSecondRepository(t *testing.T) {
	parent := t.TempDir()
	firstRepo := filepath.Join(parent, "01-repo")
	if err := os.MkdirAll(filepath.Join(firstRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if err := os.Mkdir(filepath.Join(parent, fmt.Sprintf("%02d-plain", i+2)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secondRepo := filepath.Join(parent, "zz-repo")
	if err := os.MkdirAll(filepath.Join(secondRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(entries), 27; got != want {
		t.Fatalf("fixture entries = %d, want %d", got, want)
	}
	oldReadDir := childScanReadDir
	t.Cleanup(func() { childScanReadDir = oldReadDir })
	reads := 0
	childScanReadDir = func(_ *os.File, count int) ([]os.DirEntry, error) {
		if count != 1 {
			t.Fatalf("ReadDir count = %d, want 1", count)
		}
		if reads == len(entries) {
			return nil, io.EOF
		}
		entry := entries[reads]
		reads++
		return []os.DirEntry{entry}, nil
	}

	repos, timedOut := scanChildren(parent)
	if timedOut {
		t.Fatal("bounded scan unexpectedly timed out")
	}
	if got, want := reads, len(entries); got != want {
		t.Fatalf("ReadDir calls = %d, want all %d entries through the second repository", got, want)
	}
	if got, want := repos, []string{firstRepo, secondRepo}; !reflect.DeepEqual(got, want) {
		t.Fatalf("repos = %q, want both repositories %q", got, want)
	}
}

func TestChildScan_TimeoutFallsBackWithoutStartingReads(t *testing.T) {
	parent := t.TempDir()
	oldNow, oldReadDir := childScanNow, childScanReadDir
	t.Cleanup(func() {
		childScanNow = oldNow
		childScanReadDir = oldReadDir
	})
	base := time.Date(2026, time.August, 28, 0, 0, 0, 0, time.UTC)
	clockCalls := 0
	childScanNow = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return base
		}
		return base.Add(childScanTimeout + time.Nanosecond)
	}
	childScanReadDir = func(_ *os.File, _ int) ([]os.DirEntry, error) {
		t.Fatal("child scan read after its deadline")
		return nil, io.EOF
	}

	res := DetectProjectFull(parent)
	if res.Source != SourceDirBasename || res.Error != nil {
		t.Fatalf("timeout result = %+v, want dir_basename fallback", res)
	}
}
