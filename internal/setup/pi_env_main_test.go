package setup

import (
	"fmt"
	"os"
	"testing"
)

// Pi exports PI_CODING_AGENT to every process it starts, and the Claude Code
// hooks are no-ops when it is set. Clear it so the hook tests behave the same
// from a Pi shell as from CI.
func TestMain(m *testing.M) {
	if err := os.Unsetenv("PI_CODING_AGENT"); err != nil {
		fmt.Fprintf(os.Stderr, "clear PI_CODING_AGENT: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
