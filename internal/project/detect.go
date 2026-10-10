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
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/command"
)

// ErrAmbiguousProject is returned when the working directory is a parent of
// multiple git repositories and we cannot auto-select one.
var ErrAmbiguousProject = errors.New("ambiguous project: multiple git repos found in cwd")

// ErrInvalidConfig is returned when .engram/config.json exists but cannot be
// used as a project write lock.
var ErrInvalidConfig = errors.New("invalid .engram/config.json")

// Source constants describe how the project name was resolved.
const (
	SourceGitRemote        = "git_remote"        // current repository has an origin remote; Project may come from its binding
	SourceGitRoot          = "git_root"          // current repository has no origin remote; Project may come from its binding
	SourceGitChild         = "git_child"         // auto-promoted from single child git repo
	SourceDirBasename      = "dir_basename"      // fallback: directory basename
	SourceAmbiguous        = "ambiguous"         // cwd contains multiple git repos (Case 4)
	SourceExplicitOverride = "explicit_override" // JR2-2: caller explicitly supplied a project name
	SourceSessionProject   = "session"           // caller supplied a session_id with an existing project
	// SourceUserSelectedAfterAmbiguousProject means an MCP write initially hit
	// ErrAmbiguousProject and the caller provided an explicit user-selected
	// project from the ambiguity result's available_projects list.
	SourceUserSelectedAfterAmbiguousProject = "user_selected_after_ambiguous_project"
	SourceRequestBody                       = "request_body"     // REQ-414: project came from the request body (server-side, no filesystem path)
	SourceConfig                            = "config"           // derived from .engram/config.json project_name
	SourceAllProjects                       = "all_projects"     // caller asked for cross-project search (no single project resolved)
	SourceProcessOverride                   = "process_override" // resolved from the process-level project override
	// SourceStoredProject means the observation record itself carries the
	// project. The read-only mem_get_observation tool falls back to
	// it when cwd resolution fails only because the current directory is
	// ambiguous: the integer id fully identifies the record, so its stored
	// project is a safe anchor.
	SourceStoredProject = "stored_project"
)

// EnvProjectOverride names the environment variable that carries the
// process-level project override.
const EnvProjectOverride = "ENGRAM_PROJECT"

// ProcessOverride returns the single process-level project override that every
// entry point (CLI, MCP, HTTP server) applies before working-directory
// detection. It is the one precedence rule for process-level identity:
//
//  1. explicit process argument — `engram mcp --project <name>`, which the MCP
//     server carries as MCPConfig.DefaultProject;
//  2. the ENGRAM_PROJECT environment variable;
//  3. no override, so the caller falls back to cwd detection.
//
// A request-scoped project (the CLI `engram save --project` flag or an MCP tool
// argument) is resolved by the caller before this rule and always wins over it.
// The returned name is trimmed but not normalized; callers normalize with
// store.NormalizeProject so the operator still sees the normalization warning.
func ProcessOverride(explicit string) (string, bool) {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return trimmed, true
	}
	if trimmed := strings.TrimSpace(os.Getenv(EnvProjectOverride)); trimmed != "" {
		return trimmed, true
	}
	return "", false
}

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

const (
	childScanTimeout = 200 * time.Millisecond
)

// childScanNow and childScanReadDir are test seams for bounded child scans.
// Production uses the wall clock and synchronous directory reads, so it never
// starts a goroutine that could outlive a blocked filesystem operation.
var (
	childScanNow     = time.Now
	childScanReadDir = func(directory *os.File, count int) ([]os.DirEntry, error) {
		return directory.ReadDir(count)
	}
)

// DetectionResult carries the full output of DetectProjectFull.
type DetectionResult struct {
	// Project is the resolved project name. Empty when detection returns an error.
	Project string
	// Source describes the resolution path. For Git projects, it reflects current
	// origin-remote presence while Project may come from the stored binding.
	Source string
	// Path is the canonical directory associated with the project
	// (repo root for git cases, input dir for dir_basename).
	Path string
	// Warning is a non-empty advisory message when Source==SourceGitChild.
	Warning string
	// Error is non-nil when detection cannot safely resolve a project.
	Error error
	// AvailableProjects is populated only when Error==ErrAmbiguousProject.
	AvailableProjects []string
	// Org is the optional grouping label from .engram/config.json's "org" field.
	// Only populated when Source==SourceConfig; empty means unset (#776).
	Org string
}

// DetectProjectFull resolves the project for dir using a 6-case algorithm:
//
//  0. config     — nearest .engram/config.json inside the enclosing repo/root
//  1. git_remote — Git repo currently has origin: initialize an absent private binding from the remote name; otherwise reuse it
//  2. git_root   — Git repo currently has no origin: initialize an absent private binding from the root basename; otherwise reuse it
//  3. git_child  — cwd has exactly one git-repo child → resolve its canonical identity and auto-promote it
//  4. ambiguous  — cwd has multiple git-repo children → return ErrAmbiguousProject
//  5. dir_basename — none of the above → use filepath.Base(dir)
//
// DetectionOptions supplies local history only when establishing a Git binding.
// InspectOnly never initializes a binding; an unbound candidate is not authority.
type DetectionOptions struct {
	HistoryLookup func(string) ([]string, error)
	InspectOnly   bool
}

func DetectProjectFull(dir string) DetectionResult {
	return DetectProjectFullWithOptions(dir, DetectionOptions{})
}

func DetectProjectFullWithOptions(dir string, options DetectionOptions) DetectionResult {
	return detectProjectFull(dir, options, DirectoryIdentity(dir))
}

func detectProjectFull(dir string, options DetectionOptions, historyDirectory string) DetectionResult {
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

	// ── Cases 1 & 2: Git repository binding ─────────────────────────────
	if res, ok := detectFromGitBinding(dir, options, historyDirectory); ok {
		return res
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
		childResult := detectProjectFull(child, options, historyDirectory)
		if childResult.Error != nil {
			return childResult
		}
		return DetectionResult{
			Project: childResult.Project,
			Source:  SourceGitChild,
			Path:    childResult.Path,
			Warning: "auto-promoted child repository: " + childResult.Project,
		}
	default:
		if len(children) > 1 {
			// Case 4: multiple children → ambiguous.
			names := make([]string, len(children))
			for i, c := range children {
				names[i] = normalizeAvailableProject(filepath.Base(c))
			}
			slices.Sort(names)
			absDir, _ := filepath.Abs(dir)
			// REQ-304: Project is empty on ambiguous (spec is authoritative).
			// Consumers retain the error rather than inventing a basename identity.
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
	return DetectionResult{
		Project: fallbackProjectName(dir),
		Source:  SourceDirBasename,
		Path:    absDir,
	}
}

// detectFromGitBinding preserves the first legacy Git-derived project label in
// the repository's shared Git metadata. The binding is private to a clone and
// shared by linked worktrees; mutable remotes are only consulted at creation.
func detectFromGitBinding(dir string, options DetectionOptions, historyDirectory string) (DetectionResult, bool) {
	commonDir := detectGitCommonDir(dir)
	if commonDir == "" {
		return DetectionResult{}, false
	}
	root := detectGitRootDir(dir)
	if root == "" {
		return DetectionResult{
			Source: SourceGitRoot,
			Error:  fmt.Errorf("%w: cannot determine the repository root; configure project_name explicitly", ErrRepositoryBinding),
		}, true
	}

	legacyProject := normalize(filepath.Base(root))
	source := SourceGitRoot
	if name := detectFromGitRemote(dir); name != "" {
		legacyProject = normalize(name)
		source = SourceGitRemote
	}
	binding, err := readRepositoryBinding(commonDir)
	if errors.Is(err, os.ErrNotExist) {
		if options.HistoryLookup != nil {
			history, lookupErr := options.HistoryLookup(historyDirectory)
			if lookupErr != nil {
				return DetectionResult{Source: source, Path: root, Error: fmt.Errorf("%w: %v", ErrProjectHistoryUnavailable, lookupErr)}, true
			}
			for _, previous := range history {
				if previous != legacyProject {
					choices := append([]string{legacyProject}, history...)
					slices.Sort(choices)
					return DetectionResult{Source: source, Path: root, AvailableProjects: slices.Compact(choices), Error: &ProjectTransitionError{Candidate: legacyProject, HistoricalProjects: history}}, true
				}
			}
		}
		if options.InspectOnly {
			return DetectionResult{Project: legacyProject, Source: SourceUnboundGit, Path: root}, true
		}
		// The history lookup is a snapshot, not a transaction across stores and
		// Git metadata. Atomic publication still accepts an established winner.
		binding, err = loadOrCreateRepositoryBinding(commonDir, legacyProject)
	}
	if err != nil {
		return DetectionResult{Source: source, Path: root, Error: err}, true
	}
	return DetectionResult{Project: binding.Project, Source: source, Path: root}, true
}

type configFile struct {
	ProjectName string `json:"project_name"`
	// Org is a free-text grouping axis orthogonal to scope (#776). Unlike
	// ProjectName it has no canonicalization beyond trimming whitespace.
	Org string `json:"org"`
}

func detectFromConfig(dir string) (DetectionResult, bool) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}
	absDir = canonicalizePath(absDir)

	// Project config is a project/repo lock, not a global ancestor setting. When
	// cwd is inside git, walk upward only within the enclosing repository so a
	// nearest subproject .engram/config.json can override the repo root without
	// letting ~/.engram/config.json leak into nested workspaces under $HOME.
	if worktreeRoot := canonicalizePath(detectGitWorktreeDir(absDir)); worktreeRoot != "" {
		return readNearestConfigAtOrBelow(absDir, worktreeRoot)
	}

	// Outside git, accept only the current directory's config. Do not walk to
	// arbitrary parents such as $HOME.
	return readConfigAt(absDir)
}

func readNearestConfigAtOrBelow(startDir, stopDir string) (DetectionResult, bool) {
	current := filepath.Clean(startDir)
	stop := filepath.Clean(stopDir)

	for {
		if res, ok := readConfigAt(current); ok {
			return res, true
		}
		if current == stop {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return DetectionResult{}, false
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
	return DetectionResult{Project: projectName, Source: SourceConfig, Path: projectDir, Org: strings.TrimSpace(cfg.Org)}, true
}

func invalidConfigResult(path string, err error) DetectionResult {
	return DetectionResult{
		Project: "",
		Source:  SourceConfig,
		Path:    path,
		Error:   fmt.Errorf("%w: %v", ErrInvalidConfig, err),
	}
}

func canonicalizePath(path string) string {
	if path == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(resolved)
}

// RuntimeWorktreeDirectory returns the stable directory identity for runtime
// session binding. Unlike DetectProjectFull's Path, linked worktrees retain
// their individual checkout roots while their project identity may remain
// shared with the primary checkout.
func RuntimeWorktreeDirectory(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = "."
	}
	if absolute, err := filepath.Abs(dir); err == nil {
		dir = absolute
	}
	if worktreeRoot := detectGitWorktreeDir(dir); worktreeRoot != "" {
		return runtimeCanonicalizePath(worktreeRoot)
	}
	return runtimeCanonicalizePath(dir)
}

// DirectoryIdentity compares exact directories without promoting them to a
// repository root. Historical subdirectories and worktrees remain distinct.
func DirectoryIdentity(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return runtimeCanonicalizePath(path)
}

func runtimeCanonicalizePath(path string) string {
	path = canonicalizePath(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
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

// detectGitRootDir returns the canonical repository root for dir, or "" if it is
// not in a repository. Linked worktrees share this root with their primary checkout.
func detectGitRootDir(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	commonDir := gitRevParsePath(ctx, dir, "--git-common-dir")
	if commonDir == "" {
		return ""
	}

	worktreeDir := gitRevParsePath(ctx, dir, "--show-toplevel")
	if worktreeDir != "" {
		// Ordinary repositories and submodules have the same git and common
		// directories, so their checkout root is authoritative. A linked
		// worktree has a per-worktree git directory and must retain the shared
		// primary root derived from git-common-dir.
		if gitDir := gitRevParsePath(ctx, dir, "--git-dir"); gitDir == commonDir {
			return worktreeDir
		}
	}
	return repositoryRootFromCommonDir(commonDir)
}

func detectGitCommonDir(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return gitRevParsePath(ctx, dir, "--git-common-dir")
}

func gitRevParsePath(ctx context.Context, dir, argument string) string {
	cmd := command.NewContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", argument)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	path := filepath.Clean(strings.TrimSpace(string(out)))
	if path == "." || path == "" {
		return ""
	}
	return path
}

func repositoryRootFromCommonDir(commonDir string) string {
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir)
	}
	return strings.TrimSuffix(commonDir, ".git")
}

// detectGitWorktreeDir returns the current checkout root for dir. Config files
// are scoped to that checkout rather than the shared repository identity root.
func detectGitWorktreeDir(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := command.NewContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// scanChildren scans dir at depth=1 for git repositories, skipping noise dirs,
// hidden dirs, enforcing a 200ms timeout, and
// short-circuiting as soon as more than one repository is found.
// Returns the list of found git-repo paths and a boolean indicating timeout.
func scanChildren(dir string) (repos []string, timedOut bool) {
	deadline := childScanNow().Add(childScanTimeout)
	directory, err := os.Open(dir)
	if err != nil {
		return nil, false
	}
	defer directory.Close()
	if childScanNow().After(deadline) {
		return nil, true
	}

	for {
		// Check before each bounded read. os.ReadDir materializes the whole
		// directory before this loop can enforce the deadline.
		if childScanNow().After(deadline) {
			return repos, true
		}
		entries, readErr := childScanReadDir(directory, 1)
		if errors.Is(readErr, io.EOF) {
			return repos, false
		}
		if readErr != nil {
			return repos, false
		}
		if childScanNow().After(deadline) {
			return repos, true
		}
		if len(entries) == 0 {
			return repos, false
		}
		entry := entries[0]
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
		childPath := filepath.Join(dir, name)
		// Check if this child is a git repo (has a .git entry).
		gitPath := filepath.Join(childPath, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			if childScanNow().After(deadline) {
				return repos, true
			}
			repos = append(repos, childPath)
			// Short-circuit: as soon as we have > 1, no need to keep scanning.
			if len(repos) > 1 {
				return repos, false
			}
		}
	}
}

// normalize applies the shared canonical project name rules and maps empty or
// path-like values to unknown so basename detection always returns a valid name.
func normalize(name string) string {
	n := CanonicalizeProjectName(name)
	if n == "" || strings.ContainsAny(n, `/\\`) {
		return "unknown"
	}
	return n
}

// normalizeAvailableProject preserves the exact separator form of an ambiguous
// child choice so downstream resolution can distinguish collisions.
func normalizeAvailableProject(name string) string {
	n := strings.TrimSpace(strings.ToLower(name))
	if n == "" || strings.ContainsAny(n, `/\\`) {
		return "unknown"
	}
	return n
}

func fallbackProjectName(dir string) string {
	base := filepath.Base(dir)
	if base == "" || base == "." {
		return "unknown"
	}
	return normalize(base)
}

// detectFromGitRemote attempts to determine the project name from the git
// remote "origin" URL. Returns empty string if git is unavailable, the
// directory is not a repo, or there is no origin remote.
func detectFromGitRemote(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := command.NewContext(ctx, "git", "-C", dir, "remote", "get-url", "origin")
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
