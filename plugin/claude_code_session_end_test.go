package plugin_test

import (
	"encoding/json"
	"fmt"
	"io"
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
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
				return
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
		if len(paths) != 2 {
			t.Fatalf("got %d requests, want lookup then end", len(paths))
		}
		if paths[0] != "/sessions/session%2Fid%3Fand%3Dmore" || methods[0] != http.MethodGet {
			t.Errorf("lookup = %s %q, want GET of the encoded session", methods[0], paths[0])
		}
		wantPath := "/sessions/session%2Fid%3Fand%3Dmore/end"
		if paths[1] != wantPath {
			t.Errorf("request path = %q, want %q", paths[1], wantPath)
		}
		if methods[1] != http.MethodPost {
			t.Errorf("request method = %q, want POST", methods[1])
		}
		if bodies[1] != "{}" {
			t.Errorf("request body = %q, want {}", bodies[1])
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

		requests := make(chan *http.Request, 2)
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests <- r
		})}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		runHook(t, "session-end.sh", `{"session_id":"socket-session"}`, map[string]string{"ENGRAM_SOCKET": socketPath})

		select {
		case lookup := <-requests:
			if lookup.URL.Path != "/sessions/socket-session" {
				t.Errorf("lookup path = %q, want /sessions/socket-session", lookup.URL.Path)
			}
		default:
			t.Fatal("expected a session lookup over the Unix socket")
		}
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

	// Issue #1624: a resumed session keeps its ended root ID, so SessionEnd
	// must close the live continuation through the runtime scope.
	t.Run("ends the live continuation of an already-ended root", func(t *testing.T) {
		var mu sync.Mutex
		var requests []string
		var runtimeBody map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			requests = append(requests, r.Method+" "+r.URL.Path)
			switch r.URL.Path {
			case "/sessions/resumed-root":
				_, _ = io.WriteString(w, `{"id":"resumed-root","project":"client","directory":"/work","ownership_mode":"project_owned","ended_at":"2026-10-09 12:00:00"}`)
			case "/runtime-sessions/end":
				if err := json.NewDecoder(r.Body).Decode(&runtimeBody); err != nil {
					t.Errorf("decode runtime end: %v", err)
				}
				_, _ = io.WriteString(w, `{"id":"resumed-root:resume:2","resumed_from":"resumed-root","status":"ended"}`)
			default:
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
		}))
		t.Cleanup(srv.Close)

		runHook(t, "session-end.sh", `{"session_id":"resumed-root"}`, map[string]string{"ENGRAM_URL": srv.URL})

		mu.Lock()
		defer mu.Unlock()
		want := []string{"GET /sessions/resumed-root", "POST /runtime-sessions/end"}
		if strings.Join(requests, ",") != strings.Join(want, ",") {
			t.Fatalf("requests = %v, want %v", requests, want)
		}
		wantBody := map[string]string{"id": "resumed-root", "project": "client", "directory": "/work", "ownership_mode": "project_owned"}
		if fmt.Sprint(runtimeBody) != fmt.Sprint(wantBody) {
			t.Errorf("runtime end body = %v, want %v", runtimeBody, wantBody)
		}
	})

	for _, input := range []string{`{}`, `{"session_id":""}`, `{"session_id":42}`, `{"session_id":`} {
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
