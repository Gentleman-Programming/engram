package llm

import (
	"errors"
	"fmt"
)

// ─── Sentinel errors ──────────────────────────────────────────────────────────

// ErrInvalidRunnerName is returned by NewRunner when the name argument does not
// match a known runner identifier ("claude" | "opencode").
var ErrInvalidRunnerName = errors.New("invalid runner name")

// ─── Factory ──────────────────────────────────────────────────────────────────

// NewRunner returns an AgentRunner for the given runner name.
// Supported values:
//   - "claude"    → *ClaudeRunner (shells out to the claude CLI)
//   - "opencode"  → *OpenCodeRunner (shells out to the opencode CLI)
//
// For any other value, including the empty string, a descriptive error is
// returned that names the ENGRAM_AGENT_CLI environment variable and the
// supported values.
//
// Typical usage (reading from the environment):
//
//	runner, err := llm.NewRunner(os.Getenv("ENGRAM_AGENT_CLI"))
func NewRunner(name string) (AgentRunner, error) {
	switch name {
	case "claude":
		return NewClaudeRunner(), nil

	case "opencode":
		return NewOpenCodeRunner(), nil

	case "":
		return nil, fmt.Errorf(
			"%w: ENGRAM_AGENT_CLI is not set; supported values are: claude, opencode",
			ErrInvalidRunnerName,
		)

	default:
		return nil, fmt.Errorf(
			"%w: %q is not a recognized runner; set ENGRAM_AGENT_CLI to one of: claude, opencode",
			ErrInvalidRunnerName,
			name,
		)
	}
}
