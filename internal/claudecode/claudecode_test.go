package claudecode

import (
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func TestSlugifyProjectName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"My Project", "C--My-Project"},
		{"AnitaChatBot-DrJorgeHara", "C--AnitaChatBot-DrJorgeHara"},
		{"CitaMedica Beta", "C--CitaMedica-Beta"},
		{"", "C--unknown"},
		{"Project   with   spaces", "C--Project-with-spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slugifyProjectName(tt.input)
			if got != tt.expected {
				t.Errorf("slugifyProjectName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestUnslugifyProjectName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"C--my-project", "my project"},
		{"C--anitachatbot-drjorgehara", "anitachatbot drjorgehara"},
		{"C--Users-JorgeHaraDevs-Desktop-My-Project", "my project"},
		{"C--unknown", "unknown"},
		{"C--My-Project", "my project"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := unslugifyProjectName(tt.input)
			if got != tt.expected {
				t.Errorf("unslugifyProjectName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestMemoryFileFormat(t *testing.T) {
	obs := store.Observation{
		ID:        123,
		Title:     "Test Memory",
		Content:   "This is the content of the test memory.",
		Type:      "decision",
		SessionID: "session-456",
		Project:   strPtr("my-project"),
	}

	formatted := MemoryFileFormat(obs)

	// Check frontmatter
	if !contains(formatted, "name: Test Memory") {
		t.Errorf("expected 'name: Test Memory' in output, got: %s", formatted)
	}
	if !contains(formatted, "type: decision") {
		t.Errorf("expected 'type: decision' in output, got: %s", formatted)
	}
	if !contains(formatted, "originSessionId: session-456") {
		t.Errorf("expected 'originSessionId: session-456' in output, got: %s", formatted)
	}
	if !contains(formatted, "project: my-project") {
		t.Errorf("expected 'project: my-project' in output, got: %s", formatted)
	}

	// Check content
	if !contains(formatted, "## Test Memory") {
		t.Errorf("expected '## Test Memory' in output, got: %s", formatted)
	}
	if !contains(formatted, "This is the content") {
		t.Errorf("expected content in output, got: %s", formatted)
	}
}

func TestGenerateFilename(t *testing.T) {
	tests := []struct {
		title    string
		expected string
	}{
		{"Fix auth bug", "project_fix_auth_bug.md"},
		{"JWT middleware implementation", "project_jwt_middleware_implementation.md"},
		{"ABC", "project_abc.md"},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			obs := store.Observation{Title: tt.title}
			got := generateFilename(obs)
			if got != tt.expected {
				t.Errorf("generateFilename(%q) = %q, want %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestEscapeYaml(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple text", "simple text"},
		{"text with \"quotes\"", `"text with \"quotes\""`},
		{"text\nwith\nnewlines", `"text with newlines"`},
		{"text: with colon", `"text: with colon"`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeYaml(tt.input)
			if got != tt.expected {
				t.Errorf("escapeYaml(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func strPtr(s string) *string {
	return &s
}
