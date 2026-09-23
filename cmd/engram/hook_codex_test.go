package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
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
		name, authority string
		wantNudge       bool
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

type codexWriterFunc func([]byte) (int, error)

func (f codexWriterFunc) Write(data []byte) (int, error) { return f(data) }

type codexRoundTripper func(*http.Request) (*http.Response, error)

func (f codexRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type codexCloseErrorBody struct {
	io.Reader
	closes *int
}

func (b *codexCloseErrorBody) Close() error {
	(*b.closes)++
	return errors.New("close failed")
}

func TestCodexUserPromptSubmitIOReadErrorHasNoSideEffects(t *testing.T) {
	stateDir, readErr := t.TempDir(), errors.New("read failed")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	var output strings.Builder
	if err := runCodexUserPromptSubmitIO(iotest.ErrReader(readErr), &output, server.URL, stateDir, time.Now); !errors.Is(err, readErr) {
		t.Fatalf("error = %v, want %v", err, readErr)
	}
	loaded, _ := codexPromptStatePaths(stateDir, "unknown")
	if _, err := os.Stat(loaded); !os.IsNotExist(err) {
		t.Fatalf("state marker error = %v, want not exist", err)
	}
	if output.Len() != 0 || requests.Load() != 0 {
		t.Fatalf("output=%q requests=%d, want no side effects", output.String(), requests.Load())
	}
}
func TestCodexUserPromptSubmitIOWriteFailuresReturnWithoutRetry(t *testing.T) {
	writeErr := errors.New("write failed")
	tests := []struct {
		name  string
		write func([]byte) (int, error)
		want  error
	}{
		{
			name:  "writer error",
			write: func([]byte) (int, error) { return 0, writeErr },
			want:  writeErr,
		},
		{
			name:  "short write",
			write: func(data []byte) (int, error) { return len(data) - 1, nil },
			want:  io.ErrShortWrite,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writes := 0
			writer := codexWriterFunc(func(data []byte) (int, error) {
				writes++
				return tt.write(data)
			})
			err := runCodexUserPromptSubmitIO(strings.NewReader(`{"session_id":"write-error"}`), writer, "", t.TempDir(), time.Now)
			if !errors.Is(err, tt.want) || writes != 1 {
				t.Fatalf("error=%v writes=%d, want %v once", err, writes, tt.want)
			}
		})
	}
}
func TestCodexJSONToleratesResponseCloseError(t *testing.T) {
	var requests, closes int
	client := &http.Client{Transport: codexRoundTripper(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Body: &codexCloseErrorBody{Reader: strings.NewReader(`{"project":"engram"}`), closes: &closes}}, nil
	})}
	var decoded map[string]string
	if !codexJSON(context.Background(), client, http.MethodGet, "http://example.test", nil, &decoded) {
		t.Fatal("codexJSON returned false")
	}
	if decoded["project"] != "engram" || requests != 1 || closes != 1 {
		t.Fatalf("decoded=%q requests=%d closes=%d, want one successful request and close", decoded["project"], requests, closes)
	}
}
