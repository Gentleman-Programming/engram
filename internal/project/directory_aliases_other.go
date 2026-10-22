//go:build !windows

package project

// DirectoryHistoryAliases returns selected-directory spellings for lexical history matching.
// On non-Windows platforms the existing exact matching contract is unchanged.
func DirectoryHistoryAliases(directory string) ([]string, error) {
	return []string{directory}, nil
}
