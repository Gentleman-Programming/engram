package triage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// fakeFixSource is an in-memory FixEvidenceSource that records every call and
// answers compare queries from static oracle maps. It backs both the pure
// CollectFixEvidence tests and the Run wiring tests.
type fakeFixSource struct {
	prs         []FixPR
	prsComplete bool
	prsErr      error

	tags         []RepoTag
	tagsComplete bool
	tagsErr      error

	// contained answers "ref...commit" keys; a missing key means proven not
	// contained (false, nil). reachErr injects per-query failures.
	contained map[string]bool
	reachErr  map[string]error

	timelineCalls int
	tagsCalls     int
	compareCalls  int
	compareRefs   []string // "ref/commit" in call order
}

func (f *fakeFixSource) MergedFixPRs(_ context.Context, _ int) ([]FixPR, bool, error) {
	f.timelineCalls++
	if f.prsErr != nil {
		return nil, false, f.prsErr
	}
	return f.prs, f.prsComplete, nil
}

func (f *fakeFixSource) ListTags(_ context.Context) ([]RepoTag, bool, error) {
	f.tagsCalls++
	if f.tagsErr != nil {
		return nil, false, f.tagsErr
	}
	return f.tags, f.tagsComplete, nil
}

func (f *fakeFixSource) CommitContainedIn(_ context.Context, baseRef, commitSHA string) (bool, error) {
	f.compareCalls++
	f.compareRefs = append(f.compareRefs, baseRef+"/"+commitSHA)
	key := baseRef + "..." + commitSHA
	if err, ok := f.reachErr[key]; ok {
		return false, err
	}
	return f.contained[key], nil
}

func TestParseTagVersionValid(t *testing.T) {
	tests := []struct {
		in         string
		major      uint64
		minor      uint64
		patch      uint64
		prerelease []string
	}{
		{in: "1.2.3", major: 1, minor: 2, patch: 3},
		{in: "v1.2.3", major: 1, minor: 2, patch: 3},
		{in: "0.0.1", major: 0, minor: 0, patch: 1},
		{in: "v2.0.0-rc.10", major: 2, minor: 0, patch: 0, prerelease: []string{"rc", "10"}},
		{in: "1.2.3-0", major: 1, minor: 2, patch: 3, prerelease: []string{"0"}},
		{in: "1.2.3-alpha-beta", major: 1, minor: 2, patch: 3, prerelease: []string{"alpha-beta"}},
		{in: "v1.10.0", major: 1, minor: 10, patch: 0},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := ParseTagVersion(tt.in)
			if !ok {
				t.Fatalf("ParseTagVersion(%q) rejected a valid version", tt.in)
			}
			if got.Major != tt.major || got.Minor != tt.minor || got.Patch != tt.patch {
				t.Errorf("numeric components = %d.%d.%d, want %d.%d.%d", got.Major, got.Minor, got.Patch, tt.major, tt.minor, tt.patch)
			}
			if !reflect.DeepEqual(got.Prerelease, tt.prerelease) {
				t.Errorf("prerelease = %v, want %v", got.Prerelease, tt.prerelease)
			}
			if got.IsStable() != (tt.prerelease == nil) {
				t.Errorf("IsStable() = %t for %q", got.IsStable(), tt.in)
			}
			if got.String() != tt.in {
				t.Errorf("String() = %q, want the original %q", got.String(), tt.in)
			}
		})
	}
}

func TestParseTagVersionInvalid(t *testing.T) {
	for _, in := range []string{
		"",                        // empty
		"1",                       // one component
		"1.2",                     // two components
		"1.2.3.4",                 // four components
		"01.2.3",                  // leading zero in major
		"1.02.3",                  // leading zero in minor
		"1.2.03",                  // leading zero in patch
		"V1.2.3",                  // uppercase prefix
		"x1.2.3",                  // junk prefix
		"pi-v0.1.12",              // repository tag that is not a plain version
		"1.2.x",                   // non-numeric patch
		"-1.2.3",                  // negative major
		" 1.2.3",                  // leading space
		"1.2.3 ",                  // trailing space
		"1.2.3-",                  // empty prerelease identifier
		"1.2.3-rc..1",             // empty inner prerelease identifier
		"1.2.3-rc.01",             // numeric prerelease identifier with leading zero
		"1.2.3-rc.1+build",        // build metadata is not supported
		"1.2.3+build",             // build metadata is not supported
		"9999999999999999999.2.3", // component too large for uint64
	} {
		t.Run(in, func(t *testing.T) {
			if got, ok := ParseTagVersion(in); ok {
				t.Fatalf("ParseTagVersion(%q) accepted a malformed version as %s", in, got)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a    string
		b    string
		want int
	}{
		{a: "1.0.0", b: "1.0.0", want: 0},
		{a: "v1.0.0", b: "1.0.0", want: 0},
		{a: "2.0.0", b: "1.9.9", want: 1},
		{a: "v1.10.0", b: "v1.9.0", want: 1}, // numeric compare, not lexicographic
		{a: "1.0.1", b: "1.0.0", want: 1},
		{a: "1.1.0", b: "1.0.99", want: 1},
		{a: "1.0.0-alpha", b: "1.0.0", want: -1},
		{a: "1.0.0", b: "1.0.0-rc.1", want: 1}, // stable beats prerelease
		{a: "1.0.0-rc.1", b: "1.0.0-rc.2", want: -1},
		{a: "1.0.0-rc.2", b: "1.0.0-rc.10", want: -1}, // numeric identifiers compare numerically
		{a: "1.0.0-rc.10", b: "1.0.0-rc.9", want: 1},
		{a: "1.0.0-alpha", b: "1.0.0-alpha.1", want: -1}, // fewer fields rank lower
		{a: "1.0.0-alpha.1", b: "1.0.0-alpha.beta", want: -1},
		{a: "1.0.0-2", b: "1.0.0-rc", want: -1}, // numeric identifiers rank below alphanumeric
		{a: "1.0.0-beta", b: "1.0.0-alpha", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			a, ok := ParseTagVersion(tt.a)
			if !ok {
				t.Fatalf("ParseTagVersion(%q) failed", tt.a)
			}
			b, ok := ParseTagVersion(tt.b)
			if !ok {
				t.Fatalf("ParseTagVersion(%q) failed", tt.b)
			}
			if got := CompareVersions(a, b); got != tt.want {
				t.Errorf("CompareVersions(%s, %s) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestExtractReporterVersion(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "bold label followed by the value",
			body: "### Description\n\nIt crashes.\n\n**Engram Version**\n0.3.1\n",
			want: "0.3.1",
		},
		{
			name: "bold label with a blank line before the value",
			body: "**Engram Version**\n\nv2.0.0-rc.3\n",
			want: "v2.0.0-rc.3",
		},
		{
			name: "heading form of the label",
			body: "### Engram Version\n\n0.3.1\n",
			want: "0.3.1",
		},
		{
			name: "value line picked even with later labels present",
			body: "**Engram Version**\n1.0.0\n\n**Operating System**\nmacOS\n",
			want: "1.0.0",
		},
		{
			name: "label absent",
			body: "Some body without the version field.\n",
			want: "",
		},
		{
			name: "empty body",
			body: "",
			want: "",
		},
		{
			// Documented behavior: the next non-empty line is returned raw; a
			// value that is another label simply fails version parsing and is
			// treated as missing downstream.
			name: "label present but next line is another label",
			body: "**Engram Version**\n\n**Agent / Client**\nClaude Code\n",
			want: "**Agent / Client**",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractReporterVersion(tt.body); got != tt.want {
				t.Errorf("extractReporterVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

// mkEvidence builds complete evidence: every fix commit is compared against
// main and every valid SemVer tag, defaulting to "not contained" and then
// applying the contained overrides ("main" or tag name -> commits).
func mkEvidence(prs []FixPR, tags []RepoTag, contained map[string][]string) FixEvidence {
	commits := fixCommitSHAs(prs)
	reach := make(map[RefCommitKey]bool)
	for _, commit := range commits {
		reach[RefCommitKey{Ref: fixDefaultBranchRef, Commit: commit}] = false
		for _, tag := range tags {
			if _, ok := ParseTagVersion(tag.Name); !ok {
				continue
			}
			reach[RefCommitKey{Ref: tag.Name, Commit: commit}] = false
		}
	}
	for ref, list := range contained {
		for _, commit := range list {
			reach[RefCommitKey{Ref: ref, Commit: commit}] = true
		}
	}
	return FixEvidence{
		LinkedPRs:     prs,
		LinksComplete: true,
		Tags:          tags,
		TagsComplete:  true,
		Reach:         reach,
		ReachComplete: true,
	}
}

func versionString(v *Version) string {
	if v == nil {
		return ""
	}
	return v.String()
}

func TestClassifyFixAvailability(t *testing.T) {
	tests := []struct {
		name             string
		evidence         FixEvidence
		reporterVersion  string
		wantClass        FixClass
		wantStable       string
		wantPrerelease   string
		wantRoute        FixRoute
		wantReporterSeen bool
	}{
		{
			name:      "no linked merged fix PR: unresolved",
			evidence:  mkEvidence(nil, nil, nil),
			wantClass: FixUnresolved,
			wantRoute: FixRouteNone,
		},
		{
			name: "fix merged on main only: on-main, not generally available",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "v1.0.0", Commit: "t1"}},
				map[string][]string{fixDefaultBranchRef: {"c1"}},
			),
			wantClass: FixOnMain,
			wantRoute: FixRouteNotGA,
		},
		{
			name: "fix only in an RC tag: prerelease, not generally available",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "v2.0.0-rc.3", Commit: "t1"}},
				map[string][]string{"v2.0.0-rc.3": {"c1"}},
			),
			wantClass:      FixInPrerelease,
			wantPrerelease: "v2.0.0-rc.3",
			wantRoute:      FixRouteNotGA,
		},
		{
			name: "fix in several stable tags: earliest stable wins",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "v1.10.0", Commit: "t1"}, {Name: "v1.10.1", Commit: "t2"}, {Name: "v1.9.0", Commit: "t0"}},
				map[string][]string{"v1.10.0": {"c1"}, "v1.10.1": {"c1"}},
			),
			wantClass:  FixInStable,
			wantStable: "v1.10.0",
			wantRoute:  FixRouteNoVersionCompare,
		},
		{
			name: "fix in RC then stable: union per channel",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "v2.0.0-rc.3", Commit: "t1"}, {Name: "v2.0.0", Commit: "t2"}},
				map[string][]string{"v2.0.0-rc.3": {"c1"}, "v2.0.0": {"c1"}},
			),
			wantClass:      FixInStable,
			wantStable:     "v2.0.0",
			wantPrerelease: "v2.0.0-rc.3",
			wantRoute:      FixRouteNoVersionCompare,
		},
		{
			name: "backport disagreement: one commit in stable, one only in RC",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}, {Number: 24, MergeCommit: "c2"}},
				[]RepoTag{{Name: "v2.1.0", Commit: "t1"}, {Name: "v2.0.1-rc.1", Commit: "t2"}},
				map[string][]string{"v2.1.0": {"c1"}, "v2.0.1-rc.1": {"c2"}},
			),
			wantClass:      FixInStable,
			wantStable:     "v2.1.0",
			wantPrerelease: "v2.0.1-rc.1",
			wantRoute:      FixRouteNoVersionCompare,
		},
		{
			name: "reporter predates the stable fix: upgrade routing with minimum fixed version",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "v1.10.1", Commit: "t1"}},
				map[string][]string{"v1.10.1": {"c1"}},
			),
			reporterVersion:  "1.10.0",
			wantClass:        FixInStable,
			wantStable:       "v1.10.1",
			wantRoute:        FixRouteUpgrade,
			wantReporterSeen: true,
		},
		{
			name: "reporter equals the stable fix: possible regression",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "v1.10.1", Commit: "t1"}},
				map[string][]string{"v1.10.1": {"c1"}},
			),
			reporterVersion:  "1.10.1",
			wantClass:        FixInStable,
			wantStable:       "v1.10.1",
			wantRoute:        FixRouteRegression,
			wantReporterSeen: true,
		},
		{
			name: "reporter newer than the stable fix: possible regression",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "v1.10.1", Commit: "t1"}},
				map[string][]string{"v1.10.1": {"c1"}},
			),
			reporterVersion:  "1.11.0",
			wantClass:        FixInStable,
			wantStable:       "v1.10.1",
			wantRoute:        FixRouteRegression,
			wantReporterSeen: true,
		},
		{
			name: "reporter version missing: classify without comparison",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "v1.10.1", Commit: "t1"}},
				map[string][]string{"v1.10.1": {"c1"}},
			),
			reporterVersion: "",
			wantClass:       FixInStable,
			wantStable:      "v1.10.1",
			wantRoute:       FixRouteNoVersionCompare,
		},
		{
			name: "reporter version malformed: classify without comparison",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "v1.10.1", Commit: "t1"}},
				map[string][]string{"v1.10.1": {"c1"}},
			),
			reporterVersion: "latest from source",
			wantClass:       FixInStable,
			wantStable:      "v1.10.1",
			wantRoute:       FixRouteNoVersionCompare,
		},
		{
			name: "RC-only fix with a newer reporter build: still not generally available, no regression routing",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "v2.0.1-rc.1", Commit: "t1"}},
				map[string][]string{"v2.0.1-rc.1": {"c1"}},
			),
			reporterVersion:  "2.0.0",
			wantClass:        FixInPrerelease,
			wantPrerelease:   "v2.0.1-rc.1",
			wantRoute:        FixRouteNotGA,
			wantReporterSeen: true,
		},
		{
			name: "linked fix merged somewhere unknown: no tag, not on main, conservative ambiguous",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "v1.0.0", Commit: "t1"}},
				nil,
			),
			wantClass: FixAmbiguous,
			wantRoute: FixRouteNone,
		},
		{
			name: "links fetch incomplete: ambiguous",
			evidence: FixEvidence{
				LinkedPRs:     []FixPR{{Number: 21, MergeCommit: "c1"}},
				LinksComplete: false,
			},
			wantClass: FixAmbiguous,
			wantRoute: FixRouteNone,
		},
		{
			name: "tags fetch incomplete: ambiguous",
			evidence: FixEvidence{
				LinkedPRs:     []FixPR{{Number: 21, MergeCommit: "c1"}},
				LinksComplete: true,
				Tags:          []RepoTag{{Name: "v1.0.0", Commit: "t1"}},
				TagsComplete:  false,
			},
			wantClass: FixAmbiguous,
			wantRoute: FixRouteNone,
		},
		{
			name: "reachability incomplete: ambiguous",
			evidence: FixEvidence{
				LinkedPRs:     []FixPR{{Number: 21, MergeCommit: "c1"}},
				LinksComplete: true,
				Tags:          []RepoTag{{Name: "v1.0.0", Commit: "t1"}},
				TagsComplete:  true,
				Reach: map[RefCommitKey]bool{
					{Ref: fixDefaultBranchRef, Commit: "c1"}: true,
				},
				ReachComplete: false,
			},
			wantClass: FixAmbiguous,
			wantRoute: FixRouteNone,
		},
		{
			name: "missing reach fact for a valid tag: ambiguous",
			evidence: FixEvidence{
				LinkedPRs:     []FixPR{{Number: 21, MergeCommit: "c1"}},
				LinksComplete: true,
				Tags:          []RepoTag{{Name: "v1.0.0", Commit: "t1"}},
				TagsComplete:  true,
				Reach: map[RefCommitKey]bool{
					{Ref: fixDefaultBranchRef, Commit: "c1"}: true,
					// no fact for v1.0.0
				},
				ReachComplete: true,
			},
			wantClass: FixAmbiguous,
			wantRoute: FixRouteNone,
		},
		{
			name: "valid tag without commit SHA: ambiguous",
			evidence: FixEvidence{
				LinkedPRs:     []FixPR{{Number: 21, MergeCommit: "c1"}},
				LinksComplete: true,
				Tags:          []RepoTag{{Name: "v1.0.0"}},
				TagsComplete:  true,
				Reach: map[RefCommitKey]bool{
					{Ref: fixDefaultBranchRef, Commit: "c1"}: true,
				},
				ReachComplete: true,
			},
			wantClass: FixAmbiguous,
			wantRoute: FixRouteNone,
		},
		{
			name: "merged linked PR without a merge commit: ambiguous",
			evidence: FixEvidence{
				LinkedPRs:     []FixPR{{Number: 21}},
				LinksComplete: true,
				TagsComplete:  true,
				ReachComplete: true,
			},
			wantClass: FixAmbiguous,
			wantRoute: FixRouteNone,
		},
		{
			name: "malformed tags are ignored and never block classification",
			evidence: mkEvidence(
				[]FixPR{{Number: 21, MergeCommit: "c1"}},
				[]RepoTag{{Name: "pi-v0.1.12", Commit: "t9"}, {Name: "v0.1.9", Commit: "t1"}},
				map[string][]string{"v0.1.9": {"c1"}},
			),
			wantClass:  FixInStable,
			wantStable: "v0.1.9",
			wantRoute:  FixRouteNoVersionCompare,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.evidence.ReporterVersion = tt.reporterVersion
			got := ClassifyFixAvailability(tt.evidence)
			if got.Class != tt.wantClass {
				t.Fatalf("Class = %v, want %v", got.Class, tt.wantClass)
			}
			if gotStable := versionString(got.EarliestStable); gotStable != tt.wantStable {
				t.Errorf("EarliestStable = %q, want %q", gotStable, tt.wantStable)
			}
			if gotPre := versionString(got.EarliestPrerelease); gotPre != tt.wantPrerelease {
				t.Errorf("EarliestPrerelease = %q, want %q", gotPre, tt.wantPrerelease)
			}
			if got.Route() != tt.wantRoute {
				t.Errorf("Route() = %v, want %v", got.Route(), tt.wantRoute)
			}
			if (got.Reporter != nil) != tt.wantReporterSeen {
				t.Errorf("Reporter parsed = %v, want %t", got.Reporter != nil, tt.wantReporterSeen)
			}
		})
	}
}

func TestCollectFixEvidence(t *testing.T) {
	tags := []RepoTag{
		{Name: "v2.0.0-rc.1", Commit: "t2"},
		{Name: "v1.0.0", Commit: "t1"},
		{Name: "pi-v0.1.12", Commit: "t9"}, // malformed: never queried
	}

	t.Run("no linked fix PRs: no tag or reachability queries at all", func(t *testing.T) {
		source := &fakeFixSource{prsComplete: true, tagsComplete: true}
		evidence := CollectFixEvidence(context.Background(), source, 11)
		if !evidence.LinksComplete || !evidence.TagsComplete || !evidence.ReachComplete {
			t.Fatalf("evidence completeness = links:%t tags:%t reach:%t, want all true", evidence.LinksComplete, evidence.TagsComplete, evidence.ReachComplete)
		}
		if source.tagsCalls != 0 || source.compareCalls != 0 {
			t.Fatalf("no evidence queries expected without fix PRs, tags:%d compare:%d", source.tagsCalls, source.compareCalls)
		}
	})

	t.Run("queries main and every valid tag in ascending version order", func(t *testing.T) {
		source := &fakeFixSource{
			prs:          []FixPR{{Number: 21, MergeCommit: "c1"}},
			prsComplete:  true,
			tags:         tags,
			tagsComplete: true,
			contained: map[string]bool{
				"main...c1":        true,
				"v1.0.0...c1":      true,
				"v2.0.0-rc.1...c1": false,
			},
		}
		evidence := CollectFixEvidence(context.Background(), source, 11)
		if !evidence.LinksComplete || !evidence.TagsComplete || !evidence.ReachComplete {
			t.Fatalf("evidence completeness = links:%t tags:%t reach:%t, want all true", evidence.LinksComplete, evidence.TagsComplete, evidence.ReachComplete)
		}
		wantRefs := []string{"main/c1", "v1.0.0/c1", "v2.0.0-rc.1/c1"}
		if !reflect.DeepEqual(source.compareRefs, wantRefs) {
			t.Fatalf("compare refs = %v, want %v", source.compareRefs, wantRefs)
		}
		wantReach := map[RefCommitKey]bool{
			{Ref: "main", Commit: "c1"}:        true,
			{Ref: "v1.0.0", Commit: "c1"}:      true,
			{Ref: "v2.0.0-rc.1", Commit: "c1"}: false,
		}
		if !reflect.DeepEqual(evidence.Reach, wantReach) {
			t.Fatalf("Reach = %v, want %v", evidence.Reach, wantReach)
		}
	})

	t.Run("duplicate fix commits are deduplicated before querying", func(t *testing.T) {
		source := &fakeFixSource{
			prs: []FixPR{
				{Number: 21, MergeCommit: "c1"},
				{Number: 22, MergeCommit: "c1"},
			},
			prsComplete:  true,
			tags:         []RepoTag{{Name: "v1.0.0", Commit: "t1"}},
			tagsComplete: true,
		}
		evidence := CollectFixEvidence(context.Background(), source, 11)
		if !evidence.ReachComplete {
			t.Fatal("ReachComplete = false, want true")
		}
		if source.compareCalls != 2 { // main + one tag, once for the shared commit
			t.Fatalf("compare calls = %d, want 2", source.compareCalls)
		}
	})

	t.Run("timeline failure stops collection conservatively", func(t *testing.T) {
		source := &fakeFixSource{prsErr: errors.New("502 bad gateway"), tagsComplete: true}
		evidence := CollectFixEvidence(context.Background(), source, 11)
		if evidence.LinksComplete {
			t.Fatal("LinksComplete = true after a timeline failure")
		}
		if source.tagsCalls != 0 || source.compareCalls != 0 {
			t.Fatalf("no further queries expected, tags:%d compare:%d", source.tagsCalls, source.compareCalls)
		}
	})

	t.Run("timeline page cap exhausted marks links incomplete", func(t *testing.T) {
		source := &fakeFixSource{prs: []FixPR{{Number: 21, MergeCommit: "c1"}}, prsComplete: false, tagsComplete: true}
		evidence := CollectFixEvidence(context.Background(), source, 11)
		if evidence.LinksComplete {
			t.Fatal("LinksComplete = true despite the timeline cap")
		}
	})

	t.Run("tags failure stops before reachability queries", func(t *testing.T) {
		source := &fakeFixSource{
			prs:         []FixPR{{Number: 21, MergeCommit: "c1"}},
			prsComplete: true,
			tagsErr:     errors.New("403 rate limit"),
		}
		evidence := CollectFixEvidence(context.Background(), source, 11)
		if evidence.TagsComplete {
			t.Fatal("TagsComplete = true after a tags failure")
		}
		if source.compareCalls != 0 {
			t.Fatalf("no compare expected after a tags failure, got %d", source.compareCalls)
		}
	})

	t.Run("one failed compare marks reachability incomplete", func(t *testing.T) {
		source := &fakeFixSource{
			prs:          []FixPR{{Number: 21, MergeCommit: "c1"}},
			prsComplete:  true,
			tags:         []RepoTag{{Name: "v1.0.0", Commit: "t1"}, {Name: "v2.0.0", Commit: "t2"}},
			tagsComplete: true,
			reachErr:     map[string]error{"v2.0.0...c1": errors.New("429 rate limit")},
		}
		evidence := CollectFixEvidence(context.Background(), source, 11)
		if evidence.ReachComplete {
			t.Fatal("ReachComplete = true despite a failed compare")
		}
	})

	t.Run("compare budget exhaustion marks reachability incomplete", func(t *testing.T) {
		var many []RepoTag
		for i := 0; i <= maxReachQueries; i++ {
			many = append(many, RepoTag{Name: fmt.Sprintf("v0.0.%d", i), Commit: fmt.Sprintf("t%d", i)})
		}
		source := &fakeFixSource{
			prs:          []FixPR{{Number: 21, MergeCommit: "c1"}},
			prsComplete:  true,
			tags:         many,
			tagsComplete: true,
		}
		evidence := CollectFixEvidence(context.Background(), source, 11)
		if evidence.ReachComplete {
			t.Fatal("ReachComplete = true despite exceeding the compare budget")
		}
		if source.compareCalls != maxReachQueries {
			t.Fatalf("compare calls = %d, want the budget %d", source.compareCalls, maxReachQueries)
		}
	})

	t.Run("merged PR without merge commit still collects the remaining commits", func(t *testing.T) {
		source := &fakeFixSource{
			prs: []FixPR{
				{Number: 21}, // merged but the API gave no merge commit
				{Number: 22, MergeCommit: "c2"},
			},
			prsComplete:  true,
			tags:         []RepoTag{{Name: "v1.0.0", Commit: "t1"}},
			tagsComplete: true,
		}
		evidence := CollectFixEvidence(context.Background(), source, 11)
		if !evidence.ReachComplete {
			t.Fatal("ReachComplete = false, want true")
		}
		if got := ClassifyFixAvailability(evidence); got.Class != FixAmbiguous {
			t.Fatalf("Class = %v, want FixAmbiguous for a PR without a merge commit", got.Class)
		}
	})
}
