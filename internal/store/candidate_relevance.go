package store

import (
	"database/sql/driver"
	"sort"
	"strings"
	"unicode"

	sqlite "modernc.org/sqlite"
)

func init() {
	// Register once before any store connections open. Both SQL and Go use
	// the same deterministic rule; there is no per-store/global mutable state.
	if err := sqlite.RegisterDeterministicScalarFunction("engram_save_candidate_relevant", 4,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			encodedTerms, _ := args[0].(string)
			sourceTopic, _ := args[1].(string)
			title, _ := args[2].(string)
			var topic *string
			if value, ok := args[3].(string); ok {
				topic = &value
			}
			if saveCandidateRelevant(encodedTerms, sourceTopic, title, topic) {
				return int64(1), nil
			}
			return int64(0), nil
		}); err != nil {
		panic(err)
	}
}

// saveCandidateTerms normalizes title words for the opt-in save relevance gate.
// Unlike candidateTerms, it is not used by broad-recall scans. Common English
// connective words and generic change verbs are not evidence of a shared topic.
func saveCandidateTerms(title string) []string {
	terms := make(map[string]struct{})
	for _, term := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' && r != '#'
	}) {
		// Technical identifiers retain +/# (C++ and C# are distinct). Bare
		// punctuation alone is not evidence of a shared subject.
		if strings.IndexFunc(term, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 {
			continue
		}
		switch term {
		case "a", "an", "the", "and", "or", "for", "to", "of", "in", "on", "with", "from", "by", "at", "as", "is", "are", "be", "was", "were", "we", "it", "this", "that", "use", "used", "using", "add", "added", "update", "updated", "fix", "fixed", "implement", "implemented":
			continue
		}
		terms[term] = struct{}{}
	}
	result := make([]string, 0, len(terms))
	for term := range terms {
		result = append(result, term)
	}
	sort.Strings(result)
	return result
}

// saveCandidateRelevant uses explicit topic identity or distinct title terms.
// encodedTerms contains the normalized, deduplicated source terms produced by
// saveCandidateTerms, separated by the unit separator used by FindCandidates.
func saveCandidateRelevant(encodedTerms, sourceTopic, title string, topic *string) bool {
	if sourceTopic != "" && topic != nil && sourceTopic == *topic {
		return true
	}
	if encodedTerms == "" {
		return false
	}
	if !strings.Contains(encodedTerms, "\x1f") {
		// Keep the single-term equivalence rule, including ignored words and repeats.
		targetTerms := saveCandidateTerms(title)
		return len(targetTerms) == 1 && targetTerms[0] == encodedTerms
	}
	// A match against normalized source terms cannot be a stop word or bare
	// punctuation. Remember the first match so repeated target words count once.
	first := ""
	for target := range strings.FieldsFuncSeq(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' && r != '#'
	}) {
		if target == first {
			continue
		}
		if saveCandidateEncodingContains(encodedTerms, target) {
			if first != "" {
				return true
			}
			first = target
		}
	}
	return false
}

// saveCandidateEncodingContains requires complete terms, not substrings of one.
// Both the encoding and term use the unchanged significant-word normalization.
func saveCandidateEncodingContains(encoded, term string) bool {
	for offset := 0; offset < len(encoded); {
		index := strings.Index(encoded[offset:], term)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(term)
		if (start == 0 || encoded[start-1] == '\x1f') && (end == len(encoded) || encoded[end] == '\x1f') {
			return true
		}
		separator := strings.IndexByte(encoded[end:], '\x1f')
		if separator < 0 {
			return false
		}
		// A partial match cannot become a complete match inside this same word.
		offset = end + separator + 1
	}
	return false
}
