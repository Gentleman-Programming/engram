package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/setup"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// Token-classification coverage: one test per row.

func TestCmdSetupHelpAnyPositionShowsProtocolFlagAndSkipsStdin(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)

	scanInputLine = func(a ...any) (int, error) {
		t.Fatal("scanInputLine must not be called for --help (Guarantee 2: no stdin read)")
		return 0, nil
	}

	cases := [][]string{
		{"engram", "setup", "--help"},
		{"engram", "setup", "-h"},
		{"engram", "setup", "help"},
		{"engram", "setup", "myagent", "--help"},
	}
	for _, args := range cases {
		withArgs(t, args...)
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("args=%v: setup --help should exit cleanly, panic=%v stderr=%q", args, recovered, stderr)
		}
		if !strings.Contains(stdout, "--protocol") {
			t.Fatalf("args=%v: usage output missing literal --protocol: %q", args, stdout)
		}
		if !strings.Contains(stdout, "plugin >= 0.1.1") {
			t.Fatalf("args=%v: usage output missing Claude Code plugin floor: %q", args, stdout)
		}
	}
}

func TestCmdSetupMCPOnly(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)

	oldEnsure := setupEnsureClaudeCodeUserMCP
	t.Cleanup(func() { setupEnsureClaudeCodeUserMCP = oldEnsure })

	t.Run("success", func(t *testing.T) {
		called := false
		setupEnsureClaudeCodeUserMCP = func() error {
			called = true
			return nil
		}

		withArgs(t, "engram", "setup", "claude-code", "--mcp-only")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
		if recovered != nil || stdout != "" || stderr != "" {
			t.Fatalf("success panic=%v stdout=%q stderr=%q", recovered, stdout, stderr)
		}
		if !called {
			t.Fatal("expected MCP-only setup to ensure the Claude Code MCP config")
		}
	})

	t.Run("invalid slug", func(t *testing.T) {
		setupEnsureClaudeCodeUserMCP = func() error {
			t.Fatal("MCP-only setup must reject an invalid slug before ensuring")
			return nil
		}

		withArgs(t, "engram", "setup", "codex", "--mcp-only")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
		if _, ok := recovered.(exitCode); !ok || !strings.Contains(stderr, "--mcp-only requires claude-code") {
			t.Fatalf("invalid slug panic=%v stderr=%q", recovered, stderr)
		}
	})

	t.Run("ensure failure", func(t *testing.T) {
		setupEnsureClaudeCodeUserMCP = func() error { return errors.New("MCP config unavailable") }

		withArgs(t, "engram", "setup", "claude-code", "--mcp-only")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
		if _, ok := recovered.(exitCode); !ok || !strings.Contains(stderr, "MCP config unavailable") {
			t.Fatalf("ensure failure panic=%v stderr=%q", recovered, stderr)
		}
	})
}

func TestPrintPostInstallClaudeCodeReportsMCPStatus(t *testing.T) {
	oldScan := scanInputLine
	scanInputLine = func(...any) (int, error) { return 0, nil }
	t.Cleanup(func() { scanInputLine = oldScan })

	t.Run("configured", func(t *testing.T) {
		stdout, stderr := captureOutput(t, func() {
			printPostInstall(&setup.Result{Agent: "claude-code", MCPConfigured: true})
		})
		if stderr != "" || !strings.Contains(stdout, "MCP config written to ~/.claude/mcp/engram.json") {
			t.Fatalf("configured output stdout=%q stderr=%q", stdout, stderr)
		}
	})

	t.Run("not configured", func(t *testing.T) {
		stdout, stderr := captureOutput(t, func() {
			printPostInstall(&setup.Result{Agent: "claude-code"})
		})
		if stderr != "" || !strings.Contains(stdout, "MCP configuration was not written") || !strings.Contains(stdout, "Re-run 'engram setup claude-code'") {
			t.Fatalf("unconfigured output stdout=%q stderr=%q", stdout, stderr)
		}
		if strings.Contains(stdout, "MCP config written to ~/.claude/mcp/engram.json") {
			t.Fatalf("unconfigured output must not report a successful MCP config: %q", stdout)
		}
	})
}

func TestCmdSetupProtocolEqualsFormPersistsSlim(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	setProtocolVersion(t, "1.4.0")

	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 2}, nil
	}
	scanInputLine = func(...any) (int, error) { return 0, nil }

	withArgs(t, "engram", "setup", "myagent", "--protocol=slim")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}

	if got := setup.ReadProtocolMode(cfg.DataDir, "myagent"); got != setup.ProtocolModeSlim {
		t.Fatalf("ReadProtocolMode = %q, want slim", got)
	}
}

func TestCmdSetupClaudeCodeSlimWarningPersistsMode(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	setProtocolVersion(t, "1.4.0")

	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 2}, nil
	}
	scanInputLine = func(...any) (int, error) { return 0, nil }
	oldVerify := setupVerifyClaudeCodeSlim
	setupVerifyClaudeCodeSlim = func() error { return errors.New("claude plugin list --json timed out") }
	t.Cleanup(func() { setupVerifyClaudeCodeSlim = oldVerify })

	withArgs(t, "engram", "setup", "claude-code", "--protocol=slim")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil {
		t.Fatalf("slim capability warning must not fail setup, panic=%v", recovered)
	}
	if !strings.Contains(stdout, "Installed claude-code plugin") {
		t.Fatalf("setup should still succeed: %q", stdout)
	}
	if !strings.Contains(stderr, "requires plugin 0.1.1+") || !strings.Contains(stderr, "--plugin-dir") {
		t.Fatalf("expected actionable slim capability warning, got %q", stderr)
	}
	if strings.Contains(stderr, "slim will remain full") {
		t.Fatalf("plugin capability warning must not include a binary-version warning: %q", stderr)
	}
	if got := setup.ReadProtocolMode(cfg.DataDir, "claude-code"); got != setup.ProtocolModeSlim {
		t.Fatalf("ReadProtocolMode = %q, want slim despite warning", got)
	}
}

func TestCmdSetupOnlyChecksClaudeCodeSlim(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		selected   string
		wantMode   string
		wantChecks int
	}{
		{name: "Claude Code full", args: []string{"engram", "setup", "claude-code", "--protocol=full"}, wantMode: setup.ProtocolModeFull},
		{name: "Claude Code default", args: []string{"engram", "setup", "claude-code"}, wantMode: setup.ProtocolModeFull},
		{name: "other agent slim", args: []string{"engram", "setup", "opencode", "--protocol=slim"}, wantMode: setup.ProtocolModeSlim},
		{name: "interactive other agent slim", args: []string{"engram", "setup", "--protocol=slim"}, selected: "opencode", wantMode: setup.ProtocolModeSlim},
		{name: "interactive Claude Code slim", args: []string{"engram", "setup", "--protocol=slim"}, selected: "claude-code", wantMode: setup.ProtocolModeSlim, wantChecks: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubRuntimeHooks(t)
			stubExitWithPanic(t)
			cfg := testConfig(t)
			setProtocolVersion(t, "1.4.0")
			setupInstallAgent = func(agent string) (*setup.Result, error) {
				return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 2}, nil
			}
			scanInputLine = func(...any) (int, error) { return 0, nil }
			if tt.selected != "" {
				setupSupportedAgents = func() []setup.Agent {
					return []setup.Agent{{Name: tt.selected, Description: tt.selected, InstallDir: "/tmp/dest"}}
				}
				scanInputLine = func(a ...any) (int, error) {
					*a[0].(*string) = "1"
					return 1, nil
				}
			}
			checks := 0
			oldVerify := setupVerifyClaudeCodeSlim
			setupVerifyClaudeCodeSlim = func() error {
				checks++
				return nil
			}
			t.Cleanup(func() { setupVerifyClaudeCodeSlim = oldVerify })

			withArgs(t, tt.args...)
			_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
			if recovered != nil || stderr != "" {
				t.Fatalf("panic=%v stderr=%q", recovered, stderr)
			}
			if checks != tt.wantChecks {
				t.Fatalf("capability checks = %d, want %d", checks, tt.wantChecks)
			}
			slug := "claude-code"
			if tt.selected != "" {
				slug = tt.selected
			} else if strings.Contains(tt.args[2], "opencode") {
				slug = "opencode"
			}
			if got := setup.ReadProtocolMode(cfg.DataDir, slug); got != tt.wantMode {
				t.Fatalf("ReadProtocolMode(%q) = %q, want %q", slug, got, tt.wantMode)
			}
		})
	}
}

func TestCmdSetupProtocolSpaceFormPersistsSlim(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	setProtocolVersion(t, "1.4.0")

	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 2}, nil
	}

	withArgs(t, "engram", "setup", "myagent", "--protocol", "slim")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}

	if got := setup.ReadProtocolMode(cfg.DataDir, "myagent"); got != setup.ProtocolModeSlim {
		t.Fatalf("ReadProtocolMode = %q, want slim", got)
	}
}

func TestCmdSetupProtocolFlagFirstThenSlug(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	setProtocolVersion(t, "1.4.0")

	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 2}, nil
	}

	withArgs(t, "engram", "setup", "--protocol=slim", "myagent")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}

	if got := setup.ReadProtocolMode(cfg.DataDir, "myagent"); got != setup.ProtocolModeSlim {
		t.Fatalf("ReadProtocolMode = %q, want slim", got)
	}
}

// TestCmdSetupUnknownFlagFallbackForwardsParsedProtocolMode guards JD-014:
// an already-parsed --protocol value must not be dropped when combined with
// an unrecognized flag that triggers the interactive fallback.
func TestCmdSetupUnknownFlagFallbackForwardsParsedProtocolMode(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	setProtocolVersion(t, "1.4.0")

	setupSupportedAgents = func() []setup.Agent {
		return []setup.Agent{{Name: "opencode", Description: "OpenCode", InstallDir: "/tmp/opencode"}}
	}
	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/opencode", Files: 1}, nil
	}
	scanInputLine = func(a ...any) (int, error) {
		p := a[0].(*string)
		*p = "1"
		return 1, nil
	}

	withArgs(t, "engram", "setup", "--protocol=slim", "--bogus-flag")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Installing opencode plugin") {
		t.Fatalf("expected interactive install flow: %q", stdout)
	}
	if got := setup.ReadProtocolMode(cfg.DataDir, "opencode"); got != setup.ProtocolModeSlim {
		t.Fatalf("ReadProtocolMode(opencode) = %q, want slim (parsed --protocol must survive the unknown-flag fallback)", got)
	}
}

// TestCmdSetupUnknownFlagBeforeProtocolStillForwardsMode guards the JD-014
// residual: an unrecognized hyphen-prefixed token appearing BEFORE
// --protocol must not prevent the already-later --protocol from being
// parsed and forwarded to the interactive fallback (order independence).
func TestCmdSetupUnknownFlagBeforeProtocolStillForwardsMode(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	setProtocolVersion(t, "1.4.0")

	setupSupportedAgents = func() []setup.Agent {
		return []setup.Agent{{Name: "opencode", Description: "OpenCode", InstallDir: "/tmp/opencode"}}
	}
	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/opencode", Files: 1}, nil
	}
	scanInputLine = func(a ...any) (int, error) {
		p := a[0].(*string)
		*p = "1"
		return 1, nil
	}

	withArgs(t, "engram", "setup", "--bogus-flag", "--protocol=slim")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Installing opencode plugin") {
		t.Fatalf("expected interactive install flow: %q", stdout)
	}
	if got := setup.ReadProtocolMode(cfg.DataDir, "opencode"); got != setup.ProtocolModeSlim {
		t.Fatalf("ReadProtocolMode(opencode) = %q, want slim (--protocol after an unknown flag must not be dropped)", got)
	}
}

// TestCmdSetupProtocolSpaceFormDoesNotSwallowNextFlag guards JD-015: the
// space-form --protocol must not consume a following hyphen-prefixed token
// as its value — it should be treated as dangling (empty value) so the
// flag token is classified normally on the next iteration.
func TestCmdSetupProtocolSpaceFormDoesNotSwallowNextFlag(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)

	scanInputLine = func(a ...any) (int, error) {
		t.Fatal("scanInputLine must not be called when --help is reached")
		return 0, nil
	}

	withArgs(t, "engram", "setup", "myagent", "--protocol", "--help")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "usage:") {
		t.Fatalf("expected usage output, got %q", stdout)
	}
}

func TestCmdSetupSecondBareTokenIsUsageError(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)

	withArgs(t, "engram", "setup", "myagent", "extra-token")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected exit on second bare token, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stderr, "usage") {
		t.Fatalf("expected usage error on stderr, got %q", stderr)
	}
}

func TestCmdSetupUnknownProtocolValueDefaultsFullWithWarning(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)

	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 2}, nil
	}

	withArgs(t, "engram", "setup", "myagent", "--protocol=bogus")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil {
		t.Fatalf("unknown --protocol value must not fail setup, panic=%v", recovered)
	}
	if !strings.Contains(stderr, "warning") {
		t.Fatalf("expected stderr warning for unknown --protocol value, got %q", stderr)
	}
	if !strings.Contains(stdout, "Installed myagent plugin") {
		t.Fatalf("setup should still succeed: %q", stdout)
	}
	if got := setup.ReadProtocolMode(cfg.DataDir, "myagent"); got != setup.ProtocolModeFull {
		t.Fatalf("ReadProtocolMode = %q, want full", got)
	}
}

// TestCmdSetupProtocolDanglingFlagDefaultsFullWithWarning guards JD-016: a
// dangling --protocol with no following token (last token in the arg list)
// must default the slug's mode to full with a stderr warning, same as an
// unknown --protocol value.
func TestCmdSetupProtocolDanglingFlagDefaultsFullWithWarning(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)

	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 2}, nil
	}

	withArgs(t, "engram", "setup", "myagent", "--protocol")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil {
		t.Fatalf("dangling --protocol must not fail setup, panic=%v", recovered)
	}
	if !strings.Contains(stderr, "warning") {
		t.Fatalf("expected stderr warning for dangling --protocol, got %q", stderr)
	}
	if !strings.Contains(stdout, "Installed myagent plugin") {
		t.Fatalf("setup should still succeed: %q", stdout)
	}
	if got := setup.ReadProtocolMode(cfg.DataDir, "myagent"); got != setup.ProtocolModeFull {
		t.Fatalf("ReadProtocolMode = %q, want full", got)
	}
}

// TestCmdSetupDuplicateProtocolFlagLastWins guards JD-016: when --protocol
// is given twice, the last occurrence wins.
func TestCmdSetupDuplicateProtocolFlagLastWins(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)

	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 2}, nil
	}

	withArgs(t, "engram", "setup", "myagent", "--protocol=slim", "--protocol=full")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if got := setup.ReadProtocolMode(cfg.DataDir, "myagent"); got != setup.ProtocolModeFull {
		t.Fatalf("ReadProtocolMode = %q, want full (last flag wins)", got)
	}
}

// TestCmdSetupSlugThenUnknownFlagFallsBackToInteractive guards JD-016: a
// slug followed by an unrecognized flag falls back to the interactive menu
// rather than proceeding with a direct install of the given slug.
func TestCmdSetupSlugThenUnknownFlagFallsBackToInteractive(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)

	setupSupportedAgents = func() []setup.Agent {
		return []setup.Agent{{Name: "opencode", Description: "OpenCode", InstallDir: "/tmp/opencode"}}
	}
	installedAgent := ""
	setupInstallAgent = func(agent string) (*setup.Result, error) {
		installedAgent = agent
		return &setup.Result{Agent: agent, Destination: "/tmp/opencode", Files: 1}, nil
	}
	scanInputLine = func(a ...any) (int, error) {
		p := a[0].(*string)
		*p = "1"
		return 1, nil
	}

	withArgs(t, "engram", "setup", "claude-code", "-x")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Which agent do you want to set up?") {
		t.Fatalf("expected interactive menu, got %q", stdout)
	}
	if installedAgent != "opencode" {
		t.Fatalf("installed agent = %q, want opencode (interactive selection, not the direct slug claude-code)", installedAgent)
	}
}

func TestCmdSetupNoSlugWithProtocolAppliesToSelectedAgent(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	setProtocolVersion(t, "1.4.0")

	setupSupportedAgents = func() []setup.Agent {
		return []setup.Agent{{Name: "opencode", Description: "OpenCode", InstallDir: "/tmp/opencode"}}
	}
	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/opencode", Files: 1}, nil
	}
	scanInputLine = func(a ...any) (int, error) {
		p := a[0].(*string)
		*p = "1"
		return 1, nil
	}

	withArgs(t, "engram", "setup", "--protocol=slim")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Installing opencode plugin") {
		t.Fatalf("expected interactive install flow: %q", stdout)
	}
	if got := setup.ReadProtocolMode(cfg.DataDir, "opencode"); got != setup.ProtocolModeSlim {
		t.Fatalf("ReadProtocolMode(opencode) = %q, want slim", got)
	}
}

// TestCmdSetupWriteReadPathParityUnderEnvDataDir guards JD-005: the writer
// (cmdSetup) and reader (cmdProtocolMode) must share the SAME resolved
// cfg/DataDir main() computes (store.DefaultConfig + ENGRAM_DATA_DIR
// override), not independently re-derived configs.
func TestCmdSetupWriteReadPathParityUnderEnvDataDir(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	setProtocolVersion(t, "1.4.0")

	dataDir := t.TempDir()
	t.Setenv("ENGRAM_DATA_DIR", dataDir)

	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	if dir := os.Getenv("ENGRAM_DATA_DIR"); dir != "" {
		cfg.DataDir = dir
	}

	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 2}, nil
	}

	withArgs(t, "engram", "setup", "myagent", "--protocol=slim")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}

	// Read directly through the same dataDir the ENGRAM_DATA_DIR override
	// resolved to, proving the write landed exactly there.
	if got := setup.ReadProtocolMode(dataDir, "myagent"); got != setup.ProtocolModeSlim {
		t.Fatalf("ReadProtocolMode(dataDir) = %q, want slim — write/read path mismatch", got)
	}

	withArgs(t, "engram", "protocol-mode", "myagent")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdProtocolMode(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	// A supported version makes slim output prove that the runtime read uses
	// exactly the data directory setup wrote to.
	if strings.TrimSpace(stdout) != "slim" {
		t.Fatalf("stdout = %q, want %q (write/read path mismatch)", stdout, "slim")
	}
}

func TestCmdProtocolModeSlimAndVersionFloorMet(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	if err := setup.WriteProtocolMode(cfg.DataDir, "claude-code", "slim"); err != nil {
		t.Fatalf("seed WriteProtocolMode: %v", err)
	}

	oldVersion := version
	version = "1.5.0"
	t.Cleanup(func() { version = oldVersion })

	withArgs(t, "engram", "protocol-mode", "claude-code")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdProtocolMode(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if strings.TrimSpace(stdout) != "slim" {
		t.Fatalf("stdout = %q, want %q", stdout, "slim")
	}
}

func TestCmdProtocolModeSlimButVersionBelowFloor(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	if err := setup.WriteProtocolMode(cfg.DataDir, "claude-code", "slim"); err != nil {
		t.Fatalf("seed WriteProtocolMode: %v", err)
	}

	oldVersion := version
	version = "1.3.9"
	t.Cleanup(func() { version = oldVersion })

	withArgs(t, "engram", "protocol-mode", "claude-code")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdProtocolMode(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if strings.TrimSpace(stdout) != "full" {
		t.Fatalf("stdout = %q, want %q (version below floor)", stdout, "full")
	}
}

func TestCmdProtocolModeFullPersistedIgnoresVersion(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	if err := setup.WriteProtocolMode(cfg.DataDir, "claude-code", "full"); err != nil {
		t.Fatalf("seed WriteProtocolMode: %v", err)
	}

	oldVersion := version
	version = "9.9.9"
	t.Cleanup(func() { version = oldVersion })

	withArgs(t, "engram", "protocol-mode", "claude-code")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdProtocolMode(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if strings.TrimSpace(stdout) != "full" {
		t.Fatalf("stdout = %q, want %q", stdout, "full")
	}
}

func TestCmdProtocolModeMissingFileDefaultsFull(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)

	oldVersion := version
	version = "1.5.0"
	t.Cleanup(func() { version = oldVersion })

	withArgs(t, "engram", "protocol-mode", "claude-code")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdProtocolMode(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if strings.TrimSpace(stdout) != "full" {
		t.Fatalf("stdout = %q, want %q (missing file)", stdout, "full")
	}
}

func TestCmdProtocolModeCorruptedJSONDefaultsFull(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	if err := os.WriteFile(cfg.DataDir+"/protocol-mode.json", []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write corrupted file: %v", err)
	}

	oldVersion := version
	version = "1.5.0"
	t.Cleanup(func() { version = oldVersion })

	withArgs(t, "engram", "protocol-mode", "claude-code")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdProtocolMode(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if strings.TrimSpace(stdout) != "full" {
		t.Fatalf("stdout = %q, want %q (corrupted JSON)", stdout, "full")
	}
}

func TestCmdProtocolModeMissingSlugKeyDefaultsFull(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	if err := setup.WriteProtocolMode(cfg.DataDir, "opencode", "slim"); err != nil {
		t.Fatalf("seed WriteProtocolMode: %v", err)
	}

	oldVersion := version
	version = "1.5.0"
	t.Cleanup(func() { version = oldVersion })

	withArgs(t, "engram", "protocol-mode", "claude-code")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdProtocolMode(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}
	if strings.TrimSpace(stdout) != "full" {
		t.Fatalf("stdout = %q, want %q (missing slug key)", stdout, "full")
	}
}

func TestMeetsProtocolVersionFloor(t *testing.T) {
	tests := []struct {
		name           string
		version        string
		classification protocolVersionClassification
		wantSlim       bool
	}{
		{"supported release", "1.4.0", protocolVersionSupported, true},
		{"supported tagged release", "v1.5.0", protocolVersionSupported, true},
		{"legacy numeric release", "1.4", protocolVersionSupported, true},
		{"below-floor release", "1.3.9", protocolVersionBelowFloor, false},
		{"development build", "dev", protocolVersionUnsupported, false},
		{"empty version", "", protocolVersionUnsupported, false},
		{"Go pseudo-version", "v1.4.0-0.20260102030405-abcdef012345", protocolVersionUnsupported, false},
		{"dirty build", "1.4.0-dirty", protocolVersionUnsupported, false},
		{"non-release input", "not-a-version", protocolVersionUnsupported, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyProtocolVersion(tt.version); got != tt.classification {
				t.Fatalf("classifyProtocolVersion(%q) = %v, want %v", tt.version, got, tt.classification)
			}
			if got := meetsProtocolVersionFloor(tt.version); got != tt.wantSlim {
				t.Fatalf("meetsProtocolVersionFloor(%q) = %v, want %v", tt.version, got, tt.wantSlim)
			}
		})
	}
}

func TestApplyProtocolModeWarnsOnlyWhenSlimCannotActivate(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		mode        string
		wantWarning string
	}{
		{"supported release", "1.4.0", setup.ProtocolModeSlim, ""},
		{"full mode", "dev", setup.ProtocolModeFull, ""},
		{"below-floor release", "1.3.9", setup.ProtocolModeSlim, "below 1.4.0"},
		{"development build", "dev", setup.ProtocolModeSlim, "not a clean tagged release"},
		{"Go pseudo-version", "v1.4.0-0.20260102030405-abcdef012345", setup.ProtocolModeSlim, "not a clean tagged release"},
		{"dirty build", "1.4.0-dirty", setup.ProtocolModeSlim, "not a clean tagged release"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			setProtocolVersion(t, tt.version)

			stdout, stderr := captureOutput(t, func() {
				applyProtocolMode(cfg, "claude-code", tt.mode)
			})
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if tt.wantWarning == "" {
				if stderr != "" {
					t.Fatalf("stderr = %q, want no warning", stderr)
				}
			} else if !strings.Contains(stderr, tt.wantWarning) || !strings.Contains(stderr, "install a clean tagged release") {
				t.Fatalf("stderr = %q, want actionable warning containing %q", stderr, tt.wantWarning)
			}
			if got := setup.ReadProtocolMode(cfg.DataDir, "claude-code"); got != tt.mode {
				t.Fatalf("ReadProtocolMode = %q, want %q", got, tt.mode)
			}
		})
	}
}

func TestCmdProtocolModeOutputsOnlyModeForUnsupportedVersions(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"supported release", "1.4.0", setup.ProtocolModeSlim},
		{"below-floor release", "1.3.9", setup.ProtocolModeFull},
		{"development build", "dev", setup.ProtocolModeFull},
		{"Go pseudo-version", "v1.4.0-0.20260102030405-abcdef012345", setup.ProtocolModeFull},
		{"dirty build", "1.4.0-dirty", setup.ProtocolModeFull},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubExitWithPanic(t)
			cfg := testConfig(t)
			if err := setup.WriteProtocolMode(cfg.DataDir, "claude-code", setup.ProtocolModeSlim); err != nil {
				t.Fatalf("seed WriteProtocolMode: %v", err)
			}
			setProtocolVersion(t, tt.version)

			withArgs(t, "engram", "protocol-mode", "claude-code")
			stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdProtocolMode(cfg) })
			if recovered != nil || stderr != "" {
				t.Fatalf("panic=%v stderr=%q", recovered, stderr)
			}
			if stdout != tt.want+"\n" {
				t.Fatalf("stdout = %q, want exactly %q", stdout, tt.want+"\n")
			}
		})
	}
}

func setProtocolVersion(t *testing.T, value string) {
	t.Helper()
	oldVersion := version
	version = value
	t.Cleanup(func() { version = oldVersion })
}
