package llm

import "fmt"

// ─── ObservationSnippet ────────────────────────────────────────────────────────

// ObservationSnippet carries the fields from an observation that are embedded
// into the comparison prompt. Callers should populate all fields for best
// LLM accuracy; empty fields are tolerated without panics.
type ObservationSnippet struct {
	// ID is the sync_id of the observation (e.g. "obs-a1b2c3d4...").
	ID string
	// Title is the observation title.
	Title string
	// Content is the observation body text.
	Content string
	// Type is the observation type label (e.g. "decision", "architecture").
	Type string
	// Project is the project the observation belongs to.
	Project string
}

// ─── Locked canonical prompt ───────────────────────────────────────────────────

// promptTemplate is the locked canonical prompt used by all AgentRunner
// implementations. It is intentionally frozen: changing this template changes
// the semantic meaning of stored verdicts and breaks cross-model comparison.
//
// Placeholders:
//
//	%[1]s  — observation A ID
//	%[2]s  — observation A Title
//	%[3]s  — observation A Content
//	%[4]s  — observation B ID
//	%[5]s  — observation B Title
//	%[6]s  — observation B Content
const promptTemplate = `You are a semantic memory auditor. Compare the two observations below and classify their relationship.

=== Observation A ===
ID: %[1]s
Title: %[2]s
Content: %[3]s

=== Observation B ===
ID: %[4]s
Title: %[5]s
Content: %[6]s

Choose EXACTLY ONE relation from this locked vocabulary:
- conflicts_with   — the observations make contradictory claims
- supersedes       — observation A replaces or overrides observation B (or vice versa)
- scoped           — one is a narrower instance of the other
- related          — they share a topic but do not conflict
- compatible       — they are consistent and complementary
- not_conflict     — they are unrelated; no meaningful semantic overlap

Respond with a single-line JSON object and nothing else:
{"Relation":"<verb>","Confidence":<0.0–1.0>,"Reasoning":"<≤200 chars>"}`

// BuildPrompt renders the locked canonical prompt for a pair of observations.
// The returned string is a single prompt ready to be passed to AgentRunner.Compare.
func BuildPrompt(a, b ObservationSnippet) string {
	return fmt.Sprintf(promptTemplate,
		a.ID, a.Title, a.Content,
		b.ID, b.Title, b.Content,
	)
}
