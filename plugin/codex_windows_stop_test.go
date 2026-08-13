package plugin_test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const codexStopResponse = `{"continue":true,"suppressOutput":false}`

type codexHooksManifest struct {
	Hooks map[string][]struct {
		Hooks []struct {
			Type           string `json:"type"`
			Command        string `json:"command"`
			CommandWindows string `json:"commandWindows"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func TestCodexWindowsStopHook(t *testing.T) {
	root := repoRoot(t)

	t.Run("declares the Windows adapter", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "hooks", "hooks.json"))
		if err != nil {
			t.Fatalf("read hooks manifest: %v", err)
		}

		var manifest codexHooksManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("parse hooks manifest: %v", err)
		}

		const want = `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "${PLUGIN_ROOT}\scripts\session-stop.ps1"`
		for _, group := range manifest.Hooks["Stop"] {
			for _, hook := range group.Hooks {
				if hook.Type == "command" && hook.CommandWindows == want {
					return
				}
			}
		}
		t.Fatalf("Stop hook must declare commandWindows %q", want)
	})

	t.Run("provides a fail-open PowerShell adapter", func(t *testing.T) {
		path := filepath.Join(root, "plugin", "codex", "scripts", "session-stop.ps1")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read Windows session-stop adapter: %v", err)
		}
		content := string(data)
		for _, required := range []string{
			"[Console]::In.ReadToEnd()",
			"ConvertFrom-Json",
			"session_id",
			"ENGRAM_PORT",
			"'7437'",
			"[System.Uri]::EscapeDataString",
			"http://127.0.0.1:",
			"/sessions/",
			"/end",
			"Invoke-WebRequest",
			"-UseBasicParsing",
			"-TimeoutSec 3",
			"-MaximumRedirection 0",
			"*> $null",
			codexStopResponse,
			"exit 0",
		} {
			if !strings.Contains(content, required) {
				t.Errorf("Windows session-stop adapter must contain %q", required)
			}
		}
	})

	t.Run("bumps the Codex plugin version", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", ".codex-plugin", "plugin.json"))
		if err != nil {
			t.Fatalf("read Codex plugin manifest: %v", err)
		}
		var manifest pluginJSON
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatalf("parse Codex plugin manifest: %v", err)
		}
		if manifest.Version != "0.1.2" {
			t.Errorf("Codex plugin version = %q, want 0.1.2", manifest.Version)
		}
	})
}

func TestCodexWindowsSessionStopAdapter(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows PowerShell")
	}

	root := repoRoot(t)
	source, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "scripts", "session-stop.ps1"))
	if err != nil {
		t.Fatalf("read adapter: %v", err)
	}
	pluginRoot := filepath.Join(t.TempDir(), "plugin root with spaces")
	adapterPath := filepath.Join(pluginRoot, "scripts", "session-stop.ps1")
	if err := os.MkdirAll(filepath.Dir(adapterPath), 0o755); err != nil {
		t.Fatalf("create adapter directory: %v", err)
	}
	if err := os.WriteFile(adapterPath, source, 0o644); err != nil {
		t.Fatalf("copy adapter: %v", err)
	}

	requests := make(chan string, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Method + " " + r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	})}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	port := strings.TrimPrefix(listener.Addr().String(), "127.0.0.1:")

	t.Run("posts an escaped session ID through a path containing spaces", func(t *testing.T) {
		stdout, stderr, code := runCodexWindowsStop(t, adapterPath, `{"session_id":"session id/with?characters"}`, &port)
		if code != 0 || stdout != codexStopResponse || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 0 with Stop response %q", code, stdout, stderr, codexStopResponse)
		}
		select {
		case got := <-requests:
			if got != "POST /sessions/session%20id%2Fwith%3Fcharacters/end" {
				t.Errorf("request = %q", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("adapter did not post to the loopback server")
		}
	})

	t.Run("executes the manifest command through cmd.exe", func(t *testing.T) {
		command := strings.ReplaceAll(codexWindowsStopCommand(t, root), "${PLUGIN_ROOT}", pluginRoot)
		stdout, stderr, code := runCodexWindowsManifestCommand(t, command, `{"session_id":"session id/with?characters"}`, port)
		if code != 0 || stdout != codexStopResponse || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 0 with Stop response %q", code, stdout, stderr, codexStopResponse)
		}
		select {
		case got := <-requests:
			if got != "POST /sessions/session%20id%2Fwith%3Fcharacters/end" {
				t.Errorf("request = %q", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("manifest command did not post to the loopback server")
		}
	})

	for _, tc := range []struct {
		name  string
		input string
		port  *string
	}{
		{name: "empty input", input: "", port: &port},
		{name: "malformed input", input: "{", port: &port},
		{name: "missing session ID", input: `{}`, port: &port},
		{name: "invalid nonnumeric port", input: `{"session_id":"id"}`, port: stringPointer("invalid")},
		{name: "invalid out of range port", input: `{"session_id":"id"}`, port: stringPointer("65536")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runCodexWindowsStop(t, adapterPath, tc.input, tc.port)
			if code != 0 || stdout != codexStopResponse || stderr != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 0 with Stop response %q", code, stdout, stderr, codexStopResponse)
			}
			select {
			case got := <-requests:
				t.Fatalf("unexpected request %q", got)
			default:
			}
		})
	}

	t.Run("fails open when the API is unreachable", func(t *testing.T) {
		closedListener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve closed port: %v", err)
		}
		closedPort := strings.TrimPrefix(closedListener.Addr().String(), "127.0.0.1:")
		if err := closedListener.Close(); err != nil {
			t.Fatalf("close reserved port: %v", err)
		}
		stdout, stderr, code := runCodexWindowsStop(t, adapterPath, `{"session_id":"id"}`, &closedPort)
		if code != 0 || stdout != codexStopResponse || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 0 with Stop response %q", code, stdout, stderr, codexStopResponse)
		}
	})
}

func runCodexWindowsStop(t *testing.T, adapterPath, input string, port *string) (string, string, int) {
	t.Helper()
	command := "& '" + strings.ReplaceAll(adapterPath, "'", "''") + "'"
	if port != nil {
		command = "$env:ENGRAM_PORT='" + strings.ReplaceAll(*port, "'", "''") + "'; " + command
	} else {
		command = "Remove-Item Env:ENGRAM_PORT -ErrorAction SilentlyContinue; " + command
	}
	run := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", command)
	run.Stdin = strings.NewReader(input)
	var stdout, stderr strings.Builder
	run.Stdout = &stdout
	run.Stderr = &stderr
	err := run.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdout.String(), stderr.String(), exitErr.ExitCode()
	}
	t.Fatalf("run adapter: %v", err)
	return "", "", -1
}

func codexWindowsStopCommand(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks manifest: %v", err)
	}
	var manifest codexHooksManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse hooks manifest: %v", err)
	}
	for _, group := range manifest.Hooks["Stop"] {
		for _, hook := range group.Hooks {
			if hook.Type == "command" && hook.CommandWindows != "" {
				return hook.CommandWindows
			}
		}
	}
	t.Fatal("Stop hook does not declare commandWindows")
	return ""
}

func stringPointer(value string) *string {
	return &value
}
