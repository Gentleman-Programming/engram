package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const testToken = "test-token"

func newTestServer(t *testing.T, handler http.HandlerFunc) *RESTClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewRESTClient(server.URL+"/", "owner/repo", testToken, server.Client())
}

func requireHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want Bearer token", got)
	}
	if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got)
	}
	if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version = %q", got)
	}
}

func searchItemsJSON(first, count int) string {
	var b strings.Builder
	b.WriteString(`{"total_count": 100, "items": [`)
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"number": %d, "title": "issue %d", "body": "body %d", "state": "closed"}`, first+i, first+i, first+i)
	}
	b.WriteString(`]}`)
	return b.String()
}

func TestRESTClientSearchIssuesSingleDeterministicWindow(t *testing.T) {
	var requests atomic.Int32
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		requireHeaders(t, r)
		if r.URL.Path != "/search/issues" {
			t.Errorf("path = %q, want /search/issues", r.URL.Path)
		}
		q := r.URL.Query().Get("q")
		for _, want := range []string{"repo:owner/repo", "is:issue", "in:title,body", "crash", "saving"} {
			if !strings.Contains(q, want) {
				t.Errorf("query %q missing %q", q, want)
			}
		}
		for param, want := range map[string]string{"sort": "created", "order": "desc", "per_page": "100"} {
			if got := r.URL.Query().Get(param); got != want {
				t.Errorf("%s = %q, want %q", param, got, want)
			}
		}
		if page := r.URL.Query().Get("page"); page != "" && page != "1" {
			t.Errorf("page = %q, only the first page may be requested", page)
		}
		_, _ = w.Write([]byte(searchItemsJSON(1, searchPageSize)))
	})

	issues, err := client.SearchIssues(context.Background(), []string{"crash", "saving"})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(issues) != searchPageSize {
		t.Fatalf("got %d issues, want %d", len(issues), searchPageSize)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("a full first page must not trigger a second request, got %d requests", got)
	}
}

func TestRESTClientSearchIssuesToleratedStatuses(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			})
			if _, err := client.SearchIssues(context.Background(), []string{"crash"}); err == nil {
				t.Fatalf("expected error for status %d", status)
			} else if !strings.Contains(err.Error(), fmt.Sprintf("%d", status)) {
				t.Errorf("error should mention status %d, got: %v", status, err)
			}
		})
	}
}

func TestRESTClientGetIssue(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireHeaders(t, r)
		if r.URL.Path != "/repos/owner/repo/issues/5" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"number": 5, "title": "Crash", "body": "boom", "state": "open", "labels": [{"name": "triage:possible-duplicate"}]}`))
	})

	issue, err := client.GetIssue(context.Background(), 5)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Number != 5 || issue.Title != "Crash" || issue.State != "open" {
		t.Errorf("unexpected issue: %+v", issue)
	}
	if len(issue.Labels) != 1 || issue.Labels[0] != LabelName {
		t.Errorf("labels = %v, want [%s]", issue.Labels, LabelName)
	}
	if issue.PullRequest {
		t.Error("plain issue must not be flagged as pull request")
	}
}

func TestRESTClientGetIssueDetectsPullRequest(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"number": 7, "title": "PR-ish", "body": "", "state": "open", "pull_request": {}}`))
	})
	issue, err := client.GetIssue(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if !issue.PullRequest {
		t.Error("issue with pull_request field must be flagged")
	}
}

func TestRESTClientComments(t *testing.T) {
	var path string
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		switch {
		case r.Method == http.MethodGet && path == "/repos/owner/repo/issues/5/comments":
			if got := r.URL.Query().Get("per_page"); got != "100" {
				t.Errorf("per_page = %q, want 100", got)
			}
			_, _ = w.Write([]byte(`[{"id": 101, "body": "<!-- engram-triage-duplicates -->\nold", "user": {"login": "github-actions[bot]"}}]`))
		case r.Method == http.MethodPost && path == "/repos/owner/repo/issues/5/comments":
			var payload struct{ Body string }
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			if payload.Body != "new body" {
				t.Errorf("created body = %q", payload.Body)
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && path == "/repos/owner/repo/issues/comments/101":
			var payload struct{ Body string }
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode update body: %v", err)
			}
			if payload.Body != "updated body" {
				t.Errorf("updated body = %q", payload.Body)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})

	comments, complete, err := client.ListComments(context.Background(), 5)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if !complete {
		t.Fatal("a short first page must report a complete comment list")
	}
	if len(comments) != 1 || comments[0].ID != 101 || !strings.Contains(comments[0].Body, CommentAnchor) {
		t.Fatalf("comments = %+v", comments)
	}
	if comments[0].Author != "github-actions[bot]" {
		t.Fatalf("comment author = %q, want the bot login mapped from user.login", comments[0].Author)
	}
	if err := client.CreateComment(context.Background(), 5, "new body"); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if err := client.UpdateComment(context.Background(), 101, "updated body"); err != nil {
		t.Fatalf("UpdateComment: %v", err)
	}
}

// fillerCommentsJSON builds a full page of non-bot comments.
func fillerCommentsJSON(count int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id": %d, "body": "filler %d", "user": {"login": "someone"}}`, i+1, i+1)
	}
	b.WriteString("]")
	return b.String()
}

func TestRESTClientListCommentsPaginatesUntilShortPage(t *testing.T) {
	var requests atomic.Int32
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(fillerCommentsJSON(commentsPageSize)))
			return
		}
		_, _ = w.Write([]byte(`[{"id": 101, "body": "` + CommentAnchor + `\nfound", "user": {"login": "github-actions[bot]"}}]`))
	})

	comments, complete, err := client.ListComments(context.Background(), 5)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if !complete {
		t.Fatal("a short page must report a complete comment list")
	}
	if len(comments) != commentsPageSize+1 {
		t.Fatalf("got %d comments, want %d", len(comments), commentsPageSize+1)
	}
	last := comments[len(comments)-1]
	if last.ID != 101 || last.Author != DefaultBotAuthor || !strings.HasPrefix(last.Body, CommentAnchor) {
		t.Fatalf("anchored bot comment on page 2 must be found, got %+v", last)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("expected 2 comment pages, got %d requests", got)
	}
}

// TestRESTClientListCommentsCapExhaustionIsIncomplete exercises the bounded
// cap (maxCommentPages pages of commentsPageSize items, never a short page).
func TestRESTClientListCommentsCapExhaustionIsIncomplete(t *testing.T) {
	var requests atomic.Int32
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(fillerCommentsJSON(commentsPageSize)))
	})

	comments, complete, err := client.ListComments(context.Background(), 5)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if complete {
		t.Fatal("cap exhaustion without a short page must report incomplete")
	}
	if len(comments) != maxCommentPages*commentsPageSize {
		t.Fatalf("got %d comments, want %d", len(comments), maxCommentPages*commentsPageSize)
	}
	if got := requests.Load(); got != int32(maxCommentPages) {
		t.Errorf("expected %d comment pages, got %d requests", maxCommentPages, got)
	}
}

func TestRESTClientEnsureLabel(t *testing.T) {
	t.Run("existing label is not recreated", func(t *testing.T) {
		var posts atomic.Int32
		client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/labels/triage:possible-duplicate":
				_, _ = w.Write([]byte(`{"name": "triage:possible-duplicate", "color": "d4c5f9"}`))
			default:
				posts.Add(1)
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
		})
		if err := client.EnsureLabel(context.Background(), LabelName); err != nil {
			t.Fatalf("EnsureLabel: %v", err)
		}
		if posts.Load() != 0 {
			t.Error("label exists; no POST expected")
		}
	})

	t.Run("missing label is created with the constant color", func(t *testing.T) {
		client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method != http.MethodPost || r.URL.Path != "/repos/owner/repo/labels" {
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				return
			}
			var payload struct {
				Name        string
				Color       string
				Description string
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode create label: %v", err)
			}
			if payload.Name != LabelName || payload.Color != LabelColor || payload.Description == "" {
				t.Errorf("create label payload = %+v", payload)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		})
		if err := client.EnsureLabel(context.Background(), LabelName); err != nil {
			t.Fatalf("EnsureLabel: %v", err)
		}
	})
}

func TestRESTClientAddAndRemoveIssueLabel(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/5/labels":
			var payload struct{ Labels []string }
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode add label: %v", err)
			}
			if len(payload.Labels) != 1 || payload.Labels[0] != LabelName {
				t.Errorf("labels = %v", payload.Labels)
			}
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/issues/5/labels/triage:possible-duplicate":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	if err := client.AddIssueLabel(context.Background(), 5, LabelName); err != nil {
		t.Fatalf("AddIssueLabel: %v", err)
	}
	if err := client.RemoveIssueLabel(context.Background(), 5, LabelName); err != nil {
		t.Fatalf("RemoveIssueLabel: %v", err)
	}
}

func TestRESTClientRemoveIssueLabelToleratesMissing(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if err := client.RemoveIssueLabel(context.Background(), 5, LabelName); err != nil {
		t.Errorf("404 on label removal must be tolerated, got: %v", err)
	}
}

func TestNewRESTClientNormalizesBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"number": 5, "title": "t", "body": "", "state": "open"}`))
	}))
	t.Cleanup(server.Close)

	client := NewRESTClient(server.URL, "o/r", "tok", server.Client()) // no trailing slash
	if _, err := client.GetIssue(context.Background(), 5); err != nil {
		t.Fatalf("baseURL without trailing slash should still work: %v", err)
	}
}

// fillerTimelineJSON builds a full page of irrelevant timeline events.
func fillerTimelineJSON(count int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"event": "commented", "body": "filler %d"}`, i+1)
	}
	b.WriteString("]")
	return b.String()
}

func TestRESTClientMergedFixPRs(t *testing.T) {
	t.Run("timeline cross-references resolve to merged PRs only", func(t *testing.T) {
		client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			requireHeaders(t, r)
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/11/timeline":
				if got := r.URL.Query().Get("per_page"); got != "100" {
					t.Errorf("per_page = %q, want 100", got)
				}
				_, _ = w.Write([]byte(`[
					{"event": "closed", "commit_id": "abc"},
					{"event": "cross-referenced", "source": {"issue": {"number": 23, "title": "A plain issue"}}},
					{"event": "cross-referenced", "source": {"issue": {"number": 22, "pull_request": {}}}},
					{"event": "cross-referenced", "source": {"issue": {"number": 21, "pull_request": [{}]}}}
				]`))
			case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls/21":
				_, _ = w.Write([]byte(`{"number": 21, "merged": true, "merge_commit_sha": "fix-on-main"}`))
			case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls/22":
				_, _ = w.Write([]byte(`{"number": 22, "merged": false, "merge_commit_sha": null}`))
			default:
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
		})

		prs, complete, err := client.MergedFixPRs(context.Background(), 11)
		if err != nil {
			t.Fatalf("MergedFixPRs: %v", err)
		}
		if !complete {
			t.Fatal("a short first page must report a complete timeline")
		}
		// Issue 23 is not a pull request (never fetched), 22 is not merged:
		// only 21 with its merge commit is fix evidence.
		want := []FixPR{{Number: 21, MergeCommit: "fix-on-main"}}
		if !reflect.DeepEqual(prs, want) {
			t.Fatalf("prs = %+v, want %+v", prs, want)
		}
	})

	t.Run("paginates until a short page", func(t *testing.T) {
		var requests atomic.Int32
		client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/owner/repo/issues/11/timeline":
				requests.Add(1)
				if r.URL.Query().Get("page") == "1" {
					_, _ = w.Write([]byte(fillerTimelineJSON(timelinePageSize)))
					return
				}
				_, _ = w.Write([]byte(`[{"event": "cross-referenced", "source": {"issue": {"number": 21, "pull_request": {}}}}]`))
			case "/repos/owner/repo/pulls/21":
				_, _ = w.Write([]byte(`{"number": 21, "merged": true, "merge_commit_sha": "fix-on-main"}`))
			default:
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
		})

		prs, complete, err := client.MergedFixPRs(context.Background(), 11)
		if err != nil {
			t.Fatalf("MergedFixPRs: %v", err)
		}
		if !complete {
			t.Fatal("a short page must report a complete timeline")
		}
		if len(prs) != 1 || prs[0].Number != 21 {
			t.Fatalf("prs = %+v, want the page-2 cross-reference", prs)
		}
		if got := requests.Load(); got != 2 {
			t.Errorf("expected 2 timeline pages, got %d requests", got)
		}
	})

	t.Run("cap exhaustion reports incomplete without guessing", func(t *testing.T) {
		var requests atomic.Int32
		client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			_, _ = w.Write([]byte(fillerTimelineJSON(timelinePageSize)))
		})

		if _, complete, err := client.MergedFixPRs(context.Background(), 11); err != nil || complete {
			t.Fatalf("cap exhaustion must be complete=false, err=nil; got complete=%t err=%v", complete, err)
		}
		if got := requests.Load(); got != int32(maxTimelinePages) {
			t.Errorf("expected %d timeline pages, got %d", maxTimelinePages, got)
		}
	})

	t.Run("API failure is an error", func(t *testing.T) {
		client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		if _, _, err := client.MergedFixPRs(context.Background(), 11); err == nil {
			t.Fatal("429 must be an error")
		}
	})
}

// fillerTagsJSON builds a full page of tags.
func fillerTagsJSON(count int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name": "v0.0.%d", "commit": {"sha": "t%d"}}`, i+1, i+1)
	}
	b.WriteString("]")
	return b.String()
}

func TestRESTClientListTags(t *testing.T) {
	t.Run("paginates until a short page", func(t *testing.T) {
		client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			requireHeaders(t, r)
			if r.URL.Path != "/repos/owner/repo/tags" {
				t.Errorf("path = %q, want /repos/owner/repo/tags", r.URL.Path)
			}
			if got := r.URL.Query().Get("per_page"); got != "100" {
				t.Errorf("per_page = %q, want 100", got)
			}
			if r.URL.Query().Get("page") == "1" {
				_, _ = w.Write([]byte(fillerTagsJSON(tagsPageSize)))
				return
			}
			_, _ = w.Write([]byte(`[{"name": "v2.0.0-rc.1", "commit": {"sha": "rc-sha"}}]`))
		})

		tags, complete, err := client.ListTags(context.Background())
		if err != nil {
			t.Fatalf("ListTags: %v", err)
		}
		if !complete {
			t.Fatal("a short page must report a complete tag list")
		}
		if len(tags) != tagsPageSize+1 {
			t.Fatalf("got %d tags, want %d", len(tags), tagsPageSize+1)
		}
		last := tags[len(tags)-1]
		if last.Name != "v2.0.0-rc.1" || last.Commit != "rc-sha" {
			t.Fatalf("last tag = %+v, want the page-2 tag with its commit SHA", last)
		}
	})

	t.Run("cap exhaustion reports incomplete", func(t *testing.T) {
		var requests atomic.Int32
		client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			_, _ = w.Write([]byte(fillerTagsJSON(tagsPageSize)))
		})

		tags, complete, err := client.ListTags(context.Background())
		if err != nil {
			t.Fatalf("ListTags: %v", err)
		}
		if complete {
			t.Fatal("cap exhaustion without a short page must report incomplete")
		}
		if len(tags) != maxTagPages*tagsPageSize {
			t.Fatalf("got %d tags, want %d", len(tags), maxTagPages*tagsPageSize)
		}
		if got := requests.Load(); got != int32(maxTagPages) {
			t.Errorf("expected %d tag pages, got %d", maxTagPages, got)
		}
	})

	t.Run("API failure is an error", func(t *testing.T) {
		client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		if _, _, err := client.ListTags(context.Background()); err == nil {
			t.Fatal("403 must be an error")
		}
	})
}

// TestRESTClientCommitContainedInStatuses pins the exact compare statuses
// accepted as proof of ancestry: "behind" or "identical" (tag as base, fix
// commit as head) prove containment; "ahead" and "diverged" prove absence;
// anything else is an ambiguous answer and must be an error.
func TestRESTClientCommitContainedInStatuses(t *testing.T) {
	tests := []struct {
		status  string
		want    bool
		wantErr bool
	}{
		{status: "behind", want: true},
		{status: "identical", want: true},
		{status: "ahead", want: false},
		{status: "diverged", want: false},
		{status: "", wantErr: true},
		{status: "confused", wantErr: true},
	}
	for _, tt := range tests {
		t.Run("status "+tt.status, func(t *testing.T) {
			client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				requireHeaders(t, r)
				if want := "/repos/owner/repo/compare/v1.0.0...abc123"; r.URL.Path != want {
					t.Errorf("path = %q, want %q", r.URL.Path, want)
				}
				_, _ = fmt.Fprintf(w, `{"status": %q}`, tt.status)
			})

			contained, err := client.CommitContainedIn(context.Background(), "v1.0.0", "abc123")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("status %q must be an error, got contained=%t", tt.status, contained)
				}
				return
			}
			if err != nil {
				t.Fatalf("status %q: %v", tt.status, err)
			}
			if contained != tt.want {
				t.Errorf("status %q: contained = %t, want %t", tt.status, contained, tt.want)
			}
		})
	}

	t.Run("HTTP failure is an error", func(t *testing.T) {
		client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if _, err := client.CommitContainedIn(context.Background(), "v1.0.0", "abc123"); err == nil {
			t.Fatal("500 must be an error")
		}
	})
}
