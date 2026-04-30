package llm_test

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/llm"
)

// ─── A.3 tests ────────────────────────────────────────────────────────────────

// ObservationSnippet is used in tests to pass observation data to BuildPrompt.
// The real type lives in the llm package.

// TestBuildPrompt_HappyPath verifies the golden structure of a rendered prompt:
// both observation titles and contents appear in the output.
func TestBuildPrompt_HappyPath(t *testing.T) {
	a := llm.ObservationSnippet{
		ID:      "obs-aaa111",
		Title:   "We use JWT for auth token storage",
		Content: "Decided to use JWT tokens stored in httpOnly cookies for session management.",
		Type:    "decision",
		Project: "myapp",
	}
	b := llm.ObservationSnippet{
		ID:      "obs-bbb222",
		Title:   "Switched from sessions to JWT for auth",
		Content: "Replaced express-session with jsonwebtoken. Tokens are stateless.",
		Type:    "decision",
		Project: "myapp",
	}

	prompt := llm.BuildPrompt(a, b)

	if prompt == "" {
		t.Fatal("BuildPrompt returned empty string")
	}

	// Both IDs must appear in the prompt so the model can reference them.
	if !strings.Contains(prompt, a.ID) {
		t.Errorf("prompt missing observation A ID %q", a.ID)
	}
	if !strings.Contains(prompt, b.ID) {
		t.Errorf("prompt missing observation B ID %q", b.ID)
	}

	// Both titles must appear verbatim.
	if !strings.Contains(prompt, a.Title) {
		t.Errorf("prompt missing observation A title %q", a.Title)
	}
	if !strings.Contains(prompt, b.Title) {
		t.Errorf("prompt missing observation B title %q", b.Title)
	}

	// Both contents must appear verbatim.
	if !strings.Contains(prompt, a.Content) {
		t.Errorf("prompt missing observation A content %q", a.Content)
	}
	if !strings.Contains(prompt, b.Content) {
		t.Errorf("prompt missing observation B content %q", b.Content)
	}

	// The prompt must instruct the model to return a specific JSON shape.
	for _, keyword := range []string{"Relation", "Confidence", "Reasoning"} {
		if !strings.Contains(prompt, keyword) {
			t.Errorf("prompt missing JSON field instruction %q", keyword)
		}
	}

	// The prompt must enumerate the relation vocabulary so the model can choose.
	for _, verb := range []string{"conflicts_with", "supersedes", "scoped", "related", "compatible", "not_conflict"} {
		if !strings.Contains(prompt, verb) {
			t.Errorf("prompt missing relation verb %q", verb)
		}
	}
}

// TestBuildPrompt_EmptyContent verifies that BuildPrompt handles empty content
// without panicking and still produces a non-empty prompt string.
func TestBuildPrompt_EmptyContent(t *testing.T) {
	a := llm.ObservationSnippet{ID: "obs-x", Title: "Alpha title", Content: ""}
	b := llm.ObservationSnippet{ID: "obs-y", Title: "Beta title", Content: ""}

	prompt := llm.BuildPrompt(a, b)

	if prompt == "" {
		t.Fatal("BuildPrompt with empty content must not return empty string")
	}
	if !strings.Contains(prompt, "Alpha title") {
		t.Error("prompt must still contain observation A title when content is empty")
	}
	if !strings.Contains(prompt, "Beta title") {
		t.Error("prompt must still contain observation B title when content is empty")
	}
}

// TestBuildPrompt_SpecialChars verifies that special characters in titles and
// content are embedded verbatim (no escaping that would break the prompt).
func TestBuildPrompt_SpecialChars(t *testing.T) {
	a := llm.ObservationSnippet{
		ID:      "obs-sc1",
		Title:   `Auth token: "Bearer" <token> & more`,
		Content: `Use header Authorization: Bearer <jwt>\n with \" quotes.`,
	}
	b := llm.ObservationSnippet{
		ID:      "obs-sc2",
		Title:   "Simple title",
		Content: "Simple content.",
	}

	prompt := llm.BuildPrompt(a, b)

	if !strings.Contains(prompt, a.Title) {
		t.Errorf("prompt must contain special-char title verbatim; got:\n%s", prompt)
	}
	if !strings.Contains(prompt, a.Content) {
		t.Errorf("prompt must contain special-char content verbatim; got:\n%s", prompt)
	}
}

// TestBuildPrompt_IsString verifies the return type is a plain string (single value).
func TestBuildPrompt_IsString(t *testing.T) {
	a := llm.ObservationSnippet{ID: "obs-1", Title: "T1", Content: "C1"}
	b := llm.ObservationSnippet{ID: "obs-2", Title: "T2", Content: "C2"}

	result := llm.BuildPrompt(a, b)

	if len(result) == 0 {
		t.Error("BuildPrompt must return a non-empty string")
	}
}
