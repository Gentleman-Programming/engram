package project

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestDetectProjectFull_RepositoryBindingPreservesRemoteLabel(t *testing.T) {
	repo := newIdentityGitRepo(t, "repo")
	gitIdentity(t, repo, "remote", "add", "origin", "git@example.test:team/original-project.git")

	before := DetectProjectFull(repo)
	if before.Error != nil || before.Project != "original-project" || before.Source != SourceGitRemote {
		t.Fatalf("initial detection = %+v, want original remote label", before)
	}

	gitIdentity(t, repo, "remote", "set-url", "origin", "git@example.test:team/renamed-project.git")
	after := DetectProjectFull(repo)
	if after.Error != nil || after.Project != before.Project || after.Source != SourceGitRemote {
		t.Fatalf("renamed remote detection = %+v, want bound project %q", after, before.Project)
	}
}

func TestDetectProjectFull_RepositoryBindingSharesLinkedWorktree(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "primary")
	worktree := filepath.Join(parent, "linked")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, repo)
	commitEmptyGit(t, repo)
	gitIdentity(t, repo, "remote", "add", "origin", "git@example.test:team/original-project.git")

	primary := DetectProjectFull(repo)
	if primary.Error != nil {
		t.Fatal(primary.Error)
	}
	addGitWorktree(t, repo, worktree, "feature")
	linked := DetectProjectFull(worktree)
	if linked.Error != nil || linked.Project != primary.Project {
		t.Fatalf("linked worktree detection = %+v, want project %q", linked, primary.Project)
	}

	primaryBinding, err := readRepositoryBinding(detectGitCommonDir(repo))
	if err != nil {
		t.Fatal(err)
	}
	linkedBinding, err := readRepositoryBinding(detectGitCommonDir(worktree))
	if err != nil {
		t.Fatal(err)
	}
	if primaryBinding.ID != linkedBinding.ID {
		t.Fatalf("worktree binding ID = %q, want primary binding ID %q", linkedBinding.ID, primaryBinding.ID)
	}
}

func TestDetectProjectFull_RepositoryBindingSurvivesMove(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "original-directory")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, repo)
	first := DetectProjectFull(repo)
	if first.Error != nil {
		t.Fatal(first.Error)
	}
	moved := filepath.Join(parent, "moved-directory")
	if err := os.Rename(repo, moved); err != nil {
		t.Skipf("repository move is unavailable on this platform: %v", err)
	}
	got := DetectProjectFull(moved)
	if got.Error != nil || got.Project != first.Project {
		t.Fatalf("moved repository detection = %+v, want project %q", got, first.Project)
	}
}

func TestRepositoryBinding_CloneGetsDistinctOpaqueID(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "source")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, repo)
	commitEmptyGit(t, repo)
	if res := DetectProjectFull(repo); res.Error != nil {
		t.Fatal(res.Error)
	}

	clone := filepath.Join(parent, "clone")
	gitIdentity(t, parent, "clone", repo, clone)
	if res := DetectProjectFull(clone); res.Error != nil {
		t.Fatal(res.Error)
	}

	sourceBinding, err := readRepositoryBinding(detectGitCommonDir(repo))
	if err != nil {
		t.Fatal(err)
	}
	cloneBinding, err := readRepositoryBinding(detectGitCommonDir(clone))
	if err != nil {
		t.Fatal(err)
	}
	if sourceBinding.ID == cloneBinding.ID {
		t.Fatal("clone reused the source repository opaque ID")
	}
}

func TestDetectProjectFull_CorruptRepositoryBindingFailsClosed(t *testing.T) {
	repo := newIdentityGitRepo(t, "repo")
	gitIdentity(t, repo, "remote", "add", "origin", "git@example.test:team/original-project.git")
	if res := DetectProjectFull(repo); res.Error != nil {
		t.Fatal(res.Error)
	}
	if err := os.WriteFile(repositoryBindingPath(detectGitCommonDir(repo)), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIdentity(t, repo, "remote", "set-url", "origin", "git@example.test:team/renamed-project.git")

	res := DetectProjectFull(repo)
	if !errors.Is(res.Error, ErrRepositoryBinding) {
		t.Fatalf("corrupt binding error = %v, want ErrRepositoryBinding", res.Error)
	}
	if res.Project != "" {
		t.Fatalf("corrupt binding project = %q, want empty", res.Project)
	}
}

func TestRepositoryBinding_WriteFailureFailsClosed(t *testing.T) {
	_, err := loadOrCreateRepositoryBinding(filepath.Join(t.TempDir(), "missing"), "project")
	if !errors.Is(err, ErrRepositoryBinding) {
		t.Fatalf("write failure error = %v, want ErrRepositoryBinding", err)
	}
}

func TestDetectProjectFull_NonGitAndConfigRemainCompatible(t *testing.T) {
	nonGit := filepath.Join(t.TempDir(), "plain-directory")
	if err := os.Mkdir(nonGit, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DetectProjectFull(nonGit); got.Error != nil || got.Source != SourceDirBasename || got.Project != "plain-directory" {
		t.Fatalf("non-Git detection = %+v, want dir basename", got)
	}

	repo := newIdentityGitRepo(t, "repo")
	configDir := filepath.Join(repo, ".engram")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":"configured-project"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DetectProjectFull(repo); got.Error != nil || got.Source != SourceConfig || got.Project != "configured-project" {
		t.Fatalf("configured detection = %+v, want configured project", got)
	}
	if _, err := os.Stat(repositoryBindingPath(detectGitCommonDir(repo))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config detection unexpectedly created a binding: %v", err)
	}
}

func TestRepositoryBinding_ConcurrentCreationConverges(t *testing.T) {
	commonDir := t.TempDir()
	const workers = 24
	bindings := make(chan repositoryBinding, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			binding, err := loadOrCreateRepositoryBinding(commonDir, "project")
			if err != nil {
				errs <- err
				return
			}
			bindings <- binding
		}()
	}
	wg.Wait()
	close(bindings)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first repositoryBinding
	for binding := range bindings {
		if first.ID == "" {
			first = binding
			continue
		}
		if binding != first {
			t.Fatalf("concurrent binding = %+v, want %+v", binding, first)
		}
	}
}

func newIdentityGitRepo(t *testing.T, name string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, repo)
	return repo
}

func gitIdentity(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
