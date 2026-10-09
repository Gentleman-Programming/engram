package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeHooksSocket(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "hook.sock"))
	if err != nil {
		t.Skipf("Unix socket unavailable: %v", err)
	}
	var captures atomic.Int32
	endpoint := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			codexMockHealth(w)
		case "/project/current":
			_, _ = io.WriteString(w, `{"project":"socket-project","project_source":"config"}`)
		case "/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"host","status":"created"}`)
		case "/runtime-sessions/resolve":
			_, _ = io.WriteString(w, `{"id":"host:resume:2","resumed_from":"host","status":"resolved"}`)
		case "/runtime-sessions/end":
			_, _ = io.WriteString(w, `{"id":"host:resume:2","resumed_from":"host","status":"ended"}`)
		case "/prompts":
			captures.Add(1)
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["session_id"] != "host:resume:2" {
				t.Errorf("prompt binding = %v", body)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	endpoint.Listener = listener
	endpoint.Start()
	defer endpoint.Close()
	t.Setenv("ENGRAM_URL", "")
	t.Setenv("ENGRAM_SOCKET", listener.Addr().String())
	t.Setenv("ENGRAM_PORT", "invalid")
	input := []byte(`{"session_id":"host","cwd":"/work","prompt":"remember","tool_name":"mcp__engram__mem_save","tool_input":{}}`)
	t.Run("lifecycle", func(t *testing.T) {
		if got := runCodexLifecycle("codex-register", input, codexHookURL()); got != "host" {
			t.Fatalf("registration = %q", got)
		}
	})
	t.Run("guard", func(t *testing.T) {
		if got := string(guardCodexPreToolUse(input)); !strings.Contains(got, `"session_id":"host:resume:2"`) {
			t.Fatalf("guard = %s", got)
		}
	})
	t.Run("prompt", func(t *testing.T) {
		// Count capture separately so a network-independent first message cannot hide failure.
		_ = runCodexUserPromptSubmit(input, codexHookURL(), t.TempDir(), time.Now)
		if captures.Load() != 1 {
			t.Fatalf("prompt captures = %d", captures.Load())
		}
	})
	t.Run("claude", func(t *testing.T) {
		if err := confirmHookSession("host", "/work", true); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHookURLPrecedence(t *testing.T) {
	t.Setenv("ENGRAM_URL", " http://example.test/ ")
	t.Setenv("ENGRAM_SOCKET", "unused.sock")
	t.Setenv("ENGRAM_PORT", "invalid")
	if got := codexHookURL(); got != "http://example.test" {
		t.Fatalf("URL = %q", got)
	}
}
