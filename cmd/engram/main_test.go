package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/mcp"
	"github.com/Gentleman-Programming/engram/internal/obsidian"
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

	fn()

	_ = outW.Close()
	_ = errW.Close()
	os.Stdout = oldOut
	os.Stderr = oldErr

	outBytes, err := io.ReadAll(outR)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	errBytes, err := io.ReadAll(errR)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}

	return string(outBytes), string(errBytes)
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
	if !strings.Contains(stdout, "opencode, pi, claude-code, gemini-cli, codex") {
		t.Fatalf("usage missing pi setup agent: %q", stdout)
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

func TestCmdTimeline(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "s-1", "proj", "note", "first", "first content", "project")
	focusID := mustSeedObservation(t, cfg, "s-1", "proj", "note", "focus", "focus content", "project")
	mustSeedObservation(t, cfg, "s-1", "proj", "note", "third", "third content", "project")

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

	withArgs(t, "engram", "stats")
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

	withArgs(t, "engram", "export", exportPath)
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

func TestCmdProjectsConsolidateDryRun(t *testing.T) {
	cfg := testConfig(t)

	// Seed a canonical and a similar variant (substring match, distinct after normalize)
	mustSeedObservation(t, cfg, "s-eng", "engram", "note", "eng note", "content", "project")
	mustSeedObservation(t, cfg, "s-engm", "engram-memory", "note", "engm note", "content", "project")

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
	// Verify no actual merge happened (both projects still exist)
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
	if len(names) < 2 {
		t.Fatalf("expected 2 project names after dry-run, got: %v", names)
	}
}

func TestCmdProjectsConsolidateSingleProject(t *testing.T) {
	cfg := testConfig(t)

	// Seed canonical and a similar variant (substring match, distinct after normalize)
	mustSeedObservation(t, cfg, "s-eng", "engram", "note", "eng note", "content", "project")
	mustSeedObservation(t, cfg, "s-engm", "engram-memory", "note", "engm note", "content", "project")

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
	if !strings.Contains(stdout, "Merged into") {
		t.Fatalf("expected merge result, got: %q", stdout)
	}

	// Verify engram-memory was merged into engram
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

	// Seed similar projects (substring match, stays distinct after normalize)
	mustSeedObservation(t, cfg, "s-eng", "engram", "note", "eng note", "content", "project")
	mustSeedObservation(t, cfg, "s-engm", "engram-memory", "note", "engm note", "content", "project")

	withArgs(t, "engram", "projects", "consolidate", "--all", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "dry-run") || !strings.Contains(stdout, "Group") {
		t.Fatalf("expected dry-run group output, got: %q", stdout)
	}
}

func TestCmdProjectsAllNoGroups(t *testing.T) {
	cfg := testConfig(t)

	// Seed completely unrelated projects
	mustSeedObservation(t, cfg, "s-foo", "fooproject", "note", "foo", "content", "project")
	mustSeedObservation(t, cfg, "s-bar", "barproject", "note", "bar", "content", "project")
	mustSeedObservation(t, cfg, "s-qux", "quxproject", "note", "qux", "content", "project")

	withArgs(t, "engram", "projects", "consolidate", "--all")
	stdout, stderr := captureOutput(t, func() { cmdProjectsConsolidate(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	// The three "project"-suffixed names might be grouped by similarity.
	// We just verify it runs without error and produces readable output.
	_ = stdout
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

// TestObsidianExportMinimalFlags verifies that only --vault (the required flag)
// is sufficient — optional flags default to zero values (triangulation case).
func TestObsidianExportMinimalFlags(t *testing.T) {
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

	if capturedCfg.VaultPath != vaultDir {
		t.Fatalf("expected VaultPath=%q, got %q", vaultDir, capturedCfg.VaultPath)
	}
	// Optional flags should be zero
	if capturedCfg.Project != "" {
		t.Fatalf("expected empty Project, got %q", capturedCfg.Project)
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
