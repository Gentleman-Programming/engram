package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/autosync"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// stubMCPStdioLifecycle swaps the injectable stdio seams for the duration of
// a test and restores them afterwards.
func stubMCPStdioLifecycle(t *testing.T) {
	t.Helper()
	oldInput := mcpStdioInput
	oldStopAutosync := mcpStdioStopAutosync
	oldEOFCancelAfter := mcpStdioEOFCancelAfter
	oldListen := listenMCPStdio
	t.Cleanup(func() {
		mcpStdioInput = oldInput
		mcpStdioStopAutosync = oldStopAutosync
		mcpStdioEOFCancelAfter = oldEOFCancelAfter
		listenMCPStdio = oldListen
	})
}

// enableAutosyncEnv opts the command under test into autosync, mirroring the
// ENGRAM_CLOUD_AUTOSYNC contract (exact "1" plus token and server).
func enableAutosyncEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "1")
	t.Setenv("ENGRAM_CLOUD_TOKEN", "tok")
	t.Setenv("ENGRAM_CLOUD_SERVER", "https://localhost:9999")
}

// stubAutosyncManager installs the deterministic fake autosync manager used by
// the stdio shutdown tests: runStarted signals when Run begins and stopCalled
// signals when the graceful shutdown sequence reaches Stop (lease release).
func stubAutosyncManager(t *testing.T) (runStarted chan struct{}, stopCalled chan struct{}) {
	t.Helper()
	runStarted = make(chan struct{}, 1)
	stopCalled = make(chan struct{}, 1)
	old := newAutosyncManager
	newAutosyncManager = func(_ *store.Store, _ autosync.CloudTransport, _ autosync.Config) startableAutosyncManager {
		return &fakeStartableManager{
			runFn:  func(context.Context) { runStarted <- struct{}{} },
			stopFn: func() { stopCalled <- struct{}{} },
		}
	}
	t.Cleanup(func() { newAutosyncManager = old })
	return runStarted, stopCalled
}

// waitForSignal blocks until ch delivers or the bounded timeout elapses,
// failing the test with a clear message. The graceful shutdown tests assert
// cross-goroutine sequencing, so a non-blocking check would be flaky under
// scheduler delay — tests must stay deterministic (CodeRabbit PR #1189).
func waitForSignal(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
		// expected: the signal arrived
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// requireGracefulOutcome asserts the shared graceful expectations of the stdio
// shutdown tests: cmdMCP returned cleanly (no fatal, no exit panic) and the
// autosync manager both started and was stopped through the graceful sequence.
func requireGracefulOutcome(t *testing.T, stderr string, recovered any, runStarted chan struct{}, stopCalled chan struct{}) {
	t.Helper()
	if recovered != nil {
		t.Fatalf("expected clean run, got panic=%v stderr=%q", recovered, stderr)
	}
	if strings.Contains(stderr, "engram: ") {
		t.Fatalf("expected no fatal output, got %q", stderr)
	}
	// expected: the manager ran before the transport unwound
	waitForSignal(t, runStarted, "autosync manager to start")
	// expected: graceful shutdown sequence released the sync lease
	waitForSignal(t, stopCalled, "autosync manager Stop via the graceful shutdown sequence")
}

// TestCmdMCPStdioEOFRunsGracefulShutdown pins issue #886: when the parent
// closes its end of the stdio pipe, the EOF must trigger the same graceful
// shutdown sequence as a signal (autosync lease release before Listen
// unwinds) and cmdMCP must return cleanly instead of hanging or exiting
// fatal. The real mcp-go transport is covered by the final-tool regression
// below; this focused command test keeps the autosync/store lifecycle isolated.
func TestCmdMCPStdioEOFRunsGracefulShutdown(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	stubMCPStdioLifecycle(t)
	enableAutosyncEnv(t)
	runStarted, stopCalled := stubAutosyncManager(t)

	// Consume through the production EOF wrapper while keeping mcp-go's
	// package-singleton stdio session available to the transport regression.
	serveMCP = runMCPStdio
	listenMCPStdio = func(_ context.Context, _ *mcpserver.MCPServer, stdin io.Reader, _ io.Writer) error {
		_, err := io.Copy(io.Discard, stdin)
		return err
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	mcpStdioInput = stdinR
	t.Cleanup(func() {
		_ = stdinR.Close()
		_ = stdinW.Close()
	})
	// The parent is gone before the child reads: closing the write end of an
	// empty pipe makes the first Read on it return io.EOF.
	_ = stdinW.Close()

	withArgs(t, "engram", "mcp")

	watchdog := time.AfterFunc(10*time.Second, func() {
		panic("cmdMCP did not return after the stdio pipe closed")
	})
	defer watchdog.Stop()

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })

	requireGracefulOutcome(t, stderr, recovered, runStarted, stopCalled)
}

// TestCmdMCPStdioFinalToolCallDrainsBeforeEOFShutdown proves the behavior that
// mcp-go's stdio transport requires: an EOF after the final tools/call lets
// Listen drain its worker queue before autosync stops, then cancels a
// context-aware stuck handler after the bounded deadline.
func TestCmdMCPStdioFinalToolCallDrainsBeforeEOFShutdown(t *testing.T) {
	stubMCPStdioLifecycle(t)
	mcpStdioEOFCancelAfter = 250 * time.Millisecond

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		t.Fatalf("stdout pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	})

	oldStdout := os.Stdout
	os.Stdout = stdoutW
	t.Cleanup(func() { os.Stdout = oldStdout })
	mcpStdioInput = stdinR

	autosyncStopped := make(chan struct{}, 1)
	mcpStdioStopAutosync = func() { autosyncStopped <- struct{}{} }
	allowResponse := make(chan struct{})
	stuckStarted := make(chan struct{}, 1)
	stuckExited := make(chan struct{}, 1)
	mcpSrv := mcpserver.NewMCPServer("test", "1.0.0", mcpserver.WithToolCapabilities(true))
	mcpSrv.AddTool(mcp.NewTool("final_tool"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		<-allowResponse
		if ctx.Err() != nil {
			return nil, nil
		}
		return mcp.NewToolResultText("final response"), nil
	})
	mcpSrv.AddTool(mcp.NewTool("stuck_tool"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		stuckStarted <- struct{}{}
		<-ctx.Done()
		stuckExited <- struct{}{}
		return nil, ctx.Err()
	})

	runDone := make(chan error, 1)
	go func() { runDone <- runMCPStdio(mcpSrv) }()

	writeRequest := func(request map[string]any) {
		t.Helper()
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		if _, err := stdinW.Write(append(encoded, '\n')); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}

	scanner := bufio.NewScanner(stdoutR)
	writeRequest(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]any{"name": "test-client", "version": "1.0.0"},
		},
	})
	initializeResponse := make(chan bool, 1)
	go func() { initializeResponse <- scanner.Scan() }()
	select {
	case ok := <-initializeResponse:
		if !ok {
			t.Fatal("initialize response was not written")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initialize response")
	}

	writeRequest(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": "final_tool"},
	})
	writeRequest(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params":  map[string]any{"name": "stuck_tool"},
	})
	waitForSignal(t, stuckStarted, "stuck tool to start")
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	select {
	case <-autosyncStopped:
		t.Fatal("autosync stopped before the final tools/call response completed")
	case <-time.After(25 * time.Millisecond):
	}
	close(allowResponse)

	response := make(chan []byte, 1)
	go func() {
		if scanner.Scan() {
			response <- append([]byte(nil), scanner.Bytes()...)
			return
		}
		response <- nil
	}()

	select {
	case line := <-response:
		if line == nil {
			t.Fatal("final tools/call response was not written before runMCPStdio returned")
		}
		var payload struct {
			ID     int64 `json:"id"`
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &payload); err != nil {
			t.Fatalf("unmarshal final response: %v", err)
		}
		if payload.ID != 2 || len(payload.Result.Content) != 1 || payload.Result.Content[0].Text != "final response" {
			t.Fatalf("unexpected final tools/call response: %s", line)
		}
	case <-time.After(5 * time.Second):
		select {
		case err := <-runDone:
			t.Fatalf("runMCPStdio returned without final tools/call response: %v", err)
		default:
			t.Fatal("timed out waiting for final tools/call response")
		}
	}
	select {
	case <-autosyncStopped:
		t.Fatal("autosync stopped before the final tools/call response completed")
	default:
	}

	waitForSignal(t, stuckExited, "stuck tool to exit after EOF's cancellation deadline")
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("runMCPStdio returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runMCPStdio did not return after EOF cancelled the stuck handler")
	}
	waitForSignal(t, autosyncStopped, "autosync stop after runMCPStdio returns")
}

// TestCmdMCPStdioSIGTERMRunsGracefulShutdown pins the signal half of the
// stdio lifecycle: SIGINT/SIGTERM reach runMCPStdio's watcher, run the same
// graceful shutdown sequence, and the context.Canceled returned by the
// transport is translated into a clean nil instead of the historical fatal.
// The signal channel is captured via the notifySignals/stopSignals seams the
// same way TestCmdServeSignalClosesUnixSocket drives cmdServe, and the
// transport itself is a stand-in Listen that blocks until its context is
// cancelled so the mcp-go stdio session singleton is not registered twice.
func TestCmdMCPStdioSIGTERMRunsGracefulShutdown(t *testing.T) {
	cfg := testConfig(t)
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	stubMCPStdioLifecycle(t)
	enableAutosyncEnv(t)
	runStarted, stopCalled := stubAutosyncManager(t)

	oldNotify, oldStop := notifySignals, stopSignals
	registered := make(chan chan<- os.Signal, 1)
	notifySignals = func(ch chan<- os.Signal, _ ...os.Signal) { registered <- ch }
	stopSignals = func(chan<- os.Signal) {}
	t.Cleanup(func() {
		notifySignals = oldNotify
		stopSignals = oldStop
	})

	// Stand-in transport: blocks until the lifecycle context is cancelled,
	// then reports context.Canceled exactly like mcp-go's Listen does.
	listenMCPStdio = func(ctx context.Context, _ *mcpserver.MCPServer, _ io.Reader, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}

	withArgs(t, "engram", "mcp")

	watchdog := time.AfterFunc(10*time.Second, func() {
		panic("cmdMCP did not return after SIGTERM")
	})
	defer watchdog.Stop()

	go func() {
		var sigCh chan<- os.Signal
		select {
		case sigCh = <-registered:
		case <-time.After(5 * time.Second):
			panic("runMCPStdio did not register signal handling")
		}
		sigCh <- syscall.SIGTERM
	}()

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdMCP(cfg) })

	requireGracefulOutcome(t, stderr, recovered, runStarted, stopCalled)
}

// constErrReader always returns the configured error.
type constErrReader struct{ err error }

func (r *constErrReader) Read([]byte) (int, error) { return 0, r.err }

// dataWithEOFReader models an underlying stream whose final Read returns its
// remaining bytes together with io.EOF — the (n>0, io.EOF) shape an
// os.Pipe read produces when the parent closes the pipe after writing.
type dataWithEOFReader struct {
	data string
	done bool
}

func (r *dataWithEOFReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(p, r.data), io.EOF
}

// TestEOFShutdownReaderPropagatesAndUnwindsOnce triangulates the stdin
// wrapper itself: bytes pass through untouched, and the first EOF or read
// error runs the graceful shutdown sequence exactly once before the original
// result is propagated to the transport.
func TestEOFShutdownReaderPropagatesAndUnwindsOnce(t *testing.T) {
	newCounter := func() (unwind func(), count func() int) {
		var once sync.Once
		var calls int
		return func() { once.Do(func() { calls++ }) }, func() int { return calls }
	}

	t.Run("bytes pass through untouched", func(t *testing.T) {
		unwind, count := newCounter()
		r := &eofShutdownReader{inner: strings.NewReader("ping"), onUnwind: unwind}
		buf := make([]byte, 4)
		n, err := r.Read(buf)
		if err != nil || string(buf[:n]) != "ping" {
			t.Fatalf("expected passthrough of %q, got n=%d err=%v", "ping", n, err)
		}
		if count() != 0 {
			t.Fatal("graceful sequence must not run while data still flows")
		}
	})

	t.Run("EOF triggers graceful sequence once", func(t *testing.T) {
		unwind, count := newCounter()
		r := &eofShutdownReader{
			inner:    io.MultiReader(strings.NewReader("ping"), &constErrReader{err: io.EOF}),
			onUnwind: unwind,
		}
		buf := make([]byte, 4)
		n, err := r.Read(buf)
		if err != nil || string(buf[:n]) != "ping" {
			t.Fatalf("expected passthrough of %q, got n=%d err=%v", "ping", n, err)
		}
		_, err = r.Read(buf)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected io.EOF to propagate, got %v", err)
		}
		if count() != 1 {
			t.Fatalf("expected graceful sequence exactly once on EOF, got %d", count())
		}
		// bufio may issue further reads after EOF; the sequence must stay
		// once-guarded.
		_, _ = r.Read(buf)
		if count() != 1 {
			t.Fatalf("graceful sequence must stay once-guarded, got %d", count())
		}
	})

	t.Run("read error triggers the same graceful sequence", func(t *testing.T) {
		unwind, count := newCounter()
		r := &eofShutdownReader{inner: &constErrReader{err: errors.New("broken pipe")}, onUnwind: unwind}
		buf := make([]byte, 4)
		_, err := r.Read(buf)
		if err == nil || err.Error() != "broken pipe" {
			t.Fatalf("expected original read error to propagate, got %v", err)
		}
		if count() != 1 {
			t.Fatalf("expected graceful sequence exactly once on read error, got %d", count())
		}
	})

	// CodeRabbit PR #1189: data and EOF arriving together must not unwind
	// before the transport has processed the final buffered request.
	t.Run("data with EOF defers unwind until retained EOF surfaces", func(t *testing.T) {
		unwind, count := newCounter()
		r := &eofShutdownReader{inner: &dataWithEOFReader{data: "line\n"}, onUnwind: unwind}
		buf := make([]byte, 8)

		n, err := r.Read(buf)
		if err != nil || string(buf[:n]) != "line\n" {
			t.Fatalf("expected data with nil error, got n=%d err=%v", n, err)
		}
		if count() != 0 {
			t.Fatal("graceful sequence must not run while the final request is still buffered")
		}

		n, err = r.Read(buf)
		if n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("expected retained (0, io.EOF), got n=%d err=%v", n, err)
		}
		if count() != 1 {
			t.Fatalf("expected graceful sequence exactly once when retained EOF surfaces, got %d", count())
		}

		// The retained EOF stays sticky; the sequence stays once-guarded.
		_, _ = r.Read(buf)
		if count() != 1 {
			t.Fatalf("graceful sequence must stay once-guarded, got %d", count())
		}
	})

	// bufio.Reader sits on top of the wrapper in production; the final
	// buffered request must be served before the EOF-triggered unwind.
	t.Run("bufio serves buffered final request before EOF unwind", func(t *testing.T) {
		unwind, count := newCounter()
		br := bufio.NewReader(&eofShutdownReader{inner: &dataWithEOFReader{data: "line\n"}, onUnwind: unwind})

		line, err := br.ReadString('\n')
		if err != nil || line != "line\n" {
			t.Fatalf("expected buffered final request %q with nil error, got %q err=%v", "line\n", line, err)
		}
		if count() != 0 {
			t.Fatal("graceful sequence must not run before the buffered request is served")
		}

		if _, err := br.ReadString('\n'); !errors.Is(err, io.EOF) {
			t.Fatalf("expected io.EOF after the buffer drained, got %v", err)
		}
		if count() != 1 {
			t.Fatalf("expected graceful sequence exactly once after bufio surfaced EOF, got %d", count())
		}
	})
}
