package plugin_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestClaudeCodeSessionEndHook(t *testing.T) {
	requireHookBinaries(t)

	t.Run("is a synchronous SessionEnd hook", func(t *testing.T) {
		root := repoRoot(t)
		data, err := os.ReadFile(filepath.Join(root, "plugin", "claude-code", "hooks", "hooks.json"))
		if err != nil {
			t.Fatalf("read hooks.json: %v", err)
		}

		var manifest struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
					Async   bool   `json:"async"`
					Timeout int    `json:"timeout"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("parse hooks.json: %v", err)
		}

		var sessionEndHooks int
		for _, group := range manifest.Hooks["SessionEnd"] {
			for _, hook := range group.Hooks {
				if strings.Contains(hook.Command, "session-end.sh") {
					sessionEndHooks++
					if !strings.HasPrefix(hook.Command, "bash ") {
						t.Errorf("SessionEnd command = %q, want explicit bash invocation", hook.Command)
					}
					if hook.Async {
						t.Error("SessionEnd session close hook must be synchronous")
					}
					if hook.Timeout <= 2 {
						t.Errorf("SessionEnd timeout = %d, want enough time for the bounded transport", hook.Timeout)
					}
				}
			}
		}
		if sessionEndHooks != 1 {
			t.Errorf("SessionEnd has %d session-end.sh hooks, want 1", sessionEndHooks)
		}
		for _, group := range manifest.Hooks["Stop"] {
			for _, hook := range group.Hooks {
				if strings.Contains(hook.Command, "session-stop.sh") || strings.Contains(hook.Command, "session-end.sh") {
					t.Errorf("turn-scoped Stop must not close sessions: %q", hook.Command)
				}
			}
		}
	})

	t.Run("posts a safely encoded string session ID over the configured URL", func(t *testing.T) {
		var mu sync.Mutex
		var paths []string
		var methods []string
		var bodies []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body := make([]byte, r.ContentLength)
			if _, err := r.Body.Read(body); err != nil && err.Error() != "EOF" {
				t.Errorf("read request body: %v", err)
			}
			mu.Lock()
			paths = append(paths, r.URL.EscapedPath())
			methods = append(methods, r.Method)
			bodies = append(bodies, string(body))
			mu.Unlock()
		}))
		t.Cleanup(srv.Close)

		sessionID := "session/id?and=more"
		input := fmt.Sprintf(`{"session_id":%q}`, sessionID)
		runHook(t, "session-end.sh", input, map[string]string{"ENGRAM_URL": srv.URL})

		mu.Lock()
		defer mu.Unlock()
		if len(paths) != 1 {
			t.Fatalf("got %d requests, want 1", len(paths))
		}
		wantPath := "/sessions/session%2Fid%3Fand%3Dmore/end"
		if paths[0] != wantPath {
			t.Errorf("request path = %q, want %q", paths[0], wantPath)
		}
		if methods[0] != http.MethodPost {
			t.Errorf("request method = %q, want POST", methods[0])
		}
		if bodies[0] != "{}" {
			t.Errorf("request body = %q, want {}", bodies[0])
		}
	})

	t.Run("posts over the configured Unix socket", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("Unix socket hook transport requires a Linux test process")
		}

		socketPath := filepath.Join(t.TempDir(), "engram.sock")
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Fatalf("listen on Unix socket: %v", err)
		}
		t.Cleanup(func() { _ = listener.Close() })

		requests := make(chan *http.Request, 1)
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests <- r
		})}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		runHook(t, "session-end.sh", `{"session_id":"socket-session"}`, map[string]string{"ENGRAM_SOCKET": socketPath})

		select {
		case request := <-requests:
			if request.URL.Path != "/sessions/socket-session/end" {
				t.Errorf("request path = %q, want /sessions/socket-session/end", request.URL.Path)
			}
			if request.Method != http.MethodPost {
				t.Errorf("request method = %q, want POST", request.Method)
			}
		default:
			t.Fatal("expected a request over the Unix socket")
		}
	})

	for _, input := range []string{`{}`, `{"session_id":""}`, `{"session_id":42}`} {
		t.Run("fails open without posting "+input, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				t.Errorf("unexpected request for input %s", input)
			}))
			t.Cleanup(srv.Close)

			runHook(t, "session-end.sh", input, map[string]string{"ENGRAM_PORT": serverPort(t, srv)})
			if requests != 0 {
				t.Errorf("got %d requests, want 0", requests)
			}
		})
	}

	t.Run("bounds transport and ignores HTTP failure", func(t *testing.T) {
		script := claudeScript(t, "session-end.sh")
		if !strings.Contains(script, "_helpers.sh") || !strings.Contains(script, "engram_curl") {
			t.Error("session-end.sh must use the shared bounded transport")
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}))
		t.Cleanup(srv.Close)

		runHook(t, "session-end.sh", `{"session_id":"server-failure"}`, map[string]string{"ENGRAM_PORT": serverPort(t, srv)})
	})
}
