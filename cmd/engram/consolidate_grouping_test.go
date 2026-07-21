package main

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// groupContaining returns the group whose Names include target, or nil.
func groupContaining(groups []projectGroup, target string) *projectGroup {
	for i := range groups {
		for _, n := range groups[i].Names {
			if n == target {
				return &groups[i]
			}
		}
	}
	return nil
}

// TestGroupSimilarProjects_NoisySharedDirNotGrouped reproduces issue #283 bug 4:
// many unrelated projects that merely share a common parent/root directory
// (e.g. $HOME or a session root) must NOT be unioned into one giant component.
// Before the fix, the shared-directory union step grouped all of them together
// and consolidate would propose merging them into a single canonical project —
// a catastrophic data-loss footgun.
func TestGroupSimilarProjects_NoisySharedDirNotGrouped(t *testing.T) {
	// Names are deliberately long and mutually dissimilar (no substring overlap,
	// Levenshtein distance well above the scaled threshold) so the ONLY possible
	// grouping signal is the shared "/home/user" directory.
	const sharedHome = "/home/user"
	projects := []store.ProjectStats{
		{Name: "kubernetes", ObservationCount: 5, Directories: []string{"/home/user/kubernetes", sharedHome}},
		{Name: "photoshop", ObservationCount: 7, Directories: []string{"/home/user/photoshop", sharedHome}},
		{Name: "wireguard", ObservationCount: 3, Directories: []string{"/home/user/wireguard", sharedHome}},
		{Name: "blender", ObservationCount: 9, Directories: []string{"/home/user/blender", sharedHome}},
		{Name: "terraform", ObservationCount: 2, Directories: []string{"/home/user/terraform", sharedHome}},
	}

	groups := groupSimilarProjects(projects)

	// The five names are mutually dissimilar, so the ONLY thing that could group
	// them is the shared "/home/user" directory. Since that directory is touched
	// by more than maxSharedProjectsForDirMatch distinct projects, it must be
	// treated as noise and skipped — yielding no groups at all.
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups (noisy shared dir must be skipped), got %d: %+v", len(groups), groups)
	}
}

// TestGroupSimilarProjects_RealSharedDirStillGroups guards against over-correcting:
// when only a small number of projects share a directory (a genuine rename signal),
// they SHOULD still be grouped.
func TestGroupSimilarProjects_RealSharedDirStillGroups(t *testing.T) {
	const sharedRepo = "/repos/shared-monorepo"
	projects := []store.ProjectStats{
		{Name: "webapp", ObservationCount: 12, Directories: []string{sharedRepo}},
		{Name: "legacy-portal", ObservationCount: 4, Directories: []string{sharedRepo}},
	}

	groups := groupSimilarProjects(projects)

	g := groupContaining(groups, "webapp")
	if g == nil {
		t.Fatalf("expected webapp and legacy-portal to be grouped via shared dir, got groups: %+v", groups)
	}
	if len(g.Names) != 2 {
		t.Fatalf("expected group of 2, got %d: %+v", len(g.Names), g.Names)
	}
}

// TestGroupSimilarProjects_NameSimilarityStillWorks confirms the legitimate
// name-based grouping path is untouched by the shared-dir fix.
func TestGroupSimilarProjects_NameSimilarityStillWorks(t *testing.T) {
	// "frontend" is a substring of "frontend-app" → name-similar (no shared dir).
	projects := []store.ProjectStats{
		{Name: "frontend", ObservationCount: 8, Directories: []string{"/a"}},
		{Name: "frontend-app", ObservationCount: 5, Directories: []string{"/b"}},
		{Name: "unrelated", ObservationCount: 1, Directories: []string{"/c"}},
	}

	groups := groupSimilarProjects(projects)

	g := groupContaining(groups, "frontend")
	if g == nil {
		t.Fatalf("expected frontend* projects grouped by name similarity, got: %+v", groups)
	}
	if len(g.Names) != 2 {
		t.Fatalf("expected name-similar group of 2, got %d: %+v", len(g.Names), g.Names)
	}
	if groupContaining(groups, "unrelated") != nil {
		t.Fatalf("unrelated project must not be grouped: %+v", groups)
	}
}

// TestCmdProjectsConsolidateAllDoesNotMergeNoisySharedDir is a command-level
// regression test for issue #283 bug 4: `projects consolidate --all` must not
// propose merging many unrelated projects just because their sessions share a
// common directory. mustSeedSession records every session under the same "/tmp"
// directory, so seeding several distinctly-named projects reproduces the noisy
// shared-directory scenario end-to-end.
func TestCmdProjectsConsolidateAllDoesNotMergeNoisySharedDir(t *testing.T) {
	cfg := testConfig(t)

	for _, name := range []string{"kubernetes", "photoshop", "wireguard", "blender", "terraform"} {
		mustSeedSession(t, cfg, "s-"+name, name)
	}

	withArgs(t, "engram", "projects", "consolidate", "--all", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })

	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "No similar project name groups found") {
		t.Fatalf("expected no groups for projects sharing only a noisy dir, got: %q", stdout)
	}
	if strings.Contains(stdout, "Would merge") {
		t.Fatalf("must not propose a merge for noisy-shared-dir projects, got: %q", stdout)
	}
}
