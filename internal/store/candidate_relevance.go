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
			var sourceTerms []string
			if encodedTerms != "" {
				sourceTerms = strings.Split(encodedTerms, "\x1f")
			}
			var topic *string
			if value, ok := args[3].(string); ok {
				topic = &value
			}
			if saveCandidateRelevant(sourceTerms, sourceTopic, title, topic) {
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

// saveCandidateRelevant uses explicit topic identity or distinct title terms,
// not matches in observation content or corpus-dependent BM25 magnitude.
func saveCandidateRelevant(sourceTerms []string, sourceTopic, title string, topic *string) bool {
	if sourceTopic != "" && topic != nil && sourceTopic == *topic {
		return true
	}
	targetTerms := saveCandidateTerms(title)
	shared := 0
	for _, source := range sourceTerms {
		for _, target := range targetTerms {
			if source == target {
				shared++
				break
			}
		}
	}
	// A one-term title remains eligible only against an equivalent one-term title.
	return shared >= 2 || (shared == 1 && len(sourceTerms) == 1 && len(targetTerms) == 1)
}
