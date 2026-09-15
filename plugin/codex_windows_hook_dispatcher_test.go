package plugin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
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

	const dispatcher = `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "${PLUGIN_ROOT}\scripts\run-bash-hook.ps1"`
	wantMappings := map[string]string{
		`"${PLUGIN_ROOT}/scripts/session-start.sh"`:      dispatcher + ` "${PLUGIN_ROOT}\scripts\session-start.sh"`,
		`"${PLUGIN_ROOT}/scripts/post-compaction.sh"`:    dispatcher + ` "${PLUGIN_ROOT}\scripts\post-compaction.sh"`,
		`"${PLUGIN_ROOT}/scripts/user-prompt-submit.sh"`: dispatcher + ` "${PLUGIN_ROOT}\scripts\user-prompt-submit.sh"`,
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
					if hook.CommandWindows != want {
						t.Errorf("%s hook commandWindows = %q, want %q", event, hook.CommandWindows, want)
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

	command := strings.ReplaceAll(codexBashHookWindowsCommand(t, root, `"${PLUGIN_ROOT}/scripts/session-start.sh"`), "${PLUGIN_ROOT}", pluginRoot)
	stdout, stderr, code := runCodexWindowsManifestCommand(t, command, input, "")
	// The fixture sleeps before exiting, so its exit code proves the dispatcher waited for Bash.
	if code != 23 {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want child exit 23", code, stdout, stderr)
	}
	if stdout != wantWorkingDir.String()+wantStdout || stderr != wantStderr {
		t.Fatalf("stdout=%q stderr=%q, want current directory and Unicode hook output preserved", stdout, stderr)
	}

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

	command := strings.ReplaceAll(codexBashHookWindowsCommand(t, root, `"${PLUGIN_ROOT}/scripts/session-start.sh"`), "${PLUGIN_ROOT}", pluginRoot)
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
