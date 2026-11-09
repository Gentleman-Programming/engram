package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Workspace context belongs to one call. It never changes process cwd or the
// authority of another client's discovery/write. Preserve nonblank path bytes.
func promptWorkspaceDirectory(args map[string]any) (string, error) {
	for _, key := range []string{"directory", "cwd"} {
		raw, provided := args[key]
		if !provided {
			continue
		}
		directory, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("%s must be a string", key)
		}
		if strings.TrimSpace(directory) == "" {
			continue
		}
		if !filepath.IsAbs(directory) {
			return "", fmt.Errorf("%s must be an absolute directory", key)
		}
		info, err := os.Stat(directory)
		if err != nil {
			return "", fmt.Errorf("%s is unavailable: %w", key, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s must name a directory", key)
		}
		return directory, nil
	}
	return "", nil
}

func resolvePromptWorkspaceProject(s *store.Store, directory, choice, reason string, validate ambiguousRecoveryTokenValidator, defaultProject string) (projectpkg.DetectionResult, error) {
	if directory == "" {
		return resolveWriteProjectWithChoiceAndProcessOverride(s, choice, reason, validate, defaultProject)
	}
	if result, ok, err := processProjectResult(defaultProject); ok {
		return result, err
	}
	return resolveWriteProjectWithChoiceAtDirectory(s, choice, reason, validate, directory)
}
