//go:build !windows

package plugin_test

import "testing"

func runCodexWindowsSessionEndManifestCommand(t *testing.T, command, input, port string) (string, string, int) {
	t.Helper()
	t.Fatal("requires Windows")
	return "", "", -1
}
