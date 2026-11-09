//go:build windows

// Package command provides platform-aware external command construction.
package command

import (
	"context"
	"os/exec"
	"syscall"
)

// NewContext creates a command associated with ctx and hides its window on Windows.
func NewContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}
