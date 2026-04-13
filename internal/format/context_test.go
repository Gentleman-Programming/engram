package format

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/types"
)

func strPtr(s string) *string { return &s }

func TestContext_EmptyInputs(t *testing.T) {
	result := Context(nil, nil, nil)
	if result != "" {
		t.Fatalf("expected empty string for empty inputs, got %q", result)
	}
}

func TestContext_WithSessions(t *testing.T) {
	sessions := []types.SessionSummary{
		{ID: "s1", Project: "engram", StartedAt: "2026-04-13", Summary: strPtr("Worked on auth"), ObservationCount: 5},
	}
	result := Context(sessions, nil, nil)
	if !strings.Contains(result, "### Recent Sessions") {
		t.Fatal("expected sessions header")
	}
	if !strings.Contains(result, "**engram**") {
		t.Fatal("expected project name")
	}
	if !strings.Contains(result, "Worked on auth") {
		t.Fatal("expected summary text")
	}
}

func TestContext_WithObservations(t *testing.T) {
	obs := []types.Observation{
		{Type: "decision", Title: "JWT auth", Content: "Switched to JWT tokens"},
	}
	result := Context(nil, obs, nil)
	if !strings.Contains(result, "### Recent Observations") {
		t.Fatal("expected observations header")
	}
	if !strings.Contains(result, "[decision] **JWT auth**") {
		t.Fatal("expected formatted observation")
	}
}

func TestContext_WithPrompts(t *testing.T) {
	prompts := []types.Prompt{
		{Content: "How does auth work?", CreatedAt: "2026-04-13"},
	}
	result := Context(nil, nil, prompts)
	if !strings.Contains(result, "### Recent User Prompts") {
		t.Fatal("expected prompts header")
	}
	if !strings.Contains(result, "How does auth work?") {
		t.Fatal("expected prompt content")
	}
}

func TestContext_FullOutput(t *testing.T) {
	sessions := []types.SessionSummary{
		{ID: "s1", Project: "engram", StartedAt: "2026-04-13", ObservationCount: 3},
	}
	obs := []types.Observation{
		{Type: "bugfix", Title: "Fixed N+1", Content: "Resolved query issue"},
	}
	prompts := []types.Prompt{
		{Content: "What changed?", CreatedAt: "2026-04-13"},
	}
	result := Context(sessions, obs, prompts)

	// All three sections should be present
	if !strings.Contains(result, "## Memory from Previous Sessions") {
		t.Fatal("expected main header")
	}
	if !strings.Contains(result, "### Recent Sessions") {
		t.Fatal("missing sessions section")
	}
	if !strings.Contains(result, "### Recent User Prompts") {
		t.Fatal("missing prompts section")
	}
	if !strings.Contains(result, "### Recent Observations") {
		t.Fatal("missing observations section")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		max      int
		expected string
	}{
		{"short", 10, "short"},
		{"long string here", 4, "long..."},
		{"", 5, ""},
		{"exactly5", 8, "exactly5"},
	}
	for _, tc := range tests {
		got := Truncate(tc.input, tc.max)
		if got != tc.expected {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.expected)
		}
	}
}
