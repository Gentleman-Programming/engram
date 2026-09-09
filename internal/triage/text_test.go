package triage

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "lowercase and strip punctuation", input: "Crash on Save! (macOS)", want: []string{"crash", "on", "save", "macos"}},
		{name: "whitespace variants are separators", input: "  extra \t spaces\nand\tnewlines ", want: []string{"extra", "spaces", "and", "newlines"}},
		{name: "unicode letters stay whole", input: "señales de error", want: []string{"señales", "de", "error"}},
		{name: "digits bind to words", input: "e2e-test_fails", want: []string{"e2e", "test", "fails"}},
		{name: "underscores are separators", input: "some_name", want: []string{"some", "name"}},
		{name: "empty and symbol-only input", input: "!! ?", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeTokens(tt.input); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("NormalizeTokens(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSearchTokens(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  []string
	}{
		{
			name:  "prefers distinctive tokens, caps at five",
			title: "Crash when saving large notes on macOS",
			want:  []string{"crash", "saving", "large", "notes", "macos"},
		},
		{
			name:  "falls back to shorter non-stopword tokens",
			title: "Add a fix for the bug",
			want:  []string{"add", "fix", "bug"},
		},
		{
			name:  "no usable tokens means skip search",
			title: "a b c d",
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SearchTokens(tt.title); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SearchTokens(%q) = %v, want %v", tt.title, got, tt.want)
			}
		})
	}
}

func TestExtractSnippets(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "inline backtick span",
			body: "fails with `bind: address already in use` every time",
			want: []string{"bind: address already in use"},
		},
		{
			name: "fenced block content, language tag dropped",
			body: "log:\n```\npanic: runtime error\n```\nend",
			want: []string{"panic: runtime error"},
		},
		{
			name: "deduplicates repeated snippets",
			body: "`dup snippet` and again `dup snippet`",
			want: []string{"dup snippet"},
		},
		{
			name: "ignores too-short spans",
			body: "`ab` is not evidence",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractSnippets(tt.body); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("extractSnippets(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

func TestJaccard(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want float64
	}{
		{name: "identical sets", a: []string{"x", "y"}, b: []string{"y", "x"}, want: 1},
		{name: "disjoint sets", a: []string{"x"}, b: []string{"y"}, want: 0},
		{name: "half overlap", a: []string{"a", "b", "c"}, b: []string{"b", "c", "d"}, want: 0.5},
		{name: "empty side", a: nil, b: []string{"b"}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jaccard(tt.a, tt.b); got != tt.want {
				t.Fatalf("jaccard(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestDistinctiveTokens(t *testing.T) {
	got := distinctiveTokens([]string{"app", "crashes", "when", "saving", "crashes", "large"})
	want := []string{"crashes", "saving", "large"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("distinctiveTokens = %v, want %v", got, want)
	}
}

// The stopwords map itself lives in text.go next to the scoring helpers.
func TestStopwordsCoverEnglishAndSpanish(t *testing.T) {
	for _, word := range []string{"with", "when", "como", "para", "porque"} {
		if !stopwords[word] {
			t.Errorf("expected %q in the stopword list", word)
		}
	}
	if stopwords["crash"] {
		t.Error("crash must not be a stopword")
	}
	// The list must stay small: it only guards distinctive-term scoring.
	if len(stopwords) > 150 {
		t.Errorf("stopword list has %d entries; keep it small", len(stopwords))
	}
}

func TestStopwordsAreLowercaseSingleWords(t *testing.T) {
	for word := range stopwords {
		if word == "" || strings.ContainsAny(word, " \t\n'") || strings.ToLower(word) != word {
			t.Errorf("stopword %q must be a lowercase single word", word)
		}
	}
}
