package llm

// Note: this test file lives in package llm (not llm_test) so it can inject
// the runCLI function directly on the struct for unit testing.

import (
	"context"
	"errors"
	"testing"
)

// ─── PiRunner tests ────────────────────────────────────────────────────────────

// TestPiRunner_CompileTimeCheck verifies PiRunner satisfies AgentRunner.
var _ AgentRunner = (*PiRunner)(nil)

// TestPiRunner_GoldenNDJSON verifies the runner concatenates text_delta chunks
// into a message that parses as the expected Verdict JSON.
func TestPiRunner_GoldenNDJSON(t *testing.T) {
	// Pi streams the verdict JSON across several text_delta chunks.
	ndjson := `Warning: no project session found; starting fresh
{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"comparing..."}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"{\"Relation\":\"conflicts_with\","}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"\"Confidence\":0.9,\"Reasoning\":\"A and B contradict\",\"Model\":\"deepseek-v4-pro\"}"}}
`

	r := &PiRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
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
	if v.Model != "deepseek-v4-pro" {
		t.Errorf("Model = %q; want %q", v.Model, "deepseek-v4-pro")
	}
}

// TestPiRunner_FencedJSON verifies markdown code fences around the assembled
// message are stripped before parsing.
func TestPiRunner_FencedJSON(t *testing.T) {
	ndjson := "{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"delta\":\"```json\\n{\\\"Relation\\\":\\\"scoped\\\",\\\"Confidence\\\":0.75,\\\"Reasoning\\\":\\\"B narrows A\\\",\\\"Model\\\":\\\"m\\\"}\\n```\"}}\n"

	r := &PiRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	v, err := r.Compare(context.Background(), "compare")
	if err != nil {
		t.Fatalf("Compare with fenced JSON: unexpected error: %v", err)
	}
	if v.Relation != "scoped" {
		t.Errorf("Relation = %q; want %q", v.Relation, "scoped")
	}
}

// TestPiRunner_NoText verifies that a stream with no assistant text returns a
// descriptive error.
func TestPiRunner_NoText(t *testing.T) {
	ndjson := `{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"only thinking"}}
`

	r := &PiRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	_, err := r.Compare(context.Background(), "compare")
	if err == nil {
		t.Fatal("expected error for missing assistant text; got nil")
	}
}

// TestPiRunner_MalformedLine verifies malformed NDJSON lines are skipped and
// processing continues to a valid verdict.
func TestPiRunner_MalformedLine(t *testing.T) {
	ndjson := `not json at all
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"{\"Relation\":\"related\",\"Confidence\":0.6,\"Reasoning\":\"same topic\",\"Model\":\"m\"}"}}
`

	r := &PiRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	v, err := r.Compare(context.Background(), "compare")
	if err != nil {
		t.Fatalf("Compare with malformed line: unexpected error: %v", err)
	}
	if v.Relation != "related" {
		t.Errorf("Relation = %q; want %q", v.Relation, "related")
	}
}

// TestPiRunner_InvalidInnerJSON verifies ErrInvalidJSON is returned when the
// assembled text is not valid JSON.
func TestPiRunner_InvalidInnerJSON(t *testing.T) {
	ndjson := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"this is not json"}}
`

	r := &PiRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrInvalidJSON) {
		t.Errorf("expected ErrInvalidJSON; got %v", err)
	}
}

// TestPiRunner_UnknownRelation verifies ErrUnknownRelation is returned when the
// verdict contains an unrecognized relation verb.
func TestPiRunner_UnknownRelation(t *testing.T) {
	ndjson := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"{\"Relation\":\"maybe\",\"Confidence\":0.5,\"Reasoning\":\"dunno\",\"Model\":\"m\"}"}}
`

	r := &PiRunner{runCLI: fakeCLI([]byte(ndjson), nil)}
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, ErrUnknownRelation) {
		t.Errorf("expected ErrUnknownRelation; got %v", err)
	}
}

// TestPiRunner_CLIError verifies that runCLI errors are propagated.
func TestPiRunner_CLIError(t *testing.T) {
	cliErr := errors.New("pi failed")
	r := &PiRunner{runCLI: fakeCLI(nil, cliErr)}
	_, err := r.Compare(context.Background(), "compare")
	if !errors.Is(err, cliErr) {
		t.Errorf("expected cliErr; got %v", err)
	}
}

// TestNewRunner_Pi verifies the factory routes "pi" to a *PiRunner.
func TestNewRunner_Pi(t *testing.T) {
	r, err := NewRunner("pi")
	if err != nil {
		t.Fatalf("NewRunner(pi): unexpected error: %v", err)
	}
	if _, ok := r.(*PiRunner); !ok {
		t.Errorf("NewRunner(pi) = %T; want *PiRunner", r)
	}
}
