package obsidian

import (
	"os"
	"path/filepath"
	"strings"
)

// mkdirAll creates the given directory path (all parents), used in tests.
func mkdirAll(path string) error {
	return os.MkdirAll(path, 0755)
}

// writeFile writes data to a file, creating parent dirs as needed.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// fileExists reports whether the given file path exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dirExists reports whether the given directory path exists.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// countFilesInDir returns the number of regular files in a directory (non-recursive).
func countFilesInDir(t interface {
	Helper()
	Fatalf(string, ...any)
}, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("countFilesInDir: %v", err)
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			count++
		}
	}
	return count
}

// walkDir walks all regular files under root and calls fn for each absolute path.
func walkDir(root string, fn func(path string)) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			fn(path)
		}
		return nil
	})
}

// isContainedIn reports whether path is inside (or equal to) root after cleaning.
func isContainedIn(path, root string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	return strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) || cleanPath == cleanRoot
}
