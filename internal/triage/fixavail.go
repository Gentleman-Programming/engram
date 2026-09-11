package triage

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// fixDefaultBranchRef is the ref reachability is proven against to classify a
// fix as "reachable from main". Any error comparing against it (for example a
// repository whose default branch is named differently) means unknown, so the
// classifier stays conservative.
const fixDefaultBranchRef = "main"

// maxReachQueries bounds the total compare queries one evidence collection may
// issue: the default branch plus every valid SemVer tag, per fix commit.
// Beyond the budget reachability is incomplete and classification stays
// conservative instead of claiming availability.
const maxReachQueries = 400

// Version is a semantic version parsed from a git tag or a bug report.
type Version struct {
	Major      uint64
	Minor      uint64
	Patch      uint64
	Prerelease []string // dot-separated identifiers; empty means stable
	raw        string   // the exact string this version was parsed from
}

// IsStable reports whether the version has no prerelease suffix.
func (v Version) IsStable() bool { return len(v.Prerelease) == 0 }

// String returns the exact string the version was parsed from.
func (v Version) String() string { return v.raw }

// ParseTagVersion parses self-contained SemVer support for git tags and bug
// report values: an optional "v" prefix, exactly three numeric components
// without leading zeros, and an optional well-formed prerelease suffix (dot
// separated identifiers of alphanumerics and hyphens, numeric identifiers
// without leading zeros). A version without a prerelease suffix is stable.
// Build metadata and anything else is malformed; callers must ignore malformed
// tags rather than classify from them.
func ParseTagVersion(s string) (Version, bool) {
	trimmed := strings.TrimPrefix(s, "v")
	core, prerelease, hasPrerelease := trimmed, "", false
	if i := strings.Index(trimmed, "-"); i >= 0 {
		core, prerelease, hasPrerelease = trimmed[:i], trimmed[i+1:], true
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var numbers [3]uint64
	for i, part := range parts {
		number, ok := parseNumericComponent(part)
		if !ok {
			return Version{}, false
		}
		numbers[i] = number
	}
	var identifiers []string
	if hasPrerelease {
		if prerelease == "" {
			return Version{}, false // "1.2.3-" is an empty identifier
		}
		identifiers = strings.Split(prerelease, ".")
		for _, identifier := range identifiers {
			if !validPrereleaseIdentifier(identifier) {
				return Version{}, false
			}
		}
	}
	return Version{
		Major:      numbers[0],
		Minor:      numbers[1],
		Patch:      numbers[2],
		Prerelease: identifiers,
		raw:        s,
	}, true
}

// CompareVersions returns -1, 0, or +1 following SemVer precedence: numeric
// components first, then stable over prerelease, then identifier-wise
// prerelease comparison where numeric identifiers rank below alphanumeric ones
// and longer identifier lists rank above their prefixes.
func CompareVersions(a, b Version) int {
	if a.Major != b.Major {
		return signUint64(a.Major, b.Major)
	}
	if a.Minor != b.Minor {
		return signUint64(a.Minor, b.Minor)
	}
	if a.Patch != b.Patch {
		return signUint64(a.Patch, b.Patch)
	}
	if len(a.Prerelease) == 0 && len(b.Prerelease) == 0 {
		return 0
	}
	if len(a.Prerelease) == 0 {
		return 1
	}
	if len(b.Prerelease) == 0 {
		return -1
	}
	for i := 0; i < len(a.Prerelease) && i < len(b.Prerelease); i++ {
		if c := compareIdentifiers(a.Prerelease[i], b.Prerelease[i]); c != 0 {
			return c
		}
	}
	return signInt(len(a.Prerelease), len(b.Prerelease))
}

// parseNumericComponent parses one numeric version component: digits only, no
// leading zeros (a lone "0" is fine), bounded so uint64 cannot overflow.
func parseNumericComponent(part string) (uint64, bool) {
	if part == "" || len(part) > 18 {
		return 0, false
	}
	for i := 0; i < len(part); i++ {
		if part[i] < '0' || part[i] > '9' {
			return 0, false
		}
	}
	if len(part) > 1 && part[0] == '0' {
		return 0, false
	}
	number, err := strconv.ParseUint(part, 10, 64)
	if err != nil {
		return 0, false
	}
	return number, true
}

// validPrereleaseIdentifier accepts non-empty identifiers of alphanumerics and
// hyphens where purely numeric identifiers carry no leading zeros.
func validPrereleaseIdentifier(identifier string) bool {
	if identifier == "" {
		return false
	}
	allDigits := true
	for i := 0; i < len(identifier); i++ {
		c := identifier[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
			allDigits = false
		default:
			return false
		}
	}
	return !allDigits || len(identifier) == 1 || identifier[0] != '0'
}

// compareIdentifiers compares two prerelease identifiers per SemVer.
func compareIdentifiers(a, b string) int {
	aNumber, aNumeric := parseNumericComponent(a)
	bNumber, bNumeric := parseNumericComponent(b)
	switch {
	case aNumeric && bNumeric:
		return signUint64(aNumber, bNumber)
	case aNumeric:
		return -1 // numeric identifiers rank below alphanumeric ones
	case bNumeric:
		return 1
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func signUint64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func signInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// FixPR is one pull request explicitly linked to the issue as its fix: it was
// cross-referenced from the issue timeline and GitHub reports it merged. Prose
// mentions and non-merged pull requests never produce a FixPR.
type FixPR struct {
	Number      int
	MergeCommit string // merge commit SHA; empty means the API provided none
}

// RepoTag is one repository tag with the commit SHA it points to.
type RepoTag struct {
	Name   string
	Commit string
}

// RefCommitKey identifies one reachability fact: is Commit reachable from Ref,
// where Ref is a tag name or the default branch.
type RefCommitKey struct {
	Ref    string
	Commit string
}

// FixEvidence is the fetched evidence for one canonical match issue. Unknowns
// are represented explicitly: incomplete fetches, absent reach keys, and
// unusable commits all force the classifier to stay conservative.
type FixEvidence struct {
	// LinkedPRs are the merged PRs cross-referenced from the issue timeline.
	LinkedPRs []FixPR
	// LinksComplete reports whether the full timeline cross-reference set was
	// established.
	LinksComplete bool
	// Tags are the repository tags with their commit SHAs.
	Tags []RepoTag
	// TagsComplete reports whether the full tag list was established.
	TagsComplete bool
	// Reach holds resolved reachability facts. A key present with true means
	// proven contained, present with false means proven not contained, and an
	// absent key means unknown.
	Reach map[RefCommitKey]bool
	// ReachComplete reports whether every needed compare query resolved.
	ReachComplete bool
	// ReporterVersion is the raw "Engram Version" value from the triaged
	// issue body; empty means absent.
	ReporterVersion string
}

// FixEvidenceSource is the optional GitHub surface for fix-availability
// classification. It is deliberately separate from Client so the feature can
// stay an opt-in dependency. Implementations must tolerate sequential use by
// Run; errors mean a fact is unknown, never guessed.
type FixEvidenceSource interface {
	// MergedFixPRs returns the merged pull requests cross-referenced from the
	// issue timeline, with their merge commit SHAs, and whether the full
	// timeline was established. It must never infer fixes from prose or count
	// non-merged pull requests.
	MergedFixPRs(ctx context.Context, issueNumber int) (prs []FixPR, complete bool, err error)
	// ListTags returns repository tags with the commit SHA each points to,
	// and whether the full tag list was established.
	ListTags(ctx context.Context) (tags []RepoTag, complete bool, err error)
	// CommitContainedIn reports whether commitSHA is reachable from baseRef
	// (a tag name or the default branch). Per GitHub compare semantics,
	// comparing baseRef as base and commitSHA as head, "behind" or "identical"
	// proves ancestry. Any error or ambiguous answer means unknown.
	CommitContainedIn(ctx context.Context, baseRef, commitSHA string) (contained bool, err error)
}

// FixClass is the conservative availability classification of a fix.
type FixClass int

const (
	// FixUnresolved means no explicit fix evidence exists for the issue.
	FixUnresolved FixClass = iota
	// FixOnMain means the fix is proven reachable from the default branch and
	// is contained in no release tag.
	FixOnMain
	// FixInPrerelease means a prerelease/RC tag contains the fix and no stable
	// tag does.
	FixInPrerelease
	// FixInStable means a stable release tag contains the fix.
	FixInStable
	// FixAmbiguous means fix evidence exists but could not be resolved
	// conservatively (failed or truncated fetches, unknown reachability, a
	// merged PR without a merge commit, commits contained nowhere provable).
	FixAmbiguous
)

// String names the class for logs and test output.
func (c FixClass) String() string {
	switch c {
	case FixUnresolved:
		return "unresolved"
	case FixOnMain:
		return "reachable from main"
	case FixInPrerelease:
		return "in a prerelease tag"
	case FixInStable:
		return "in a stable release tag"
	case FixAmbiguous:
		return "ambiguous"
	default:
		return "unknown"
	}
}

// FixRoute is the deterministic routing guidance derived from a
// classification plus the reporter version when one is available.
type FixRoute int

const (
	// FixRouteNone means no routing guidance applies.
	FixRouteNone FixRoute = iota
	// FixRouteUpgrade means the reporter's version predates the stable fix, so
	// the minimum fixed version should be explained.
	FixRouteUpgrade
	// FixRouteRegression means the reporter's version is the same as or newer
	// than the earliest stable fix, so the report is a regression candidate.
	FixRouteRegression
	// FixRouteNotGA means the fix is only on main or in a prerelease tag, so
	// it is not yet generally available.
	FixRouteNotGA
	// FixRouteNoVersionCompare means a stable fix exists but the reporter
	// version is missing or malformed, so no version comparison was made.
	FixRouteNoVersionCompare
)

// String names the route for test output.
func (r FixRoute) String() string {
	switch r {
	case FixRouteNone:
		return "none"
	case FixRouteUpgrade:
		return "upgrade to minimum fixed version"
	case FixRouteRegression:
		return "possible regression"
	case FixRouteNotGA:
		return "not yet generally available"
	case FixRouteNoVersionCompare:
		return "no version comparison"
	default:
		return "unknown"
	}
}

// FixAvailability is the deterministic result of classifying one canonical
// match's fix evidence.
type FixAvailability struct {
	// Class is the conservative availability classification.
	Class FixClass
	// EarliestStable is the earliest stable tag containing any fix commit;
	// nil unless Class is FixInStable.
	EarliestStable *Version
	// EarliestPrerelease is the earliest prerelease tag containing any fix
	// commit; nil when no prerelease tag contains a fix commit.
	EarliestPrerelease *Version
	// OnMain reports whether some fix commit is proven reachable from the
	// default branch.
	OnMain bool
	// Reporter is the parsed reporter version; nil when missing or malformed.
	Reporter *Version
	// ReporterRaw is the raw reporter version string, "" when absent.
	ReporterRaw string
}

// Route derives the deterministic routing guidance for the classification.
func (f FixAvailability) Route() FixRoute {
	switch f.Class {
	case FixInStable:
		if f.Reporter == nil || f.EarliestStable == nil {
			return FixRouteNoVersionCompare
		}
		if CompareVersions(*f.Reporter, *f.EarliestStable) < 0 {
			return FixRouteUpgrade
		}
		return FixRouteRegression
	case FixOnMain, FixInPrerelease:
		return FixRouteNotGA
	default:
		return FixRouteNone
	}
}

// fixCommitSHAs returns the deduplicated, non-empty merge commit SHAs of the
// linked fix PRs in first-appearance order.
func fixCommitSHAs(prs []FixPR) []string {
	seen := make(map[string]bool, len(prs))
	var commits []string
	for _, pr := range prs {
		if pr.MergeCommit == "" || seen[pr.MergeCommit] {
			continue
		}
		seen[pr.MergeCommit] = true
		commits = append(commits, pr.MergeCommit)
	}
	return commits
}

// ClassifyFixAvailability classifies fix availability from evidence values
// only; it performs no I/O. Every unknown or incomplete fact keeps the result
// conservative: the classifier never claims availability without proof.
func ClassifyFixAvailability(ev FixEvidence) FixAvailability {
	result := FixAvailability{ReporterRaw: ev.ReporterVersion}
	if reporter, ok := ParseTagVersion(ev.ReporterVersion); ok {
		result.Reporter = &reporter
	}
	if !ev.LinksComplete {
		result.Class = FixAmbiguous
		return result
	}
	for _, pr := range ev.LinkedPRs {
		if pr.MergeCommit == "" {
			// A merged PR without a merge commit cannot be located anywhere.
			result.Class = FixAmbiguous
			return result
		}
	}
	commits := fixCommitSHAs(ev.LinkedPRs)
	if len(commits) == 0 {
		result.Class = FixUnresolved
		return result
	}
	if !ev.TagsComplete || !ev.ReachComplete {
		result.Class = FixAmbiguous
		return result
	}
	// Defensive completeness: the default branch and every valid SemVer tag
	// must have a resolved fact for every fix commit, and a valid tag without
	// a commit SHA cannot be compared at all.
	for _, commit := range commits {
		contained, ok := ev.Reach[RefCommitKey{Ref: fixDefaultBranchRef, Commit: commit}]
		if !ok {
			result.Class = FixAmbiguous
			return result
		}
		result.OnMain = result.OnMain || contained
	}
	for _, tag := range ev.Tags {
		version, ok := ParseTagVersion(tag.Name)
		if !ok {
			continue // malformed tags are ignored, never classified from
		}
		if tag.Commit == "" {
			result.Class = FixAmbiguous
			return result
		}
		for _, commit := range commits {
			contained, ok := ev.Reach[RefCommitKey{Ref: tag.Name, Commit: commit}]
			if !ok {
				result.Class = FixAmbiguous
				return result
			}
			if !contained {
				continue
			}
			if version.IsStable() {
				keepEarliest(&result.EarliestStable, version)
			} else {
				keepEarliest(&result.EarliestPrerelease, version)
			}
		}
	}
	switch {
	case result.EarliestStable != nil:
		result.Class = FixInStable
	case result.EarliestPrerelease != nil:
		result.Class = FixInPrerelease
	case result.OnMain:
		result.Class = FixOnMain
	default:
		// Fix commits exist but are contained in no tag and are not reachable
		// from the default branch (for example merged to a side branch), so no
		// availability claim is possible.
		result.Class = FixAmbiguous
	}
	return result
}

// keepEarliest stores version in target when target is nil or version sorts
// before the stored one.
func keepEarliest(target **Version, version Version) {
	if *target == nil || CompareVersions(version, **target) < 0 {
		*target = &version
	}
}

// CollectFixEvidence gathers the fix-availability evidence for one canonical
// match issue through the source. It never returns an error: failed or
// truncated fetches are recorded as incomplete facts so classification stays
// conservative. When no linked merged fix PR exists, tag and reachability
// queries are skipped entirely.
func CollectFixEvidence(ctx context.Context, source FixEvidenceSource, issueNumber int) FixEvidence {
	var ev FixEvidence
	prs, complete, err := source.MergedFixPRs(ctx, issueNumber)
	if err != nil || !complete {
		ev.LinkedPRs = prs
		return ev
	}
	ev.LinkedPRs = prs
	ev.LinksComplete = true
	if len(fixCommitSHAs(prs)) == 0 {
		// No fix to locate; tags and reachability are not needed.
		ev.TagsComplete = true
		ev.ReachComplete = true
		return ev
	}
	tags, tagsComplete, err := source.ListTags(ctx)
	if err != nil || !tagsComplete {
		ev.Tags = tags
		return ev
	}
	ev.Tags = tags
	ev.TagsComplete = true
	ev.Reach = make(map[RefCommitKey]bool)
	ev.ReachComplete = collectReachability(ctx, source, fixCommitSHAs(prs), tags, ev.Reach)
	return ev
}

// collectReachability resolves every needed (ref, fix commit) fact: the
// default branch first, then every valid SemVer tag in ascending version
// order. It reports false on the first failure, on a tag that cannot be
// compared, or when the query budget is exhausted; partial facts already
// recorded stay in reach but classification treats them as insufficient.
func collectReachability(ctx context.Context, source FixEvidenceSource, commits []string, tags []RepoTag, reach map[RefCommitKey]bool) bool {
	queries := 0
	for _, commit := range commits {
		if queries >= maxReachQueries {
			return false
		}
		queries++
		contained, err := source.CommitContainedIn(ctx, fixDefaultBranchRef, commit)
		if err != nil {
			return false
		}
		reach[RefCommitKey{Ref: fixDefaultBranchRef, Commit: commit}] = contained
	}
	for _, tag := range sortedVersionTags(tags) {
		if tag.Commit == "" {
			return false
		}
		for _, commit := range commits {
			if queries >= maxReachQueries {
				return false
			}
			queries++
			contained, err := source.CommitContainedIn(ctx, tag.Name, commit)
			if err != nil {
				return false
			}
			reach[RefCommitKey{Ref: tag.Name, Commit: commit}] = contained
		}
	}
	return true
}

// sortedVersionTags returns the valid SemVer tags sorted ascending by version.
// Malformed tag names are skipped because availability is never classified
// from them.
func sortedVersionTags(tags []RepoTag) []RepoTag {
	type parsedTag struct {
		tag     RepoTag
		version Version
	}
	var valid []parsedTag
	for _, tag := range tags {
		if version, ok := ParseTagVersion(tag.Name); ok {
			valid = append(valid, parsedTag{tag: tag, version: version})
		}
	}
	sort.Slice(valid, func(i, j int) bool {
		return CompareVersions(valid[i].version, valid[j].version) < 0
	})
	result := make([]RepoTag, 0, len(valid))
	for _, item := range valid {
		result = append(result, item.tag)
	}
	return result
}

// reporterVersionLabelPatterns match the "Engram Version" label line as the
// bug report form renders it (a bold label line) plus the plain heading form.
var reporterVersionLabelPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^\*\*Engram Version\*\*\s*$`),
	regexp.MustCompile(`(?m)^###\s*Engram Version\s*$`),
}

// extractReporterVersion returns the raw value line following the Engram
// Version label in an issue body, or "" when the label is absent. It does not
// validate the value: malformed values fail version parsing downstream and are
// treated as missing evidence, never an error.
func extractReporterVersion(body string) string {
	for _, pattern := range reporterVersionLabelPatterns {
		location := pattern.FindStringIndex(body)
		if location == nil {
			continue
		}
		for _, line := range strings.Split(body[location[1]:], "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
