package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/command"
)

// gitInspection belongs to one detection, never a cache across requests.
type gitInspection struct {
	commonDir   string
	worktreeDir string
	gitDir      string
}

// inspectGitCheckoutRoot batches metadata only at checkout roots. Other layouts
// and ambiguous output retain the existing Git probes. Git remains authoritative;
// Lstat only gates the optimization without following a Git-marker symlink.
func inspectGitCheckoutRoot(parent context.Context, dir string) *gitInspection {
	if parent.Err() != nil {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	out, err := command.NewContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute",
		"--git-common-dir", "--show-toplevel", "--git-dir").Output()
	if err != nil {
		return nil
	}
	// Paths containing newlines, unsupported output and failures fall back;
	// never interpret an ambiguous record as authoritative metadata.
	paths := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(paths) != 3 {
		return nil
	}
	for i, path := range paths {
		paths[i] = filepath.Clean(strings.TrimSpace(path))
		if !filepath.IsAbs(paths[i]) {
			return nil
		}
	}
	return &gitInspection{commonDir: paths[0], worktreeDir: paths[1], gitDir: paths[2]}
}
