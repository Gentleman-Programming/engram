package mcp

import (
	"testing"
	"unicode/utf8"
)

// TestServerInstructions_StaysUnder2048CharsIssue1019 enforces the boundary
// that issue #1019 surfaced: Claude Code's MCP client truncates server
// instructions at 2048 CHARS (runes, after UTF-8 decode) and SIGINTs the
// connection ~4 seconds after handshake when truncation happens. Keeping
// the constant under that ceiling is what keeps `engram` connected in the
// plugin marketplace path.
//
// Source-side `len(serverInstructions)` is BYTES (Go strings are UTF-8 byte
// slices). The figure Claude Code uses is the RUNE count — em-dashes, ⏤
// arrows, etc. each decode as one rune but several bytes. Issue #1019
// reported "Server instructions truncated from 2539 to 2048 chars" — that
// 2539 is the rune count after decode, not the byte count. We assert on
// runes here.
func TestServerInstructions_StaysUnder2048CharsIssue1019(t *testing.T) {
	const claudeCodeTruncationCeiling = 2048
	runes := utf8.RuneCountInString(serverInstructions)
	t.Logf("serverInstructions rune count: %d (byte count: %d)", runes, len(serverInstructions))
	if runes >= claudeCodeTruncationCeiling {
		t.Errorf("serverInstructions is %d runes (>=%d) — Claude Code will truncate and SIGINT the connection per issue #1019. Trim prose.",
			runes, claudeCodeTruncationCeiling)
	}
}
