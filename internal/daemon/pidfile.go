// Package daemon owns the local serve daemon lifecycle primitives shared by
// the engram CLI verbs: pidfile custody, daemon health probing, and the
// daemon log sink. It is deliberately free of store and server dependencies
// so CLI commands can inspect daemon state without opening a database.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// PidfileName is the pidfile basename under the engram data dir (issue #2228).
const PidfileName = "serve.pid"

// ErrNoPidfile reports that no pidfile exists: no daemon start is recorded.
var ErrNoPidfile = errors.New("no pidfile")

// maxPid bounds a pidfile pid: Unix pid_t is a signed 32-bit integer, and a
// larger value would truncate in kernel calls (a pid of 2^32 would signal
// process group 0). The bound is applied on both write and read so a
// corrupt or hand-edited record can never smuggle such a pid through.
const maxPid = math.MaxInt32

// PidRecord is the pidfile payload: the daemon process id, the TCP port it
// serves, and the start token tying the record to one daemon start. Port is
// what `engram ps` and `engram stop` need when the daemon was started with a
// custom `-p` port; Token is what lets them confirm the pid still belongs to
// that daemon (the pidfile pid is never signaled without it, see the daemon
// verbs). Legacy records without a token stay readable.
type PidRecord struct {
	Pid   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token,omitempty"`
}

// PidfilePath returns the pidfile location for an engram data dir.
func PidfilePath(dataDir string) string {
	return filepath.Join(dataDir, PidfileName)
}

// WritePidfile atomically records the daemon record, replacing any stale
// pidfile left behind by a previous daemon. The write lands via a temporary
// file and rename so readers never observe a torn record.
func WritePidfile(path string, record PidRecord) error {
	if record.Pid <= 0 || record.Pid > maxPid {
		return fmt.Errorf("daemon: invalid pid %d", record.Pid)
	}
	if record.Port < 0 {
		return fmt.Errorf("daemon: invalid port %d", record.Port)
	}
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".serve-pid-")
	if err != nil {
		return fmt.Errorf("daemon: create temporary pidfile: %w", err)
	}
	temporary := file.Name()
	if err := json.NewEncoder(file).Encode(record); err != nil {
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

// ReadPidfile returns the recorded daemon record. A missing pidfile yields
// ErrNoPidfile; a malformed one yields a parse error.
func ReadPidfile(path string) (PidRecord, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return PidRecord{}, fmt.Errorf("%w: %s", ErrNoPidfile, path)
	}
	if err != nil {
		return PidRecord{}, fmt.Errorf("daemon: read pidfile: %w", err)
	}
	var record PidRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Pid <= 0 || record.Pid > maxPid || record.Port < 0 {
		return PidRecord{}, fmt.Errorf("daemon: parse pidfile %s: invalid record %q", path, strings.TrimSpace(string(data)))
	}
	return record, nil
}

// RemovePidfile deletes the pidfile, tolerating an already-missing file so
// cleanup after a dead daemon stays idempotent.
func RemovePidfile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("daemon: remove pidfile: %w", err)
	}
	return nil
}
