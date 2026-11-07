//go:build !windows

package daemon

import (
	"errors"
	"syscall"
)

// ProcessAlive reports whether a pid belongs to a live process. Signal 0
// performs the existence check; a permission error still proves the process
// exists, so it counts as alive. Pid 0 and negative pids address process
// groups rather than one process, so they are rejected up front.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
