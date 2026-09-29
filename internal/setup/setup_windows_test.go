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

func TestCodexConfigPathUsesActiveWindowsHome(t *testing.T) {
	resetSetupSeams(t)
	profile := useIsolatedProfile(t)
	custom := filepath.Join(t.TempDir(), "custom codex home")
	for _, tt := range []struct {
		name, codexHome, want string
	}{
		{"default despite APPDATA", "", filepath.Join(profile, ".codex", "config.toml")},
		{"explicit absolute CODEX_HOME", custom, filepath.Join(custom, "config.toml")},
		{"relative CODEX_HOME", "relative-codex-home", filepath.Join(profile, ".codex", "config.toml")},
		{"drive-relative CODEX_HOME", `C:relative-home`, filepath.Join(profile, ".codex", "config.toml")},
		{"root-relative CODEX_HOME", `\relative-home`, filepath.Join(profile, ".codex", "config.toml")},
		{"whitespace CODEX_HOME", "   ", filepath.Join(profile, ".codex", "config.toml")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CODEX_HOME", tt.codexHome)
			if got := codexConfigPath(); got != tt.want {
				t.Fatalf("codexConfigPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInstallCodexWritesActiveWindowsConfig(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "default"
		if explicit {
			name = "CODEX_HOME"
		}
		t.Run(name, func(t *testing.T) {
			resetSetupSeams(t)
			profile := useIsolatedProfile(t)
			wantDir := filepath.Join(profile, ".codex")
			if explicit {
				wantDir = filepath.Join(t.TempDir(), "custom codex home")
				t.Setenv("CODEX_HOME", wantDir)
			}
			exe := filepath.Join(t.TempDir(), "engram.exe")
			osExecutable = func() (string, error) { return exe, nil }
			lookPathFn = func(string) (string, error) { return "", errors.New("not found") }
			if _, err := Install("codex"); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(wantDir, "config.toml")
			data, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(data), windowsHookCommandMarkerPrefix+strconv.Quote(exe)) {
				t.Fatalf("setup config at %q lacks native hook pin: %s", config, data)
			}
			if _, err := os.Stat(filepath.Join(profile, "AppData", "Roaming", "codex", "config.toml")); !os.IsNotExist(err) {
				t.Fatalf("setup wrote APPDATA Codex config: %v", err)
			}
		})
	}
}

func TestInstallCodexRejectsMissingUserHomeBeforeWrites(t *testing.T) {
	for _, tt := range []struct {
		name, codexHome, home string
		err                   error
	}{
		{"home lookup fails without CODEX_HOME", "", "", errors.New("home unavailable")},
		{"home empty with relative CODEX_HOME", "relative-home", "", nil},
		{"relative home without CODEX_HOME", "", "relative-home", nil},
		{"drive-relative home without CODEX_HOME", "", `C:relative-home`, nil},
		{"root-relative home without CODEX_HOME", "", `\relative-home`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resetSetupSeams(t)
			useIsolatedProfile(t)
			t.Setenv("CODEX_HOME", tt.codexHome)
			userHomeDir = func() (string, error) { return tt.home, tt.err }
			if got := codexConfigPath(); got != "" {
				t.Fatalf("codexConfigPath() = %q, want no writable path", got)
			}
			osExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "engram.exe"), nil }
			writeCodexMemoryInstructionFilesFn = func() (string, error) {
				t.Fatal("setup wrote instructions without an absolute config path")
				return "", nil
			}
			if result, err := Install("codex"); err == nil || result != nil {
				t.Fatalf("Install(codex) = %#v, %v; want fail-closed path error", result, err)
			}
		})
	}
}
