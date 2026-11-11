package main

import (
	"fmt"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
)

// listenerConflict observes the selected port without adopting or signaling its
// owner. Health metadata and an OS-reported owner PID are diagnostic snapshots;
// only the existing pidfile/start-token checks authorize stopping a daemon.
func listenerConflict(port int) error {
	return listenerConflictForProbe(port, daemonStatusProbe(port))
}

func listenerConflictForProbe(port int, probe daemon.ProbeResult) error {
	if probe.Status == daemon.StatusNotRunning {
		return nil
	}
	kind := "unknown listener"
	if probe.Status == daemon.StatusRunning && probe.Service == "engram" {
		kind = "unmanaged Engram listener"
	}
	owner := "owning PID unavailable"
	if pid := daemon.ListenerOwnerPID(port); pid > 0 {
		owner = fmt.Sprintf("observed owning pid %d", pid)
	}
	return fmt.Errorf("%s on port %d (%s); not managed by this pidfile; not signaling or adopting it; verify the owner locally and stop it manually if appropriate", kind, port, owner)
}

func daemonStatusPort(record daemon.PidRecord) int {
	port, _ := resolveRestartPort(nil, record)
	return port
}

func listenerObservation(port int, probe daemon.ProbeResult) string {
	if err := listenerConflictForProbe(port, probe); err != nil {
		return err.Error()
	}
	return fmt.Sprintf("no listener answered on port %d", port)
}
