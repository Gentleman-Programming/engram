package triage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// fakeTriageClient records every Client call so tests can assert exactly which
// mutations Run performed.
type fakeTriageClient struct {
	issue          Issue
	search         []Issue
	searchErr      error
	comments       []Comment
	listIncomplete bool // cap exhausted: anchored-comment absence is unprovable

	calls         []string
	createdBodies []string
	updatedBodies map[int64]string
}

func (f *fakeTriageClient) GetIssue(ctx context.Context, number int) (Issue, error) {
	f.calls = append(f.calls, "get-issue")
	return f.issue, nil
}

func (f *fakeTriageClient) SearchIssues(ctx context.Context, queryTokens []string) ([]Issue, error) {
	f.calls = append(f.calls, "search")
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.search, nil
}

func (f *fakeTriageClient) ListComments(ctx context.Context, number int) ([]Comment, bool, error) {
	f.calls = append(f.calls, "list-comments")
	return f.comments, !f.listIncomplete, nil
}

func (f *fakeTriageClient) CreateComment(ctx context.Context, number int, body string) error {
	f.calls = append(f.calls, "create-comment")
	f.createdBodies = append(f.createdBodies, body)
	return nil
}

func (f *fakeTriageClient) UpdateComment(ctx context.Context, commentID int64, body string) error {
	f.calls = append(f.calls, fmt.Sprintf("update-comment:%d", commentID))
	if f.updatedBodies == nil {
		f.updatedBodies = map[int64]string{}
	}
	f.updatedBodies[commentID] = body
	return nil
}

func (f *fakeTriageClient) EnsureLabel(ctx context.Context, name string) error {
	f.calls = append(f.calls, "ensure-label")
	return nil
}

func (f *fakeTriageClient) AddIssueLabel(ctx context.Context, number int, name string) error {
	f.calls = append(f.calls, "add-label")
	return nil
}

func (f *fakeTriageClient) RemoveIssueLabel(ctx context.Context, number int, name string) error {
	f.calls = append(f.calls, "remove-label")
	return nil
}

// mutations returns the mutating calls in order, excluding reads.
func (f *fakeTriageClient) mutations() []string {
	var result []string
	for _, call := range f.calls {
		switch {
		case call == "create-comment",
			call == "ensure-label",
			call == "add-label",
			call == "remove-label",
			strings.HasPrefix(call, "update-comment"):
			result = append(result, call)
		}
	}
	return result
}

func TestRunReconcilesLabelAndComment(t *testing.T) {
	target := Issue{Number: 10, Title: "App crashes when saving large notes", Body: "It crashes every time."}
	candidateA := Issue{Number: 11, Title: "App crashes when saving a large note", State: "open"}
	candidateB := Issue{Number: 12, Title: "App crashes when saving large notes quickly", State: "closed"}
	botComment := func(body string) Comment {
		return Comment{ID: 101, Body: body, Author: DefaultBotAuthor}
	}

	tests := []struct {
		name                string
		issue               Issue
		search              []Issue
		comments            []Comment
		searchErr           error
		wantErr             bool
		wantMutations       []string
		wantCreatedContains string
		wantUpdatedContains string
	}{
		{
			name:                "first run: ensure label, add label, create anchored comment",
			issue:               target,
			search:              []Issue{candidateA},
			wantMutations:       []string{"ensure-label", "add-label", "create-comment"},
			wantCreatedContains: "- #11:",
		},
		{
			name:     "second run with unchanged candidates: complete no-op",
			issue:    withLabels(target, LabelName),
			search:   []Issue{candidateA},
			comments: []Comment{botComment(RenderCandidatesComment(RankCandidates(target, []Issue{candidateA})))},
		},
		{
			name:                "edited issue adds a candidate: comment updated in place, not duplicated",
			issue:               withLabels(target, LabelName),
			search:              []Issue{candidateA, candidateB},
			comments:            []Comment{botComment(RenderCandidatesComment(RankCandidates(target, []Issue{candidateA})))},
			wantMutations:       []string{"update-comment:101"},
			wantUpdatedContains: "- #12:",
		},
		{
			name:     "rejection respected: label removed, same candidate set, no-op",
			issue:    target, // label absent: maintainer removed it
			search:   []Issue{candidateA},
			comments: []Comment{botComment(RenderCandidatesComment(RankCandidates(target, []Issue{candidateA})))},
		},
		{
			name:                "new candidate after rejection: label re-added, comment updated",
			issue:               target,
			search:              []Issue{candidateB}, // set differs from recorded {11}
			comments:            []Comment{botComment(RenderCandidatesComment(RankCandidates(target, []Issue{candidateA})))},
			wantMutations:       []string{"ensure-label", "add-label", "update-comment:101"},
			wantUpdatedContains: "- #12:",
		},
		{
			name:  "no candidates anywhere: nothing happens",
			issue: target,
		},
		{
			name:                "candidates disappear: label removed, comment updated to note",
			issue:               withLabels(target, LabelName),
			comments:            []Comment{botComment(RenderCandidatesComment(RankCandidates(target, []Issue{candidateA})))},
			wantMutations:       []string{"remove-label", "update-comment:101"},
			wantUpdatedContains: "No duplicate candidates remain",
		},
		{
			name:     "note already current with no label: no-op",
			issue:    target,
			comments: []Comment{botComment(RenderNoCandidatesComment())},
		},
		{
			name:      "search failure propagates without mutations",
			issue:     target,
			searchErr: errors.New("GET https://api.github.com/search/issues: 429"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeTriageClient{
				issue:     tt.issue,
				search:    tt.search,
				searchErr: tt.searchErr,
				comments:  tt.comments,
			}
			err := Run(context.Background(), Options{IssueNumber: tt.issue.Number, Client: fake})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Run succeeded, want error")
				}
			} else if err != nil {
				t.Fatalf("Run: %v", err)
			}

			got := fake.mutations()
			if len(got) != len(tt.wantMutations) {
				t.Fatalf("mutations = %v, want %v", got, tt.wantMutations)
			}
			for i := range tt.wantMutations {
				if got[i] != tt.wantMutations[i] {
					t.Fatalf("mutations = %v, want %v", got, tt.wantMutations)
				}
			}

			if tt.wantCreatedContains != "" {
				if len(fake.createdBodies) == 0 || !strings.Contains(fake.createdBodies[len(fake.createdBodies)-1], tt.wantCreatedContains) {
					t.Fatalf("created bodies = %v, want one containing %q", fake.createdBodies, tt.wantCreatedContains)
				}
			}
			if tt.wantUpdatedContains != "" {
				body, ok := fake.updatedBodies[101]
				if !ok || !strings.Contains(body, tt.wantUpdatedContains) {
					t.Fatalf("updated body = %q, want one containing %q", body, tt.wantUpdatedContains)
				}
			}
		})
	}
}

func withLabels(issue Issue, labels ...string) Issue {
	issue.Labels = labels
	return issue
}

func TestRunRejectsBadOptions(t *testing.T) {
	if err := Run(context.Background(), Options{}); err == nil {
		t.Fatal("expected error for nil client")
	}
	if err := Run(context.Background(), Options{Client: &fakeTriageClient{}}); err == nil {
		t.Fatal("expected error for non-positive issue number")
	}
}

func TestRunExcludesSelfAndPullRequestsFromSearchResults(t *testing.T) {
	target := Issue{Number: 10, Title: "App crashes when saving large notes"}
	fake := &fakeTriageClient{
		issue: target,
		search: []Issue{
			{Number: 10, Title: "App crashes when saving large notes", State: "open"},                            // self
			{Number: 12, Title: "App crashes when saving large notes again", State: "closed", PullRequest: true}, // PR
			{Number: 11, Title: "App crashes when saving a large note", State: "open"},
		},
	}
	if err := Run(context.Background(), Options{IssueNumber: 10, Client: fake}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.createdBodies) != 1 {
		t.Fatalf("expected exactly one created comment, got %d", len(fake.createdBodies))
	}
	body := fake.createdBodies[0]
	if strings.Contains(body, "#12:") || strings.Contains(body, "#10:") {
		t.Errorf("comment must exclude self and pull requests:\n%s", body)
	}
	if !strings.Contains(body, "#11:") {
		t.Errorf("comment must include the accepted candidate:\n%s", body)
	}
}

// spoofedComment builds a non-bot comment that plants the anchor, as any
// commenter can do.
func spoofedComment(body string) Comment {
	return Comment{ID: 999, Body: body, Author: "attacker"}
}

func TestRunIgnoresAnchoredCommentsFromOtherAuthors(t *testing.T) {
	target := Issue{Number: 10, Title: "App crashes when saving large notes"}
	candidateA := Issue{Number: 11, Title: "App crashes when saving a large note", State: "open"}
	fake := &fakeTriageClient{
		issue:    target,
		search:   []Issue{candidateA},
		comments: []Comment{spoofedComment(CommentAnchor + "\n### Possible duplicates detected\n\n- #77: **fake candidate** (open) — 99% title similarity\n")},
	}
	if err := Run(context.Background(), Options{IssueNumber: 10, Client: fake}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, patched := fake.updatedBodies[999]; patched {
		t.Fatal("bot must never update a comment authored by someone else")
	}
	if len(fake.createdBodies) != 1 {
		t.Fatalf("bot must create its own anchored comment, created %d", len(fake.createdBodies))
	}
	if want := []string{"ensure-label", "add-label", "create-comment"}; !reflect.DeepEqual(fake.mutations(), want) {
		t.Fatalf("mutations = %v, want %v", fake.mutations(), want)
	}
}

func TestRunSpoofedCandidateLinesDoNotDefeatRejection(t *testing.T) {
	target := Issue{Number: 10, Title: "App crashes when saving large notes"}
	candidateA := Issue{Number: 11, Title: "App crashes when saving a large note", State: "open"}
	fake := &fakeTriageClient{
		issue:  target, // label absent: maintainer rejected
		search: []Issue{candidateA},
		comments: []Comment{
			spoofedComment(CommentAnchor + "\n### Possible duplicates detected\n\n- #77: **fake candidate** (open)\n"),
			{ID: 101, Body: RenderCandidatesComment(RankCandidates(target, []Issue{candidateA})), Author: DefaultBotAuthor},
		},
	}
	if err := Run(context.Background(), Options{IssueNumber: 10, Client: fake}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := fake.mutations(); len(got) != 0 {
		t.Fatalf("rejection must be respected via the bot's own comment; mutations = %v", got)
	}
	if _, patched := fake.updatedBodies[999]; patched {
		t.Fatal("spoofed comment must be ignored entirely")
	}
}

func TestRunHonorsExplicitBotAuthorOption(t *testing.T) {
	target := Issue{Number: 10, Title: "App crashes when saving large notes"}
	candidateA := Issue{Number: 11, Title: "App crashes when saving a large note", State: "open"}
	rendered := RenderCandidatesComment(RankCandidates(target, []Issue{candidateA}))
	fake := &fakeTriageClient{
		issue:    withLabels(target, LabelName),
		search:   []Issue{candidateA},
		comments: []Comment{{ID: 55, Body: rendered, Author: "custom-triage-bot"}},
	}
	if err := Run(context.Background(), Options{IssueNumber: 10, Client: fake, BotAuthor: "custom-triage-bot"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := fake.mutations(); len(got) != 0 {
		t.Fatalf("custom bot identity must own its anchored comment; mutations = %v", got)
	}
}

func TestRunAnchorMustBeAPrefixNotMidBody(t *testing.T) {
	target := Issue{Number: 10, Title: "App crashes when saving large notes"}
	candidateA := Issue{Number: 11, Title: "App crashes when saving a large note", State: "open"}
	// Bot-authored comment whose anchor is buried mid-body must not count as
	// the anchored comment (prefix contract).
	fake := &fakeTriageClient{
		issue:    withLabels(target, LabelName),
		search:   []Issue{candidateA},
		comments: []Comment{{ID: 101, Body: "noise before " + CommentAnchor + "\n- #11: x\n", Author: DefaultBotAuthor}},
	}
	if err := Run(context.Background(), Options{IssueNumber: 10, Client: fake}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.createdBodies) != 1 {
		t.Fatalf("mid-body anchor must not match; bot creates its own comment, created %d", len(fake.createdBodies))
	}
	if _, patched := fake.updatedBodies[101]; patched {
		t.Fatal("mid-body anchor comment must not be updated")
	}
}

func TestRunSkipsMutationsWhenCommentStateUnprovable(t *testing.T) {
	target := Issue{Number: 10, Title: "App crashes when saving large notes"}
	candidateA := Issue{Number: 11, Title: "App crashes when saving a large note", State: "open"}
	// Comment-page cap exhausted without finding the anchored comment: the bot
	// must not create comments or touch labels (unprovable is not absent).
	fake := &fakeTriageClient{issue: target, search: []Issue{candidateA}, listIncomplete: true}
	if err := Run(context.Background(), Options{IssueNumber: 10, Client: fake}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := fake.mutations(); len(got) != 0 {
		t.Fatalf("unprovable anchored-comment state must cause zero mutations, got %v", got)
	}
}
