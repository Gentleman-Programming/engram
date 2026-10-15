package project

import "path/filepath"

// ObserveGitRoot observes only the current repository, without initializing a
// binding or treating configuration and child repositories as routing evidence.
func ObserveGitRoot(directory string) string {
	return detectGitRootDir(directory)
}

type RoutingCandidateInput struct {
	Designation string
	GitRoot     string
	Directory   string
}

type RoutingCandidate struct {
	Key        string `json:"key"`
	Source     string `json:"source"`
	Directory  string `json:"directory"`
	RawProject string `json:"raw_project"`
}

// DeriveRoutingCandidate trusts only designation or an observed Git root, never
// the capture directory or a historical project's ownership label.
func DeriveRoutingCandidate(input RoutingCandidateInput) (RoutingCandidate, error) {
	candidate := RoutingCandidate{Source: "unknown", Directory: input.Directory, RawProject: input.Designation}
	if input.Designation != "" {
		result, err := Resolve(ResolutionOptions{Mode: ResolutionExplicit, Explicit: input.Designation})
		if err != nil {
			return candidate, err
		}
		candidate.Key = result.Project
		candidate.Source = SourceExplicitOverride
		return candidate, nil
	}
	if input.GitRoot != "" {
		result, err := Resolve(ResolutionOptions{Mode: ResolutionExplicit, Explicit: filepath.Base(filepath.Clean(input.GitRoot))})
		if err != nil {
			return candidate, err
		}
		candidate.Key = result.Project
		candidate.Source = SourceGitRoot
	}
	return candidate, nil
}
