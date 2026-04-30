// Package llm defines the AgentRunner abstraction used by Engram to delegate
// semantic comparison of observation pairs to an external LLM CLI tool
// (e.g., Claude Code or OpenCode). It ships concrete runner implementations
// and a factory that selects the runner via the ENGRAM_AGENT_CLI env var.
//
// The package is a strict boundary: only cmd/engram/conflicts.go and
// internal/store/relations.go are permitted to import it. No other package
// in the Engram codebase imports internal/llm.
package llm

import (
	"context"
	"errors"
)

// ─── AgentRunner interface ─────────────────────────────────────────────────────

// AgentRunner is the abstraction over an external LLM CLI that performs
// semantic comparison of two observations. Each runner implementation
// shells out to a specific CLI tool, parses its output, and returns a
// structured Verdict.
type AgentRunner interface {
	// Compare sends prompt to the underlying LLM CLI and returns a structured
	// Verdict with the semantic relation between two observations.
	// On error the returned Verdict is zero-value.
	Compare(ctx context.Context, prompt string) (Verdict, error)
}

// ─── Verdict struct ────────────────────────────────────────────────────────────

// Verdict is the parsed output of a single AgentRunner.Compare call.
// It holds the semantic relation verb, confidence score, model attribution,
// and timing information captured from the CLI output.
type Verdict struct {
	// Relation is the semantic relation verb returned by the LLM.
	// Must be one of: conflicts_with | supersedes | scoped | related | compatible | not_conflict
	Relation string

	// Confidence is the LLM's self-reported confidence score in [0.0, 1.0].
	Confidence float64

	// Reasoning is the LLM's short explanation (≤200 chars).
	Reasoning string

	// Model is the model identifier captured from the CLI output
	// (e.g., "claude-haiku-4-5"). May be empty if the CLI does not report it.
	Model string

	// DurationMS is the wall-clock duration of the CLI call in milliseconds.
	DurationMS int64
}

// ─── Error sentinels ──────────────────────────────────────────────────────────

var (
	// ErrCLINotInstalled is returned when the agent CLI binary is not found in PATH.
	ErrCLINotInstalled = errors.New("agent CLI binary not found in PATH")

	// ErrCLIAuthMissing is returned when the agent CLI is installed but not authenticated.
	ErrCLIAuthMissing = errors.New("agent CLI is not authenticated")

	// ErrTimeout is returned when the agent CLI call exceeds the configured per-pair timeout.
	ErrTimeout = errors.New("agent CLI call exceeded timeout")

	// ErrInvalidJSON is returned when the agent CLI returns output that cannot be parsed
	// as the expected JSON envelope or Verdict JSON.
	ErrInvalidJSON = errors.New("agent CLI returned malformed JSON")

	// ErrUnknownRelation is returned when the LLM verdict contains a relation verb
	// that is not in the locked vocabulary
	// (conflicts_with | supersedes | scoped | related | compatible | not_conflict).
	ErrUnknownRelation = errors.New("agent returned a relation outside the locked vocabulary")
)
