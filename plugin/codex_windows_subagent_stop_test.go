package plugin_test

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCodexSubagentStopHook(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks manifest: %v", err)
	}

	var manifest codexHooksManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse hooks manifest: %v", err)
	}

	const wantUnix = `"${PLUGIN_ROOT}/scripts/subagent-stop.sh"`
	const wantWindows = `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "${PLUGIN_ROOT}\scripts\subagent-stop.ps1"`
	for _, group := range manifest.Hooks["SubagentStop"] {
		for _, hook := range group.Hooks {
			if hook.Type == "command" && hook.Command == wantUnix && hook.CommandWindows == wantWindows && hook.Timeout == 10 {
				return
			}
		}
	}
	t.Fatalf("SubagentStop hook must declare command %q, commandWindows %q, and timeout 10", wantUnix, wantWindows)
}

func TestCodexWindowsSubagentStopAdapter(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires native Windows cmd.exe and PowerShell")
	}

	root := repoRoot(t)
	source, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "scripts", "subagent-stop.ps1"))
	if err != nil {
		t.Fatalf("read adapter: %v", err)
	}
	pluginRoot := filepath.Join(t.TempDir(), "plugin root with spaces")
	adapterPath := filepath.Join(pluginRoot, "scripts", "subagent-stop.ps1")
	if err := os.MkdirAll(filepath.Dir(adapterPath), 0o755); err != nil {
		t.Fatalf("create adapter directory: %v", err)
	}
	if err := os.WriteFile(adapterPath, source, 0o644); err != nil {
		t.Fatalf("copy adapter: %v", err)
	}

	requests := make(chan map[string]any, 2)
	redirectedRequests := make(chan string, 2)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/project/current":
			if r.URL.Query().Get("cwd") == "redirect-project" {
				http.Redirect(w, r, "/redirected-project", http.StatusFound)
				return
			}
			if r.URL.Query().Get("cwd") != root {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"project":"codex-test-project","project_source":"git_root"}`))
		case "/observations/passive":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if payload["content"] == "unavailable capture" {
				http.Error(w, "capture unavailable", http.StatusServiceUnavailable)
				return
			}
			if payload["content"] == "redirect capture" {
				http.Redirect(w, r, "/redirected-capture", http.StatusFound)
				return
			}
			requests <- payload
			w.WriteHeader(http.StatusOK)
		case "/redirected-project", "/redirected-capture":
			redirectedRequests <- r.URL.Path
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	port := strings.TrimPrefix(listener.Addr().String(), "127.0.0.1:")

	input := `{"session_id":"session-1","cwd":"` + strings.ReplaceAll(root, `\`, `\\`) + `","last_assistant_message":"completed normally"}`
	t.Run("captures through the manifest command", func(t *testing.T) {
		command := strings.ReplaceAll(codexSubagentStopWindowsCommand(t, root), "${PLUGIN_ROOT}", pluginRoot)
		stdout, stderr, code := runCodexWindowsManifestCommand(t, command, input, port)
		assertCodexSubagentStopHookEnvelope(t, stdout, stderr, code)
		select {
		case payload := <-requests:
			if payload["content"] != "completed normally" || payload["project"] != "codex-test-project" || payload["source"] != "subagent-stop" {
				t.Fatalf("unexpected passive capture payload: %#v", payload)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("manifest command did not post passive capture")
		}
	})

	t.Run("captures stdout when last assistant message is omitted", func(t *testing.T) {
		stdoutFallbackInput := `{"session_id":"session-stdout","cwd":"` + strings.ReplaceAll(root, `\`, `\\`) + `","stdout":"captured from stdout"}`
		stdout, stderr, code := runCodexWindowsSubagentStop(t, adapterPath, stdoutFallbackInput, &port)
		assertCodexSubagentStopHookEnvelope(t, stdout, stderr, code)
		select {
		case payload := <-requests:
			if payload["content"] != "captured from stdout" {
				t.Fatalf("passive capture content = %#v, want stdout fallback", payload["content"])
			}
		case <-time.After(3 * time.Second):
			t.Fatal("stdout fallback did not post passive capture")
		}
	})

	t.Run("enforces a shared network budget", func(t *testing.T) {
		slowListener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen for slow server: %v", err)
		}
		slowServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/project/current":
				time.Sleep(time.Second)
				_, _ = w.Write([]byte(`{"project":"codex-test-project","project_source":"git_root"}`))
			case "/observations/passive":
				time.Sleep(10 * time.Second)
			default:
				http.NotFound(w, r)
			}
		})}
		go func() { _ = slowServer.Serve(slowListener) }()
		t.Cleanup(func() { _ = slowServer.Close() })
		slowPort := strings.TrimPrefix(slowListener.Addr().String(), "127.0.0.1:")

		started := time.Now()
		stdout, stderr, code := runCodexWindowsPowerShell(t, adapterPath, input, &slowPort, 9*time.Second)
		elapsed := time.Since(started)
		assertCodexSubagentStopHookEnvelope(t, stdout, stderr, code)
		if !strings.Contains(stderr, "passive capture failed") {
			t.Fatalf("stderr=%q, want capture failure diagnostic", stderr)
		}
		if elapsed >= 8*time.Second {
			t.Fatalf("adapter ran for %v, want completion with finalization margin before the 10-second hook timeout", elapsed)
		}
	})

	for _, tc := range []struct {
		name       string
		input      string
		wantStderr string
	}{
		{name: "empty input", input: ""},
		{name: "malformed input", input: "{", wantStderr: "invalid SubagentStop input"},
		{name: "project resolution unavailable", input: `{"cwd":"missing","last_assistant_message":"captured"}`, wantStderr: "unable to resolve project"},
		{name: "capture unavailable", input: strings.Replace(input, "completed normally", "unavailable capture", 1), wantStderr: "passive capture failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runCodexWindowsSubagentStop(t, adapterPath, tc.input, &port)
			assertCodexSubagentStopHookEnvelope(t, stdout, stderr, code)
			if tc.wantStderr != "" && !strings.Contains(stderr, tc.wantStderr) {
				t.Fatalf("stderr=%q, want diagnostic containing %q", stderr, tc.wantStderr)
			}
			select {
			case payload := <-requests:
				t.Fatalf("unexpected passive capture payload: %#v", payload)
			default:
			}
		})
	}

	for _, tc := range []struct {
		name string
		port string
	}{
		{name: "rejects nonnumeric port", port: "invalid"},
		{name: "rejects zero port", port: "0"},
		{name: "rejects out of range port", port: "65536"},
		{name: "rejects signed port", port: "-1"},
		{name: "rejects padded port", port: " 7437"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runCodexWindowsSubagentStop(t, adapterPath, input, &tc.port)
			assertCodexSubagentStopHookEnvelope(t, stdout, stderr, code)
			if strings.Count(stderr, "engram:") != 1 || !strings.Contains(stderr, "invalid ENGRAM_PORT") {
				t.Fatalf("stderr=%q, want one invalid port diagnostic", stderr)
			}
			select {
			case payload := <-requests:
				t.Fatalf("unexpected passive capture payload: %#v", payload)
			case redirected := <-redirectedRequests:
				t.Fatalf("unexpected redirected request: %q", redirected)
			default:
			}
		})
	}

	t.Run("refuses project resolution redirects", func(t *testing.T) {
		redirectInput := `{"session_id":"session-redirect-project","cwd":"redirect-project","last_assistant_message":"captured"}`
		stdout, stderr, code := runCodexWindowsSubagentStop(t, adapterPath, redirectInput, &port)
		assertCodexSubagentStopHookEnvelope(t, stdout, stderr, code)
		if !strings.Contains(stderr, "unable to resolve project") {
			t.Fatalf("stderr=%q, want project resolution diagnostic", stderr)
		}
		select {
		case redirected := <-redirectedRequests:
			t.Fatalf("followed project resolution redirect to %q", redirected)
		default:
		}
	})

	t.Run("refuses passive capture redirects", func(t *testing.T) {
		redirectInput := strings.Replace(input, "completed normally", "redirect capture", 1)
		stdout, stderr, code := runCodexWindowsSubagentStop(t, adapterPath, redirectInput, &port)
		assertCodexSubagentStopHookEnvelope(t, stdout, stderr, code)
		if !strings.Contains(stderr, "passive capture failed") {
			t.Fatalf("stderr=%q, want passive capture failure diagnostic", stderr)
		}
		select {
		case redirected := <-redirectedRequests:
			t.Fatalf("followed passive capture redirect to %q", redirected)
		default:
		}
	})
}

func runCodexWindowsSubagentStop(t *testing.T, adapterPath, input string, port *string) (string, string, int) {
	t.Helper()
	return runCodexWindowsPowerShell(t, adapterPath, input, port, 5*time.Second)
}

func codexSubagentStopWindowsCommand(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks manifest: %v", err)
	}
	var manifest codexHooksManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse hooks manifest: %v", err)
	}
	for _, group := range manifest.Hooks["SubagentStop"] {
		for _, hook := range group.Hooks {
			if hook.Type == "command" && hook.CommandWindows != "" {
				return hook.CommandWindows
			}
		}
	}
	t.Fatal("SubagentStop hook does not declare commandWindows")
	return ""
}

func assertCodexSubagentStopHookEnvelope(t *testing.T, stdout, stderr string, code int) {
	t.Helper()
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 0", code, stdout, stderr)
	}
	if stdout != "{}\n" && stdout != "{}\r\n" {
		t.Fatalf("stdout=%q, want exactly one empty JSON hook result", stdout)
	}
	var envelope map[string]any
	if err := json.NewDecoder(bytes.NewBufferString(stdout)).Decode(&envelope); err != nil {
		t.Fatalf("stdout=%q is not valid JSON: %v", stdout, err)
	}
	if len(envelope) != 0 {
		t.Fatalf("stdout=%q decoded to non-empty hook result: %#v", stdout, envelope)
	}
}
