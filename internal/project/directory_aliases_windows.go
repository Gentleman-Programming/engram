package project

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/windows"
)

// DirectoryHistoryAliases queries only the canonical selected local directory.
// Stored history must remain lexical evidence, never an input to these queries.
func DirectoryHistoryAliases(directory string) ([]string, error) {
	return directoryHistoryAliases(directory, windows.GetLongPathName, windows.GetShortPathName)
}

type directoryPathQuery func(*uint16, *uint16, uint32) (uint32, error)

func directoryHistoryAliases(directory string, longQuery, shortQuery directoryPathQuery) ([]string, error) {
	// Do not expand the contract for remote/UNC or nonabsolute selections.
	if !filepath.IsAbs(directory) || strings.HasPrefix(directory, `\\`) || strings.HasPrefix(directory, "//") {
		return []string{directory}, nil
	}
	canonical := DirectoryIdentity(directory)
	if strings.HasPrefix(canonical, `\\`) {
		return []string{canonical}, nil
	}
	aliases := []string{canonical}
	for _, query := range []directoryPathQuery{longQuery, shortQuery} {
		input, err := windows.UTF16PtrFromString(canonical)
		if err != nil {
			return nil, fmt.Errorf("selected directory history aliases: %w", err)
		}
		buffer := make([]uint16, 32768)
		n, err := query(input, &buffer[0], uint32(len(buffer)))
		if err != nil {
			return nil, fmt.Errorf("selected directory history aliases: %w", err)
		}
		if n == 0 || n >= uint32(len(buffer)) {
			return nil, fmt.Errorf("selected directory history aliases: invalid path length %d", n)
		}
		aliases = append(aliases, strings.ToLower(filepath.Clean(windows.UTF16ToString(buffer[:n]))))
	}
	slices.Sort(aliases)
	return slices.Compact(aliases), nil
}
