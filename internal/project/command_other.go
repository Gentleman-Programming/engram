//go:build !windows

package project

import (
	"context"
	"os/exec"
)

func newProjectCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
