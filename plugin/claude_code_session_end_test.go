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
	"sync/atomic"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
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

	for _, tc := range []struct {
		name, session  string
		initiallyEnded bool
	}{
		{"live session", `{"id":"fallback-root","project":"client","directory":"/work","ownership_mode":"project_owned","ended_at":null}`, false},
		{"ended root without project", `{"id":"fallback-root","project":"","directory":"/work","ownership_mode":"project_owned","ended_at":"2026-10-09 12:00:00"}`, true},
		{"ended root without directory", `{"id":"fallback-root","project":"client","directory":"","ownership_mode":"project_owned","ended_at":"2026-10-09 12:00:00"}`, true},
	} {
		t.Run("uses regular end for "+tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var requests []string
			ended := tc.initiallyEnded
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests = append(requests, r.Method+" "+r.URL.Path)
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/sessions/fallback-root":
					_, _ = io.WriteString(w, tc.session)
				case r.Method == http.MethodPost && r.URL.Path == "/sessions/fallback-root/end":
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read end body: %v", err)
						return
					}
					if string(body) != "{}" || r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("end body=%q content-type=%q, want {} and application/json", body, r.Header.Get("Content-Type"))
						return
					}
					ended = true
					_, _ = io.WriteString(w, `{"status":"ended"}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			t.Cleanup(srv.Close)

			stdout, stderr := runHookWithStderr(t, "session-end.sh", `{"session_id":"fallback-root"}`, map[string]string{"ENGRAM_URL": srv.URL})
			if stdout != "" || stderr != "" {
				t.Errorf("hook stdout=%q stderr=%q, want both empty", stdout, stderr)
			}
			mu.Lock()
			defer mu.Unlock()
			want := "GET /sessions/fallback-root,POST /sessions/fallback-root/end"
			if strings.Join(requests, ",") != want {
				t.Errorf("requests=%v, want %s (no runtime end)", requests, want)
			}
			if !ended {
				t.Error("session remains live after SessionEnd")
			}
		})
	}

	// Issue #1624: a resumed session keeps its ended root ID, so SessionEnd
	// must close the live continuation through the runtime scope.
	t.Run("ends a continuation on an older core", func(t *testing.T) {
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
			case "/health":
				_, _ = io.WriteString(w, `{"status":"ok","capabilities":{"runtime_session_resolution":true}}`)
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
		want := []string{"GET /sessions/resumed-root", "GET /health", "POST /runtime-sessions/end"}
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

func TestClaudeSessionEndDoesNotCloseReplacementContinuation(t *testing.T) {
	requireHookBinaries(t)
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "engram.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.New(store.FallbackConfig(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const root, project, mode = "delayed-end", "test-project", store.SessionOwnershipProjectOwned
	directory := t.TempDir()
	if err := db.StartSessionWithOwnershipMode(root, project, directory, mode); err != nil {
		t.Fatal(err)
	}
	if err := db.EndSession(root, ""); err != nil {
		t.Fatal(err)
	}
	first, err := db.ResumeSessionWithOwnershipMode(root, project, directory, mode)
	if err != nil || first != root+":resume:2" {
		t.Fatalf("first continuation = %q, %v", first, err)
	}
	production := server.New(db, 0).Handler()
	var ends, status atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runtime-sessions/end" {
			production.ServeHTTP(w, r)
			return
		}
		if ends.Add(1) == 1 {
			if err := db.EndSession(first, ""); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if id, err := db.ResumeSessionWithOwnershipMode(root, project, directory, mode); err != nil || id != root+":resume:3" {
				t.Errorf("replacement continuation = %q, %v", id, err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		response := httptest.NewRecorder()
		production.ServeHTTP(response, r)
		status.Store(int32(response.Code))
		w.WriteHeader(response.Code)
	}))
	t.Cleanup(endpoint.Close)
	stdout, stderr := runHookWithStderr(t, "session-end.sh", `{"session_id":"delayed-end"}`, map[string]string{"ENGRAM_URL": endpoint.URL})
	if stdout != "" || stderr != "" {
		t.Fatalf("expected a silent hook, got %q, %q", stdout, stderr)
	}
	next, err := db.GetSession(root + ":resume:3")
	if err != nil || next.EndedAt != nil {
		t.Fatalf("replacement should remain live after the delayed close, got %+v, %v", next, err)
	}
	if ends.Load() != 1 || status.Load() != http.StatusConflict {
		t.Fatalf("end calls/status = %d/%d, want one conflict without retry", ends.Load(), status.Load())
	}
}

func TestClaudeContinuationEndRequiresConfirmedScope(t *testing.T) {
	requireHookBinaries(t)
	const supported = `{"status":"ok","capabilities":{"runtime_session_conditional_end":true}}`
	const resolved = `{"id":"root:resume:2","resumed_from":"root","status":"resolved"}`
	for _, tc := range []struct {
		name, health, resolution string
		healthCode, resolveCode  int
		wantEnd                  bool
	}{
		{name: "capability unavailable", healthCode: http.StatusServiceUnavailable},
		{name: "malformed capability", health: `{"status":"ok","capabilities":{"runtime_session_conditional_end":"true"}}`},
		{name: "multiple capability envelopes", health: supported + "\n" + `{"status":"ok"}`},
		{name: "contradictory capability keys", health: `{"status":"ok","capabilities":{"runtime_session_conditional_end":true,"runtime_session_conditional_end":false}}`},
		{name: "overwritten capability object", health: `{"status":"ok","capabilities":{"runtime_session_conditional_end":true},"capabilities":{}}`},
		{name: "redirected capability", health: `{"status":"ok"}`, healthCode: http.StatusFound},
		{name: "resolve unavailable", health: supported, resolveCode: http.StatusNotFound},
		{name: "redirected resolution", health: supported, resolution: resolved, resolveCode: http.StatusFound},
		{name: "multiple resolution envelopes", health: supported, resolution: resolved + "\n" + resolved},
		{name: "wrong root", health: supported, resolution: `{"id":"root:resume:2","resumed_from":"different","status":"resolved"}`},
		{name: "invalid continuation", health: supported, resolution: `{"id":"root:resume:bad","resumed_from":"root","status":"resolved"}`},
		{name: "continuation with trailing newline", health: supported, resolution: `{"id":"root:resume:2\n","resumed_from":"root","status":"resolved"}`},
		{name: "exact continuation", health: supported, resolution: resolved, wantEnd: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ends atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /sessions/root":
					_, _ = io.WriteString(w, `{"id":"root","project":"client","directory":"/work","ownership_mode":"project_owned","ended_at":"2026-10-09 12:00:00"}`)
				case "GET /health":
					if tc.healthCode != 0 {
						w.WriteHeader(tc.healthCode)
					}
					_, _ = io.WriteString(w, tc.health)
				case "POST /runtime-sessions/resolve":
					if tc.resolveCode != 0 {
						w.WriteHeader(tc.resolveCode)
					}
					_, _ = io.WriteString(w, tc.resolution)
				case "POST /runtime-sessions/end":
					ends.Add(1)
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["expected_effective_id"] != "root:resume:2" || body["id"] != "root" {
						t.Errorf("conditional end body = %v, %v", body, err)
					}
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
			}))
			t.Cleanup(endpoint.Close)
			runHook(t, "session-end.sh", `{"session_id":"root"}`, map[string]string{"ENGRAM_URL": endpoint.URL})
			var want int32
			if tc.wantEnd {
				want = 1
			}
			if got := ends.Load(); got != want {
				t.Fatalf("runtime end requests = %d, want %d", got, want)
			}
		})
	}
}
