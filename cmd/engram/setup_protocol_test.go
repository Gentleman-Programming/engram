package main

import (
	"os"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/setup"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// Token-classification rows from openspec/changes/setup-protocol-flag/proposal.md
// (Approach table): one test per row.

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
	}
}

func TestCmdSetupProtocolEqualsFormPersistsSlim(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)

	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 2}, nil
	}

	withArgs(t, "engram", "setup", "myagent", "--protocol=slim")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("panic=%v stderr=%q", recovered, stderr)
	}

	if got := setup.ReadProtocolMode(cfg.DataDir, "myagent"); got != setup.ProtocolModeSlim {
		t.Fatalf("ReadProtocolMode = %q, want slim", got)
	}
}

func TestCmdSetupProtocolSpaceFormPersistsSlim(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)

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
	// version == "dev" in the test binary, so the version floor is never
	// met — this assertion targets read-path parity, not the floor check.
	if strings.TrimSpace(stdout) != "full" {
		t.Fatalf("stdout = %q, want %q (version=dev fails the floor check)", stdout, "full")
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
		in   string
		want bool
	}{
		{"1.4.0", true},
		{"1.4.1", true},
		{"1.5.0", true},
		{"2.0.0", true},
		{"1.3.9", false},
		{"1.0.0", false},
		{"0.9.9", false},
		{"dev", false},
		{"", false},
		{"not-a-version", false},
		{"v1.4.0", true},
		{"1.4", true},
	}
	for _, tt := range tests {
		if got := meetsProtocolVersionFloor(tt.in); got != tt.want {
			t.Errorf("meetsProtocolVersionFloor(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
