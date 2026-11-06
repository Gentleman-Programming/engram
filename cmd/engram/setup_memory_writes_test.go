package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/setup"
)

func TestSetupMemoryWritesRealSettings(t *testing.T) {
	stubRuntimeHooks(t)
	cfg := testConfig(t)
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	setupInstallAgent = func(string) (*setup.Result, error) {
		return &setup.Result{Agent: "claude-code", MCPConfigured: true}, nil
	}
	scanInputLine = func(...any) (int, error) { t.Fatal("explicit opt-in read stdin"); return 0, nil }
	withArgs(t, "engram", "setup", "claude-code", "--allow-memory-writes", "--protocol=full")
	_, stderr := captureOutput(t, func() { cmdSetup(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%s", stderr)
	}
	raw, err := os.ReadFile(filepath.Join(root, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct{ Permissions struct{ Allow []string } }
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	if len(settings.Permissions.Allow) != 24 {
		t.Fatalf("unexpected grant %s", raw)
	}
	for _, entry := range settings.Permissions.Allow {
		if strings.Contains(entry, "*") || strings.HasSuffix(entry, "mem_delete") || strings.HasSuffix(entry, "mem_merge_projects") {
			t.Fatalf("overbroad grant %s", entry)
		}
	}
}

func TestSetupMemoryWritesDefaultEOFAndHelp(t *testing.T) {
	for _, help := range []bool{false, true} {
		t.Run(map[bool]string{false: "default EOF", true: "help"}[help], func(t *testing.T) {
			stubRuntimeHooks(t)
			cfg := testConfig(t)
			old := setupAddClaudeCodeMemoryWriteAllowlist
			t.Cleanup(func() { setupAddClaudeCodeMemoryWriteAllowlist = old })
			setupAddClaudeCodeMemoryWriteAllowlist = func() error { t.Fatal("unconsented grant"); return nil }
			setupAddClaudeCodeAllowlist = func() error { t.Fatal("EOF granted broader tools"); return nil }
			setupInstallAgent = func(string) (*setup.Result, error) {
				if help {
					t.Fatal("help installed plugin")
				}
				return &setup.Result{Agent: "claude-code", MCPConfigured: true}, nil
			}
			reads := 0
			scanInputLine = func(...any) (int, error) {
				if help {
					t.Fatal("help read stdin")
				}
				reads++
				return 0, io.EOF
			}
			args := []string{"engram", "setup", "claude-code"}
			if help {
				args = append(args, "--allow-memory-writes", "--help")
			}
			withArgs(t, args...)
			out, stderr := captureOutput(t, func() { cmdSetup(cfg) })
			if stderr != "" {
				t.Fatal(stderr)
			}
			if help {
				if !strings.Contains(out, "--allow-memory-writes") || reads != 0 {
					t.Fatalf("help output=%s reads=%d", out, reads)
				}
			} else if !strings.Contains(out, "Skipped") || reads != 1 {
				t.Fatalf("EOF output=%s reads=%d", out, reads)
			}
		})
	}
}

func TestSetupMemoryWritesOptIn(t *testing.T) {
	for _, args := range [][]string{{"claude-code", "--allow-memory-writes"}, {"--allow-memory-writes", "claude-code"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stubRuntimeHooks(t)
			cfg := testConfig(t)
			old := setupAddClaudeCodeMemoryWriteAllowlist
			t.Cleanup(func() { setupAddClaudeCodeMemoryWriteAllowlist = old })
			installs, grants := 0, 0
			setupInstallAgent = func(slug string) (*setup.Result, error) {
				installs++
				return &setup.Result{Agent: slug, MCPConfigured: true}, nil
			}
			setupAddClaudeCodeMemoryWriteAllowlist = func() error { grants++; return nil }
			setupAddClaudeCodeAllowlist = func() error { t.Fatal("broader grant requested"); return nil }
			scanInputLine = func(...any) (int, error) { t.Fatal("opt-in read stdin"); return 0, nil }
			withArgs(t, append([]string{"engram", "setup"}, args...)...)
			out, errOut := captureOutput(t, func() { cmdSetup(cfg) })
			if installs != 1 || grants != 1 || errOut != "" || !strings.Contains(out, "memory-write permissions") {
				t.Fatalf("installs=%d grants=%d stdout=%s stderr=%s", installs, grants, out, errOut)
			}
		})
	}
}

func TestSetupMemoryWritesInvalidArgumentsHaveNoEffects(t *testing.T) {
	for _, args := range [][]string{{"--allow-memory-writes"}, {"codex", "--allow-memory-writes"}, {"claude-code", "--mcp-only", "--allow-memory-writes"}, {"claude-code", "extra", "--allow-memory-writes"}, {"claude-code", "--unknown", "--allow-memory-writes"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stubRuntimeHooks(t)
			stubExitWithPanic(t)
			cfg := testConfig(t)
			setupInstallAgent = func(string) (*setup.Result, error) { t.Fatal("invalid args installed plugin"); return nil, nil }
			scanInputLine = func(...any) (int, error) { t.Fatal("invalid args read stdin"); return 0, nil }
			withArgs(t, append([]string{"engram", "setup"}, args...)...)
			_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
			if _, ok := recovered.(exitCode); !ok || !strings.Contains(stderr, "--allow-memory-writes") {
				t.Fatalf("panic=%v stderr=%s", recovered, stderr)
			}
		})
	}
}

func TestSetupMemoryWritesFailuresDoNotReportSuccess(t *testing.T) {
	for _, stage := range []string{"install", "mcp", "settings"} {
		t.Run(stage, func(t *testing.T) {
			stubRuntimeHooks(t)
			stubExitWithPanic(t)
			cfg := testConfig(t)
			old := setupAddClaudeCodeMemoryWriteAllowlist
			t.Cleanup(func() { setupAddClaudeCodeMemoryWriteAllowlist = old })
			setupInstallAgent = func(string) (*setup.Result, error) {
				if stage == "install" {
					return nil, errors.New("install failed")
				}
				return &setup.Result{Agent: "claude-code", MCPConfigured: stage != "mcp"}, nil
			}
			calls := 0
			setupAddClaudeCodeMemoryWriteAllowlist = func() error { calls++; return errors.New("settings failed") }
			scanInputLine = func(...any) (int, error) { t.Fatal("failure read stdin"); return 0, nil }
			withArgs(t, "engram", "setup", "claude-code", "--allow-memory-writes")
			out, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
			want := 0
			if stage == "settings" {
				want = 1
			}
			if _, ok := recovered.(exitCode); !ok || calls != want || stderr == "" || strings.Contains(out, "memory-write permissions added") {
				t.Fatalf("calls=%d panic=%v stdout=%s stderr=%s", calls, recovered, out, stderr)
			}
		})
	}
}
