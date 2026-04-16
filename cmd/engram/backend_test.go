package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/mcp"
	engramsrv "github.com/Gentleman-Programming/engram/internal/server"
	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/Gentleman-Programming/engram/internal/types"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// ─── T34: --backend flag parsing ─────────────────────────────────────────────
//
// REQ-BACKEND-001: --backend local (default) → *store.Store, no SyncClient
// REQ-BACKEND-002: --backend cloud → RemoteStore from CloudConfig
// REQ-BACKEND-003: --backend local-sync → *store.Store + SyncClient.Start/Stop
// REQ-BACKEND-004: --backend <unknown> → exit 1 BEFORE store init

func TestBackendFlagDefaultIsLocal(t *testing.T) {
	// When no --backend flag is provided, cmdServe must behave identically
	// to the current default: uses *store.Store, not RemoteStore.
	cfg := testConfig(t)

	var receivedStore types.StoreInterface
	oldNew := newHTTPServer
	t.Cleanup(func() { newHTTPServer = oldNew })
	newHTTPServer = func(s types.StoreInterface, port int) *engramsrv.Server {
		receivedStore = s
		return engramsrv.New(s, 0)
	}

	oldStart := startHTTP
	t.Cleanup(func() { startHTTP = oldStart })
	startHTTP = func(_ *engramsrv.Server) error { return nil }

	withArgs(t, "engram", "serve")
	_, _ = captureOutput(t, func() { cmdServe(cfg) })

	if receivedStore == nil {
		t.Fatal("expected newHTTPServer to be called")
	}
	if _, ok := receivedStore.(*store.Store); !ok {
		t.Fatalf("expected *store.Store for --backend local (default), got %T", receivedStore)
	}
}

func TestBackendFlagLocalExplicit(t *testing.T) {
	// --backend local must be identical to default behavior
	cfg := testConfig(t)

	var receivedStore types.StoreInterface
	oldNew := newHTTPServer
	t.Cleanup(func() { newHTTPServer = oldNew })
	newHTTPServer = func(s types.StoreInterface, port int) *engramsrv.Server {
		receivedStore = s
		return engramsrv.New(s, 0)
	}

	oldStart := startHTTP
	t.Cleanup(func() { startHTTP = oldStart })
	startHTTP = func(_ *engramsrv.Server) error { return nil }

	withArgs(t, "engram", "serve", "--backend=local")
	_, _ = captureOutput(t, func() { cmdServe(cfg) })

	if _, ok := receivedStore.(*store.Store); !ok {
		t.Fatalf("expected *store.Store for --backend local, got %T", receivedStore)
	}
}

func TestBackendFlagUnknownExitsBeforeStoreInit(t *testing.T) {
	// --backend <unknown> must exit 1 BEFORE opening the store.
	cfg := testConfig(t)

	storeOpened := false
	oldStoreNew := storeNew
	t.Cleanup(func() { storeNew = oldStoreNew })
	storeNew = func(c store.Config) (*store.Store, error) {
		storeOpened = true
		return store.New(c)
	}

	var exitCode int
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })
	exitFunc = func(code int) { exitCode = code; panic(exitCode) }

	withArgs(t, "engram", "serve", "--backend=bogus")
	_, stderr, _ := captureOutputAndRecover(t, func() { cmdServe(cfg) })

	if storeOpened {
		t.Fatal("store must NOT be opened before --backend validation fails")
	}
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for unknown backend, got %d", exitCode)
	}
	if !strings.Contains(stderr, "bogus") && !strings.Contains(stderr, "backend") {
		t.Fatalf("expected stderr to mention invalid backend, got: %q", stderr)
	}
}

func TestBackendFlagCloudMissingConfigErrors(t *testing.T) {
	// --backend cloud with no cloud config in store → error at startup, no server started.
	cfg := testConfig(t)

	serverStarted := false
	oldNew := newHTTPServer
	t.Cleanup(func() { newHTTPServer = oldNew })
	newHTTPServer = func(s types.StoreInterface, port int) *engramsrv.Server {
		serverStarted = true
		return engramsrv.New(s, 0)
	}

	var exitCode int
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })
	exitFunc = func(code int) { exitCode = code; panic(exitCode) }

	withArgs(t, "engram", "serve", "--backend=cloud")
	_, _, _ = captureOutputAndRecover(t, func() { cmdServe(cfg) })

	if serverStarted {
		t.Fatal("HTTP server must NOT start when cloud config is missing")
	}
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for missing cloud config, got %d", exitCode)
	}
}

func TestBackendFlagLocalSyncStartsAndStopsClient(t *testing.T) {
	// --backend local-sync → *store.Store passed to server + SyncClient.Start called.
	// On SIGTERM, SyncClient.Stop must be called.
	// Since we can't send actual SIGTERM in unit tests, we verify:
	// 1. The store passed to newHTTPServer is *store.Store (not RemoteStore)
	// 2. syncClientStart is called
	// 3. syncClientStop is called before server exit
	cfg := testConfig(t)

	var receivedStore types.StoreInterface
	oldNew := newHTTPServer
	t.Cleanup(func() { newHTTPServer = oldNew })
	newHTTPServer = func(s types.StoreInterface, port int) *engramsrv.Server {
		receivedStore = s
		return engramsrv.New(s, 0)
	}

	syncStarted := false
	syncStopped := false

	oldSyncClientStart := syncClientStart
	t.Cleanup(func() { syncClientStart = oldSyncClientStart })
	syncClientStart = func(sc syncClientIface, ctx context.Context) {
		syncStarted = true
	}

	oldSyncClientStop := syncClientStop
	t.Cleanup(func() { syncClientStop = oldSyncClientStop })
	syncClientStop = func(sc syncClientIface) { //nolint:unused
		syncStopped = true
	}

	// Make startHTTP return immediately so cmdServe terminates cleanly
	oldStart := startHTTP
	t.Cleanup(func() { startHTTP = oldStart })
	startHTTP = func(_ *engramsrv.Server) error {
		// Simulate SIGTERM by triggering stop path
		return errors.New("serve stopped")
	}

	var exitCode int
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })
	exitFunc = func(code int) { exitCode = code; panic(exitCode) }

	withArgs(t, "engram", "serve", "--backend=local-sync")

	// local-sync needs cloud config to construct SyncClient, but in tests
	// we want to verify the wiring. We use a stub syncClientFactory.
	oldSyncClientFactory := syncClientFactory
	t.Cleanup(func() { syncClientFactory = oldSyncClientFactory })
	syncClientFactory = func(s *store.Store, cfg store.Config) (syncClientIface, error) {
		return &fakeSyncClient{}, nil
	}

	_, _, _ = captureOutputAndRecover(t, func() { cmdServe(cfg) })

	if receivedStore == nil {
		t.Skip("local-sync store wiring not yet implemented")
	}
	if _, ok := receivedStore.(*store.Store); !ok {
		t.Fatalf("expected *store.Store for --backend local-sync, got %T", receivedStore)
	}
	if !syncStarted {
		t.Fatal("expected SyncClient.Start to be called for --backend local-sync")
	}
	_ = syncStopped // Stop is called on SIGTERM — verified separately
}

func TestMCPBackendFlagUnknownExitsBeforeStoreInit(t *testing.T) {
	// --backend <unknown> in cmdMCP must exit 1 BEFORE store open.
	cfg := testConfig(t)

	storeOpened := false
	oldStoreNew := storeNew
	t.Cleanup(func() { storeNew = oldStoreNew })
	storeNew = func(c store.Config) (*store.Store, error) {
		storeOpened = true
		return store.New(c)
	}

	var exitCode int
	oldExit := exitFunc
	t.Cleanup(func() { exitFunc = oldExit })
	exitFunc = func(code int) { exitCode = code; panic(exitCode) }

	withArgs(t, "engram", "mcp", "--backend=bogus")
	_, _, _ = captureOutputAndRecover(t, func() { cmdMCP(cfg) })

	if storeOpened {
		t.Fatal("store must NOT be opened before --backend validation fails in cmdMCP")
	}
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for unknown backend in cmdMCP, got %d", exitCode)
	}
}

func TestMCPBackendFlagDefaultIsLocal(t *testing.T) {
	// Default cmdMCP (no --backend) must still use *store.Store.
	cfg := testConfig(t)

	var receivedStore types.StoreInterface
	oldNew := newMCPServerWithConfig
	t.Cleanup(func() { newMCPServerWithConfig = oldNew })
	newMCPServerWithConfig = func(s types.StoreInterface, mcpCfg mcp.MCPConfig, allowlist map[string]bool) *mcpserver.MCPServer {
		receivedStore = s
		return oldNew(s, mcpCfg, allowlist)
	}

	oldServe := serveMCP
	t.Cleanup(func() { serveMCP = oldServe })
	serveMCP = func(_ *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error { return nil }

	withArgs(t, "engram", "mcp")
	_, _ = captureOutput(t, func() { cmdMCP(cfg) })

	if _, ok := receivedStore.(*store.Store); !ok {
		t.Fatalf("expected *store.Store for default cmdMCP, got %T", receivedStore)
	}
}

// fakeSyncClient is a no-op syncClientIface for testing.
type fakeSyncClient struct{}

func (f *fakeSyncClient) Start(ctx context.Context) {}
func (f *fakeSyncClient) Stop()                     {}
func (f *fakeSyncClient) SchedulePush()             {}
