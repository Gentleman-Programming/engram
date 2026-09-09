package triage

import "sort"

// Match is one candidate issue that passed the conservative thresholds,
// together with the evidence that qualified it.
type Match struct {
	Issue            Issue
	TitleSimilarity  float64 // Jaccard over normalized title token sets
	SharedTerms      []string
	MatchingSnippets []string
}

const (
	// titleSimilarityThreshold plus sharedDistinctiveMin form the primary
	// acceptance rule: clearly similar titles with repeated distinctive terms.
	titleSimilarityThreshold = 0.6
	sharedDistinctiveMin     = 2
	// snippetSimilarityFloor is the lower title-similarity bar accepted when
	// both bodies share an exact code/error snippet.
	snippetSimilarityFloor = 0.35
	// maxReportedCandidates caps how many candidates one comment lists.
	maxReportedCandidates = 3
)

// RankCandidates scores found issues against the target issue and returns the
// accepted matches: self (by number) and pull requests are excluded, the
// conservative thresholds decide acceptance, results are sorted by score
// (descending, issue number ascending as deterministic tie-break) and capped
// at maxReportedCandidates.
func RankCandidates(target Issue, found []Issue) []Match {
	targetTokens := NormalizeTokens(target.Title)
	targetSnippets := extractSnippets(target.Body)

	var matches []Match
	for _, candidate := range found {
		if candidate.Number == target.Number || candidate.PullRequest {
			continue
		}
		candidateTokens := NormalizeTokens(candidate.Title)
		similarity := jaccard(targetTokens, candidateTokens)
		shared := sharedDistinctiveTerms(targetTokens, candidateTokens)
		snippets := sharedSnippets(targetSnippets, candidate.Body)

		acceptedByTitle := similarity >= titleSimilarityThreshold && len(shared) >= sharedDistinctiveMin
		acceptedBySnippet := len(snippets) > 0 && similarity >= snippetSimilarityFloor
		if !acceptedByTitle && !acceptedBySnippet {
			continue
		}
		matches = append(matches, Match{
			Issue:            candidate,
			TitleSimilarity:  similarity,
			SharedTerms:      shared,
			MatchingSnippets: snippets,
		})
	}

	sort.Slice(matches, func(i, j int) bool {
		si, sj := matches[i].score(), matches[j].score()
		if si != sj {
			return si > sj
		}
		return matches[i].Issue.Number < matches[j].Issue.Number
	})
	if len(matches) > maxReportedCandidates {
		matches = matches[:maxReportedCandidates]
	}
	return matches
}

// score is a deterministic ordering heuristic only; it never decides
// acceptance.
func (m Match) score() float64 {
	shared := float64(len(m.SharedTerms))
	if shared > 10 {
		shared = 10
	}
	return m.TitleSimilarity + 0.05*shared + float64(len(m.MatchingSnippets))
}

// sharedDistinctiveTerms returns the distinctive tokens of a that also appear
// in b, in a's order.
func sharedDistinctiveTerms(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, token := range b {
		inB[token] = true
	}
	var shared []string
	for _, token := range distinctiveTokens(a) {
		if inB[token] {
			shared = append(shared, token)
		}
	}
	return shared
}

// sharedSnippets returns the target's snippets that appear verbatim in the
// candidate body.
func sharedSnippets(targetSnippets []string, candidateBody string) []string {
	if len(targetSnippets) == 0 {
		return nil
	}
	candidateSnippets := make(map[string]bool)
	for _, snippet := range extractSnippets(candidateBody) {
		candidateSnippets[snippet] = true
	}
	var shared []string
	for _, snippet := range targetSnippets {
		if candidateSnippets[snippet] {
			shared = append(shared, snippet)
		}
	}
	return shared
}
