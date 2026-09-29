package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// launchServeBackground starts a server without inheriting the caller's pipe handles.
// In particular, Git Bash redirection alone cannot detach Windows named pipes.
func launchServeBackground(executable, logPath string) (retErr error) {
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
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() {
		if err := null.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close null device: %w", err)
		}
	}()
	child := exec.Command(executable, "serve")
	child.Stdin, child.Stdout, child.Stderr = null, null, logFile
	if err := child.Start(); err != nil {
		return fmt.Errorf("start background serve: %w", err)
	}
	return child.Process.Release()
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
