package main

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// TestIsPathLikeProjectName covers the detector used to single out garbage
// "path-as-name" projects (issue #283 bug 2 leftovers). It mirrors the
// write-boundary rejection in normalizeExplicitWriteProject: a project name that
// contains a path separator is a filesystem path, not a real project name.
func TestIsPathLikeProjectName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{`c:\users\foo`, true},
		{`/home/user`, true},
		{`c:/users/foo`, true},
		{`\\server\share`, true},
		{"d:\\workspace\\sub", true},
		{"engram", false},
		{"agent-teams-lite", false},
		{"e3", false},
		{"general", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isPathLikeProjectName(tc.name); got != tc.want {
			t.Errorf("isPathLikeProjectName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestCmdProjectsPrunePathsOnlyDryRun verifies that `--paths-only` narrows the
// prune candidate set to path-named projects with 0 observations, leaving
// legitimate empty projects and projects with observations untouched.
func TestCmdProjectsPrunePathsOnlyDryRun(t *testing.T) {
	cfg := testConfig(t)

	// Garbage: path-as-name project, 0 observations (only a session).
	mustSeedSession(t, cfg, "s-garbage", `c:\users\foo`)
	// Legitimate empty project, 0 observations — must NOT be selected by --paths-only.
	mustSeedSession(t, cfg, "s-legit", "legit-empty")
	// Real project with observations — never a prune candidate.
	mustSeedObservation(t, cfg, "s-real", "realproject", "note", "title", "content", "project")

	withArgs(t, "engram", "projects", "prune", "--paths-only", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdProjectsPrune(cfg) })

	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, `c:\users\foo`) {
		t.Fatalf("expected path-named project listed, got: %q", stdout)
	}
	if strings.Contains(stdout, "legit-empty") {
		t.Fatalf("legitimate empty project must NOT be listed with --paths-only, got: %q", stdout)
	}
	if strings.Contains(stdout, "realproject") {
		t.Fatalf("project with observations must never be a prune candidate, got: %q", stdout)
	}
	if !strings.Contains(stdout, "dry-run") {
		t.Fatalf("expected dry-run notice, got: %q", stdout)
	}
}

// TestCmdProjectsPrunePathsOnlyDeletesOnlyPathNamed verifies the actual
// (non-dry-run) deletion: `prune --paths-only` removes path-named empty projects
// while leaving legitimate empty projects and projects with observations intact.
func TestCmdProjectsPrunePathsOnlyDeletesOnlyPathNamed(t *testing.T) {
	cfg := testConfig(t)

	mustSeedSession(t, cfg, "s-garbage", `c:\users\foo`)
	mustSeedSession(t, cfg, "s-legit", "legit-empty")
	mustSeedObservation(t, cfg, "s-real", "realproject", "note", "title", "content", "project")

	// Answer "all" to prune every (path-named) candidate.
	oldScan := scanInputLine
	t.Cleanup(func() { scanInputLine = oldScan })
	scanInputLine = func(a ...any) (int, error) {
		if ptr, ok := a[0].(*string); ok {
			*ptr = "all"
		}
		return 1, nil
	}

	withArgs(t, "engram", "projects", "prune", "--paths-only")
	_, stderr := captureOutput(t, func() { cmdProjectsPrune(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	// Use ListProjectsWithStats: it includes 0-observation projects (derived from
	// sessions), so a not-pruned empty project like "legit-empty" still shows up.
	stats, err := s.ListProjectsWithStats()
	if err != nil {
		t.Fatalf("ListProjectsWithStats: %v", err)
	}
	names := make([]string, len(stats))
	for i, ps := range stats {
		names[i] = ps.Name
	}

	has := func(target string) bool {
		for _, n := range names {
			if n == target {
				return true
			}
		}
		return false
	}
	if has(`c:\users\foo`) {
		t.Fatalf("path-named project should have been pruned, still present: %v", names)
	}
	if !has("legit-empty") {
		t.Fatalf("legitimate empty project must NOT be pruned by --paths-only: %v", names)
	}
	if !has("realproject") {
		t.Fatalf("project with observations must remain: %v", names)
	}
}
