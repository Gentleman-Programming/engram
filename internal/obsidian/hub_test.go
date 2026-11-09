package obsidian

import (
	"strings"
	"testing"
)

func TestSessionHub(t *testing.T) {
	t.Run("session hub contains backlinks for all observations", func(t *testing.T) {
		refs := []ObsRef{
			{Slug: "fixed-auth-bug-1", Title: "Fixed auth bug", Type: "bugfix"},
			{Slug: "architecture-proposal-obsidian-2", Title: "Architecture Proposal: Obsidian", Type: "architecture"},
		}

		got := SessionHubMarkdown("sess-42", refs)

		// Must have YAML frontmatter
		if !strings.HasPrefix(got, "---\n") {
			t.Errorf("expected SessionHubMarkdown to start with YAML frontmatter")
		}

		// Frontmatter must specify type: session-hub
		if !strings.Contains(got, "type: session-hub") {
			t.Errorf("frontmatter must contain 'type: session-hub'")
		}

		// Must have H1 heading with session ID
		if !strings.Contains(got, "# Session: sess-42") {
			t.Errorf("expected H1 heading 'Session: sess-42'")
		}

		// Must contain both wikilinks
		if !strings.Contains(got, "[[fixed-auth-bug-1]]") {
			t.Errorf("expected wikilink [[fixed-auth-bug-1]] in session hub")
		}
		if !strings.Contains(got, "[[architecture-proposal-obsidian-2]]") {
			t.Errorf("expected wikilink [[architecture-proposal-obsidian-2]] in session hub")
		}
	})

	t.Run("session hub with single observation still renders", func(t *testing.T) {
		refs := []ObsRef{
			{Slug: "only-obs-99", Title: "Only observation", Type: "decision"},
		}

		got := SessionHubMarkdown("sess-01", refs)

		if !strings.Contains(got, "[[only-obs-99]]") {
			t.Errorf("expected wikilink [[only-obs-99]] in session hub")
		}
		if !strings.Contains(got, "# Session: sess-01") {
			t.Errorf("expected H1 heading 'Session: sess-01'")
		}
	})
}

func TestTopicHub(t *testing.T) {
	t.Run("topic hub with two observations lists both wikilinks", func(t *testing.T) {
		// The observations share a topic prefix, so a hub is generated.
		refs := []ObsRef{
			{Slug: "architecture-analysis-obs-1", Title: "Architecture Analysis", Type: "architecture", TopicKey: "architecture/analysis"},
			{Slug: "architecture-design-obs-2", Title: "Architecture Design", Type: "architecture", TopicKey: "architecture/design"},
		}

		got := TopicHubMarkdown("architecture", refs)

		// Must have YAML frontmatter with type: topic-hub
		if !strings.HasPrefix(got, "---\n") {
			t.Errorf("expected TopicHubMarkdown to start with YAML frontmatter")
		}
		if !strings.Contains(got, "type: topic-hub") {
			t.Errorf("frontmatter must contain 'type: topic-hub'")
		}

		// Must have H1 heading with topic prefix
		if !strings.Contains(got, "# Topic: architecture") {
			t.Errorf("expected H1 heading 'Topic: architecture'")
		}

		// Must contain both wikilinks
		if !strings.Contains(got, "[[architecture-analysis-obs-1]]") {
			t.Errorf("expected wikilink [[architecture-analysis-obs-1]] in topic hub")
		}
		if !strings.Contains(got, "[[architecture-design-obs-2]]") {
			t.Errorf("expected wikilink [[architecture-design-obs-2]] in topic hub")
		}
	})

	t.Run("topic hub shows type annotation in the observation list", func(t *testing.T) {
		refs := []ObsRef{
			{Slug: "analysis-obs-1", Title: "Analysis", Type: "architecture", TopicKey: "architecture/analysis"},
			{Slug: "decision-obs-2", Title: "Decision", Type: "decision", TopicKey: "architecture/decision"},
		}

		got := TopicHubMarkdown("architecture", refs)

		// Design template shows: - [[slug]] (type)
		if !strings.Contains(got, "(architecture)") {
			t.Errorf("expected type annotation '(architecture)' in topic hub")
		}
		if !strings.Contains(got, "(decision)") {
			t.Errorf("expected type annotation '(decision)' in topic hub")
		}
	})
}

func TestTopicHubSkipped(t *testing.T) {
	// This test verifies the THRESHOLD RULE from the caller's perspective:
	// TopicHubMarkdown is only called when ≥2 obs share a prefix.
	// The function itself always renders — the threshold is enforced by the exporter.
	// Here we verify that with exactly 1 ref, the function still returns a valid hub
	// (the SKIP decision is the exporter's responsibility, not the hub generator's).
	// We test the "caller should skip" contract via a helper or documented behavior.

	t.Run("TopicHubMarkdown renders even with 1 observation (caller enforces threshold)", func(t *testing.T) {
		refs := []ObsRef{
			{Slug: "auth-jwt-1", Title: "Auth JWT", Type: "architecture", TopicKey: "auth/jwt"},
		}

		got := TopicHubMarkdown("auth", refs)

		// The function renders even for 1 — caller is responsible for not calling it
		if !strings.Contains(got, "[[auth-jwt-1]]") {
			t.Errorf("expected wikilink [[auth-jwt-1]] in the single-obs hub")
		}
	})

	t.Run("ShouldCreateTopicHub returns false for singleton prefix", func(t *testing.T) {
		// REQ-EXPORT-05: Only create hub when ≥2 observations share the same prefix
		counts := map[string]int{
			"auth":         1, // singleton — no hub
			"architecture": 3, // ≥2 — hub should be created
		}

		if ShouldCreateTopicHub(counts["auth"]) {
			t.Errorf("ShouldCreateTopicHub(1) must return false — singleton prefix")
		}
		if !ShouldCreateTopicHub(counts["architecture"]) {
			t.Errorf("ShouldCreateTopicHub(3) must return true — ≥2 observations")
		}
	})
}
