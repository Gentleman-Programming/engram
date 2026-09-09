// Package triage implements conservative post-creation duplicate-issue
// detection for GitHub repositories. It scores deterministic lexical evidence
// (title-token similarity, shared distinctive terms, exact code/error snippet
// matches) between one issue and existing open/closed issues, and maintains a
// single anchored evidence comment plus one triage label.
//
// The detector never closes, rewrites, or consolidates issues: maintainers
// confirm (close as duplicate) or reject (remove the label) manually. It
// deliberately does not use status:* labels, which are maintainer-owned and
// gate PR checks.
package triage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	// LabelName marks an issue whose anchored comment lists possible duplicates.
	LabelName = "triage:possible-duplicate"
	// LabelColor is used when the bot creates LabelName on the repository.
	LabelColor = "d4c5f9"
	// CommentAnchor marks the bot-owned comment so re-runs update it in place
	// instead of posting new comments.
	CommentAnchor = "<!-- engram-triage-duplicates -->"
	// DefaultBotAuthor is the identity Actions assigns to GITHUB_TOKEN runs;
	// used when Options.BotAuthor is empty.
	DefaultBotAuthor = "github-actions[bot]"
)

// Issue is the minimal issue shape the detector needs.
type Issue struct {
	Number      int
	Title       string
	Body        string
	State       string // "open" or "closed"
	PullRequest bool   // search results can include PRs; filtered before scoring
	Labels      []string
}

// Comment is an existing issue comment.
type Comment struct {
	ID     int64
	Body   string
	Author string // user.login of the commenter; anchors are only honored from the bot identity
}

// Client is the GitHub surface the detector needs. Implementations must
// tolerate being called sequentially by Run and return errors for the caller
// to handle (the CLI logs a warning and exits 0).
type Client interface {
	// GetIssue fetches one issue including its current labels.
	GetIssue(ctx context.Context, number int) (Issue, error)
	// SearchIssues searches open and closed issues whose title or body match
	// the given query tokens. Pull requests may appear in results and must be
	// flagged via Issue.PullRequest.
	SearchIssues(ctx context.Context, queryTokens []string) ([]Issue, error)
	// ListComments lists comments on one issue, paging forward up to a
	// bounded cap. The boolean reports whether the full comment list was
	// established; false means the cap was hit, so absence of an anchored
	// comment cannot be proven.
	ListComments(ctx context.Context, number int) (comments []Comment, complete bool, err error)
	// CreateComment posts a new comment on one issue.
	CreateComment(ctx context.Context, number int, body string) error
	// UpdateComment edits an existing comment in place.
	UpdateComment(ctx context.Context, commentID int64, body string) error
	// EnsureLabel makes sure the label exists on the repository, creating it
	// when missing. Idempotent.
	EnsureLabel(ctx context.Context, name string) error
	// AddIssueLabel adds a label to one issue.
	AddIssueLabel(ctx context.Context, number int, name string) error
	// RemoveIssueLabel removes a label from one issue. A 404 (already absent)
	// is not an error.
	RemoveIssueLabel(ctx context.Context, number int, name string) error
}

// Options configures one duplicate-detection run for a single issue.
type Options struct {
	IssueNumber int
	Client      Client
	// BotAuthor is the login whose anchored comments the bot owns; empty means
	// DefaultBotAuthor.
	BotAuthor string
	// Log receives informational action lines; nil means silent.
	Log func(format string, args ...any)
}

// Run evaluates one issue against the repository's issues and reconciles the
// triage label and the anchored comment:
//
//   - No candidates: remove the label if present and leave a short
//     "no candidates remain" note in the anchored comment when one exists.
//   - Same candidates, label removed by a maintainer: respect the rejection
//     and do nothing.
//   - New or changed candidates: ensure the label exists, add it when absent,
//     and create or update the anchored comment in place.
//
// API and network errors are returned for the caller to tolerate.
func Run(ctx context.Context, opts Options) error {
	if opts.Client == nil {
		return errors.New("triage: nil client")
	}
	if opts.IssueNumber <= 0 {
		return errors.New("triage: issue number must be positive")
	}
	log := opts.Log
	if log == nil {
		log = func(string, ...any) {}
	}

	issue, err := opts.Client.GetIssue(ctx, opts.IssueNumber)
	if err != nil {
		return fmt.Errorf("get issue #%d: %w", opts.IssueNumber, err)
	}

	var matches []Match
	if tokens := SearchTokens(issue.Title); len(tokens) > 0 {
		found, err := opts.Client.SearchIssues(ctx, tokens)
		if err != nil {
			return fmt.Errorf("search issues: %w", err)
		}
		matches = RankCandidates(issue, found)
	}

	botAuthor := opts.BotAuthor
	if botAuthor == "" {
		botAuthor = DefaultBotAuthor
	}

	comments, commentsComplete, err := opts.Client.ListComments(ctx, opts.IssueNumber)
	if err != nil {
		return fmt.Errorf("list comments: %w", err)
	}
	anchored := findAnchoredComment(comments, botAuthor)
	// Being unable to prove the anchored comment's absence is not absence:
	// when the comment-page cap is exhausted, never create a comment or touch
	// labels.
	if anchored == nil && !commentsComplete {
		log("triage: cannot establish anchored-comment state within comment-page cap; skipping issue #%d", opts.IssueNumber)
		return nil
	}

	labelPresent := slices.Contains(issue.Labels, LabelName)

	if len(matches) == 0 {
		if labelPresent {
			if err := opts.Client.RemoveIssueLabel(ctx, opts.IssueNumber, LabelName); err != nil {
				return fmt.Errorf("remove label %q: %w", LabelName, err)
			}
			log("triage: removed label %s from issue #%d", LabelName, opts.IssueNumber)
		}
		if anchored != nil {
			note := RenderNoCandidatesComment()
			if anchored.Body != note {
				if err := opts.Client.UpdateComment(ctx, anchored.ID, note); err != nil {
					return fmt.Errorf("update anchored comment: %w", err)
				}
				log("triage: updated anchored comment on issue #%d: no candidates remain", opts.IssueNumber)
			}
		}
		return nil
	}

	// Rejection semantics: the maintainer removed the label while the anchored
	// comment still records this exact candidate set. Respect that until new
	// evidence (a changed set) appears.
	if !labelPresent && anchored != nil && sameCandidateSet(ParseRecordedCandidates(anchored.Body), matches) {
		log("triage: rejection respected on issue #%d; candidates unchanged", opts.IssueNumber)
		return nil
	}

	if !labelPresent {
		if err := opts.Client.EnsureLabel(ctx, LabelName); err != nil {
			return fmt.Errorf("ensure label %q: %w", LabelName, err)
		}
		if err := opts.Client.AddIssueLabel(ctx, opts.IssueNumber, LabelName); err != nil {
			return fmt.Errorf("add label %q: %w", LabelName, err)
		}
		log("triage: added label %s to issue #%d", LabelName, opts.IssueNumber)
	}

	body := RenderCandidatesComment(matches)
	if anchored == nil {
		if err := opts.Client.CreateComment(ctx, opts.IssueNumber, body); err != nil {
			return fmt.Errorf("create anchored comment: %w", err)
		}
		log("triage: created anchored comment on issue #%d with %d candidate(s)", opts.IssueNumber, len(matches))
	} else if anchored.Body != body {
		if err := opts.Client.UpdateComment(ctx, anchored.ID, body); err != nil {
			return fmt.Errorf("update anchored comment: %w", err)
		}
		log("triage: updated anchored comment on issue #%d with %d candidate(s)", opts.IssueNumber, len(matches))
	}
	return nil
}

// findAnchoredComment returns the bot-owned comment, if any. Only a comment
// authored by the bot identity and whose body STARTS with the anchor counts:
// anyone can quote the anchor mid-body, and a spoofed comment must stay
// invisible so the bot creates its own instead of patching an attacker's.
func findAnchoredComment(comments []Comment, botAuthor string) *Comment {
	for i := range comments {
		if strings.EqualFold(comments[i].Author, botAuthor) && strings.HasPrefix(comments[i].Body, CommentAnchor) {
			return &comments[i]
		}
	}
	return nil
}

// sameCandidateSet reports whether the recorded numbers exactly cover the
// freshly computed matches.
func sameCandidateSet(recorded []int, matches []Match) bool {
	if len(recorded) != len(matches) {
		return false
	}
	current := make(map[int]bool, len(matches))
	for _, match := range matches {
		current[match.Issue.Number] = true
	}
	for _, number := range recorded {
		if !current[number] {
			return false
		}
	}
	return true
}
