//go:build !windows

package main

// retainMCPProcessUntilParentExit is intentionally a no-op outside Windows.
func retainMCPProcessUntilParentExit() error { return nil }
