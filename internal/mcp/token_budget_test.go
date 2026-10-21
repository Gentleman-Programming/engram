package mcp

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"unicode/utf8"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// updateTokenBudgets regenerates the pinned baseline from the current code.
// Both measurements are pure functions of the code, so the baseline is
// reproducible without a fixture, a network call or a manual audit.
var updateTokenBudgets = flag.Bool("update", false, "rewrite the token budget baseline from the current code")

// tokenBudgetsPath holds the pinned baseline. Its keys name the measured
// surface, and its values are the rune budget for that surface.
const tokenBudgetsPath = "testdata/token_budgets.json"

// tokenMeasurement is one measured surface and its current size.
type tokenMeasurement struct {
	name  string
	runes int
}

// TestTokenBudget pins the per-session cost of the two surfaces every agent
// session pays before any tool call: the eager tool schemas returned at
// handshake, and the server instructions.
//
// Today a field added to a response schema, a paragraph added to a tool
// description, or a tool moved into the agent profile costs a few hundred
// runes permanently, per session, with no CI signal. The budget is a
// ratchet: a decrease is always free, and an increase has to be deliberate.
//
// The client truncates the handshake instructions field at 2048 runes, so the
// ceiling above is a hard backstop, not the budget: growth up to the cliff is
// free today and must stop being free here.
func TestTokenBudget(t *testing.T) {
	measured := []tokenMeasurement{
		{"eager_schemas.agent", eagerSchemaRunes(t)},
		{"server_instructions.agent", utf8.RuneCountInString(buildServerInstructions(ProfileAgent))},
	}

	if *updateTokenBudgets {
		writeTokenBudgets(t, measured)
		return
	}

	budgets := readTokenBudgets(t)
	for _, measurement := range measured {
		budget, ok := budgets[measurement.name]
		if !ok {
			t.Errorf("token budget %q is not pinned; regenerate with: go test ./internal/mcp -run TestTokenBudget -update", measurement.name)
			continue
		}
		if measurement.runes > budget {
			t.Errorf("%s is %d runes, over the pinned budget of %d — trim the surface or raise the budget deliberately",
				measurement.name, measurement.runes, budget)
		}
	}
}

// eagerSchemaRunes measures the tools/list payload an agent session receives
// at handshake. The SDK sorts the tools it returns, so the measurement sorts
// them the same way to stay reproducible.
func eagerSchemaRunes(t *testing.T) int {
	t.Helper()

	srv := NewServerWithConfig(newMCPTestStore(t), MCPConfig{}, ProfileAgent)
	registered := srv.ListTools()
	tools := make([]mcppkg.Tool, 0, len(registered))
	for _, tool := range registered {
		tools = append(tools, tool.Tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	raw, err := json.Marshal(mcppkg.ListToolsResult{Tools: tools})
	if err != nil {
		t.Fatalf("marshal tools/list result: %v", err)
	}
	return utf8.RuneCountInString(string(raw))
}

// readTokenBudgets loads the pinned baseline.
func readTokenBudgets(t *testing.T) map[string]int {
	t.Helper()

	raw, err := os.ReadFile(tokenBudgetsPath)
	if err != nil {
		t.Fatalf("read %s: %v — pin it with: go test ./internal/mcp -run TestTokenBudget -update", tokenBudgetsPath, err)
	}
	budgets := map[string]int{}
	if err := json.Unmarshal(raw, &budgets); err != nil {
		t.Fatalf("parse %s: %v", tokenBudgetsPath, err)
	}
	return budgets
}

// writeTokenBudgets rewrites the pinned baseline from the current code.
func writeTokenBudgets(t *testing.T, measured []tokenMeasurement) {
	t.Helper()

	budgets := make(map[string]int, len(measured))
	for _, measurement := range measured {
		budgets[measurement.name] = measurement.runes
	}
	raw, err := json.MarshalIndent(budgets, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", tokenBudgetsPath, err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(tokenBudgetsPath), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(tokenBudgetsPath), err)
	}
	if err := os.WriteFile(tokenBudgetsPath, raw, 0o644); err != nil {
		t.Fatalf("write %s: %v", tokenBudgetsPath, err)
	}
	t.Logf("pinned %s: %s", tokenBudgetsPath, string(raw))
}

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
//
// The ceiling is the hard backstop above the pinned budget in
// TestTokenBudget: it can only fail where the budget already failed.
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
