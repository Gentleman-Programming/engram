//go:build windows

package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestClaudeCodeEngramCommandPreservesWindowsAbsolutePath covers the
// Windows-specific branch of claudeCodeEngramCommand that cannot be exercised
// truthfully on macOS/Linux: filepath.IsAbs rejects drive-letter paths there,
// so the same input would route to the error path instead of the preserve
// path. On Windows, a non-Cellar absolute path (no "/Cellar/engram/" marker)
// is returned unchanged.
//
// t.TempDir() guarantees a real absolute Windows path (drive-letter rooted,
// so filepath.IsAbs is true) while the joined "engram.exe" leaf is never
// created, so filepath.EvalSymlinks errors on the missing leaf and leaves the
// path unchanged inside canonicalEngramCommand; stableHomebrewEngramCommand
// then returns ("", false) early since the TempDir path has no
// "/Cellar/engram/" marker. This guards the durable Claude Code user MCP
// config (writeClaudeCodeUserMCP), which must never persist a PATH-dependent
// command on Windows.
func TestClaudeCodeEngramCommandPreservesWindowsAbsolutePath(t *testing.T) {
	resetSetupSeams(t)

	exe := filepath.Join(t.TempDir(), "engram.exe")
	got, err := claudeCodeEngramCommand(exe)
	if err != nil {
		t.Fatalf("claudeCodeEngramCommand(%q) returned error: %v; want nil", exe, err)
	}
	if got != exe {
		t.Fatalf("claudeCodeEngramCommand(%q) = %q; want %q (preserved absolute path)", exe, got, exe)
	}
}

func TestInstallCodexPinsAndRefreshesWindowsExecutable(t *testing.T) {
	resetSetupSeams(t)
	useIsolatedProfile(t)
	lookPathFn = func(string) (string, error) { return "", errors.New("not found") }

	first := filepath.Join(t.TempDir(), "renamed-first.exe")
	second := filepath.Join(t.TempDir(), "renamed-second.exe")
	for _, exe := range []string{first, second} {
		if !filepath.IsAbs(exe) || !strings.EqualFold(filepath.Ext(exe), ".exe") {
			t.Fatalf("expected rooted Windows executable path, got %q", exe)
		}
	}

	osExecutable = func() (string, error) { return first, nil }
	if _, err := Install("codex"); err != nil {
		t.Fatalf("initial Codex setup: %v", err)
	}
	configPath := codexConfigPath()
	firstConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read initial Codex config: %v", err)
	}
	if !strings.Contains(string(firstConfig), "command = "+strconv.Quote(first)) {
		t.Fatalf("Codex config does not pin first rooted executable:\n%s", firstConfig)
	}

	osExecutable = func() (string, error) { return second, nil }
	if _, err := Install("codex"); err != nil {
		t.Fatalf("repeat Codex setup after executable move: %v", err)
	}
	secondConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read refreshed Codex config: %v", err)
	}
	if strings.Contains(string(secondConfig), strconv.Quote(first)) || !strings.Contains(string(secondConfig), "command = "+strconv.Quote(second)) {
		t.Fatalf("Codex config did not replace the executable pin:\n%s", secondConfig)
	}
	if strings.Count(string(secondConfig), "[mcp_servers.engram]") != 1 {
		t.Fatalf("expected one Codex MCP block after rerun:\n%s", secondConfig)
	}
}
