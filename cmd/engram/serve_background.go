package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// launchServeBackground starts a server without inheriting the caller's pipe handles.
// In particular, Git Bash redirection alone cannot detach Windows named pipes.
func launchServeBackground(executable, logPath string) error {
	return launchServeBackgroundContext(context.Background(), executable, logPath, nil)
}

// Native file opens and process creation are synchronous, not forcibly
// cancellable. Check admission after preparation and never spawn a goroutine
// that could create a child after its caller returns.
func launchServeBackgroundContext(ctx context.Context, executable, logPath string, env []string) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return fmt.Errorf("background serve is supported only on Windows")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err := logFile.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close serve log: %w", err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() {
		if err := null.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close null device: %w", err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	child := exec.Command(executable, "serve")
	child.Env = env
	child.Stdin, child.Stdout, child.Stderr = null, null, logFile
	if err := child.Start(); err != nil {
		return fmt.Errorf("start background serve: %w", err)
	}
	// This is shared serve: another hook may already have adopted it. Once
	// started, cancellation must not kill it merely because this caller has
	// not observed readiness. Release only our process handle, never a PID
	// discovered by name or port.
	if err := child.Process.Release(); err != nil {
		return err
	}
	return ctx.Err()
}

func cmdServeBackground(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return fmt.Errorf("usage: engram serve-background LOG_PATH")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return launchServeBackground(executable, filepath.Clean(args[0]))
}
