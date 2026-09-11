package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// searchPageSize bounds one duplicate search to a single request over the
	// 100 most recent matches: multi-page windows are non-deterministic when
	// new issues land between requests.
	searchPageSize = 100
	// commentsPageSize is the per-page size for forward comment pagination.
	commentsPageSize = 100
	// maxCommentPages bounds forward comment pagination; beyond it, absence of
	// an anchored comment is unprovable.
	maxCommentPages = 10
	// timelinePageSize and maxTimelinePages bound issue-timeline pagination
	// when collecting cross-referenced pull requests; beyond the cap, the
	// cross-reference set is unprovable.
	timelinePageSize = 100
	maxTimelinePages = 10
	// tagsPageSize and maxTagPages bound tag pagination; beyond the cap, the
	// tag list is unprovable and fix availability stays conservative.
	tagsPageSize = 100
	maxTagPages  = 10
	// requestTimeout bounds each individual HTTP request.
	requestTimeout = 30 * time.Second
	// githubAPIVersion is the stable REST API version header.
	githubAPIVersion = "2022-11-28"
)

// RESTClient is a stdlib-only GitHub REST client for one repository. It
// implements both Client and FixEvidenceSource.
type RESTClient struct {
	baseURL    string
	repo       string
	token      string
	httpClient *http.Client
}

// NewRESTClient builds a RESTClient. baseURL is the API root (default
// https://api.github.com/); a nil httpClient gets a request timeout.
func NewRESTClient(baseURL, repo, token string, httpClient *http.Client) *RESTClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	return &RESTClient{baseURL: baseURL, repo: repo, token: token, httpClient: httpClient}
}

// GetIssue fetches one issue including its current labels.
func (c *RESTClient) GetIssue(ctx context.Context, number int) (Issue, error) {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("repos/%s/issues/%d", c.repo, number), nil, nil)
	if err != nil {
		return Issue{}, err
	}
	var payload ghIssue
	if err := decodeResponse(resp, &payload); err != nil {
		return Issue{}, err
	}
	return payload.toIssue(), nil
}

// SearchIssues searches open and closed issues matching the query tokens
// over a bounded, deterministic window: a single request returning the 100
// most recent matches (sort=created, order=desc). Results may include pull
// requests (flagged via Issue.PullRequest) which the caller filters.
func (c *RESTClient) SearchIssues(ctx context.Context, queryTokens []string) ([]Issue, error) {
	query := url.Values{}
	query.Set("q", fmt.Sprintf("repo:%s is:issue in:title,body %s", c.repo, strings.Join(queryTokens, " ")))
	query.Set("sort", "created")
	query.Set("order", "desc")
	query.Set("per_page", strconv.Itoa(searchPageSize))
	resp, err := c.do(ctx, http.MethodGet, "search/issues", query, nil)
	if err != nil {
		return nil, err
	}
	var result ghSearchResult
	if err := decodeResponse(resp, &result); err != nil {
		return nil, err
	}
	issues := make([]Issue, 0, len(result.Items))
	for _, item := range result.Items {
		issues = append(issues, item.toIssue())
	}
	return issues, nil
}

// ListComments lists comments on one issue, paging forward until a short
// page or the maxCommentPages cap. The returned boolean reports whether the
// full comment list was established; false means the cap was hit, so absence
// of an anchored bot comment cannot be proven.
func (c *RESTClient) ListComments(ctx context.Context, number int) ([]Comment, bool, error) {
	var comments []Comment
	for page := 1; page <= maxCommentPages; page++ {
		query := url.Values{}
		query.Set("per_page", strconv.Itoa(commentsPageSize))
		query.Set("page", strconv.Itoa(page))
		resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("repos/%s/issues/%d/comments", c.repo, number), query, nil)
		if err != nil {
			return nil, false, err
		}
		var payload []ghComment
		if err := decodeResponse(resp, &payload); err != nil {
			return nil, false, err
		}
		for _, item := range payload {
			author := ""
			if item.User != nil {
				author = item.User.Login
			}
			comments = append(comments, Comment{ID: item.ID, Body: item.Body, Author: author})
		}
		if len(payload) < commentsPageSize {
			return comments, true, nil
		}
	}
	return comments, false, nil
}

// CreateComment posts a new comment on one issue.
func (c *RESTClient) CreateComment(ctx context.Context, number int, body string) error {
	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/issues/%d/comments", c.repo, number), nil, ghCommentBody{Body: body})
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

// UpdateComment edits an existing comment in place.
func (c *RESTClient) UpdateComment(ctx context.Context, commentID int64, body string) error {
	resp, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("repos/%s/issues/comments/%d", c.repo, commentID), nil, ghCommentBody{Body: body})
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

// EnsureLabel makes sure the label exists on the repository, creating it with
// LabelColor when missing. Idempotent.
func (c *RESTClient) EnsureLabel(ctx context.Context, name string) error {
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("repos/%s/labels/%s", c.repo, labelPathEscape(name)), nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body) // drain 404 body before close
		_ = resp.Body.Close()
		resp, err = c.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/labels", c.repo), nil, ghLabelCreate{
			Name:        name,
			Color:       LabelColor,
			Description: "Possible duplicate — set by the triage bot; remove to reject",
		})
		if err != nil {
			return err
		}
	}
	return decodeResponse(resp, nil)
}

// AddIssueLabel adds a label to one issue.
func (c *RESTClient) AddIssueLabel(ctx context.Context, number int, name string) error {
	resp, err := c.do(ctx, http.MethodPost, fmt.Sprintf("repos/%s/issues/%d/labels", c.repo, number), nil, ghLabelAdd{Labels: []string{name}})
	if err != nil {
		return err
	}
	return decodeResponse(resp, nil)
}

// RemoveIssueLabel removes a label from one issue; 404 (already absent) is not
// an error.
func (c *RESTClient) RemoveIssueLabel(ctx context.Context, number int, name string) error {
	resp, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("repos/%s/issues/%d/labels/%s", c.repo, number, labelPathEscape(name)), nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body) // drain 404 body before close
		_ = resp.Body.Close()
		return nil
	}
	return decodeResponse(resp, nil)
}

// labelPathEscape escapes a label name for use as a URL path segment.
func labelPathEscape(name string) string {
	return url.PathEscape(name)
}

// MergedFixPRs returns the merged pull requests cross-referenced from the
// issue's timeline (explicit links only; prose mentions never count) with
// their merge commit SHAs, and whether the full timeline was established
// within the page cap. Non-merged pull requests are skipped because they are
// not fix evidence; a cap-exhausted timeline yields incomplete rather than a
// guessed subset.
func (c *RESTClient) MergedFixPRs(ctx context.Context, number int) ([]FixPR, bool, error) {
	seen := make(map[int]bool)
	var numbers []int
	complete := false
	for page := 1; page <= maxTimelinePages; page++ {
		query := url.Values{}
		query.Set("per_page", strconv.Itoa(timelinePageSize))
		query.Set("page", strconv.Itoa(page))
		resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("repos/%s/issues/%d/timeline", c.repo, number), query, nil)
		if err != nil {
			return nil, false, err
		}
		var events []ghTimelineEvent
		if err := decodeResponse(resp, &events); err != nil {
			return nil, false, err
		}
		for _, event := range events {
			if prNumber, ok := event.crossReferencedPR(); ok && !seen[prNumber] {
				seen[prNumber] = true
				numbers = append(numbers, prNumber)
			}
		}
		if len(events) < timelinePageSize {
			complete = true
			break
		}
	}
	if !complete {
		return nil, false, nil
	}
	prs := make([]FixPR, 0, len(numbers))
	for _, prNumber := range numbers {
		resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("repos/%s/pulls/%d", c.repo, prNumber), nil, nil)
		if err != nil {
			return nil, false, err
		}
		var payload ghPullRequest
		if err := decodeResponse(resp, &payload); err != nil {
			return nil, false, err
		}
		if !payload.Merged {
			continue
		}
		prs = append(prs, FixPR{Number: payload.Number, MergeCommit: payload.MergeCommitSHA})
	}
	return prs, true, nil
}

// ListTags returns repository tags with the commit SHA each points to, paging
// forward until a short page or the maxTagPages cap. The boolean reports
// whether the full tag list was established.
func (c *RESTClient) ListTags(ctx context.Context) ([]RepoTag, bool, error) {
	var tags []RepoTag
	for page := 1; page <= maxTagPages; page++ {
		query := url.Values{}
		query.Set("per_page", strconv.Itoa(tagsPageSize))
		query.Set("page", strconv.Itoa(page))
		resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("repos/%s/tags", c.repo), query, nil)
		if err != nil {
			return nil, false, err
		}
		var payload []ghTag
		if err := decodeResponse(resp, &payload); err != nil {
			return nil, false, err
		}
		for _, item := range payload {
			commit := ""
			if item.Commit != nil {
				commit = item.Commit.SHA
			}
			tags = append(tags, RepoTag{Name: item.Name, Commit: commit})
		}
		if len(payload) < tagsPageSize {
			return tags, true, nil
		}
	}
	return tags, false, nil
}

// CommitContainedIn reports whether commitSHA is reachable from baseRef (a tag
// name or the default branch) using GitHub compare semantics: with baseRef as
// base and commitSHA as head, only "behind" or "identical" prove ancestry.
// "ahead" and "diverged" prove absence; any other status is an ambiguous
// answer and an error, so callers treat it as unknown.
func (c *RESTClient) CommitContainedIn(ctx context.Context, baseRef, commitSHA string) (bool, error) {
	path := fmt.Sprintf("repos/%s/compare/%s...%s", c.repo, url.PathEscape(baseRef), url.PathEscape(commitSHA))
	resp, err := c.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return false, err
	}
	var payload ghCompare
	if err := decodeResponse(resp, &payload); err != nil {
		return false, err
	}
	switch payload.Status {
	case "behind", "identical":
		return true, nil
	case "ahead", "diverged":
		return false, nil
	default:
		return false, fmt.Errorf("triage: ambiguous compare status %q for %s...%s", payload.Status, baseRef, commitSHA)
	}
}

// do issues one API request with the standard GitHub headers.
func (c *RESTClient) do(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	target := c.baseURL + path
	if query != nil {
		target += "?" + query.Encode()
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("triage: encode request body: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return nil, fmt.Errorf("triage: build request %s %s: %w", method, path, err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return c.httpClient.Do(request)
}

// decodeResponse closes the response body and decodes a 2xx response into
// out; non-2xx statuses become errors carrying the status code.
func decodeResponse(resp *http.Response, out any) error {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("triage: GitHub API %s: status %d: %s", resp.Request.Method, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body) // best-effort drain of unused body
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ghIssue mirrors the issue fields the detector uses; a present pull_request
// key marks a pull request.
type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	Labels      []ghLabel `json:"labels"`
	PullRequest *struct{} `json:"pull_request"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghComment struct {
	ID   int64   `json:"id"`
	Body string  `json:"body"`
	User *ghUser `json:"user"`
}

type ghUser struct {
	Login string `json:"login"`
}

type ghSearchResult struct {
	TotalCount int       `json:"total_count"`
	Items      []ghIssue `json:"items"`
}

// ghTimelineEvent mirrors the timeline fields used to find explicitly linked
// pull requests. The embedded pull_request marker appears both as an object
// and as a single-element array depending on the endpoint, so it is kept raw.
type ghTimelineEvent struct {
	Event  string `json:"event"`
	Source *struct {
		Issue *struct {
			Number      int             `json:"number"`
			PullRequest json.RawMessage `json:"pull_request"`
		} `json:"issue"`
	} `json:"source"`
}

// crossReferencedPR reports the pull request number when the event is a
// cross-reference to a pull request.
func (e ghTimelineEvent) crossReferencedPR() (int, bool) {
	if e.Event != "cross-referenced" || e.Source == nil || e.Source.Issue == nil {
		return 0, false
	}
	if len(e.Source.Issue.PullRequest) == 0 || string(e.Source.Issue.PullRequest) == "null" {
		return 0, false
	}
	return e.Source.Issue.Number, true
}

type ghPullRequest struct {
	Number         int    `json:"number"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
}

type ghTag struct {
	Name   string `json:"name"`
	Commit *struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

type ghCompare struct {
	Status string `json:"status"`
}

type ghCommentBody struct {
	Body string `json:"body"`
}

type ghLabelCreate struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type ghLabelAdd struct {
	Labels []string `json:"labels"`
}

func (g ghIssue) toIssue() Issue {
	labels := make([]string, 0, len(g.Labels))
	for _, label := range g.Labels {
		labels = append(labels, label.Name)
	}
	return Issue{
		Number:      g.Number,
		Title:       g.Title,
		Body:        g.Body,
		State:       g.State,
		PullRequest: g.PullRequest != nil,
		Labels:      labels,
	}
}
