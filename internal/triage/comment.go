package triage

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// recordedCandidatePattern matches the "- #N:" list lines that carry the
// recorded candidate set; no other rendered line starts with "- #".
var recordedCandidatePattern = regexp.MustCompile(`(?m)^- #(\d+):`)

// maxSnippetDisplayRunes bounds an evidence snippet shown in the comment.
const maxSnippetDisplayRunes = 80

// RenderCandidatesComment renders the single anchored bot comment listing the
// accepted candidate issues with their evidence. The rendered candidate lines
// round-trip through ParseRecordedCandidates.
func RenderCandidatesComment(matches []Match) string {
	var b strings.Builder
	b.WriteString(CommentAnchor)
	b.WriteString("\n### Possible duplicates detected\n\n")
	b.WriteString("This issue may describe the same problem as earlier issues:\n\n")
	for _, match := range matches {
		state := "open"
		if match.Issue.State == "closed" {
			state = "closed"
		}
		fmt.Fprintf(&b, "- #%d: **%s** (%s) — %d%% title similarity",
			match.Issue.Number, commentSafe(match.Issue.Title), state,
			int(math.Round(match.TitleSimilarity*100)))
		if len(match.SharedTerms) > 0 {
			fmt.Fprintf(&b, ", shared terms: %s", quoteTerms(match.SharedTerms))
		}
		if len(match.MatchingSnippets) > 0 {
			fmt.Fprintf(&b, ", matching snippet: `%s`", commentSafe(match.MatchingSnippets[0]))
		}
		b.WriteString("\n")
		if match.Fix != nil {
			b.WriteString(renderFixAvailability(*match.Fix))
		}
	}
	b.WriteString("\nMaintainers decide next: confirm by closing this issue as a duplicate of a")
	b.WriteString(" candidate above, or reject by removing the `triage:possible-duplicate` label")
	b.WriteString(" — the bot will not suggest the same candidates again.\n\n")
	b.WriteString("The bot never closes, edits, or consolidates issues by itself.\n")
	return b.String()
}

// RenderNoCandidatesComment renders the anchored comment body used when a
// previously reported issue no longer has duplicate candidates.
func RenderNoCandidatesComment() string {
	var b strings.Builder
	b.WriteString(CommentAnchor)
	b.WriteString("\n### Possible duplicates detected\n\n")
	b.WriteString("No duplicate candidates remain for this issue, so the `triage:possible-duplicate`")
	b.WriteString(" label has been removed.\n\n")
	b.WriteString("The bot never closes, edits, or consolidates issues by itself.\n")
	return b.String()
}

// ambiguousFixSentence is the conservative wording used whenever fix
// evidence cannot be resolved.
const ambiguousFixSentence = "linked fix evidence is incomplete or ambiguous, so availability was not verified; maintainers should check manually."

// renderFixAvailability renders the deterministic fix-availability lines for
// one classified candidate: exactly one availability line, plus one routing
// line when routing guidance applies. Version strings come from parsed
// values, so they cannot carry markdown hazards.
func renderFixAvailability(fix FixAvailability) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  - Fix availability: %s\n", fixAvailabilitySentence(fix))
	if route := fix.Route(); route != FixRouteNone {
		fmt.Fprintf(&b, "  - Fix routing: %s\n", fixRoutingSentence(fix, route))
	}
	return b.String()
}

// fixAvailabilitySentence describes the classification.
func fixAvailabilitySentence(fix FixAvailability) string {
	switch fix.Class {
	case FixUnresolved:
		return "no merged fix pull request is linked to this candidate, so the problem appears unresolved."
	case FixOnMain:
		return "the linked fix is merged on the default branch and not in any release tag yet, so it is not yet generally available."
	case FixInPrerelease:
		if fix.EarliestPrerelease == nil {
			return ambiguousFixSentence
		}
		return fmt.Sprintf("the linked fix is included in %s (prerelease); no stable release contains it yet, so it is not yet generally available.", fix.EarliestPrerelease)
	case FixInStable:
		if fix.EarliestStable == nil {
			return ambiguousFixSentence
		}
		sentence := fmt.Sprintf("the linked fix is included in %s (stable release)", fix.EarliestStable)
		if fix.EarliestPrerelease != nil {
			sentence += fmt.Sprintf(", first shipped in %s (prerelease)", fix.EarliestPrerelease)
		}
		return sentence + "."
	default: // FixAmbiguous and anything unexpected stays conservative
		return ambiguousFixSentence
	}
}

// fixRoutingSentence renders the routing guidance for the derived route.
func fixRoutingSentence(fix FixAvailability, route FixRoute) string {
	switch route {
	case FixRouteUpgrade:
		return fmt.Sprintf("the reporter runs %s, which predates the fix; the minimum fixed version is %s, so upgrading to %s or later should resolve this report.", fix.ReporterRaw, fix.EarliestStable, fix.EarliestStable)
	case FixRouteRegression:
		return fmt.Sprintf("the reporter runs %s, which is the same as or newer than the earliest stable fix %s; treat this report as a possible regression rather than an old occurrence.", fix.ReporterRaw, fix.EarliestStable)
	case FixRouteNotGA:
		return "no stable release contains the fix yet, so the reporter cannot upgrade past this problem today."
	default: // FixRouteNoVersionCompare
		return "the reporter version is missing or malformed in the report body, so no version comparison was made."
	}
}

// ParseRecordedCandidates extracts the candidate issue numbers recorded in an
// anchored comment body (the "- #N:" list lines). Empty when none are
// recorded.
func ParseRecordedCandidates(body string) []int {
	var numbers []int
	for _, match := range recordedCandidatePattern.FindAllStringSubmatch(body, -1) {
		var number int
		if _, err := fmt.Sscanf(match[1], "%d", &number); err == nil {
			numbers = append(numbers, number)
		}
	}
	return numbers
}

// commentSafe makes free-form API text safe to embed in the markdown comment:
// no backticks, no line breaks, no @ mentions (replaced with "(at)"), collapsed
// whitespace, bounded length.
func commentSafe(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, "@", "(at)")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if runes := []rune(s); len(runes) > maxSnippetDisplayRunes {
		return string(runes[:maxSnippetDisplayRunes-1]) + "…"
	}
	return s
}

func quoteTerms(terms []string) string {
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, "`"+term+"`")
	}
	return strings.Join(quoted, ", ")
}
