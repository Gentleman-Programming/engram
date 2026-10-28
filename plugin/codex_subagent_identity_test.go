package plugin_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Exercise registration, closure, continuation and capture through production HTTP
// handlers. A second live host must never become an attribution fallback.
func TestCodexSubagentStopConfirmedIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("executes Codex shell hook and native CLI")
	}
	bash := codexTestBash(t)
	binary := buildCodexFixtureCLI(t)
	for _, tc := range []struct {
		name, mode, confirmation string
		accept                   bool
	}{
		{"fresh", "fresh", "", true},
		{"resumed", "resumed", "", true},
		{"opaque host with trailing LF", "opaque", "", true},
		{"ended root", "ended", "", false},
		{"unregistered host", "unregistered", "", false},
		{"resolver unavailable", "unavailable", "", false},
		{"malformed confirmation", "stub", "not-json", false},
		{"foreign confirmation", "stub", `{"session_id":"other-host"}`, false},
		{"empty confirmation", "stub", `{"session_id":""}`, false},
		{"missing confirmation", "stub", "", false},
		{"multiple confirmations", "stub", `{"session_id":"host"} {"session_id":"host"}`, false},
		{"failed native call", "failed", `{"session_id":"host"}`, false},
		{"missing host", "missing", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const project = "codex-passive-identity"
			codexFixtureProject(t, root, project)
			db, err := store.New(store.FallbackConfig(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			production := server.New(db, 0).Handler()
			var posts, registrations atomic.Int32
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/observations/passive" {
					posts.Add(1)
				}
				if r.URL.Path == "/sessions" && r.Method == http.MethodPost {
					registrations.Add(1)
				}
				if tc.mode == "unavailable" && r.URL.Path == "/runtime-sessions/resolve" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				production.ServeHTTP(w, r)
			}))
			defer fixture.Close()
			request := func(method, path string, body any) []byte {
				t.Helper()
				var input io.Reader
				if body != nil {
					data, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					input = bytes.NewReader(data)
				}
				req, err := http.NewRequest(method, fixture.URL+path, input)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := fixture.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := resp.Body.Close(); err != nil {
						t.Errorf("close response body: %v", err)
					}
				}()
				data, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
				}
				return data
			}
			host := "host"
			if tc.mode == "opaque" {
				host = "opaque \"host\\id\n"
			}
			register := func(id string, resume bool) string {
				t.Helper()
				data := request(http.MethodPost, "/sessions", map[string]any{"id": id, "project": project, "directory": root, "resume": resume})
				var out struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(data, &out); err != nil || out.ID == "" {
					t.Fatalf("registration: %s, %v", data, err)
				}
				return out.ID
			}
			register("other-host", false)
			effective := host
			if tc.mode != "unregistered" && tc.mode != "missing" {
				register(host, false)
				if tc.mode == "ended" || tc.mode == "resumed" {
					request(http.MethodPost, "/sessions/"+url.PathEscape(host)+"/end", map[string]string{"summary": "finished"})
					if tc.mode == "resumed" {
						effective = register(host, true)
					}
				}
			}
			paths := []string{"/stats", "/observations?project=" + project, "/sessions/other-host"}
			if tc.mode != "unregistered" && tc.mode != "missing" {
				paths = append(paths, "/sessions/"+url.PathEscape(host))
			}
			if effective != host {
				paths = append(paths, "/sessions/"+url.PathEscape(effective))
			}
			before := make([][]byte, len(paths))
			for i, p := range paths {
				before[i] = request(http.MethodGet, p, nil)
			}
			registrations.Store(0)
			env := codexHandoffEnv(t, root, fixture.URL, binary)
			// This hook's legacy transport budget is nine seconds. Retain the shared
			// fixture's loopback and payload guards while allowing that exact value.
			curlPath := filepath.Join(root, "bin", "curl")
			curlScript, err := os.ReadFile(curlPath)
			if err != nil {
				t.Fatal(err)
			}
			curlScript = bytes.Replace(curlScript, []byte("0.2|1|2|3)"), []byte("0.2|1|2|3|9)"), 1)
			if err := os.WriteFile(curlPath, curlScript, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.mode == "stub" || tc.mode == "failed" {
				exit := 0
				if tc.mode == "failed" {
					exit = 1
				}
				stub := "#!/bin/bash -p\nprintf '%s' " + codexIntentQuote(tc.confirmation) + fmt.Sprintf("\nexit %d\n", exit)
				if err := os.WriteFile(filepath.Join(root, "bin", "engram"), []byte(stub), 0700); err != nil {
					t.Fatal(err)
				}
			}
			inputHost := host
			if tc.mode == "missing" {
				inputHost = ""
			}
			const learning = "Closed roots must not receive resumed captures; preserve the confirmed runtime binding."
			const content = "## Key Learnings:\n- " + learning
			input, _ := json.Marshal(map[string]string{"session_id": inputHost, "cwd": root, "last_assistant_message": content})
			cmd := exec.Command(bash, repoRoot(t)+"/plugin/codex/scripts/subagent-stop.sh")
			cmd.Env = env
			cmd.Stdin = bytes.NewReader(input)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			finishTrace := startCodexChildTrace(t, tc.name, root, cmd, &stdout, &stderr)
			defer finishTrace()
			err = cmd.Run()
			if err != nil || stdout.String() != "{}\n" || stderr.Len() != 0 {
				t.Fatalf("hook exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if registrations.Load() != 0 {
				t.Fatal("hook registered a session")
			}
			if !tc.accept {
				if posts.Load() != 0 {
					t.Errorf("rejected identity produced %d passive POSTs", posts.Load())
				}
				for i, p := range paths {
					if after := request(http.MethodGet, p, nil); !bytes.Equal(after, before[i]) {
						t.Errorf("rejected identity changed %s: before=%s after=%s", p, before[i], after)
					}
				}
				return
			}
			if posts.Load() != 1 {
				t.Fatalf("passive POSTs=%d, want 1", posts.Load())
			}
			observations := request(http.MethodGet, "/observations?project="+project, nil)
			var result []struct {
				SessionID string `json:"session_id"`
				Content   string `json:"content"`
				ToolName  string `json:"tool_name"`
				Project   string `json:"project"`
			}
			if err := json.Unmarshal(observations, &result); err != nil {
				t.Fatal(err)
			}
			if len(result) != 1 {
				t.Fatalf("stored captures=%d, want exactly one: %s", len(result), observations)
			}
			if got := result[0]; got.SessionID != effective || got.Content != learning || got.ToolName != "subagent-stop" || got.Project != project {
				t.Errorf("stored capture=%+v, want session=%q content=%q source=subagent-stop project=%q", got, effective, learning, project)
			}
			if after := request(http.MethodGet, "/sessions/other-host", nil); !bytes.Equal(after, before[2]) {
				t.Fatal("concurrent host changed")
			}
			if effective != host {
				if after := request(http.MethodGet, "/sessions/"+url.PathEscape(host), nil); !bytes.Equal(after, before[3]) {
					t.Fatal("closed root changed")
				}
			}
		})
	}
}
