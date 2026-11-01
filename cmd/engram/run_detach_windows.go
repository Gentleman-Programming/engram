//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// spawnDaemon starts the detached daemon child on Windows. The child must
// outlive the spawning session: sshd (and some supervisors) tie session
// processes to a job object that kills the whole tree when the session ends,
// so the child is created with CREATE_BREAKAWAY_FROM_JOB to leave that job,
// DETACHED_PROCESS to own no console, and its own process group so the
// parent's console events cannot reach it. Stdio lands on the daemon log
// sink either way.
func spawnDaemon(executable string, args, env []string, logFile *os.File) (int, error) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = devnull.Close() }()
	child := exec.Command(executable, args...)
	child.Env = env
	child.Stdin, child.Stdout, child.Stderr = devnull, logFile, logFile
	child.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | windows.CREATE_BREAKAWAY_FROM_JOB,
	}
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
