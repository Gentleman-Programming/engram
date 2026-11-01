//go:build windows

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
)

// stopDaemonProcess terminates the daemon on Windows. Graceful console
// events (CTRL_BREAK) cannot reach the detached child: spawnDaemon starts it
// with CREATE_NEW_PROCESS_GROUP and no console, so no console-event lever
// exists and TerminateProcess via Kill is the only way to stop it. The error
// wording stays aligned with the unix path (Kill there means SIGKILL).
func stopDaemonProcess(pid int) error {
	if pid == os.Getpid() {
		// A daemon record can never describe this CLI process itself: the
		// daemon is a detached child. Refuse before terminating so a corrupt
		// or test-written pidfile can never make engram kill itself; the
		// caller keeps the record and reports it as unmanaged.
		return fmt.Errorf("refusing to signal own process (pid %d)", pid)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find daemon process: %w", err)
	}
	if err := process.Kill(); err != nil {
		return fmt.Errorf("terminate daemon: %w", err)
	}
	if !pollDaemonExit(pid, stopKillGrace) {
		return fmt.Errorf("daemon (pid %d) did not exit after SIGKILL", pid)
	}
	return nil
}

// pollDaemonExit polls daemon.ProcessAlive until the pid is gone or the
// timeout expires, reporting whether the process exited.
func pollDaemonExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !daemon.ProcessAlive(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(stopPollInterval)
	}
}
