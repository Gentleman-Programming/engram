package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ─── PiRunner ─────────────────────────────────────────────────────────────────

// PiRunner implements AgentRunner by shelling out to the `pi` CLI.
// It invokes: pi -p --mode json --no-context-files (with the prompt on stdin)
// and parses Pi's NDJSON event stream, accumulating assistant text deltas into
// the final message which is then parsed as a Verdict JSON object.
//
// Pi routes through the user's own provider configuration (e.g. a cheap
// 9router model), which makes it the low-cost background consolidation runner:
// set ENGRAM_AGENT_CLI=pi to drive `conflicts scan --semantic` with it.
type PiRunner struct {
	// runCLI is the shell-out function. Defaults to defaultRunCLI.
	// Tests inject a fake implementation to avoid spawning real processes.
	runCLI func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)
}

// NewPiRunner constructs a PiRunner with the real exec.CommandContext
// implementation. Tests should inject a fake via the struct field directly.
func NewPiRunner() *PiRunner {
	return &PiRunner{runCLI: defaultRunCLI}
}

// Compare sends prompt to the Pi CLI and returns a structured Verdict.
// Invokes: pi -p --mode json --no-context-files
//
// Pi's output is NDJSON (newline-delimited JSON). Assistant text arrives as a
// stream of "message_update" events carrying "text_delta" chunks; the runner
// concatenates those chunks and parses the assembled message as a Verdict.
func (r *PiRunner) Compare(ctx context.Context, prompt string) (Verdict, error) {
	args := []string{"-p", "--mode", "json", "--no-context-files"}
	raw, err := r.runCLI(ctx, "pi", args, prompt)
	if err != nil {
		// Propagate sentinel errors directly (e.g. ErrCLINotInstalled).
		return Verdict{}, err
	}

	return parsePiNDJSON(raw)
}

// ─── Compile-time interface satisfaction ──────────────────────────────────────

var _ AgentRunner = (*PiRunner)(nil)

// ─── NDJSON parsing ───────────────────────────────────────────────────────────

// piEvent is the generic envelope for each NDJSON line Pi emits in --mode json.
type piEvent struct {
	Type                  string          `json:"type"`
	AssistantMessageEvent *piAssistantMsg `json:"assistantMessageEvent,omitempty"`
}

// piAssistantMsg is the payload of a "message_update" event.
type piAssistantMsg struct {
	Type  string `json:"type"` // text_delta | thinking_delta | ...
	Delta string `json:"delta"`
	Model string `json:"model,omitempty"`
}

// parsePiNDJSON scans Pi's NDJSON output, concatenates assistant text_delta
// chunks into the final message, and parses it as a Verdict JSON object.
// Malformed lines (Pi prints a non-JSON banner before the stream) and non-text
// events are skipped; thinking_delta chunks are ignored (reasoning stream, not
// the answer).
func parsePiNDJSON(raw []byte) (Verdict, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	// Pi echoes large payloads on a single line; raise the token cap well above
	// bufio's 64KB default so long lines don't abort the scan.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var (
		text  strings.Builder
		model string
	)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var ev piEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// Malformed line: skip and continue.
			continue
		}

		if ev.Type == "message_update" && ev.AssistantMessageEvent != nil {
			ame := ev.AssistantMessageEvent
			if ame.Type == "text_delta" && ame.Delta != "" {
				text.WriteString(ame.Delta)
			}
			if ame.Model != "" {
				model = ame.Model
			}
		}
	}

	final := strings.TrimSpace(text.String())
	if final == "" {
		return Verdict{}, fmt.Errorf("pi: no assistant text found in NDJSON stream")
	}

	// Strip optional markdown code fences before parsing the inner Verdict JSON.
	if m := fenceRE.FindStringSubmatch(final); len(m) == 2 {
		final = strings.TrimSpace(m[1])
	}

	var iv innerVerdict
	if err := json.Unmarshal([]byte(final), &iv); err != nil {
		return Verdict{}, fmt.Errorf("%w: inner verdict from pi text: %v", ErrInvalidJSON, err)
	}

	// Validate the relation verb.
	if !validRelations[iv.Relation] {
		return Verdict{}, fmt.Errorf("%w: %q", ErrUnknownRelation, iv.Relation)
	}

	// Prefer a model reported by the stream, else the inner JSON field.
	if model == "" {
		model = iv.Model
	}

	return Verdict{
		Relation:   iv.Relation,
		Confidence: iv.Confidence,
		Reasoning:  iv.Reasoning,
		Model:      model,
	}, nil
}
