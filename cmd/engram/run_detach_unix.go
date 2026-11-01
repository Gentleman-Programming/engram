//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// spawnDaemon starts the detached daemon child: its own session (setsid), no
// controlling terminal, stdio on the daemon log sink. The child is fully
// released; the daemon outlives its parent.
func spawnDaemon(executable string, args, env []string, logFile *os.File) (int, error) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = devnull.Close() }()
	child := exec.Command(executable, args...)
	child.Env = env
	child.Stdin, child.Stdout, child.Stderr = devnull, logFile, logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return 0, fmt.Errorf("start daemon: %w", err)
	}
	return child.Process.Pid, nil
}

// childExited reports whether a spawned child has already exited, reaping it
// non-blockingly. Signal 0 alone cannot tell: an exited-but-unreaped child
// stays a zombie and still answers signal 0, which would stall the readiness
// loop on the full timeout after a fast bind failure. ECHILD (already reaped
// or not our child) also counts as exited. Only valid for direct children.
func childExited(pid int) bool {
	var status syscall.WaitStatus
	pidResult, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	if err == syscall.ECHILD {
		return true
	}
	return err == nil && pidResult == pid
}
