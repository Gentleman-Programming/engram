//go:build !windows

package daemon

// ListenerOwnerPID is unavailable on this platform. It never authorizes a signal.
func ListenerOwnerPID(int) int { return 0 }
