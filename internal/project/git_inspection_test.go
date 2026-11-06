package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGitInspectionRootProcessBudget(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)
	trace := filepath.Join(t.TempDir(), "git-trace")
	t.Setenv("GIT_TRACE", filepath.ToSlash(trace))
	res := DetectProjectFullWithOptions(dir, DetectionOptions{InspectOnly: true})
	if res.Error != nil || res.Source != SourceUnboundGit || DirectoryIdentity(res.Path) != DirectoryIdentity(dir) {
		t.Fatalf("incorrect root inspection: %+v", res)
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	commands := strings.Count(string(raw), "built-in: git ")
	if commands < 1 || commands > 2 {
		t.Fatalf("root inspection launched %d Git processes, want at most 2; trace:\n%s", commands, raw)
	}
}

func TestGitInspectionUnusualRootPaths(t *testing.T) {
	for _, name := range []string{"space and 'quote'", "snowman-☃", "line\nbreak"} {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.Contains(name, "\n") {
				t.Skip("Windows filenames cannot contain newlines")
			}
			dir := filepath.Join(t.TempDir(), name)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			initGit(t, dir)
			inspection := inspectGitCheckoutRoot(context.Background(), dir)
			if (inspection == nil) != strings.Contains(name, "\n") {
				t.Fatalf("unexpected fast-path admission for %q: %+v", name, inspection)
			}
			res := DetectProjectFullWithOptions(dir, DetectionOptions{InspectOnly: true})
			if res.Error != nil || res.Source != SourceUnboundGit || DirectoryIdentity(res.Path) != DirectoryIdentity(dir) {
				t.Fatalf("incorrect path inspection: %+v", res)
			}
		})
	}
}

func TestGitInspectionSeesConfigChanges(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)
	opts := DetectionOptions{InspectOnly: true}
	if res := DetectProjectFullWithOptions(dir, opts); res.Error != nil || res.Source != SourceUnboundGit {
		t.Fatalf("initial inspection: %+v", res)
	}
	if err := os.Mkdir(filepath.Join(dir, ".engram"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, ".engram", "config.json")
	if err := os.WriteFile(config, []byte(`{"project_name":"changed-project"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := DetectProjectFullWithOptions(dir, opts); res.Error != nil || res.Source != SourceConfig || res.Project != "changed-project" {
		t.Fatalf("config change was hidden: %+v", res)
	}
	if err := os.WriteFile(config, []byte(`not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := DetectProjectFullWithOptions(dir, opts); !errors.Is(res.Error, ErrInvalidConfig) {
		t.Fatalf("invalid config was bypassed: %+v", res)
	}
}

func TestGitInspectionCanceledMetadataCannotReachHistory(t *testing.T) {
	dir := t.TempDir()
	initGit(t, dir)
	inspection := inspectGitCheckoutRoot(context.Background(), dir)
	if inspection == nil {
		t.Fatal("expected root metadata")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := inspectGitCheckoutRoot(ctx, dir); got != nil {
		t.Fatal("canceled inspection returned metadata")
	}
	opts := DetectionOptions{Context: ctx, HistoryLookup: func(string) ([]string, error) {
		t.Fatal("canceled metadata reached history lookup")
		return nil, nil
	}}
	res, handled := detectFromGitBinding(dir, opts, dir, inspection)
	if !handled || !errors.Is(res.Error, context.Canceled) {
		t.Fatalf("canceled cached binding inspection: %+v handled=%t", res, handled)
	}
	if _, err := readRepositoryBinding(inspection.commonDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled inspection published a binding: %v", err)
	}
}

func TestGitInspectionMarkerFallback(t *testing.T) {
	for _, kind := range []string{"missing", "malformed", "dangling"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			switch kind {
			case "malformed":
				if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("invalid git marker"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "dangling":
				if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), filepath.Join(dir, ".git")); err != nil {
					t.Skipf("symlink fixture unavailable: %v", err)
				}
			}
			if got := inspectGitCheckoutRoot(context.Background(), dir); got != nil {
				t.Fatalf("invalid marker admitted metadata: %+v", got)
			}
			trace := filepath.Join(t.TempDir(), "trace")
			t.Setenv("GIT_TRACE", filepath.ToSlash(trace))
			res := DetectProjectFullWithOptions(dir, DetectionOptions{InspectOnly: true})
			if res.Error != nil || res.Source != SourceDirBasename || res.Project != fallbackProjectName(dir) {
				t.Fatalf("legacy fallback changed: %+v", res)
			}
			raw, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Count(string(raw), "built-in: git ")
			want := 3 // failed batch, then the two original failed probes
			if kind == "missing" {
				want = 2
			}
			if calls != want {
				t.Fatalf("fallback processes=%d want%d; trace:\n%s", calls, want, raw)
			}
		})
	}
}

func TestGitInspectionHonorsEnvironmentOverrides(t *testing.T) {
	for _, override := range []string{"worktree", "common-dir"} {
		t.Run(override, func(t *testing.T) {
			dir, other := t.TempDir(), t.TempDir()
			initGit(t, dir)
			initGit(t, other)
			if override == "worktree" {
				t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
				t.Setenv("GIT_WORK_TREE", other)
				inspection := inspectGitCheckoutRoot(context.Background(), dir)
				if inspection == nil || DirectoryIdentity(inspection.worktreeDir) != DirectoryIdentity(other) || DirectoryIdentity(inspection.gitDir) != DirectoryIdentity(filepath.Join(other, ".git")) {
					t.Fatalf("Git worktree override ignored: %+v", inspection)
				}
				// An explicit cwd config still precedes the Git identity,
				// including when Git's worktree is elsewhere.
				if err := os.Mkdir(filepath.Join(dir, ".engram"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".engram", "config.json"), []byte(`{"project_name":"cwd-lock"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				res := DetectProjectFullWithOptions(dir, DetectionOptions{InspectOnly: true})
				if res.Error != nil || res.Source != SourceConfig || res.Project != "cwd-lock" {
					t.Fatalf("cwd config precedence changed: %+v", res)
				}
				return
			} else {
				t.Setenv("GIT_COMMON_DIR", filepath.Join(other, ".git"))
			}
			res := DetectProjectFullWithOptions(dir, DetectionOptions{InspectOnly: true})
			if res.Error != nil || res.Source != SourceUnboundGit || DirectoryIdentity(res.Path) != DirectoryIdentity(other) {
				t.Fatalf("Git override ignored: %+v", res)
			}
		})
	}
}
