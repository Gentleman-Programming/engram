package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRoutingCandidateObserveGitRoot(t *testing.T) {
	// A private discovery ceiling isolates fixtures from the host repository.
	root, err := os.MkdirTemp(os.Getenv("TEMP"), "g-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove private Git fixture: %v", err)
		}
	})
	// git discovery must not walk into the host worktree containing this fixture.
	t.Setenv("GIT_CEILING_DIRECTORIES", root)
	repo := filepath.Join(root, "repo")
	nested := filepath.Join(repo, "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repo, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	configDir := filepath.Join(repo, ".engram")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "config.json")
	data := []byte(`{"project_name":"not-the-root"}`)
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	wantInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{repo, nested} {
		got := ObserveGitRoot(directory)
		gotInfo, err := os.Stat(got)
		if err != nil {
			t.Fatalf("stat observed root %q: %v", got, err)
		}
		// Git may resolve a junction while TEMP retains its alias spelling.
		if !os.SameFile(gotInfo, wantInfo) {
			t.Fatalf("root %q want %q", got, repo)
		}
	}
	binding := filepath.Join(repo, ".git", repositoryBindingFilename)
	if _, err := os.Stat(binding); !os.IsNotExist(err) {
		t.Fatalf("binding initialized: %v", err)
	}
	if got, err := os.ReadFile(config); err != nil || string(got) != string(data) {
		t.Fatalf("config changed %q %v", got, err)
	}
	nonrepo := filepath.Join(root, "plain")
	if err := os.MkdirAll(nonrepo, 0700); err != nil {
		t.Fatal(err)
	}
	if got := ObserveGitRoot(nonrepo); got != "" {
		t.Fatalf("nonrepo root %q", got)
	}
	for _, name := range []string{"child-a", "child-b"} {
		child := filepath.Join(nonrepo, name)
		if err := os.MkdirAll(child, 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "-C", child, "init")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child init: %v %s", err, out)
		}
	}
	if got := ObserveGitRoot(nonrepo); got != "" {
		t.Fatalf("child promotion %q", got)
	}
	if got := ObserveGitRoot(filepath.Join(root, "missing")); got != "" {
		t.Fatalf("missing root %q", got)
	}
}

func TestRoutingCandidate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		input       RoutingCandidateInput
		key, source string
		invalid     bool
	}{
		{"designation", RoutingCandidateInput{Designation: " Unknown--Name ", GitRoot: "/repo/other", Directory: "raw/../cwd"}, "unknown-name", SourceExplicitOverride, false},
		{"git", RoutingCandidateInput{GitRoot: filepath.Join("repo", "Root--Name"), Directory: "different"}, "root-name", SourceGitRoot, false},
		{"nonrepo", RoutingCandidateInput{Directory: "/folder/not-a-key"}, "", "unknown", false},
		{"omitted", RoutingCandidateInput{}, "", "unknown", false},
		{"invalid designation", RoutingCandidateInput{Designation: "bad/name", GitRoot: "/repo/good"}, "", "", true},
		{"blank designation", RoutingCandidateInput{Designation: "   ", GitRoot: "/repo/good"}, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveRoutingCandidate(tc.input)
			if tc.invalid {
				if err == nil {
					t.Fatal("expected rejection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Key != tc.key || got.Source != tc.source || got.Directory != tc.input.Directory || got.RawProject != tc.input.Designation {
				t.Fatalf("candidate = %#v", got)
			}
		})
	}
}
