package triage

import (
	"reflect"
	"strings"
	"testing"
)

func TestRenderCandidatesComment(t *testing.T) {
	matches := []Match{
		{
			Issue:            Issue{Number: 11, Title: "App crashes when saving a large note", State: "open"},
			TitleSimilarity:  0.625,
			SharedTerms:      []string{"crashes", "saving", "large"},
			MatchingSnippets: nil,
		},
		{
			Issue:            Issue{Number: 13, Title: "App crashes when saving large notes again", State: "closed"},
			TitleSimilarity:  5.0 / 7.0,
			SharedTerms:      []string{"crashes", "saving", "large", "notes"},
			MatchingSnippets: []string{"panic: runtime error: index out of range"},
		},
	}
	body := RenderCandidatesComment(matches)

	if !strings.HasPrefix(body, CommentAnchor+"\n") {
		t.Errorf("comment must start with the anchor, got: %q", body)
	}
	for _, want := range []string{
		"- #11: **App crashes when saving a large note** (open)",
		"63% title similarity",
		"shared terms: `crashes`, `saving`, `large`",
		"- #13: **App crashes when saving large notes again** (closed)",
		"71% title similarity",
		"matching snippet: `panic: runtime error: index out of range`",
		"triage:possible-duplicate",
		"never closes",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("comment missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "@") {
		t.Errorf("comment must not @-mention anyone, got:\n%s", body)
	}

	recorded := ParseRecordedCandidates(body)
	if want := []int{11, 13}; !reflect.DeepEqual(recorded, want) {
		t.Errorf("ParseRecordedCandidates(rendered) = %v, want %v", recorded, want)
	}
}

func TestRenderNoCandidatesComment(t *testing.T) {
	body := RenderNoCandidatesComment()
	if !strings.HasPrefix(body, CommentAnchor+"\n") {
		t.Errorf("note must start with the anchor, got: %q", body)
	}
	if !strings.Contains(body, "No duplicate candidates remain") {
		t.Errorf("note missing the no-candidates sentence, got:\n%s", body)
	}
	if got := ParseRecordedCandidates(body); len(got) != 0 {
		t.Errorf("ParseRecordedCandidates(note) = %v, want empty", got)
	}
}

func TestParseRecordedCandidatesIgnoresOtherLines(t *testing.T) {
	body := "Maintainer note: #12 looks related\n- #99 maybe\nsome `- #55` inline text\n"
	if got := ParseRecordedCandidates(body); len(got) != 0 {
		t.Errorf("ParseRecordedCandidates(non-bot text) = %v, want empty", got)
	}
}

func TestCommentSafeStripsMarkdownHazards(t *testing.T) {
	got := commentSafe("`code`\nwith\ttab")
	if strings.ContainsAny(got, "`\n\t") {
		t.Errorf("commentSafe left markdown hazards: %q", got)
	}
	// Mentions must be neutralized so titles/snippets cannot ping anyone.
	got = commentSafe("crash @victim on save")
	if strings.Contains(got, "@") {
		t.Errorf("commentSafe left an @ mention: %q", got)
	}
	if !strings.Contains(got, "(at)") {
		t.Errorf("commentSafe should substitute (at) for @, got: %q", got)
	}
	long := strings.Repeat("x", 200)
	if got := commentSafe(long); len([]rune(got)) > 80 {
		t.Errorf("commentSafe did not bound length: %d runes", len([]rune(got)))
	}
}

func TestRenderedCommentNeverContainsMentions(t *testing.T) {
	matches := []Match{
		{
			Issue:            Issue{Number: 13, Title: "Crash pings @maintainer on save", State: "closed"},
			TitleSimilarity:  0.7,
			SharedTerms:      []string{"crash", "save"},
			MatchingSnippets: []string{"panic: runtime error @someone caused this"},
		},
	}
	body := RenderCandidatesComment(matches)
	if strings.Contains(body, "@") {
		t.Errorf("rendered comment must not contain @ (mention risk):\n%s", body)
	}
	if !strings.Contains(body, "(at)maintainer") {
		t.Errorf("title mention should be neutralized in place:\n%s", body)
	}
	if !strings.Contains(body, "(at)someone") {
		t.Errorf("snippet mention should be neutralized in place:\n%s", body)
	}
}
