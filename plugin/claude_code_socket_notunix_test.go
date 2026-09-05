//go:build !unix

package plugin_test

import "testing"

func requireUnixSocketHooks(t *testing.T) {
	t.Helper()
	t.Skip("Unix-domain socket hook behavior requires a Unix target")
}
