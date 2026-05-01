// Package project provides utilities for detecting and normalizing project names.
//
// It replicates the detection logic from the Claude Code shell helpers and
// OpenCode TypeScript plugin in pure Go, so CLI and MCP server can share
// a single canonical implementation.
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrAmbiguousProject is returned when the working directory is a parent of
// multiple git repositories and we cannot auto-select one.
var ErrAmbiguousProject = errors.New("ambiguous project: multiple git repos found in cwd")

// ErrInvalidConfig is returned when .engram/config.json exists but cannot be
// used as a project write lock.
var ErrInvalidConfig = errors.New("invalid .engram/config.json")

// Source constants describe how the project name was resolved.
const (
	SourceGitRemote        = "git_remote"        // derived from git remote origin URL
	SourceGitRoot          = "git_root"          // derived from git repository root basename
	SourceGitChild         = "git_child"         // auto-promoted from single child git repo
	SourceDirBasename      = "dir_basename"      // fallback: directory basename
	SourceAmbiguous        = "ambiguous"         // cwd contains multiple git repos (Case 4)
	SourceExplicitOverride = "explicit_override" // JR2-2: caller explicitly supplied a project name
	SourceRequestBody      = "request_body"      // REQ-414: project came from the request body (server-side, no filesystem path)
	SourceConfig           = "config"            // derived from .engram/config.json project_name
)

// noiseSet lists directory names that are skipped during child-repo scanning.
var noiseSet = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	".venv":        true,
	"__pycache__":  true,
	"target":       true,
	"dist":         true,
	"build":        true,
	".idea":        true,
	".vscode":      true,
}

// DetectionResult carries the full output of DetectProjectFull.
type DetectionResult struct {
	// Project is the resolved project name. Empty when Error==ErrAmbiguousProject.
	Project string
	// Source describes how the project name was derived.
	Source string
	// Path is the canonical directory associated with the project
	// (repo root for git cases, input dir for dir_basename).
	Path string
	// Warning is a non-empty advisory message when Source==SourceGitChild.
	Warning string
	// Error is non-nil only for ErrAmbiguousProject.
	Error error
	// AvailableProjects is populated only when Error==ErrAmbiguousProject.
	AvailableProjects []string
}

// DetectProjectFull resolves the project for dir using a 5-case algorithm:
//
//  1. git_remote — cwd is a git root with a remote → derive name from remote URL
//  2. git_root   — cwd is inside a git repo → use repo root basename
//  3. git_child  — cwd has exactly one git-repo child → auto-promote it
//  4. ambiguous  — cwd has multiple git-repo children → return ErrAmbiguousProject
//  5. dir_basename — none of the above → use filepath.Base(dir)
func DetectProjectFull(dir string) DetectionResult {
	if dir == "" {
		dir = "."
	}
	// Guard against arg injection.
	if strings.HasPrefix(dir, "-") {
		dir = "./" + dir
	}

	if res, ok := detectFromConfig(dir); ok {
		return res
	}

	// ── Case 1: git_remote ──────────────────────────────────────────────
	if name := detectFromGitRemote(dir); name != "" {
		// JS2: use repo root as Path for consistency with Case 2 (git_root).
		// When called from a subdir, both cases should set Path to the root.
		path := detectGitRootDir(dir)
		if path == "" {
			// Fallback: should not happen if detectFromGitRemote succeeded, but be safe.
			path, _ = filepath.Abs(dir)
		}
		return DetectionResult{
			Project: normalize(name),
			Source:  SourceGitRemote,
			Path:    path,
		}
	}

	// ── Case 2: git_root (includes subdir case) ─────────────────────────
	if root := detectGitRootDir(dir); root != "" {
		return DetectionResult{
			Project: normalize(filepath.Base(root)),
			Source:  SourceGitRoot,
			Path:    root,
		}
	}

	// ── Cases 3 & 4: scan child directories ────────────────────────────
	children, timedOut := scanChildren(dir)
	if timedOut {
		// Fall through to dir_basename (Case 5).
		goto basename
	}
	switch len(children) {
	case 1:
		// Case 3: exactly one child repo — auto-promote.
		child := children[0]
		childName := normalize(filepath.Base(child))
		absChild, _ := filepath.Abs(child)
		return DetectionResult{
			Project: childName,
			Source:  SourceGitChild,
			Path:    absChild,
			Warning: "auto-promoted child repository: " + childName,
		}
	default:
		if len(children) > 1 {
			// Case 4: multiple children → ambiguous.
			names := make([]string, len(children))
			for i, c := range children {
				names[i] = normalize(filepath.Base(c))
			}
			absDir, _ := filepath.Abs(dir)
			// REQ-304: Project is empty on ambiguous (spec is authoritative).
			// DetectProject wrapper handles CLI compat by using filepath.Base on error.
			// JW3: use SourceAmbiguous (not SourceDirBasename) to avoid misleading consumers.
			return DetectionResult{
				Project:           "",
				Source:            SourceAmbiguous,
				Path:              absDir,
				Error:             ErrAmbiguousProject,
				AvailableProjects: names,
			}
		}
	}

basename:
	// ── Case 5: dir_basename ─────────────────────────────────────────────
	absDir, _ := filepath.Abs(dir)
	base := filepath.Base(dir)
	if base == "" || base == "." {
		base = "unknown"
	}
	return DetectionResult{
		Project: normalize(base),
		Source:  SourceDirBasename,
		Path:    absDir,
	}
}

type configFile struct {
	ProjectName string `json:"project_name"`
}

func detectFromConfig(dir string) (DetectionResult, bool) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}

	// Project config is a project/repo lock, not a global ancestor setting. When
	// cwd is inside git, only the repository root may define the lock. This keeps
	// ~/.engram/config.json (the global data directory) from being inherited by
	// every directory under $HOME.
	if gitRoot := detectGitRootDir(absDir); gitRoot != "" {
		return readConfigAt(gitRoot)
	}

	// Outside git, accept only the current directory's config. Do not walk to
	// arbitrary parents such as $HOME.
	return readConfigAt(absDir)
}

func readConfigAt(projectDir string) (DetectionResult, bool) {
	configPath := filepath.Join(projectDir, ".engram", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DetectionResult{}, false
		}
		return invalidConfigResult(projectDir, fmt.Errorf("read %s: %w", configPath, err)), true
	}

	var cfg configFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return invalidConfigResult(projectDir, fmt.Errorf("parse %s: %w", configPath, err)), true
	}
	projectName, err := normalizeConfigProjectName(cfg.ProjectName)
	if err != nil {
		return invalidConfigResult(projectDir, err), true
	}
	return DetectionResult{Project: projectName, Source: SourceConfig, Path: projectDir}, true
}

func invalidConfigResult(path string, err error) DetectionResult {
	return DetectionResult{
		Project: "",
		Source:  SourceConfig,
		Path:    path,
		Error:   fmt.Errorf("%w: %v", ErrInvalidConfig, err),
	}
}

func normalizeConfigProjectName(projectName string) (string, error) {
	trimmed := strings.TrimSpace(projectName)
	if trimmed == "" {
		return "", fmt.Errorf("%w: project_name is required", ErrInvalidConfig)
	}
	if strings.ContainsAny(trimmed, `/\\`) {
		return "", fmt.Errorf("%w: project_name must be a name, not a path", ErrInvalidConfig)
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: project_name contains control characters", ErrInvalidConfig)
		}
	}
	return normalize(trimmed), nil
}

// detectGitRootDir returns the git repository root for dir, or "" if not in a repo.
func detectGitRootDir(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(out))
	return root
}

// scanChildren scans dir at depth=1 for git repositories, skipping noise dirs,
// hidden dirs, enforcing a 200ms timeout, a 20-entry cap, and short-circuiting
// as soon as more than 1 repo is found.
// Returns the list of found git-repo paths and a boolean indicating timeout.
func scanChildren(dir string) (repos []string, timedOut bool) {
	deadline := time.Now().Add(200 * time.Millisecond)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}

	scanned := 0
	for _, entry := range entries {
		if time.Now().After(deadline) {
			return repos, true
		}
		if scanned >= 20 {
			break
		}
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip hidden directories (prefix ".").
		if strings.HasPrefix(name, ".") {
			continue
		}
		// Skip known noise directories.
		if noiseSet[name] {
			continue
		}
		scanned++
		childPath := filepath.Join(dir, name)
		// Check if this child is a git repo (has a .git entry).
		gitPath := filepath.Join(childPath, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			repos = append(repos, childPath)
			// Short-circuit: as soon as we have > 1, no need to keep scanning.
			if len(repos) > 1 {
				return repos, false
			}
		}
	}
	return repos, false
}

// DetectProject detects the project name for a given directory.
// Priority: git remote origin repo name → git root basename → dir basename.
// The returned name is always non-empty and already normalized (lowercase, trimmed).
// This function is a backward-compatible wrapper around DetectProjectFull.
// On ErrAmbiguousProject, falls back to filepath.Base(dir) so CLI callers
// never receive an empty string (design §9 backward-compat requirement).
func DetectProject(dir string) string {
	res := DetectProjectFull(dir)
	if errors.Is(res.Error, ErrAmbiguousProject) {
		// CLI compat: return basename rather than empty string.
		if dir == "" {
			return "unknown"
		}
		base := filepath.Base(dir)
		if base == "" || base == "." {
			return "unknown"
		}
		return normalize(base)
	}
	if res.Project == "" {
		return "unknown"
	}
	return res.Project
}

// normalize applies canonical project name rules: lowercase + trim whitespace.
// It mirrors the normalization applied by the store layer so that DetectProject
// always returns a value that is consistent with stored project names.
func normalize(name string) string {
	n := strings.TrimSpace(strings.ToLower(name))
	if n == "" {
		return "unknown"
	}
	return n
}

// detectFromGitRemote attempts to determine the project name from the git
// remote "origin" URL. Returns empty string if git is unavailable, the
// directory is not a repo, or there is no origin remote.
func detectFromGitRemote(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(string(out))
	return extractRepoName(url)
}

// extractRepoName parses a git remote URL and returns just the repository name.
//
// Supported URL formats:
//   - SSH:   git@github.com:user/repo.git
//   - HTTPS: https://github.com/user/repo.git
//   - Either with or without the trailing .git suffix
func extractRepoName(url string) string {
	// Strip trailing .git suffix
	url = strings.TrimSuffix(url, ".git")

	// Split on both "/" and ":" to handle SSH and HTTPS uniformly
	parts := strings.FieldsFunc(url, func(r rune) bool {
		return r == '/' || r == ':'
	})
	if len(parts) == 0 {
		return ""
	}
	name := parts[len(parts)-1]
	return strings.TrimSpace(name)
}
