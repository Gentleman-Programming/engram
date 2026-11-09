package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// filesystemSupport describes what Engram can establish about a filesystem.
// Unknown is deliberately not treated as local: it preserves compatibility
// when an operating system cannot classify a filesystem type.
type filesystemSupport string

const (
	filesystemLocal   filesystemSupport = "local"
	filesystemRemote  filesystemSupport = "remote"
	filesystemUnknown filesystemSupport = "unknown"
)

type filesystemInfo struct {
	Type    string
	Support filesystemSupport
}

// NetworkFilesystemError rejects persistent SQLite WAL storage on a known
// remote filesystem. Callers must propagate it unchanged so CLI, server, and
// MCP users receive the same actionable diagnosis.
type NetworkFilesystemError struct {
	DataDir    string
	Filesystem string
}

func (e *NetworkFilesystemError) Error() string {
	return fmt.Sprintf("engram: data directory %q is on %s; persistent SQLite WAL is unsafe on network filesystems — set ENGRAM_DATA_DIR to a local directory", e.DataDir, e.Filesystem)
}

// filesystemInspector is an OS-specific read-only adapter. Tests replace it
// to prove rejection happens before any data-directory or SQLite mutation.
var filesystemInspector = detectFilesystem

// checkDataDirectoryFilesystem rejects only filesystem types the platform
// positively identifies as remote. Detection failures and unknown types remain
// compatible rather than claiming they are local.
func checkDataDirectoryFilesystem(dataDir string) error {
	probePath, err := existingFilesystemPath(dataDir)
	if err != nil {
		return nil
	}
	info, err := filesystemInspector(probePath)
	if err != nil || info.Support != filesystemRemote {
		return nil
	}
	return &NetworkFilesystemError{DataDir: dataDir, Filesystem: info.Type}
}

// existingFilesystemPath finds an existing ancestor without creating the data
// directory. A missing data directory inherits its parent filesystem policy.
func existingFilesystemPath(dataDir string) (string, error) {
	path := dataDir
	for {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", os.ErrNotExist
		}
		path = parent
	}
}
