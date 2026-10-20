package store

import (
	"fmt"
	"path/filepath"
)

// ValidateDataDirectory checks store path admission without creating files or
// changing process configuration. Callers must still handle creation errors.
func ValidateDataDirectory(dataDir string) error {
	if !filepath.IsAbs(dataDir) {
		return fmt.Errorf("engram: data directory must be an absolute path, got %q — set ENGRAM_DATA_DIR or ensure your home directory is resolvable", dataDir)
	}
	return checkDataDirectoryFilesystem(dataDir)
}
