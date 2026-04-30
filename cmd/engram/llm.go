package main

import (
	"context"
	"errors"
	"os"

	"github.com/Gentleman-Programming/engram/internal/llm"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── agentRunnerFactory default ───────────────────────────────────────────────

// defaultAgentRunnerFactory is the production implementation of agentRunnerFactory.
// It delegates to llm.NewRunner, which selects a runner based on the provided name
// (typically read from ENGRAM_AGENT_CLI). The returned runner satisfies
// store.SemanticRunner via the llmRunnerAdapter bridge.
func defaultAgentRunnerFactory(name string) (store.SemanticRunner, error) {
	runner, err := llm.NewRunner(name)
	if err != nil {
		return nil, err
	}
	return llmRunnerAdapter{inner: runner}, nil
}

// ─── llmRunnerAdapter ─────────────────────────────────────────────────────────

// llmRunnerAdapter bridges llm.AgentRunner (internal/llm) to store.SemanticRunner
// (internal/store) without introducing a store→llm import cycle.
// The two Verdict/SemanticVerdict types are structurally identical; the adapter
// copies fields one-to-one.
type llmRunnerAdapter struct {
	inner llm.AgentRunner
}

// Compare satisfies store.SemanticRunner. It delegates to the wrapped llm.AgentRunner
// and copies the result into a store.SemanticVerdict.
func (a llmRunnerAdapter) Compare(ctx context.Context, prompt string) (store.SemanticVerdict, error) {
	v, err := a.inner.Compare(ctx, prompt)
	if err != nil {
		return store.SemanticVerdict{}, err
	}
	return store.SemanticVerdict{
		Relation:   v.Relation,
		Confidence: v.Confidence,
		Reasoning:  v.Reasoning,
		Model:      v.Model,
		DurationMS: v.DurationMS,
	}, nil
}

// ─── llmBuildPrompt ───────────────────────────────────────────────────────────

// llmBuildPrompt adapts store.ObservationSnippet to llm.ObservationSnippet and
// delegates to llm.BuildPrompt. Used by both CLI and HTTP server codepaths to
// ensure prompt generation is consistent.
func llmBuildPrompt(a, b store.ObservationSnippet) string {
	return llm.BuildPrompt(
		llm.ObservationSnippet{ID: a.SyncID, Title: a.Title, Type: a.Type, Content: a.Content},
		llm.ObservationSnippet{ID: b.SyncID, Title: b.Title, Type: b.Type, Content: b.Content},
	)
}

// ─── resolveAgentRunner ───────────────────────────────────────────────────────

// resolveAgentRunner reads ENGRAM_AGENT_CLI and calls agentRunnerFactory to
// obtain a store.SemanticRunner. Returns a clear error if the env var is unset
// or the factory rejects the value.
func resolveAgentRunner() (store.SemanticRunner, error) {
	name := os.Getenv("ENGRAM_AGENT_CLI")
	if name == "" {
		return nil, errors.New("ENGRAM_AGENT_CLI is not set; required for --semantic scan (set to 'claude' or 'opencode')")
	}
	return agentRunnerFactory(name)
}
