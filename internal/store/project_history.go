package store

import (
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
	rows, err := s.queryItHook(s.db, `SELECT DISTINCT project, directory FROM sessions WHERE project IS NOT NULL AND project != '' AND directory != ''`)
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
	return project.DetectProjectFullWithOptions(directory, project.DetectionOptions{HistoryLookup: s.ProjectHistory})
}

// InspectProject cannot create a binding or elevate an unbound Git candidate.
func (s *Store) InspectProject(directory string) project.DetectionResult {
	return project.DetectProjectFullWithOptions(directory, project.DetectionOptions{InspectOnly: true, HistoryLookup: s.ProjectHistory})
}
