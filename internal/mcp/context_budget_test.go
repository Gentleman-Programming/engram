package mcp

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// ─── mem_context budget tests (issue #1039) ─────────────────────────────────
//
// handleContext must not use the unbounded legacy FormatContext path. It has
// to route through FormatContextWithOptions with the #1012 hook defaults
// (16 KiB byte budget, 20 pinned rows) plus the optional max_bytes/compact
// tool arguments. Every case compares the handler's rendered context against
// a direct store call using the exact options the handler is expected to
// pass, so a regression back to FormatContext fails loudly.
//
// Byte-budget constants are deliberately spelled as literals here (not the
// implementation constants) so the tests pin the public contract
// independently of how mcp.go names it.

// budgetFiller returns 290 two-byte runes (580 bytes). Observation bullets
// preview 300 runes, so multibyte filler makes each bullet ~620 rendered
// bytes and the capped sections (20 pinned + 20 recent observations) exceed
// the 16 KiB default budget — exercising limitContextBytes deterministically.
// ASCII filler of the same size cannot: 40 bullets × ~330 bytes ≈ 13 KiB,
// which never truncates under the 16 KiB default.
func budgetFiller() string {
	return strings.Repeat("é", 290)
}

// budgetDataset seeds the standard budget-test dataset: 120 pinned
// observations (the issue's pin-count stress shape), 25 unpinned
// observations (20 render under the legacy MaxContextResults default), and
// 10 user prompts, all under session "s-budget" in project "engram".
// Pre-cap rendering is ~28 KiB, so every default-budget case truncates while
// the 64 KiB ceiling case does not.
func budgetDataset(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.CreateSession("s-budget", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	filler := budgetFiller()
	for i := 0; i < 120; i++ {
		title := fmt.Sprintf("pinned-%03d", i)
		id, err := s.AddObservation(store.AddObservationParams{
			SessionID: "s-budget",
			Type:      "note",
			Title:     title,
			Content:   title + " " + filler,
			Project:   "engram",
		})
		if err != nil {
			t.Fatalf("add pinned observation %d: %v", i, err)
		}
		if err := s.PinObservation(id); err != nil {
			t.Fatalf("pin observation %d: %v", i, err)
		}
	}
	for i := 0; i < 25; i++ {
		title := fmt.Sprintf("recent-%03d", i)
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "s-budget",
			Type:      "note",
			Title:     title,
			Content:   title + " " + filler,
			Project:   "engram",
		}); err != nil {
			t.Fatalf("add recent observation %d: %v", i, err)
		}
	}
	for i := 0; i < 10; i++ {
		if _, err := s.AddPrompt(store.AddPromptParams{
			SessionID: "s-budget",
			Content:   strings.Repeat("é", 150),
			Project:   "engram",
		}); err != nil {
			t.Fatalf("add prompt %d: %v", i, err)
		}
	}
}

// budgetCallContext invokes the mem_context handler with args and returns the
// envelope's "result" string.
func budgetCallContext(t *testing.T, s *store.Store, args map[string]any) string {
	t.Helper()
	handler := handleContext(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	res, err := handler(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
	if err != nil {
		t.Fatalf("context handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected context error: %s", callResultText(t, res))
	}
	result, _ := callResultJSON(t, res)["result"].(string)
	if result == "" {
		t.Fatalf("context envelope result empty: %s", callResultText(t, res))
	}
	return result
}

// budgetContextPart strips the "\n---\nMemory stats: ..." suffix (and any
// nudge appended after it), leaving exactly the store-rendered context block.
func budgetContextPart(t *testing.T, result string) string {
	t.Helper()
	i := strings.Index(result, "\n---\nMemory stats:")
	if i < 0 {
		t.Fatalf("context result missing stats separator:\n%.300s", result)
	}
	return result[:i]
}

// budgetSection returns the body of the "### <name>" markdown section,
// stopping at the next section header.
func budgetSection(t *testing.T, context, name string) string {
	t.Helper()
	header := "### " + name + "\n"
	start := strings.Index(context, header)
	if start < 0 {
		t.Fatalf("section %q not found in context:\n%.500s", name, context)
	}
	body := context[start+len(header):]
	if end := strings.Index(body, "\n### "); end >= 0 {
		body = body[:end+1]
	}
	return body
}

// budgetStatsSuffix renders the exact stats suffix the handler appends after
// the context block (fresh activity produces no nudge in these tests).
func budgetStatsSuffix(t *testing.T, s *store.Store) string {
	t.Helper()
	stats, err := loadContextStats(s)
	if err != nil {
		t.Fatalf("loadContextStats: %v", err)
	}
	return fmt.Sprintf("\n---\nMemory stats: %d sessions, %d observations across projects: %s",
		stats.TotalSessions, stats.TotalObservations, formatContextProjects(stats.Projects))
}

// budgetWant renders the direct store call the handler output must match.
func budgetWant(t *testing.T, s *store.Store, opts store.ContextOptions) string {
	t.Helper()
	want, err := s.FormatContextWithOptions("engram", "", opts)
	if err != nil {
		t.Fatalf("direct FormatContextWithOptions call: %v", err)
	}
	return want
}

func TestMemContextBudgetDefaultBindsOutput(t *testing.T) {
	s := newMCPTestStore(t)
	budgetDataset(t, s)

	got := budgetCallContext(t, s, map[string]any{"project": "engram"})
	suffix := budgetStatsSuffix(t, s)
	want := budgetWant(t, s, store.ContextOptions{MaxBytes: 16*1024 - len(suffix), Pinned: 20})

	if got != want+suffix {
		t.Fatalf("complete result must equal the suffix-reserved context plus the stats suffix: got %d bytes, want %d bytes", len(got), len(want)+len(suffix))
	}
	if len(got) > 16*1024 {
		t.Fatalf("complete default-bounded result = %d bytes, want <= %d", len(got), 16*1024)
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("expected truncation marker in default-bounded result")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("default-bounded result is not valid UTF-8")
	}
	if !strings.Contains(got, "Memory stats:") {
		t.Fatalf("stats suffix must survive inside the default budget")
	}
}

func TestMemContextBudgetPinnedCap(t *testing.T) {
	s := newMCPTestStore(t)
	budgetDataset(t, s)

	got := budgetContextPart(t, budgetCallContext(t, s, map[string]any{
		"project":   "engram",
		"max_bytes": 1e12, // clamps to the 64 KiB ceiling: full render, no truncation
	}))
	pinned := budgetSection(t, got, "Pinned")
	if n := strings.Count(pinned, "- ["); n != 20 {
		t.Fatalf("pinned bullets = %d, want 20", n)
	}
	// pinnedObservationsLimit orders by datetime(created_at) DESC, id DESC.
	// Ids follow insertion order, so the top 20 are deterministic:
	// pinned-119 (newest) down to pinned-100 (cap boundary).
	lines := strings.Split(strings.TrimSpace(pinned), "\n")
	if len(lines) != 20 {
		t.Fatalf("pinned section lines = %d, want 20", len(lines))
	}
	if !strings.HasPrefix(lines[0], "- [note] **pinned-119**") {
		t.Fatalf("first pinned bullet = %q, want pinned-119 (newest first)", lines[0])
	}
	if !strings.HasPrefix(lines[19], "- [note] **pinned-100**") {
		t.Fatalf("last pinned bullet = %q, want pinned-100 (cap boundary)", lines[19])
	}
}

func TestMemContextBudgetMaxBytesParamHonored(t *testing.T) {
	s := newMCPTestStore(t)
	budgetDataset(t, s)

	got := budgetCallContext(t, s, map[string]any{"project": "engram", "max_bytes": 1024.0})

	if len(got) > 1024 {
		t.Fatalf("max_bytes=1024 complete result = %d bytes, want <= 1024", len(got))
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("max_bytes=1024 should truncate the ~28 KiB dataset")
	}
	if !strings.HasPrefix(got, "## Memory from Previous Sessions") {
		t.Fatalf("max_bytes=1024 result should still open with the context header")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("max_bytes=1024 result is not valid UTF-8")
	}
}

func TestMemContextBudgetCeilingClamp(t *testing.T) {
	s := newMCPTestStore(t)
	budgetDataset(t, s)

	got := budgetContextPart(t, budgetCallContext(t, s, map[string]any{"project": "engram", "max_bytes": 1e12}))
	want := budgetWant(t, s, store.ContextOptions{MaxBytes: 64 * 1024, Pinned: 20})

	if got != want {
		t.Fatalf("max_bytes above ceiling must clamp to 64 KiB: got %d bytes, want %d bytes", len(got), len(want))
	}
	if strings.Contains(want, "[truncated]") {
		t.Fatalf("dataset should fit under the 64 KiB ceiling; got truncated output")
	}
}

func TestMemContextBudgetNonPositiveFallsBackToDefault(t *testing.T) {
	for name, value := range map[string]float64{"zero": 0.0, "negative": -5.0} {
		t.Run(name, func(t *testing.T) {
			s := newMCPTestStore(t)
			budgetDataset(t, s)

			got := budgetCallContext(t, s, map[string]any{"project": "engram", "max_bytes": value})
			suffix := budgetStatsSuffix(t, s)
			want := budgetWant(t, s, store.ContextOptions{MaxBytes: 16*1024 - len(suffix), Pinned: 20})

			if got != want+suffix {
				t.Fatalf("max_bytes=%v must fall back to the bounded default: got %d bytes, want %d bytes", value, len(got), len(want)+len(suffix))
			}
			if len(got) > 16*1024 || !strings.Contains(got, "[truncated]") {
				t.Fatalf("default fallback should stay bounded and truncate the ~28 KiB dataset (len=%d)", len(got))
			}
		})
	}
}

func TestMemContextBudgetCompactParam(t *testing.T) {
	s := newMCPTestStore(t)
	budgetDataset(t, s)

	got := budgetContextPart(t, budgetCallContext(t, s, map[string]any{"project": "engram", "compact": true}))
	want := budgetWant(t, s, store.ContextOptions{MaxBytes: 16 * 1024, Pinned: 20, Compact: true})

	if got != want {
		t.Fatalf("compact=true not honored: got %d bytes, want %d bytes", len(got), len(want))
	}
	// Compact bullets render "- [type] **title**" with no ": body" preview.
	lines := strings.Split(strings.TrimSpace(budgetSection(t, got, "Pinned")), "\n")
	if len(lines) == 0 {
		t.Fatalf("pinned section empty under compact rendering")
	}
	if lines[0] != "- [note] **pinned-119**" {
		t.Fatalf("compact pinned bullet = %q, want %q", lines[0], "- [note] **pinned-119**")
	}
}

// TestMemContextBudgetFractionalAndMistypedMaxBytes pins the resolver's
// never-unbounded guarantee for the invalid input classes: fractional values
// (including 1.5, which must fall back rather than int-truncate to 1 —
// CodeRabbit minor on PR #1074), positive fractions below 1 byte (int
// truncation must never reach the store as MaxBytes=0, the unbounded legacy
// sentinel), NaN, and mistyped (non-float64) values. All must render the
// default-bounded result. The comparison is direct equality on the complete
// result: when the context alone fills the budget, the final total clamp
// strips exactly the stats suffix, leaving the already-marked context.
func TestMemContextBudgetSubIntegerAndMistypedMaxBytes(t *testing.T) {
	for name, value := range map[string]float64{
		"fraction-below-one": 0.5,
		"fraction-above-one": 1.5,
		"fraction-large":     2048.75,
		"nan":                math.NaN(),
	} {
		t.Run(name, func(t *testing.T) {
			s := newMCPTestStore(t)
			budgetDataset(t, s)

			got := budgetCallContext(t, s, map[string]any{"project": "engram", "max_bytes": value})
			if len(got) > 16*1024 {
				t.Fatalf("fractional/NaN max_bytes must fall back to the bounded default: got %d bytes", len(got))
			}
			if !strings.Contains(got, "[truncated]") || !strings.HasPrefix(got, "## Memory from Previous Sessions") {
				t.Fatalf("fallback result must be a truncated context (len=%d)", len(got))
			}
			if !utf8.ValidString(got) {
				t.Fatalf("fallback result is not valid UTF-8")
			}
		})
	}
	t.Run("mistyped-string", func(t *testing.T) {
		s := newMCPTestStore(t)
		budgetDataset(t, s)

		got := budgetCallContext(t, s, map[string]any{"project": "engram", "max_bytes": "1024"})
		if len(got) > 16*1024 || !strings.Contains(got, "[truncated]") {
			t.Fatalf("mistyped max_bytes must fall back to the bounded truncated default: got %d bytes", len(got))
		}
	})
}

// TestMemContextBudgetMinFloor pins the smallest valid explicit budget: an
// integral max_bytes=1 renders at most a single byte (the marker cannot fit,
// so the clamp degrades to a bare UTF-8-safe prefix cut).
func TestMemContextBudgetMinFloor(t *testing.T) {
	s := newMCPTestStore(t)
	budgetDataset(t, s)

	got := budgetCallContext(t, s, map[string]any{"project": "engram", "max_bytes": 1.0})
	if len(got) > 1 {
		t.Fatalf("max_bytes=1 result = %d bytes, want <= 1", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("max_bytes=1 result is not valid UTF-8")
	}
}

// TestMemContextBudgetCompactMistypedIsFalse documents the lenient parsing
// convention shared with project/scope: a non-boolean compact argument is
// ignored and renders the default (non-compact) context.
func TestMemContextBudgetCompactMistypedIsFalse(t *testing.T) {
	s := newMCPTestStore(t)
	budgetDataset(t, s)

	got := budgetCallContext(t, s, map[string]any{"project": "engram", "compact": "yes"})
	if len(got) > 16*1024 || !strings.Contains(got, "[truncated]") {
		t.Fatalf("mistyped compact must render the bounded default: got %d bytes", len(got))
	}
	if !strings.Contains(got, ": ") || strings.Count(got, "- [note] **") == 0 {
		t.Fatalf("mistyped compact must keep the non-compact preview rendering")
	}
}

// TestMemContextBudgetTotalResultBound pins the CodeRabbit major finding on
// PR #1074: the byte budget must apply to the COMPLETE mem_context result,
// including the "Memory stats" suffix that joins every project name in the
// store and the activity nudge. A store with many long project names must
// never push the tool result past the requested budget.
func TestMemContextBudgetTotalResultBound(t *testing.T) {
	s := newMCPTestStore(t)
	// Small memory dataset so the context block itself stays tiny; the bulk of
	// the result is the projects join in the stats suffix (~150 x ~120 chars).
	if err := s.CreateSession("s-projects", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-projects", Type: "note", Title: "tiny", Content: "tiny",
		Project: "engram",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	longName := strings.Repeat("proj", 30) // 120 chars
	for i := 0; i < 150; i++ {
		project := fmt.Sprintf("%s-%03d", longName, i)
		if err := s.CreateSession(fmt.Sprintf("s-proj-%03d", i), project, "/tmp/engram"); err != nil {
			t.Fatalf("create project session %d: %v", i, err)
		}
		// stats.Projects is derived from observations, not sessions.
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: fmt.Sprintf("s-proj-%03d", i), Type: "note",
			Title: "p", Content: "p", Project: project,
		}); err != nil {
			t.Fatalf("add project observation %d: %v", i, err)
		}
	}

	for name, args := range map[string]map[string]any{
		"default-budget":  {"project": "engram"},
		"explicit-budget": {"project": "engram", "max_bytes": 2048.0},
	} {
		t.Run(name, func(t *testing.T) {
			got := budgetCallContext(t, s, args)
			limit := 16 * 1024
			if args["max_bytes"] != nil {
				limit = 2048
			}
			if len(got) > limit {
				t.Fatalf("complete result = %d bytes, want <= %d (stats suffix must not escape the budget)", len(got), limit)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("complete result is not valid UTF-8")
			}
			if !strings.HasPrefix(got, "## Memory from Previous Sessions") {
				t.Fatalf("result should still open with the context header")
			}
			if !strings.Contains(got, "Memory stats:") || !strings.Contains(got, "+143 more") {
				t.Fatalf("stats suffix must survive with the capped projects join")
			}
			if strings.Contains(got, fmt.Sprintf("%s-008", longName)) {
				t.Fatalf("the 9th project name must be elided by the join cap")
			}
		})
	}
}

// TestMemContextBudgetNoMemoryMessageBounded pins the second CodeRabbit
// finding on PR #1074: the fixed no-context message is part of the complete
// mem_context result, so it must flow through the same clamp. A caller with
// max_bytes=1 and no matching memory gets a one-byte UTF-8-safe prefix of the
// message instead of the full 36-byte sentence, while the default budget
// keeps the message verbatim.
func TestMemContextBudgetNoMemoryMessageBounded(t *testing.T) {
	// A completely empty store keeps the explicit project override
	// unresolvable (overrides are validated against the store), so both cases
	// auto-detect like TestMCPAdditionalCoverageBranches does and reach the
	// fixed-message no-context branch with zero memories to render.
	t.Run("default-budget-verbatim", func(t *testing.T) {
		s := newMCPTestStore(t)

		got := budgetCallContext(t, s, nil)
		if got != "No previous session memories found." {
			t.Fatalf("no-memory default result = %q, want the verbatim message", got)
		}
	})
	t.Run("max-bytes-one", func(t *testing.T) {
		s := newMCPTestStore(t)

		got := budgetCallContext(t, s, map[string]any{"max_bytes": 1.0})
		if len(got) > 1 {
			t.Fatalf("max_bytes=1 no-memory result = %d bytes (%q), want <= 1", len(got), got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("max_bytes=1 no-memory result is not valid UTF-8")
		}
		if got != "N" {
			t.Fatalf("max_bytes=1 no-memory result = %q, want the exact one-byte prefix %q", got, "N")
		}
	})
}
