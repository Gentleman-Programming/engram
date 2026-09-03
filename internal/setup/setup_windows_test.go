//go:build windows

package setup

import (
	"path/filepath"
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
