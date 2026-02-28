package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func testConfig(t *testing.T) store.Config {
	t.Helper()
	cfg := store.DefaultConfig()
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
}

func TestPrintPostInstall(t *testing.T) {
	tests := []struct {
		agent   string
		expects []string
	}{
		{agent: "opencode", expects: []string{"Restart OpenCode", "engram serve &"}},
		{agent: "gemini-cli", expects: []string{"Restart Gemini CLI", "~/.gemini/settings.json"}},
		{agent: "codex", expects: []string{"Restart Codex", "~/.codex/config.toml"}},
		{agent: "unknown", expects: nil},
	}

	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			stdout, stderr := captureOutput(t, func() { printPostInstall(tc.agent) })
			if stderr != "" {
				t.Fatalf("expected no stderr, got: %q", stderr)
			}
			for _, expected := range tc.expects {
				if !strings.Contains(stdout, expected) {
					t.Fatalf("output missing %q: %q", expected, stdout)
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

		stdout, _ := captureOutput(t, func() { printPostInstall("claude-code") })
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

		stdout, _ := captureOutput(t, func() { printPostInstall("claude-code") })
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

		_, stderr := captureOutput(t, func() { printPostInstall("claude-code") })
		if !strings.Contains(stderr, "warning") {
			t.Fatalf("expected warning in stderr, got: %q", stderr)
		}
	})
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
	if !strings.Contains(noopOut, "Already up to date") {
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
	default:
		os.Args = []string{"engram", "--help"}
	}

	main()
}
