//go:build !windows

// Package command provides platform-aware external command construction.
package command

import (
	"context"
	"os/exec"
)

// NewContext creates a command associated with ctx using the platform's process attributes.
func NewContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
