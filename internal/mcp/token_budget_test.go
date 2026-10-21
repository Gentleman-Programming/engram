package mcp

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	tools := eagerSchemaToolRunes(t)

	if *updateTokenBudgets {
		writeTokenBudgets(t, measured, tools)
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

	// A tool registered in the agent profile without a budget entry would
	// otherwise opt out of the ratchet silently: the aggregate could stay flat
	// while a new tool paid for itself with a smaller one, and nothing would
	// identify or acknowledge the new tool.
	for _, tool := range tools {
		entry := toolBudgetKey(tool.name)
		budget, ok := budgets[entry]
		if !ok {
			t.Errorf("agent tool %q is registered without a token budget entry (%q) — regenerate with: go test ./internal/mcp -run TestTokenBudget -update",
				tool.name, entry)
			continue
		}
		if tool.runes > budget {
			t.Errorf("agent tool %q is %d runes, over its pinned budget of %d — trim the tool or raise the budget deliberately",
				tool.name, tool.runes, budget)
		}
	}

	// The other direction: an entry for a tool that is no longer registered
	// would keep a stale budget line alive and hide the removal.
	for name := range budgets {
		if !strings.HasPrefix(name, toolBudgetPrefix) {
			continue
		}
		toolName := strings.TrimPrefix(name, toolBudgetPrefix)
		if _, ok := tools[toolName]; !ok {
			t.Errorf("token budget %q has no registered agent tool %q — regenerate with: go test ./internal/mcp -run TestTokenBudget -update",
				name, toolName)
		}
	}
}

// toolBudgetPrefix marks the per-tool budget entries in the baseline.
const toolBudgetPrefix = "eager_schemas.agent.tool."

// toolBudgetKey returns the baseline key of one agent tool.
func toolBudgetKey(toolName string) string { return toolBudgetPrefix + toolName }

// toolMeasurement is one registered agent tool and its current size.
type toolMeasurement struct {
	name  string
	runes int
}

// eagerSchemaToolRunes measures each registered agent tool on its own. The
// per-tool size is the tool definition as the SDK marshals it, which is what
// the tool contributes to the handshake payload; the aggregate above measures
// the payload as delivered, so the two do not have to add up exactly.
func eagerSchemaToolRunes(t *testing.T) map[string]toolMeasurement {
	t.Helper()

	srv := NewServerWithConfig(newMCPTestStore(t), MCPConfig{}, ProfileAgent)
	registered := srv.ListTools()
	tools := make([]mcppkg.Tool, 0, len(registered))
	for _, tool := range registered {
		tools = append(tools, tool.Tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	measured := make(map[string]toolMeasurement, len(tools))
	for _, tool := range tools {
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("marshal tool %s: %v", tool.Name, err)
		}
		measured[tool.Name] = toolMeasurement{name: tool.Name, runes: utf8.RuneCountInString(string(raw))}
	}
	return measured
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
func writeTokenBudgets(t *testing.T, measured []tokenMeasurement, tools map[string]toolMeasurement) {
	t.Helper()

	budgets := make(map[string]int, len(measured)+len(tools))
	for _, measurement := range measured {
		budgets[measurement.name] = measurement.runes
	}
	for _, tool := range tools {
		budgets[toolBudgetKey(tool.name)] = tool.runes
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
