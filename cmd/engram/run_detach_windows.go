//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// spawnDaemon starts the detached daemon child on Windows: hidden window, its
// own process group so the parent's console events cannot reach it, stdio on
// the daemon log sink. The child outlives the parent.
func spawnDaemon(executable string, args, env []string, logFile *os.File) (int, error) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = devnull.Close() }()
	child := exec.Command(executable, args...)
	child.Env = env
	child.Stdin, child.Stdout, child.Stderr = devnull, logFile, logFile
	child.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	if err := child.Start(); err != nil {
		return 0, fmt.Errorf("start daemon: %w", err)
	}
	return child.Process.Pid, nil
}

// childExited always reports false on Windows: without the process handle the
// exit state cannot be probed by pid, so the readiness loop relies on the
// bounded timeout instead.
func childExited(pid int) bool {
	return false
}
