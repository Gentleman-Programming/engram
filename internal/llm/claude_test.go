package llm

// Note: this test file lives in package llm (not llm_test) so it can access
// and inject the package-level runCLI variable for unit testing without
// spawning real processes.

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// fakeCLI returns a runCLI func that always returns the given bytes/error.
func fakeCLI(out []byte, err error) func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	return func(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
		return out, err
	}
}

// ─── ClaudeRunner tests ───────────────────────────────────────────────────────

// TestClaudeRunner_CompileTimeCheck verifies ClaudeRunner satisfies AgentRunner.
var _ AgentRunner = (*ClaudeRunner)(nil)

// TestClaudeRunner_GoldenEnvelope verifies the runner parses a clean Claude
// JSON envelope and extracts all Verdict fields correctly.
func TestClaudeRunner_GoldenEnvelope(t *testing.T) {
	innerJSON := `{"Relation":"supersedes","Confidence":0.98,"Reasoning":"A replaces B","Model":"claude-haiku-4-5"}`
	envelope := fmt.Sprintf(
		`{"type":"result","result":%q,"total_cost_usd":0.0001,"modelUsage":{"claude-haiku-4-5":{"input_tokens":300,"output_tokens":50}},"duration_ms":1234}`,
		innerJSON,
	)

	r := &ClaudeRunner{runCLI: fakeCLI([]byte(envelope), nil)}
	v, err := r.Compare(context.Background(), "compare these two")
	if err != nil {
		t.Fatalf("Compare: unexpected error: %v", err)
	}
	if v.Relation != "supersedes" {
		t.Errorf("Relation = %q; want %q", v.Relation, "supersedes")
	}
	if v.Confidence != 0.98 {
		t.Errorf("Confidence = %v; want 0.98", v.Confidence)
	}
	if v.Reasoning != "A replaces B" {
		t.Errorf("Reasoning = %q; want %q", v.Reasoning, "A replaces B")
	}
	if v.Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q; want %q", v.Model, "claude-haiku-4-5")
	}
	if v.DurationMS != 1234 {
		t.Errorf("DurationMS = %d; want 1234", v.DurationMS)
	}
}

// TestClaudeRunner_FenceStrippingWithFence verifies the runner strips markdown
// fences from the inner result before JSON parsing.
func TestClaudeRunner_FenceStrippingWithFence(t *testing.T) {
	innerJSON := "```json\n{\"Relation\":\"compatible\",\"Confidence\":0.85,\"Reasoning\":\"They agree\",\"Model\":\"claude-haiku-4-5\"}\n```"
	envelope := fmt.Sprintf(
		`{"type":"result","result":%q,"total_cost_usd":0.0001,"modelUsage":{"claude-haiku-4-5":{"input_tokens":300,"output_tokens":50}},"duration_ms":500}`,
		innerJSON,
	)

	r := &ClaudeRunner{runCLI: fakeCLI([]byte(envelope), nil)}
	v, err := r.Compare(context.Background(), "compare")
	if err != nil {
		t.Fatalf("Compare with fence: unexpected error: %v", err)
	}
	if v.Relation != "compatible" {
		t.Errorf("Relation = %q; want %q", v.Relation, "compatible")
	}
}

// TestClaudeRunner_FenceStrippingWithoutFence verifies that when the inner
// result has no fences, parsing succeeds normally.
func TestClaudeRunner_FenceStrippingWithoutFence(t *testing.T) {
	innerJSON := `{"Relation":"related","Confidence":0.7,"Reasoning":"Shared topic","Model":"claude-haiku-4-5"}`
	envelope := fmt.Sprintf(
		`{"type":"result","result":%q,"total_cost_usd":0.0001,"modelUsage":{"claude-haiku-4-5":{"input_tokens":300,"output_tokens":50}},"duration_ms":300}`,
		innerJSON,
	)

	r := &ClaudeRunner{runCLI: fakeCLI([]byte(envelope), nil)}
	v, err := r.Compare(context.Background(), "compare")
	if err != nil {
		t.Fatalf("Compare without fence: unexpected error: %v", err)
	}
	if v.Relation != "related" {
		t.Errorf("Relation = %q; want %q", v.Relation, "related")
	}
}

// TestClaudeRunner_InvalidInnerJSON verifies ErrInvalidJSON is returned when
// the inner .result field contains non-JSON text.
func TestClaudeRunner_InvalidInnerJSON(t *testing.T) {
	envelope := `{"type":"result","result":"not valid json","total_cost_usd":0,"modelUsage":{},"duration_ms":0}`

	r := &ClaudeRunner{runCLI: fakeCLI([]byte(envelope), nil)}
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrInvalidJSON) {
		t.Errorf("expected ErrInvalidJSON; got %v", err)
	}
}

// TestClaudeRunner_InvalidOuterJSON verifies ErrInvalidJSON is returned when
// the CLI output is not valid JSON at all.
func TestClaudeRunner_InvalidOuterJSON(t *testing.T) {
	r := &ClaudeRunner{runCLI: fakeCLI([]byte("totally not json"), nil)}
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrInvalidJSON) {
		t.Errorf("expected ErrInvalidJSON; got %v", err)
	}
}

// TestClaudeRunner_UnknownRelation verifies ErrUnknownRelation is returned when
// the LLM returns a relation verb outside the locked vocabulary.
func TestClaudeRunner_UnknownRelation(t *testing.T) {
	innerJSON := `{"Relation":"maybe_conflict","Confidence":0.5,"Reasoning":"dunno","Model":"claude-haiku-4-5"}`
	envelope := fmt.Sprintf(
		`{"type":"result","result":%q,"total_cost_usd":0.0001,"modelUsage":{"claude-haiku-4-5":{"input_tokens":300,"output_tokens":50}},"duration_ms":200}`,
		innerJSON,
	)

	r := &ClaudeRunner{runCLI: fakeCLI([]byte(envelope), nil)}
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrUnknownRelation) {
		t.Errorf("expected ErrUnknownRelation; got %v", err)
	}
}

// TestClaudeRunner_CLIError verifies that runCLI errors are wrapped and returned.
func TestClaudeRunner_CLIError(t *testing.T) {
	cliErr := errors.New("exec failed")
	r := &ClaudeRunner{runCLI: fakeCLI(nil, cliErr)}
	_, err := r.Compare(context.Background(), "compare")
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if !errors.Is(err, cliErr) {
		t.Errorf("expected wrapped cliErr; got %v", err)
	}
}

// TestClaudeRunner_MissingBinary verifies ErrCLINotInstalled is returned when
// the runCLI implementation returns an exec.ErrNotFound-like error.
func TestClaudeRunner_MissingBinary(t *testing.T) {
	// Simulate a PATH-not-found error by using a runCLI that returns ErrCLINotInstalled.
	r := &ClaudeRunner{runCLI: fakeCLI(nil, ErrCLINotInstalled)}
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrCLINotInstalled) {
		t.Errorf("expected ErrCLINotInstalled; got %v", err)
	}
}
