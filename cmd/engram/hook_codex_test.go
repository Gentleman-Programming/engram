package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/server"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func TestCodexUserPromptFirstMessageIsNetworkIndependent(t *testing.T) {
	for _, tc := range []struct {
		name, authority string
		status          int
		wantPosts       int32
	}{
		{"valid", `{"project":"engram","project_source":"git_root"}`, http.StatusOK, 1},
		{"unavailable", ``, http.StatusServiceUnavailable, 0},
		{"invalid", `{"error_hint":"missing"}`, http.StatusOK, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/project/current":
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.authority)
				case "/prompts":
					posts.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			output := runCodexUserPromptSubmit([]byte(`{"cwd":"C:/work","session_id":"first","prompt":"remember this"}`), server.URL, t.TempDir(), time.Now)
			if !strings.Contains(string(output), "CRITICAL FIRST ACTION") || !json.Valid(output) {
				t.Fatalf("first output = %q, want valid ToolSearch JSON", output)
			}
			if got := posts.Load(); got != tc.wantPosts {
				t.Fatalf("prompt posts = %d, want %d before ToolSearch returns", got, tc.wantPosts)
			}
		})
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
	if got := promptPosts.Load(); got != 2 {
		t.Fatalf("prompt posts = %d, want one dispatch per call without retry", got)
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
func TestCodexUserPromptReminderBoundaries(t *testing.T) {
	now := time.Date(2026, 2, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, started, observations, cooldown string
		status                                int
		wantSecond, wantThird                 bool
	}{
		{"recent session", `2026-02-20T11:58:00Z`, `[{"created_at":"2026-02-20T11:40:00Z"}]`, "", 200, false, false},
		{"cooldown", `2026-02-20T11:40:00Z`, `[{"created_at":"2026-02-20T11:40:00Z"}]`, "", 200, true, false},
		{"zero cooldown", `2026-02-20T11:40:00Z`, `[{"created_at":"2026-02-20T11:40:00Z"}]`, "0", 200, true, true},
		{"empty observations", `2026-02-20T11:40:00Z`, `[]`, "", 200, true, false},
		{"alternate timestamps", `2026-02-20 11:40:00`, `[{"created_at":"2026-02-20 11:40:00"}]`, "", 200, true, false},
		{"non-2xx authority", `2026-02-20T11:40:00Z`, `[]`, "", 503, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_NUDGE_COOLDOWN_SECS", tc.cooldown)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/project/current":
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, `{"project":"engram","project_source":"git_root"}`)
				case "/sessions/s":
					_, _ = io.WriteString(w, `{"started_at":"`+tc.started+`"}`)
				case "/observations":
					_, _ = io.WriteString(w, tc.observations)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			state := t.TempDir()
			input := []byte(`{"cwd":"C:/work","session_id":"s"}`)
			for i, want := range []bool{false, tc.wantSecond, tc.wantThird} {
				got := string(runCodexUserPromptSubmit(input, server.URL, state, func() time.Time { return now }))
				if i == 0 {
					if !strings.Contains(got, "CRITICAL FIRST ACTION") {
						t.Fatalf("first output = %q", got)
					}
					continue
				}
				if strings.Contains(got, "MEMORY REMINDER") != want {
					t.Fatalf("call %d output = %q, want reminder %v", i+1, got, want)
				}
			}
		})
	}
}

func TestCodexUserPromptExactReminderCutoffs(t *testing.T) {
	now := time.Date(2026, 2, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name           string
		started, saved time.Duration
		want           bool
	}{
		{"session before five minutes", 5*time.Minute - time.Second, 20 * time.Minute, false},
		{"session at five minutes", 5 * time.Minute, 20 * time.Minute, true},
		{"session after five minutes", 5*time.Minute + time.Second, 20 * time.Minute, true},
		{"save before fifteen minutes", 20 * time.Minute, 15*time.Minute - time.Second, false},
		{"save at fifteen minutes", 20 * time.Minute, 15 * time.Minute, true},
		{"save after fifteen minutes", 20 * time.Minute, 15*time.Minute + time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/project/current":
					_, _ = io.WriteString(w, `{"project":"engram","project_source":"git_root"}`)
				case "/sessions/s":
					_ = json.NewEncoder(w).Encode(map[string]string{"started_at": now.Add(-tc.started).Format(time.RFC3339)})
				case "/observations":
					_ = json.NewEncoder(w).Encode([]map[string]string{{"created_at": now.Add(-tc.saved).Format(time.RFC3339)}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			input := []byte(`{"cwd":"C:/work","session_id":"s"}`)
			state := t.TempDir()
			runCodexUserPromptSubmit(input, ts.URL, state, func() time.Time { return now })
			got := string(runCodexUserPromptSubmit(input, ts.URL, state, func() time.Time { return now }))
			if strings.Contains(got, "MEMORY REMINDER") != tc.want {
				t.Fatalf("output = %q, want reminder %v", got, tc.want)
			}
		})
	}
}

func TestCodexUserPromptFirstPromptPersistsThroughServer(t *testing.T) {
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = filepath.Join(t.TempDir(), "db")
	db, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	t.Setenv("ENGRAM_PROJECT", "codex-hook-test")
	ts := httptest.NewServer(server.New(db, 0).Handler())
	defer ts.Close()
	create := func(id, project string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"id": id, "project": project, "directory": t.TempDir()})
		resp, err := ts.Client().Post(ts.URL+"/sessions", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create session %s: %d", id, resp.StatusCode)
		}
	}
	create("registered", "codex-hook-test")
	create("mismatched", "another-project")
	for _, tc := range []struct {
		id, prompt  string
		wantPersist bool
	}{
		{"registered", "first prompt survives", true},
		{"missing", "unknown session must not persist", false},
		{"mismatched", "wrong project must not persist", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			input, _ := json.Marshal(codexPromptInput{CWD: t.TempDir(), SessionID: tc.id, Prompt: tc.prompt})
			output := runCodexUserPromptSubmit(input, ts.URL, t.TempDir(), time.Now)
			if !json.Valid(output) || !strings.Contains(string(output), "CRITICAL FIRST ACTION") {
				t.Fatalf("first output: %s", output)
			}
			resp, err := ts.Client().Get(ts.URL + "/prompts/recent?project=" + url.QueryEscape("codex-hook-test"))
			if err != nil {
				t.Fatal(err)
			}
			var prompts []store.Prompt
			decodeErr := json.NewDecoder(resp.Body).Decode(&prompts)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || decodeErr != nil {
				t.Fatalf("recent: status %d error %v", resp.StatusCode, decodeErr)
			}
			found := false
			for _, p := range prompts {
				if p.Content == tc.prompt {
					found = true
					if p.SessionID != tc.id || p.Project != "codex-hook-test" {
						t.Fatalf("wrong attribution: %+v", p)
					}
				}
			}
			if found != tc.wantPersist {
				t.Fatalf("prompt %q persisted=%v, want %v; recent=%+v", tc.prompt, found, tc.wantPersist, prompts)
			}
		})
	}
}

func TestCodexUserPromptInvalidPort(t *testing.T) {
	for _, port := range []string{"invalid", "0", "65536"} {
		t.Run(port, func(t *testing.T) {
			t.Setenv("ENGRAM_PORT", port)
			if got := codexHookURL(); got != "" {
				t.Fatalf("URL = %q, want empty", got)
			}
			output := runCodexUserPromptSubmit([]byte(`{"cwd":"C:/work","session_id":"s","prompt":"hello"}`), codexHookURL(), t.TempDir(), time.Now)
			if !strings.Contains(string(output), "CRITICAL FIRST ACTION") {
				t.Fatalf("first output = %q", output)
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
	if got := string(runCodexUserPromptSubmit(input, server.URL, stateDir, time.Now)); !strings.Contains(got, "CRITICAL FIRST ACTION") {
		t.Fatalf("timed-out first output = %q, want ToolSearch", got)
	}
	if got := string(runCodexUserPromptSubmit(input, server.URL, stateDir, time.Now)); got != "{}" {
		t.Fatalf("timeout output = %q, want {}", got)
	}
	close(release)
	if got := posts.Load(); got != 2 {
		t.Fatalf("timed-out prompt posts = %d, want one dispatch per call without retry", got)
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
	entries, err := os.ReadDir(stateDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("state directory entries = %v, error = %v; want empty", entries, err)
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
