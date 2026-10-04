package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFirstBindingHistoryPrecaution(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	for _, tt := range []struct {
		name                  string
		history               []string
		lookupErr             error
		conflict, unavailable bool
	}{
		{name: "empty"}, {name: "matching", history: []string{"remote-app"}},
		{name: "different", history: []string{"local-app"}, conflict: true},
		{name: "multiple", history: []string{"remote-app", "local-app"}, conflict: true},
		{name: "unavailable", lookupErr: errors.New("store closed"), unavailable: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newIdentityGitRepo(t, "local-app")
			gitIdentity(t, repo, "remote", "add", "origin", "https://example.test/team/remote-app.git")
			lookup := func(dir string) ([]string, error) {
				if dir != DirectoryIdentity(repo) {
					t.Fatalf("history directory=%q", dir)
				}
				return tt.history, tt.lookupErr
			}
			got := DetectProjectFullWithOptions(repo, DetectionOptions{HistoryLookup: lookup})
			var conflict *ProjectTransitionError
			if errors.As(got.Error, &conflict) != tt.conflict || errors.Is(got.Error, ErrProjectHistoryUnavailable) != tt.unavailable {
				t.Fatalf("result=%+v", got)
			}
			_, err := os.Stat(repositoryBindingPath(detectGitCommonDir(repo)))
			if tt.conflict || tt.unavailable {
				if !os.IsNotExist(err) || got.Project != "" {
					t.Fatalf("unsafe binding/result: %+v, %v", got, err)
				}
			} else if err != nil || got.Project != "remote-app" {
				t.Fatalf("binding/result: %+v, %v", got, err)
			}
		})
	}
}

func TestBindingInspectionAndEstablishedIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	repo := newIdentityGitRepo(t, "local-app")
	gitIdentity(t, repo, "remote", "add", "origin", "https://example.test/team/remote-app.git")
	nested := filepath.Join(repo, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	inspect := DetectProjectFullWithOptions(nested, DetectionOptions{InspectOnly: true})
	if inspect.Error != nil || inspect.Source != SourceUnboundGit {
		t.Fatalf("inspect=%+v", inspect)
	}
	if _, err := os.Stat(repositoryBindingPath(detectGitCommonDir(repo))); !os.IsNotExist(err) {
		t.Fatalf("inspection created binding: %v", err)
	}
	got := DetectProjectFullWithOptions(nested, DetectionOptions{HistoryLookup: func(dir string) ([]string, error) {
		if dir != DirectoryIdentity(nested) {
			t.Fatalf("collapsed nested directory: %q", dir)
		}
		return nil, nil
	}})
	if got.Error != nil {
		t.Fatal(got.Error)
	}
	gitIdentity(t, repo, "remote", "set-url", "origin", "https://example.test/team/changed.git")
	again := DetectProjectFullWithOptions(repo, DetectionOptions{HistoryLookup: func(string) ([]string, error) { t.Fatal("established binding must not query history"); return nil, nil }})
	if again.Error != nil || again.Project != got.Project {
		t.Fatalf("established binding changed: %+v", again)
	}
}

func TestFirstBindingPromotionKeepsRequestedDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	initGit(t, child)
	got := DetectProjectFullWithOptions(parent, DetectionOptions{HistoryLookup: func(dir string) ([]string, error) {
		if dir != DirectoryIdentity(parent) {
			t.Errorf("lost requested cwd: %q", dir)
			return nil, nil
		}
		return []string{"parent-history"}, nil
	}})
	var conflict *ProjectTransitionError
	if !errors.As(got.Error, &conflict) {
		t.Fatalf("parent history ignored: %+v", got)
	}
	if _, err := os.Stat(repositoryBindingPath(detectGitCommonDir(child))); !os.IsNotExist(err) {
		t.Fatalf("promotion published binding: %v", err)
	}
}

func TestFirstBindingConcurrentPublisherWins(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	repo := newIdentityGitRepo(t, "local-app")
	got := DetectProjectFullWithOptions(repo, DetectionOptions{HistoryLookup: func(string) ([]string, error) {
		// Another caller publishes after inspection but before this caller does.
		if _, err := loadOrCreateRepositoryBinding(detectGitCommonDir(repo), "established-winner"); err != nil {
			t.Fatal(err)
		}
		return nil, nil
	}})
	if got.Error != nil || got.Project != "established-winner" {
		t.Fatalf("publisher race: %+v", got)
	}
}

func TestFirstBindingHistoryUsesLinkedWorktreeDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	repo := newIdentityGitRepo(t, "primary")
	commitEmptyGit(t, repo)
	linked := filepath.Join(t.TempDir(), "linked")
	addGitWorktree(t, repo, linked, "feature")
	got := DetectProjectFullWithOptions(linked, DetectionOptions{HistoryLookup: func(dir string) ([]string, error) {
		if dir != DirectoryIdentity(linked) {
			t.Fatalf("used primary instead of linked directory: %q", dir)
		}
		return []string{"prior-project"}, nil
	}})
	var conflict *ProjectTransitionError
	if !errors.As(got.Error, &conflict) {
		t.Fatalf("linked history ignored: %+v", got)
	}
	if _, err := os.Stat(repositoryBindingPath(detectGitCommonDir(repo))); !os.IsNotExist(err) {
		t.Fatalf("unexpected shared binding: %v", err)
	}
}
