// Package hostbind links a host agent process (for example the Claude Code
// process) to the runtime session it currently serves.
//
// The host's hooks and its stdio MCP servers are all direct children of the
// same host process, but a hook can be cancelled by the host and an MCP server
// is not restarted when the session changes (/clear, resume, fork). The hooks
// therefore publish "host process -> session" here, and the MCP server reads it
// back through its own parent process whenever a write omits session_id.
//
// A binding never creates or chooses an identity on its own: it records the
// session ID the host itself reported, and it is honored only while the host
// process that published it is still the same process (PID plus start time).
// Any missing, unreadable, or stale binding means "no binding", and callers
// keep their existing behavior.
package hostbind

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const dirName = "host-sessions"

// processStart returns an opaque token that changes whenever a PID is reused.
// It is a variable so tests can simulate process identity.
var processStart = ProcessStart

// Binding is the persisted host-process to session link.
type Binding struct {
	PID       int    `json:"pid"`
	Start     string `json:"start"`
	SessionID string `json:"session_id"`
	UpdatedAt string `json:"updated_at"`
}

func path(dataDir string, pid int) string {
	return filepath.Join(dataDir, dirName, strconv.Itoa(pid)+".json")
}

// Publish records that the host process pid currently serves sessionID. The
// write is atomic, so a concurrent Lookup sees the old or the new binding and
// never a partial file.
func Publish(dataDir string, pid int, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if dataDir == "" || pid <= 1 || sessionID == "" {
		return errors.New("hostbind: data directory, host pid, and session id are required")
	}
	start, err := processStart(pid)
	if err != nil {
		return err
	}
	data, err := json.Marshal(Binding{PID: pid, Start: start, SessionID: sessionID, UpdatedAt: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	dir := filepath.Join(dataDir, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bind-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path(dataDir, pid)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	prune(dir, pid)
	return nil
}

// Lookup returns the session ID bound to the host process pid. ok is false when
// there is no binding, it cannot be read, or the process that published it is
// gone (the PID now names a different process).
func Lookup(dataDir string, pid int) (sessionID string, ok bool) {
	if dataDir == "" || pid <= 1 {
		return "", false
	}
	data, err := os.ReadFile(path(dataDir, pid))
	if err != nil {
		return "", false
	}
	var b Binding
	if json.Unmarshal(data, &b) != nil || b.PID != pid || strings.TrimSpace(b.SessionID) == "" || b.Start == "" {
		return "", false
	}
	current, err := processStart(pid)
	if err != nil || current != b.Start {
		return "", false
	}
	return b.SessionID, true
}

// prune removes bindings whose host process has exited. It is best effort and
// never touches the binding that was just written.
func prune(dir string, keepPID int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if err != nil || pid == keepPID {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var b Binding
		if json.Unmarshal(data, &b) != nil {
			continue
		}
		if current, err := processStart(pid); err == nil && current == b.Start {
			continue
		} else if err != nil && !errors.Is(err, ErrNoProcess) {
			// Identity could not be determined (for example unsupported
			// platform): keep the file rather than guess it is stale.
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}
