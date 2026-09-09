package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/triage"
)

func TestParseTriageArgs(t *testing.T) {
	t.Run("explicit repo and issue", func(t *testing.T) {
		opts, help, err := parseTriageArgs([]string{"--repo", "owner/repo", "--issue", "5"})
		if err != nil || help {
			t.Fatalf("parseTriageArgs err=%v help=%t", err, help)
		}
		if opts.repo != "owner/repo" || opts.issue != 5 {
			t.Errorf("opts = %+v", opts)
		}
	})

	t.Run("repo falls back to GITHUB_REPOSITORY", func(t *testing.T) {
		t.Setenv("GITHUB_REPOSITORY", "fallback/repo")
		opts, _, err := parseTriageArgs([]string{"--issue", "7"})
		if err != nil {
			t.Fatalf("parseTriageArgs: %v", err)
		}
		if opts.repo != "fallback/repo" || opts.issue != 7 {
			t.Errorf("opts = %+v", opts)
		}
	})

	t.Run("help flag", func(t *testing.T) {
		_, help, err := parseTriageArgs([]string{"--help"})
		if err != nil || !help {
			t.Fatalf("help=%t err=%v", help, err)
		}
	})

	errors := []struct {
		name string
		args []string
		env  string
	}{
		{name: "missing issue", args: []string{"--repo", "o/r"}},
		{name: "non-numeric issue", args: []string{"--repo", "o/r", "--issue", "abc"}},
		{name: "zero issue", args: []string{"--repo", "o/r", "--issue", "0"}},
		{name: "missing repo without env", args: []string{"--issue", "5"}, env: ""},
		{name: "repo without slash", args: []string{"--repo", "justname", "--issue", "5"}},
		{name: "unknown flag", args: []string{"--repo", "o/r", "--issue", "5", "--wat"}},
		{name: "repo missing value", args: []string{"--repo"}},
		{name: "issue missing value", args: []string{"--issue"}},
	}
	for _, tt := range errors {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GITHUB_REPOSITORY", tt.env)
			if _, _, err := parseTriageArgs(tt.args); err == nil {
				t.Fatalf("expected error for %v", tt.args)
			}
		})
	}
}

// triageTestServer returns an httptest GitHub API where the issue exists and
// search responds with the given status.
func triageTestServer(t *testing.T, searchStatus int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/5":
			_, _ = w.Write([]byte(`{"number": 5, "title": "App crashes when saving large notes", "body": "", "state": "open", "labels": []}`))
		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			w.WriteHeader(searchStatus)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/5/comments":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func stubTriageClientServer(t *testing.T, server *httptest.Server) {
	t.Helper()
	original := newTriageClient
	newTriageClient = func(baseURL, repo, token string) triage.Client {
		return triage.NewRESTClient(server.URL+"/", repo, token, server.Client())
	}
	t.Cleanup(func() { newTriageClient = original })
}

func TestCmdTriageDuplicatesToleratesSearchFailures(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("search status %d exits zero", status), func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", "test-token")
			t.Setenv("GITHUB_REPOSITORY", "")
			server := triageTestServer(t, status)
			stubTriageClientServer(t, server)

			var code int
			_, stderr := captureOutput(t, func() {
				code = cmdTriageDuplicates([]string{"--repo", "o/r", "--issue", "5"})
			})
			if code != triageExitSuccess {
				t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
			}
			if !strings.Contains(stderr, "warning") {
				t.Errorf("expected a warning on stderr, got: %q", stderr)
			}
		})
	}
}

func TestCmdTriageDuplicatesCleanNoCandidateRun(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/o/r/issues/5":
			_, _ = w.Write([]byte(`{"number": 5, "title": "Nothing similar", "body": "", "state": "open", "labels": []}`))
		case r.URL.Path == "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 0, "items": []}`))
		case r.URL.Path == "/repos/o/r/issues/5/comments":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	stubTriageClientServer(t, server)

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = cmdTriageDuplicates([]string{"--repo", "o/r", "--issue", "5"})
	})
	if code != triageExitSuccess {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("expected clean stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "") {
		t.Error("unreachable")
	}
}

func TestCmdTriageDuplicatesUsageErrors(t *testing.T) {
	t.Run("missing token", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GITHUB_REPOSITORY", "o/r")
		var code int
		_, stderr := captureOutput(t, func() {
			code = cmdTriageDuplicates([]string{"--issue", "5"})
		})
		if code != triageExitUsage {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "GITHUB_TOKEN") {
			t.Errorf("stderr = %q", stderr)
		}
	})

	t.Run("bad flag", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "tok")
		var code int
		_, stderr := captureOutput(t, func() {
			code = cmdTriageDuplicates([]string{"--nope"})
		})
		if code != triageExitUsage {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr, "unknown triage-duplicates flag") {
			t.Errorf("stderr = %q", stderr)
		}
	})

	t.Run("help exits zero and prints usage", func(t *testing.T) {
		var code int
		stdout, _ := captureOutput(t, func() {
			code = cmdTriageDuplicates([]string{"--help"})
		})
		if code != triageExitSuccess {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.Contains(stdout, "usage: engram triage-duplicates") {
			t.Errorf("stdout = %q", stdout)
		}
	})
}

func TestCmdTriageDuplicatesCommentAgainstRealServer(t *testing.T) {
	// End-to-end over one httptest API: one accepted candidate forces label
	// creation, label add, and one anchored comment.
	t.Setenv("GITHUB_TOKEN", "test-token")
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/5":
			_, _ = w.Write([]byte(`{"number": 5, "title": "App crashes when saving large notes", "body": "", "state": "open", "labels": []}`))
		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 1, "items": [{"number": 11, "title": "App crashes when saving a large note", "body": "", "state": "open"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/5/comments":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/labels/triage:possible-duplicate":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/labels":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/5/labels":
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/5/comments":
			var payload struct{ Body string }
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode comment: %v", err)
			}
			if !strings.Contains(payload.Body, "- #11:") || !strings.Contains(payload.Body, triage.CommentAnchor) {
				t.Errorf("comment body missing evidence:\n%s", payload.Body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	stubTriageClientServer(t, server)

	var code int
	_, stderr := captureOutput(t, func() {
		code = cmdTriageDuplicates([]string{"--repo", "o/r", "--issue", "5"})
	})
	if code != triageExitSuccess {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"POST /repos/o/r/labels", "POST /repos/o/r/issues/5/labels", "POST /repos/o/r/issues/5/comments"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected call %q, got:\n%s", want, joined)
		}
	}
}

// TestCmdMain_TriageDuplicatesWired verifies that `engram triage-duplicates`
// is wired in the top-level dispatch — it must NOT produce "unknown command".
func TestCmdMain_TriageDuplicatesWired(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	withArgs(t, "engram", "triage-duplicates")
	stubCheckForUpdates(t, versionCheckResult())
	stubExitWithPanic(t)
	_, stderr, _ := captureOutputAndRecover(t, func() { main() })
	if strings.Contains(stderr, "unknown command: triage-duplicates") {
		t.Errorf("triage-duplicates not wired in main switch; stderr: %q", stderr)
	}
}
