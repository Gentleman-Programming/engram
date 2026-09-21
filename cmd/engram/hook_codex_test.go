package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodexUserPromptFirstMessageIsNetworkIndependent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("first message must not call the local server")
	}))
	defer server.Close()

	output := runCodexUserPromptSubmit([]byte(`{"cwd":"C:/work","session_id":"first"}`), server.URL, t.TempDir(), time.Now)
	if !strings.Contains(string(output), "CRITICAL FIRST ACTION") || !json.Valid(output) {
		t.Fatalf("first output = %q, want valid ToolSearch JSON", output)
	}
}

func TestCodexUserPromptSubsequentMessageSharesDeadlineAndPersistsOnce(t *testing.T) {
	now := time.Date(2026, 2, 20, 12, 0, 0, 0, time.UTC)
	var promptPosts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/project/current":
			_, _ = io.WriteString(w, `{"project":"engram","project_source":"git_root"}`)
		case "/prompts":
			promptPosts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case "/sessions/s-1":
			_, _ = io.WriteString(w, `{"started_at":"2026-02-20T11:40:00Z"}`)
		case "/observations":
			_, _ = io.WriteString(w, `[{"created_at":"2026-02-20T11:40:00Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stateDir := t.TempDir()
	input := []byte(`{"cwd":"C:/work","session_id":"s-1","prompt":"persist once"}`)
	if got := string(runCodexUserPromptSubmit(input, server.URL, stateDir, func() time.Time { return now })); !strings.Contains(got, "CRITICAL FIRST ACTION") {
		t.Fatalf("first output = %q, want ToolSearch", got)
	}
	if got := string(runCodexUserPromptSubmit(input, server.URL, stateDir, func() time.Time { return now })); !strings.Contains(got, "MEMORY REMINDER") {
		t.Fatalf("subsequent output = %q, want reminder", got)
	}
	if got := promptPosts.Load(); got != 1 {
		t.Fatalf("prompt posts = %d, want one dispatch without retry", got)
	}
}

func TestCodexUserPromptProjectAuthorityMatchesUnixJQ(t *testing.T) {
	now := time.Date(2026, 2, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		authority string
		wantNudge bool
	}{
		{"valid", `{"project":"engram","project_source":"git_root"}`, true},
		{"wrong-case-project", `{"Project":"engram","project_source":"git_root"}`, false},
		{"wrong-case-source", `{"project":"engram","Project_Source":"git_root"}`, false},
		{"weak-source", `{"project":"engram","project_source":"git_root "}`, false},
		{"empty-error-hint", `{"project":"engram","project_source":"git_root","error_hint":""}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/project/current":
					_, _ = io.WriteString(w, tc.authority)
				case "/sessions/s-1":
					_, _ = io.WriteString(w, `{"started_at":"2026-02-20T11:40:00Z"}`)
				case "/observations":
					_, _ = io.WriteString(w, `[{"created_at":"2026-02-20T11:40:00Z"}]`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			input, stateDir := []byte(`{"cwd":"C:/work","session_id":"s-1"}`), t.TempDir()
			runCodexUserPromptSubmit(input, server.URL, stateDir, func() time.Time { return now })
			got := string(runCodexUserPromptSubmit(input, server.URL, stateDir, func() time.Time { return now }))
			if tc.wantNudge && !strings.Contains(got, "MEMORY REMINDER") {
				t.Fatalf("output = %q, want reminder", got)
			}
			if !tc.wantNudge && got != "{}" {
				t.Fatalf("output = %q, want {}", got)
			}
		})
	}
}

func TestCodexUserPromptSessionMarkersAreIsolated(t *testing.T) {
	stateDir := t.TempDir()
	for _, sessionID := range []string{"one", "two"} {
		output := runCodexUserPromptSubmit([]byte(`{"session_id":"`+sessionID+`"}`), "", stateDir, time.Now)
		if !strings.Contains(string(output), "CRITICAL FIRST ACTION") {
			t.Fatalf("session %q did not receive its first-message ToolSearch: %q", sessionID, output)
		}
	}
}

func TestCodexUserPromptDeadlineHasHostSafetyMargin(t *testing.T) {
	if codexUserPromptDeadline <= 0 || codexUserPromptDeadline >= 2*time.Second {
		t.Fatalf("aggregate deadline = %s, want positive safety margin below host 2s", codexUserPromptDeadline)
	}
	if codexUserPromptDeadline > 1500*time.Millisecond {
		t.Fatalf("aggregate deadline = %s, want at least 500ms host safety margin", codexUserPromptDeadline)
	}
}

func TestCodexUserPromptTimeoutFailsOpenWithoutRetry(t *testing.T) {
	var posts atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/project/current":
			_, _ = io.WriteString(w, `{"project":"engram","project_source":"git_root"}`)
		case "/prompts":
			posts.Add(1)
			<-release
		}
	}))
	defer server.Close()
	stateDir := t.TempDir()
	input := []byte(`{"cwd":"C:/work","session_id":"timeout","prompt":"once"}`)
	runCodexUserPromptSubmit(input, server.URL, stateDir, time.Now)
	if got := string(runCodexUserPromptSubmit(input, server.URL, stateDir, time.Now)); got != "{}" {
		t.Fatalf("timeout output = %q, want {}", got)
	}
	close(release)
	if got := posts.Load(); got != 1 {
		t.Fatalf("timed-out prompt posts = %d, want no retry", got)
	}
}

func TestCodexUserPromptMalformedInputFailsOpen(t *testing.T) {
	output := runCodexUserPromptSubmit([]byte(`{`), "http://127.0.0.1:1", t.TempDir(), time.Now)
	if !json.Valid(output) || !strings.Contains(string(output), "CRITICAL FIRST ACTION") {
		t.Fatalf("malformed output = %q, want valid first-message JSON", output)
	}
}
