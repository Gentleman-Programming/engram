//go:build !windows

package project

import "slices"

// DirectoryHistoryAliases returns selected-directory spellings for lexical history matching.
// On non-Windows platforms the existing exact matching contract is unchanged.
func DirectoryHistoryAliases(directory string) ([]string, error) {
	return []string{directory}, nil
}

// DirectoryHistoryMatches preserves exact lexical history matching off Windows.
func DirectoryHistoryMatches(aliases []string, historical string) bool {
	return slices.Contains(aliases, historical)
}
