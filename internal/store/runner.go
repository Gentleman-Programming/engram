package store

import "context"

// SemanticVerdict is the result of a semantic comparison between two observations.
// It mirrors llm.Verdict but lives in this package to avoid a store→llm import cycle.
type SemanticVerdict struct {
	// Relation is one of: conflicts_with, supersedes, scoped, related, compatible, not_conflict.
	Relation   string
	// Confidence is a 0.0–1.0 score from the runner.
	Confidence float64
	// Reasoning is a short human-readable explanation (≤200 chars).
	Reasoning  string
	// Model is the model identifier reported by the CLI (e.g. "claude-haiku-4-5").
	Model      string
	// DurationMS is wall-clock time for the CLI invocation in milliseconds.
	DurationMS int64
}

// SemanticRunner is a duck-typed interface satisfied by *llm.ClaudeRunner and
// *llm.OpenCodeRunner without requiring this package to import internal/llm.
// Any value whose Compare method matches this signature satisfies the interface.
type SemanticRunner interface {
	Compare(ctx context.Context, prompt string) (SemanticVerdict, error)
}
