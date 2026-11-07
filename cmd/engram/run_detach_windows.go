//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// spawnDaemon starts the detached daemon child on Windows. The child should
// outlive the spawning session: sshd (and some supervisors) tie session
// processes to a job object that kills the whole tree when the session ends,
// so the child is created with CREATE_BREAKAWAY_FROM_JOB to leave that job,
// DETACHED_PROCESS to own no console, and its own process group so the
// parent's console events cannot reach it. Stdio lands on the daemon log
// sink either way. Not every spawning job allows breakaway (for example CI
// runner step jobs): when the OS denies the breakaway spawn, the child is
// retried without CREATE_BREAKAWAY_FROM_JOB so the daemon still starts
// detached, losing only survive-job-close in that environment.
func spawnDaemon(executable string, args, env []string, logFile *os.File) (int, error) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = devnull.Close() }()
	pid, err := startDetachedChild(executable, args, env, devnull, logFile, true)
	if err == nil {
		return pid, nil
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return 0, fmt.Errorf("start daemon: %w", err)
	}
	pid, err = startDetachedChild(executable, args, env, devnull, logFile, false)
	if err != nil {
		return 0, fmt.Errorf("start daemon: %w", err)
	}
	return pid, nil
}

// startDetachedChild starts one detached spawn attempt; breakaway selects
// whether the child leaves the spawning job object.
func startDetachedChild(executable string, args, env []string, stdin, stdout *os.File, breakaway bool) (int, error) {
	child := exec.Command(executable, args...)
	child.Env = env
	child.Stdin, child.Stdout, child.Stderr = stdin, stdout, stdout
	flags := uint32(syscall.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS)
	if breakaway {
		flags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
	if err := child.Start(); err != nil {
		return 0, err
	}
	return child.Process.Pid, nil
}

// childExited always reports false on Windows: without the process handle the
// exit state cannot be probed by pid, so the readiness loop relies on the
// bounded timeout instead.
func childExited(pid int) bool {
	return false
}
