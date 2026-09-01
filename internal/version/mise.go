package version

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var currentExecutableFn = os.Executable
var userHomeDirFn = os.UserHomeDir

// miseInstallsRoot follows mise's own precedence. Whitespace-only values are
// unset. goos is a parameter so the Windows branch is testable on Linux CI.
func miseInstallsRoot(goos string) string {
	if root := strings.TrimSpace(os.Getenv("MISE_INSTALLS_DIR")); root != "" {
		return root
	}
	if dataDir := strings.TrimSpace(os.Getenv("MISE_DATA_DIR")); dataDir != "" {
		return filepath.Join(dataDir, "installs")
	}
	if xdgDataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdgDataHome != "" {
		return filepath.Join(xdgDataHome, "mise", "installs")
	}
	if goos == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			return filepath.Join(localAppData, "mise", "installs")
		}
	}
	home, err := userHomeDirFn()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	if goos == "windows" {
		return filepath.Join(home, "AppData", "Local", "mise", "installs")
	}
	return filepath.Join(home, ".local", "share", "mise", "installs")
}

// pathContains reports whether path is root itself or lies beneath it. It climbs
// path's lexical ancestors and asks the OS for directory identity via os.SameFile
// (device+inode) rather than comparing strings, so symlinked ancestors,
// case-insensitive filesystems, and Unicode-equivalent names all answer correctly.
func pathContains(root, path string) bool {
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return false
	}
	current, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for {
		if info, statErr := os.Stat(current); statErr == nil && info.IsDir() && os.SameFile(rootInfo, info) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

func runningBinaryIsMiseManaged() bool {
	root := miseInstallsRoot(runtime.GOOS)
	if root == "" {
		return false
	}
	exe, err := currentExecutableFn()
	if err != nil {
		return false
	}
	return pathContains(root, exe)
}
