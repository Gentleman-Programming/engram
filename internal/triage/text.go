package triage

import (
	"regexp"
	"strings"
	"unicode"
)

// inlineSnippetPattern matches single-backtick inline spans on one line;
// fenced ``` markers cannot match it because their backticks are adjacent.
var inlineSnippetPattern = regexp.MustCompile("`([^`\n]+)`")

// fencedSnippetPattern matches ``` fenced blocks, dropping the language tag.
var fencedSnippetPattern = regexp.MustCompile("(?s)```[^\n]*\n(.*?)```")

// stopwords is the small built-in English/Spanish filler-word list used to
// keep distinctive-term scoring meaningful. Deliberately lowercase single
// words only.
var stopwords = map[string]bool{
	"about": true, "above": true, "after": true, "again": true, "against": true,
	"all": true, "and": true, "any": true, "been": true, "before": true,
	"being": true, "below": true, "between": true, "both": true, "but": true,
	"cannot": true, "could": true, "did": true, "doing": true, "doesn": true,
	"down": true, "during": true, "each": true, "for": true, "from": true,
	"further": true, "had": true, "has": true, "have": true, "having": true,
	"her": true, "here": true, "hers": true, "himself": true, "his": true,
	"how": true, "into": true, "its": true, "itself": true, "just": true,
	"more": true, "most": true, "nor": true, "not": true, "once": true,
	"only": true, "other": true, "our": true, "over": true, "own": true,
	"same": true, "should": true, "she": true, "some": true, "such": true,
	"than": true, "that": true, "the": true, "theirs": true, "them": true,
	"then": true, "there": true, "these": true, "they": true, "this": true,
	"those": true, "through": true, "too": true, "under": true, "until": true,
	"very": true, "want": true, "wants": true, "when": true, "where": true,
	"which": true, "while": true, "who": true, "why": true, "will": true,
	"with": true, "would": true, "yes": true, "you": true, "your": true,
	"yours": true,
	"como":  true, "con": true, "cuando": true, "donde": true, "desde": true,
	"del": true, "entre": true, "esta": true, "estas": true, "este": true,
	"esto": true, "estos": true, "hasta": true, "las": true, "los": true,
	"mas": true, "más": true, "muy": true, "para": true, "pero": true,
	"porque": true, "por": true, "ser": true, "sin": true, "solo": true,
	"son": true, "sus": true, "todos": true, "todas": true, "también": true,
	"una": true, "uno": true, "unos": true, "unas": true, "fue": true,
	"que": true,
}

// minSnippetRunes is the shortest backticked span counted as evidence.
const minSnippetRunes = 3

// NormalizeTokens lowercases s and splits it into maximal runs of unicode
// letters and digits. Every other rune — punctuation, symbols, whitespace,
// underscores — acts as a separator. Order is preserved and duplicates are
// kept.
func NormalizeTokens(s string) []string {
	var tokens []string
	var current []rune
	for _, r := range toLower(s) {
		if isWordRune(r) {
			current = append(current, r)
			continue
		}
		if len(current) > 0 {
			tokens = append(tokens, string(current))
			current = nil
		}
	}
	if len(current) > 0 {
		tokens = append(tokens, string(current))
	}
	return tokens
}

// SearchTokens returns the normalized title tokens used to build the GitHub
// issue-search query: up to 5 distinctive tokens (rune length >= 4, not
// stopwords), falling back to shorter non-stopword tokens when the title has
// no distinctive ones. An empty result means search must be skipped.
func SearchTokens(title string) []string {
	normalized := NormalizeTokens(title)
	if tokens := firstDistinctive(normalized, 5); len(tokens) > 0 {
		return tokens
	}
	// Fallback: shorter non-stopword tokens still narrow the repo-wide search.
	var fallback []string
	seen := make(map[string]bool)
	for _, token := range normalized {
		if len([]rune(token)) < 3 || stopwords[token] || seen[token] {
			continue
		}
		seen[token] = true
		fallback = append(fallback, token)
		if len(fallback) == 5 {
			break
		}
	}
	return fallback
}

// distinctiveTokens filters tokens to rune length >= 4 non-stopwords,
// deduplicated, preserving first-appearance order.
func distinctiveTokens(tokens []string) []string {
	return firstDistinctive(tokens, 0)
}

// firstDistinctive is distinctiveTokens with an optional cap; cap <= 0 means
// unlimited.
func firstDistinctive(tokens []string, cap int) []string {
	var result []string
	seen := make(map[string]bool)
	for _, token := range tokens {
		if len([]rune(token)) < 4 || stopwords[token] || seen[token] {
			continue
		}
		seen[token] = true
		result = append(result, token)
		if cap > 0 && len(result) == cap {
			break
		}
	}
	return result
}

// extractSnippets returns backticked code/error snippets from an issue body:
// inline `span` snippets plus fenced ``` block contents, deduplicated, each at
// least minSnippetRunes long.
func extractSnippets(body string) []string {
	var snippets []string
	seen := make(map[string]bool)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if len([]rune(s)) < minSnippetRunes || seen[s] {
			return
		}
		seen[s] = true
		snippets = append(snippets, s)
	}
	for _, match := range inlineSnippetPattern.FindAllStringSubmatch(body, -1) {
		add(match[1])
	}
	for _, match := range fencedSnippetPattern.FindAllStringSubmatch(body, -1) {
		add(match[1])
	}
	return snippets
}

// jaccard returns |A∩B| / |A∪B| over the two token sets; 0 when either set is
// empty.
func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	setA := make(map[string]bool, len(a))
	for _, token := range a {
		setA[token] = true
	}
	setB := make(map[string]bool, len(b))
	for _, token := range b {
		setB[token] = true
	}
	intersection := 0
	for token := range setA {
		if setB[token] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	return float64(intersection) / float64(union)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func toLower(s string) string {
	return strings.ToLower(s)
}
