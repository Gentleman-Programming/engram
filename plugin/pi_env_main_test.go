package plugin_test

import (
	"os"
	"testing"
)

// Pi exports PI_CODING_AGENT to every process it starts, and the Claude Code
// hooks are no-ops when it is set. Clear it so the hook tests behave the same
// from a Pi shell as from CI; the Pi guard tests set it explicitly.
func TestMain(m *testing.M) {
	os.Unsetenv("PI_CODING_AGENT")
	os.Exit(m.Run())
}
