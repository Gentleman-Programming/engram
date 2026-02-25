package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	engramsrv "github.com/alanbuscaglia/engram/internal/server"
	"github.com/alanbuscaglia/engram/internal/setup"
	"github.com/alanbuscaglia/engram/internal/store"
	engramsync "github.com/alanbuscaglia/engram/internal/sync"
	"github.com/alanbuscaglia/engram/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

type exitCode int

func captureOutputAndRecover(t *testing.T, fn func()) (stdout string, stderr string, recovered any) {
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

	func() {
		defer func() {
			recovered = recover()
		}()
		fn()
	}()

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

	return string(outBytes), string(errBytes), recovered
}

func stubExitWithPanic(t *testing.T) {
	t.Helper()
	old := exitFunc
	exitFunc = func(code int) { panic(exitCode(code)) }
	t.Cleanup(func() { exitFunc = old })
}

func stubRuntimeHooks(t *testing.T) {
	t.Helper()
	oldStoreNew := storeNew
	oldNewHTTPServer := newHTTPServer
	oldStartHTTP := startHTTP
	oldNewMCPServer := newMCPServer
	oldNewMCPServerWithTools := newMCPServerWithTools
	oldServeMCP := serveMCP
	oldNewTUIModel := newTUIModel
	oldNewTeaProgram := newTeaProgram
	oldRunTeaProgram := runTeaProgram
	oldSetupSupportedAgents := setupSupportedAgents
	oldSetupInstallAgent := setupInstallAgent
	oldScanInputLine := scanInputLine
	oldStoreSearch := storeSearch
	oldStoreAddObservation := storeAddObservation
	oldStoreTimeline := storeTimeline
	oldStoreFormatContext := storeFormatContext
	oldStoreStats := storeStats
	oldStoreExport := storeExport
	oldJSONMarshalIndent := jsonMarshalIndent
	oldSyncStatus := syncStatus
	oldSyncImport := syncImport
	oldSyncExport := syncExport

	storeNew = store.New
	newHTTPServer = func(s *store.Store, _ int) *engramsrv.Server { return engramsrv.New(s, 0) }
	startHTTP = func(_ *engramsrv.Server) error { return nil }
	newMCPServer = func(s *store.Store) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0", mcpserver.WithRecovery())
	}
	newMCPServerWithTools = func(s *store.Store, allowlist map[string]bool) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0", mcpserver.WithRecovery())
	}
	serveMCP = func(_ *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error { return nil }
	newTUIModel = func(_ *store.Store) tui.Model { return tui.New(nil, "") }
	newTeaProgram = func(tea.Model, ...tea.ProgramOption) *tea.Program { return &tea.Program{} }
	runTeaProgram = func(*tea.Program) (tea.Model, error) { return nil, nil }
	setupSupportedAgents = setup.SupportedAgents
	setupInstallAgent = setup.Install
	scanInputLine = fmt.Scanln
	storeSearch = func(s *store.Store, query string, opts store.SearchOptions) ([]store.SearchResult, error) {
		return s.Search(query, opts)
	}
	storeAddObservation = func(s *store.Store, p store.AddObservationParams) (int64, error) {
		return s.AddObservation(p)
	}
	storeTimeline = func(s *store.Store, observationID int64, before, after int) (*store.TimelineResult, error) {
		return s.Timeline(observationID, before, after)
	}
	storeFormatContext = func(s *store.Store, project, scope string) (string, error) {
		return s.FormatContext(project, scope)
	}
	storeStats = func(s *store.Store) (*store.Stats, error) { return s.Stats() }
	storeExport = func(s *store.Store) (*store.ExportData, error) { return s.Export() }
	jsonMarshalIndent = json.MarshalIndent
	syncStatus = func(sy *engramsync.Syncer) (localChunks int, remoteChunks int, pendingImport int, err error) {
		return sy.Status()
	}
	syncImport = func(sy *engramsync.Syncer) (*engramsync.ImportResult, error) { return sy.Import() }
	syncExport = func(sy *engramsync.Syncer, createdBy, project string) (*engramsync.SyncResult, error) {
		return sy.Export(createdBy, project)
	}

	t.Cleanup(func() {
		storeNew = oldStoreNew
		newHTTPServer = oldNewHTTPServer
		startHTTP = oldStartHTTP
		newMCPServer = oldNewMCPServer
		newMCPServerWithTools = oldNewMCPServerWithTools
		serveMCP = oldServeMCP
		newTUIModel = oldNewTUIModel
		newTeaProgram = oldNewTeaProgram
		runTeaProgram = oldRunTeaProgram
		setupSupportedAgents = oldSetupSupportedAgents
		setupInstallAgent = oldSetupInstallAgent
		scanInputLine = oldScanInputLine
		storeSearch = oldStoreSearch
		storeAddObservation = oldStoreAddObservation
		storeTimeline = oldStoreTimeline
		storeFormatContext = oldStoreFormatContext
		storeStats = oldStoreStats
		storeExport = oldStoreExport
		jsonMarshalIndent = oldJSONMarshalIndent
		syncStatus = oldSyncStatus
		syncImport = oldSyncImport
		syncExport = oldSyncExport
	})
}

func TestFatal(t *testing.T) {
	stubExitWithPanic(t)
	_, stderr, recovered := captureOutputAndRecover(t, func() {
		fatal(errors.New("boom"))
	})

	code, ok := recovered.(exitCode)
	if !ok || int(code) != 1 {
		t.Fatalf("expected exit code 1 panic, got %v", recovered)
	}
	if !strings.Contains(stderr, "engram: boom") {
		t.Fatalf("fatal stderr mismatch: %q", stderr)
	}
}

func TestCmdServeParsesPortAndErrors(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)

	tests := []struct {
		name      string
		envPort   string
		argPort   string
		wantPort  int
		startErr  error
		wantFatal bool
	}{
		{name: "default port", wantPort: 7437},
		{name: "env port", envPort: "8123", wantPort: 8123},
		{name: "arg overrides env", envPort: "8123", argPort: "9001", wantPort: 9001},
		{name: "invalid env keeps default", envPort: "nope", wantPort: 7437},
		{name: "invalid arg keeps env", envPort: "8123", argPort: "bad", wantPort: 8123},
		{name: "start failure", wantPort: 7437, startErr: errors.New("listen failed"), wantFatal: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubExitWithPanic(t)
			if tc.envPort != "" {
				t.Setenv("ENGRAM_PORT", tc.envPort)
			} else {
				t.Setenv("ENGRAM_PORT", "")
			}

			args := []string{"engram", "serve"}
			if tc.argPort != "" {
				args = append(args, tc.argPort)
			}
			withArgs(t, args...)

			seenPort := -1
			newHTTPServer = func(s *store.Store, port int) *engramsrv.Server {
				seenPort = port
				return engramsrv.New(s, 0)
			}
			startHTTP = func(_ *engramsrv.Server) error {
				return tc.startErr
			}

			_, stderr, recovered := captureOutputAndRecover(t, func() {
				cmdServe(cfg)
			})

			if seenPort != tc.wantPort {
				t.Fatalf("port=%d want=%d", seenPort, tc.wantPort)
			}
			if tc.wantFatal {
				if _, ok := recovered.(exitCode); !ok {
					t.Fatalf("expected fatal exit, got %v", recovered)
				}
				if !strings.Contains(stderr, "listen failed") {
					t.Fatalf("stderr missing start error: %q", stderr)
				}
			} else if recovered != nil {
				t.Fatalf("expected no panic, got %v", recovered)
			}
		})
	}
}

func TestCmdMCPAndTUIBranches(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	serveMCP = func(_ *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error { return errors.New("mcp failed") }
	_, mcpErr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if _, ok := recovered.(exitCode); !ok || !strings.Contains(mcpErr, "mcp failed") {
		t.Fatalf("expected mcp fatal, got panic=%v stderr=%q", recovered, mcpErr)
	}

	serveMCP = func(_ *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error { return nil }
	_, _, recovered = captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil {
		t.Fatalf("unexpected panic on successful mcp: %v", recovered)
	}

	runTeaProgram = func(*tea.Program) (tea.Model, error) { return nil, errors.New("tui failed") }
	_, tuiErr, recovered := captureOutputAndRecover(t, func() { cmdTUI(cfg) })
	if _, ok := recovered.(exitCode); !ok || !strings.Contains(tuiErr, "tui failed") {
		t.Fatalf("expected tui fatal, got panic=%v stderr=%q", recovered, tuiErr)
	}

	runTeaProgram = func(*tea.Program) (tea.Model, error) { return nil, nil }
	_, _, recovered = captureOutputAndRecover(t, func() { cmdTUI(cfg) })
	if recovered != nil {
		t.Fatalf("unexpected panic on successful tui: %v", recovered)
	}
}

func TestCmdSetupDirectAndInteractive(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	setupInstallAgent = func(agent string) (*setup.Result, error) {
		if agent == "broken" {
			return nil, errors.New("install failed")
		}
		return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 2}, nil
	}

	withArgs(t, "engram", "setup", "codex")
	out, errOut, recovered := captureOutputAndRecover(t, func() { cmdSetup() })
	if recovered != nil || errOut != "" {
		t.Fatalf("direct setup should succeed, panic=%v stderr=%q", recovered, errOut)
	}
	if !strings.Contains(out, "Installed codex plugin") {
		t.Fatalf("unexpected direct setup output: %q", out)
	}

	withArgs(t, "engram", "setup", "broken")
	_, errOut, recovered = captureOutputAndRecover(t, func() { cmdSetup() })
	if _, ok := recovered.(exitCode); !ok || !strings.Contains(errOut, "install failed") {
		t.Fatalf("expected direct setup fatal, panic=%v stderr=%q", recovered, errOut)
	}

	setupSupportedAgents = func() []setup.Agent {
		return []setup.Agent{{Name: "opencode", Description: "OpenCode", InstallDir: "/tmp/opencode"}}
	}
	scanInputLine = func(a ...any) (int, error) {
		p := a[0].(*string)
		*p = "1"
		return 1, nil
	}

	withArgs(t, "engram", "setup")
	out, errOut, recovered = captureOutputAndRecover(t, func() { cmdSetup() })
	if recovered != nil || errOut != "" {
		t.Fatalf("interactive setup should succeed, panic=%v stderr=%q", recovered, errOut)
	}
	if !strings.Contains(out, "Installing opencode plugin") {
		t.Fatalf("unexpected interactive setup output: %q", out)
	}

	scanInputLine = func(a ...any) (int, error) {
		p := a[0].(*string)
		*p = "99"
		return 1, nil
	}
	withArgs(t, "engram", "setup")
	_, errOut, recovered = captureOutputAndRecover(t, func() { cmdSetup() })
	if _, ok := recovered.(exitCode); !ok || !strings.Contains(errOut, "Invalid choice") {
		t.Fatalf("expected invalid choice exit, panic=%v stderr=%q", recovered, errOut)
	}
}

func TestCmdExportDefaultAndCmdImportErrors(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)

	cfg := testConfig(t)
	stubExitWithPanic(t)

	mustSeedObservation(t, cfg, "s-exp-default", "proj", "note", "title", "content", "project")

	withArgs(t, "engram", "export")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("export default should succeed, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Exported to engram-export.json") {
		t.Fatalf("unexpected default export output: %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(workDir, "engram-export.json")); err != nil {
		t.Fatalf("expected default export file: %v", err)
	}

	badPath := filepath.Join(workDir, "missing", "out.json")
	withArgs(t, "engram", "export", badPath)
	_, stderr, recovered = captureOutputAndRecover(t, func() { cmdExport(cfg) })
	if _, ok := recovered.(exitCode); !ok || !strings.Contains(stderr, "no such file or directory") {
		t.Fatalf("expected export write fatal, panic=%v stderr=%q", recovered, stderr)
	}

	withArgs(t, "engram", "import")
	_, stderr, recovered = captureOutputAndRecover(t, func() { cmdImport(cfg) })
	if _, ok := recovered.(exitCode); !ok || !strings.Contains(stderr, "usage: engram import") {
		t.Fatalf("expected import usage exit, panic=%v stderr=%q", recovered, stderr)
	}

	withArgs(t, "engram", "import", filepath.Join(workDir, "nope.json"))
	_, stderr, recovered = captureOutputAndRecover(t, func() { cmdImport(cfg) })
	if _, ok := recovered.(exitCode); !ok || !strings.Contains(stderr, "read") {
		t.Fatalf("expected import read fatal, panic=%v stderr=%q", recovered, stderr)
	}

	invalidJSON := filepath.Join(workDir, "invalid.json")
	if err := os.WriteFile(invalidJSON, []byte("{invalid"), 0644); err != nil {
		t.Fatalf("write invalid json: %v", err)
	}
	withArgs(t, "engram", "import", invalidJSON)
	_, stderr, recovered = captureOutputAndRecover(t, func() { cmdImport(cfg) })
	if _, ok := recovered.(exitCode); !ok || !strings.Contains(stderr, "parse") {
		t.Fatalf("expected import parse fatal, panic=%v stderr=%q", recovered, stderr)
	}
}

func TestMainDispatchServeMCPAndTUI(t *testing.T) {
	stubRuntimeHooks(t)

	t.Setenv("ENGRAM_DATA_DIR", t.TempDir())
	withArgs(t, "engram", "serve", "8088")
	_, stderr, recovered := captureOutputAndRecover(t, func() { main() })
	if recovered != nil || stderr != "" {
		t.Fatalf("serve dispatch failed: panic=%v stderr=%q", recovered, stderr)
	}

	withArgs(t, "engram", "mcp")
	_, stderr, recovered = captureOutputAndRecover(t, func() { main() })
	if recovered != nil || stderr != "" {
		t.Fatalf("mcp dispatch failed: panic=%v stderr=%q", recovered, stderr)
	}

	withArgs(t, "engram", "tui")
	_, stderr, recovered = captureOutputAndRecover(t, func() { main() })
	if recovered != nil || stderr != "" {
		t.Fatalf("tui dispatch failed: panic=%v stderr=%q", recovered, stderr)
	}
}

func TestStoreInitFailurePaths(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	importFile := filepath.Join(t.TempDir(), "import.json")
	if err := os.WriteFile(importFile, []byte(`{"version":"0.1.0","exported_at":"2026-01-01T00:00:00Z","sessions":[],"observations":[],"prompts":[]}`), 0644); err != nil {
		t.Fatalf("write import file: %v", err)
	}

	storeNew = func(store.Config) (*store.Store, error) {
		return nil, errors.New("store init failed")
	}

	cmds := []func(store.Config){
		cmdServe,
		cmdMCP,
		cmdTUI,
		cmdSearch,
		cmdSave,
		cmdTimeline,
		cmdContext,
		cmdStats,
		cmdExport,
		cmdImport,
		cmdSync,
	}

	argsByCmd := [][]string{
		{"engram", "serve"},
		{"engram", "mcp"},
		{"engram", "tui"},
		{"engram", "search", "q"},
		{"engram", "save", "t", "c"},
		{"engram", "timeline", "1"},
		{"engram", "context"},
		{"engram", "stats"},
		{"engram", "export"},
		{"engram", "import", importFile},
		{"engram", "sync"},
	}

	for i, fn := range cmds {
		withArgs(t, argsByCmd[i]...)
		_, stderr, recovered := captureOutputAndRecover(t, func() { fn(cfg) })
		if _, ok := recovered.(exitCode); !ok {
			t.Fatalf("command %d: expected exit panic, got %v", i, recovered)
		}
		if !strings.Contains(stderr, "store init failed") {
			t.Fatalf("command %d: expected store failure stderr, got %q", i, stderr)
		}
	}
}

func TestUsageAndValidationExits(t *testing.T) {
	cfg := testConfig(t)
	stubExitWithPanic(t)

	tests := []struct {
		name       string
		args       []string
		run        func(store.Config)
		errSubstr  string
		stderrOnly bool
	}{
		{name: "search usage", args: []string{"engram", "search"}, run: cmdSearch, errSubstr: "usage: engram search"},
		{name: "search missing query", args: []string{"engram", "search", "--limit", "3"}, run: cmdSearch, errSubstr: "search query is required"},
		{name: "save usage", args: []string{"engram", "save", "title"}, run: cmdSave, errSubstr: "usage: engram save"},
		{name: "timeline usage", args: []string{"engram", "timeline"}, run: cmdTimeline, errSubstr: "usage: engram timeline"},
		{name: "timeline invalid id", args: []string{"engram", "timeline", "abc"}, run: cmdTimeline, errSubstr: "invalid observation id"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withArgs(t, tc.args...)
			_, stderr, recovered := captureOutputAndRecover(t, func() { tc.run(cfg) })
			if _, ok := recovered.(exitCode); !ok {
				t.Fatalf("expected exit panic, got %v", recovered)
			}
			if !strings.Contains(stderr, tc.errSubstr) {
				t.Fatalf("stderr missing %q: %q", tc.errSubstr, stderr)
			}
		})
	}
}

func TestMainDispatchRemainingCommands(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	withCwd(t, t.TempDir())

	dataDir := t.TempDir()
	t.Setenv("ENGRAM_DATA_DIR", dataDir)

	seedCfg := store.DefaultConfig()
	seedCfg.DataDir = dataDir
	focusID := mustSeedObservation(t, seedCfg, "s-main", "main-proj", "note", "focus", "focus content", "project")

	importFile := filepath.Join(t.TempDir(), "import.json")
	if err := os.WriteFile(importFile, []byte(`{"version":"0.1.0","exported_at":"2026-01-01T00:00:00Z","sessions":[],"observations":[],"prompts":[]}`), 0644); err != nil {
		t.Fatalf("write import file: %v", err)
	}

	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/dest", Files: 1}, nil
	}

	tests := []struct {
		name string
		args []string
	}{
		{name: "search", args: []string{"engram", "search", "focus"}},
		{name: "save", args: []string{"engram", "save", "t", "c"}},
		{name: "timeline", args: []string{"engram", "timeline", fmt.Sprintf("%d", focusID)}},
		{name: "context", args: []string{"engram", "context", "main-proj"}},
		{name: "stats", args: []string{"engram", "stats"}},
		{name: "export", args: []string{"engram", "export", filepath.Join(t.TempDir(), "exp.json")}},
		{name: "import", args: []string{"engram", "import", importFile}},
		{name: "sync", args: []string{"engram", "sync", "--all"}},
		{name: "setup", args: []string{"engram", "setup", "codex"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withArgs(t, tc.args...)
			_, stderr, recovered := captureOutputAndRecover(t, func() { main() })
			if recovered != nil {
				t.Fatalf("main panic for %s: %v stderr=%q", tc.name, recovered, stderr)
			}
		})
	}
}

func TestCmdSyncAdditionalBranches(t *testing.T) {
	stubExitWithPanic(t)

	t.Run("all projects empty export message", func(t *testing.T) {
		workDir := t.TempDir()
		withCwd(t, workDir)
		cfg := testConfig(t)

		withArgs(t, "engram", "sync", "--all")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "Exporting ALL memories") || !strings.Contains(stdout, "Nothing new to sync") {
			t.Fatalf("unexpected output: %q", stdout)
		}
	})

	t.Run("status parse error", func(t *testing.T) {
		workDir := t.TempDir()
		withCwd(t, workDir)
		cfg := testConfig(t)

		if err := os.MkdirAll(filepath.Join(workDir, ".engram"), 0755); err != nil {
			t.Fatalf("mkdir .engram: %v", err)
		}
		if err := os.WriteFile(filepath.Join(workDir, ".engram", "manifest.json"), []byte("{bad json"), 0644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}

		withArgs(t, "engram", "sync", "--status")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
		if _, ok := recovered.(exitCode); !ok {
			t.Fatalf("expected fatal exit, got %v", recovered)
		}
		if !strings.Contains(stderr, "parse manifest") {
			t.Fatalf("unexpected stderr: %q", stderr)
		}
	})

	t.Run("import parse error", func(t *testing.T) {
		workDir := t.TempDir()
		withCwd(t, workDir)
		cfg := testConfig(t)

		if err := os.MkdirAll(filepath.Join(workDir, ".engram"), 0755); err != nil {
			t.Fatalf("mkdir .engram: %v", err)
		}
		if err := os.WriteFile(filepath.Join(workDir, ".engram", "manifest.json"), []byte("{bad json"), 0644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}

		withArgs(t, "engram", "sync", "--import")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
		if _, ok := recovered.(exitCode); !ok {
			t.Fatalf("expected fatal exit, got %v", recovered)
		}
		if !strings.Contains(stderr, "parse manifest") {
			t.Fatalf("unexpected stderr: %q", stderr)
		}
	})

	t.Run("export parse error", func(t *testing.T) {
		workDir := t.TempDir()
		withCwd(t, workDir)
		cfg := testConfig(t)

		if err := os.MkdirAll(filepath.Join(workDir, ".engram"), 0755); err != nil {
			t.Fatalf("mkdir .engram: %v", err)
		}
		if err := os.WriteFile(filepath.Join(workDir, ".engram", "manifest.json"), []byte("{bad json"), 0644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}

		withArgs(t, "engram", "sync")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
		if _, ok := recovered.(exitCode); !ok {
			t.Fatalf("expected fatal exit, got %v", recovered)
		}
		if !strings.Contains(stderr, "parse manifest") {
			t.Fatalf("unexpected stderr: %q", stderr)
		}
	})
}

func TestCmdImportStoreImportFailure(t *testing.T) {
	stubExitWithPanic(t)
	cfg := testConfig(t)

	badImport := filepath.Join(t.TempDir(), "bad-import.json")
	badJSON := `{
		"version":"0.1.0",
		"exported_at":"2026-01-01T00:00:00Z",
		"sessions":[],
		"observations":[{"id":1,"session_id":"missing-session","type":"note","title":"x","content":"y","scope":"project","revision_count":1,"duplicate_count":1,"created_at":"2026-01-01 00:00:00","updated_at":"2026-01-01 00:00:00"}],
		"prompts":[]
	}`
	if err := os.WriteFile(badImport, []byte(badJSON), 0644); err != nil {
		t.Fatalf("write bad import: %v", err)
	}

	withArgs(t, "engram", "import", badImport)
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdImport(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "import observation") {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
}

func TestCmdSearchAndSaveDanglingFlags(t *testing.T) {
	cfg := testConfig(t)

	withArgs(t, "engram", "save", "dangling-title", "dangling-content", "--type")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSave(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("save with dangling flag failed, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Memory saved:") {
		t.Fatalf("unexpected save output: %q", stdout)
	}

	withArgs(t, "engram", "search", "dangling-content", "--limit", "not-a-number", "--project")
	stdout, stderr, recovered = captureOutputAndRecover(t, func() { cmdSearch(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("search with dangling flags failed, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Found") {
		t.Fatalf("unexpected search output: %q", stdout)
	}
}

func TestCmdSetupHyphenArgFallsBackToInteractive(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	setupSupportedAgents = func() []setup.Agent {
		return []setup.Agent{{Name: "codex", Description: "Codex", InstallDir: "/tmp/codex"}}
	}
	setupInstallAgent = func(agent string) (*setup.Result, error) {
		return &setup.Result{Agent: agent, Destination: "/tmp/codex", Files: 1}, nil
	}
	scanInputLine = func(a ...any) (int, error) {
		p := a[0].(*string)
		*p = "1"
		return 1, nil
	}

	withArgs(t, "engram", "setup", "--not-an-agent")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup() })
	if recovered != nil || stderr != "" {
		t.Fatalf("setup interactive fallback failed: panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Which agent do you want to set up?") || !strings.Contains(stdout, "Installing codex plugin") {
		t.Fatalf("unexpected setup output: %q", stdout)
	}
}

func TestCmdTimelineNoBeforeAfterSections(t *testing.T) {
	cfg := testConfig(t)
	focusID := mustSeedObservation(t, cfg, "solo-session", "solo", "note", "focus", "only content", "project")

	withArgs(t, "engram", "timeline", fmt.Sprintf("%d", focusID), "--before", "0", "--after", "0")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdTimeline(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("timeline failed: panic=%v stderr=%q", recovered, stderr)
	}
	if strings.Contains(stdout, "─── Before ───") || strings.Contains(stdout, "─── After ───") {
		t.Fatalf("unexpected before/after sections in output: %q", stdout)
	}
}

func TestCmdStatsNoProjectsYet(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "stats")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdStats(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("stats failed: panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Projects:     none yet") {
		t.Fatalf("expected empty projects output, got: %q", stdout)
	}
}

func TestCmdSyncImportEmptyAndMixedChunks(t *testing.T) {
	stubExitWithPanic(t)

	t.Run("import with empty manifest", func(t *testing.T) {
		workDir := t.TempDir()
		withCwd(t, workDir)
		cfg := testConfig(t)

		if err := os.MkdirAll(filepath.Join(workDir, ".engram"), 0755); err != nil {
			t.Fatalf("mkdir .engram: %v", err)
		}
		if err := os.WriteFile(filepath.Join(workDir, ".engram", "manifest.json"), []byte(`{"version":1,"chunks":[]}`), 0644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}

		withArgs(t, "engram", "sync", "--import")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("empty import failed: panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "Already up to date") || strings.Contains(stdout, "already imported") {
			t.Fatalf("unexpected empty import output: %q", stdout)
		}
	})

	t.Run("import new plus skipped chunk", func(t *testing.T) {
		workDir := t.TempDir()
		withCwd(t, workDir)

		exportCfg := testConfig(t)
		importCfg := testConfig(t)

		mustSeedObservation(t, exportCfg, "mix-1", "mix", "note", "one", "content-one", "project")
		withArgs(t, "engram", "sync", "--all")
		_, _, _ = captureOutputAndRecover(t, func() { cmdSync(exportCfg) })

		withArgs(t, "engram", "sync", "--import")
		_, _, _ = captureOutputAndRecover(t, func() { cmdSync(importCfg) })

		time.Sleep(1100 * time.Millisecond)
		mustSeedObservation(t, exportCfg, "mix-2", "mix", "note", "two", "content-two", "project")
		withArgs(t, "engram", "sync", "--all")
		_, _, _ = captureOutputAndRecover(t, func() { cmdSync(exportCfg) })

		withArgs(t, "engram", "sync", "--import")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(importCfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("mixed import failed: panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "Imported 1 new chunk(s)") || !strings.Contains(stdout, "Skipped:") {
			t.Fatalf("unexpected mixed import output: %q", stdout)
		}
	})
}

func TestCommandErrorSeamsAndUncoveredBranches(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)

	assertFatal := func(t *testing.T, stderr string, recovered any, want string) {
		t.Helper()
		if _, ok := recovered.(exitCode); !ok {
			t.Fatalf("expected fatal exit, got %v", recovered)
		}
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q: %q", want, stderr)
		}
	}

	t.Run("search seam error", func(t *testing.T) {
		withArgs(t, "engram", "search", "needle")
		storeSearch = func(*store.Store, string, store.SearchOptions) ([]store.SearchResult, error) {
			return nil, errors.New("forced search error")
		}
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSearch(cfg) })
		assertFatal(t, stderr, recovered, "forced search error")
	})

	t.Run("save seam error", func(t *testing.T) {
		withArgs(t, "engram", "save", "title", "content")
		storeAddObservation = func(*store.Store, store.AddObservationParams) (int64, error) {
			return 0, errors.New("forced save error")
		}
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSave(cfg) })
		assertFatal(t, stderr, recovered, "forced save error")
	})

	t.Run("timeline seam error", func(t *testing.T) {
		withArgs(t, "engram", "timeline", "1")
		storeTimeline = func(*store.Store, int64, int, int) (*store.TimelineResult, error) {
			return nil, errors.New("forced timeline error")
		}
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdTimeline(cfg) })
		assertFatal(t, stderr, recovered, "forced timeline error")
	})

	t.Run("timeline prints session summary", func(t *testing.T) {
		summary := "this session has a non-empty summary"
		withArgs(t, "engram", "timeline", "1")
		storeTimeline = func(*store.Store, int64, int, int) (*store.TimelineResult, error) {
			return &store.TimelineResult{
				Focus:        store.Observation{ID: 1, Type: "note", Title: "focus", Content: "content", CreatedAt: "2026-01-01"},
				SessionInfo:  &store.Session{Project: "proj", StartedAt: "2026-01-01", Summary: &summary},
				TotalInRange: 1,
			}, nil
		}
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdTimeline(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected successful timeline render, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "Session: proj") || !strings.Contains(stdout, "non-empty summary") {
			t.Fatalf("expected summary in timeline output, got: %q", stdout)
		}
	})

	t.Run("context seam error", func(t *testing.T) {
		withArgs(t, "engram", "context")
		storeFormatContext = func(*store.Store, string, string) (string, error) {
			return "", errors.New("forced context error")
		}
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdContext(cfg) })
		assertFatal(t, stderr, recovered, "forced context error")
	})

	t.Run("stats seam error", func(t *testing.T) {
		withArgs(t, "engram", "stats")
		storeStats = func(*store.Store) (*store.Stats, error) {
			return nil, errors.New("forced stats error")
		}
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdStats(cfg) })
		assertFatal(t, stderr, recovered, "forced stats error")
	})

	t.Run("export seam error", func(t *testing.T) {
		withArgs(t, "engram", "export")
		storeExport = func(*store.Store) (*store.ExportData, error) {
			return nil, errors.New("forced export error")
		}
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
		assertFatal(t, stderr, recovered, "forced export error")
	})

	t.Run("export marshal seam error", func(t *testing.T) {
		withArgs(t, "engram", "export")
		storeExport = func(s *store.Store) (*store.ExportData, error) { return s.Export() }
		jsonMarshalIndent = func(any, string, string) ([]byte, error) {
			return nil, errors.New("forced marshal error")
		}
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdExport(cfg) })
		assertFatal(t, stderr, recovered, "forced marshal error")
	})

	t.Run("sync seam status error", func(t *testing.T) {
		withCwd(t, t.TempDir())
		withArgs(t, "engram", "sync", "--status")
		syncStatus = func(*engramsync.Syncer) (int, int, int, error) {
			return 0, 0, 0, errors.New("forced status error")
		}
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
		assertFatal(t, stderr, recovered, "forced status error")
	})

	t.Run("sync uses explicit project flag", func(t *testing.T) {
		withCwd(t, t.TempDir())
		withArgs(t, "engram", "sync", "--project", "explicit-proj")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("sync with --project should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, `Exporting memories for project "explicit-proj"`) {
			t.Fatalf("expected explicit project output, got: %q", stdout)
		}
	})

	t.Run("setup interactive install error", func(t *testing.T) {
		setupSupportedAgents = func() []setup.Agent {
			return []setup.Agent{{Name: "codex", Description: "Codex", InstallDir: "/tmp/codex"}}
		}
		scanInputLine = func(a ...any) (int, error) {
			p := a[0].(*string)
			*p = "1"
			return 1, nil
		}
		setupInstallAgent = func(string) (*setup.Result, error) {
			return nil, errors.New("forced setup error")
		}

		withArgs(t, "engram", "setup")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSetup() })
		assertFatal(t, stderr, recovered, "forced setup error")
	})
}

func TestCmdMCP(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	assertFatal := func(t *testing.T, stderr string, recovered any, want string) {
		t.Helper()
		code, ok := recovered.(exitCode)
		if !ok || int(code) != 1 {
			t.Fatalf("expected exit code 1 panic, got %v", recovered)
		}
		if !strings.Contains(stderr, want) {
			t.Fatalf("expected stderr to contain %q, got %q", want, stderr)
		}
	}

	t.Run("no tools filter uses newMCPServer", func(t *testing.T) {
		called := false
		newMCPServer = func(s *store.Store) *mcpserver.MCPServer {
			called = true
			return mcpserver.NewMCPServer("test", "0")
		}
		withArgs(t, "engram", "mcp")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if !called {
			t.Fatal("expected newMCPServer to be called")
		}
	})

	t.Run("--tools flag uses newMCPServerWithTools", func(t *testing.T) {
		var gotAllowlist map[string]bool
		newMCPServerWithTools = func(s *store.Store, allowlist map[string]bool) *mcpserver.MCPServer {
			gotAllowlist = allowlist
			return mcpserver.NewMCPServer("test", "0")
		}
		withArgs(t, "engram", "mcp", "--tools=agent")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if gotAllowlist == nil {
			t.Fatal("expected newMCPServerWithTools to be called with non-nil allowlist")
		}
	})

	t.Run("--tools as separate arg uses newMCPServerWithTools", func(t *testing.T) {
		var gotAllowlist map[string]bool
		newMCPServerWithTools = func(s *store.Store, allowlist map[string]bool) *mcpserver.MCPServer {
			gotAllowlist = allowlist
			return mcpserver.NewMCPServer("test", "0")
		}
		withArgs(t, "engram", "mcp", "--tools", "agent")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if gotAllowlist == nil {
			t.Fatal("expected newMCPServerWithTools to be called with non-nil allowlist")
		}
	})

	t.Run("storeNew failure calls fatal", func(t *testing.T) {
		storeNew = func(cfg store.Config) (*store.Store, error) {
			return nil, errors.New("db open failed")
		}
		withArgs(t, "engram", "mcp")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		assertFatal(t, stderr, recovered, "db open failed")
	})

	t.Run("serveMCP failure calls fatal", func(t *testing.T) {
		storeNew = store.New
		serveMCP = func(_ *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error {
			return errors.New("stdio failed")
		}
		withArgs(t, "engram", "mcp")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		assertFatal(t, stderr, recovered, "stdio failed")
	})
}
