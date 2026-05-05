package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	"github.com/Gentleman-Programming/engram/internal/cloud/autosync"
	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/internal/mcp"
	engramsrv "github.com/Gentleman-Programming/engram/internal/server"
	"github.com/Gentleman-Programming/engram/internal/setup"
	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
	"github.com/Gentleman-Programming/engram/internal/tui"
	versioncheck "github.com/Gentleman-Programming/engram/internal/version"

	tea "github.com/charmbracelet/bubbletea"
	mcpserver "github.com/mark3labs/mcp-go/server"
	_ "modernc.org/sqlite"
)

type exitCode int

type stubCloudAutosyncManager struct {
	onRun    func()
	notified int
}

type stubCloudRuntimeServer struct {
	started bool
	err     error
}

type stubManifestReader struct {
	manifest *engramsync.Manifest
	err      error
}

func (s stubManifestReader) ReadManifest(context.Context, string) (*engramsync.Manifest, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.manifest != nil {
		return s.manifest, nil
	}
	return &engramsync.Manifest{}, nil
}

func (s *stubCloudRuntimeServer) Start() error {
	s.started = true
	return s.err
}

func (m *stubCloudAutosyncManager) Run(ctx context.Context) {
	if m.onRun != nil {
		m.onRun()
	}
	<-ctx.Done()
}

func (m *stubCloudAutosyncManager) NotifyDirty() {
	m.notified++
}

func (m *stubCloudAutosyncManager) Status() cloudSyncStatus {
	return cloudSyncStatus{Phase: "idle"}
}

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
	oldNewCloudAutosyncManager := newCloudAutosyncManager
	oldCheckForUpdates := checkForUpdates

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
	newCloudAutosyncManager = func(*store.Store, any) cloudAutosyncManager { return &stubCloudAutosyncManager{} }
	checkForUpdates = func(string) versioncheck.CheckResult {
		return versioncheck.CheckResult{Status: versioncheck.StatusUpToDate}
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
		newCloudAutosyncManager = oldNewCloudAutosyncManager
		checkForUpdates = oldCheckForUpdates
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

func TestCmdServeAutosyncLifecycleGating(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)

	t.Run("local-only serve does not start autosync", func(t *testing.T) {
		cfg := testConfig(t)
		t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "")

		started := false
		newCloudAutosyncManager = func(*store.Store, any) cloudAutosyncManager {
			started = true
			return &stubCloudAutosyncManager{}
		}

		withArgs(t, "engram", "serve", "9011")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("local serve should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if started {
			t.Fatal("autosync manager must remain off for local-only serve")
		}
	})

	t.Run("cloud autosync env with token and server starts successfully", func(t *testing.T) {
		// REQ-210: inverted test — with valid config, serve starts WITHOUT fatal.
		cfg := testConfig(t)
		t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
		t.Setenv("ENGRAM_CLOUD_TOKEN", "test-token")
		t.Setenv("ENGRAM_CLOUD_SERVER", "http://127.0.0.1:9999")

		withArgs(t, "engram", "serve", "9011")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
		// Must NOT call fatal / panic with exitCode.
		if _, ok := recovered.(exitCode); ok {
			t.Fatalf("expected serve to start without fatal, got exitCode panic; stderr=%q", stderr)
		}
		if strings.Contains(stderr, "cloud autosync is not available") {
			t.Fatalf("should not get autosync unavailability message; stderr=%q", stderr)
		}
	})
}

func TestAutosyncEnvAbsent(t *testing.T) {
	// REQ-210: ENGRAM_CLOUD_AUTOSYNC not set → autosync does not start.
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "tok")
	t.Setenv("ENGRAM_CLOUD_SERVER", "http://localhost:9999")

	withArgs(t, "engram", "serve", "9111")
	_, _, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("serve should not fatal when autosync is absent")
	}
}

func TestAutosyncEnvNotOne(t *testing.T) {
	// REQ-210: ENGRAM_CLOUD_AUTOSYNC=true (not "1") → autosync does not start.
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "true")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "tok")
	t.Setenv("ENGRAM_CLOUD_SERVER", "http://localhost:9999")

	withArgs(t, "engram", "serve", "9111")
	_, _, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("serve should not fatal when ENGRAM_CLOUD_AUTOSYNC=true (not '1')")
	}
}

func TestAutosyncGatingTokenMissing(t *testing.T) {
	// REQ-211: token missing → skip autosync with error log, serve continues.
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_SERVER", "http://localhost:9999")

	withArgs(t, "engram", "serve", "9112")
	_, _, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("serve should continue even when token is missing")
	}
}

func TestAutosyncGatingServerMissing(t *testing.T) {
	// REQ-211: server URL missing → skip autosync with error log, serve continues.
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "tok")
	t.Setenv("ENGRAM_CLOUD_SERVER", "")

	withArgs(t, "engram", "serve", "9113")
	_, _, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("serve should continue even when server URL is missing")
	}
}

func TestAutosyncGatingBothPresent(t *testing.T) {
	// REQ-211: both token and server set → tryStartAutosync returns non-nil manager.
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "tok")
	t.Setenv("ENGRAM_CLOUD_SERVER", "http://localhost:9999")

	withArgs(t, "engram", "serve", "9114")
	_, _, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatal("serve should not fatal when both token and server are present")
	}
}

func TestCmdServeStartsWithoutAutosync(t *testing.T) {
	// REQ-211: serve must start successfully even without autosync.
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "")

	withArgs(t, "engram", "serve", "9115")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if _, ok := recovered.(exitCode); ok {
		t.Fatalf("serve should start without autosync, stderr=%q", stderr)
	}
}

// TestTryStartAutosyncReturnsStopFn verifies BW7:
// tryStartAutosync must return a non-nil stop function when autosync starts,
// so the signal handler can call it before os.Exit to release the sync lease.
//
// BR2-3: Stubs newAutosyncManager with a fully deterministic fake that has no
// goroutines, no WaitGroup, and no real network calls — eliminating the racy
// wg.Add/wg.Wait interleave that occurred when using the real *autosync.Manager.
func TestTryStartAutosyncReturnsStopFn(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "test-token")
	t.Setenv("ENGRAM_CLOUD_SERVER", "http://localhost:9999")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	// BR2-3: stub newAutosyncManager with a deterministic fake.
	// No real goroutines are spawned; Stop() is synchronous and race-free.
	stopCalled := make(chan struct{}, 1)
	fakeMgr := &fakeStartableManager{
		stopFn: func() { stopCalled <- struct{}{} },
	}
	oldNewAutosyncManager := newAutosyncManager
	newAutosyncManager = func(_ *store.Store, _ autosync.CloudTransport, _ autosync.Config) startableAutosyncManager {
		return fakeMgr
	}
	defer func() { newAutosyncManager = oldNewAutosyncManager }()

	_, stopFn := tryStartAutosync(ctx, s, cfg)
	if stopFn == nil {
		t.Fatal("expected tryStartAutosync to return a non-nil stop function when autosync is enabled")
	}
	// stopFn must not panic and must return synchronously.
	stopFn()
	select {
	case <-stopCalled:
		// expected
	default:
		t.Fatal("expected Stop to be called via stopFn")
	}
}

// fakeStartableManager is a deterministic fake implementing startableAutosyncManager.
// Run exits immediately (no goroutines); Stop is synchronous and calls stopFn.
// BR2-3: Used to stub newAutosyncManager in tests.
type fakeStartableManager struct {
	runFn  func(context.Context)
	stopFn func()
}

func (f *fakeStartableManager) Run(ctx context.Context) {
	if f.runFn != nil {
		f.runFn(ctx)
	}
} // exits immediately — no goroutine spawned unless runFn blocks
func (f *fakeStartableManager) Stop() {
	if f.stopFn != nil {
		f.stopFn()
	}
}
func (f *fakeStartableManager) Status() autosync.Status {
	return autosync.Status{Phase: autosync.PhaseIdle}
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

func TestCloudCommandIsolationDoesNotMutateLocalState(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cfg := testConfig(t)
	cfg.DataDir = filepath.Join(tmpHome, ".engram")

	withArgs(t, "engram", "cloud", "status")
	_, _, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil {
		t.Fatalf("cloud status should not exit: %v", recovered)
	}

	if _, err := os.Stat(filepath.Join(cfg.DataDir, "engram.db")); err == nil {
		t.Fatalf("cloud status should not create local database")
	}

	if _, err := os.Stat(filepath.Join(cfg.DataDir, "cloud.json")); err == nil {
		t.Fatalf("cloud status should not mutate cloud config")
	}
}

func TestCmdSaveCreatesManualSessionWithCWDDirectory(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	cwd := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	withArgs(t, "engram", "save", "Manual title", "Manual content", "--project", "manual-proj")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSave(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cmdSave should succeed, panic=%v stderr=%q", recovered, stderr)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	session, err := s.GetSession("manual-save-manual-proj")
	if err != nil {
		t.Fatalf("get manual session: %v", err)
	}
	wantDir, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatalf("resolve cwd symlinks: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(session.Directory)
	if err != nil {
		t.Fatalf("resolve session directory symlinks: %v", err)
	}
	if gotDir != wantDir {
		t.Fatalf("manual session directory = %q, want %q", session.Directory, cwd)
	}
}

func TestCloudEnrollAndSyncHelpDoNotMutateLocalState(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	for _, tc := range []struct {
		name string
		args []string
		run  func(store.Config)
	}{
		{name: "cloud enroll help", args: []string{"engram", "cloud", "enroll", "--help"}, run: cmdCloud},
		{name: "sync help", args: []string{"engram", "sync", "--help"}, run: cmdSync},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpHome := t.TempDir()
			cfg := testConfig(t)
			cfg.DataDir = filepath.Join(tmpHome, ".engram")

			withArgs(t, tc.args...)
			stdout, stderr, recovered := captureOutputAndRecover(t, func() { tc.run(cfg) })
			if recovered != nil || stderr != "" {
				t.Fatalf("help should return cleanly, panic=%v stderr=%q stdout=%q", recovered, stderr, stdout)
			}
			if !strings.Contains(stdout, "usage:") {
				t.Fatalf("expected usage output, got %q", stdout)
			}
			if _, err := os.Stat(filepath.Join(cfg.DataDir, "engram.db")); err == nil {
				t.Fatalf("help should not create local database")
			}
		})
	}
}

func TestUpdateChecksSkipCriticalStartupCommands(t *testing.T) {
	if shouldCheckForUpdates([]string{"mcp"}) {
		t.Fatal("mcp startup must not run update check")
	}
	if shouldCheckForUpdates([]string{"serve"}) {
		t.Fatal("serve startup must not run update check")
	}
	if shouldCheckForUpdates([]string{"cloud", "serve"}) {
		t.Fatal("cloud serve startup must not run update check")
	}
	if !shouldCheckForUpdates([]string{"version"}) {
		t.Fatal("normal commands should keep update output")
	}
}

func TestMainCloudHelpDoesNotCreateLocalDatabase(t *testing.T) {
	stubRuntimeHooks(t)
	dataDir := filepath.Join(t.TempDir(), ".engram")
	t.Setenv("ENGRAM_DATA_DIR", dataDir)
	withArgs(t, "engram", "cloud", "--help")

	stdout, stderr, recovered := captureOutputAndRecover(t, func() { main() })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud help should return cleanly, panic=%v stderr=%q stdout=%q", recovered, stderr, stdout)
	}
	if !strings.Contains(stdout, "usage: engram cloud") {
		t.Fatalf("expected cloud usage output, got %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "engram.db")); err == nil {
		t.Fatal("cloud help should not create local database")
	}
}

func TestCmdCloudStatusDistinguishesAuthAndSyncReadiness(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	t.Run("configured but missing token", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_TOKEN", "")
		t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")
		withArgs(t, "engram", "cloud", "status")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("cloud status should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "Cloud status: configured") {
			t.Fatalf("expected configured output, got %q", stdout)
		}
		if !strings.Contains(stdout, "Auth status: token not configured") || !strings.Contains(stdout, "Sync readiness: ready to attempt") {
			t.Fatalf("expected token-optional readiness output, got %q", stdout)
		}
	})

	t.Run("configured in insecure no-auth mode", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_TOKEN", "")
		t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "1")
		withArgs(t, "engram", "cloud", "status")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("cloud status should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "Auth status: ready") || !strings.Contains(stdout, "insecure local-dev mode") {
			t.Fatalf("expected insecure ready auth output, got %q", stdout)
		}
		if !strings.Contains(stdout, "Sync readiness: ready") {
			t.Fatalf("expected ready sync output in insecure mode, got %q", stdout)
		}
	})

	t.Run("configured with token", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")
		t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")
		withArgs(t, "engram", "cloud", "status")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("cloud status should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "Auth status: ready") || !strings.Contains(stdout, "Sync readiness: ready") {
			t.Fatalf("expected ready readiness output, got %q", stdout)
		}
	})
}

func TestCmdCloudUpgradeDoctorRequiresProjectAndIsDeterministic(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	t.Run("missing project fails loudly", func(t *testing.T) {
		withArgs(t, "engram", "cloud", "upgrade", "doctor")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if _, ok := recovered.(exitCode); !ok {
			t.Fatalf("expected fatal exit for missing --project, got %v", recovered)
		}
		if !strings.Contains(stderr, "--project") || !strings.Contains(stderr, "usage") {
			t.Fatalf("expected usage guidance mentioning --project, got %q", stderr)
		}
	})

	t.Run("deterministic findings for unchanged state", func(t *testing.T) {
		withArgs(t, "engram", "cloud", "upgrade", "doctor", "--project", "proj-a")
		stdout1, stderr1, recovered1 := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered1 != nil || stderr1 != "" {
			t.Fatalf("doctor should succeed, panic=%v stderr=%q", recovered1, stderr1)
		}
		if !strings.Contains(stdout1, "status: blocked") || !strings.Contains(stdout1, "class: repairable") {
			t.Fatalf("expected categorized blocked+repairable output, got %q", stdout1)
		}

		withArgs(t, "engram", "cloud", "upgrade", "doctor", "--project", "proj-a")
		stdout2, stderr2, recovered2 := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered2 != nil || stderr2 != "" {
			t.Fatalf("second doctor should succeed, panic=%v stderr=%q", recovered2, stderr2)
		}
		if stdout1 != stdout2 {
			t.Fatalf("expected deterministic doctor output, got first=%q second=%q", stdout1, stdout2)
		}
	})

	t.Run("policy denied is surfaced from runtime sync state", func(t *testing.T) {
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		targetKey := cloudTargetKeyForProject("proj-a")
		if err := s.MarkSyncBlocked(targetKey, constants.ReasonPolicyForbidden, "project blocked by org policy"); err != nil {
			t.Fatalf("seed policy denied sync state: %v", err)
		}

		withArgs(t, "engram", "cloud", "upgrade", "doctor", "--project", "proj-a")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("doctor should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "status: blocked") || !strings.Contains(stdout, "class: policy") || !strings.Contains(stdout, "reason_code: policy_forbidden") {
			t.Fatalf("expected policy-denied diagnosis output, got %q", stdout)
		}
	})

	t.Run("legacy payload gaps are surfaced by doctor and block bootstrap preflight", func(t *testing.T) {
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		if err := s.CreateSession("legacy-s1", "proj-legacy", "/tmp/proj-legacy"); err != nil {
			_ = s.Close()
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(store.AddObservationParams{SessionID: "legacy-s1", Type: "decision", Title: "Authoritative title", Content: "Authoritative content", Project: "proj-legacy", Scope: "project"}); err != nil {
			_ = s.Close()
			t.Fatalf("add observation: %v", err)
		}
		if err := s.EnrollProject("proj-legacy"); err != nil {
			_ = s.Close()
			t.Fatalf("enroll project: %v", err)
		}
		_ = s.Close()

		db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
		if err != nil {
			t.Fatalf("open raw db: %v", err)
		}
		defer db.Close()

		var syncID string
		if err := db.QueryRow(`SELECT sync_id FROM observations WHERE session_id = ? ORDER BY id DESC LIMIT 1`, "legacy-s1").Scan(&syncID); err != nil {
			t.Fatalf("lookup sync id: %v", err)
		}
		payload := `{"sync_id":"` + syncID + `","session_id":"legacy-s1","type":"decision","content":"legacy payload missing title","scope":"project"}`
		if _, err := db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			store.DefaultSyncTargetKey,
			store.SyncEntityObservation,
			syncID,
			store.SyncOpUpsert,
			payload,
			store.SyncSourceLocal,
			"proj-legacy",
		); err != nil {
			t.Fatalf("insert malformed mutation: %v", err)
		}

		withArgs(t, "engram", "cloud", "upgrade", "doctor", "--project", "proj-legacy")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("doctor should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "status: blocked") || !strings.Contains(stdout, "class: repairable") || !strings.Contains(stdout, "reason_code: upgrade_repairable_legacy_mutation_payload") {
			t.Fatalf("expected doctor to classify legacy mutation as repairable blocker, got %q", stdout)
		}

		bootstrapCalled := false
		oldBootstrap := runUpgradeBootstrap
		runUpgradeBootstrap = func(_ *store.Store, _ string, _ *cloudConfig) (*engramsync.UpgradeBootstrapResult, error) {
			bootstrapCalled = true
			return &engramsync.UpgradeBootstrapResult{Project: "proj-legacy", Stage: store.UpgradeStageBootstrapVerified}, nil
		}
		t.Cleanup(func() { runUpgradeBootstrap = oldBootstrap })

		withArgs(t, "engram", "cloud", "upgrade", "bootstrap", "--project", "proj-legacy")
		_, bootstrapStderr, bootstrapRecovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if _, ok := bootstrapRecovered.(exitCode); !ok {
			t.Fatalf("expected bootstrap preflight to fail loudly, got %v", bootstrapRecovered)
		}
		if bootstrapCalled {
			t.Fatal("bootstrap preflight must block before running bootstrap orchestration")
		}
		if !strings.Contains(bootstrapStderr, "legacy mutation payloads require repair") {
			t.Fatalf("expected actionable legacy-repair guidance, got %q", bootstrapStderr)
		}
	})
}

func TestCmdSyncCloudPreflightsLegacyMutationPayloads(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.CreateSession("sync-legacy-s1", "sync-legacy", "/tmp/sync-legacy"); err != nil {
		_ = s.Close()
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{SessionID: "sync-legacy-s1", Type: "decision", Title: "Canonical title", Content: "Canonical content", Project: "sync-legacy", Scope: "project"}); err != nil {
		_ = s.Close()
		t.Fatalf("add observation: %v", err)
	}
	if err := s.EnrollProject("sync-legacy"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	_ = s.Close()

	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer db.Close()
	var syncID string
	if err := db.QueryRow(`SELECT sync_id FROM observations WHERE session_id = ? ORDER BY id DESC LIMIT 1`, "sync-legacy-s1").Scan(&syncID); err != nil {
		t.Fatalf("lookup sync id: %v", err)
	}
	legacyPayload := `{"sync_id":"` + syncID + `","session_id":"sync-legacy-s1","type":"decision","content":"legacy payload missing title","scope":"project"}`
	if _, err := db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		store.DefaultSyncTargetKey,
		store.SyncEntityObservation,
		syncID,
		store.SyncOpUpsert,
		legacyPayload,
		store.SyncSourceLocal,
		"sync-legacy",
	); err != nil {
		t.Fatalf("insert legacy mutation: %v", err)
	}

	exportCalled := false
	oldSyncExport := syncExport
	syncExport = func(_ *engramsync.Syncer, _, _ string) (*engramsync.SyncResult, error) {
		exportCalled = true
		return &engramsync.SyncResult{}, nil
	}
	t.Cleanup(func() { syncExport = oldSyncExport })

	withArgs(t, "engram", "sync", "--cloud", "--project", "sync-legacy")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected cloud sync preflight to fail loudly, got %v", recovered)
	}
	if exportCalled {
		t.Fatal("cloud sync must block before export/canonicalization")
	}
	if !strings.Contains(stderr, "legacy mutation payloads require repair before cloud sync") ||
		!strings.Contains(stderr, "engram cloud upgrade doctor --project sync-legacy") ||
		!strings.Contains(stderr, "engram cloud upgrade repair --project sync-legacy --apply") {
		t.Fatalf("expected actionable legacy mutation guidance, got %q", stderr)
	}

	var persistedPayload string
	if err := db.QueryRow(`SELECT payload FROM sync_mutations WHERE project = ? AND payload = ?`, "sync-legacy", legacyPayload).Scan(&persistedPayload); err != nil {
		t.Fatalf("expected sync preflight not to auto-repair payload: %v", err)
	}
}

func TestCmdCloudUpgradeBootstrapStatusAndRollbackSemantics(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	t.Run("status shows stage and reason", func(t *testing.T) {
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{Project: "proj-a", Stage: store.UpgradeStageBootstrapPushed, RepairClass: store.UpgradeRepairClassRepairable, LastErrorCode: "upgrade_repair_backfill_sync_journal", LastErrorMessage: "repair pending"}); err != nil {
			t.Fatalf("seed upgrade state: %v", err)
		}

		withArgs(t, "engram", "cloud", "upgrade", "status", "--project", "proj-a")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("status should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "stage: bootstrap_pushed") || !strings.Contains(stdout, "reason_code: upgrade_repair_backfill_sync_journal") {
			t.Fatalf("expected stage+reason in status output, got %q", stdout)
		}
	})

	t.Run("rollback blocked after bootstrap verified", func(t *testing.T) {
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{Project: "proj-a", Stage: store.UpgradeStageBootstrapVerified, RepairClass: store.UpgradeRepairClassReady}); err != nil {
			t.Fatalf("seed verified state: %v", err)
		}

		withArgs(t, "engram", "cloud", "upgrade", "rollback", "--project", "proj-a")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if _, ok := recovered.(exitCode); !ok {
			t.Fatalf("expected rollback to fail after verified boundary, got %v", recovered)
		}
		if !strings.Contains(stderr, "rollback is unavailable post-bootstrap") {
			t.Fatalf("expected explicit rollback boundary message, got %q", stderr)
		}
	})

	t.Run("bootstrap resume flag accepted", func(t *testing.T) {
		oldBootstrap := runUpgradeBootstrap
		runUpgradeBootstrap = func(_ *store.Store, project string, _ *cloudConfig) (*engramsync.UpgradeBootstrapResult, error) {
			return &engramsync.UpgradeBootstrapResult{Project: project, Stage: store.UpgradeStageBootstrapVerified, Resumed: true, NoOp: false}, nil
		}
		t.Cleanup(func() { runUpgradeBootstrap = oldBootstrap })

		withArgs(t, "engram", "cloud", "upgrade", "bootstrap", "--project", "proj-a", "--resume")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("bootstrap should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "stage: bootstrap_verified") {
			t.Fatalf("expected verified bootstrap stage output, got %q", stdout)
		}
	})

	t.Run("bootstrap captures rollback snapshot before progression", func(t *testing.T) {
		captured := false
		oldBootstrap := runUpgradeBootstrap
		runUpgradeBootstrap = func(s *store.Store, project string, _ *cloudConfig) (*engramsync.UpgradeBootstrapResult, error) {
			captured = true
			state, err := s.GetCloudUpgradeState(project)
			if err != nil {
				return nil, fmt.Errorf("load state inside bootstrap stub: %w", err)
			}
			if state == nil {
				return nil, fmt.Errorf("expected pre-bootstrap state snapshot")
			}
			if !state.Snapshot.CloudConfigPresent {
				return nil, fmt.Errorf("expected snapshot cloud config presence to be true")
			}
			if !strings.Contains(state.Snapshot.CloudConfigJSON, "cloud.example.test") {
				return nil, fmt.Errorf("expected snapshot cloud config json to include configured server")
			}
			if state.Snapshot.ProjectEnrolled {
				return nil, fmt.Errorf("expected snapshot to preserve pre-bootstrap unenrolled state")
			}
			return &engramsync.UpgradeBootstrapResult{Project: project, Stage: store.UpgradeStageBootstrapVerified}, nil
		}
		t.Cleanup(func() { runUpgradeBootstrap = oldBootstrap })

		withArgs(t, "engram", "cloud", "upgrade", "bootstrap", "--project", "proj-a", "--resume")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("bootstrap should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !captured {
			t.Fatal("expected bootstrap stub to verify snapshot capture")
		}
		if !strings.Contains(stdout, "stage: bootstrap_verified") {
			t.Fatalf("expected verified bootstrap stage output, got %q", stdout)
		}
	})
}

func TestCmdCloudUpgradeRepairStatusAndRollbackBranches(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	t.Run("repair requires project", func(t *testing.T) {
		cfg := testConfig(t)
		withArgs(t, "engram", "cloud", "upgrade", "repair")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if _, ok := recovered.(exitCode); !ok {
			t.Fatalf("expected fatal exit for missing --project, got %v", recovered)
		}
		if !strings.Contains(stderr, "usage: engram cloud upgrade repair") || !strings.Contains(stderr, "--project") {
			t.Fatalf("expected usage guidance with --project, got %q", stderr)
		}
	})

	t.Run("repair dry-run stays non-applied", func(t *testing.T) {
		cfg := testConfig(t)
		withArgs(t, "engram", "cloud", "upgrade", "repair", "--project", "proj-a")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("repair should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "class: blocked") || !strings.Contains(stdout, "applied: false") {
			t.Fatalf("expected deterministic blocked dry-run output, got %q", stdout)
		}
	})

	t.Run("repair apply flag is accepted", func(t *testing.T) {
		cfg := testConfig(t)
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		if err := s.CreateSession("repair-s1", "proj-a", "/tmp/proj-a"); err != nil {
			_ = s.Close()
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(store.AddObservationParams{SessionID: "repair-s1", Type: "decision", Title: "repair", Content: "repair", Project: "proj-a", Scope: "project"}); err != nil {
			_ = s.Close()
			t.Fatalf("seed observation: %v", err)
		}
		if err := s.EnrollProject("proj-a"); err != nil {
			_ = s.Close()
			t.Fatalf("enroll project: %v", err)
		}
		_ = s.Close()

		withArgs(t, "engram", "cloud", "upgrade", "repair", "--project", "proj-a", "--apply")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("repair apply should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "class: ready") || !strings.Contains(stdout, "applied: false") {
			t.Fatalf("expected successful apply-flag execution output, got %q", stdout)
		}
	})

	t.Run("status defaults to planned when state is absent", func(t *testing.T) {
		cfg := testConfig(t)
		withArgs(t, "engram", "cloud", "upgrade", "status", "--project", "proj-a")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("status should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "stage: planned") {
			t.Fatalf("expected planned default stage output, got %q", stdout)
		}
	})

	t.Run("rollback requires existing checkpoint state", func(t *testing.T) {
		cfg := testConfig(t)
		withArgs(t, "engram", "cloud", "upgrade", "rollback", "--project", "proj-a")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if _, ok := recovered.(exitCode); !ok {
			t.Fatalf("expected fatal rollback exit without checkpoint state, got %v", recovered)
		}
		if !strings.Contains(stderr, "rollback requires existing upgrade checkpoint state") {
			t.Fatalf("expected missing-checkpoint rollback error, got %q", stderr)
		}
	})

	t.Run("rollback restores cloud config when snapshot captured it", func(t *testing.T) {
		cfg := testConfig(t)
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{
			Project:     "proj-a",
			Stage:       store.UpgradeStageBootstrapPushed,
			RepairClass: store.UpgradeRepairClassRepairable,
			Snapshot: store.CloudUpgradeSnapshot{
				CloudConfigPresent: true,
				CloudConfigJSON:    `{"server_url":"https://rollback.example.test"}`,
				ProjectEnrolled:    false,
			},
		}); err != nil {
			_ = s.Close()
			t.Fatalf("seed rollback state: %v", err)
		}
		_ = s.Close()

		withArgs(t, "engram", "cloud", "upgrade", "rollback", "--project", "proj-a")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("rollback should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "stage: rolled_back") {
			t.Fatalf("expected rolled_back stage output, got %q", stdout)
		}
		data, err := os.ReadFile(filepath.Join(cfg.DataDir, "cloud.json"))
		if err != nil {
			t.Fatalf("expected restored cloud config file: %v", err)
		}
		if !strings.Contains(string(data), "rollback.example.test") {
			t.Fatalf("expected restored cloud config content, got %q", string(data))
		}
	})

	t.Run("rollback removes cloud config when snapshot had none", func(t *testing.T) {
		cfg := testConfig(t)
		if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
			t.Fatalf("seed current cloud config: %v", err)
		}
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{
			Project:     "proj-a",
			Stage:       store.UpgradeStageBootstrapPushed,
			RepairClass: store.UpgradeRepairClassRepairable,
			Snapshot: store.CloudUpgradeSnapshot{
				CloudConfigPresent: false,
				ProjectEnrolled:    false,
			},
		}); err != nil {
			_ = s.Close()
			t.Fatalf("seed rollback state: %v", err)
		}
		_ = s.Close()

		withArgs(t, "engram", "cloud", "upgrade", "rollback", "--project", "proj-a")
		stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("rollback should succeed, panic=%v stderr=%q", recovered, stderr)
		}
		if !strings.Contains(stdout, "stage: rolled_back") {
			t.Fatalf("expected rolled_back stage output, got %q", stdout)
		}
		if _, err := os.Stat(filepath.Join(cfg.DataDir, "cloud.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected cloud config to be removed, err=%v", err)
		}
	})
}

func TestCmdCloudUpgradeHelpShowsGuidedWorkflow(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)
	cfg := testConfig(t)

	withArgs(t, "engram", "cloud", "upgrade", "--help")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("upgrade help should succeed, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "doctor -> repair -> bootstrap -> status/rollback") {
		t.Fatalf("expected guided workflow in help output, got %q", stdout)
	}
	if !strings.Contains(stdout, "local SQLite remains source of truth") {
		t.Fatalf("expected local-first semantics in help output, got %q", stdout)
	}
}

func TestCloudUpgradeDocsMatchHelpAndLocalFirstSemantics(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)
	cfg := testConfig(t)

	withArgs(t, "engram", "cloud", "upgrade", "--help")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("upgrade help should succeed, panic=%v stderr=%q", recovered, stderr)
	}

	helpRequired := []string{
		"doctor -> repair -> bootstrap -> status/rollback",
		"local SQLite remains source of truth",
	}
	for _, token := range helpRequired {
		if !strings.Contains(stdout, token) {
			t.Fatalf("help output missing %q, got %q", token, stdout)
		}
	}

	read := func(pathParts ...string) string {
		t.Helper()
		path := filepath.Join(pathParts...)
		bytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(bytes)
	}

	readme := read("..", "..", "README.md")
	docs := read("..", "..", "DOCS.md")
	agentSetup := read("..", "..", "docs", "AGENT-SETUP.md")
	plugins := read("..", "..", "docs", "PLUGINS.md")

	commandExamples := []string{
		"engram cloud upgrade doctor --project",
		"engram cloud upgrade repair --project",
		"engram cloud upgrade bootstrap --project",
		"engram cloud upgrade status --project",
	}
	for _, cmd := range commandExamples {
		if !strings.Contains(readme, cmd) {
			t.Fatalf("README missing command example %q", cmd)
		}
		if !strings.Contains(docs, cmd) {
			t.Fatalf("DOCS missing command example %q", cmd)
		}
	}

	localFirstTokens := []string{
		"local SQLite",
		"replication/shared access",
	}
	for _, token := range localFirstTokens {
		if !strings.Contains(strings.ToLower(readme), strings.ToLower(token)) {
			t.Fatalf("README missing local-first token %q", token)
		}
		if !strings.Contains(strings.ToLower(docs), strings.ToLower(token)) {
			t.Fatalf("DOCS missing local-first token %q", token)
		}
	}

	if !strings.Contains(strings.ToLower(agentSetup), "deferred") || !strings.Contains(agentSetup, "engram cloud") {
		t.Fatalf("AGENT-SETUP must describe deferred automation/manual cloud CLI flow")
	}
	if !strings.Contains(strings.ToLower(plugins), "deferred") || !strings.Contains(plugins, "engram cloud") {
		t.Fatalf("PLUGINS must describe deferred automation/manual cloud CLI flow")
	}
}

func TestCloudDashboardDocsEnablementFlowIsExecutable(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)
	cfg := testConfig(t)

	read := func(pathParts ...string) string {
		t.Helper()
		path := filepath.Join(pathParts...)
		bytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(bytes)
	}

	readme := read("..", "..", "README.md")
	docs := read("..", "..", "DOCS.md")
	for _, token := range []string{
		"engram cloud config --server",
		"engram cloud enroll smoke-project",
		"/dashboard/login",
		"/dashboard/contributors",
		"ENGRAM_CLOUD_TOKEN",
		"ENGRAM_JWT_SECRET",
		"ENGRAM_CLOUD_ADMIN",
	} {
		if !strings.Contains(readme, token) && !strings.Contains(docs, token) {
			t.Fatalf("expected docs-backed enablement token %q in README or DOCS", token)
		}
	}

	withArgs(t, "engram", "cloud", "config", "--server", "http://127.0.0.1:18080")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud config should succeed, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Cloud server set") {
		t.Fatalf("expected cloud config success output, got %q", stdout)
	}

	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	withArgs(t, "engram", "cloud", "status")
	stdout, stderr, recovered = captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud status should succeed after config, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Cloud status: configured") {
		t.Fatalf("expected configured cloud status output, got %q", stdout)
	}

	withArgs(t, "engram", "cloud", "enroll", "smoke-project")
	stdout, stderr, recovered = captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud enroll should succeed, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "enrolled for cloud sync") {
		t.Fatalf("expected enroll success output, got %q", stdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	enrolled, err := s.IsProjectEnrolled("smoke-project")
	if err != nil {
		t.Fatalf("check enrolled project: %v", err)
	}
	if !enrolled {
		t.Fatal("expected smoke-project to be enrolled after docs flow command sequence")
	}

	t.Setenv("ENGRAM_JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "smoke-project")
	t.Setenv("ENGRAM_CLOUD_ADMIN", "token-abc")

	var seen cloud.Config
	runtimeStub := &stubCloudRuntimeServer{}
	oldRuntime := newCloudRuntime
	newCloudRuntime = func(c cloud.Config) (cloudServerRuntime, error) {
		seen = c
		return runtimeStub, nil
	}
	t.Cleanup(func() { newCloudRuntime = oldRuntime })

	withArgs(t, "engram", "cloud", "serve")
	_, stderr, recovered = captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud serve should succeed for docs-backed authenticated runtime, panic=%v stderr=%q", recovered, stderr)
	}
	if !runtimeStub.started {
		t.Fatal("expected cloud runtime start to be called in docs-backed flow")
	}
	if seen.AdminToken != "token-abc" {
		t.Fatalf("expected ENGRAM_CLOUD_ADMIN to flow into runtime config, got %q", seen.AdminToken)
	}
}

func TestCmdCloudStatusHonorsEnvServerOverride(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://env-cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "env-token")

	withArgs(t, "engram", "cloud", "status")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud status should succeed, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Cloud status: configured") {
		t.Fatalf("expected configured output, got %q", stdout)
	}
	if !strings.Contains(stdout, "Server: https://env-cloud.example.test") {
		t.Fatalf("expected env server override to be reported, got %q", stdout)
	}
	if !strings.Contains(stdout, "Auth status: ready") {
		t.Fatalf("expected ready auth state with env token, got %q", stdout)
	}
}

func TestCmdCloudStatusSurfacesPersistedNonEnrolledPendingDiagnostic(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://env-cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "env-token")
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.MarkSyncBlocked(store.DefaultSyncTargetKey, constants.ReasonNonEnrolledPendingMutations, "pending cloud sync mutations are blocked because project(s) are not enrolled: alpha=2. Run `engram cloud enroll <project>` for each intended project or review enrollment."); err != nil {
		_ = s.Close()
		t.Fatalf("mark blocked: %v", err)
	}
	_ = s.Close()

	withArgs(t, "engram", "cloud", "status")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud status should succeed, panic=%v stderr=%q", recovered, stderr)
	}
	for _, want := range []string{"Sync diagnostic: degraded", "reason_code: non_enrolled_pending_mutations", "engram cloud enroll <project>"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected cloud status output to contain %q, got %q", want, stdout)
		}
	}
}

func TestCmdCloudStatusRejectsInvalidEffectiveRuntimeServerURL(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}

	t.Setenv("ENGRAM_CLOUD_SERVER", "https://env-cloud.example.test?debug=1")
	withArgs(t, "engram", "cloud", "status")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit for invalid runtime server url, got %v", recovered)
	}
	if strings.Contains(stdout, "Cloud status: configured") {
		t.Fatalf("status must not report configured when runtime url is invalid, stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "invalid cloud runtime server URL") || !strings.Contains(stderr, "query is not allowed") {
		t.Fatalf("expected runtime URL validation error in stderr, got %q", stderr)
	}
}

func TestResolveCloudRuntimeConfigReturnsErrorWhenPersistedConfigUnreadable(t *testing.T) {
	cfg := testConfig(t)
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "cloud.json"), []byte("{invalid-json"), 0644); err != nil {
		t.Fatalf("write invalid cloud config: %v", err)
	}

	runtimeCfg, err := resolveCloudRuntimeConfig(cfg)
	if err == nil {
		t.Fatal("expected cloud runtime config error for malformed cloud.json")
	}
	if !strings.Contains(err.Error(), "read cloud config") {
		t.Fatalf("expected read cloud config context, got %v", err)
	}
	if runtimeCfg != nil {
		t.Fatalf("expected nil runtime config on malformed file, got %+v", runtimeCfg)
	}
}

func TestResolveCloudRuntimeConfigIgnoresPersistedTokenWithoutEnvOverride(t *testing.T) {
	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test", Token: "legacy-token"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	runtimeCfg, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("resolve cloud runtime config: %v", err)
	}
	if runtimeCfg == nil {
		t.Fatal("expected non-nil cloud runtime config")
	}
	if runtimeCfg.Token != "" {
		t.Fatalf("expected persisted legacy token to be ignored, got %q", runtimeCfg.Token)
	}
	if runtimeCfg.ServerURL != "https://cloud.example.test" {
		t.Fatalf("expected server URL to remain available, got %q", runtimeCfg.ServerURL)
	}
}

func TestCmdCloudConfigRejectsInvalidServerURL(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	tests := []string{
		"cloud.example.test",
		"ftp://cloud.example.test",
		"http://",
		"://bad-url",
		"https://cloud.example.test?debug=1",
		"https://cloud.example.test#dev",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			withArgs(t, "engram", "cloud", "config", "--server", input)
			_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
			if _, ok := recovered.(exitCode); !ok {
				t.Fatalf("expected fatal exit for invalid URL, got %v", recovered)
			}
			if !strings.Contains(stderr, "invalid server URL") {
				t.Fatalf("expected invalid server URL error, got %q", stderr)
			}
		})
	}
}

func TestCmdCloudConfigAcceptsValidServerURL(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	withArgs(t, "engram", "cloud", "config", "--server", "https://cloud.example.test")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected cloud config success, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Cloud server set") {
		t.Fatalf("expected success output, got %q", stdout)
	}

	cc, err := loadCloudConfig(cfg)
	if err != nil {
		t.Fatalf("load cloud config: %v", err)
	}
	if cc == nil || cc.ServerURL != "https://cloud.example.test" {
		t.Fatalf("expected persisted server URL, got %+v", cc)
	}
}

func TestCmdCloudStatusSurfacesCloudConfigParseError(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "cloud.json"), []byte("{invalid-json"), 0644); err != nil {
		t.Fatalf("write invalid cloud config: %v", err)
	}

	withArgs(t, "engram", "cloud", "status")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit on malformed cloud config, got %v", recovered)
	}
	if strings.Contains(stdout, "Cloud status: not configured") {
		t.Fatalf("cloud status must surface parse error instead of not configured, stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "unable to read cloud runtime config") || !strings.Contains(stderr, "invalid") {
		t.Fatalf("expected parse error surfaced in stderr, got %q", stderr)
	}
}

func TestCmdCloudServeStartsCloudRuntime(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("ENGRAM_CLOUD_HOST", "0.0.0.0")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "proj-a")

	var seen cloud.Config
	runtimeStub := &stubCloudRuntimeServer{}
	oldRuntime := newCloudRuntime
	newCloudRuntime = func(c cloud.Config) (cloudServerRuntime, error) {
		seen = c
		return runtimeStub, nil
	}
	t.Cleanup(func() { newCloudRuntime = oldRuntime })

	withArgs(t, "engram", "cloud", "serve")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cloud serve should succeed, panic=%v stderr=%q", recovered, stderr)
	}
	if !runtimeStub.started {
		t.Fatal("expected cloud runtime server Start to be called")
	}
	if seen.JWTSecret == "" {
		t.Fatal("expected cloud runtime config to include jwt secret")
	}
	if seen.BindHost != "0.0.0.0" {
		t.Fatalf("expected cloud runtime config bind host from env, got %q", seen.BindHost)
	}
}

func TestCmdCloudServeRequiresAuthTokenByDefault(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "proj-a")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")

	withArgs(t, "engram", "cloud", "serve")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "ENGRAM_CLOUD_TOKEN") {
		t.Fatalf("expected auth token requirement error, got %q", stderr)
	}
}

func TestCmdCloudServeRequiresProjectAllowlistByDefault(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")

	withArgs(t, "engram", "cloud", "serve")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "ENGRAM_CLOUD_ALLOWED_PROJECTS") {
		t.Fatalf("expected project allowlist requirement error, got %q", stderr)
	}
}

func TestCmdCloudServeRequiresProjectAllowlistInInsecureMode(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "1")

	withArgs(t, "engram", "cloud", "serve")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "ENGRAM_CLOUD_ALLOWED_PROJECTS") {
		t.Fatalf("expected insecure allowlist requirement error, got %q", stderr)
	}
}

func TestCmdCloudServeInsecureModeDoesNotRequireJWTServiceStartup(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "proj-a")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "1")
	t.Setenv("ENGRAM_JWT_SECRET", "short")

	runtimeStub := &stubCloudRuntimeServer{}
	oldRuntime := newCloudRuntime
	newCloudRuntime = func(c cloud.Config) (cloudServerRuntime, error) {
		return runtimeStub, nil
	}
	t.Cleanup(func() { newCloudRuntime = oldRuntime })

	withArgs(t, "engram", "cloud", "serve")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if recovered != nil {
		t.Fatalf("expected insecure cloud serve startup to continue without JWT-backed auth service, panic=%v stderr=%q", recovered, stderr)
	}
	if !runtimeStub.started {
		t.Fatal("expected cloud runtime start to be called")
	}
	if strings.Contains(stderr, "jwt secret") || strings.Contains(stderr, "ErrSecretTooShort") {
		t.Fatalf("expected insecure startup to ignore jwt secret validation, stderr=%q", stderr)
	}
}

func TestCmdCloudServeInsecureModeRejectsDashboardAdminToken(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "proj-a")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "1")
	t.Setenv("ENGRAM_CLOUD_ADMIN", "admin-token")

	withArgs(t, "engram", "cloud", "serve")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "ENGRAM_CLOUD_ADMIN") || !strings.Contains(stderr, "ENGRAM_CLOUD_INSECURE_NO_AUTH") {
		t.Fatalf("expected clear insecure/admin conflict error, got %q", stderr)
	}
}

func TestCmdCloudServeAuthenticatedModeRequiresExplicitJWTSecret(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_JWT_SECRET", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "proj-a")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")

	withArgs(t, "engram", "cloud", "serve")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "ENGRAM_JWT_SECRET") || !strings.Contains(stderr, "non-default") {
		t.Fatalf("expected explicit jwt secret requirement error, got %q", stderr)
	}
}

func TestCmdCloudServeAuthenticatedModeRejectsDefaultJWTSecret(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_JWT_SECRET", cloud.DefaultJWTSecret)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "proj-a")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")

	withArgs(t, "engram", "cloud", "serve")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "ENGRAM_JWT_SECRET") || !strings.Contains(stderr, "default") {
		t.Fatalf("expected default jwt secret rejection, got %q", stderr)
	}
}

func TestUnconfiguredCloudKeepsLocalCommandDefaults(t *testing.T) {
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	t.Setenv("ENGRAM_CLOUD_SERVER", "")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	cfg := testConfig(t)

	withArgs(t, "engram", "serve", "9011")
	_, _, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if recovered != nil {
		t.Fatalf("serve should keep local path with no cloud config, got %v", recovered)
	}

	withArgs(t, "engram", "mcp")
	_, _, recovered = captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil {
		t.Fatalf("mcp should keep local path with no cloud config, got %v", recovered)
	}

	withArgs(t, "engram", "search")
	_, searchErr, recovered := captureOutputAndRecover(t, func() { cmdSearch(cfg) })
	if code, ok := recovered.(exitCode); !ok || int(code) != 1 {
		t.Fatalf("search missing query should keep exit code 1, got %v", recovered)
	}
	if !strings.Contains(searchErr, "usage: engram search <query>") {
		t.Fatalf("unexpected search usage: %q", searchErr)
	}

	withArgs(t, "engram", "context")
	ctxOut, ctxErr, recovered := captureOutputAndRecover(t, func() { cmdContext(cfg) })
	if recovered != nil {
		t.Fatalf("context should not exit, got %v", recovered)
	}
	if ctxErr != "" {
		t.Fatalf("context stderr must be empty, got %q", ctxErr)
	}
	if !strings.Contains(ctxOut, "No previous session memories found") {
		t.Fatalf("unexpected context output: %q", ctxOut)
	}

	withArgs(t, "engram", "sync", "--status")
	_, _, recovered = captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil {
		t.Fatalf("sync --status should keep local path with no cloud config, got %v", recovered)
	}
}

func TestCmdServeWiresPersistedSyncStatusProvider(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")
	t.Setenv("ENGRAM_PROJECT", "proj-a")
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	withArgs(t, "engram", "serve")

	newHTTPServer = func(s *store.Store, _ int) *engramsrv.Server {
		return engramsrv.New(s, 0)
	}
	startHTTP = func(srv *engramsrv.Server) error {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected /sync/status=200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"enabled":true`) {
			t.Fatalf("expected configured sync status provider, got body=%q", rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"phase":"idle"`) {
			t.Fatalf("expected idle phase from persisted sync state, got body=%q", rec.Body.String())
		}
		return nil
	}

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cmdServe should not fail, panic=%v stderr=%q", recovered, stderr)
	}
}

func TestCmdCloudServeRejectsInsecureModeWhenTokenIsSet(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "proj-a")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "1")

	withArgs(t, "engram", "cloud", "serve")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdCloud(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "ENGRAM_CLOUD_INSECURE_NO_AUTH") || !strings.Contains(stderr, "ENGRAM_CLOUD_TOKEN") {
		t.Fatalf("expected conflicting auth config error, got %q", stderr)
	}
}

func TestCmdServeSyncStatusUsesDetectedProjectScopedState(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.MarkSyncFailure(cloudTargetKeyForProject("proj-a"), "network timeout", time.Now().UTC().Add(30*time.Second)); err != nil {
		_ = s.Close()
		t.Fatalf("mark sync failure: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	t.Setenv("ENGRAM_PROJECT", "proj-a")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	withArgs(t, "engram", "serve")

	newHTTPServer = func(s *store.Store, _ int) *engramsrv.Server {
		return engramsrv.New(s, 0)
	}
	startHTTP = func(srv *engramsrv.Server) error {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected /sync/status=200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"enabled":true`) {
			t.Fatalf("expected enabled=true, got body=%q", body)
		}
		if !strings.Contains(body, `"phase":"degraded"`) || !strings.Contains(body, `"reason_code":"transport_failed"`) {
			t.Fatalf("expected persisted project-scoped degraded sync status, got body=%q", body)
		}
		return nil
	}

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cmdServe should not fail, panic=%v stderr=%q", recovered, stderr)
	}
}

func TestCmdServeSyncStatusRequiresProjectScopeWhenNoDefaultResolves(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)

	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")
	t.Setenv("ENGRAM_PROJECT", "")

	oldDetectProject := detectProject
	detectProject = func(string) string { return "" }
	t.Cleanup(func() { detectProject = oldDetectProject })

	withArgs(t, "engram", "serve")
	newHTTPServer = func(s *store.Store, _ int) *engramsrv.Server {
		return engramsrv.New(s, 0)
	}
	startHTTP = func(srv *engramsrv.Server) error {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected /sync/status=200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"enabled":false`) {
			t.Fatalf("expected enabled=false when no project scope resolves, got body=%q", body)
		}
		if !strings.Contains(body, `"reason_code":"project_required"`) {
			t.Fatalf("expected project_required reason code, got body=%q", body)
		}
		return nil
	}

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cmdServe should not fail, panic=%v stderr=%q", recovered, stderr)
	}
}

func TestCmdServeSyncStatusAllowsProjectOverridePerRequest(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-b"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll proj-b: %v", err)
	}
	if err := s.MarkSyncFailure(cloudTargetKeyForProject("proj-a"), "timeout a", time.Now().UTC().Add(30*time.Second)); err != nil {
		_ = s.Close()
		t.Fatalf("mark proj-a degraded: %v", err)
	}
	if err := s.MarkSyncHealthy(cloudTargetKeyForProject("proj-b")); err != nil {
		_ = s.Close()
		t.Fatalf("mark proj-b healthy: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	t.Setenv("ENGRAM_PROJECT", "proj-a")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	withArgs(t, "engram", "serve")

	newHTTPServer = func(s *store.Store, _ int) *engramsrv.Server {
		return engramsrv.New(s, 0)
	}
	startHTTP = func(srv *engramsrv.Server) error {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/sync/status?project=proj-b", nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected /sync/status=200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"phase":"healthy"`) {
			t.Fatalf("expected project override to use proj-b healthy state, got body=%q", body)
		}
		return nil
	}

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("cmdServe should not fail, panic=%v stderr=%q", recovered, stderr)
	}
}

func TestStoreSyncStatusProviderRequiresExplicitProjectScope(t *testing.T) {
	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	status := storeSyncStatusProvider{store: s, defaultProject: "", cfg: cfg}.Status("")
	if status.Enabled {
		t.Fatalf("expected enabled=false when no project scope resolves, got %+v", status)
	}
	if status.Phase != store.SyncLifecycleIdle {
		t.Fatalf("expected idle phase without explicit project scope, got %q", status.Phase)
	}
	if status.ReasonCode != "project_required" {
		t.Fatalf("expected project_required reason code, got %q", status.ReasonCode)
	}
	if !strings.Contains(status.ReasonMessage, "explicit project") {
		t.Fatalf("expected explicit project message, got %q", status.ReasonMessage)
	}
}

func TestStoreSyncStatusProviderDisabledWhenCloudNotConfigured(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	status := storeSyncStatusProvider{store: s, defaultProject: "proj-a", cfg: cfg}.Status("")
	if status.Enabled {
		t.Fatalf("expected enabled=false when cloud runtime config is absent, got %+v", status)
	}
	if status.ReasonCode != "cloud_not_configured" {
		t.Fatalf("expected cloud_not_configured reason, got %q", status.ReasonCode)
	}
}

func TestStoreSyncStatusProviderDoesNotFallbackToLegacyGlobalStateWithoutProjectScope(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.MarkSyncFailure(cloudTargetKeyForProject(""), "legacy global state", time.Now().UTC().Add(30*time.Second)); err != nil {
		t.Fatalf("seed legacy global sync state: %v", err)
	}

	status := storeSyncStatusProvider{store: s, defaultProject: "", cfg: cfg}.Status("")
	if status.Enabled {
		t.Fatalf("expected enabled=false when project scope is unresolved, got %+v", status)
	}
	if status.ReasonCode != "cloud_not_configured" {
		t.Fatalf("expected cloud_not_configured reason without project scope, got %q", status.ReasonCode)
	}
}

func TestStoreSyncStatusProviderUsesPersistedStateWhenCloudConfigMissing(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	targetKey := cloudTargetKeyForProject("proj-a")
	if err := s.MarkSyncBlocked(targetKey, constants.ReasonBlockedUnenrolled, "project \"proj-a\" is not enrolled for cloud sync"); err != nil {
		t.Fatalf("seed blocked sync state: %v", err)
	}

	status := storeSyncStatusProvider{store: s, defaultProject: "proj-a", cfg: cfg}.Status("")
	if !status.Enabled {
		t.Fatalf("expected enabled=true when persisted sync state exists, got %+v", status)
	}
	if status.ReasonCode != constants.ReasonBlockedUnenrolled {
		t.Fatalf("expected persisted reason %q, got %q", constants.ReasonBlockedUnenrolled, status.ReasonCode)
	}
	if status.Phase != store.SyncLifecycleDegraded {
		t.Fatalf("expected degraded phase from persisted sync state, got %q", status.Phase)
	}
}

func TestStoreSyncStatusProviderDoesNotUsePersistedStateWhenProjectNotEnrolled(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	targetKey := cloudTargetKeyForProject("proj-a")
	if err := s.MarkSyncFailure(targetKey, "stale network timeout", time.Now().UTC().Add(30*time.Second)); err != nil {
		t.Fatalf("seed stale degraded sync state: %v", err)
	}

	status := storeSyncStatusProvider{store: s, defaultProject: "proj-a", cfg: cfg}.Status("")
	if status.Enabled {
		t.Fatalf("expected enabled=false when project is not enrolled, got %+v", status)
	}
	if status.ReasonCode != constants.ReasonBlockedUnenrolled {
		t.Fatalf("expected reason %q when project is not enrolled, got %q", constants.ReasonBlockedUnenrolled, status.ReasonCode)
	}
	if !strings.Contains(status.ReasonMessage, "not enrolled") {
		t.Fatalf("expected not enrolled message, got %q", status.ReasonMessage)
	}
}

func TestCloudDashboardStatusProviderSanitizesManifestErrors(t *testing.T) {
	provider := cloudDashboardStatusProvider{
		store:    stubManifestReader{err: errors.New("dial tcp 10.0.0.10:443: connection refused")},
		projects: []string{"proj-a"},
	}

	status := provider.Status()
	if status.Phase != "degraded" {
		t.Fatalf("expected degraded phase, got %q", status.Phase)
	}
	if status.ReasonCode != constants.ReasonTransportFailed {
		t.Fatalf("expected reason code %q, got %q", constants.ReasonTransportFailed, status.ReasonCode)
	}
	if status.ReasonMessage != "cloud sync status is temporarily unavailable" {
		t.Fatalf("expected sanitized user-facing message, got %q", status.ReasonMessage)
	}
	if strings.Contains(status.ReasonMessage, "10.0.0.10") || strings.Contains(status.ReasonMessage, "connection refused") {
		t.Fatalf("expected backend details to be hidden from reason_message, got %q", status.ReasonMessage)
	}
}

func TestStoreSyncStatusProviderPrefersCloudConfigErrorOverPersistedState(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	targetKey := cloudTargetKeyForProject("proj-a")
	if err := s.MarkSyncFailure(targetKey, "stale network timeout", time.Now().UTC().Add(30*time.Second)); err != nil {
		t.Fatalf("seed stale degraded sync state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "cloud.json"), []byte("{invalid-json"), 0644); err != nil {
		t.Fatalf("write invalid cloud config: %v", err)
	}

	status := storeSyncStatusProvider{store: s, defaultProject: "proj-a", cfg: cfg}.Status("")
	if status.Enabled {
		t.Fatalf("expected enabled=false when runtime cloud config is malformed, got %+v", status)
	}
	if status.ReasonCode != "cloud_config_error" {
		t.Fatalf("expected cloud_config_error reason, got %q", status.ReasonCode)
	}
}

func TestStoreSyncStatusProviderRejectsInvalidRuntimeServerURL(t *testing.T) {
	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test?debug=1")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	status := storeSyncStatusProvider{store: s, defaultProject: "proj-a", cfg: cfg}.Status("")
	if status.Enabled {
		t.Fatalf("expected enabled=false for malformed runtime server URL, got %+v", status)
	}
	if status.ReasonCode != "cloud_config_error" {
		t.Fatalf("expected cloud_config_error reason, got %q", status.ReasonCode)
	}
	if !strings.Contains(status.ReasonMessage, "invalid cloud runtime server URL") {
		t.Fatalf("expected invalid runtime URL context, got %q", status.ReasonMessage)
	}
}

func TestStoreSyncStatusProviderRequiresProjectEvenWhenCloudConfigured(t *testing.T) {
	cfg := testConfig(t)
	if err := saveCloudConfig(cfg, &cloudConfig{ServerURL: "https://cloud.example.test"}); err != nil {
		t.Fatalf("save cloud config: %v", err)
	}
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	status := storeSyncStatusProvider{store: s, defaultProject: "", cfg: cfg}.Status("")
	if status.Enabled {
		t.Fatalf("expected enabled=false when project scope is unresolved, got %+v", status)
	}
	if status.ReasonCode != "project_required" {
		t.Fatalf("expected project_required reason_code, got %q", status.ReasonCode)
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

	seedCfg, scErr := store.DefaultConfig()
	if scErr != nil {
		t.Fatalf("DefaultConfig: %v", scErr)
	}
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

func TestCmdSyncCloudPreflightMissingServerMarksConfigError(t *testing.T) {
	stubExitWithPanic(t)
	workDir := t.TempDir()
	withCwd(t, workDir)

	cfg := testConfig(t)
	t.Setenv("ENGRAM_CLOUD_SYNC", "1")

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "cloud_config_error") {
		t.Fatalf("expected cloud_config_error stderr, got %q", stderr)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	state, err := s.GetSyncState(cloudTargetKeyForProject("proj-a"))
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.ReasonCode == nil || *state.ReasonCode != "cloud_config_error" {
		t.Fatalf("expected cloud_config_error reason code, got %v", state.ReasonCode)
	}
}

func TestCmdSyncCloudPreflightSurfacesCloudConfigParseError(t *testing.T) {
	stubExitWithPanic(t)
	workDir := t.TempDir()
	withCwd(t, workDir)

	cfg := testConfig(t)
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "cloud.json"), []byte("{invalid-json"), 0644); err != nil {
		t.Fatalf("write invalid cloud config: %v", err)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	_ = s.Close()

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "cloud sync config error") || !strings.Contains(stderr, "invalid") {
		t.Fatalf("expected cloud config parse error surfaced, got %q", stderr)
	}
	if strings.Contains(stderr, "auth_required") || strings.Contains(stderr, "cloud server is missing") {
		t.Fatalf("expected parse error to be surfaced directly, got %q", stderr)
	}
}

func TestPreflightCloudSyncAllowsEmptyTokenWhenServerConfigured(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	t.Setenv("ENGRAM_CLOUD_SERVER", "http://127.0.0.1:9090")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_INSECURE_NO_AUTH", "")

	cc, err := preflightCloudSync(s, cfg, "proj-a", true)
	if err != nil {
		t.Fatalf("preflight should allow missing token when server is configured: %v", err)
	}
	if cc == nil {
		t.Fatal("expected non-nil cloud config")
	}
	if cc.Token != "" {
		t.Fatalf("expected empty token passthrough in insecure mode, got %q", cc.Token)
	}
}

func TestCmdSyncCloudInvalidRuntimeServerURLMarkedConfigError(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)

	t.Setenv("ENGRAM_CLOUD_SERVER", "://bad-url")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "invalid cloud runtime server URL") {
		t.Fatalf("expected runtime server URL validation error, got %q", stderr)
	}

	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s.Close()

	state, err := s.GetSyncState(cloudTargetKeyForProject("proj-a"))
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.ReasonCode == nil || *state.ReasonCode != "cloud_config_error" {
		t.Fatalf("expected reason_code=%q, got %v", "cloud_config_error", state.ReasonCode)
	}
}

func TestCmdSyncCloudStatusTransportConfigErrorIsReadOnly(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)

	t.Setenv("ENGRAM_CLOUD_SERVER", "://bad-url")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	withArgs(t, "engram", "sync", "--cloud", "--status", "--project", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "invalid cloud runtime server URL") {
		t.Fatalf("expected runtime server URL validation error, got %q", stderr)
	}

	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s.Close()

	state, err := s.GetSyncState(cloudTargetKeyForProject("proj-a"))
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.ReasonCode != nil {
		t.Fatalf("status path must be side-effect free; expected nil reason_code, got %v", *state.ReasonCode)
	}
}

func TestCmdSyncCloudStatusPrintsProjectScopedChunkCounts(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "")

	oldSyncStatus := syncStatus
	syncStatus = func(_ *engramsync.Syncer) (int, int, int, error) {
		return 3, 7, 2, nil
	}
	t.Cleanup(func() { syncStatus = oldSyncStatus })

	withArgs(t, "engram", "sync", "--cloud", "--status", "--project", "proj-a")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("sync --cloud --status should succeed, panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "Cloud sync status (project=\"proj-a\")") {
		t.Fatalf("expected cloud sync status header, got %q", stdout)
	}
	if !strings.Contains(stdout, "Local chunks:    3") || !strings.Contains(stdout, "Remote chunks:   7") || !strings.Contains(stdout, "Pending import:  2") {
		t.Fatalf("expected local/remote/pending chunk counts in output, got %q", stdout)
	}
	if strings.Contains(stdout, "not project-scoped") {
		t.Fatalf("stale non-project-scoped disclaimer must not be shown, got %q", stdout)
	}
}

func TestMarkCloudSyncFailureClassifiesHTTPStatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		syncErr    error
		expectCode string
	}{
		{
			name:       "401 maps to auth_required",
			syncErr:    &remote.HTTPStatusError{Operation: "fetch manifest", StatusCode: http.StatusUnauthorized, Body: "unauthorized"},
			expectCode: constants.ReasonAuthRequired,
		},
		{
			name:       "403 maps to policy_forbidden",
			syncErr:    &remote.HTTPStatusError{Operation: "fetch manifest", StatusCode: http.StatusForbidden, Body: "forbidden"},
			expectCode: constants.ReasonPolicyForbidden,
		},
		{
			name:       "generic failures remain transport_failed",
			syncErr:    errors.New("dial tcp timeout"),
			expectCode: constants.ReasonTransportFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			s, err := store.New(cfg)
			if err != nil {
				t.Fatalf("store.New: %v", err)
			}
			defer s.Close()
			targetKey := cloudTargetKeyForProject("proj-a")
			markCloudSyncFailure(s, targetKey, tc.syncErr)

			state, err := s.GetSyncState(targetKey)
			if err != nil {
				t.Fatalf("get sync state: %v", err)
			}
			if state.ReasonCode == nil || *state.ReasonCode != tc.expectCode {
				t.Fatalf("expected reason_code=%q, got %v", tc.expectCode, state.ReasonCode)
			}
		})
	}
}

func TestMarkCloudSyncFailureStoresRepairGuidance(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	targetKey := cloudTargetKeyForProject("proj-a")
	syncErr := &remote.HTTPStatusError{
		Operation:  "push chunk abc123",
		StatusCode: http.StatusBadRequest,
		ErrorClass: "repairable",
		ErrorCode:  "upgrade_repairable_legacy_mutation_payload",
		Body:       "legacy mutation payload missing required field: directory is required",
	}
	markCloudSyncFailure(s, targetKey, syncErr)

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.LastError == nil {
		t.Fatal("expected stored last_error")
	}
	for _, want := range []string{
		"legacy mutation payload missing required field",
		"Known repairable cloud sync failure detected.",
		"engram cloud upgrade doctor --project proj-a",
		"engram cloud upgrade repair --project proj-a --dry-run",
		"engram cloud upgrade repair --project proj-a --apply",
		"engram sync --cloud --project proj-a",
	} {
		if !strings.Contains(*state.LastError, want) {
			t.Fatalf("expected last_error to contain %q, got %q", want, *state.LastError)
		}
	}
	if strings.Contains(*state.LastError, "--auto-repair") {
		t.Fatalf("guidance must not mention auto-repair, got %q", *state.LastError)
	}
	upgradeState, err := s.GetCloudUpgradeState("proj-a")
	if err != nil {
		t.Fatalf("get cloud upgrade state: %v", err)
	}
	if upgradeState != nil {
		t.Fatalf("sync failure guidance must not auto-create repair state, got %+v", upgradeState)
	}
}

func TestCmdSyncCloudFailurePrintsRepairGuidance(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	oldSyncExport := syncExport
	t.Cleanup(func() { syncExport = oldSyncExport })

	workDir := t.TempDir()
	withCwd(t, workDir)
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
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	repairableErr := &remote.HTTPStatusError{
		Operation:  "push chunk abc123",
		StatusCode: http.StatusBadRequest,
		ErrorClass: "repairable",
		ErrorCode:  "upgrade_repairable_payload_invalid",
		Body:       "invalid upsert payload: sessions[0].directory is required",
	}
	syncExport = func(*engramsync.Syncer, string, string) (*engramsync.SyncResult, error) {
		return nil, repairableErr
	}

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if code, ok := recovered.(exitCode); !ok || int(code) != 1 {
		t.Fatalf("expected fatal exit code 1, got %v", recovered)
	}
	for _, want := range []string{
		"invalid upsert payload: sessions[0].directory is required",
		"Known repairable cloud sync failure detected.",
		"engram cloud upgrade doctor --project proj-a",
		"engram cloud upgrade repair --project proj-a --dry-run",
		"engram cloud upgrade repair --project proj-a --apply",
		"engram sync --cloud --project proj-a",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("expected stderr to contain %q, got %q", want, stderr)
		}
	}
	if strings.Contains(stderr, "--auto-repair") {
		t.Fatalf("guidance must not mention auto-repair, got %q", stderr)
	}
	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New after failure: %v", err)
	}
	defer s.Close()
	upgradeState, err := s.GetCloudUpgradeState("proj-a")
	if err != nil {
		t.Fatalf("get cloud upgrade state: %v", err)
	}
	if upgradeState != nil {
		t.Fatalf("sync --cloud failure guidance must not auto-create repair state, got %+v", upgradeState)
	}
}

func TestCmdSyncCloudSuccessMarksTargetHealthy(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	originalSyncStatus := syncStatus
	originalSyncImport := syncImport
	originalSyncExport := syncExport
	t.Cleanup(func() {
		syncStatus = originalSyncStatus
		syncImport = originalSyncImport
		syncExport = originalSyncExport
	})

	tests := []struct {
		name          string
		args          []string
		stub          func()
		expectHealthy bool
	}{
		{
			name: "status",
			args: []string{"engram", "sync", "--cloud", "--status", "--project", "proj-a"},
			stub: func() {
				syncStatus = func(*engramsync.Syncer) (int, int, int, error) {
					return 2, 2, 0, nil
				}
			},
			expectHealthy: false,
		},
		{
			name: "import",
			args: []string{"engram", "sync", "--cloud", "--import", "--project", "proj-a"},
			stub: func() {
				syncImport = func(*engramsync.Syncer) (*engramsync.ImportResult, error) {
					return &engramsync.ImportResult{}, nil
				}
			},
			expectHealthy: true,
		},
		{
			name: "export",
			args: []string{"engram", "sync", "--cloud", "--project", "proj-a"},
			stub: func() {
				syncExport = func(*engramsync.Syncer, string, string) (*engramsync.SyncResult, error) {
					return &engramsync.SyncResult{ChunkID: "chunk-test", SessionsExported: 1}, nil
				}
			},
			expectHealthy: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			withCwd(t, workDir)
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
			if err := s.MarkSyncFailure(cloudTargetKeyForProject("proj-a"), "previous failure", time.Now().UTC().Add(30*time.Second)); err != nil {
				_ = s.Close()
				t.Fatalf("seed degraded sync state: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}

			tc.stub()
			withArgs(t, tc.args...)
			_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
			if recovered != nil || stderr != "" {
				t.Fatalf("expected successful cloud sync cycle, panic=%v stderr=%q", recovered, stderr)
			}

			s, err = store.New(cfg)
			if err != nil {
				t.Fatalf("store.New after sync: %v", err)
			}
			defer s.Close()

			state, err := s.GetSyncState(cloudTargetKeyForProject("proj-a"))
			if err != nil {
				t.Fatalf("get sync state: %v", err)
			}
			if tc.expectHealthy {
				if state.Lifecycle != store.SyncLifecycleHealthy {
					t.Fatalf("expected lifecycle healthy, got %q", state.Lifecycle)
				}
				if state.ReasonCode != nil || state.LastError != nil {
					t.Fatalf("expected cleared degraded fields, got reason=%v last_error=%v", state.ReasonCode, state.LastError)
				}

				status := storeSyncStatusProvider{store: s, defaultProject: "proj-a"}.Status("")
				if status.LastSyncAt == nil {
					t.Fatal("expected /sync/status last_sync_at after successful cloud cycle")
				}
			} else {
				if state.Lifecycle != store.SyncLifecycleDegraded {
					t.Fatalf("status-only sync must be read-only; expected degraded lifecycle, got %q", state.Lifecycle)
				}
			}
		})
	}
}

func TestCmdSyncCloudImportKeepsPendingWhenLocalMutationsRemain(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	originalSyncImport := syncImport
	originalSyncStatus := syncStatus
	t.Cleanup(func() {
		syncImport = originalSyncImport
		syncStatus = originalSyncStatus
	})

	workDir := t.TempDir()
	withCwd(t, workDir)
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
	if err := s.CreateSession("sess-pending", "proj-a", "/tmp/proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-pending",
		Type:      "note",
		Title:     "pending local mutation",
		Content:   "still unacked",
		Project:   "proj-a",
		Scope:     "project",
	}); err != nil {
		_ = s.Close()
		t.Fatalf("add observation: %v", err)
	}
	targetKey := cloudTargetKeyForProject("proj-a")
	if err := s.MarkSyncFailure(targetKey, "previous transport failure", time.Now().UTC().Add(30*time.Second)); err != nil {
		_ = s.Close()
		t.Fatalf("seed degraded state: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	syncImport = func(*engramsync.Syncer) (*engramsync.ImportResult, error) {
		return &engramsync.ImportResult{}, nil
	}
	syncStatus = func(*engramsync.Syncer) (int, int, int, error) {
		return 1, 1, 0, nil
	}

	withArgs(t, "engram", "sync", "--cloud", "--import", "--project", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected successful cloud import, panic=%v stderr=%q", recovered, stderr)
	}

	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s.Close()

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != store.SyncLifecyclePending {
		t.Fatalf("expected lifecycle pending when local mutations remain, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.LastError != nil {
		t.Fatalf("expected successful import to clear degraded metadata, got reason=%v last_error=%v", state.ReasonCode, state.LastError)
	}
}

func TestCmdSyncCloudExportKeepsPendingWhenLocalMutationsRemain(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	originalSyncExport := syncExport
	originalSyncStatus := syncStatus
	t.Cleanup(func() {
		syncExport = originalSyncExport
		syncStatus = originalSyncStatus
	})

	workDir := t.TempDir()
	withCwd(t, workDir)
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
	if err := s.CreateSession("sess-pending-export", "proj-a", "/tmp/proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-pending-export",
		Type:      "note",
		Title:     "pending local mutation",
		Content:   "still unacked",
		Project:   "proj-a",
		Scope:     "project",
	}); err != nil {
		_ = s.Close()
		t.Fatalf("add observation: %v", err)
	}
	targetKey := cloudTargetKeyForProject("proj-a")
	if err := s.MarkSyncFailure(targetKey, "previous transport failure", time.Now().UTC().Add(30*time.Second)); err != nil {
		_ = s.Close()
		t.Fatalf("seed degraded state: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	syncExport = func(*engramsync.Syncer, string, string) (*engramsync.SyncResult, error) {
		return &engramsync.SyncResult{ChunkID: "chunk-stub", SessionsExported: 1, ObservationsExported: 1}, nil
	}
	syncStatus = func(*engramsync.Syncer) (int, int, int, error) {
		return 1, 1, 0, nil
	}

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected successful cloud export, panic=%v stderr=%q", recovered, stderr)
	}

	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s.Close()

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != store.SyncLifecyclePending {
		t.Fatalf("expected lifecycle pending when local mutations remain, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.LastError != nil {
		t.Fatalf("expected successful export to clear degraded metadata, got reason=%v last_error=%v", state.ReasonCode, state.LastError)
	}
}

func TestCmdSyncCloudExportKeepsPendingWhenRemoteImportsRemain(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	originalSyncExport := syncExport
	originalSyncStatus := syncStatus
	t.Cleanup(func() {
		syncExport = originalSyncExport
		syncStatus = originalSyncStatus
	})

	workDir := t.TempDir()
	withCwd(t, workDir)
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
	targetKey := cloudTargetKeyForProject("proj-a")
	if err := s.MarkSyncFailure(targetKey, "previous transport failure", time.Now().UTC().Add(30*time.Second)); err != nil {
		_ = s.Close()
		t.Fatalf("seed degraded state: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	syncExport = func(*engramsync.Syncer, string, string) (*engramsync.SyncResult, error) {
		return &engramsync.SyncResult{ChunkID: "chunk-stub", SessionsExported: 1}, nil
	}
	syncStatus = func(*engramsync.Syncer) (int, int, int, error) {
		return 1, 2, 1, nil
	}

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected successful cloud export, panic=%v stderr=%q", recovered, stderr)
	}

	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s.Close()

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != store.SyncLifecyclePending {
		t.Fatalf("expected lifecycle pending when remote imports remain, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.LastError != nil {
		t.Fatalf("expected successful export to clear degraded metadata, got reason=%v last_error=%v", state.ReasonCode, state.LastError)
	}
}

func TestCmdSyncCloudExportPreservesDegradedWhenPostSuccessStatusRefreshFails(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	originalSyncExport := syncExport
	originalSyncStatus := syncStatus
	t.Cleanup(func() {
		syncExport = originalSyncExport
		syncStatus = originalSyncStatus
	})

	workDir := t.TempDir()
	withCwd(t, workDir)
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
	targetKey := cloudTargetKeyForProject("proj-a")
	if err := s.MarkSyncFailure(targetKey, "previous transport failure", time.Now().UTC().Add(30*time.Second)); err != nil {
		_ = s.Close()
		t.Fatalf("seed degraded state: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	syncExport = func(*engramsync.Syncer, string, string) (*engramsync.SyncResult, error) {
		return &engramsync.SyncResult{ChunkID: "chunk-stub", SessionsExported: 1}, nil
	}
	syncStatus = func(*engramsync.Syncer) (int, int, int, error) {
		return 0, 0, 0, errors.New("remote status unavailable")
	}

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected successful cloud export despite status refresh error, panic=%v stderr=%q", recovered, stderr)
	}

	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s.Close()

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != store.SyncLifecycleDegraded {
		t.Fatalf("expected lifecycle to remain degraded when refresh is unavailable, got %q", state.Lifecycle)
	}
	if state.ReasonCode == nil || *state.ReasonCode != constants.ReasonTransportFailed {
		t.Fatalf("expected degraded reason %q to be preserved, got %v", constants.ReasonTransportFailed, state.ReasonCode)
	}
	if state.LastError == nil || !strings.Contains(*state.LastError, "previous transport failure") {
		t.Fatalf("expected degraded last_error to be preserved, got %v", state.LastError)
	}
}

func TestCmdSyncCloudExportPreservesPendingWhenPostSuccessStatusRefreshFails(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	originalSyncExport := syncExport
	originalSyncStatus := syncStatus
	t.Cleanup(func() {
		syncExport = originalSyncExport
		syncStatus = originalSyncStatus
	})

	workDir := t.TempDir()
	withCwd(t, workDir)
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
	targetKey := cloudTargetKeyForProject("proj-a")
	if err := s.MarkSyncPending(targetKey); err != nil {
		_ = s.Close()
		t.Fatalf("seed pending state: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	syncExport = func(*engramsync.Syncer, string, string) (*engramsync.SyncResult, error) {
		return &engramsync.SyncResult{ChunkID: "chunk-stub", SessionsExported: 1}, nil
	}
	syncStatus = func(*engramsync.Syncer) (int, int, int, error) {
		return 0, 0, 0, errors.New("remote status unavailable")
	}

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-a")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected successful cloud export despite status refresh error, panic=%v stderr=%q", recovered, stderr)
	}

	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s.Close()

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != store.SyncLifecyclePending {
		t.Fatalf("expected lifecycle to remain pending when refresh is unavailable, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.LastError != nil {
		t.Fatalf("expected pending state metadata to remain clear, got reason=%v last_error=%v", state.ReasonCode, state.LastError)
	}
}

func TestCmdSyncCloudLifecycleStateIsProjectScoped(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)

	originalSyncExport := syncExport
	originalSyncStatus := syncStatus
	t.Cleanup(func() {
		syncExport = originalSyncExport
		syncStatus = originalSyncStatus
	})

	workDir := t.TempDir()
	withCwd(t, workDir)
	cfg := testConfig(t)

	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll proj-a: %v", err)
	}
	if err := s.EnrollProject("proj-b"); err != nil {
		_ = s.Close()
		t.Fatalf("enroll proj-b: %v", err)
	}
	if err := s.MarkSyncFailure(cloudTargetKeyForProject("proj-a"), "previous failure a", time.Now().UTC().Add(30*time.Second)); err != nil {
		_ = s.Close()
		t.Fatalf("seed proj-a degraded state: %v", err)
	}
	if err := s.MarkSyncFailure(cloudTargetKeyForProject("proj-b"), "previous failure b", time.Now().UTC().Add(30*time.Second)); err != nil {
		_ = s.Close()
		t.Fatalf("seed proj-b degraded state: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	syncExport = func(*engramsync.Syncer, string, string) (*engramsync.SyncResult, error) {
		return &engramsync.SyncResult{ChunkID: "chunk-proj-b", SessionsExported: 1}, nil
	}
	syncStatus = func(*engramsync.Syncer) (int, int, int, error) {
		return 1, 1, 0, nil
	}

	withArgs(t, "engram", "sync", "--cloud", "--project", "proj-b")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected successful cloud sync cycle, panic=%v stderr=%q", recovered, stderr)
	}

	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New (verify): %v", err)
	}
	defer s.Close()

	stateA, err := s.GetSyncState(cloudTargetKeyForProject("proj-a"))
	if err != nil {
		t.Fatalf("get proj-a state: %v", err)
	}
	if stateA.Lifecycle != store.SyncLifecycleDegraded {
		t.Fatalf("expected proj-a to remain degraded, got %q", stateA.Lifecycle)
	}

	stateB, err := s.GetSyncState(cloudTargetKeyForProject("proj-b"))
	if err != nil {
		t.Fatalf("get proj-b state: %v", err)
	}
	if stateB.Lifecycle != store.SyncLifecycleHealthy {
		t.Fatalf("expected proj-b healthy after successful sync, got %q", stateB.Lifecycle)
	}
}

func TestCmdSyncCloudRequiresExplicitProjectAndRejectsAll(t *testing.T) {
	stubExitWithPanic(t)
	stubRuntimeHooks(t)
	cfg := testConfig(t)

	t.Setenv("ENGRAM_CLOUD_SERVER", "https://cloud.example.test")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "token-abc")

	withArgs(t, "engram", "sync", "--cloud", "--all")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "--all is not supported") {
		t.Fatalf("expected --all cloud rejection, got %q", stderr)
	}

	withArgs(t, "engram", "sync", "--cloud")
	_, stderr, recovered = captureOutputAndRecover(t, func() { cmdSync(cfg) })
	if _, ok := recovered.(exitCode); !ok {
		t.Fatalf("expected fatal exit, got %v", recovered)
	}
	if !strings.Contains(stderr, "explicit non-empty --project") {
		t.Fatalf("expected explicit project requirement, got %q", stderr)
	}
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
		if !strings.Contains(stdout, "No new chunks to import") || strings.Contains(stdout, "already imported") {
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

	t.Run("no tools filter uses newMCPServerWithConfig with nil allowlist", func(t *testing.T) {
		called := false
		newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
			called = true
			if allowlist != nil {
				t.Errorf("expected nil allowlist for no tools filter, got %v", allowlist)
			}
			return mcpserver.NewMCPServer("test", "0")
		}
		withArgs(t, "engram", "mcp")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if !called {
			t.Fatal("expected newMCPServerWithConfig to be called")
		}
	})

	t.Run("--tools flag uses newMCPServerWithConfig with non-nil allowlist", func(t *testing.T) {
		var gotAllowlist map[string]bool
		newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
			gotAllowlist = allowlist
			return mcpserver.NewMCPServer("test", "0")
		}
		withArgs(t, "engram", "mcp", "--tools=agent")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if gotAllowlist == nil {
			t.Fatal("expected newMCPServerWithConfig to be called with non-nil allowlist")
		}
	})

	t.Run("--tools as separate arg uses newMCPServerWithConfig with non-nil allowlist", func(t *testing.T) {
		var gotAllowlist map[string]bool
		newMCPServerWithConfig = func(s *store.Store, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
			gotAllowlist = allowlist
			return mcpserver.NewMCPServer("test", "0")
		}
		withArgs(t, "engram", "mcp", "--tools", "agent")
		_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil || stderr != "" {
			t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
		}
		if gotAllowlist == nil {
			t.Fatal("expected newMCPServerWithConfig to be called with non-nil allowlist")
		}
	})

	t.Run("cloud autosync env with token and server starts and stops manager", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
		t.Setenv("ENGRAM_CLOUD_TOKEN", "tok")
		t.Setenv("ENGRAM_CLOUD_SERVER", "http://localhost:9999")

		runStarted := make(chan struct{}, 1)
		stopCalled := make(chan struct{}, 1)
		oldNewAutosyncManager := newAutosyncManager
		newAutosyncManager = func(_ *store.Store, _ autosync.CloudTransport, _ autosync.Config) startableAutosyncManager {
			return &fakeStartableManager{
				runFn:  func(context.Context) { runStarted <- struct{}{} },
				stopFn: func() { stopCalled <- struct{}{} },
			}
		}
		t.Cleanup(func() { newAutosyncManager = oldNewAutosyncManager })

		serveMCP = func(_ *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error {
			select {
			case <-runStarted:
				return nil
			case <-time.After(time.Second):
				t.Fatal("expected MCP autosync manager to start before serving returned")
				return nil
			}
		}

		withArgs(t, "engram", "mcp")
		_, _, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
		if recovered != nil {
			t.Fatalf("expected clean run, got panic=%v", recovered)
		}
		select {
		case <-stopCalled:
			// expected
		default:
			t.Fatal("expected MCP autosync manager to stop after stdio server exits")
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

func TestCmdMCPAutosyncPushesWriteDuringServe(t *testing.T) {
	cfg := testConfig(t)
	stubExitWithPanic(t)

	var mu sync.Mutex
	var pushed []autosync.MutationEntry
	observationPushed := make(chan struct{})
	var closeObservationPushed sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/sync/mutations/push":
			var req struct {
				Entries []autosync.MutationEntry `json:"entries"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			mu.Lock()
			pushed = append(pushed, req.Entries...)
			for _, entry := range req.Entries {
				if entry.Project == "engram" && entry.Entity == store.SyncEntityObservation && strings.Contains(string(entry.Payload), "mcp autosync proof") {
					closeObservationPushed.Do(func() { close(observationPushed) })
				}
			}
			mu.Unlock()

			seqs := make([]int64, len(req.Entries))
			for i := range seqs {
				seqs[i] = int64(i + 1)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted_seqs": seqs})

		case "/sync/mutations/pull":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mutations":  []any{},
				"has_more":   false,
				"latest_seq": 0,
			})

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "test-token")
	t.Setenv("ENGRAM_CLOUD_SERVER", srv.URL)

	oldStoreNew := storeNew
	oldNewMCPServerWithConfig := newMCPServerWithConfig
	oldServeMCP := serveMCP
	oldNewAutosyncManager := newAutosyncManager
	storeNew = store.New
	t.Cleanup(func() {
		storeNew = oldStoreNew
		newMCPServerWithConfig = oldNewMCPServerWithConfig
		serveMCP = oldServeMCP
		newAutosyncManager = oldNewAutosyncManager
	})

	var mcpStore *store.Store
	newMCPServerWithConfig = func(s *store.Store, _ mcp.MCPConfig, _ map[string]bool) *mcpserver.MCPServer {
		mcpStore = s
		return mcpserver.NewMCPServer("test", "0")
	}
	newAutosyncManager = func(s *store.Store, transport autosync.CloudTransport, cfg autosync.Config) startableAutosyncManager {
		cfg.DebounceDuration = 5 * time.Millisecond
		cfg.PollInterval = 10 * time.Millisecond
		cfg.BaseBackoff = 20 * time.Millisecond
		cfg.MaxBackoff = 50 * time.Millisecond
		return autosync.New(s, transport, cfg)
	}
	serveMCP = func(_ *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error {
		if mcpStore == nil {
			return errors.New("MCP store was not wired into server construction")
		}
		if err := mcpStore.EnrollProject("engram"); err != nil {
			return fmt.Errorf("enroll project: %w", err)
		}
		if err := mcpStore.CreateSession("mcp-autosync-session", "engram", t.TempDir()); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		if _, err := mcpStore.AddObservation(store.AddObservationParams{
			SessionID: "mcp-autosync-session",
			Type:      "bugfix",
			Title:     "mcp autosync proof",
			Content:   "mcp autosync proof mutation created during stdio serving",
			Project:   "engram",
			Scope:     "project",
		}); err != nil {
			return fmt.Errorf("add observation: %w", err)
		}

		select {
		case <-observationPushed:
			return nil
		case <-time.After(2 * time.Second):
			return errors.New("timed out waiting for autosync to push MCP write")
		}
	}

	withArgs(t, "engram", "mcp")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected MCP autosync proof to complete cleanly, panic=%v stderr=%q", recovered, stderr)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(pushed) == 0 {
		t.Fatal("expected remote mutation endpoint to receive at least one pushed mutation")
	}
}

func TestCmdMCPAutosyncPollTickerPullsDuringServe(t *testing.T) {
	cfg := testConfig(t)
	stubExitWithPanic(t)

	pullCalled := make(chan struct{})
	var closePullCalled sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/sync/mutations/pull":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			closePullCalled.Do(func() { close(pullCalled) })
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mutations":  []any{},
				"has_more":   false,
				"latest_seq": 0,
			})

		case "/sync/mutations/push":
			http.Error(w, "unexpected push without MCP write", http.StatusInternalServerError)

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "test-token")
	t.Setenv("ENGRAM_CLOUD_SERVER", srv.URL)

	oldStoreNew := storeNew
	oldNewMCPServerWithConfig := newMCPServerWithConfig
	oldServeMCP := serveMCP
	oldNewAutosyncManager := newAutosyncManager
	storeNew = store.New
	t.Cleanup(func() {
		storeNew = oldStoreNew
		newMCPServerWithConfig = oldNewMCPServerWithConfig
		serveMCP = oldServeMCP
		newAutosyncManager = oldNewAutosyncManager
	})

	newMCPServerWithConfig = func(s *store.Store, _ mcp.MCPConfig, _ map[string]bool) *mcpserver.MCPServer {
		return mcpserver.NewMCPServer("test", "0")
	}
	newAutosyncManager = func(s *store.Store, transport autosync.CloudTransport, cfg autosync.Config) startableAutosyncManager {
		cfg.DebounceDuration = time.Hour
		cfg.PollInterval = 10 * time.Millisecond
		cfg.BaseBackoff = 20 * time.Millisecond
		cfg.MaxBackoff = 50 * time.Millisecond
		return autosync.New(s, transport, cfg)
	}
	serveMCP = func(_ *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error {
		select {
		case <-pullCalled:
			return nil
		case <-time.After(2 * time.Second):
			return errors.New("timed out waiting for autosync poll ticker pull during MCP serve")
		}
	}

	withArgs(t, "engram", "mcp")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })
	if recovered != nil || stderr != "" {
		t.Fatalf("expected MCP autosync poll ticker proof to complete cleanly, panic=%v stderr=%q", recovered, stderr)
	}
}
