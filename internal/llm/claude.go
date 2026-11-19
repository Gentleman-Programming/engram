package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/command"
)

// ─── ClaudeRunner ─────────────────────────────────────────────────────────────

// ClaudeRunner implements AgentRunner by shelling out to the `claude` CLI.
// It invokes: claude -p <prompt> --output-format json --model haiku --max-turns 1
// and parses the JSON envelope returned by `claude --output-format json`.
type ClaudeRunner struct {
	// runCLI is the shell-out function. Defaults to defaultRunCLI.
	// Tests inject a fake implementation to avoid spawning real processes.
	runCLI func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)
	// generateCLI runs Generate calls. Nil uses neutralDirRunCLI; tests inject a fake.
	generateCLI func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)
}

// NewClaudeRunner constructs a ClaudeRunner with the real exec.CommandContext
// implementation. Tests should inject a fake via the struct field directly.
func NewClaudeRunner() *ClaudeRunner {
	return &ClaudeRunner{runCLI: defaultRunCLI, generateCLI: neutralDirRunCLI}
}

// Compare sends prompt to the Claude CLI and returns a structured Verdict.
// Invokes: claude -p --output-format json --model haiku --max-turns 1
//
// Claude's JSON envelope format:
//
//	{
//	  "type":         "result",
//	  "result":       "<inner JSON or fence-wrapped JSON>",
//	  "total_cost_usd": ...,
//	  "modelUsage":   { "<model-id>": { "input_tokens": N, "output_tokens": N } },
//	  "duration_ms":  N
//	}
//
// The inner result is parsed as a Verdict JSON object (possibly wrapped in
// markdown code fences, which are stripped before parsing).
func (r *ClaudeRunner) Compare(ctx context.Context, prompt string) (Verdict, error) {
	args := []string{"-p", "--output-format", "json", "--model", "haiku", "--max-turns", "1"}
	raw, err := r.runCLI(ctx, "claude", args, prompt)
	if err != nil {
		// Propagate sentinel errors directly (e.g. ErrCLINotInstalled injected by tests
		// or returned by defaultRunCLI on exec.ErrNotFound).
		return Verdict{}, err
	}

	return parseClaudeEnvelope(raw)
}

// Generate sends prompt to the Claude CLI and returns the model's raw reply
// (the envelope's result, fences stripped) without verdict parsing. Used by
// `engram dream` to draft consolidated observations. The prompt embeds
// third-party memory content, so the CLI runs with no built-in tools
// (--tools ""), no MCP servers (--strict-mcp-config) and a throwaway cwd.
func (r *ClaudeRunner) Generate(ctx context.Context, prompt string) (string, error) {
	args := []string{"-p", "--output-format", "json", "--model", "haiku", "--max-turns", "1", "--tools", "", "--strict-mcp-config"}
	run := r.generateCLI
	if run == nil {
		run = neutralDirRunCLI
	}
	raw, err := run(ctx, "claude", args, prompt)
	if err != nil {
		return "", err
	}
	var env claudeEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("%w: outer envelope: %v", ErrInvalidJSON, err)
	}
	if env.IsError || (env.Subtype != "" && env.Subtype != "success") {
		return "", fmt.Errorf("claude CLI reported an error (subtype %q): %.200s", env.Subtype, env.Result)
	}
	inner := strings.TrimSpace(env.Result)
	if m := fenceRE.FindStringSubmatch(inner); len(m) == 2 {
		inner = strings.TrimSpace(m[1])
	}
	return inner, nil
}

// ─── Compile-time interface satisfaction ──────────────────────────────────────

var _ AgentRunner = (*ClaudeRunner)(nil)

// ─── Envelope parsing ─────────────────────────────────────────────────────────

// claudeEnvelope is the top-level JSON object returned by `claude --output-format json`.
type claudeEnvelope struct {
	Type       string                     `json:"type"`
	Result     string                     `json:"result"`
	DurationMS int64                      `json:"duration_ms"`
	IsError    bool                       `json:"is_error"`
	Subtype    string                     `json:"subtype"`
	ModelUsage map[string]json.RawMessage `json:"modelUsage"`
}

// innerVerdict is the JSON shape the LLM is prompted to return.
type innerVerdict struct {
	Relation   string  `json:"Relation"`
	Confidence float64 `json:"Confidence"`
	Reasoning  string  `json:"Reasoning"`
	Model      string  `json:"Model"`
}

// fenceRE strips optional markdown code fences (```json ... ``` or ``` ... ```).
var fenceRE = regexp.MustCompile("(?s)^```[a-zA-Z]*\\n?(.+?)\\n?```$")

// validRelations is the locked vocabulary of relation verbs.
var validRelations = map[string]bool{
	"conflicts_with": true,
	"supersedes":     true,
	"scoped":         true,
	"related":        true,
	"compatible":     true,
	"not_conflict":   true,
}

// parseClaudeEnvelope decodes the Claude CLI JSON envelope and returns a Verdict.
func parseClaudeEnvelope(raw []byte) (Verdict, error) {
	var env claudeEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Verdict{}, fmt.Errorf("%w: outer envelope: %v", ErrInvalidJSON, err)
	}

	// Strip markdown fences from the inner result string.
	inner := strings.TrimSpace(env.Result)
	if m := fenceRE.FindStringSubmatch(inner); len(m) == 2 {
		inner = strings.TrimSpace(m[1])
	}

	// Parse the inner Verdict JSON.
	var iv innerVerdict
	if err := json.Unmarshal([]byte(inner), &iv); err != nil {
		return Verdict{}, fmt.Errorf("%w: inner verdict: %v", ErrInvalidJSON, err)
	}

	// Validate the relation verb.
	if !validRelations[iv.Relation] {
		return Verdict{}, fmt.Errorf("%w: %q", ErrUnknownRelation, iv.Relation)
	}

	// Capture model from modelUsage keys (there is exactly one key per call).
	model := iv.Model
	if model == "" {
		for k := range env.ModelUsage {
			model = k
			break
		}
	}

	return Verdict{
		Relation:   iv.Relation,
		Confidence: iv.Confidence,
		Reasoning:  iv.Reasoning,
		Model:      model,
		DurationMS: env.DurationMS,
	}, nil
}

// ─── Default runCLI implementation ────────────────────────────────────────────

// defaultRunCLI executes an external CLI command, passing stdin as the process's
// standard input, and returns the combined stdout+stderr output.
// It translates exec.ErrNotFound into ErrCLINotInstalled.
func defaultRunCLI(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	return runCLIIn(ctx, "", name, args, stdin)
}

// neutralDirRunCLI runs the CLI from a fresh empty temp directory so it never
// picks up the caller's project files or project-level settings.
func neutralDirRunCLI(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "engram-llm-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	return runCLIIn(ctx, dir, name, args, stdin)
}

// runCLIIn is defaultRunCLI with an explicit working directory ("" inherits).
func runCLIIn(ctx context.Context, dir, name string, args []string, stdin string) ([]byte, error) {
	cmd := command.NewContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Include stderr in the error for diagnostics.
			return nil, fmt.Errorf("CLI %q exited %d: %s", name, exitErr.ExitCode(), string(exitErr.Stderr))
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%w: %q", ErrCLINotInstalled, name)
		}
		return nil, err
	}
	return out, nil
}
