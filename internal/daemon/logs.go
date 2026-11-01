package daemon

import (
	"fmt"
	"os"
	"path/filepath"
)

// LogName is the daemon log sink basename under the engram data dir.
const LogName = "serve.log"

// LogPath returns the daemon log sink location for an engram data dir.
func LogPath(dataDir string) string {
	return filepath.Join(dataDir, LogName)
}

// OpenLog opens the daemon log sink for appending, creating it on first use.
// The detached daemon redirects its stdout and stderr here so `engram logs`
// can replay and follow daemon output.
func OpenLog(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: open daemon log: %w", err)
	}
	return file, nil
}
