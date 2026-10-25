package mcp

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestServerInstructionsStaysUnderClientTruncationLimit enforces the
// boundary documented for the Claude Code MCP client: it truncates the
// `instructions` field returned during handshake at 2048 runes (UTF-8
// chars after decode). Keeping the constant under that ceiling avoids
// a class of handshake-validity regressions reported against Claude
// Code sessions where `mem_*` tools stopped responding shortly after
// connect.
//
// Source-side `len(serverInstructions)` is BYTES (Go strings are UTF-8
// byte slices). The figure the client uses is the RUNE count —
// em-dashes, ⏤ arrows, etc. each decode as one rune but several
// bytes. Asserting on runes here matches the client's truncation rule.
//
// The reported `Server instructions truncated from 2539 to 2048 chars`
// log line is the empirical evidence for the 2048-rune ceiling; the
// client-side handling after truncation (rejection, reconnect, or
// SIGINT) is documented in the field reports but not reproduced in
// this repo.
func TestServerInstructionsStaysUnderClientTruncationLimit(t *testing.T) {
	const clientTruncationCeiling = 2048

	profiles := map[string]map[string]bool{
		"all (nil)": nil,
		"agent":     ProfileAgent,
		"admin":     ProfileAdmin,
		"empty":     {},
		"custom": {
			"mem_save":   true,
			"mem_search": true,
			"mem_judge":  true,
		},
	}

	for name, allowlist := range profiles {
		t.Run(name, func(t *testing.T) {
			instructions := buildServerInstructions(allowlist)
			runes := utf8.RuneCountInString(instructions)
			t.Logf("profile %q instructions rune count: %d (byte count: %d)", name, runes, len(instructions))
			if runes >= clientTruncationCeiling {
				t.Errorf("profile %q instructions is %d runes (>=%d) — exceeds the documented 2048-rune MCP client truncation ceiling. Trim prose.",
					name, runes, clientTruncationCeiling)
			}
		})
	}
}

// Exercise delivered initialize instructions rather than source literals. These
// assertions establish prose alignment, not actual client/model compliance.
func TestServerInstructionsSelectiveMemoryCadence(t *testing.T) {
	core := make(map[string]bool, len(coreTools))
	for _, tool := range coreTools {
		core[tool.name] = true
	}
	for name, allowlist := range map[string]map[string]bool{
		"all":   nil,
		"core":  core,
		"agent": ProfileAgent,
		"custom": {
			"mem_save": true, "mem_search": true, "mem_session_summary": true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			instructions := strings.Join(strings.Fields(buildServerInstructions(allowlist)), " ")
			for _, rule := range []struct{ name, pattern string }{
				{"verified durable selection", `(?i)verified.{0,80}durable.{0,80}costly[ -]to[ -]reconstruct`},
				{"deliberate topic grouping", `(?i)group.{0,60}(related )?(conclusions|topics)`},
				{"significant explicit closure or handoff", `(?i)significant.{0,60}explicit.{0,60}(clos|handoff)`},
				{"ordinary completion is not closure", `(?i)(ordinary.{0,40}(completion|done).{0,80}(not|no)|not.{0,40}(ordinary completion|merely saying|saying).{0,30}done)`},
			} {
				if !regexp.MustCompile(rule.pattern).MatchString(instructions) {
					t.Errorf("initialize instructions missing %s", rule.name)
				}
			}
			for _, pattern := range []string{
				`(?i)immediately after ANY`,
				`(?i)(mandatory|must).{0,40}before saying.{0,10}done`,
				`(?i)after every (task|event|decision|bug fix|discovery)`,
			} {
				if regexp.MustCompile(pattern).MatchString(instructions) {
					t.Errorf("initialize instructions retain universal mandate %q", pattern)
				}
			}
		})
	}
}

func TestServerInstructionsUsesCandidateJudgmentIDs(t *testing.T) {
	const candidateInstruction = "with candidates[].judgment_id"
	const topLevelWarning = "never reuse the top-level judgment_id"

	candidateIndex := strings.Index(serverInstructions, candidateInstruction)
	warningIndex := strings.Index(serverInstructions, topLevelWarning)
	if candidateIndex < 0 || warningIndex < candidateIndex {
		t.Errorf("serverInstructions must require each candidate's judgment_id and prohibit reusing the top-level judgment_id")
	}
}

func TestMCPToolInventoryMatchesDocumentation(t *testing.T) {
	tools := NewServer(newMCPTestStore(t)).ListTools()
	if len(tools) != 27 {
		t.Fatalf("registered %d tools, want 27", len(tools))
	}
	for _, name := range []string{"mem_judge_many", "mem_session_finalize", "mem_consolidate"} {
		if tools[name] == nil {
			t.Errorf("missing integrated tool %s", name)
		}
	}
	docs, err := os.ReadFile("../../DOCS.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"## MCP Tools (27 tools)",
		`<a id="mcp-tools-23-tools"></a>`,
		`<a id="mcp-tools-25-tools"></a>`,
		"### mem_consolidate",
	} {
		if !strings.Contains(string(docs), text) {
			t.Errorf("MCP documentation missing %q", text)
		}
	}
}

func TestServerInstructionsDeliveryGuarantee(t *testing.T) {
	for _, requirement := range []string{
		"Memory operations are internal bookkeeping, never the user-facing answer.",
		"Complete required memory work before composing the completed-task reply;",
		"send the complete answer as the final message of the turn with no later tool calls.",
		"If memory work fails or needs follow-up, still send the answer.",
	} {
		if !strings.Contains(serverInstructions, requirement) {
			t.Errorf("server instructions missing delivery guarantee: %q", requirement)
		}
	}
}
