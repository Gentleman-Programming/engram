package store

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// EnvAllowUnsafeNFS is the environment variable that allows bypassing the
// network filesystem startup check.
const EnvAllowUnsafeNFS = "ENGRAM_ALLOW_UNSAFE_NFS"

func (e *NetworkFilesystemError) Error() string {
	return fmt.Sprintf("engram: data directory %q is on %s; persistent SQLite WAL is unsafe on network filesystems — set ENGRAM_DATA_DIR to a local directory or set %s=1 to bypass (unsupported, risks database corruption)", e.DataDir, e.Filesystem, EnvAllowUnsafeNFS)
}

// filesystemInspector is an OS-specific read-only adapter. Tests replace it
// to prove rejection happens before any data-directory or SQLite mutation.
var filesystemInspector = detectFilesystem

func allowUnsafeNFS() bool {
	v := strings.TrimSpace(os.Getenv(EnvAllowUnsafeNFS))
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return strings.EqualFold(v, "yes")
}

// checkDataDirectoryFilesystem rejects only filesystem types the platform
// positively identifies as remote. Detection failures and unknown types remain
// compatible rather than claiming they are local.
// When ENGRAM_ALLOW_UNSAFE_NFS is enabled, the check logs a warning to stderr
// and permits startup to continue.
func checkDataDirectoryFilesystem(dataDir string) error {
	probePath, err := existingFilesystemPath(dataDir)
	if err != nil {
		return nil
	}
	info, err := filesystemInspector(probePath)
	if err != nil || info.Support != filesystemRemote {
		return nil
	}
	if allowUnsafeNFS() {
		log.Printf("[store] WARNING: %s=1 is set; bypassing filesystem check for %q (%s). Running SQLite over network filesystems risks database corruption and lock errors.", EnvAllowUnsafeNFS, dataDir, info.Type)
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
