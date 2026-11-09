package project

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// InspectLocalProject resolves client-side identity without opening persistence,
// consulting history or establishing a repository binding. The caller retains
// authority over its total deadline and subsequent session registration.
func InspectLocalProject(ctx context.Context, directory string) (DetectionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return DetectionResult{}, err
	}
	if strings.TrimSpace(directory) == "" || strings.ContainsRune(directory, 0) {
		return DetectionResult{}, fmt.Errorf("inspection requires a usable cwd")
	}
	// A basename from a missing or unreadable directory is not client evidence.
	// Read one entry to check directory access, without materializing its contents.
	dir, err := os.Open(directory)
	if err != nil {
		return DetectionResult{}, err
	}
	info, statErr := dir.Stat()
	if statErr != nil || !info.IsDir() {
		_ = dir.Close()
		return DetectionResult{}, fmt.Errorf("inspection requires a readable directory")
	}
	_, readErr := dir.Readdirnames(1)
	closeErr := dir.Close()
	if readErr != nil && readErr != io.EOF {
		return DetectionResult{}, readErr
	}
	if closeErr != nil {
		return DetectionResult{}, closeErr
	}
	options := DetectionOptions{Context: ctx, InspectOnly: true, RequireExplicitHomeProject: true}
	result := DetectProjectFullWithOptions(directory, options)
	if result.Error != nil {
		return DetectionResult{}, result.Error
	}
	if result.Source == SourceGitChild {
		// Auto-promotion changes the source label. Inspect the selected identity
		// again so an unbound child cannot masquerade as established authority.
		child := DetectProjectFullWithOptions(result.Path, options)
		if child.Error != nil {
			return DetectionResult{}, child.Error
		}
		if child.Source == SourceUnboundGit || child.Source == SourceDirBasename || child.Project != result.Project {
			return DetectionResult{}, ErrRepositoryBinding
		}
	}
	switch result.Source {
	case SourceDirBasename:
		if err := validateBasenameInspection(ctx, directory); err != nil {
			return DetectionResult{}, err
		}
	case SourceConfig, SourceGitRemote, SourceGitRoot, SourceGitChild:
	default:
		return DetectionResult{}, ErrRepositoryBinding
	}
	project, err := normalizeProjectName(result.Project)
	if err != nil {
		return DetectionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return DetectionResult{}, err
	}
	result.Project = project
	return result, nil
}

// A failed Git lookup does not prove that a directory is non-Git. Reject
// fallback across visible metadata, including broken gitfiles and ancestors;
// this validation never selects an identity or changes ordinary detection.
func validateBasenameInspection(ctx context.Context, directory string) error {
	current, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	current = canonicalizePath(current)
	start := DirectoryIdentity(current)
	ceilings := make(map[string]bool)
	for _, ceiling := range filepath.SplitList(os.Getenv("GIT_CEILING_DIRECTORIES")) {
		if filepath.IsAbs(ceiling) {
			ceilings[DirectoryIdentity(ceiling)] = true
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Like Git, a ceiling bounds ancestor discovery, not cwd itself.
		identity := DirectoryIdentity(current)
		if identity != start && ceilings[identity] {
			return nil
		}
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return ErrRepositoryBinding
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: cannot verify Git metadata: %v", ErrRepositoryBinding, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}
