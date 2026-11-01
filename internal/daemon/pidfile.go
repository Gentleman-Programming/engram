// Package daemon owns the local serve daemon lifecycle primitives shared by
// the engram CLI verbs: pidfile custody, daemon health probing, and the
// daemon log sink. It is deliberately free of store and server dependencies
// so CLI commands can inspect daemon state without opening a database.
package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PidfileName is the pidfile basename under the engram data dir (issue #2228).
const PidfileName = "serve.pid"

// ErrNoPidfile reports that no pidfile exists: no daemon start is recorded.
var ErrNoPidfile = errors.New("no pidfile")

// PidfilePath returns the pidfile location for an engram data dir.
func PidfilePath(dataDir string) string {
	return filepath.Join(dataDir, PidfileName)
}

// WritePidfile atomically records the daemon pid, replacing any stale pidfile
// left behind by a previous daemon. The write lands via a temporary file and
// rename so readers never observe a torn pid.
func WritePidfile(path string, pid int) error {
	if pid <= 0 {
		return fmt.Errorf("daemon: invalid pid %d", pid)
	}
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".serve-pid-")
	if err != nil {
		return fmt.Errorf("daemon: create temporary pidfile: %w", err)
	}
	temporary := file.Name()
	if _, err := file.WriteString(strconv.Itoa(pid) + "\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return fmt.Errorf("daemon: write pidfile: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return fmt.Errorf("daemon: sync pidfile: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("daemon: close pidfile: %w", err)
	}
	// Plain rename atomically replaces any stale pidfile left by a previous
	// daemon; the no-replace semantics of identityfile.Publish would refuse it.
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("daemon: publish pidfile: %w", err)
	}
	return nil
}

// ReadPidfile returns the recorded daemon pid. A missing pidfile yields
// ErrNoPidfile; a malformed one yields a parse error.
func ReadPidfile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, fmt.Errorf("%w: %s", ErrNoPidfile, path)
	}
	if err != nil {
		return 0, fmt.Errorf("daemon: read pidfile: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("daemon: parse pidfile %s: invalid pid %q", path, strings.TrimSpace(string(data)))
	}
	return pid, nil
}

// RemovePidfile deletes the pidfile, tolerating an already-missing file so
// cleanup after a dead daemon stays idempotent.
func RemovePidfile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("daemon: remove pidfile: %w", err)
	}
	return nil
}
