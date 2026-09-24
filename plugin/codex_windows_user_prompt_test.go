//go:build windows

package plugin_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
)

func TestCodexWindowsNativeUserPromptAdapterContract(t *testing.T) {
	root := repoRoot(t)
	command := codexWindowsUserPromptCommand(t, root)
	// Codex does not expand environment references in executable position.
	const prefix = `\\.\GLOBALROOT\SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand `
	if !strings.HasPrefix(command, prefix) || strings.Contains(command, "%SystemRoot%") || strings.ContainsAny(command, `"'`) {
		t.Fatalf("UserPromptSubmit commandWindows must have quote-free pinned encoded launch: %q", command)
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(command, prefix))
	if err != nil || len(payload)%2 != 0 {
		t.Fatalf("invalid UTF-16LE encoded command: %v", err)
	}
	units := make([]uint16, len(payload)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(payload[i*2:])
	}
	const bootstrap = `$ProgressPreference = 'SilentlyContinue'; if ($env:PLUGIN_ROOT) { & (Join-Path $env:PLUGIN_ROOT 'scripts\run-native-hook.ps1'); exit $LASTEXITCODE }`
	if decoded := string(utf16.Decode(units)); decoded != bootstrap {
		t.Fatalf("decoded bootstrap = %q, want %q", decoded, bootstrap)
	}
	data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest codexHooksManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if got := manifest.Hooks["UserPromptSubmit"][0].Hooks[0].Timeout; got != 2 {
		t.Fatalf("UserPromptSubmit timeout = %d, want 2", got)
	}

	source, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "scripts", "run-native-hook.ps1"))
	if err != nil {
		t.Fatalf("read native hook adapter: %v", err)
	}
	content := string(source)
	for _, token := range []string{"APPDATA", "USERPROFILE", "config.toml", "ReadLine()", "# engram-windows-hook-command-v1: ", "ConvertFrom-Json", "hook codex-user-prompt-submit", "[System.IO.Path]::IsPathRooted", "[System.IO.File]::Exists", "exit $LASTEXITCODE"} {
		if !strings.Contains(content, token) {
			t.Errorf("native hook adapter must contain %q", token)
		}
	}
	for _, token := range []string{"mcp_servers", "[regex]", "Get-Command", "LookPath", "PATHEXT", "where.exe", "Start-Process", "engram.exe", "engram.cmd", "engram.bat"} {
		if strings.Contains(strings.ToLower(content), strings.ToLower(token)) {
			t.Errorf("native hook adapter must not contain runtime discovery token %q", token)
		}
	}
}

func TestCodexWindowsNativeUserPromptPreservesPinnedProcessIOAndExit(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and executes a pinned executable")
	}
	root, appData := repoRoot(t), t.TempDir()
	pinned := buildCodexWindowsProgram(t, `package main
import ("io"; "os")
func main() { _, _ = io.Copy(os.Stdout, os.Stdin); os.Exit(23) }
`)
	input, profile := `{"session_id":"pinned-process"}`, t.TempDir()
	for _, tt := range []struct {
		name, configRoot, appData string
		env                       []string
	}{
		{name: "APPDATA", configRoot: appData, appData: appData},
		{name: "USERPROFILE fallback", configRoot: filepath.Join(profile, "AppData", "Roaming"), appData: "relative", env: []string{"USERPROFILE=" + profile}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writeCodexWindowsConfig(t, tt.configRoot, codexWindowsHookMarker(pinned)+"\n[mcp_servers.engram]\ncommand = \"ignored.exe\"")
			stdout, stderr, code := runCodexNativeManifestCommand(t, codexNativeAdapter(t, root), input, tt.appData, "0", t.TempDir(), t.TempDir(), tt.env)
			if code != 23 || string(stdout) != input || len(stderr) != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want pinned child stdin/stdout and exit", code, stdout, stderr)
			}
		})
	}
}

func TestCodexWindowsNativeUserPromptTrustedPinForwardsInputAndMeetsTimingBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and executes the native hook command")
	}
	root, appData := repoRoot(t), t.TempDir()
	trusted := buildCodexWindowsEngram(t, root, filepath.Join(t.TempDir(), "trusted binary with spaces", "engram.exe"))
	writeCodexWindowsConfig(t, appData, codexWindowsHookMarker(trusted)+"\n# engram-windows-hook-command-v1: \"C:\\\\later-spoof.exe\"\n[mcp_servers.engram]\ncommand = \"ignored.exe\"\nargs = [\"mcp\"]\n")

	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/project/current":
			_, _ = io.WriteString(w, `{"project":"engram","project_source":"git_root"}`)
		case "/prompts":
			posts.Add(1)
			var prompt struct{ Content string }
			if err := json.NewDecoder(r.Body).Decode(&prompt); err != nil || prompt.Content != "persist once" {
				t.Errorf("prompt POST = %#v, decode error = %v", prompt, err)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/sessions/window-session":
			_, _ = io.WriteString(w, `{"started_at":"2020-01-01T00:00:00Z"}`)
		case "/observations":
			_, _ = io.WriteString(w, `[{"created_at":"2020-01-01T00:00:00Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	port, command, stateDir := strings.TrimPrefix(server.URL, "http://127.0.0.1:"), codexNativeAdapter(t, root), t.TempDir()
	run := func(session string) (string, time.Duration) {
		start := time.Now()
		stdout, stderr, code := runCodexNativeManifestCommand(t, command, `{"cwd":"C:\\work","session_id":"`+session+`","prompt":"persist once"}`, appData, port, stateDir, t.TempDir(), nil)
		if code != 0 || !json.Valid(stdout) || len(stderr) != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 0, valid JSON, and no stderr", code, stdout, stderr)
		}
		return string(stdout), time.Since(start)
	}
	for _, want := range []string{"CRITICAL FIRST ACTION", "MEMORY REMINDER"} {
		if output, _ := run("window-session"); !strings.Contains(output, want) {
			t.Fatalf("output=%q, want %q", output, want)
		}
	}
	if posts.Load() != 2 {
		t.Fatalf("prompt posts=%d, want one dispatch per invocation without retry", posts.Load())
	}

	first, subsequent := make([]time.Duration, 0, 12), make([]time.Duration, 0, 12)
	for i := 0; i < 12; i++ {
		session := "timing-" + strconv.Itoa(i)
		_, firstElapsed := run(session)
		_, subsequentElapsed := run(session)
		first, subsequent = append(first, firstElapsed), append(subsequent, subsequentElapsed)
	}
	p95 := func(durations []time.Duration) time.Duration {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		return durations[(len(durations)*95+99)/100-1]
	}
	firstP95, subsequentP95 := p95(first), p95(subsequent)
	t.Logf("native first-message p95=%s; subsequent-message network p95=%s across %d paired runs", firstP95, subsequentP95, len(first))
	if posts.Load() != 26 {
		t.Fatalf("prompt posts=%d, want 26 posts", posts.Load())
	}
	if os.Getenv("ENGRAM_TIMING_TESTS") == "1" && (firstP95 >= 1500*time.Millisecond || subsequentP95 >= 1500*time.Millisecond) {
		t.Fatalf("p95 first=%s subsequent=%s, want both <1.5s", firstP95, subsequentP95)
	}
}

func TestCodexWindowsNativeUserPromptIgnoresInterpreterAndBinaryHijacks(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and executes hostile executable fixtures")
	}
	root, appData := repoRoot(t), t.TempDir()
	trusted := buildCodexWindowsEngram(t, root, filepath.Join(t.TempDir(), "trusted binary with spaces", "engram.exe"))
	writeCodexWindowsConfig(t, appData, codexWindowsHookMarker(trusted)+"\n[mcp_servers.engram]\ncommand = \"ignored.exe\"\n")

	marker := filepath.Join(t.TempDir(), "hijack-marker")
	fake := buildCodexWindowsProgram(t, `package main
import "os"
func main() { _ = os.WriteFile(os.Getenv("FAKE_HIJACK_MARKER"), []byte("hijacked"), 0600) }
`)
	cwd, hostilePath := t.TempDir(), t.TempDir()
	for _, path := range []string{filepath.Join(cwd, "powershell.exe"), filepath.Join(cwd, "engram.exe"), filepath.Join(hostilePath, "powershell.exe"), filepath.Join(hostilePath, "engram.exe")} {
		copyCodexWindowsFile(t, fake, path)
	}
	for _, name := range []string{"engram.cmd", "engram.bat"} {
		if err := os.WriteFile(filepath.Join(cwd, name), []byte("@echo hijacked\r\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	stdout, stderr, code := runCodexNativeManifestCommand(t, codexNativeAdapter(t, root), `{"cwd":"C:\\work","session_id":"hijack-proof","prompt":"persist once"}`, appData, "0", t.TempDir(), cwd, []string{"PATH=" + hostilePath, "FAKE_HIJACK_MARKER=" + marker})
	if code != 0 || !json.Valid(stdout) || len(stderr) != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want trusted hook result", code, stdout, stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("fake interpreter or binary was invoked; marker stat error = %v", err)
	}
}

func TestCodexWindowsNativeUserPromptMissingPluginRootFailsOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("executes the native hook command")
	}
	command := codexWindowsUserPromptCommand(t, repoRoot(t))
	stdout, stderr, code := runCodexNativeManifestCommand(t, command, `{"session_id":"missing-root"}`, t.TempDir(), "0", t.TempDir(), t.TempDir(), []string{"PLUGIN_ROOT="})
	if code != 0 || len(stdout) != 0 || len(stderr) != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want silent fail-open", code, stdout, stderr)
	}
}

func TestCodexWindowsNativeUserPromptInvalidPinsFailOpenSilently(t *testing.T) {
	if testing.Short() {
		t.Skip("executes the native hook command")
	}
	root, appData := repoRoot(t), t.TempDir()
	command, missing, stale := codexNativeAdapter(t, root), filepath.Join(appData, "codex", "config.toml"), filepath.Join(t.TempDir(), "missing.exe")
	const marker = "# engram-windows-hook-command-v1: "
	cases := []struct {
		name, content string
	}{
		{name: "missing config"},
		{name: "no marker", content: "[mcp_servers.engram]\ncommand = \"ignored.exe\""},
		{name: "displaced marker", content: "[mcp_servers.engram]\n" + marker + strconv.Quote(stale)},
		{name: "malformed JSON", content: marker + "\"C:\\\\broken"},
		{name: "non-string JSON", content: marker + "42"},
		{name: "relative executable", content: marker + "\"engram.exe\""},
		{name: "wrong extension", content: marker + "\"C:\\\\safe\\\\engram.cmd\""},
		{name: "stale executable", content: codexWindowsHookMarker(stale)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.Remove(missing); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove config: %v", err)
			}
			if tt.content != "" {
				writeCodexWindowsConfig(t, appData, tt.content)
			}
			stdout, stderr, code := runCodexNativeManifestCommand(t, command, `{"session_id":"invalid-pin"}`, appData, "0", t.TempDir(), t.TempDir(), nil)
			if code != 0 || len(stdout) != 0 || len(stderr) != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want silent fail-open", code, stdout, stderr)
			}
		})
	}
}

func codexWindowsHookMarker(command string) string {
	return "# engram-windows-hook-command-v1: " + strconv.Quote(command)
}

func codexNativeAdapter(t *testing.T, root string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "scripts", "run-native-hook.ps1"))
	if err != nil {
		t.Fatalf("read native hook adapter: %v", err)
	}
	pluginRoot := filepath.Join(t.TempDir(), "plugin root with spaces")
	destination := filepath.Join(pluginRoot, "scripts", "run-native-hook.ps1")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatalf("create plugin scripts: %v", err)
	}
	if err := os.WriteFile(destination, source, 0o600); err != nil {
		t.Fatalf("copy native hook adapter: %v", err)
	}
	t.Setenv("PLUGIN_ROOT", pluginRoot)
	return codexWindowsUserPromptCommand(t, root)
}

func codexWindowsUserPromptCommand(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest codexHooksManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, group := range manifest.Hooks["UserPromptSubmit"] {
		for _, hook := range group.Hooks {
			if hook.CommandWindows != "" {
				return hook.CommandWindows
			}
		}
	}
	t.Fatal("missing UserPromptSubmit commandWindows")
	return ""
}

func buildCodexWindowsEngram(t *testing.T, root, target string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create binary directory: %v", err)
	}
	build := exec.Command("go", "build", "-o", target, "./cmd/engram")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build engram: %v: %s", err, output)
	}
	return target
}

func buildCodexWindowsProgram(t *testing.T, source string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("write fixture source: %v", err)
	}
	target := filepath.Join(directory, "fixture.exe")
	build := exec.Command("go", "build", "-o", target, "main.go")
	build.Dir = directory
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	return target
}

func copyCodexWindowsFile(t *testing.T, source, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read %q: %v", source, err)
	}
	if err := os.WriteFile(destination, data, 0o700); err != nil {
		t.Fatalf("write %q: %v", destination, err)
	}
}

func writeCodexWindowsConfig(t *testing.T, appData, content string) {
	t.Helper()
	path := filepath.Join(appData, "codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func runCodexNativeManifestCommand(t *testing.T, command, input, appData, port, stateDir, cwd string, envOverrides []string) ([]byte, []byte, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, "cmd.exe")
	run.WaitDelay = time.Second
	run.SysProcAttr = &syscall.SysProcAttr{CmdLine: "/D /S /C \"" + command + "\""}
	run.Dir, run.Env = cwd, append([]string{}, os.Environ()...)
	for _, override := range append([]string{"APPDATA=" + appData, "ENGRAM_PORT=" + port, "TEMP=" + stateDir, "TMP=" + stateDir}, envOverrides...) {
		key, _, _ := strings.Cut(override, "=")
		prefix := strings.ToUpper(key) + "="
		filtered := run.Env[:0]
		for _, entry := range run.Env {
			if !strings.HasPrefix(strings.ToUpper(entry), prefix) {
				filtered = append(filtered, entry)
			}
		}
		run.Env = append(filtered, override)
	}
	run.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	run.Stdout, run.Stderr = &stdout, &stderr
	if err := run.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return stdout.Bytes(), stderr.Bytes(), exit.ExitCode()
		}
		t.Fatal(err)
	}
	return stdout.Bytes(), stderr.Bytes(), 0
}
