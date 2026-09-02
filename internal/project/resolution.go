package project

import (
	"errors"
	"fmt"
	"strings"
)

// ResolutionMode makes the omission contract explicit at every entry point.
// Current resolves a single current project, Explicit requires a supplied
// project name, and All deliberately leaves the project filter unset.
type ResolutionMode string

const (
	ResolutionCurrent  ResolutionMode = "current"
	ResolutionExplicit ResolutionMode = "explicit"
	ResolutionAll      ResolutionMode = "all"
)

var ErrInvalidProjectName = errors.New("invalid project name")

// UnknownProjectError reports a structurally valid name that is not known by
// the caller's persistence boundary.
type UnknownProjectError struct {
	Name string
}

func (e *UnknownProjectError) Error() string { return "unknown project: " + e.Name }

// ResolutionOptions provides the small policy seam shared by CLI, HTTP, and
// MCP. Detection stays in this package; callers supply persistence existence
// checks only for contracts where an override must not create a new bucket.
type ResolutionOptions struct {
	Mode            ResolutionMode
	Explicit        string
	ProcessOverride string
	Directory       string
	Detect          func(string) DetectionResult
	ProjectExists   func(string) (bool, error)

	RequireKnownExplicit bool
	RequireKnownProcess  bool
}

// Resolve applies the canonical project-resolution contract. Explicit request
// values win over process overrides; process overrides win over cwd detection.
// ResolutionAll is intentionally global and ignores every project input.
func Resolve(options ResolutionOptions) (DetectionResult, error) {
	if options.Mode == ResolutionAll {
		return DetectionResult{Source: SourceAllProjects}, nil
	}

	explicit := strings.TrimSpace(options.Explicit)
	if options.Mode == ResolutionExplicit && explicit == "" {
		return DetectionResult{}, invalidProjectName(options.Explicit, "project is required")
	}
	if explicit != "" {
		project, err := normalizeProjectName(explicit)
		if err != nil {
			return DetectionResult{}, err
		}
		if err := requireKnown(project, options.RequireKnownExplicit, options.ProjectExists); err != nil {
			return DetectionResult{}, err
		}
		return DetectionResult{Project: project, Source: SourceExplicitOverride}, nil
	}

	if override, ok := ProcessOverride(options.ProcessOverride); ok {
		project, err := normalizeProjectName(override)
		if err != nil {
			return DetectionResult{}, err
		}
		if err := requireKnown(project, options.RequireKnownProcess, options.ProjectExists); err != nil {
			return DetectionResult{}, err
		}
		return DetectionResult{Project: project, Source: SourceProcessOverride}, nil
	}

	detect := options.Detect
	if detect == nil {
		detect = DetectProjectFull
	}
	result := detect(options.Directory)
	if result.Error != nil {
		return result, result.Error
	}
	return result, nil
}

func requireKnown(project string, required bool, exists func(string) (bool, error)) error {
	if !required {
		return nil
	}
	if exists == nil {
		return fmt.Errorf("project existence check is required for %q", project)
	}
	known, err := exists(project)
	if err != nil {
		return err
	}
	if !known {
		return &UnknownProjectError{Name: project}
	}
	return nil
}

func normalizeProjectName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", invalidProjectName(name, "project is required")
	}
	if strings.ContainsAny(trimmed, `/\\`) {
		return "", invalidProjectName(name, "project must be a name, not a path")
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return "", invalidProjectName(name, "project contains control characters")
		}
	}
	return CanonicalizeProjectName(trimmed), nil
}

// CanonicalizeProjectName returns the canonical storage form of a project name.
// Callers that accept external input must validate it before canonicalizing.
func CanonicalizeProjectName(name string) string {
	canonical := strings.TrimSpace(strings.ToLower(name))
	for strings.Contains(canonical, "--") {
		canonical = strings.ReplaceAll(canonical, "--", "-")
	}
	for strings.Contains(canonical, "__") {
		canonical = strings.ReplaceAll(canonical, "__", "_")
	}
	return canonical
}

func invalidProjectName(name, reason string) error {
	return fmt.Errorf("%w: %s (%s)", ErrInvalidProjectName, name, reason)
}
