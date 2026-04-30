package llm

// Note: this test file lives in package llm (not llm_test) so it can inject
// the runCLI function directly on the struct for unit testing.

import (
	"context"
	"errors"
	"testing"
)

// ─── OpenCodeRunner tests ─────────────────────────────────────────────────────

// TestOpenCodeRunner_CompileTimeCheck verifies OpenCodeRunner satisfies AgentRunner.
var _ AgentRunner = (*OpenCodeRunner)(nil)

// TestOpenCodeRunner_GoldenNDJSON verifies the runner parses a valid NDJSON
// event stream containing a text event with the expected Verdict JSON.
func TestOpenCodeRunner_GoldenNDJSON(t *testing.T) {
	// Simulate an OpenCode NDJSON stream with step_start, text, step_finish events.
	ndjson := `{"type":"step_start","timestamp":"2026-01-01T00:00:00Z","metadata":{}}
{"type":"text","part":{"text":"{\"Relation\":\"conflicts_with\",\"Confidence\":0.9,\"Reasoning\":\"A and B contradict\",\"Model\":\"gpt-4o\"}"}}
{"type":"step_finish","timestamp":"2026-01-01T00:00:01Z","metadata":{"model":"gpt-4o","tokens":{"input":300,"output":50}}}
`

	r := &OpenCodeRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	v, err := r.Compare(context.Background(), "compare")
	if err != nil {
		t.Fatalf("Compare: unexpected error: %v", err)
	}
	if v.Relation != "conflicts_with" {
		t.Errorf("Relation = %q; want %q", v.Relation, "conflicts_with")
	}
	if v.Confidence != 0.9 {
		t.Errorf("Confidence = %v; want 0.9", v.Confidence)
	}
	if v.Reasoning != "A and B contradict" {
		t.Errorf("Reasoning = %q; want %q", v.Reasoning, "A and B contradict")
	}
}

// TestOpenCodeRunner_MissingTextEvent verifies that a stream with no text event
// returns a descriptive error.
func TestOpenCodeRunner_MissingTextEvent(t *testing.T) {
	ndjson := `{"type":"step_start","timestamp":"2026-01-01T00:00:00Z","metadata":{}}
{"type":"step_finish","timestamp":"2026-01-01T00:00:01Z","metadata":{}}
`

	r := &OpenCodeRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	_, err := r.Compare(context.Background(), "compare")
	if err == nil {
		t.Fatal("expected error for missing text event; got nil")
	}
}

// TestOpenCodeRunner_MalformedLine verifies that malformed NDJSON lines are
// skipped and processing continues; if a valid text event exists later, it succeeds.
func TestOpenCodeRunner_MalformedLine(t *testing.T) {
	ndjson := `not json at all
{"type":"text","part":{"text":"{\"Relation\":\"scoped\",\"Confidence\":0.75,\"Reasoning\":\"B narrows A\",\"Model\":\"gpt-4o\"}"}}
`

	r := &OpenCodeRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	v, err := r.Compare(context.Background(), "compare")
	if err != nil {
		t.Fatalf("Compare with malformed line: unexpected error: %v", err)
	}
	if v.Relation != "scoped" {
		t.Errorf("Relation = %q; want %q", v.Relation, "scoped")
	}
}

// TestOpenCodeRunner_MultipleTextEvents verifies that when multiple text events
// appear, the LAST one is used.
func TestOpenCodeRunner_MultipleTextEvents(t *testing.T) {
	ndjson := `{"type":"text","part":{"text":"{\"Relation\":\"related\",\"Confidence\":0.6,\"Reasoning\":\"first\",\"Model\":\"gpt-4o\"}"}}
{"type":"text","part":{"text":"{\"Relation\":\"compatible\",\"Confidence\":0.8,\"Reasoning\":\"last\",\"Model\":\"gpt-4o\"}"}}
`

	r := &OpenCodeRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	v, err := r.Compare(context.Background(), "compare")
	if err != nil {
		t.Fatalf("Compare with multiple text events: unexpected error: %v", err)
	}
	if v.Relation != "compatible" {
		t.Errorf("Relation = %q; want %q (last event wins)", v.Relation, "compatible")
	}
	if v.Reasoning != "last" {
		t.Errorf("Reasoning = %q; want %q (last event wins)", v.Reasoning, "last")
	}
}

// TestOpenCodeRunner_InvalidInnerJSON verifies ErrInvalidJSON is returned when
// the text event's part.text is not valid JSON.
func TestOpenCodeRunner_InvalidInnerJSON(t *testing.T) {
	ndjson := `{"type":"text","part":{"text":"this is not json"}}
`

	r := &OpenCodeRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrInvalidJSON) {
		t.Errorf("expected ErrInvalidJSON; got %v", err)
	}
}

// TestOpenCodeRunner_CLIError verifies that runCLI errors are propagated.
func TestOpenCodeRunner_CLIError(t *testing.T) {
	cliErr := errors.New("opencode failed")
	r := &OpenCodeRunner{runCLI: fakeCLI(nil, cliErr)}
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, cliErr) {
		t.Errorf("expected cliErr; got %v", err)
	}
}

// TestOpenCodeRunner_UnknownRelation verifies ErrUnknownRelation is returned
// when the text event contains an unrecognized relation verb.
func TestOpenCodeRunner_UnknownRelation(t *testing.T) {
	ndjson := `{"type":"text","part":{"text":"{\"Relation\":\"maybe\",\"Confidence\":0.5,\"Reasoning\":\"dunno\",\"Model\":\"gpt-4o\"}"}}
`

	r := &OpenCodeRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrUnknownRelation) {
		t.Errorf("expected ErrUnknownRelation; got %v", err)
	}
}
