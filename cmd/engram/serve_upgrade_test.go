package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	engramsrv "github.com/Gentleman-Programming/engram/v2/internal/server"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

const selfCheckTempFailCode = 75

// selfCheckStub replaces every process seam of the stale-binary self-check with
// a deterministic double: no real engram binary runs, no real supervisor is
// detected, and exitFunc records codes instead of ending the test process.
type selfCheckStub struct {
	mu               sync.Mutex
	report           engramsrv.StaleBinaryReport
	reportFor        func(call int) engramsrv.StaleBinaryReport
	supervision      engramsrv.SupervisionContext
	supervisionFor   func(call int) engramsrv.SupervisionContext
	calls            int
	supervisionCalls int
	requested        []string
	exitCodes        []int
}

func (s *selfCheckStub) install(t *testing.T) {
	t.Helper()
	oldProbe, oldSupervision, oldExit := staleBinaryProbe, supervisionProbe, exitFunc
	staleBinaryProbe = func(_ context.Context, current string) engramsrv.StaleBinaryReport {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls++
		s.requested = append(s.requested, current)
		if s.reportFor != nil {
			return s.reportFor(s.calls)
		}
		return s.report
	}
	supervisionProbe = func() engramsrv.SupervisionContext {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.supervisionCalls++
		if s.supervisionFor != nil {
			return s.supervisionFor(s.supervisionCalls)
		}
		return s.supervision
	}
	exitFunc = func(code int) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.exitCodes = append(s.exitCodes, code)
	}
	t.Cleanup(func() {
		staleBinaryProbe, supervisionProbe, exitFunc = oldProbe, oldSupervision, oldExit
	})
}

func (s *selfCheckStub) exits() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.exitCodes...)
}

func (s *selfCheckStub) probeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *selfCheckStub) supervisionProbeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.supervisionCalls
}

func (s *selfCheckStub) lastRequested() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requested) == 0 {
		return ""
	}
	return s.requested[len(s.requested)-1]
}

// staleActionableReport is the finding of a supervised daemon whose own binary
// was replaced on disk.
func staleActionableReport() engramsrv.StaleBinaryReport {
	return engramsrv.StaleBinaryReport{
		Known:      true,
		Stale:      true,
		Actionable: true,
		Running:    "2.0.0",
		OnDisk:     "2.1.0",
		Candidate:  "/opt/homebrew/bin/engram",
	}
}

// launchdSupervision is a job reparented to pid 1: the supervised case.
func launchdSupervision() engramsrv.SupervisionContext {
	return engramsrv.SupervisionContext{XPCServiceName: "com.gentleman.engram", PPID: 1}
}

// supervisedLaunchdDecision is the startup decision launchdSupervision resolves to.
func supervisedLaunchdDecision() engramsrv.SupervisorDecision {
	return engramsrv.SupervisorDecision{Supervised: true, Source: engramsrv.SupervisorLaunchd}
}

func captureServeLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &logs
}

func serveHealthPayload(t *testing.T, srv *engramsrv.Server) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200", rec.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode GET /health: %v", err)
	}
	return payload
}

func TestRunServeSelfCheckExitsUnderSupervisor(t *testing.T) {
	stub := &selfCheckStub{report: staleActionableReport(), supervision: launchdSupervision()}
	stub.install(t)
	logs := captureServeLogs(t)

	srv := engramsrv.New(nil, 0)
	srv.SetVersion("2.0.0")

	if !runServeSelfCheck(context.Background(), srv, "2.0.0", supervisedLaunchdDecision(), true) {
		t.Fatal("an actionable finding under a supervisor must ask for a restart")
	}

	if got := stub.exits(); len(got) != 1 || got[0] != selfCheckTempFailCode {
		t.Fatalf("exit codes = %v, want exactly [%d]", got, selfCheckTempFailCode)
	}

	payload := serveHealthPayload(t, srv)
	if payload["binary_stale"] != true || payload["binary_version_on_disk"] != "2.1.0" {
		t.Fatalf("GET /health = %v, want the stale finding published", payload)
	}

	line := logs.String()
	if !strings.Contains(line, "2.0.0") || !strings.Contains(line, "2.1.0") {
		t.Fatalf("log line %q must name both the running and the on-disk version", line)
	}
}

func TestRunServeSelfCheckReportsWithoutExiting(t *testing.T) {
	tests := []struct {
		name        string
		report      engramsrv.StaleBinaryReport
		supervision engramsrv.SupervisorDecision
		wantHealth  bool
	}{
		{
			name:        "a different install is a report",
			report:      engramsrv.StaleBinaryReport{Known: true, Stale: true, Running: "2.0.0", OnDisk: "2.1.0"},
			supervision: supervisedLaunchdDecision(),
			wantHealth:  true,
		},
		{
			name:        "a process started without a supervisor keeps serving",
			report:      staleActionableReport(),
			supervision: engramsrv.SupervisorDecision{},
			wantHealth:  true,
		},
		{
			name:        "a startup opt-out disables the self exit",
			report:      staleActionableReport(),
			supervision: engramsrv.SupervisorDecision{Disabled: true},
			wantHealth:  true,
		},
		{
			name:        "an unknown on-disk version is reported without a restart",
			report:      engramsrv.StaleBinaryReport{},
			supervision: supervisedLaunchdDecision(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &selfCheckStub{report: tt.report}
			stub.install(t)
			logs := captureServeLogs(t)

			srv := engramsrv.New(nil, 0)
			srv.SetVersion("2.0.0")

			if runServeSelfCheck(context.Background(), srv, "2.0.0", tt.supervision, true) {
				t.Fatal("only an actionable finding under a supervisor may ask for a restart")
			}
			if got := stub.exits(); len(got) != 0 {
				t.Fatalf("exit codes = %v, want none", got)
			}

			payload := serveHealthPayload(t, srv)
			_, staleReported := payload["binary_stale"]
			_, versionReported := payload["binary_version_on_disk"]
			if staleReported != tt.wantHealth || versionReported != tt.wantHealth {
				t.Fatalf("GET /health = %v, want binary diagnostics reported=%v", payload, tt.wantHealth)
			}
			if staleReported && logs.String() == "" {
				t.Fatal("a known stale binary must be reported even when the process keeps serving")
			}
			if !tt.wantHealth && logs.String() == "" {
				t.Fatal("an unknown comparison must be reported once instead of staying silent")
			}
		})
	}
}

func TestRunServeSelfCheckStopsLoggingUnknownFindings(t *testing.T) {
	stub := &selfCheckStub{
		report:      engramsrv.StaleBinaryReport{},
		supervision: launchdSupervision(),
	}
	stub.install(t)
	logs := captureServeLogs(t)

	if runServeSelfCheck(context.Background(), engramsrv.New(nil, 0), "2.0.0", supervisedLaunchdDecision(), false) {
		t.Fatal("an unknown comparison must never ask for a restart")
	}
	if got := logs.String(); got != "" {
		t.Fatalf("log output = %q, want no per-interval noise for an unknown comparison", got)
	}
}

func TestStartServeSelfCheckRechecksOnTheConfiguredInterval(t *testing.T) {
	t.Setenv(engramsrv.SelfCheckIntervalEnvVar, "5ms")
	stub := &selfCheckStub{
		reportFor: func(call int) engramsrv.StaleBinaryReport {
			if call < 2 {
				return engramsrv.StaleBinaryReport{Known: true, Running: "2.0.0", OnDisk: "2.0.0"}
			}
			return staleActionableReport()
		},
		supervision: launchdSupervision(),
	}
	stub.install(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := engramsrv.New(nil, 0)
	srv.SetVersion("2.0.0")

	if startServeSelfCheck(ctx, srv, "2.0.0") {
		t.Fatal("a current binary at startup must not ask for a restart")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(stub.exits()) == 0 {
		time.Sleep(2 * time.Millisecond)
	}

	if got := stub.exits(); len(got) != 1 || got[0] != selfCheckTempFailCode {
		t.Fatalf("exit codes = %v, want [%d] from a periodic re-check", got, selfCheckTempFailCode)
	}
	if got := stub.probeCount(); got < 2 {
		t.Fatalf("probe ran %d times, want the startup check plus at least one periodic re-check", got)
	}
}

func TestStartServeSelfCheckZeroIntervalDisablesTheRecheck(t *testing.T) {
	t.Setenv(engramsrv.SelfCheckIntervalEnvVar, "0")
	stub := &selfCheckStub{
		report:      engramsrv.StaleBinaryReport{Known: true, Running: "2.0.0", OnDisk: "2.0.0"},
		supervision: launchdSupervision(),
	}
	stub.install(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if startServeSelfCheck(ctx, engramsrv.New(nil, 0), "2.0.0") {
		t.Fatal("a current binary must not ask for a restart")
	}
	time.Sleep(30 * time.Millisecond)

	if got := stub.probeCount(); got != 1 {
		t.Fatalf("probe ran %d times, want only the startup check when the interval is 0", got)
	}
	if got := stub.exits(); len(got) != 0 {
		t.Fatalf("exit codes = %v, want none", got)
	}
}

func TestStartServeSelfCheckNeverLoopsAfterAnExit(t *testing.T) {
	t.Setenv(engramsrv.SelfCheckIntervalEnvVar, "5ms")
	stub := &selfCheckStub{report: staleActionableReport(), supervision: launchdSupervision()}
	stub.install(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !startServeSelfCheck(ctx, engramsrv.New(nil, 0), "2.0.0") {
		t.Fatal("an actionable finding under a supervisor must ask for a restart")
	}
	time.Sleep(30 * time.Millisecond)

	if got := stub.exits(); len(got) != 1 {
		t.Fatalf("exit codes = %v, want exactly one restart request", got)
	}
	if got := stub.probeCount(); got != 1 {
		t.Fatalf("probe ran %d times, want no re-check once the process is exiting", got)
	}
}

func TestStartServeSelfCheckFreezesSupervisionAtStartup(t *testing.T) {
	tests := []struct {
		name     string
		startup  engramsrv.SupervisionContext
		later    engramsrv.SupervisionContext
		wantExit bool
	}{
		{
			// The falsified case: the Pi plugin spawns the server detached and
			// unref'd, so the orphan is reparented to PID 1 when Pi exits while
			// still holding the inherited launchd variable. Nothing would relaunch
			// it, so the frozen startup decision must keep it serving.
			name:    "a plugin-spawned child orphaned to pid 1 stays unsupervised",
			startup: engramsrv.SupervisionContext{XPCServiceName: "com.gentleman.engram", PPID: 4242},
			later:   engramsrv.SupervisionContext{XPCServiceName: "com.gentleman.engram", PPID: 1},
		},
		{
			name:     "a launchd job stays supervised when the environment changes later",
			startup:  launchdSupervision(),
			later:    engramsrv.SupervisionContext{PPID: 4242},
			wantExit: true,
		},
		{
			name:     "a systemd job keeps its startup parent-comm evidence",
			startup:  engramsrv.SupervisionContext{InvocationID: "9f1a", PPID: 900, ParentComm: "systemd\n"},
			later:    engramsrv.SupervisionContext{InvocationID: "9f1a", PPID: 900},
			wantExit: true,
		},
		{
			name:     "an override of 1 at startup forces the restart path",
			startup:  engramsrv.SupervisionContext{Override: "1", PPID: 4242},
			later:    engramsrv.SupervisionContext{PPID: 4242},
			wantExit: true,
		},
		{
			name:    "an override of 0 at startup keeps the process serving",
			startup: engramsrv.SupervisionContext{XPCServiceName: "com.gentleman.engram", PPID: 1, Override: "0"},
			later:   engramsrv.SupervisionContext{XPCServiceName: "com.gentleman.engram", PPID: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(engramsrv.SelfCheckIntervalEnvVar, "5ms")
			stub := &selfCheckStub{
				reportFor: func(call int) engramsrv.StaleBinaryReport {
					if call < 2 {
						return engramsrv.StaleBinaryReport{Known: true, Running: "2.0.0", OnDisk: "2.0.0"}
					}
					return staleActionableReport()
				},
				supervisionFor: func(call int) engramsrv.SupervisionContext {
					if call == 1 {
						return tt.startup
					}
					return tt.later
				},
			}
			stub.install(t)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			srv := engramsrv.New(nil, 0)
			srv.SetVersion("2.0.0")

			if startServeSelfCheck(ctx, srv, "2.0.0") {
				t.Fatal("a current binary at startup must not ask for a restart")
			}

			if tt.wantExit {
				if exits := waitForExit(t, stub); len(exits) != 1 || exits[0] != selfCheckTempFailCode {
					t.Fatalf("exit codes = %v, want [%d] from the startup decision", exits, selfCheckTempFailCode)
				}
			} else {
				waitForRechecks(t, stub, 3)
			}

			if got := stub.supervisionProbeCount(); got != 1 {
				t.Fatalf("supervision was resolved %d times, want exactly once at startup", got)
			}
		})
	}
}

// waitForExit blocks until the self-check asked for a restart and returns the
// recorded exit codes.
func waitForExit(t *testing.T, stub *selfCheckStub) []int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if exits := stub.exits(); len(exits) != 0 {
			return exits
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no restart was requested after %d probes, want the startup decision to be honoured", stub.probeCount())
	return nil
}

// waitForRechecks blocks until the periodic check ran count times. It fails as
// soon as the process asks for a restart, because every caller models a process
// that nothing would relaunch.
func waitForRechecks(t *testing.T, stub *selfCheckStub, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if exits := stub.exits(); len(exits) != 0 {
			t.Fatalf("exit codes = %v, want none: this process has no supervisor to relaunch it", exits)
		}
		if stub.probeCount() >= count {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("probe ran %d times, want at least %d", stub.probeCount(), count)
}

func TestCmdServeStaleBinaryExitLeavesThePortFree(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "serve")
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "")
	t.Setenv(engramsrv.SelfCheckIntervalEnvVar, "0")

	oldVersion := version
	version = "2.0.0"
	t.Cleanup(func() { version = oldVersion })

	stub := &selfCheckStub{report: staleActionableReport(), supervision: launchdSupervision()}
	stub.install(t)

	oldStartHTTP := startHTTP
	startedHTTP := false
	startHTTP = func(_ *engramsrv.Server) error {
		startedHTTP = true
		return nil
	}
	t.Cleanup(func() { startHTTP = oldStartHTTP })

	cmdServe(cfg)

	if got := stub.exits(); len(got) != 1 || got[0] != selfCheckTempFailCode {
		t.Fatalf("exit codes = %v, want [%d]", got, selfCheckTempFailCode)
	}
	if startedHTTP {
		t.Fatal("the listener must never bind when the process exits for a restart")
	}
	if got := stub.lastRequested(); got != "2.0.0" {
		t.Fatalf("self-check ran for version %q, want the build version", got)
	}
}

func TestCmdServeSelfCheckUsesTheBuildVersion(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "serve")
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "")
	t.Setenv(engramsrv.SelfCheckIntervalEnvVar, "0")

	oldVersion := version
	version = "3.4.5"
	t.Cleanup(func() { version = oldVersion })

	stub := &selfCheckStub{
		report:      engramsrv.StaleBinaryReport{Known: true, Running: "3.4.5", OnDisk: "3.4.5"},
		supervision: launchdSupervision(),
	}
	stub.install(t)

	oldStartHTTP := startHTTP
	oldNewHTTPServer := newHTTPServer
	var captured *engramsrv.Server
	newHTTPServer = func(s *store.Store, _ int) *engramsrv.Server {
		captured = engramsrv.New(s, 0)
		return captured
	}
	startHTTP = func(_ *engramsrv.Server) error { return nil }
	t.Cleanup(func() {
		startHTTP = oldStartHTTP
		newHTTPServer = oldNewHTTPServer
	})

	cmdServe(cfg)

	if got := stub.lastRequested(); got != "3.4.5" {
		t.Fatalf("self-check compared against %q, want the ldflags build version", got)
	}
	if captured == nil {
		t.Fatal("cmdServe did not create an HTTP server")
	}
	payload := serveHealthPayload(t, captured)
	if payload["binary_stale"] != false || payload["binary_version_on_disk"] != "3.4.5" {
		t.Fatalf("GET /health = %v, want the current-binary finding", payload)
	}
}
