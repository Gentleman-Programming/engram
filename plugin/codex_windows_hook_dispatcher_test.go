package plugin_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestCodexWindowsBashHookDispatcherContract(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks manifest: %v", err)
	}

	var manifest codexHooksManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse hooks manifest: %v", err)
	}

	const prefix = `\\.\GLOBALROOT\SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand `
	wantMappings := map[string]string{
		`"${PLUGIN_ROOT}/scripts/session-start.sh"`:   "session-start.sh",
		`"${PLUGIN_ROOT}/scripts/post-compaction.sh"`: "post-compaction.sh",
	}
	seenMappings := make(map[string]bool, len(wantMappings))
	for event, groups := range manifest.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if hook.Type != "command" || !strings.HasSuffix(strings.Trim(hook.Command, `"`), ".sh") {
					continue
				}
				if hook.CommandWindows == "" {
					t.Errorf("%s hook %q must declare commandWindows", event, hook.Command)
					continue
				}
				if want, ok := wantMappings[hook.Command]; ok {
					seenMappings[hook.Command] = true
					if hook.Timeout != 10 || !strings.HasPrefix(hook.CommandWindows, prefix) || strings.ContainsAny(hook.CommandWindows, `"'`) {
						t.Errorf("%s hook must retain timeout 10 and quote-free pinned launch: %q", event, hook.CommandWindows)
						continue
					}
					payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(hook.CommandWindows, prefix))
					if err != nil || len(payload)%2 != 0 {
						t.Errorf("%s invalid UTF-16LE bootstrap: %v", event, err)
						continue
					}
					units := make([]uint16, len(payload)/2)
					for i := range units {
						units[i] = binary.LittleEndian.Uint16(payload[i*2:])
					}
					bootstrap := `$ProgressPreference = 'SilentlyContinue'; if ($env:PLUGIN_ROOT) { $scripts = Join-Path $env:PLUGIN_ROOT 'scripts'; & (Join-Path $scripts 'run-bash-hook.ps1') (Join-Path $scripts '` + want + `'); exit $LASTEXITCODE }`
					if got := string(utf16.Decode(units)); got != bootstrap {
						t.Errorf("%s bootstrap = %q, want %q", event, got, bootstrap)
					}
				}
			}
		}
	}
	for command := range wantMappings {
		if !seenMappings[command] {
			t.Errorf("missing expected hook command %q", command)
		}
	}

	source, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "scripts", "run-bash-hook.ps1"))
	if err != nil {
		t.Fatalf("read Windows Bash dispatcher: %v", err)
	}
	content := string(source)
	for _, required := range []string{
		"[System.Diagnostics.ProcessStartInfo]",
		"UseShellExecute = $false",
		"CreateNoWindow = $true",
		"RedirectStandardInput = $true",
		"RedirectStandardOutput = $true",
		"RedirectStandardError = $true",
		"[Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false)",
		"OpenStandardInput().CopyToAsync($standardInput.BaseStream)",
		"StandardOutput.BaseStream.CopyToAsync([Console]::OpenStandardOutput())",
		"StandardError.BaseStream.CopyToAsync([Console]::OpenStandardError())",
		"git.exe",
		"bin/bash.exe",
		"usr/bin/bash.exe",
		"--noprofile",
		"--norc",
		"WaitForExit()",
		"exit $process.ExitCode",
		"exit 0",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("dispatcher must contain %q", required)
		}
	}
	for _, forbidden := range []string{"git-bash.exe", "mintty.exe", "& bash", "Start-Process", "Get-Command bash"} {
		if strings.Contains(strings.ToLower(content), strings.ToLower(forbidden)) {
			t.Errorf("dispatcher must not contain %q", forbidden)
		}
	}
}

func TestCodexWindowsUserPromptManifestKeepsUnixScriptAndTimeout(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks manifest: %v", err)
	}
	var manifest codexHooksManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse hooks manifest: %v", err)
	}
	for _, group := range manifest.Hooks["UserPromptSubmit"] {
		for _, hook := range group.Hooks {
			if hook.Command == `"${PLUGIN_ROOT}/scripts/user-prompt-submit.sh"` && hook.Timeout == 2 {
				return
			}
		}
	}
	t.Fatal("UserPromptSubmit must retain the Unix script and 2-second timeout")
}

func TestCodexWindowsBashHookDispatcherRuntime(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires native Windows cmd.exe and PowerShell")
	}
	if testing.Short() {
		t.Skip("executes Git for Windows Bash through the dispatcher")
	}
	bashPath, ok := codexGitForWindowsBash()
	if !ok {
		t.Skip("Git for Windows Bash unavailable: git.exe does not resolve to an installation with bin/bash.exe or usr/bin/bash.exe")
	}

	root := repoRoot(t)
	source, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "scripts", "run-bash-hook.ps1"))
	if err != nil {
		t.Fatalf("read dispatcher: %v", err)
	}
	pluginRoot := filepath.Join(t.TempDir(), "plugin root with spaces")
	dispatcherPath := filepath.Join(pluginRoot, "scripts", "run-bash-hook.ps1")
	scriptPath := filepath.Join(pluginRoot, "scripts", "session-start.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("create plugin scripts: %v", err)
	}
	if err := os.WriteFile(dispatcherPath, source, 0o644); err != nil {
		t.Fatalf("copy dispatcher: %v", err)
	}
	const input = `{"message":"stdin survives EOF"}`
	const wantStdout = "stdout: snowman ☃\n"
	const wantStderr = "stderr: café ☕\n"
	if err := os.WriteFile(scriptPath, []byte("#!/usr/bin/env bash\n[ \"$(cat)\" = '"+input+"' ] || exit 41\npwd\nprintf '"+wantStdout+"'\nprintf '"+wantStderr+"' >&2\nsleep 1\nexit 23\n"), 0o755); err != nil {
		t.Fatalf("write hook fixture: %v", err)
	}
	pwd := exec.Command(bashPath, "--noprofile", "--norc", "-c", "pwd")
	var wantWorkingDir bytes.Buffer
	pwd.Stdout = &wantWorkingDir
	if err := pwd.Run(); err != nil {
		t.Fatalf("resolve expected Bash working directory: %v", err)
	}

	command := codexBashHookWindowsCommand(t, root, `"${PLUGIN_ROOT}/scripts/session-start.sh"`)
	t.Setenv("PLUGIN_ROOT", pluginRoot)
	stdout, stderr, code := runCodexWindowsManifestCommand(t, command, input, "")
	// The fixture sleeps before exiting, so its exit code proves the dispatcher waited for Bash.
	if code != 23 {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want child exit 23", code, stdout, stderr)
	}
	if stdout != wantWorkingDir.String()+wantStdout || stderr != wantStderr {
		t.Fatalf("stdout=%q stderr=%q, want current directory and Unicode hook output preserved", stdout, stderr)
	}

	t.Run("missing root fails open", func(t *testing.T) {
		t.Setenv("PLUGIN_ROOT", "")
		out, errOut, status := runCodexWindowsManifestCommand(t, command, input, "")
		if status != 0 || out != "" || errOut != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q", status, out, errOut)
		}
	})

	t.Run("rejects unapproved hook names without output", func(t *testing.T) {
		stdout, stderr, code := runCodexWindowsPowerShellWithEnv(t, dispatcherPath, filepath.Join(pluginRoot, "scripts", "other.sh"), input, nil)
		if code != 0 || len(stdout) != 0 || len(stderr) != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want silent fail-open", code, stdout, stderr)
		}
	})

	t.Run("fails open when Git Bash cannot be discovered", func(t *testing.T) {
		stdout, stderr, code := runCodexWindowsPowerShellWithEnv(t, dispatcherPath, scriptPath, input, []string{"PATH=" + t.TempDir()})
		if code != 0 || len(stdout) != 0 || len(stderr) != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want silent fail-open", code, stdout, stderr)
		}
	})
}

func TestCodexWindowsBashHookDispatcherRealSessionContext(t *testing.T) {
	if runtime.GOOS != "windows" || testing.Short() {
		t.Skip("requires Windows external commands")
	}
	bashPath, ok := codexGitForWindowsBash()
	if !ok {
		t.Skip("Git for Windows Bash unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, bashPath, "--noprofile", "--norc", "-c", "command -v jq >/dev/null && command -v curl >/dev/null")
	if err := probe.Run(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("Git Bash prerequisite check timed out: %v", ctx.Err())
		}
		t.Skipf("jq or curl unavailable in selected Git Bash: %v", err)
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/project/current":
			_, _ = w.Write([]byte(`{"project":"fixture-project","project_source":"dir_basename"}`))
		case "/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"fixture-session","status":"created"}`))
		case "/context":
			_, _ = w.Write([]byte(`{"context":"DISTINCTIVE SESSION CONTEXT FROM FIXTURE"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixture.Close()
	root := repoRoot(t)
	pluginRoot := filepath.Join(t.TempDir(), "plugin root with spaces")
	if err := os.MkdirAll(filepath.Join(pluginRoot, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run-bash-hook.ps1", "session-start.sh", "_helpers.sh"} {
		data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginRoot, "scripts", name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PLUGIN_ROOT", pluginRoot)
	t.Setenv("ENGRAM_URL", fixture.URL)
	command := codexBashHookWindowsCommand(t, root, `"${PLUGIN_ROOT}/scripts/session-start.sh"`)
	stdout, stderr, code := runCodexWindowsManifestCommand(t, command, `{"session_id":"fixture-session","cwd":"C:/fixture-project"}`, "")
	if code != 0 || !strings.Contains(stdout, "DISTINCTIVE SESSION CONTEXT FROM FIXTURE") || !strings.Contains(stdout, "Registered runtime session") {
		t.Fatalf("exit=%d contextPresent=%t identityPresent=%t stderr=%q", code, strings.Contains(stdout, "DISTINCTIVE SESSION CONTEXT FROM FIXTURE"), strings.Contains(stdout, "Registered runtime session"), stderr)
	}
}

func TestCodexWindowsBashHookDispatcherSurvivesConsoleEncodingMutation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires native Windows cmd.exe and PowerShell")
	}
	if testing.Short() {
		t.Skip("executes the console-encoding regression through Git for Windows Bash")
	}
	if _, ok := codexGitForWindowsBash(); !ok {
		t.Skip("Git for Windows Bash unavailable: git.exe does not resolve to an installation with bin/bash.exe or usr/bin/bash.exe")
	}

	originalInputCodePage, originalOutputCodePage := codexWindowsConsoleCodePages(t)
	t.Cleanup(func() { codexSetWindowsConsoleCodePages(t, originalInputCodePage, originalOutputCodePage) })

	root := repoRoot(t)
	subagentPath := filepath.Join(root, "plugin", "codex", "scripts", "subagent-stop.ps1")
	subagentStdout, subagentStderr, code := runCodexWindowsPowerShellFile(t, subagentPath, "", nil)
	if code != 0 || (string(subagentStdout) != "{}\n" && string(subagentStdout) != "{}\r\n") || len(subagentStderr) != 0 {
		t.Fatalf("contaminate console encoding: exit=%d stdout=%q stderr=%q", code, subagentStdout, subagentStderr)
	}
	inputCodePage, _ := codexWindowsConsoleCodePages(t)
	if inputCodePage != 65001 {
		t.Fatalf("subagent-stop input console code page = %d, want 65001", inputCodePage)
	}

	source, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "scripts", "run-bash-hook.ps1"))
	if err != nil {
		t.Fatalf("read dispatcher: %v", err)
	}
	pluginRoot := filepath.Join(t.TempDir(), "plugin root with spaces")
	dispatcherPath := filepath.Join(pluginRoot, "scripts", "run-bash-hook.ps1")
	scriptPath := filepath.Join(pluginRoot, "scripts", "session-start.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("create plugin scripts: %v", err)
	}
	if err := os.WriteFile(dispatcherPath, source, 0o644); err != nil {
		t.Fatalf("copy dispatcher: %v", err)
	}
	const input = `{"message":"snowman ☃ survives EOF"}`
	const wantStdout = "stdout: snowman ☃\n"
	const wantStderr = "stderr: café ☕\n"
	fixture := "#!/usr/bin/env bash\n[ \"$(cat)\" = '" + input + "' ] || exit 41\nprintf '" + wantStdout + "'\nprintf '" + wantStderr + "' >&2\nexit 23\n"
	if err := os.WriteFile(scriptPath, []byte(fixture), 0o755); err != nil {
		t.Fatalf("write hook fixture: %v", err)
	}

	command := codexBashHookWindowsCommand(t, root, `"${PLUGIN_ROOT}/scripts/session-start.sh"`)
	t.Setenv("PLUGIN_ROOT", pluginRoot)
	dispatcherStdout, dispatcherStderr, code := runCodexWindowsManifestCommand(t, command, input, "")
	if code != 23 || dispatcherStdout != wantStdout || dispatcherStderr != wantStderr {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want exact stdin EOF and Unicode fidelity", code, dispatcherStdout, dispatcherStderr)
	}
}

func TestCodexWindowsConsoleCodePagesRestoreIndependently(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires native Windows cmd.exe and PowerShell")
	}
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skip("requires PowerShell")
	}

	originalInput, originalOutput := codexWindowsConsoleCodePages(t)
	t.Cleanup(func() {
		codexSetWindowsConsoleCodePages(t, originalInput, originalOutput)
		input, output := codexWindowsConsoleCodePages(t)
		if input != originalInput || output != originalOutput {
			t.Errorf("restored console code pages = input %d, output %d, want input %d, output %d", input, output, originalInput, originalOutput)
		}
	})

	const inputCodePage = 65001
	const outputCodePage = 437
	codexSetWindowsConsoleCodePages(t, inputCodePage, outputCodePage)
	input, output := codexWindowsConsoleCodePages(t)
	if input != inputCodePage || output != outputCodePage {
		t.Fatalf("console code pages = input %d, output %d, want input %d, output %d", input, output, inputCodePage, outputCodePage)
	}
}

// TestCodexWindowsBashHookDispatcherBackground exercises the actual CLI launcher
// through the manifest dispatcher. Set ENGRAM_TEST_ORIGINAL_LAUNCH=1 to substitute
// the pre-fix Bash launch in an isolated copy and observe the EOF assertion RED.
func TestCodexWindowsBashHookDispatcherBackground(t *testing.T) {
	if runtime.GOOS != "windows" || testing.Short() {
		t.Skip("requires native Windows and Git Bash")
	}
	bash, ok := codexGitForWindowsBash()
	if !ok {
		t.Skip("Git for Windows Bash unavailable")
	}
	probe := exec.Command(bash, "--noprofile", "--norc", "-c", "command -v jq && command -v curl")
	if err := probe.Run(); err != nil {
		t.Skipf("jq/curl unavailable: %v", err)
	}
	root := repoRoot(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "engram.exe")
	build := exec.Command("go", "build", "-o", binary, "./cmd/engram")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build isolated CLI: %v: %s", err, output)
	}
	pluginRoot := filepath.Join(dir, "plugin root with spaces")
	scripts := filepath.Join(pluginRoot, "scripts")
	if err := os.MkdirAll(scripts, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run-bash-hook.ps1", "session-start.sh", "_helpers.sh"} {
		data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "session-start.sh" && os.Getenv("ENGRAM_TEST_ORIGINAL_LAUNCH") == "1" {
			before := []byte("ENGRAM_CLOUD_AUTOSYNC=1 engram serve-background \"$ENGRAM_SERVE_ERR_LOG\" || true")
			if !bytes.Contains(data, before) {
				t.Fatal("cannot reconstruct original Bash launch")
			}
			data = bytes.Replace(data, before, []byte("ENGRAM_CLOUD_AUTOSYNC=1 engram serve > /dev/null 2>> \"$ENGRAM_SERVE_ERR_LOG\" &"), 1)
		}
		if err := os.WriteFile(filepath.Join(scripts, name), data, 0755); err != nil {
			t.Fatal(err)
		}
	}
	command := codexBashHookWindowsCommand(t, root, `"${PLUGIN_ROOT}/scripts/session-start.sh"`)
	// External ownership is the no-child control: it must not launch a local server.
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/project/current":
			_, _ = w.Write([]byte(`{"project":"fixture","project_source":"dir_basename"}`))
		case "/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"fixture-session","status":"created"}`))
		case "/context":
			_, _ = w.Write([]byte(`{"context":"EXTERNAL CONTEXT"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer external.Close()
	baseEnv := append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "PLUGIN_ROOT="+pluginRoot, "ENGRAM_DATA_DIR="+filepath.Join(dir, "data"))
	input := `{"session_id":"fixture-session","cwd":"C:/fixture"}`
	type hookResult struct {
		stdout, stderr              string
		code                        int
		exitAfter, eofAfter, eofLag time.Duration
	}
	run := func(env []string) hookResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "cmd.exe", "/D", "/S", "/C", command)
		cmd.WaitDelay = time.Second
		cmd.Env = env
		cmd.Stdin = strings.NewReader(input)
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := reader.Close(); err != nil {
				t.Errorf("close hook stdout reader: %v", err)
			}
		}()
		cmd.Stdout = writer
		stderrFile, err := os.CreateTemp(dir, "hook-stderr-*.log")
		if err != nil {
			_ = writer.Close()
			t.Fatal(err)
		}
		defer func() {
			if err := stderrFile.Close(); err != nil {
				t.Errorf("close hook stderr file: %v", err)
			}
		}()
		cmd.Stderr = stderrFile
		start := time.Now()
		if err := cmd.Start(); err != nil {
			_ = writer.Close()
			t.Fatal(err)
		}
		_ = writer.Close()
		type readResult struct {
			output []byte
			err    error
			at     time.Time
		}
		type waitResult struct {
			err error
			at  time.Time
		}
		readDone := make(chan readResult, 1)
		waitDone := make(chan waitResult, 1)
		go func() {
			data, err := io.ReadAll(reader)
			readDone <- readResult{data, err, time.Now()}
		}()
		go func() {
			err := cmd.Wait()
			waitDone <- waitResult{err, time.Now()}
		}()
		var read readResult
		var wait waitResult
		for read.at.IsZero() || wait.at.IsZero() {
			select {
			case read = <-readDone:
			case wait = <-waitDone:
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				t.Fatalf("hook exit or consumer EOF exceeded 12 seconds (exit=%t EOF=%t): %v", !wait.at.IsZero(), !read.at.IsZero(), ctx.Err())
			}
		}
		if read.err != nil {
			t.Fatal(read.err)
		}
		stderr, err := os.ReadFile(stderrFile.Name())
		if err != nil {
			t.Fatal(err)
		}
		code := 0
		if wait.err != nil {
			var exit *exec.ExitError
			if !errors.As(wait.err, &exit) {
				t.Fatal(wait.err)
			}
			code = exit.ExitCode()
		}
		lag := read.at.Sub(wait.at)
		if lag < 0 {
			lag = 0
		}
		return hookResult{string(read.output), string(stderr), code, wait.at.Sub(start), read.at.Sub(start), lag}
	}
	externalResult := run(append(append([]string{}, baseEnv...), "ENGRAM_URL="+external.URL))
	t.Logf("external exit=%s EOF=%s lag=%s", externalResult.exitAfter, externalResult.eofAfter, externalResult.eofLag)
	if externalResult.code != 0 || externalResult.eofAfter >= 5*time.Second || externalResult.eofLag >= time.Second || !strings.Contains(externalResult.stdout, "EXTERNAL CONTEXT") || !strings.Contains(externalResult.stdout, `{"session_id":"fixture-session"}`) || externalResult.stderr != "" {
		t.Fatalf("external ownership: exit=%d processExit=%s EOF=%s lag=%s context=%t stderr=%q", externalResult.code, externalResult.exitAfter, externalResult.eofAfter, externalResult.eofLag, strings.Contains(externalResult.stdout, "EXTERNAL CONTEXT"), externalResult.stderr)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	// Resolve the listener PID only after verifying its executable is our
	// temporary CLI; a released ephemeral port could be reused by another app.
	t.Cleanup(func() {
		output, err := exec.Command("netstat", "-ano", "-p", "TCP").Output()
		if err != nil {
			t.Errorf("find test server for cleanup: %v", err)
			return
		}
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 5 && fields[1] == fmt.Sprintf("127.0.0.1:%d", port) && fields[3] == "LISTENING" {
				pid, err := strconv.Atoi(fields[4])
				if err != nil {
					t.Errorf("parse listener PID: %v", err)
					continue
				}
				pathOutput, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", fmt.Sprintf("(Get-Process -Id %d -ErrorAction SilentlyContinue).Path", pid)).Output()
				if err != nil {
					t.Errorf("identify listener executable: %v", err)
					continue
				}
				if !strings.EqualFold(filepath.Clean(strings.TrimSpace(string(pathOutput))), binary) {
					t.Errorf("refusing cleanup of unrelated listener on port %d: %q", port, pathOutput)
					continue
				}
				if process, err := os.FindProcess(pid); err == nil {
					if err := process.Kill(); err != nil {
						t.Errorf("stop test server: %v", err)
					}
				}
			}
		}
	})
	cold := run(append(append([]string{}, baseEnv...), "ENGRAM_URL=", fmt.Sprintf("ENGRAM_PORT=%d", port)))
	t.Logf("cold start exit=%s EOF=%s lag=%s", cold.exitAfter, cold.eofAfter, cold.eofLag)
	if cold.code != 0 || cold.eofAfter >= 10*time.Second || cold.eofLag >= time.Second || !strings.Contains(cold.stdout, "RUNTIME SESSION IDENTITY") || cold.stderr != "" {
		t.Errorf("cold start: exit=%d processExit=%s EOF=%s lag=%s identity=%t stderr=%q", cold.code, cold.exitAfter, cold.eofAfter, cold.eofLag, strings.Contains(cold.stdout, "RUNTIME SESSION IDENTITY"), cold.stderr)
	}
	time.Sleep(4100 * time.Millisecond)
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		t.Fatalf("server died before four seconds: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("server health after four seconds: %d", response.StatusCode)
	}
	logData, err := os.ReadFile(filepath.Join(dir, "data", "serve.err.log"))
	if err != nil {
		t.Fatalf("server stderr log missing: %v", err)
	}
	_ = logData // The server may have no stderr on a healthy start.
}

func codexWindowsConsoleCodePages(t *testing.T) (int, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[Console]::InputEncoding.CodePage; [Console]::OutputEncoding.CodePage")
	output, err := run.Output()
	if err != nil {
		t.Fatalf("read console code pages: %v", err)
	}
	codePages := strings.Fields(string(output))
	if len(codePages) != 2 {
		t.Fatalf("parse console code pages %q: want input and output values", output)
	}
	inputCodePage, err := strconv.Atoi(codePages[0])
	if err != nil {
		t.Fatalf("parse input console code page %q: %v", codePages[0], err)
	}
	outputCodePage, err := strconv.Atoi(codePages[1])
	if err != nil {
		t.Fatalf("parse output console code page %q: %v", codePages[1], err)
	}
	return inputCodePage, outputCodePage
}

func codexSetWindowsConsoleCodePages(t *testing.T, inputCodePage, outputCodePage int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	command := "[Console]::InputEncoding = [System.Text.Encoding]::GetEncoding(" + strconv.Itoa(inputCodePage) + "); [Console]::OutputEncoding = [System.Text.Encoding]::GetEncoding(" + strconv.Itoa(outputCodePage) + ")"
	if output, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput(); err != nil {
		t.Errorf("set console code pages input %d output %d: %v: %s", inputCodePage, outputCodePage, err, output)
	}
}

func codexBashHookWindowsCommand(t *testing.T, root, command string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "plugin", "codex", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks manifest: %v", err)
	}
	var manifest codexHooksManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse hooks manifest: %v", err)
	}
	for _, groups := range manifest.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if hook.Type == "command" && hook.Command == command {
					return hook.CommandWindows
				}
			}
		}
	}
	t.Fatalf("hook command %q does not declare commandWindows", command)
	return ""
}

func codexGitForWindowsBash() (string, bool) {
	gitPath, err := exec.LookPath("git.exe")
	if err != nil {
		return "", false
	}
	directory := filepath.Dir(gitPath)
	for i := 0; i < 4; i++ {
		for _, relative := range []string{filepath.Join("bin", "bash.exe"), filepath.Join("usr", "bin", "bash.exe")} {
			candidate := filepath.Join(directory, relative)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, true
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	return "", false
}

func runCodexWindowsPowerShellWithEnv(t *testing.T, dispatcherPath, hookPath, input string, envOverrides []string) ([]byte, []byte, int) {
	t.Helper()
	return runCodexWindowsPowerShellFile(t, dispatcherPath, input, envOverrides, hookPath)
}

func runCodexWindowsPowerShellFile(t *testing.T, scriptPath, input string, envOverrides []string, arguments ...string) ([]byte, []byte, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	args := append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath}, arguments...)
	run := exec.CommandContext(ctx, "powershell.exe", args...)
	run.Env = append([]string{}, os.Environ()...)
	for _, override := range envOverrides {
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
	err := run.Run()
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode()
	}
	t.Fatalf("run dispatcher: %v", err)
	return nil, nil, -1
}
