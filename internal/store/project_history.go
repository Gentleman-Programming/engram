package store

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/project"
)

// ProjectHistory returns weak scope evidence for an exact directory. Session
// history can be explicit or imported; it does not establish project ownership.
// This query is needed only before first binding creation, not on each read.
func (s *Store) ProjectHistory(directory string) ([]string, error) {
	return s.projectHistory(context.Background(), directory)
}

func (s *Store) projectHistory(ctx context.Context, directory string) ([]string, error) {
	rows, err := s.queryContextSection(ctx, s.db, `SELECT DISTINCT project, directory FROM sessions WHERE project IS NOT NULL AND project != '' AND directory != ''`)
	if err != nil {
		return nil, fmt.Errorf("project directory history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var projects []string
	for rows.Next() {
		var name, storedDir string
		if err := rows.Scan(&name, &storedDir); err != nil {
			return nil, err
		}
		// Imported and legacy rows may contain noncanonical or invalid labels.
		// Only a valid canonical name can be weak project evidence.
		resolved, err := project.Resolve(project.ResolutionOptions{Mode: project.ResolutionExplicit, Explicit: name})
		historicalDir := historicalDirectoryIdentity(storedDir)
		if err == nil && historicalDir != "" && historicalDir == directory {
			projects = append(projects, resolved.Project)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Sort(projects)
	return slices.Compact(projects), nil
}

// Historical paths are evidence, not locations to visit. Imported relative
// paths have no recorded base, and unrelated absolute paths may be offline.
// Canonicalization belongs only to the caller's selected working directory;
// legacy symlink aliases cannot be reconstructed safely from this history.
func historicalDirectoryIdentity(directory string) string {
	if !filepath.IsAbs(directory) {
		return ""
	}
	directory = filepath.Clean(directory)
	if runtime.GOOS == "windows" {
		directory = strings.ToLower(directory)
	}
	return directory
}

// DetectProject applies the first-Git-binding precaution using this local store.
func (s *Store) DetectProject(directory string) project.DetectionResult {
	return s.DetectProjectContext(context.Background(), directory)
}

// DetectProjectContext associates request cancellation with project detection and history reads.
func (s *Store) DetectProjectContext(ctx context.Context, directory string) project.DetectionResult {
	return s.DetectProjectWithOptions(directory, project.DetectionOptions{Context: ctx})
}

// InspectProject cannot create a binding or elevate an unbound Git candidate.
func (s *Store) InspectProject(directory string) project.DetectionResult {
	return s.DetectProjectWithOptions(directory, project.DetectionOptions{InspectOnly: true})
}

// DetectProjectWithOptions resolves a local project using this store's binding history.
func (s *Store) DetectProjectWithOptions(directory string, options project.DetectionOptions) project.DetectionResult {
	options.HistoryLookup = s.ProjectHistory
	if ctx := options.Context; ctx != nil {
		options.HistoryLookup = func(path string) ([]string, error) {
			return s.projectHistory(ctx, path)
		}
	}
	return project.DetectProjectFullWithOptions(directory, options)
}
