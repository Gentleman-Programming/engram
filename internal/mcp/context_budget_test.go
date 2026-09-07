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
	want := budgetWant(t, s, store.ContextOptions{MaxBytes: 16 * 1024, Pinned: 20})

	if !strings.HasPrefix(got, want) {
		t.Fatalf("handler result must start with the 16 KiB-bounded context; got %d-byte prefix mismatch (want %d bytes)", len(got), len(want))
	}
	if len(want) > 16*1024 {
		t.Fatalf("default-bounded context = %d bytes, want <= %d", len(want), 16*1024)
	}
	if !strings.Contains(want, "[truncated]") {
		t.Fatalf("expected truncation marker in default-bounded context")
	}
	if !utf8.ValidString(want) {
		t.Fatalf("default-bounded context is not valid UTF-8")
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

	got := budgetContextPart(t, budgetCallContext(t, s, map[string]any{"project": "engram", "max_bytes": 1024.0}))
	want := budgetWant(t, s, store.ContextOptions{MaxBytes: 1024, Pinned: 20})

	if got != want {
		t.Fatalf("max_bytes=1024 not honored: got %d bytes, want %d bytes", len(got), len(want))
	}
	if len(want) > 1024 {
		t.Fatalf("max_bytes=1024 context = %d bytes, want <= 1024", len(want))
	}
	if !utf8.ValidString(want) {
		t.Fatalf("max_bytes=1024 context is not valid UTF-8")
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

			got := budgetContextPart(t, budgetCallContext(t, s, map[string]any{"project": "engram", "max_bytes": value}))
			want := budgetWant(t, s, store.ContextOptions{MaxBytes: 16 * 1024, Pinned: 20})

			if got != want {
				t.Fatalf("max_bytes=%v must fall back to the 16 KiB default: got %d bytes, want %d bytes", value, len(got), len(want))
			}
			if !strings.Contains(want, "[truncated]") {
				t.Fatalf("default fallback should truncate the ~28 KiB dataset")
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

// TestMemContextBudgetSubIntegerAndMistypedMaxBytes pins the resolver's
// never-unbounded guarantee for the input classes the first review round
// flagged: positive fractions below 1 byte (int truncation must never reach
// the store as MaxBytes=0, the unbounded legacy sentinel), NaN, and mistyped
// (non-float64) values. All must render the default-bounded context.
func TestMemContextBudgetSubIntegerAndMistypedMaxBytes(t *testing.T) {
	mistyped := map[string]any{"project": "engram", "max_bytes": "1024"}
	nan := map[string]any{"project": "engram", "max_bytes": math.NaN()}
	fraction := map[string]any{"project": "engram", "max_bytes": 0.5}
	for name, args := range map[string]map[string]any{
		"fraction-below-one": fraction,
		"nan":                nan,
		"mistyped-string":    mistyped,
	} {
		t.Run(name, func(t *testing.T) {
			s := newMCPTestStore(t)
			budgetDataset(t, s)

			got := budgetContextPart(t, budgetCallContext(t, s, args))
			want := budgetWant(t, s, store.ContextOptions{MaxBytes: 16 * 1024, Pinned: 20})

			if got != want {
				t.Fatalf("must fall back to the bounded 16 KiB default: got %d bytes, want %d bytes", len(got), len(want))
			}
			if !strings.Contains(want, "[truncated]") {
				t.Fatalf("fallback should truncate the ~28 KiB dataset; got unbounded output (%d bytes)", len(want))
			}
		})
	}
}

// TestMemContextBudgetCompactMistypedIsFalse documents the lenient parsing
// convention shared with project/scope: a non-boolean compact argument is
// ignored and renders the default (non-compact) context.
func TestMemContextBudgetCompactMistypedIsFalse(t *testing.T) {
	s := newMCPTestStore(t)
	budgetDataset(t, s)

	got := budgetContextPart(t, budgetCallContext(t, s, map[string]any{"project": "engram", "compact": "yes"}))
	want := budgetWant(t, s, store.ContextOptions{MaxBytes: 16 * 1024, Pinned: 20})

	if got != want {
		t.Fatalf("mistyped compact must render the default non-compact context: got %d bytes, want %d bytes", len(got), len(want))
	}
	if !strings.Contains(budgetSection(t, got, "Pinned"), "- [note] **pinned-119**:") {
		t.Fatalf("mistyped compact must keep the non-compact preview rendering (colon + body)")
	}
}
