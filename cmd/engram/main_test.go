package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/mcp"
	"github.com/Gentleman-Programming/engram/internal/obsidian"
	"github.com/Gentleman-Programming/engram/internal/project"
	"github.com/Gentleman-Programming/engram/internal/setup"
	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
	versioncheck "github.com/Gentleman-Programming/engram/internal/version"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func testConfig(t *testing.T) store.Config {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = t.TempDir()
	return cfg
}

func withArgs(t *testing.T, args ...string) {
	t.Helper()
	old := os.Args
	os.Args = args
	t.Cleanup(func() {
		os.Args = old
	})
}

func withCwd(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir to %s: %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(old)
	})
}

func stubCheckForUpdates(t *testing.T, result versioncheck.CheckResult) {
	t.Helper()
	old := checkForUpdates
	checkForUpdates = func(string) versioncheck.CheckResult { return result }
	t.Cleanup(func() { checkForUpdates = old })
}

func captureOutput(t *testing.T, fn func()) (stdout string, stderr string) {
	t.Helper()

	oldOut := os.Stdout
	oldErr := os.Stderr

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}

	os.Stdout = outW
	os.Stderr = errW

	type capturedOutput struct {
		bytes []byte
		err   error
	}
	outDone := make(chan capturedOutput, 1)
	errDone := make(chan capturedOutput, 1)
	go func() {
		bytes, err := io.ReadAll(outR)
		outDone <- capturedOutput{bytes: bytes, err: err}
	}()
	go func() {
		bytes, err := io.ReadAll(errR)
		errDone <- capturedOutput{bytes: bytes, err: err}
	}()

	fn()

	_ = outW.Close()
	_ = errW.Close()
	os.Stdout = oldOut
	os.Stderr = oldErr

	out := <-outDone
	if out.err != nil {
		t.Fatalf("read stdout: %v", out.err)
	}
	errOut := <-errDone
	if errOut.err != nil {
		t.Fatalf("read stderr: %v", errOut.err)
	}

	return string(out.bytes), string(errOut.bytes)
}

func TestCaptureOutputDrainsLargeStdoutAndStderrConcurrently(t *testing.T) {
	stdout := strings.Repeat("stdout ", 12*1024)
	stderr := strings.Repeat("stderr ", 12*1024)

	gotStdout, gotStderr := captureOutput(t, func() {
		_, _ = fmt.Fprint(os.Stdout, stdout)
		_, _ = fmt.Fprint(os.Stderr, stderr)
	})

	if gotStdout != stdout || gotStderr != stderr {
		t.Fatalf("captureOutput() = (%d stdout bytes, %d stderr bytes), want exact output", len(gotStdout), len(gotStderr))
	}
}

func mustSeedObservation(t *testing.T, cfg store.Config, sessionID, project, typ, title, content, scope string) int64 {
	t.Helper()

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession(sessionID, project, "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	id, err := s.AddObservation(store.AddObservationParams{
		SessionID: sessionID,
		Type:      typ,
		Title:     title,
		Content:   content,
		Project:   project,
		Scope:     scope,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	return id
}

func rewriteLegacyProjectName(t *testing.T, cfg store.Config, from, to string) {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"observations", "sessions", "user_prompts"} {
		if _, err := db.Exec("UPDATE "+table+" SET project = ? WHERE project = ?", to, from); err != nil {
			t.Fatalf("rewrite %s project: %v", table, err)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "short string", in: "abc", max: 10, want: "abc"},
		{name: "exact length", in: "hello", max: 5, want: "hello"},
		{name: "long string", in: "abcdef", max: 3, want: "abc..."},
		{name: "spanish accents", in: "Decisión de arquitectura", max: 8, want: "Decisión..."},
		{name: "emoji", in: "🐛🔧🚀✨🎉💡", max: 3, want: "🐛🔧🚀..."},
		{name: "mixed ascii and multibyte", in: "café☕latte", max: 5, want: "café☕..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.max)
			if got != tc.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

func TestPrintUsage(t *testing.T) {
	oldVersion := version
	version = "test-version"
	t.Cleanup(func() {
		version = oldVersion
	})

	stdout, stderr := captureOutput(t, func() { printUsage() })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "engram vtest-version") {
		t.Fatalf("usage missing version: %q", stdout)
	}
	if !strings.Contains(stdout, "search <query>") || !strings.Contains(stdout, "setup [agent]") {
		t.Fatalf("usage missing expected commands: %q", stdout)
	}
	for _, agent := range []string{"opencode", "pi", "claude-code", "gemini-cli", "codex", "antigravity-cli", "windsurf", "qwen", "kiro", "cursor", "vscode-copilot", "kilocode"} {
		if !strings.Contains(stdout, agent) {
			t.Fatalf("usage missing setup agent %q: %q", agent, stdout)
		}
	}
	if !strings.Contains(stdout, "cloud <subcommand>") {
		t.Fatalf("usage missing cloud command tree: %q", stdout)
	}
	if !strings.Contains(stdout, "serve      Run cloud backend + dashboard") {
		t.Fatalf("usage missing cloud serve command: %q", stdout)
	}
	if !strings.Contains(stdout, "Required for cloud serve in BOTH token auth and insecure no-auth mode") {
		t.Fatalf("usage missing updated ENGRAM_CLOUD_ALLOWED_PROJECTS contract: %q", stdout)
	}
	for _, token := range []string{
		"ENGRAM_DATABASE_URL",
		"ENGRAM_CLOUD_HOST",
		"ENGRAM_CLOUD_MAX_PUSH_BYTES",
		"ENGRAM_CLOUD_TOKEN",
		"ENGRAM_CLOUD_INSECURE_NO_AUTH",
		"Cannot be combined with ENGRAM_CLOUD_TOKEN",
		"Cannot be combined with ENGRAM_CLOUD_ADMIN",
		"ENGRAM_CLOUD_ADMIN",
	} {
		if !strings.Contains(stdout, token) {
			t.Fatalf("usage missing cloud serve env/runtime rule %q: %q", token, stdout)
		}
	}
}

func TestPrintPostInstall(t *testing.T) {
	tests := []struct {
		name       string
		result     *setup.Result
		expects    []string
		notExpects []string
	}{
		{
			name:       "opencode with subagent monitor enabled",
			result:     &setup.Result{Agent: "opencode", TUIPluginEnabled: true},
			expects:    []string{"Restart OpenCode", "opencode-subagent-statusline", "auto-starts"},
			notExpects: []string{"engram serve &"},
		},
		{
			name:       "opencode with subagent monitor not enabled",
			result:     &setup.Result{Agent: "opencode", TUIPluginEnabled: false},
			expects:    []string{"Restart OpenCode", "auto-starts"},
			notExpects: []string{"opencode-subagent-statusline", "engram serve &"},
		},
		{
			name:       "pi",
			result:     &setup.Result{Agent: "pi"},
			expects:    []string{"Restart Pi", "pi list"},
			notExpects: []string{"ENGRAM_BIN"},
		},
		{
			name:    "gemini-cli",
			result:  &setup.Result{Agent: "gemini-cli"},
			expects: []string{"Restart Gemini CLI", "~/.gemini/settings.json"},
		},
		{
			name:    "codex",
			result:  &setup.Result{Agent: "codex"},
			expects: []string{"Restart Codex", "~/.codex/config.toml"},
		},
		{
			name:    "antigravity-cli",
			result:  &setup.Result{Agent: "antigravity-cli"},
			expects: []string{"Restart Antigravity", "~/.gemini/config/mcp_config.json", "~/.gemini/GEMINI.md"},
		},
		{
			name:    "windsurf",
			result:  &setup.Result{Agent: "windsurf"},
			expects: []string{"Restart Windsurf", "~/.codeium/windsurf/mcp_config.json"},
		},
		{
			name:    "qwen",
			result:  &setup.Result{Agent: "qwen"},
			expects: []string{"Restart Qwen Code", "~/.qwen/settings.json"},
		},
		{
			name:    "kiro",
			result:  &setup.Result{Agent: "kiro"},
			expects: []string{"Restart Kiro", "~/.kiro/settings/mcp.json"},
		},
		{
			name:    "cursor",
			result:  &setup.Result{Agent: "cursor"},
			expects: []string{"Restart Cursor", "~/.cursor/mcp.json", "engram-memory-protocol.md", "User Rules"},
		},
		{
			name:    "vscode-copilot",
			result:  &setup.Result{Agent: "vscode-copilot"},
			expects: []string{"Restart VS Code", "servers.engram", "engram.instructions.md"},
		},
		{
			name:    "kilocode",
			result:  &setup.Result{Agent: "kilocode"},
			expects: []string{"Restart Kilo Code", "~/.config/kilo/opencode.json"},
		},
		{
			name:   "unknown",
			result: &setup.Result{Agent: "unknown"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr := captureOutput(t, func() { printPostInstall(tc.result) })
			if stderr != "" {
				t.Fatalf("expected no stderr, got: %q", stderr)
			}
			for _, expected := range tc.expects {
				if !strings.Contains(stdout, expected) {
					t.Fatalf("output missing %q: %q", expected, stdout)
				}
			}
			for _, forbidden := range tc.notExpects {
				if strings.Contains(stdout, forbidden) {
					t.Fatalf("output unexpectedly contains %q: %q", forbidden, stdout)
				}
			}
			if len(tc.expects) == 0 && stdout != "" {
				t.Fatalf("expected empty output for unknown agent, got: %q", stdout)
			}
		})
	}
}

func TestPrintPostInstallClaudeCodeAllowlist(t *testing.T) {
	t.Run("user accepts allowlist", func(t *testing.T) {
		oldScan := scanInputLine
		oldAllowlist := setupAddClaudeCodeAllowlist
		t.Cleanup(func() {
			scanInputLine = oldScan
			setupAddClaudeCodeAllowlist = oldAllowlist
		})

		scanInputLine = func(a ...any) (int, error) {
			ptr := a[0].(*string)
			*ptr = "y"
			return 1, nil
		}
		allowlistCalled := false
		setupAddClaudeCodeAllowlist = func() error {
			allowlistCalled = true
			return nil
		}

		stdout, _ := captureOutput(t, func() { printPostInstall(&setup.Result{Agent: "claude-code"}) })
		if !allowlistCalled {
			t.Fatalf("expected AddClaudeCodeAllowlist to be called")
		}
		if !strings.Contains(stdout, "tools added to allowlist") {
			t.Fatalf("expected success message, got: %q", stdout)
		}
		if !strings.Contains(stdout, "Restart Claude Code") {
			t.Fatalf("expected next steps, got: %q", stdout)
		}
	})

	t.Run("user declines allowlist", func(t *testing.T) {
		oldScan := scanInputLine
		oldAllowlist := setupAddClaudeCodeAllowlist
		t.Cleanup(func() {
			scanInputLine = oldScan
			setupAddClaudeCodeAllowlist = oldAllowlist
		})

		scanInputLine = func(a ...any) (int, error) {
			ptr := a[0].(*string)
			*ptr = "n"
			return 1, nil
		}
		allowlistCalled := false
		setupAddClaudeCodeAllowlist = func() error {
			allowlistCalled = true
			return nil
		}

		stdout, _ := captureOutput(t, func() { printPostInstall(&setup.Result{Agent: "claude-code"}) })
		if allowlistCalled {
			t.Fatalf("expected AddClaudeCodeAllowlist NOT to be called")
		}
		if !strings.Contains(stdout, "Skipped") {
			t.Fatalf("expected skip message, got: %q", stdout)
		}
	})

	t.Run("allowlist error shows warning", func(t *testing.T) {
		oldScan := scanInputLine
		oldAllowlist := setupAddClaudeCodeAllowlist
		t.Cleanup(func() {
			scanInputLine = oldScan
			setupAddClaudeCodeAllowlist = oldAllowlist
		})

		scanInputLine = func(a ...any) (int, error) {
			ptr := a[0].(*string)
			*ptr = "y"
			return 1, nil
		}
		setupAddClaudeCodeAllowlist = func() error {
			return os.ErrPermission
		}

		_, stderr := captureOutput(t, func() { printPostInstall(&setup.Result{Agent: "claude-code"}) })
		if !strings.Contains(stderr, "warning") {
			t.Fatalf("expected warning in stderr, got: %q", stderr)
		}
	})
}

func TestCmdSyncCloudRegressionPreservesLegacyBehaviorWithUpgradeStatePresent(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	originalSyncExport := syncExport
	originalSyncStatus := syncStatus
	t.Cleanup(func() {
		syncExport = originalSyncExport
		syncStatus = originalSyncStatus
	})

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{
		Project:          "proj-a",
		Stage:            store.UpgradeStageDoctorBlocked,
		RepairClass:      store.UpgradeRepairClassRepairable,
		LastErrorCode:    "upgrade_repairable_unenrolled",
		LastErrorMessage: "legacy metadata drift",
	}); err != nil {
		_ = s.Close()
		t.Fatalf("seed upgrade state: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	syncExport = func(*engramsync.Syncer, string, string) (*engramsync.SyncResult, error) {
		return &engramsync.SyncResult{ChunkID: "chunk-regression", SessionsExported: 1}, nil
	}
	syncStatus = func(*engramsync.Syncer) (int, int, int, error) {
		return 1, 1, 0, nil
	}

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-a")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud sync regression path should stay successful, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Cloud sync complete for project \"proj-a\".") {
		t.Fatalf("expected unchanged cloud sync success messaging, got %q", stdout)
	}

	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s.Close()
	state, err := s.GetCloudUpgradeState("proj-a")
	if err != nil {
		t.Fatalf("load upgrade state: %v", err)
	}
	if state == nil || state.Stage != store.UpgradeStageDoctorBlocked {
		t.Fatalf("sync --cloud must not mutate upgrade stage; got %+v", state)
	}
}

func TestCmdSaveAndSearch(t *testing.T) {
	cfg := testConfig(t)

	withArgs(t,
		"engram", "save", "my-title", "my-content",
		"--type", "bugfix",
		"--project", "alpha",
		"--scope", "personal",
		"--topic", "auth/token",
	)

	stdout, stderr := captureOutput(t, func() { cmdSave(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "Memory saved:") || !strings.Contains(stdout, "my-title") {
		t.Fatalf("unexpected save output: %q", stdout)
	}

	withArgs(t, "engram", "search", "my-content", "--type", "bugfix", "--project", "alpha", "--scope", "personal", "--limit", "1")
	searchOut, searchErr := captureOutput(t, func() { cmdSearch(cfg) })
	if searchErr != "" {
		t.Fatalf("expected no stderr from search, got: %q", searchErr)
	}
	if !strings.Contains(searchOut, "Found 1 memories") || !strings.Contains(searchOut, "my-title") {
		t.Fatalf("unexpected search output: %q", searchOut)
	}

	withArgs(t, "engram", "search", "definitely-not-found")
	noneOut, noneErr := captureOutput(t, func() { cmdSearch(cfg) })
	if noneErr != "" {
		t.Fatalf("expected no stderr from empty search, got: %q", noneErr)
	}
	if !strings.Contains(noneOut, "No memories found") {
		t.Fatalf("expected empty search message, got: %q", noneOut)
	}
}

func TestCmdSaveResolvesConfiguredProjectWithoutFlag(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	cwd := t.TempDir()
	configDir := filepath.Join(cwd, ".engram")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":"Configured-Project"}`), 0644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	withCwd(t, cwd)
	withArgs(t, "engram", "save", "resolved-title", "resolved-content")

	stdout, stderr := captureOutput(t, func() { cmdSave(cfg) })
	if stderr != "" || !strings.Contains(stdout, "Memory saved:") {
		t.Fatalf("cmdSave output = stdout %q stderr %q", stdout, stderr)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	session, err := s.GetSession("manual-save-configured-project")
	if err != nil || session.Project != "configured-project" {
		t.Fatalf("resolved session = %#v, err=%v", session, err)
	}
	observations, err := s.RecentObservations("configured-project", "project", 10)
	if err != nil || len(observations) != 1 || observations[0].Title != "resolved-title" {
		t.Fatalf("resolved observations = %#v, err=%v", observations, err)
	}
	var mutations int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = ?`, "configured-project").Scan(&mutations); err != nil || mutations != 2 {
		t.Fatalf("resolved journal mutations = %d, err=%v, want 2", mutations, err)
	}
}

// assertCmdSaveOwnedBy runs cmdSave and asserts the manual session and its
// observation landed under the expected project.
func assertCmdSaveOwnedBy(t *testing.T, cfg store.Config, rawProject, wantProject string) {
	t.Helper()
	stdout, stderr := captureOutput(t, func() { cmdSave(cfg) })
	wantWarning := fmt.Sprintf("Project name normalized: %q → %q", rawProject, wantProject)
	if !strings.Contains(stdout, "Memory saved:") || !strings.Contains(stderr, wantWarning) {
		t.Fatalf("cmdSave output = stdout %q stderr %q, want %q", stdout, stderr, wantWarning)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	session, err := s.GetSession("manual-save-" + wantProject)
	if err != nil || session.Project != wantProject {
		t.Fatalf("resolved session = %#v, err=%v, want project %q", session, err, wantProject)
	}
	observations, err := s.RecentObservations(wantProject, "project", 10)
	if err != nil || len(observations) != 1 || observations[0].Title != "resolved-title" {
		t.Fatalf("resolved observations = %#v, err=%v", observations, err)
	}
}

// seedDetectedProjectCWD points the working directory at a project whose
// .engram/config.json names a project other than any process-level override.
func seedDetectedProjectCWD(t *testing.T) {
	t.Helper()
	cwd := t.TempDir()
	configDir := filepath.Join(cwd, ".engram")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":"Configured-Project"}`), 0644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	withCwd(t, cwd)
}

func TestCmdSaveHonorsEngramProjectEnvironmentOverride(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	seedDetectedProjectCWD(t)
	t.Setenv("ENGRAM_PROJECT", "Env-Project")
	withArgs(t, "engram", "save", "resolved-title", "resolved-content")

	assertCmdSaveOwnedBy(t, cfg, "Env-Project", "env-project")
}

func TestCmdSaveExplicitProjectFlagBeatsEnvironmentOverride(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	seedDetectedProjectCWD(t)
	t.Setenv("ENGRAM_PROJECT", "Env-Project")
	withArgs(t, "engram", "save", "resolved-title", "resolved-content", "--project", "Flag-Project")

	assertCmdSaveOwnedBy(t, cfg, "Flag-Project", "flag-project")
}

func TestCmdSaveUsesDetectionSeamAndPrintsNormalizationWarning(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	cwd := t.TempDir()
	withCwd(t, cwd)
	withArgs(t, "engram", "save", "resolved-title", "resolved-content")

	originalDetectProjectFull := detectProjectFull
	detectProjectFull = func(gotCWD string) project.DetectionResult {
		if gotCWD != cwd {
			t.Fatalf("detection cwd = %q, want %q", gotCWD, cwd)
		}
		return project.DetectionResult{Project: " Configured--Project "}
	}
	t.Cleanup(func() { detectProjectFull = originalDetectProjectFull })

	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSave(cfg) })
	if recovered != nil || !strings.Contains(stdout, "Memory saved:") {
		t.Fatalf("cmdSave result = stdout %q stderr %q panic %v", stdout, stderr, recovered)
	}
	if !strings.Contains(stderr, `Project name normalized: " Configured--Project " → "configured-project"`) {
		t.Fatalf("normalization warning = %q", stderr)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	if _, err := s.GetSession("manual-save-configured-project"); err != nil {
		t.Fatalf("normalized manual session: %v", err)
	}
}

func TestCmdSaveRejectsUnresolvableProjectBeforeOpeningStore(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)
	cwd := t.TempDir()
	configDir := filepath.Join(cwd, ".engram")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":""}`), 0644); err != nil {
		t.Fatalf("write invalid project config: %v", err)
	}
	withCwd(t, cwd)
	withArgs(t, "engram", "save", "rejected-title", "rejected-content")

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSave(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "cannot save without an unambiguous project identity") {
		t.Fatalf("unexpected rejection: %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "engram.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unresolvable project opened store or left state: %v", err)
	}
}

func TestCmdTimeline(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "s-1", "proj", "note", "first", "first content", "project")
	focusID := mustSeedObservation(t, cfg, "s-1", "proj", "note", "focus", "focus content", "project")
	mustSeedObservation(t, cfg, "s-1", "proj", "note", "third", "third content", "project")
	t.Setenv("ENGRAM_PROJECT", "proj")

	withArgs(t, "engram", "timeline", strconv.FormatInt(focusID, 10), "--before", "1", "--after", "1")
	stdout, stderr := captureOutput(t, func() { cmdTimeline(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "Session:") || !strings.Contains(stdout, ">>> #"+strconv.FormatInt(focusID, 10)) {
		t.Fatalf("timeline output missing expected focus/session info: %q", stdout)
	}
	if !strings.Contains(stdout, "Before") || !strings.Contains(stdout, "After") {
		t.Fatalf("timeline output missing before/after sections: %q", stdout)
	}
}

func TestCmdContextAndStats(t *testing.T) {
	cfg := testConfig(t)

	withArgs(t, "engram", "context")
	emptyCtxOut, emptyCtxErr := captureOutput(t, func() { cmdContext(cfg) })
	if emptyCtxErr != "" {
		t.Fatalf("expected no stderr for empty context, got: %q", emptyCtxErr)
	}
	if !strings.Contains(emptyCtxOut, "No previous session memories found") {
		t.Fatalf("unexpected empty context output: %q", emptyCtxOut)
	}

	mustSeedObservation(t, cfg, "s-ctx", "project-x", "decision", "title", "content", "project")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	_, err = s.AddPrompt(store.AddPromptParams{SessionID: "s-ctx", Content: "user asked about context", Project: "project-x"})
	if err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}
	_ = s.Close()

	withArgs(t, "engram", "context", "project-x")
	ctxOut, ctxErr := captureOutput(t, func() { cmdContext(cfg) })
	if ctxErr != "" {
		t.Fatalf("expected no stderr for populated context, got: %q", ctxErr)
	}
	if !strings.Contains(ctxOut, "## Memory from Previous Sessions") || !strings.Contains(ctxOut, "Recent Observations") {
		t.Fatalf("unexpected populated context output: %q", ctxOut)
	}

	withArgs(t, "engram", "stats", "--all")
	statsOut, statsErr := captureOutput(t, func() { cmdStats(cfg) })
	if statsErr != "" {
		t.Fatalf("expected no stderr from stats, got: %q", statsErr)
	}
	if !strings.Contains(statsOut, "Engram Memory Stats") || !strings.Contains(statsOut, "project-x") {
		t.Fatalf("unexpected stats output: %q", statsOut)
	}
}

func TestCmdExportAndImport(t *testing.T) {
	sourceCfg := testConfig(t)
	targetCfg := testConfig(t)

	mustSeedObservation(t, sourceCfg, "s-exp", "proj-exp", "pattern", "exported", "export me", "project")

	exportPath := filepath.Join(t.TempDir(), "memories.json")

	withArgs(t, "engram", "export", exportPath, "--all")
	exportOut, exportErr := captureOutput(t, func() { cmdExport(sourceCfg) })
	if exportErr != "" {
		t.Fatalf("expected no stderr from export, got: %q", exportErr)
	}
	if !strings.Contains(exportOut, "Exported to "+exportPath) {
		t.Fatalf("unexpected export output: %q", exportOut)
	}

	withArgs(t, "engram", "import", exportPath)
	importOut, importErr := captureOutput(t, func() { cmdImport(targetCfg) })
	if importErr != "" {
		t.Fatalf("expected no stderr from import, got: %q", importErr)
	}
	if !strings.Contains(importOut, "Imported from "+exportPath) {
		t.Fatalf("unexpected import output: %q", importOut)
	}

	s, err := store.New(targetCfg)
	if err != nil {
		t.Fatalf("store.New target: %v", err)
	}
	defer s.Close()

	results, err := s.Search("export", store.SearchOptions{Limit: 10, Project: "proj-exp"})
	if err != nil {
		t.Fatalf("Search after import: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected imported data to be searchable")
	}
}

func TestCmdSyncStatusExportAndImport(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)

	exportCfg := testConfig(t)
	importCfg := testConfig(t)

	mustSeedObservation(t, exportCfg, "s-sync", "sync-project", "note", "sync title", "sync content", "project")

	withArgs(t, "engram", "sync", "--status")
	statusOut, statusErr := captureOutput(t, func() { cmdSync(exportCfg) })
	if statusErr != "" {
		t.Fatalf("expected no stderr from status, got: %q", statusErr)
	}
	if !strings.Contains(statusOut, "Sync status:") {
		t.Fatalf("unexpected status output: %q", statusOut)
	}

	withArgs(t, "engram", "sync", "--all")
	exportOut, exportErr := captureOutput(t, func() { cmdSync(exportCfg) })
	if exportErr != "" {
		t.Fatalf("expected no stderr from sync export, got: %q", exportErr)
	}
	if !strings.Contains(exportOut, "Created chunk") {
		t.Fatalf("unexpected sync export output: %q", exportOut)
	}

	withArgs(t, "engram", "sync", "--import")
	importOut, importErr := captureOutput(t, func() { cmdSync(importCfg) })
	if importErr != "" {
		t.Fatalf("expected no stderr from sync import, got: %q", importErr)
	}
	if !strings.Contains(importOut, "Imported 1 new chunk(s)") {
		t.Fatalf("unexpected sync import output: %q", importOut)
	}

	withArgs(t, "engram", "sync", "--import")
	noopOut, noopErr := captureOutput(t, func() { cmdSync(importCfg) })
	if noopErr != "" {
		t.Fatalf("expected no stderr from second sync import, got: %q", noopErr)
	}
	if !strings.Contains(noopOut, "No new chunks to import") {
		t.Fatalf("unexpected second sync import output: %q", noopOut)
	}
}

func TestCmdSyncDefaultProjectNoData(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "repo-name")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	withCwd(t, workDir)

	cfg := testConfig(t)
	withArgs(t, "engram", "sync")
	stdout, stderr := captureOutput(t, func() { cmdSync(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, `Exporting memories for project "repo-name"`) {
		t.Fatalf("expected default project message, got: %q", stdout)
	}
	if !strings.Contains(stdout, `Nothing new to sync for project "repo-name"`) {
		t.Fatalf("expected no-data sync message, got: %q", stdout)
	}
}

func TestCmdSyncHonorsProcessProjectOverride(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	t.Setenv(project.EnvProjectOverride, "override-project")

	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "override-session", "override-project", "note", "override note", "override content", "project")
	withArgs(t, "engram", "sync")
	stdout, stderr := captureOutput(t, func() { cmdSync(cfg) })
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, `Exporting memories for project "override-project"`) {
		t.Fatalf("sync did not use process override: %q", stdout)
	}
}

func TestMainVersionAndHelpAliases(t *testing.T) {
	oldVersion := version
	version = "9.9.9-test"
	t.Cleanup(func() { version = oldVersion })
	stubCheckForUpdates(t, versioncheck.CheckResult{Status: versioncheck.StatusUpToDate})

	tests := []struct {
		name      string
		arg       string
		contains  string
		notStderr bool
	}{
		{name: "version", arg: "version", contains: "engram 9.9.9-test", notStderr: true},
		{name: "version short", arg: "-v", contains: "engram 9.9.9-test", notStderr: true},
		{name: "version long", arg: "--version", contains: "engram 9.9.9-test", notStderr: true},
		{name: "help", arg: "help", contains: "Usage:", notStderr: true},
		{name: "help short", arg: "-h", contains: "Commands:", notStderr: true},
		{name: "help long", arg: "--help", contains: "Environment:", notStderr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withArgs(t, "engram", tc.arg)
			stdout, stderr := captureOutput(t, func() { main() })
			if tc.notStderr && stderr != "" {
				t.Fatalf("expected no stderr, got: %q", stderr)
			}
			if !strings.Contains(stdout, tc.contains) {
				t.Fatalf("stdout %q does not include %q", stdout, tc.contains)
			}
		})
	}
}

func TestMainPrintsUpdateFailuresAndUpdates(t *testing.T) {
	oldVersion := version
	version = "1.10.7"
	t.Cleanup(func() { version = oldVersion })

	t.Run("prints check failure", func(t *testing.T) {
		stubCheckForUpdates(t, versioncheck.CheckResult{
			Status:  versioncheck.StatusCheckFailed,
			Message: "Could not check for updates: GitHub took too long to respond.",
		})
		withArgs(t, "engram", "version")

		stdout, stderr := captureOutput(t, func() { main() })
		if !strings.Contains(stdout, "engram 1.10.7") {
			t.Fatalf("stdout = %q", stdout)
		}
		if !strings.Contains(stderr, "Could not check for updates") {
			t.Fatalf("stderr = %q", stderr)
		}
	})

	t.Run("prints available update", func(t *testing.T) {
		stubCheckForUpdates(t, versioncheck.CheckResult{
			Status:  versioncheck.StatusUpdateAvailable,
			Message: "Update available: 1.10.7 -> 1.10.8",
		})
		withArgs(t, "engram", "version")

		stdout, stderr := captureOutput(t, func() { main() })
		if !strings.Contains(stdout, "engram 1.10.7") {
			t.Fatalf("stdout = %q", stdout)
		}
		if !strings.Contains(stderr, "Update available") {
			t.Fatalf("stderr = %q", stderr)
		}
	})

	t.Run("prints nothing when up to date", func(t *testing.T) {
		stubCheckForUpdates(t, versioncheck.CheckResult{Status: versioncheck.StatusUpToDate})
		withArgs(t, "engram", "version")

		stdout, stderr := captureOutput(t, func() { main() })
		if !strings.Contains(stdout, "engram 1.10.7") {
			t.Fatalf("stdout = %q", stdout)
		}
		if stderr != "" {
			t.Fatalf("stderr = %q, want empty", stderr)
		}
	})
}

func TestMainExitPaths(t *testing.T) {
	tests := []struct {
		name            string
		helperCase      string
		expectedOutput  string
		expectedStderr  string
		expectedExitOne bool
	}{
		{name: "no args", helperCase: "no-args", expectedOutput: "Usage:", expectedExitOne: true},
		{name: "unknown command", helperCase: "unknown", expectedOutput: "Usage:", expectedStderr: "unknown command:", expectedExitOne: true},
		{name: "cloud missing subcommand", helperCase: "cloud-missing", expectedOutput: "usage: engram cloud", expectedExitOne: true},
		{name: "cloud unknown subcommand", helperCase: "cloud-unknown", expectedOutput: "supported subcommands", expectedStderr: "unknown cloud command", expectedExitOne: true},
		{name: "cloud enroll missing project", helperCase: "cloud-enroll-missing", expectedOutput: "usage: engram cloud enroll <project>", expectedExitOne: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestMainExitHelper")
			cmd.Env = append(os.Environ(),
				"GO_WANT_HELPER_PROCESS=1",
				"HELPER_CASE="+tc.helperCase,
			)

			out, err := cmd.CombinedOutput()
			if tc.expectedExitOne {
				exitErr, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatalf("expected exit error, got %T (%v)", err, err)
				}
				if exitErr.ExitCode() != 1 {
					t.Fatalf("expected exit code 1, got %d; output=%q", exitErr.ExitCode(), string(out))
				}
			}

			if !strings.Contains(string(out), tc.expectedOutput) {
				t.Fatalf("output missing %q: %q", tc.expectedOutput, string(out))
			}
			if tc.expectedStderr != "" && !strings.Contains(string(out), tc.expectedStderr) {
				t.Fatalf("output missing stderr text %q: %q", tc.expectedStderr, string(out))
			}
		})
	}
}

func TestMainExitHelper(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	switch os.Getenv("HELPER_CASE") {
	case "no-args":
		os.Args = []string{"engram"}
	case "unknown":
		os.Args = []string{"engram", "definitely-unknown-command"}
	case "cloud-missing":
		os.Args = []string{"engram", "cloud"}
	case "cloud-unknown":
		os.Args = []string{"engram", "cloud", "nope"}
	case "cloud-enroll-missing":
		os.Args = []string{"engram", "cloud", "enroll"}
	default:
		os.Args = []string{"engram", "--help"}
	}

	main()
}

func TestCmdSearchLocalMode(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "s-local", "proj-local", "note", "local-result", "local content for search", "project")

	withArgs(t, "engram", "search", "local", "--project", "proj-local")
	stdout, stderr := captureOutput(t, func() { cmdSearch(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "Found") && !strings.Contains(stdout, "local-result") {
		t.Fatalf("expected local search results, got: %q", stdout)
	}
}

func TestUsageAdvertisesAllProjectReadSelectors(t *testing.T) {
	stdout, _ := captureOutput(t, func() { printUsage() })
	for _, want := range []string{
		"search <query>     Search memories [--type TYPE] [--project PROJECT|--all]",
		"timeline <obs_id>  Show chronological context around an observation [--before N] [--after N] [--project PROJECT|--all]",
		"stats [--project PROJECT|--all]",
		"export [file] [--project PROJECT|--all]",
		"obsidian-export [--project PROJECT|--all]",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("usage missing %q:\n%s", want, stdout)
		}
	}
}

func TestCmdStatsAndExportProjectScopeSkipGlobalLoads(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "scoped-load", "alpha", "note", "scoped", "scoped content", "project")

	oldStats := storeStats
	oldExport := storeExport
	storeStats = func(*store.Store) (*store.Stats, error) {
		return nil, errors.New("global stats should not run")
	}
	storeExport = func(*store.Store) (*store.ExportData, error) {
		return nil, errors.New("global export should not run")
	}
	t.Cleanup(func() {
		storeStats = oldStats
		storeExport = oldExport
	})

	withArgs(t, "engram", "stats", "--project", "alpha")
	stdout, stderr := captureOutput(t, func() { cmdStats(cfg) })
	if stderr != "" || !strings.Contains(stdout, "Observations: 1") {
		t.Fatalf("project stats must avoid the global loader: stdout=%q stderr=%q", stdout, stderr)
	}

	path := filepath.Join(workDir, "alpha.json")
	withArgs(t, "engram", "export", path, "--project", "alpha")
	_, stderr = captureOutput(t, func() { cmdExport(cfg) })
	if stderr != "" {
		t.Fatalf("project export must avoid the global loader: stderr=%q", stderr)
	}
}

func TestCmdTimelineStatsAndExportRejectInvalidProjectValues(t *testing.T) {
	commands := []struct {
		name string
		args []string
		run  func(store.Config)
	}{
		{name: "timeline", args: []string{"timeline", "1"}, run: cmdTimeline},
		{name: "stats", args: []string{"stats"}, run: cmdStats},
		{name: "export", args: []string{"export", "out.json"}, run: cmdExport},
	}
	values := []struct {
		name string
		args []string
	}{
		{name: "missing", args: []string{"--project"}},
		{name: "blank", args: []string{"--project", " "}},
		{name: "flag", args: []string{"--project", "--all"}},
	}
	for _, command := range commands {
		for _, value := range values {
			t.Run(command.name+"/"+value.name, func(t *testing.T) {
				stubExitWithPanic(t)
				args := append([]string{"engram"}, command.args...)
				args = append(args, value.args...)
				withArgs(t, args...)
				_, stderr, recovered := captureOutputAndRecover(t, func() { command.run(testConfig(t)) })
				if _, ok := recovered.(exitCode); !ok {
					t.Fatalf("expected invalid --project to exit, got %v", recovered)
				}
				if !strings.Contains(stderr, "--project requires a non-empty value") {
					t.Fatalf("invalid --project error = %q", stderr)
				}
			})
		}
	}
}

func TestProjectScopedCLIReadFamiliesUseCurrentAndExplicitAll(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	t.Setenv("ENGRAM_PROJECT", "alpha")
	cfg := testConfig(t)
	alphaID := mustSeedObservation(t, cfg, "scope-alpha", "alpha", "note", "alpha scoped", "shared scoped search", "project")
	betaID := mustSeedObservation(t, cfg, "scope-beta", "beta", "note", "beta scoped", "shared scoped search", "project")

	withArgs(t, "engram", "search", "shared", "--limit", "10")
	current, stderr := captureOutput(t, func() { cmdSearch(cfg) })
	if stderr != "" || !strings.Contains(current, "alpha scoped") || strings.Contains(current, "beta scoped") {
		t.Fatalf("current search must be alpha-only: stdout=%q stderr=%q", current, stderr)
	}
	withArgs(t, "engram", "search", "shared", "--all", "--limit", "10")
	all, stderr := captureOutput(t, func() { cmdSearch(cfg) })
	if stderr != "" || !strings.Contains(all, "alpha scoped") || !strings.Contains(all, "beta scoped") {
		t.Fatalf("explicit all search must include both projects: stdout=%q stderr=%q", all, stderr)
	}

	withArgs(t, "engram", "timeline", strconv.FormatInt(betaID, 10))
	stubExitWithPanic(t)
	_, _, recovered := captureOutputAndRecover(t, func() { cmdTimeline(cfg) })
	if recovered == nil {
		t.Fatal("current timeline must reject an observation from another project")
	}
	withArgs(t, "engram", "timeline", strconv.FormatInt(betaID, 10), "--all")
	timeline, stderr := captureOutput(t, func() { cmdTimeline(cfg) })
	if stderr != "" || !strings.Contains(timeline, "beta scoped") {
		t.Fatalf("all timeline must include beta observation: stdout=%q stderr=%q", timeline, stderr)
	}

	withArgs(t, "engram", "stats")
	stats, stderr := captureOutput(t, func() { cmdStats(cfg) })
	if stderr != "" || !strings.Contains(stats, "Observations: 1") || strings.Contains(stats, "beta") {
		t.Fatalf("current stats must be alpha-only: stdout=%q stderr=%q", stats, stderr)
	}
	withArgs(t, "engram", "stats", "--all")
	stats, stderr = captureOutput(t, func() { cmdStats(cfg) })
	if stderr != "" || !strings.Contains(stats, "Observations: 2") || !strings.Contains(stats, "beta") {
		t.Fatalf("all stats must include both projects: stdout=%q stderr=%q", stats, stderr)
	}

	withArgs(t, "engram", "context")
	contextCurrent, stderr := captureOutput(t, func() { cmdContext(cfg) })
	if stderr != "" || !strings.Contains(contextCurrent, "alpha scoped") || strings.Contains(contextCurrent, "beta scoped") {
		t.Fatalf("current context must be alpha-only: stdout=%q stderr=%q", contextCurrent, stderr)
	}
	withArgs(t, "engram", "context", "--project", "beta")
	contextExplicit, stderr := captureOutput(t, func() { cmdContext(cfg) })
	if stderr != "" || !strings.Contains(contextExplicit, "beta scoped") || strings.Contains(contextExplicit, "alpha scoped") {
		t.Fatalf("explicit context must be beta-only: stdout=%q stderr=%q", contextExplicit, stderr)
	}
	withArgs(t, "engram", "context", "beta")
	contextLegacy, stderr := captureOutput(t, func() { cmdContext(cfg) })
	if stderr != "" || !strings.Contains(contextLegacy, "beta scoped") || strings.Contains(contextLegacy, "alpha scoped") {
		t.Fatalf("legacy positional context must remain beta-only: stdout=%q stderr=%q", contextLegacy, stderr)
	}
	withArgs(t, "engram", "context", "--all")
	contextAll, stderr := captureOutput(t, func() { cmdContext(cfg) })
	if stderr != "" || !strings.Contains(contextAll, "alpha scoped") || !strings.Contains(contextAll, "beta scoped") {
		t.Fatalf("all context must include both projects: stdout=%q stderr=%q", contextAll, stderr)
	}

	currentExport := filepath.Join(workDir, "current.json")
	withArgs(t, "engram", "export", currentExport)
	_, stderr = captureOutput(t, func() { cmdExport(cfg) })
	if stderr != "" {
		t.Fatalf("current export stderr: %q", stderr)
	}
	var currentData store.ExportData
	bytes, err := os.ReadFile(currentExport)
	if err != nil || json.Unmarshal(bytes, &currentData) != nil || len(currentData.Observations) != 1 || currentData.Observations[0].ID != alphaID {
		t.Fatalf("current export must contain only alpha observation: err=%v data=%#v", err, currentData)
	}
	allExport := filepath.Join(workDir, "all.json")
	withArgs(t, "engram", "export", allExport, "--all")
	_, stderr = captureOutput(t, func() { cmdExport(cfg) })
	if stderr != "" {
		t.Fatalf("all export stderr: %q", stderr)
	}
	bytes, err = os.ReadFile(allExport)
	var allData store.ExportData
	if err != nil || json.Unmarshal(bytes, &allData) != nil || len(allData.Observations) != 2 {
		t.Fatalf("all export must contain both observations: err=%v data=%#v", err, allData)
	}

	vault := t.TempDir()
	withArgs(t, "engram", "obsidian-export", "--vault", vault)
	_, stderr = captureOutput(t, func() { cmdObsidianExport(cfg) })
	if stderr != "" {
		t.Fatalf("current Obsidian export stderr: %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(vault, "engram", "alpha")); err != nil {
		t.Fatalf("current Obsidian export missing alpha data: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "engram", "beta")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current Obsidian export must not write beta data: %v", err)
	}
}

func TestCmdContextRejectsConflictingProjectSelectors(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "context-selectors", "alpha", "note", "alpha", "content", "project")
	stubExitWithPanic(t)

	for _, args := range [][]string{
		{"engram", "context", "alpha", "--project", "alpha"},
		{"engram", "context", "--project", "alpha", "--all"},
		{"engram", "context", "alpha", "beta"},
		{"engram", "context", "--project", "alpha", "--project", "alpha"},
	} {
		withArgs(t, args...)
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdContext(cfg) })
		if recovered == nil || !strings.Contains(stderr, "project selector") {
			t.Fatalf("args %v must reject conflicting selectors: stderr=%q panic=%v", args, stderr, recovered)
		}
	}
}

// ─── Projects command tests ───────────────────────────────────────────────────

func TestCmdProjectsListEmpty(t *testing.T) {
	cfg := testConfig(t)

	withArgs(t, "engram", "projects", "list")
	stdout, stderr := captureOutput(t, func() { cmdProjectsList(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "No projects found") {
		t.Fatalf("expected empty projects message, got: %q", stdout)
	}
}

func TestCmdProjectsList(t *testing.T) {
	cfg := testConfig(t)

	// Seed observations for two projects
	mustSeedObservation(t, cfg, "s-alpha", "alpha", "note", "alpha-note", "alpha content", "project")
	mustSeedObservation(t, cfg, "s-alpha", "alpha", "bugfix", "alpha-bug", "alpha bug", "project")
	mustSeedObservation(t, cfg, "s-beta", "beta", "decision", "beta-note", "beta content", "project")

	withArgs(t, "engram", "projects", "list")
	stdout, stderr := captureOutput(t, func() { cmdProjectsList(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "Projects (2)") {
		t.Fatalf("expected 'Projects (2)', got: %q", stdout)
	}
	if !strings.Contains(stdout, "alpha") || !strings.Contains(stdout, "beta") {
		t.Fatalf("expected project names in output, got: %q", stdout)
	}
	// alpha has 2 observations, beta has 1 — alpha should appear first
	alphaIdx := strings.Index(stdout, "alpha")
	betaIdx := strings.Index(stdout, "beta")
	if alphaIdx > betaIdx {
		t.Fatalf("expected alpha (more obs) before beta, got: %q", stdout)
	}
}

func TestCmdProjectsRoutesSubcommands(t *testing.T) {
	cfg := testConfig(t)

	// "list" subcommand
	withArgs(t, "engram", "projects", "list")
	stdout, _ := captureOutput(t, func() { cmdProjects(cfg) })
	if !strings.Contains(stdout, "No projects found") && !strings.Contains(stdout, "Projects") {
		t.Fatalf("expected projects list output, got: %q", stdout)
	}

	// default (no subcommand) → list
	withArgs(t, "engram", "projects")
	stdout2, _ := captureOutput(t, func() { cmdProjects(cfg) })
	_ = stdout2 // just checking it doesn't crash
}

// seedLegacyNullableSession builds the shape an upgraded database has: a
// sessions table whose project column is still nullable, carrying rows that
// identify no project.
func seedLegacyNullableSession(t *testing.T, cfg store.Config, sessionID string) {
	t.Helper()
	raw, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		project TEXT,
		directory TEXT NOT NULL,
		started_at TEXT NOT NULL DEFAULT (datetime('now')),
		ended_at TEXT,
		summary TEXT
	)`); err != nil {
		t.Fatalf("create legacy sessions: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, NULL, ?)`, sessionID, "/tmp"); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}
}

// The repair for an unowned session must be reachable in a zero-config install,
// where ENGRAM_HTTP_TOKEN is unset and the HTTP rescue endpoint answers 503.
// This CLI path talks to the store directly and never needs server auth.
func TestCmdProjectsRescueOwnershipWorksWithoutServerToken(t *testing.T) {
	cfg := testConfig(t)
	seedLegacyNullableSession(t, cfg, "legacy-session")
	t.Setenv("ENGRAM_HTTP_TOKEN", "")

	withArgs(t, "engram", "projects", "rescue-ownership", "--project", "target", "--session", "legacy-session")
	stdout, stderr := captureOutput(t, func() { cmdProjects(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "target") {
		t.Fatalf("expected the target project in output, got: %q", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	sess, err := s.GetSession("legacy-session")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Project != "target" {
		t.Fatalf("session project = %q, want target", sess.Project)
	}
}

// A partial rescue must say exactly what it left behind, not just a counter.
func TestCmdProjectsRescueOwnershipReportsWhatWasLeftBehind(t *testing.T) {
	cfg := testConfig(t)
	seedLegacyNullableSession(t, cfg, "legacy-session")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope) VALUES ('obs-foreign', 'legacy-session', 'note', 'foreign', 'content', 'other', 'project')`,
	); err != nil {
		t.Fatalf("seed foreign-owned observation: %v", err)
	}
	s.Close()

	withArgs(t, "engram", "projects", "rescue-ownership", "--project", "target", "--session", "legacy-session")
	stdout, _ := captureOutput(t, func() { cmdProjects(cfg) })
	if !strings.Contains(stdout, "left behind") {
		t.Fatalf("expected the partial outcome to be named, got: %q", stdout)
	}
	if !strings.Contains(stdout, "legacy-session") {
		t.Fatalf("expected the blocked session to be listed, got: %q", stdout)
	}
}

func TestCmdProjectsRescueOwnershipRequiresScope(t *testing.T) {
	cfg := testConfig(t)

	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "projects", "rescue-ownership", "--project", "target")
	_, stderr := captureOutput(t, func() { cmdProjects(cfg) })
	if !strings.Contains(stderr, "usage:") {
		t.Fatalf("expected usage on missing scope, got: %q", stderr)
	}
	if !exited {
		t.Fatal("expected a non-zero exit when no records are selected")
	}
}

func TestCmdProjectsConsolidateNoSimilar(t *testing.T) {
	cfg := testConfig(t)

	// Seed a single unique project
	mustSeedObservation(t, cfg, "s-unique", "unique-project", "note", "unique note", "content", "project")

	// Set cwd to a temp dir named "unique-project" with no git
	workDir := filepath.Join(t.TempDir(), "unique-project")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	withCwd(t, workDir)

	// Stub detectProject to return the known canonical
	old := detectProject
	detectProject = func(string) string { return "unique-project" }
	t.Cleanup(func() { detectProject = old })

	withArgs(t, "engram", "projects", "consolidate")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "No similar") {
		t.Fatalf("expected no-similar message, got: %q", stdout)
	}
}

func TestCmdProjectsConsolidateRejectsWeakCandidates(t *testing.T) {
	tests := []struct {
		name      string
		canonical string
		candidate string
	}{
		{name: "shared directory", canonical: "alpha", candidate: "beta"},
		{name: "substring", canonical: "engram", candidate: "engram-memory"},
		{name: "levenshtein", canonical: "engram", candidate: "engramm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			mustSeedObservation(t, cfg, "s-canonical", tt.canonical, "note", "canonical", "content", "project")
			mustSeedObservation(t, cfg, "s-candidate", tt.candidate, "note", "candidate", "content", "project")

			old := detectProject
			detectProject = func(string) string { return tt.canonical }
			t.Cleanup(func() { detectProject = old })

			withArgs(t, "engram", "projects", "consolidate")
			stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
			if stderr != "" {
				t.Fatalf("expected no stderr, got: %q", stderr)
			}
			if !strings.Contains(stdout, "No similar") {
				t.Fatalf("expected no-candidate message, got: %q", stdout)
			}
		})
	}
}

func TestCmdProjectsConsolidateDryRun(t *testing.T) {
	cfg := testConfig(t)

	// Seed a canonical name and rewrite a second project's records as a legacy case variant.
	mustSeedObservation(t, cfg, "s-eng", "engram", "note", "eng note", "content", "project")
	mustSeedObservation(t, cfg, "s-legacy", "legacy-source", "note", "legacy note", "content", "project")
	rewriteLegacyProjectName(t, cfg, "legacy-source", "ENGRAM")

	old := detectProject
	detectProject = func(string) string { return "engram" }
	t.Cleanup(func() { detectProject = old })

	withArgs(t, "engram", "projects", "consolidate", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "dry-run") {
		t.Fatalf("expected dry-run message, got: %q", stdout)
	}
	// Verify no actual merge happened (both project names still exist).
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	names, err := s.ListProjectNames()
	if err != nil {
		t.Fatalf("ListProjectNames: %v", err)
	}
	// Should still have both names (no merge happened)
	if len(names) != 2 || names[0] != "ENGRAM" || names[1] != "engram" {
		t.Fatalf("expected legacy and canonical names after dry-run, got: %v", names)
	}
}

func TestCmdProjectsConsolidateSingleProject(t *testing.T) {
	cfg := testConfig(t)

	// Seed canonical and normalization-equivalent legacy records.
	mustSeedObservation(t, cfg, "s-eng", "engram", "note", "eng note", "content", "project")
	mustSeedObservation(t, cfg, "s-legacy", "legacy-source", "note", "legacy note", "content", "project")
	rewriteLegacyProjectName(t, cfg, "legacy-source", "ENGRAM")

	old := detectProject
	detectProject = func(string) string { return "engram" }
	t.Cleanup(func() { detectProject = old })

	// Stub scanInputLine to answer "all"
	oldScan := scanInputLine
	t.Cleanup(func() { scanInputLine = oldScan })
	scanInputLine = func(a ...any) (int, error) {
		if ptr, ok := a[0].(*string); ok {
			*ptr = "all"
		}
		return 1, nil
	}

	withArgs(t, "engram", "projects", "consolidate")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, `Merged 1 project(s) into "engram"`) {
		t.Fatalf("expected merge result, got: %q", stdout)
	}

	// Verify the legacy variant was merged into engram.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	names, err := s.ListProjectNames()
	if err != nil {
		t.Fatalf("ListProjectNames: %v", err)
	}
	if len(names) != 1 || names[0] != "engram" {
		t.Fatalf("expected only 'engram' after merge, got: %v", names)
	}
}

func TestCmdProjectsConsolidateAllDryRun(t *testing.T) {
	cfg := testConfig(t)

	// Seed normalization-equivalent legacy project records.
	mustSeedObservation(t, cfg, "s-eng", "engram", "note", "eng note", "content", "project")
	mustSeedObservation(t, cfg, "s-legacy", "legacy-source", "note", "legacy note", "content", "project")
	rewriteLegacyProjectName(t, cfg, "legacy-source", "ENGRAM")

	withArgs(t, "engram", "projects", "consolidate", "--all", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "dry-run") || !strings.Contains(stdout, "Group") {
		t.Fatalf("expected dry-run group output, got: %q", stdout)
	}
	if !strings.Contains(stdout, `Would merge into "engram"`) {
		t.Fatalf("expected normalized canonical in dry-run output, got: %q", stdout)
	}
}

func TestCmdProjectsPrunePathsOnlyDryRun(t *testing.T) {
	cfg := testConfig(t)
	pathProject := `c:\workspace\orphan`
	mustSeedSession(t, cfg, "s-path", pathProject)
	mustSeedSession(t, cfg, "s-ordinary", "ordinary-empty")
	mustSeedObservation(t, cfg, "s-active", "active-project", "note", "active", "content", "project")

	withArgs(t, "engram", "projects", "prune", "--paths-only", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdProjectsPrune(cfg) })
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, pathProject) || strings.Contains(stdout, "ordinary-empty") || strings.Contains(stdout, "active-project") {
		t.Fatalf("paths-only candidates = %q", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	stats, err := s.ListProjectsWithStats()
	if err != nil {
		t.Fatalf("ListProjectsWithStats: %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("dry-run mutated projects: %+v", stats)
	}
}

func TestCmdProjectsPrunePathsOnly(t *testing.T) {
	cfg := testConfig(t)
	forwardSlashProject := "/tmp/orphan"
	backslashProject := `c:\workspace\orphan`
	mustSeedPrompt(t, cfg, "s-forward-slash", forwardSlashProject)
	mustSeedPrompt(t, cfg, "s-backslash", backslashProject)
	mustSeedSession(t, cfg, "s-ordinary", "ordinary-empty")
	mustSeedObservation(t, cfg, "s-active", "active-project", "note", "active", "content", "project")

	oldScan := scanInputLine
	scanInputLine = func(a ...any) (int, error) {
		*a[0].(*string) = "all"
		return 1, nil
	}
	t.Cleanup(func() { scanInputLine = oldScan })

	withArgs(t, "engram", "projects", "prune", "--paths-only")
	stdout, stderr := captureOutput(t, func() { cmdProjectsPrune(cfg) })
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	for _, project := range []string{forwardSlashProject, backslashProject} {
		if !strings.Contains(stdout, project) {
			t.Fatalf("paths-only output missing %q: %q", project, stdout)
		}
	}
	if strings.Contains(stdout, "ordinary-empty") || strings.Contains(stdout, "active-project") {
		t.Fatalf("paths-only output included a retained project: %q", stdout)
	}
	if !strings.Contains(stdout, "Pruned 2 project(s): 2 sessions, 2 prompts removed.") {
		t.Fatalf("prune result = %q", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	for _, sessionID := range []string{"s-forward-slash", "s-backslash"} {
		if _, err := s.GetSession(sessionID); err == nil {
			t.Fatalf("pruned session %q still exists", sessionID)
		}
	}
	stats, err := s.ListProjectsWithStats()
	if err != nil {
		t.Fatalf("ListProjectsWithStats: %v", err)
	}
	remaining := make(map[string]store.ProjectStats, len(stats))
	for _, ps := range stats {
		remaining[ps.Name] = ps
	}
	if _, ok := remaining[forwardSlashProject]; ok {
		t.Fatalf("pruned project %q still has data: %+v", forwardSlashProject, remaining[forwardSlashProject])
	}
	if _, ok := remaining[backslashProject]; ok {
		t.Fatalf("pruned project %q still has data: %+v", backslashProject, remaining[backslashProject])
	}
	if ordinary, ok := remaining["ordinary-empty"]; !ok || ordinary.SessionCount != 1 {
		t.Fatalf("ordinary empty project = %+v, want one retained session", ordinary)
	}
	if active, ok := remaining["active-project"]; !ok || active.ObservationCount != 1 || active.SessionCount != 1 {
		t.Fatalf("active project = %+v, want one retained observation and session", active)
	}
}

func TestCmdProjectsPruneWithoutPathsOnlyKeepsOrdinaryBehavior(t *testing.T) {
	cfg := testConfig(t)
	mustSeedSession(t, cfg, "s-path", `c:\workspace\orphan`)
	mustSeedSession(t, cfg, "s-ordinary", "ordinary-empty")

	withArgs(t, "engram", "projects", "prune", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdProjectsPrune(cfg) })
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, `c:\workspace\orphan`) || !strings.Contains(stdout, "ordinary-empty") {
		t.Fatalf("ordinary prune candidates = %q", stdout)
	}
}

func TestCmdProjectsPruneReportsOnlySuccessfulProjects(t *testing.T) {
	cfg := testConfig(t)
	mustSeedSession(t, cfg, "s-success", "success-empty")
	mustSeedSession(t, cfg, "s-failure", "failure-empty")

	oldPrune := storePruneProject
	storePruneProject = func(s *store.Store, project string) (*store.PruneResult, error) {
		if project == "failure-empty" {
			return nil, errors.New("forced failure")
		}
		return oldPrune(s, project)
	}
	t.Cleanup(func() { storePruneProject = oldPrune })
	oldScan := scanInputLine
	scanInputLine = func(a ...any) (int, error) {
		*a[0].(*string) = "all"
		return 1, nil
	}
	t.Cleanup(func() { scanInputLine = oldScan })

	withArgs(t, "engram", "projects", "prune")
	stdout, stderr := captureOutput(t, func() { cmdProjectsPrune(cfg) })
	if !strings.Contains(stderr, `Error pruning "failure-empty": forced failure`) {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, "Pruned 1 project(s): 1 sessions, 0 prompts removed.") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestCmdProjectsConsolidateAllRenameMigratesMergedIdentity(t *testing.T) {
	cfg := testConfig(t)

	// Seed a canonical project and a normalization-equivalent legacy variant.
	mustSeedObservation(t, cfg, "s-eng", "engram", "note", "eng note", "content", "project")
	mustSeedObservation(t, cfg, "s-legacy", "legacy-source", "note", "legacy note", "content", "project")
	rewriteLegacyProjectName(t, cfg, "legacy-source", "ENGRAM")

	// Answer "rename" first, then provide the new canonical name.
	answers := []string{"rename", "Engram Core"}
	oldScan := scanInputLine
	t.Cleanup(func() { scanInputLine = oldScan })
	scanInputLine = func(a ...any) (int, error) {
		answer := ""
		if len(answers) > 0 {
			answer = answers[0]
			answers = answers[1:]
		}
		if ptr, ok := a[0].(*string); ok {
			*ptr = answer
		}
		return 1, nil
	}

	withArgs(t, "engram", "projects", "consolidate", "--all")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "Merged") {
		t.Fatalf("expected merge output, got: %q", stdout)
	}
	if !strings.Contains(stdout, `"engram core"`) {
		t.Fatalf("expected rename output mentioning new canonical, got: %q", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	names, err := s.ListProjectNames()
	if err != nil {
		t.Fatalf("ListProjectNames: %v", err)
	}
	if len(names) != 1 || names[0] != "engram core" {
		t.Fatalf("expected all records under renamed canonical, got: %v", names)
	}
}

func TestCmdProjectsAllRejectsWeakAndTransitiveGroups(t *testing.T) {
	cfg := testConfig(t)

	// These names form substring and Levenshtein weak edges and all share /tmp.
	mustSeedObservation(t, cfg, "s-client", "client", "note", "client", "content", "project")
	mustSeedObservation(t, cfg, "s-client-api", "client-api", "note", "client api", "content", "project")
	mustSeedObservation(t, cfg, "s-client-apj", "client-apj", "note", "client apj", "content", "project")

	withArgs(t, "engram", "projects", "consolidate", "--all")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "No similar") {
		t.Fatalf("expected no weak-edge group, got: %q", stdout)
	}
}

func TestGroupSimilarProjectsUsesNormalizationEquivalenceAndNormalizedCanonical(t *testing.T) {
	groups := groupSimilarProjects([]store.ProjectStats{
		{Name: "engram", ObservationCount: 1, Directories: []string{"/shared"}},
		{Name: "ENGRAM", ObservationCount: 1, Directories: []string{"/shared"}},
		{Name: "engram-memory", ObservationCount: 100, Directories: []string{"/shared"}},
	})

	if len(groups) != 1 {
		t.Fatalf("expected one normalization-equivalent group, got: %#v", groups)
	}
	if got, want := groups[0].Names, []string{"ENGRAM", "engram"}; !slices.Equal(got, want) {
		t.Fatalf("group names = %v, want %v", got, want)
	}
	if groups[0].Canonical != "engram" {
		t.Fatalf("canonical = %q, want normalized group key %q", groups[0].Canonical, "engram")
	}
}

// projectRecordCounts reports how many observations, sessions and prompts are
// stored under an exact project spelling, so tests can compare the counts the
// CLI printed against the records that actually moved.
func projectRecordCounts(t *testing.T, cfg store.Config, project string) (observations, sessions, prompts int) {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	queries := []struct {
		query string
		dest  *int
	}{
		{`SELECT COUNT(*) FROM observations WHERE project = ? AND deleted_at IS NULL`, &observations},
		{`SELECT COUNT(*) FROM sessions WHERE project = ?`, &sessions},
		{`SELECT COUNT(*) FROM user_prompts WHERE project = ?`, &prompts},
	}
	for _, q := range queries {
		if err := db.QueryRow(q.query, project).Scan(q.dest); err != nil {
			t.Fatalf("count %q rows: %v", project, err)
		}
	}
	return observations, sessions, prompts
}

func TestCmdProjectsConsolidateCaseOnlyVariantReportsMovedRecords(t *testing.T) {
	cfg := testConfig(t)

	// A case-only legacy spelling must actually move its records, and the
	// printed counts must match what moved.
	mustSeedObservation(t, cfg, "s-eng", "engram", "note", "eng note", "content", "project")
	mustSeedObservation(t, cfg, "s-legacy", "legacy-source", "note", "legacy note", "content", "project")
	mustSeedPrompt(t, cfg, "s-legacy", "legacy-source")
	rewriteLegacyProjectName(t, cfg, "legacy-source", "ENGRAM")

	old := detectProject
	detectProject = func(string) string { return "engram" }
	t.Cleanup(func() { detectProject = old })

	oldScan := scanInputLine
	t.Cleanup(func() { scanInputLine = oldScan })
	scanInputLine = func(a ...any) (int, error) {
		if ptr, ok := a[0].(*string); ok {
			*ptr = "all"
		}
		return 1, nil
	}

	withArgs(t, "engram", "projects", "consolidate")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}

	for _, want := range []string{
		`Done! Merged 1 project(s) into "engram"`,
		"Observations: 1",
		"Sessions:     1",
		"Prompts:      1",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in merge report, got: %q", want, stdout)
		}
	}

	// The reported counts must match the records that actually moved.
	if obs, sessions, prompts := projectRecordCounts(t, cfg, "ENGRAM"); obs+sessions+prompts != 0 {
		t.Fatalf("legacy spelling still holds records: obs=%d sessions=%d prompts=%d", obs, sessions, prompts)
	}
	obs, sessions, prompts := projectRecordCounts(t, cfg, "engram")
	if obs != 2 || sessions != 2 || prompts != 1 {
		t.Fatalf("canonical records = obs:%d sessions:%d prompts:%d, want 2/2/1", obs, sessions, prompts)
	}
}

func TestCmdProjectsConsolidateReportsNothingMergedWhenNoRecordsMove(t *testing.T) {
	cfg := testConfig(t)

	// " engram " normalizes to the canonical name, so it is offered as a
	// candidate, but the store fail-closes on it because its trimmed spelling
	// is the canonical name itself. The CLI must not announce completion.
	mustSeedObservation(t, cfg, "s-legacy", "legacy-source", "note", "legacy note", "content", "project")
	rewriteLegacyProjectName(t, cfg, "legacy-source", " engram ")

	old := detectProject
	detectProject = func(string) string { return "engram" }
	t.Cleanup(func() { detectProject = old })

	oldScan := scanInputLine
	t.Cleanup(func() { scanInputLine = oldScan })
	scanInputLine = func(a ...any) (int, error) {
		if ptr, ok := a[0].(*string); ok {
			*ptr = "all"
		}
		return 1, nil
	}

	withArgs(t, "engram", "projects", "consolidate")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if strings.Contains(stdout, "Done!") {
		t.Fatalf("completion reported without moving records: %q", stdout)
	}
	if !strings.Contains(stdout, "Nothing merged") {
		t.Fatalf("expected an honest no-op report, got: %q", stdout)
	}

	// The records must still be reachable under their original spelling.
	if obs, sessions, _ := projectRecordCounts(t, cfg, " engram "); obs != 1 || sessions != 1 {
		t.Fatalf("legacy records lost: obs=%d sessions=%d", obs, sessions)
	}
}

func TestCmdProjectsConsolidateAllCaseOnlyVariantReportsMovedRecords(t *testing.T) {
	cfg := testConfig(t)

	mustSeedObservation(t, cfg, "s-eng", "engram", "note", "eng note", "content", "project")
	mustSeedObservation(t, cfg, "s-legacy", "legacy-source", "note", "legacy note", "content", "project")
	mustSeedPrompt(t, cfg, "s-legacy", "legacy-source")
	rewriteLegacyProjectName(t, cfg, "legacy-source", "ENGRAM")

	oldScan := scanInputLine
	t.Cleanup(func() { scanInputLine = oldScan })
	scanInputLine = func(a ...any) (int, error) {
		if ptr, ok := a[0].(*string); ok {
			*ptr = "all"
		}
		return 1, nil
	}

	withArgs(t, "engram", "projects", "consolidate", "--all")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "Merged: 1 obs, 1 sessions, 1 prompts") {
		t.Fatalf("expected counts matching the moved records, got: %q", stdout)
	}
	if obs, sessions, prompts := projectRecordCounts(t, cfg, "ENGRAM"); obs+sessions+prompts != 0 {
		t.Fatalf("legacy spelling still holds records: obs=%d sessions=%d prompts=%d", obs, sessions, prompts)
	}
	obs, sessions, prompts := projectRecordCounts(t, cfg, "engram")
	if obs != 2 || sessions != 2 || prompts != 1 {
		t.Fatalf("canonical records = obs:%d sessions:%d prompts:%d, want 2/2/1", obs, sessions, prompts)
	}
}

func TestCmdProjectsConsolidateAllReportsNothingMergedWhenNoRecordsMove(t *testing.T) {
	cfg := testConfig(t)

	mustSeedObservation(t, cfg, "s-eng", "engram", "note", "eng note", "content", "project")
	mustSeedObservation(t, cfg, "s-legacy", "legacy-source", "note", "legacy note", "content", "project")
	rewriteLegacyProjectName(t, cfg, "legacy-source", " engram ")

	oldScan := scanInputLine
	t.Cleanup(func() { scanInputLine = oldScan })
	scanInputLine = func(a ...any) (int, error) {
		if ptr, ok := a[0].(*string); ok {
			*ptr = "all"
		}
		return 1, nil
	}

	withArgs(t, "engram", "projects", "consolidate", "--all")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if strings.Contains(stdout, "Merged:") {
		t.Fatalf("merge reported without moving records: %q", stdout)
	}
	if !strings.Contains(stdout, "Nothing merged") {
		t.Fatalf("expected an honest no-op report, got: %q", stdout)
	}
	if obs, sessions, _ := projectRecordCounts(t, cfg, " engram "); obs != 1 || sessions != 1 {
		t.Fatalf("legacy records lost: obs=%d sessions=%d", obs, sessions)
	}
}

func TestCmdProjectsConsolidateAllNamesSourcesTheStoreLeftUntouched(t *testing.T) {
	cfg := testConfig(t)

	// "ENGRAM" moves; " engram " is fail-closed by the store because its
	// trimmed spelling is the canonical name. A partial merge must say so.
	mustSeedObservation(t, cfg, "s-eng", "engram", "note", "eng note", "content", "project")
	mustSeedObservation(t, cfg, "s-upper", "upper-source", "note", "upper note", "content", "project")
	mustSeedObservation(t, cfg, "s-padded", "padded-source", "note", "padded note", "content", "project")
	rewriteLegacyProjectName(t, cfg, "upper-source", "ENGRAM")
	rewriteLegacyProjectName(t, cfg, "padded-source", " engram ")

	oldScan := scanInputLine
	t.Cleanup(func() { scanInputLine = oldScan })
	scanInputLine = func(a ...any) (int, error) {
		if ptr, ok := a[0].(*string); ok {
			*ptr = "all"
		}
		return 1, nil
	}

	withArgs(t, "engram", "projects", "consolidate", "--all")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "Merged: 1 obs, 1 sessions, 0 prompts") {
		t.Fatalf("expected counts for the single moved source, got: %q", stdout)
	}
	if !strings.Contains(stdout, "Not merged (no records moved):  engram ") {
		t.Fatalf("expected the untouched source to be named, got: %q", stdout)
	}
	if obs, sessions, _ := projectRecordCounts(t, cfg, " engram "); obs != 1 || sessions != 1 {
		t.Fatalf("untouched source lost records: obs=%d sessions=%d", obs, sessions)
	}
}

func TestCmdProjectsConsolidateLeavesFuzzyMatchesUnmerged(t *testing.T) {
	// Substring and Levenshtein neighbours are not normalization-equivalent, so
	// neither cleanup route may merge them or touch their records.
	tests := []struct {
		name      string
		canonical string
		candidate string
	}{
		{name: "substring", canonical: "engram", candidate: "engram-memory"},
		{name: "levenshtein", canonical: "engram", candidate: "engramm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, args := range [][]string{
				{"engram", "projects", "consolidate"},
				{"engram", "projects", "consolidate", "--all"},
			} {
				cfg := testConfig(t)
				mustSeedObservation(t, cfg, "s-canonical", tt.canonical, "note", "canonical", "content", "project")
				mustSeedObservation(t, cfg, "s-candidate", tt.candidate, "note", "candidate", "content", "project")

				old := detectProject
				detectProject = func(string) string { return tt.canonical }
				t.Cleanup(func() { detectProject = old })

				oldScan := scanInputLine
				t.Cleanup(func() { scanInputLine = oldScan })
				scanInputLine = func(a ...any) (int, error) {
					if ptr, ok := a[0].(*string); ok {
						*ptr = "all"
					}
					return 1, nil
				}

				withArgs(t, args...)
				stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
				if stderr != "" {
					t.Fatalf("%v: expected no stderr, got: %q", args, stderr)
				}
				if !strings.Contains(stdout, "No similar") {
					t.Fatalf("%v: fuzzy candidate offered for merge: %q", args, stdout)
				}
				for _, project := range []string{tt.canonical, tt.candidate} {
					if obs, sessions, _ := projectRecordCounts(t, cfg, project); obs != 1 || sessions != 1 {
						t.Fatalf("%v: %q records changed: obs=%d sessions=%d", args, project, obs, sessions)
					}
				}
			}
		})
	}
}

func TestCmdMCPDetectsProjectFromFlag(t *testing.T) {
	cfg := testConfig(t)

	var capturedCfg mcp.MCPConfig
	oldNew := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNew })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		capturedCfg = mcpCfg
		// Return a valid server so serveMCP doesn't panic
		return oldNew(s, mcpCfg, allowlist)
	}

	oldServe := serveMCP
	t.Cleanup(func() { serveMCP = oldServe })
	// Prevent actual stdio serve — return immediately
	serveMCP = func(srv *mcpserver.MCPServer, opts ...mcpserver.StdioOption) error {
		return nil
	}

	withArgs(t, "engram", "mcp", "--project=myproject")
	_, _ = captureOutput(t, func() { cmdMCP(cfg) })

	if capturedCfg.DefaultProject != "myproject" {
		t.Fatalf("DefaultProject = %q; want myproject", capturedCfg.DefaultProject)
	}
}

func TestCmdMCPDetectsProjectFromEnv(t *testing.T) {
	cfg := testConfig(t)

	t.Setenv("ENGRAM_PROJECT", "env-project")

	var capturedCfg mcp.MCPConfig
	oldNew := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNew })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		capturedCfg = mcpCfg
		return oldNew(s, mcpCfg, allowlist)
	}

	oldServe := serveMCP
	t.Cleanup(func() { serveMCP = oldServe })
	serveMCP = func(srv *mcpserver.MCPServer, opts ...mcpserver.StdioOption) error {
		return nil
	}

	withArgs(t, "engram", "mcp")
	_, _ = captureOutput(t, func() { cmdMCP(cfg) })

	if capturedCfg.DefaultProject != "env-project" {
		t.Fatalf("DefaultProject = %q; want env-project", capturedCfg.DefaultProject)
	}
}

func TestCmdMCPDetectsProjectFromGit(t *testing.T) {
	cfg := testConfig(t)

	// Stub detectProject to simulate git detection
	old := detectProject
	t.Cleanup(func() { detectProject = old })
	detectProject = func(string) string { return "detected-from-git" }

	var capturedCfg mcp.MCPConfig
	oldNew := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNew })
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		capturedCfg = mcpCfg
		return oldNew(s, mcpCfg, allowlist)
	}

	oldServe := serveMCP
	t.Cleanup(func() { serveMCP = oldServe })
	serveMCP = func(srv *mcpserver.MCPServer, opts ...mcpserver.StdioOption) error {
		return nil
	}

	withArgs(t, "engram", "mcp")
	_, _ = captureOutput(t, func() { cmdMCP(cfg) })

	if capturedCfg.DefaultProject != "" {
		t.Fatalf("DefaultProject = %q; want empty without flag/env", capturedCfg.DefaultProject)
	}
}

func TestCmdMCPServesBeforeDeferredEnrolledProjectRepair(t *testing.T) {
	cfg := testConfig(t)
	seed, err := store.New(cfg)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := seed.CreateSession("legacy-session", "legacy-project", t.TempDir()); err != nil {
		_ = seed.Close()
		t.Fatalf("seed session: %v", err)
	}
	if err := seed.EnrollProject("legacy-project"); err != nil {
		_ = seed.Close()
		t.Fatalf("enroll project: %v", err)
	}
	if _, err := seed.DB().Exec(`DELETE FROM sync_mutations WHERE project = ?`, "legacy-project"); err != nil {
		_ = seed.Close()
		t.Fatalf("remove journal entries: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	oldStoreNew := storeNew
	oldNewMCPServerWithConfig := newMCPServerWithConfig
	oldServeMCP := serveMCP
	t.Cleanup(func() {
		storeNew = oldStoreNew
		newMCPServerWithConfig = oldNewMCPServerWithConfig
		serveMCP = oldServeMCP
	})
	storeNew = store.New

	var mcpStore *store.Store
	newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		mcpStore = s
		return oldNewMCPServerWithConfig(s, mcpCfg, allowlist)
	}
	serveMCP = func(_ *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error {
		if mcpStore == nil {
			return errors.New("MCP server did not receive a store")
		}
		var mutations int
		if err := mcpStore.DB().QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = ?`, "legacy-project").Scan(&mutations); err != nil {
			return fmt.Errorf("count journal entries at MCP readiness: %w", err)
		}
		if mutations != 0 {
			return fmt.Errorf("MCP readiness ran deferred repair: got %d mutations", mutations)
		}
		return nil
	}

	withArgs(t, "engram", "mcp")
	_, stderr := captureOutput(t, func() { cmdMCP(cfg) })
	if stderr != "" {
		t.Fatalf("MCP startup stderr = %q", stderr)
	}
}

func TestCmdSyncUsesDetectProject(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)

	cfg := testConfig(t)

	// Stub detectProject to verify it's called instead of filepath.Base
	old := detectProject
	t.Cleanup(func() { detectProject = old })
	detectProject = func(dir string) string { return "git-detected-project" }

	withArgs(t, "engram", "sync")
	stdout, stderr := captureOutput(t, func() { cmdSync(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "git-detected-project") {
		t.Fatalf("expected detectProject result in output, got: %q", stdout)
	}
}

// ─── obsidian-export command tests ───────────────────────────────────────────

// TestObsidianExportMissingVault verifies that omitting --vault exits with code 1
// and prints an error message to stderr (REQ-EXPORT-01: missing --vault scenario).
func TestObsidianExportMissingVault(t *testing.T) {
	cfg := testConfig(t)

	var exitCode int
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })
	exitFunc = func(code int) { exitCode = code; panic("exit") }

	withArgs(t, "engram", "obsidian-export", "--project", "eng")

	// Capture stderr before the panic unwinds by closing pipes inside captureOutput.
	// We use a wrapper that recovers from the exitFunc panic and then still closes
	// the write-end pipes so ReadAll can drain them.
	oldOut := os.Stdout
	oldErr := os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdout = outW
	os.Stderr = errW

	func() {
		defer func() {
			recover() //nolint:errcheck
		}()
		cmdObsidianExport(cfg)
	}()

	_ = outW.Close()
	_ = errW.Close()
	os.Stdout = oldOut
	os.Stderr = oldErr

	errBytes, _ := io.ReadAll(errR)
	_, _ = io.ReadAll(outR)
	stderr := string(errBytes)

	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(stderr, "--vault") {
		t.Fatalf("expected '--vault' in stderr, got: %q", stderr)
	}
}

// TestObsidianExportCallsInjectedExporter verifies that when --vault is provided,
// the injected newObsidianExporter is called with the correct config
// (REQ-EXPORT-01: happy path with all flags).
func TestObsidianExportCallsInjectedExporter(t *testing.T) {
	cfg := testConfig(t)
	vaultDir := t.TempDir()
	mustSeedObservation(t, cfg, "obsidian-eng", "eng", "note", "Known project", "content", "project")

	// Track the ExportConfig passed to the injected constructor
	var capturedCfg obsidian.ExportConfig
	exporterCalled := false

	oldNew := newObsidianExporter
	t.Cleanup(func() { newObsidianExporter = oldNew })
	newObsidianExporter = func(s obsidian.StoreReader, c obsidian.ExportConfig) *obsidian.Exporter {
		capturedCfg = c
		exporterCalled = true
		return obsidian.NewExporter(s, c)
	}

	withArgs(t, "engram", "obsidian-export",
		"--vault", vaultDir,
		"--project", "eng",
		"--limit", "50",
		"--since", "2026-01-01",
	)

	_, _ = captureOutput(t, func() { cmdObsidianExport(cfg) })

	if !exporterCalled {
		t.Fatalf("expected newObsidianExporter to be called")
	}
	if capturedCfg.VaultPath != vaultDir {
		t.Fatalf("expected VaultPath=%q, got %q", vaultDir, capturedCfg.VaultPath)
	}
	if capturedCfg.Project != "eng" {
		t.Fatalf("expected Project=%q, got %q", "eng", capturedCfg.Project)
	}
	if capturedCfg.Limit != 50 {
		t.Fatalf("expected Limit=50, got %d", capturedCfg.Limit)
	}
	if capturedCfg.Since.IsZero() {
		t.Fatalf("expected Since to be set from --since 2026-01-01, got zero")
	}
}

// TestObsidianExportMinimalFlags verifies that --vault uses the current project.
func TestObsidianExportMinimalFlags(t *testing.T) {
	cfg := testConfig(t)
	vaultDir := t.TempDir()
	mustSeedObservation(t, cfg, "obsidian-default", "default-project", "note", "Known project", "content", "project")
	t.Setenv("ENGRAM_PROJECT", "default-project")

	var capturedCfg obsidian.ExportConfig
	oldNew := newObsidianExporter
	t.Cleanup(func() { newObsidianExporter = oldNew })
	newObsidianExporter = func(s obsidian.StoreReader, c obsidian.ExportConfig) *obsidian.Exporter {
		capturedCfg = c
		return obsidian.NewExporter(s, c)
	}

	withArgs(t, "engram", "obsidian-export", "--vault", vaultDir)

	_, _ = captureOutput(t, func() { cmdObsidianExport(cfg) })

	if capturedCfg.VaultPath != vaultDir {
		t.Fatalf("expected VaultPath=%q, got %q", vaultDir, capturedCfg.VaultPath)
	}
	if capturedCfg.Project != "default-project" {
		t.Fatalf("expected current project, got %q", capturedCfg.Project)
	}
	if capturedCfg.Limit != 0 {
		t.Fatalf("expected Limit=0, got %d", capturedCfg.Limit)
	}
	if !capturedCfg.Since.IsZero() {
		t.Fatalf("expected Since=zero, got %v", capturedCfg.Since)
	}
}

// TestObsidianExportInHelpText verifies that "obsidian-export" appears in printUsage output.
func TestObsidianExportInHelpText(t *testing.T) {
	stdout, _ := captureOutput(t, func() { printUsage() })
	if !strings.Contains(stdout, "obsidian-export") {
		t.Fatalf("expected 'obsidian-export' in help text, got: %q", stdout)
	}
}

// ─── obsidian-export Phase 4 tests (graph-config, watch, interval) ───────────

// captureExitPanic is a helper that runs fn inside a panic-recovering wrapper,
// captures stdout/stderr via os.Pipe, and returns the exit code (via exitFunc stub).
func captureExitPanic(t *testing.T, fn func()) (stdout, stderr string, exitCode int) {
	t.Helper()

	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })
	exitFunc = func(code int) { exitCode = code; panic("exit") }

	oldOut := os.Stdout
	oldErr := os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdout = outW
	os.Stderr = errW

	func() {
		defer func() { recover() }() //nolint:errcheck
		fn()
	}()

	_ = outW.Close()
	_ = errW.Close()
	os.Stdout = oldOut
	os.Stderr = oldErr

	outBytes, _ := io.ReadAll(outR)
	errBytes, _ := io.ReadAll(errR)
	return string(outBytes), string(errBytes), exitCode
}

// TestObsidianExportGraphConfigInvalid verifies that --graph-config with an
// invalid value exits 1 and prints an error to stderr. (REQ-GRAPH-01)
func TestObsidianExportGraphConfigInvalid(t *testing.T) {
	cfg := testConfig(t)
	vaultDir := t.TempDir()

	withArgs(t, "engram", "obsidian-export",
		"--vault", vaultDir,
		"--graph-config", "bananas",
	)

	_, stderr, code := captureExitPanic(t, func() { cmdObsidianExport(cfg) })

	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr, "graph-config") {
		t.Fatalf("expected 'graph-config' in stderr, got: %q", stderr)
	}
}

// TestObsidianExportGraphConfigDefaultsToPreserve verifies that when --graph-config
// is not set, the exporter is called with GraphConfigPreserve. (REQ-GRAPH-01)
func TestObsidianExportGraphConfigDefaultsToPreserve(t *testing.T) {
	cfg := testConfig(t)
	vaultDir := t.TempDir()

	var capturedCfg obsidian.ExportConfig
	oldNew := newObsidianExporter
	t.Cleanup(func() { newObsidianExporter = oldNew })
	newObsidianExporter = func(s obsidian.StoreReader, c obsidian.ExportConfig) *obsidian.Exporter {
		capturedCfg = c
		return obsidian.NewExporter(s, c)
	}

	withArgs(t, "engram", "obsidian-export", "--vault", vaultDir)

	_, _ = captureOutput(t, func() { cmdObsidianExport(cfg) })

	if capturedCfg.GraphConfig != obsidian.GraphConfigPreserve {
		t.Fatalf("expected GraphConfig=%q (preserve), got %q", obsidian.GraphConfigPreserve, capturedCfg.GraphConfig)
	}
}

// TestObsidianExportWatchRequiresInterval verifies that --watch alone uses
// the default 10m interval and does NOT exit with an error. (REQ-WATCH-02)
func TestObsidianExportWatchRequiresInterval(t *testing.T) {
	cfg := testConfig(t)
	vaultDir := t.TempDir()

	// Inject a fake watcher that records the call and returns immediately.
	var watcherCalled bool
	var capturedInterval time.Duration
	oldWatcher := newObsidianWatcher
	t.Cleanup(func() { newObsidianWatcher = oldWatcher })
	newObsidianWatcher = func(wc obsidian.WatcherConfig) *obsidian.Watcher {
		watcherCalled = true
		capturedInterval = wc.Interval
		return nil // nil signals the CLI to skip watcher.Run()
	}

	withArgs(t, "engram", "obsidian-export", "--vault", vaultDir, "--watch")

	// --watch with nil watcher should not panic and should not exit 1
	var exitCode int
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })
	exitFunc = func(code int) { exitCode = code; panic("exit") }

	func() {
		defer func() { recover() }() //nolint:errcheck
		_, _ = captureOutput(t, func() { cmdObsidianExport(cfg) })
	}()

	// Exit code should be 0 (clean exit after watcher returns nil)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !watcherCalled {
		t.Fatalf("expected newObsidianWatcher to be called")
	}
	if capturedInterval != 10*time.Minute {
		t.Fatalf("expected default interval 10m, got %v", capturedInterval)
	}
}

// TestObsidianExportIntervalWithoutWatchErrors verifies that --interval without
// --watch exits 1. (REQ-WATCH-07)
func TestObsidianExportIntervalWithoutWatchErrors(t *testing.T) {
	cfg := testConfig(t)
	vaultDir := t.TempDir()

	withArgs(t, "engram", "obsidian-export",
		"--vault", vaultDir,
		"--interval", "5m",
	)

	_, stderr, code := captureExitPanic(t, func() { cmdObsidianExport(cfg) })

	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr, "--interval") && !strings.Contains(stderr, "watch") {
		t.Fatalf("expected '--interval' or 'watch' in stderr, got: %q", stderr)
	}
}

// TestObsidianExportIntervalBelowMinimumErrors verifies that --watch --interval 30s
// exits 1 because the interval is below the 1-minute minimum. (REQ-WATCH-07)
func TestObsidianExportIntervalBelowMinimumErrors(t *testing.T) {
	cfg := testConfig(t)
	vaultDir := t.TempDir()

	withArgs(t, "engram", "obsidian-export",
		"--vault", vaultDir,
		"--watch",
		"--interval", "30s",
	)

	_, stderr, code := captureExitPanic(t, func() { cmdObsidianExport(cfg) })

	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr, "1m") && !strings.Contains(stderr, "minimum") {
		t.Fatalf("expected minimum interval message in stderr, got: %q", stderr)
	}
}

// TestObsidianExportIntervalUnparseableErrors verifies that --watch --interval banana
// exits 1 with a parse error. (REQ-WATCH-07)
func TestObsidianExportIntervalUnparseableErrors(t *testing.T) {
	cfg := testConfig(t)
	vaultDir := t.TempDir()

	withArgs(t, "engram", "obsidian-export",
		"--vault", vaultDir,
		"--watch",
		"--interval", "banana",
	)

	_, stderr, code := captureExitPanic(t, func() { cmdObsidianExport(cfg) })

	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(stderr, "interval") {
		t.Fatalf("expected 'interval' in stderr, got: %q", stderr)
	}
}

// TestObsidianExportWatchModeCallsInjectedWatcher verifies that with --watch,
// the injected newObsidianWatcher is called with the correct WatcherConfig.
// Uses a fake that records the call. (REQ-WATCH-01)
func TestObsidianExportWatchModeCallsInjectedWatcher(t *testing.T) {
	cfg := testConfig(t)
	vaultDir := t.TempDir()

	var watcherCfg obsidian.WatcherConfig
	watcherCalled := false
	oldWatcher := newObsidianWatcher
	t.Cleanup(func() { newObsidianWatcher = oldWatcher })
	newObsidianWatcher = func(wc obsidian.WatcherConfig) *obsidian.Watcher {
		watcherCalled = true
		watcherCfg = wc
		return nil // nil means Run() is skipped; clean exit
	}

	withArgs(t, "engram", "obsidian-export",
		"--vault", vaultDir,
		"--watch",
		"--interval", "2m",
	)

	var exitCode int
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })
	exitFunc = func(code int) { exitCode = code; panic("exit") }

	func() {
		defer func() { recover() }() //nolint:errcheck
		_, _ = captureOutput(t, func() { cmdObsidianExport(cfg) })
	}()

	if exitCode != 0 {
		t.Fatalf("expected clean exit (0), got %d", exitCode)
	}
	if !watcherCalled {
		t.Fatalf("expected newObsidianWatcher to be called")
	}
	if watcherCfg.Interval != 2*time.Minute {
		t.Fatalf("expected interval 2m, got %v", watcherCfg.Interval)
	}
	if watcherCfg.Exporter == nil {
		t.Fatalf("expected non-nil Exporter in WatcherConfig")
	}
	if watcherCfg.Logf == nil {
		t.Fatalf("expected non-nil Logf in WatcherConfig")
	}
}

// ─── Delete command tests ─────────────────────────────────────────────────────

func TestCmdDeleteSoftDeleteSuccess(t *testing.T) {
	cfg := testConfig(t)
	id := mustSeedObservation(t, cfg, "s-del", "proj-del", "decision", "to-delete", "delete me", "project")

	withArgs(t, "engram", "delete", strconv.FormatInt(id, 10))
	stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Fatalf("expected deletion confirmation, got: %q", stdout)
	}
	if !strings.Contains(stdout, strconv.FormatInt(id, 10)) {
		t.Fatalf("expected id in output, got: %q", stdout)
	}
}

func TestCmdDeleteHardDeleteSuccess(t *testing.T) {
	cfg := testConfig(t)
	id := mustSeedObservation(t, cfg, "s-del2", "proj-del2", "decision", "hard-delete", "hard delete me", "project")

	withArgs(t, "engram", "delete", strconv.FormatInt(id, 10), "--hard")
	stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Fatalf("expected deletion confirmation, got: %q", stdout)
	}
	if !strings.Contains(stdout, strconv.FormatInt(id, 10)) {
		t.Fatalf("expected id in output, got: %q", stdout)
	}
}

func TestCmdDeleteNonExistentID(t *testing.T) {
	cfg := testConfig(t)

	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "delete", "999999")
	_, stderr := captureOutput(t, func() { cmdDelete(cfg) })

	if !exited {
		t.Fatalf("expected exitFunc to be called for non-existent observation")
	}
	if !strings.Contains(stderr, "not found") && !strings.Contains(stderr, "observation") {
		t.Fatalf("expected not-found error in stderr, got: %q", stderr)
	}
}

func TestCmdDeleteMissingIDArg(t *testing.T) {
	cfg := testConfig(t)

	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "delete")
	_, stderr := captureOutput(t, func() { cmdDelete(cfg) })

	if !exited {
		t.Fatalf("expected exitFunc to be called when no ID arg provided")
	}
	if !strings.Contains(stderr, "usage") {
		t.Fatalf("expected usage message in stderr, got: %q", stderr)
	}
}

func TestCmdDeleteInvalidIDArg(t *testing.T) {
	cfg := testConfig(t)

	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "delete", "not-a-number")
	_, stderr := captureOutput(t, func() { cmdDelete(cfg) })

	if !exited {
		t.Fatalf("expected exitFunc to be called for invalid id")
	}
	if !strings.Contains(stderr, "invalid") {
		t.Fatalf("expected invalid id error in stderr, got: %q", stderr)
	}
}

func TestCmdDeleteInUsage(t *testing.T) {
	stdout, _ := captureOutput(t, func() { printUsage() })
	if !strings.Contains(stdout, "delete") {
		t.Fatalf("expected 'delete' in usage output, got: %q", stdout)
	}
}

// ─── delete session sub-command tests ─────────────────────────────────────────

func mustSeedSession(t *testing.T, cfg store.Config, sessionID, project string) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.CreateSession(sessionID, project, "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
}

func mustSeedPrompt(t *testing.T, cfg store.Config, sessionID, project string) int64 {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.CreateSession(sessionID, project, "/tmp"); err != nil {
		// ignore if already exists
		_ = err
	}
	id, err := s.AddPrompt(store.AddPromptParams{SessionID: sessionID, Content: "test prompt", Project: project})
	if err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}
	return id
}

func TestCmdDeleteSessionSuccess(t *testing.T) {
	cfg := testConfig(t)
	mustSeedSession(t, cfg, "sess-to-delete", "proj-del-sess")

	withArgs(t, "engram", "delete", "session", "sess-to-delete")
	stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Fatalf("expected deletion confirmation in stdout, got: %q", stdout)
	}
}

func TestCmdDeleteSessionNotFound(t *testing.T) {
	cfg := testConfig(t)

	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "delete", "session", "no-such-session")
	_, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if !exited {
		t.Fatal("expected exitFunc to be called for not-found session")
	}
	if !strings.Contains(stderr, "not found") && !strings.Contains(stderr, "session") {
		t.Fatalf("expected not-found error in stderr, got: %q", stderr)
	}
}

func TestCmdDeleteSessionMissingID(t *testing.T) {
	cfg := testConfig(t)

	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "delete", "session")
	_, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if !exited {
		t.Fatal("expected exitFunc to be called when session id is missing")
	}
	if !strings.Contains(stderr, "usage") {
		t.Fatalf("expected usage message in stderr, got: %q", stderr)
	}
}

// ─── delete prompt sub-command tests ──────────────────────────────────────────

func TestCmdDeletePromptSuccess(t *testing.T) {
	cfg := testConfig(t)
	promptID := mustSeedPrompt(t, cfg, "sess-prompt-del", "proj-del-prompt")

	withArgs(t, "engram", "delete", "prompt", strconv.FormatInt(promptID, 10))
	stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Fatalf("expected deletion confirmation in stdout, got: %q", stdout)
	}
}

func TestCmdDeletePromptNotFound(t *testing.T) {
	cfg := testConfig(t)

	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "delete", "prompt", "999999")
	_, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if !exited {
		t.Fatal("expected exitFunc to be called for not-found prompt")
	}
	if !strings.Contains(stderr, "not found") && !strings.Contains(stderr, "prompt") {
		t.Fatalf("expected not-found error in stderr, got: %q", stderr)
	}
}

func TestCmdDeletePromptMissingID(t *testing.T) {
	cfg := testConfig(t)

	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "delete", "prompt")
	_, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if !exited {
		t.Fatal("expected exitFunc to be called when prompt id is missing")
	}
	if !strings.Contains(stderr, "usage") {
		t.Fatalf("expected usage message in stderr, got: %q", stderr)
	}
}

func TestCmdDeletePromptInvalidID(t *testing.T) {
	cfg := testConfig(t)

	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "delete", "prompt", "not-a-number")
	_, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if !exited {
		t.Fatal("expected exitFunc to be called for invalid prompt id")
	}
	if !strings.Contains(stderr, "invalid") {
		t.Fatalf("expected invalid id error in stderr, got: %q", stderr)
	}
}

// ─── delete project sub-command tests ─────────────────────────────────────────

func TestCmdDeleteProjectSuccess(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "sess-proj-del", "proj-cascade", "decision", "title", "content", "project")

	withArgs(t, "engram", "delete", "project", "proj-cascade", "--hard")
	stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Fatalf("expected deletion confirmation in stdout, got: %q", stdout)
	}
}

func TestCmdDeleteProjectSoftDefault(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "sess-proj-soft", "proj-soft", "decision", "title", "content", "project")

	withArgs(t, "engram", "delete", "project", "proj-soft")
	stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr (soft), got: %q", stderr)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Fatalf("expected deletion confirmation in stdout, got: %q", stdout)
	}
}

func TestCmdDeleteProjectNotFound(t *testing.T) {
	cfg := testConfig(t)

	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "delete", "project", "no-such-project-xyz")
	_, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if !exited {
		t.Fatal("expected exitFunc to be called for not-found project")
	}
	if !strings.Contains(stderr, "not found") && !strings.Contains(stderr, "project") {
		t.Fatalf("expected not-found error in stderr, got: %q", stderr)
	}
}

func TestCmdDeleteProjectMissingName(t *testing.T) {
	cfg := testConfig(t)

	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "delete", "project")
	_, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if !exited {
		t.Fatal("expected exitFunc to be called when project name is missing")
	}
	if !strings.Contains(stderr, "usage") {
		t.Fatalf("expected usage message in stderr, got: %q", stderr)
	}
}

// ─── backward-compat: delete <obs_id> still works ─────────────────────────────

func TestCmdDeleteObservationBackwardCompat(t *testing.T) {
	cfg := testConfig(t)
	id := mustSeedObservation(t, cfg, "s-compat", "proj-compat", "decision", "compat-title", "compat-content", "project")

	withArgs(t, "engram", "delete", strconv.FormatInt(id, 10))
	stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "deleted") {
		t.Fatalf("expected deletion confirmation, got: %q", stdout)
	}
}

// ─── usage shows new sub-commands ─────────────────────────────────────────────

func TestCmdDeleteSubCommandsInUsage(t *testing.T) {
	stdout, _ := captureOutput(t, func() { printUsage() })
	for _, want := range []string{"delete session", "delete prompt", "delete project"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in usage output, got:\n%s", want, stdout)
		}
	}
}
