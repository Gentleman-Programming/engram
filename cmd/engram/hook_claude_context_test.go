package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
)

func runClaudeSessionContext(t *testing.T, input string) string {
	t.Helper()
	stdin, output, exit := os.Stdin, claudeHookOutput, exitFunc
	t.Cleanup(func() { os.Stdin, claudeHookOutput, exitFunc = stdin, output, exit })
	os.Stdin = claudeHookStdin(t, input, false)
	var result string
	claudeHookOutput = func(value []byte) error { result += string(value); return nil }
	exitFunc = func(code int) { t.Errorf("context hook exited with %d", code) }
	cmdHook([]string{"claude-session-context"})
	return result
}

func TestClaudeSessionContextConfirmedBinding(t *testing.T) {
	// Keep the listener and its real I/O outside the virtual-time bubble. The
	// success matrix exercises real store/handler behavior through in-process
	// HTTP transport, without making its assertions depend on runner latency.
	root, db, registrations, requests := claudeConfirmedInputFixture(t)
	production := server.New(db, 0).Handler()
	synctest.Test(t, func(t *testing.T) {
		original := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = original })
		http.DefaultTransport = codexRoundTripper(func(r *http.Request) (*http.Response, error) {
			requests.Add(1)
			if r.Method == http.MethodPost && r.URL.Path == "/sessions" {
				registrations.Add(1)
			}
			response := httptest.NewRecorder()
			production.ServeHTTP(response, r)
			result := response.Result()
			result.Request = r
			return result, nil
		})
		for _, host := range []string{"live-host", "peer-host"} {
			if host != "live-host" {
				if err := db.StartSessionWithOwnershipMode(host, "project-a", root, "project_owned"); err != nil {
					t.Fatal(err)
				}
			}
			for _, resumed := range []bool{false, true} {
				t.Logf("host=%q resumed=%t", host, resumed)
				effective := host
				if resumed {
					if err := db.EndSession(host, "finished"); err != nil {
						t.Fatal(err)
					}
					var err error
					effective, err = db.ResumeSessionWithOwnershipMode(host, "project-a", root, "project_owned")
					if err != nil {
						t.Fatal(err)
					}
				}
				input, _ := json.Marshal(map[string]string{"session_id": host, "cwd": root})
				before := registrations.Load()
				context := runClaudeSessionContext(t, string(input))
				encoded, _ := json.Marshal(effective)
				if !strings.Contains(context, "Confirmed Engram session ID (JSON): "+string(encoded)) {
					t.Fatalf("context does not expose confirmed effective identity: %q", context)
				}
				if registrations.Load() != before+1 {
					t.Fatal("context skipped confirmation")
				}
				for _, prefix := range claudeEngramToolPrefixes {
					for _, tool := range []string{"mem_save", "mem_session_start", "mem_session_end"} {
						args := map[string]string{claudeAuthoritativeSessionField(prefix + tool): effective}
						call, _ := json.Marshal(map[string]any{"session_id": host, "cwd": root, "tool_name": prefix + tool, "tool_input": args})
						before := registrations.Load()
						if got := string(guardClaudePreToolUse(call)); got != "{}" {
							t.Fatalf("confirmed context needs rewrite: %s", got)
						}
						if registrations.Load() != before+1 {
							t.Fatal("model argument bypassed guard confirmation")
						}
					}
				}
			}
		}
	})
}

func TestClaudeSessionContextDeadlineOmitsIdentity(t *testing.T) {
	root, _, registrations, requests := claudeConfirmedInputFixture(t)
	input, _ := json.Marshal(map[string]string{"session_id": "live-host", "cwd": root})
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("ENGRAM_HOOK_SESSION_CONFIRMATION_TIMEOUT", "1h")
		original := http.DefaultTransport
		t.Cleanup(func() { http.DefaultTransport = original })
		started := time.Now()
		attempts := 0
		http.DefaultTransport = codexRoundTripper(func(r *http.Request) (*http.Response, error) {
			attempts++
			deadline, ok := r.Context().Deadline()
			if !ok || !deadline.Equal(started.Add(500*time.Millisecond)) || r.URL.Path != "/health" {
				t.Fatalf("unexpected optional confirmation: path=%s deadline=%v", r.URL.Path, deadline)
			}
			<-r.Context().Done()
			if r.Context().Err() != context.DeadlineExceeded {
				t.Fatalf("unexpected cancellation: %v", r.Context().Err())
			}
			return nil, r.Context().Err()
		})
		if output := runClaudeSessionContext(t, string(input)); output != "" {
			t.Fatalf("deadline exposed identity or permission output: %q", output)
		}
		if attempts != 1 || registrations.Load() != 0 || requests.Load() != 0 {
			t.Fatal("deadline retried or dispatched a registration")
		}
		if elapsed := time.Since(started); elapsed != 500*time.Millisecond {
			t.Fatalf("optional deadline elapsed=%s", elapsed)
		}
	})
}

func TestClaudeSessionContextRejectsUnconfirmedInput(t *testing.T) {
	for _, input := range []string{`null`, `{`, `{}`, `{"session_id":"s"}`, `{"cwd":"/synthetic"}`, `{"session_id":42,"cwd":"/synthetic"}`} {
		if got := runClaudeSessionContext(t, input); got != "" {
			t.Fatalf("unconfirmed context: %q", got)
		}
	}
	root, db, _, _ := claudeConfirmedInputFixture(t)
	if err := db.EndSession("live-host", "finished"); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"session_id": "live-host", "cwd": root})
	if got := runClaudeSessionContext(t, string(input)); got != "" {
		t.Fatalf("ended root exposed identity: %q", got)
	}
}

func TestClaudeSessionContextResponseFailureAndDeadline(t *testing.T) {
	for _, kind := range []string{"wrong-id", "malformed", "forbidden", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "wrong-id":
					_, _ = w.Write([]byte(`{"id":"other-host","project":"project-a"}`))
				case "malformed":
					_, _ = w.Write([]byte(`{"id":`))
				case "forbidden":
					w.WriteHeader(http.StatusForbidden)
				case "timeout":
					<-r.Context().Done()
				}
			}))
			defer endpoint.Close()
			t.Setenv("ENGRAM_URL", endpoint.URL)
			t.Setenv("ENGRAM_HOOK_SESSION_CONFIRMATION_TIMEOUT", "1h")
			start := time.Now()
			if got := runClaudeSessionContext(t, `{"session_id":"live-host","cwd":"/synthetic"}`); got != "" {
				t.Fatalf("failed confirmation exposed context: %q", got)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("optional context exceeded bounded deadline")
			}
		})
	}
}

func TestClaudeSessionContextEscapesIdentityAndSkipsPi(t *testing.T) {
	root, db, registrations, _ := claudeConfirmedInputFixture(t)
	const id = "quoted\"\\<binding>\nnext"
	if err := db.StartSessionWithOwnershipMode(id, "project-a", root, "project_owned"); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]string{"session_id": id, "cwd": root})
	output := runClaudeSessionContext(t, string(input))
	line := strings.Split(strings.Split(output, "Confirmed Engram session ID (JSON): ")[1], "\n")[0]
	var decoded string
	if err := json.Unmarshal([]byte(line), &decoded); err != nil || decoded != id {
		t.Fatalf("identity did not round trip as one JSON string: %q %v", line, err)
	}
	t.Setenv("PI_CODING_AGENT", "1")
	before := registrations.Load()
	if got := runClaudeSessionContext(t, string(input)); got != "" || registrations.Load() != before {
		t.Fatal("Pi-owned session received Claude context or confirmation")
	}
}
