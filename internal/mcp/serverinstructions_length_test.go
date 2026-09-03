package mcp

import (
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

func TestServerInstructionsUsesCandidateJudgmentIDs(t *testing.T) {
	const candidateInstruction = "once per entry using that entry's judgment_id"
	const topLevelWarning = "never reuse the top-level judgment_id"

	candidateIndex := strings.Index(serverInstructions, candidateInstruction)
	warningIndex := strings.Index(serverInstructions, topLevelWarning)
	if candidateIndex < 0 || warningIndex < candidateIndex {
		t.Errorf("serverInstructions must require each candidate's judgment_id and prohibit reusing the top-level judgment_id")
	}
}
