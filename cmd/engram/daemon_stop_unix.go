//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
)

// stopDaemonProcess stops the daemon gracefully then forcibly (docker `stop`
// semantics, spec S5): SIGTERM with a grace period, then SIGKILL with a
// shorter one. Only the pidfile pid is ever signaled.
func stopDaemonProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find daemon process: %w", err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("SIGTERM daemon: %w", err)
	}
	if awaitDaemonExit(pid, stopTermGrace) {
		return nil
	}
	if err := process.Kill(); err != nil {
		return fmt.Errorf("SIGKILL daemon: %w", err)
	}
	if awaitDaemonExit(pid, stopKillGrace) {
		return nil
	}
	return fmt.Errorf("daemon (pid %d) did not exit after SIGKILL", pid)
}

// awaitDaemonExit polls until the daemon pid is gone or the timeout expires,
// reporting whether the process exited. ProcessAlive alone cannot see an
// exited direct child: until reaped it stays a zombie and still answers
// signal 0 (see childExited in run_detach_unix.go), so wait4(WNOHANG) both
// detects and reaps our own children. ECHILD (not our child — the production
// case, since `engram stop` runs in a different process than `engram run`)
// falls through to plain ProcessAlive polling.
func awaitDaemonExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !daemon.ProcessAlive(pid) {
			return true
		}
		var status syscall.WaitStatus
		if reaped, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); err == nil && reaped == pid {
			return true
		}
		if !time.Now().Before(deadline) {
			return !daemon.ProcessAlive(pid)
		}
		time.Sleep(stopPollInterval)
	}
}
