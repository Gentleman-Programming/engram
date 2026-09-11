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

func TestRenderCandidatesCommentWithFixAvailability(t *testing.T) {
	stable, _ := ParseTagVersion("v1.10.1")
	prerelease, _ := ParseTagVersion("v2.0.0-rc.3")
	reporterOld, _ := ParseTagVersion("1.10.0")
	reporterNew, _ := ParseTagVersion("1.10.1")

	tests := []struct {
		name string
		fix  FixAvailability
		want []string
	}{
		{
			name: "unresolved",
			fix:  FixAvailability{Class: FixUnresolved},
			want: []string{
				"  - Fix availability: no merged fix pull request is linked to this candidate, so the problem appears unresolved.",
			},
		},
		{
			name: "on main only",
			fix:  FixAvailability{Class: FixOnMain},
			want: []string{
				"  - Fix availability: the linked fix is merged on the default branch and not in any release tag yet, so it is not yet generally available.",
				"  - Fix routing: no stable release contains the fix yet, so the reporter cannot upgrade past this problem today.",
			},
		},
		{
			name: "prerelease only",
			fix:  FixAvailability{Class: FixInPrerelease, EarliestPrerelease: &prerelease},
			want: []string{
				"  - Fix availability: the linked fix is included in v2.0.0-rc.3 (prerelease); no stable release contains it yet, so it is not yet generally available.",
				"  - Fix routing: no stable release contains the fix yet, so the reporter cannot upgrade past this problem today.",
			},
		},
		{
			name: "stable with earlier prerelease (union per channel)",
			fix:  FixAvailability{Class: FixInStable, EarliestStable: &stable, EarliestPrerelease: &prerelease},
			want: []string{
				"  - Fix availability: the linked fix is included in v1.10.1 (stable release), first shipped in v2.0.0-rc.3 (prerelease).",
				"  - Fix routing: the reporter version is missing or malformed in the report body, so no version comparison was made.",
			},
		},
		{
			name: "upgrade routing",
			fix:  FixAvailability{Class: FixInStable, EarliestStable: &stable, Reporter: &reporterOld, ReporterRaw: "1.10.0"},
			want: []string{
				"  - Fix availability: the linked fix is included in v1.10.1 (stable release).",
				"  - Fix routing: the reporter runs 1.10.0, which predates the fix; the minimum fixed version is v1.10.1, so upgrading to v1.10.1 or later should resolve this report.",
			},
		},
		{
			name: "regression routing",
			fix:  FixAvailability{Class: FixInStable, EarliestStable: &stable, Reporter: &reporterNew, ReporterRaw: "1.10.1"},
			want: []string{
				"  - Fix routing: the reporter runs 1.10.1, which is the same as or newer than the earliest stable fix v1.10.1; treat this report as a possible regression rather than an old occurrence.",
			},
		},
		{
			name: "ambiguous",
			fix:  FixAvailability{Class: FixAmbiguous},
			want: []string{
				"  - Fix availability: linked fix evidence is incomplete or ambiguous, so availability was not verified; maintainers should check manually.",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := []Match{{
				Issue:           Issue{Number: 11, Title: "App crashes when saving a large note", State: "open"},
				TitleSimilarity: 0.7,
				Fix:             &tt.fix,
			}}
			body := RenderCandidatesComment(matches)
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("comment missing %q:\n%s", want, body)
				}
			}
			if recorded := ParseRecordedCandidates(body); !reflect.DeepEqual(recorded, []int{11}) {
				t.Errorf("ParseRecordedCandidates must ignore fix lines, got %v", recorded)
			}
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(line, "  - Fix") && strings.Contains(line, "—") {
					t.Errorf("fix lines must not use em dashes: %q", line)
				}
			}
		})
	}
}
