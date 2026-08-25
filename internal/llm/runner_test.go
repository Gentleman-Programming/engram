package llm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/llm"
)

// fakeRunner is a compile-time check that a concrete struct satisfying
// the AgentRunner interface can be constructed.
// This pattern proves the interface contract exists and has the right signature.
type fakeRunner struct {
	verdict llm.Verdict
	err     error
}

// Compile-time interface satisfaction check.
var _ llm.AgentRunner = (*fakeRunner)(nil)

func (f *fakeRunner) Compare(ctx context.Context, prompt string) (llm.Verdict, error) {
	return f.verdict, f.err
}

// ─── A.1 tests ────────────────────────────────────────────────────────────────

// TestAgentRunner_HappyPath verifies that a fake AgentRunner returning a valid
// Verdict propagates all fields correctly.
func TestAgentRunner_HappyPath(t *testing.T) {
	want := llm.Verdict{
		Relation:   "compatible",
		Confidence: 0.95,
		Reasoning:  "Both observations talk about the same topic from different angles.",
		Model:      "claude-haiku-4-5",
		DurationMS: 1234,
	}

	runner := &fakeRunner{verdict: want}
	got, err := runner.Compare(context.Background(), "compare these two")
	if err != nil {
		t.Fatalf("Compare: unexpected error: %v", err)
	}

	if got.Relation != want.Relation {
		t.Errorf("Verdict.Relation = %q; want %q", got.Relation, want.Relation)
	}
	if got.Confidence != want.Confidence {
		t.Errorf("Verdict.Confidence = %v; want %v", got.Confidence, want.Confidence)
	}
	if got.Reasoning != want.Reasoning {
		t.Errorf("Verdict.Reasoning = %q; want %q", got.Reasoning, want.Reasoning)
	}
	if got.Model != want.Model {
		t.Errorf("Verdict.Model = %q; want %q", got.Model, want.Model)
	}
	if got.DurationMS != want.DurationMS {
		t.Errorf("Verdict.DurationMS = %d; want %d", got.DurationMS, want.DurationMS)
	}
}

// TestAgentRunner_ErrorPropagation verifies that when a runner returns an error,
// the Verdict is zero-value and the error is propagated.
func TestAgentRunner_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("runner failure")
	runner := &fakeRunner{err: sentinel}

	got, err := runner.Compare(context.Background(), "compare these two")
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error; got %v", err)
	}

	// Verdict MUST be zero-value on error.
	if got.Relation != "" {
		t.Errorf("Verdict.Relation must be empty on error; got %q", got.Relation)
	}
	if got.Confidence != 0 {
		t.Errorf("Verdict.Confidence must be 0 on error; got %v", got.Confidence)
	}
	if got.Model != "" {
		t.Errorf("Verdict.Model must be empty on error; got %q", got.Model)
	}
}

// TestErrorSentinels verifies that all required error sentinels are exported
// and have non-empty messages.
func TestErrorSentinels(t *testing.T) {
	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrCLINotInstalled", llm.ErrCLINotInstalled},
		{"ErrCLIAuthMissing", llm.ErrCLIAuthMissing},
		{"ErrTimeout", llm.ErrTimeout},
		{"ErrInvalidJSON", llm.ErrInvalidJSON},
		{"ErrUnknownRelation", llm.ErrUnknownRelation},
	}

	for _, tc := range sentinels {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil {
				t.Fatalf("%s must not be nil", tc.name)
			}
			if tc.err.Error() == "" {
				t.Fatalf("%s must have a non-empty message", tc.name)
			}
		})
	}
}

// TestVerdict_ZeroValueSafe verifies that an uninitialized Verdict is safe to
// inspect (all fields default to Go zero values — no panics).
func TestVerdict_ZeroValueSafe(t *testing.T) {
	var v llm.Verdict
	_ = v.Relation
	_ = v.Confidence
	_ = v.Reasoning
	_ = v.Model
	_ = v.DurationMS
	// If we reached here, the struct is zero-value safe.
}
