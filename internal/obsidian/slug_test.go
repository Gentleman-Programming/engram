package obsidian

import "testing"

func TestSlugify(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		id       int64
		expected string
	}{
		{
			name:     "simple title",
			title:    "Fixed FTS5 syntax error",
			id:       1401,
			expected: "fixed-fts5-syntax-error-1401",
		},
		{
			name:     "title with colon and mixed case",
			title:    "Architecture Proposal: obsidian-plugin",
			id:       1720,
			expected: "architecture-proposal-obsidian-plugin-1720",
		},
		{
			name:     "empty title falls back to observation-{id}",
			title:    "",
			id:       42,
			expected: "observation-42",
		},
		{
			name:     "title longer than 60 chars is truncated",
			title:    "This is a very long title that exceeds sixty characters limit by a lot",
			id:       99,
			expected: "this-is-a-very-long-title-that-exceeds-sixty-characters-limi-99",
		},
		{
			name:     "collision by ID — same prefix different IDs",
			title:    "Fixed authentication",
			id:       1,
			expected: "fixed-authentication-1",
		},
		{
			name:     "collision by ID — same prefix different IDs (id=2)",
			title:    "Fixed authentication",
			id:       2,
			expected: "fixed-authentication-2",
		},
		{
			name:     "unicode characters replaced with hyphens",
			title:    "Lösung für das Problem",
			id:       7,
			expected: "l-sung-f-r-das-problem-7",
		},
		{
			name:     "special characters only become hyphens, then trimmed",
			title:    "!!! Hello World !!!",
			id:       5,
			expected: "hello-world-5",
		},
		{
			name:     "numbers preserved in slug",
			title:    "Fix bug #42 in v2.0",
			id:       10,
			expected: "fix-bug-42-in-v2-0-10",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Slugify(tc.title, tc.id)
			if got != tc.expected {
				t.Errorf("Slugify(%q, %d) = %q; want %q", tc.title, tc.id, got, tc.expected)
			}
		})
	}
}
