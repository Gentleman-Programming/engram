package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ─── OpenCodeRunner ───────────────────────────────────────────────────────────

// OpenCodeRunner implements AgentRunner by shelling out to the `opencode` CLI.
// It invokes: opencode run --format json --pure <prompt>
// and parses the NDJSON event stream returned on stdout.
type OpenCodeRunner struct {
	// runCLI is the shell-out function. Defaults to defaultRunCLI.
	// Tests inject a fake implementation to avoid spawning real processes.
	runCLI func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)
	// generateCLI runs Generate calls. Nil uses neutralDirRunCLI; tests inject a fake.
	generateCLI func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)
}

// NewOpenCodeRunner constructs an OpenCodeRunner with the real exec.CommandContext
// implementation. Tests should inject a fake via the struct field directly.
func NewOpenCodeRunner() *OpenCodeRunner {
	return &OpenCodeRunner{runCLI: defaultRunCLI, generateCLI: neutralDirRunCLI}
}

// Compare sends prompt to the OpenCode CLI and returns a structured Verdict.
// Invokes: opencode run --format json --pure (with prompt on stdin)
//
// OpenCode's output is NDJSON (newline-delimited JSON). Each line is a JSON
// object with a "type" field. The runner scans for events of type "text",
// extracts ".part.text", and parses that as a Verdict JSON object.
// If multiple text events exist, the last one wins.
func (r *OpenCodeRunner) Compare(ctx context.Context, prompt string) (Verdict, error) {
	args := []string{"run", "--format", "json", "--pure"}
	raw, err := r.runCLI(ctx, "opencode", args, prompt)
	if err != nil {
		return Verdict{}, err
	}

	return parseOpenCodeNDJSON(raw)
}

// Generate sends prompt to the OpenCode CLI and returns the concatenated text
// parts without verdict parsing. Used by `engram dream`. It runs from a
// throwaway cwd; `opencode run` has no flag to disable tools, so tool access
// follows the user's opencode permission config.
func (r *OpenCodeRunner) Generate(ctx context.Context, prompt string) (string, error) {
	run := r.generateCLI
	if run == nil {
		run = neutralDirRunCLI
	}
	raw, err := run(ctx, "opencode", []string{"run", "--format", "json", "--pure"}, prompt)
	if err != nil {
		return "", err
	}
	var text strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var ev openCodeEvent
		if json.Unmarshal(bytes.TrimSpace(scanner.Bytes()), &ev) == nil && ev.Type == "text" && ev.Part != nil {
			text.WriteString(ev.Part.Text)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("opencode: read NDJSON stream: %w", err)
	}
	if text.Len() == 0 {
		return "", fmt.Errorf("opencode: no text event found in NDJSON stream")
	}
	return text.String(), nil
}

// ─── Compile-time interface satisfaction ──────────────────────────────────────

var _ AgentRunner = (*OpenCodeRunner)(nil)

// ─── NDJSON parsing ───────────────────────────────────────────────────────────

// openCodeEvent is the generic envelope for each NDJSON line from OpenCode.
type openCodeEvent struct {
	Type      string          `json:"type"`
	Part      *openCodePart   `json:"part,omitempty"`
	Timestamp string          `json:"timestamp,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// openCodePart is the payload of a "text" event.
type openCodePart struct {
	Text string `json:"text"`
}

// openCodeStepFinishMeta captures model and token info from step_finish metadata.
type openCodeStepFinishMeta struct {
	Model  string `json:"model"`
	Tokens struct {
		Input  int `json:"input"`
		Output int `json:"output"`
	} `json:"tokens"`
}

// parseOpenCodeNDJSON scans NDJSON output from OpenCode and returns a Verdict.
// Malformed lines are skipped and scanning continues.
// If no text event is found, a descriptive error is returned.
// Multiple text events: last one wins.
func parseOpenCodeNDJSON(raw []byte) (Verdict, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))

	var (
		lastTextContent string
		foundText       bool
		stepStartTime   time.Time
		stepFinishTime  time.Time
		model           string
	)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var ev openCodeEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// Malformed line: skip and continue.
			continue
		}

		switch ev.Type {
		case "step_start":
			if ev.Timestamp != "" {
				if t, err := time.Parse(time.RFC3339, ev.Timestamp); err == nil {
					stepStartTime = t
				}
			}

		case "text":
			if ev.Part != nil && ev.Part.Text != "" {
				lastTextContent = ev.Part.Text
				foundText = true
			}

		case "step_finish":
			if ev.Timestamp != "" {
				if t, err := time.Parse(time.RFC3339, ev.Timestamp); err == nil {
					stepFinishTime = t
				}
			}
			if len(ev.Metadata) > 0 {
				var meta openCodeStepFinishMeta
				if err := json.Unmarshal(ev.Metadata, &meta); err == nil {
					if meta.Model != "" {
						model = meta.Model
					}
				}
			}
		}
	}

	if !foundText {
		return Verdict{}, fmt.Errorf("opencode: no text event found in NDJSON stream")
	}

	// Parse the inner Verdict JSON from the last text event.
	var iv innerVerdict
	if err := json.Unmarshal([]byte(lastTextContent), &iv); err != nil {
		return Verdict{}, fmt.Errorf("%w: inner verdict from opencode text event: %v", ErrInvalidJSON, err)
	}

	// Validate the relation verb.
	if !validRelations[iv.Relation] {
		return Verdict{}, fmt.Errorf("%w: %q", ErrUnknownRelation, iv.Relation)
	}

	// Use model from step_finish metadata, fall back to the inner JSON field.
	if model == "" {
		model = iv.Model
	}

	// Calculate duration from step_start → step_finish timestamps if available.
	var durationMS int64
	if !stepStartTime.IsZero() && !stepFinishTime.IsZero() && stepFinishTime.After(stepStartTime) {
		durationMS = stepFinishTime.Sub(stepStartTime).Milliseconds()
	}

	return Verdict{
		Relation:   iv.Relation,
		Confidence: iv.Confidence,
		Reasoning:  iv.Reasoning,
		Model:      model,
		DurationMS: durationMS,
	}, nil
}
