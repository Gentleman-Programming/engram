package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// init() child emulation for `engram run -d` (issue #2228): when the parent
// test re-executes the test binary with ENGRAM_RUN_TEST_CHILD=1, this runs
// before any test machinery and behaves like the production daemon child —
// config from storeDefaultConfig with the data dir taken from
// ENGRAM_DATA_DIR, port from ENGRAM_RUN_TEST_PORT, then serveWith honors the
// inherited readiness env markers (ENGRAM_RUN_READY_FILE / ENGRAM_RUN_READY_TOKEN)
// through the onReady hook.
func init() {
	if os.Getenv("ENGRAM_RUN_SLEEPY_CHILD") == "1" {
		// Readiness-timeout fixture: a child that starts, records its pid, and
		// then never becomes ready (a daemon wedged on a store migration that
		// outlasts the parent's readiness deadline behaves exactly like this).
		if pidFile := os.Getenv("ENGRAM_RUN_SLEEPY_PIDFILE"); pidFile != "" {
			_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if os.Getenv("ENGRAM_RUN_TEST_CHILD") != "1" {
		return
	}
	cfg, err := storeDefaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if dir := strings.TrimSpace(os.Getenv("ENGRAM_DATA_DIR")); dir != "" {
		cfg.DataDir = dir
	}
	port, err := strconv.Atoi(os.Getenv("ENGRAM_RUN_TEST_PORT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := serveWith(cfg, serveOptions{port: port}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// clearRunEnv neutralizes the environment variables parseRunArgs reads so a
// case starts from the documented defaults; individual cases re-set the one
// under test with t.Setenv.
func clearRunEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ENGRAM_PORT", "")
	t.Setenv("ENGRAM_SOCKET", "")
}

// freeRunPort reserves an ephemeral TCP port and releases it for the caller.
func freeRunPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for a free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close port probe listener: %v", err)
	}
	return port
}

// newRunTestConfig returns a config whose data dir is a fresh temp dir, with
// ENGRAM_DATA_DIR exported so the spawned daemon child resolves the same dir.
func newRunTestConfig(t *testing.T) (store.Config, string) {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("ENGRAM_DATA_DIR", dataDir)
	cfg, err := storeDefaultConfig()
	if err != nil {
		t.Fatalf("default config: %v", err)
	}
	cfg.DataDir = dataDir
	return cfg, dataDir
}

// swapRunDaemonSeams redirects the detach seams at this test binary: the child
// gets no positional arguments (the init() marker above drives it) and the
// run-test env markers alongside the production readiness markers.
func swapRunDaemonSeams(t *testing.T, port int) {
	t.Helper()
	oldArgs, oldEnv := spawnDaemonArgs, spawnDaemonEnv
	spawnDaemonArgs = func(int) []string { return nil }
	spawnDaemonEnv = func(readyPath, token string) []string {
		return append(os.Environ(),
			"ENGRAM_RUN_TEST_CHILD=1",
			"ENGRAM_RUN_TEST_PORT="+strconv.Itoa(port),
			"ENGRAM_RUN_READY_FILE="+readyPath,
			"ENGRAM_RUN_READY_TOKEN="+token,
		)
	}
	t.Cleanup(func() {
		spawnDaemonArgs = oldArgs
		spawnDaemonEnv = oldEnv
	})
}

// TestParseRunArgs pins `engram run` argument parsing: -d, -p in both spellings,
// the error cases, and the ENGRAM_PORT / ENGRAM_SOCKET interactions.
func TestParseRunArgs(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		clearRunEnv(t)
		options, err := parseRunArgs(nil)
		if err != nil {
			t.Fatalf("parse nil args: %v", err)
		}
		if options.port != 7437 || options.detach || options.portExplicit {
			t.Fatalf("defaults = %+v, want port 7437, no detach, no explicit port", options)
		}
	})
	t.Run("detach flags", func(t *testing.T) {
		clearRunEnv(t)
		for _, arg := range []string{"-d", "--detach"} {
			options, err := parseRunArgs([]string{arg})
			if err != nil {
				t.Fatalf("parse %s: %v", arg, err)
			}
			if !options.detach || options.portExplicit {
				t.Fatalf("%s = %+v, want detach without explicit port", arg, options)
			}
		}
	})
	t.Run("port flag spellings", func(t *testing.T) {
		clearRunEnv(t)
		options, err := parseRunArgs([]string{"-p", "1234"})
		if err != nil {
			t.Fatalf("parse -p 1234: %v", err)
		}
		if options.port != 1234 || !options.portExplicit || options.detach {
			t.Fatalf("-p 1234 = %+v, want port 1234 explicit", options)
		}
		options, err = parseRunArgs([]string{"-p=9000"})
		if err != nil {
			t.Fatalf("parse -p=9000: %v", err)
		}
		if options.port != 9000 || !options.portExplicit {
			t.Fatalf("-p=9000 = %+v, want port 9000 explicit", options)
		}
	})
	t.Run("unknown argument", func(t *testing.T) {
		clearRunEnv(t)
		if _, err := parseRunArgs([]string{"bogus"}); err == nil || !strings.Contains(err.Error(), "unknown run argument: bogus") {
			t.Fatalf("unknown arg error = %v, want unknown run argument", err)
		}
	})
	t.Run("port without value", func(t *testing.T) {
		clearRunEnv(t)
		if _, err := parseRunArgs([]string{"-p"}); err == nil || !strings.Contains(err.Error(), "-p requires a port") {
			t.Fatalf("missing port error = %v, want -p requires a port", err)
		}
	})
	t.Run("port out of range", func(t *testing.T) {
		clearRunEnv(t)
		for _, value := range []string{"abc", "0"} {
			_, err := parseRunArgs([]string{"-p", value})
			if err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
				t.Fatalf("-p %q error = %v, want range rejection", value, err)
			}
		}
	})
	t.Run("ENGRAM_PORT fallback", func(t *testing.T) {
		clearRunEnv(t)
		t.Setenv("ENGRAM_PORT", "8123")
		options, err := parseRunArgs(nil)
		if err != nil {
			t.Fatalf("parse with ENGRAM_PORT: %v", err)
		}
		if options.port != 8123 || options.portExplicit {
			t.Fatalf("ENGRAM_PORT fallback = %+v, want port 8123 not explicit", options)
		}
	})
	t.Run("detach refuses ENGRAM_SOCKET", func(t *testing.T) {
		clearRunEnv(t)
		t.Setenv("ENGRAM_SOCKET", "/tmp/engram.sock")
		if _, err := parseRunArgs([]string{"-d"}); err == nil || !strings.Contains(err.Error(), "unset ENGRAM_SOCKET") {
			t.Fatalf("detach + socket error = %v, want unset ENGRAM_SOCKET guidance", err)
		}
	})
	t.Run("socket allows foreground run without explicit port", func(t *testing.T) {
		clearRunEnv(t)
		t.Setenv("ENGRAM_SOCKET", "/tmp/engram.sock")
		if _, err := parseRunArgs(nil); err != nil {
			t.Fatalf("foreground socket run: %v", err)
		}
	})
	t.Run("socket refuses explicit port in foreground", func(t *testing.T) {
		clearRunEnv(t)
		t.Setenv("ENGRAM_SOCKET", "/tmp/engram.sock")
		if _, err := parseRunArgs([]string{"-p", "8123"}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("socket + -p error = %v, want combination rejection", err)
		}
	})
}

// TestRunToken pins the readiness handshake token: present and unique per start.
func TestRunToken(t *testing.T) {
	first, err := runToken()
	if err != nil {
		t.Fatalf("runToken: %v", err)
	}
	if first == "" {
		t.Fatal("runToken returned an empty token")
	}
	second, err := runToken()
	if err != nil {
		t.Fatalf("runToken: %v", err)
	}
	if first == second {
		t.Fatalf("runToken returned the same token twice: %s", first)
	}
}

// TestReadRunReadiness pins the handshake reader: a matching record parses,
// mismatched tokens, malformed JSON, and missing files all count as not ready.
func TestReadRunReadiness(t *testing.T) {
	path := filepath.Join(t.TempDir(), runReadyName)
	if _, err := readRunReadiness(path, "start-token"); err == nil {
		t.Fatal("missing readiness file parsed without error")
	}
	if err := os.WriteFile(path, []byte(`{"token":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunReadiness(path, "start-token"); err == nil {
		t.Fatal("malformed readiness file parsed without error")
	}
	valid := `{"token":"start-token","pid":4242,"port":7437}`
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRunReadiness(path, "other-token"); err == nil {
		t.Fatal("readiness record accepted a foreign token")
	}
	report, err := readRunReadiness(path, "start-token")
	if err != nil {
		t.Fatalf("valid readiness record: %v", err)
	}
	if report.PID != 4242 || report.Port != 7437 || report.Token != "start-token" {
		t.Fatalf("readiness record = %+v, want pid 4242 port 7437 with matching token", report)
	}
}

// TestWriteRunReadiness pins the onReady hook body: a TCP listener publishes
// both the daemon pidfile and the parent handshake record; a non-TCP listener
// is rejected so a half-started daemon never reports success.
func TestWriteRunReadiness(t *testing.T) {
	dataDir := t.TempDir()
	readyPath := filepath.Join(dataDir, runReadyName)
	if err := writeRunReadiness(dataDir, readyPath, "start-token", &net.UnixAddr{Name: "ignored", Net: "unix"}); err == nil || !strings.Contains(err.Error(), "not TCP") {
		t.Fatalf("unix listener error = %v, want non-TCP rejection", err)
	}
	if err := writeRunReadiness(dataDir, readyPath, "start-token", &net.TCPAddr{Port: 7437}); err != nil {
		t.Fatalf("writeRunReadiness: %v", err)
	}
	record, err := daemon.ReadPidfile(daemon.PidfilePath(dataDir))
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	if record.Pid != os.Getpid() || record.Port != 7437 || record.Token != "start-token" {
		t.Fatalf("pidfile record = %+v, want pid %d port 7437 token start-token", record, os.Getpid())
	}
	data, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatalf("read readiness record: %v", err)
	}
	var report runReadiness
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("readiness record is not JSON: %v", err)
	}
	if report.Token != "start-token" || report.PID != os.Getpid() || report.Port != 7437 {
		t.Fatalf("readiness record = %+v, want matching token, pid %d, port 7437", report, os.Getpid())
	}
}

// TestRunDetachedLifecycle covers spec S2 (detached start printing pid and
// port) and S3 (a second start against the live daemon reports
// "already running (pid N)" as an error) end to end with a real child process.
func TestRunDetachedLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	clearRunEnv(t)
	port := freeRunPort(t)
	cfg, dataDir := newRunTestConfig(t)
	swapRunDaemonSeams(t, port)
	// Cleanup reads the pidfile at exit time so an unexpected failure on the
	// start path still reaps whatever daemon the spawn left behind.
	t.Cleanup(func() {
		if record, err := daemon.ReadPidfile(daemon.PidfilePath(dataDir)); err == nil {
			if process, perr := os.FindProcess(record.Pid); perr == nil {
				_ = process.Kill()
				_, _ = process.Wait()
			}
		}
		_ = daemon.RemovePidfile(daemon.PidfilePath(dataDir))
	})

	// S2: the detached start succeeds.
	if err := cmdRun(cfg, []string{"-d", "-p", strconv.Itoa(port)}); err != nil {
		t.Fatalf("detached start: %v", err)
	}
	record, err := daemon.ReadPidfile(daemon.PidfilePath(dataDir))
	if err != nil {
		t.Fatalf("read pidfile after start: %v", err)
	}
	if record.Port != port {
		t.Fatalf("pidfile port = %d, want %d", record.Port, port)
	}
	if !daemon.ProcessAlive(record.Pid) {
		t.Fatalf("daemon pid %d is not alive", record.Pid)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), daemonProbeTimeout)
		res := daemon.Probe(ctx, port, daemonProbeTimeout)
		cancel()
		if res.Status == daemon.StatusRunning {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("daemon probe status = %q, want %q", res.Status, daemon.StatusRunning)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// S3: a second start against the live daemon refuses to double-start.
	err = cmdRun(cfg, []string{"-d", "-p", strconv.Itoa(port)})
	if err == nil || !strings.Contains(err.Error(), "already running (pid") {
		t.Fatalf("second start error = %v, want already running (pid N)", err)
	}
}

// TestRunDetachedPortConflict covers spec S6: `engram run -d -p PORT` with the
// port already held by a foreign listener fails loudly instead of exiting 0.
func TestRunDetachedPortConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	clearRunEnv(t)
	port := freeRunPort(t)
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("hold port %d: %v", port, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	cfg, dataDir := newRunTestConfig(t)
	swapRunDaemonSeams(t, port)

	err = cmdRun(cfg, []string{"-d", "-p", strconv.Itoa(port)})
	if err == nil || !strings.Contains(err.Error(), "daemon did not start") {
		t.Fatalf("port-conflict start error = %v, want a loud daemon-did-not-start failure", err)
	}
	// A failed start must not leave a live daemon behind.
	if record, rerr := daemon.ReadPidfile(daemon.PidfilePath(dataDir)); rerr == nil {
		if daemon.ProcessAlive(record.Pid) {
			t.Fatalf("failed start left a live daemon pid %d", record.Pid)
		}
		_ = daemon.RemovePidfile(daemon.PidfilePath(dataDir))
	}
}

// TestRunDetachedTimeoutTerminatesChild pins the readiness-timeout contract:
// a spawned child that never reports readiness is terminated and reaped by
// the failing parent, and only this start's records are cleaned up, so a
// timed-out start can never leave an orphan that later becomes the daemon.
func TestRunDetachedTimeoutTerminatesChild(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	clearRunEnv(t)
	cfg, dataDir := newRunTestConfig(t)
	childPidFile := filepath.Join(dataDir, "sleepy-child.pid")
	oldArgs, oldEnv := spawnDaemonArgs, spawnDaemonEnv
	spawnDaemonArgs = func(int) []string { return nil }
	spawnDaemonEnv = func(readyPath, token string) []string {
		return append(os.Environ(),
			"ENGRAM_RUN_SLEEPY_CHILD=1",
			"ENGRAM_RUN_SLEEPY_PIDFILE="+childPidFile,
		)
	}
	t.Cleanup(func() { spawnDaemonArgs, spawnDaemonEnv = oldArgs, oldEnv })
	// Even a failing (RED) run must not leak the sleeping child: kill it by
	// the pid it recorded before the parent's cleanup existed.
	t.Cleanup(func() {
		data, err := os.ReadFile(childPidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return
		}
		if daemon.ProcessAlive(pid) {
			if process, perr := os.FindProcess(pid); perr == nil {
				_ = process.Kill()
				_, _ = process.Wait()
			}
		}
	})
	oldTimeout := runReadyTimeout
	runReadyTimeout = 500 * time.Millisecond
	t.Cleanup(func() { runReadyTimeout = oldTimeout })

	err := runDetached(cfg, serveOptions{port: 7602})
	if err == nil || !strings.Contains(err.Error(), "daemon did not start") {
		t.Fatalf("timeout start error = %v, want daemon did not start", err)
	}
	data, err := os.ReadFile(childPidFile)
	if err != nil {
		t.Fatalf("child never recorded its pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("child pid record %q: %v", data, err)
	}
	if daemon.ProcessAlive(pid) {
		t.Fatalf("timed-out start left child pid %d alive", pid)
	}
	if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(dataDir)); !errors.Is(rerr, daemon.ErrNoPidfile) {
		t.Fatalf("pidfile survived failed start: %v", rerr)
	}
	if _, rerr := os.Stat(filepath.Join(dataDir, runReadyName)); !os.IsNotExist(rerr) {
		t.Fatalf("readiness file survived failed start: %v", rerr)
	}
}

// TestAssertDaemonStartableIdentity pins the pid-reuse guard in run
// admission: a live pidfile pid only blocks a new start when the daemon
// identity answers; an unconfirmed record is stale and gets cleared so the
// start proceeds. Legacy token-less records keep the running-probe
// classification the previous release used.
func TestAssertDaemonStartableIdentity(t *testing.T) {
	t.Run("matching token reports already running", func(t *testing.T) {
		dataDir := t.TempDir()
		if err := daemon.WritePidfile(daemon.PidfilePath(dataDir), daemon.PidRecord{Pid: os.Getpid(), Port: 7606, Token: "ours"}); err != nil {
			t.Fatalf("write pidfile: %v", err)
		}
		swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: 7606, Token: "ours"})
		err := assertDaemonStartable(dataDir, 7606)
		if err == nil || !strings.Contains(err.Error(), "already running (pid") {
			t.Fatalf("confirmed admission error = %v, want already running", err)
		}
		if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(dataDir)); rerr != nil {
			t.Fatalf("confirmed record was cleared: %v", rerr)
		}
	})
	t.Run("token mismatch clears the unmanaged pidfile", func(t *testing.T) {
		dataDir := t.TempDir()
		if err := daemon.WritePidfile(daemon.PidfilePath(dataDir), daemon.PidRecord{Pid: os.Getpid(), Port: 7606, Token: "ours"}); err != nil {
			t.Fatalf("write pidfile: %v", err)
		}
		swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: 7606, Token: "foreign"})
		out := swapStdoutPrinter(t)
		if err := assertDaemonStartable(dataDir, 7606); err != nil {
			t.Fatalf("unconfirmed admission: %v", err)
		}
		if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(dataDir)); !errors.Is(rerr, daemon.ErrNoPidfile) {
			t.Fatalf("unconfirmed record survived admission: %v", rerr)
		}
		if !strings.Contains(out.String(), "Clearing unmanaged pidfile") {
			t.Fatalf("unconfirmed admission output = %q, want a clearing note", out.String())
		}
	})
	t.Run("legacy record kept answering stays already running", func(t *testing.T) {
		dataDir := t.TempDir()
		if err := daemon.WritePidfile(daemon.PidfilePath(dataDir), daemon.PidRecord{Pid: os.Getpid(), Port: 7606}); err != nil {
			t.Fatalf("write pidfile: %v", err)
		}
		swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: 7606})
		err := assertDaemonStartable(dataDir, 7606)
		if err == nil || !strings.Contains(err.Error(), "already running (pid") {
			t.Fatalf("legacy confirmed admission error = %v, want already running", err)
		}
		if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(dataDir)); rerr != nil {
			t.Fatalf("legacy confirmed record was cleared: %v", rerr)
		}
	})
}

// TestRunCleanupFailedStartRemovesOnlyOwnRecords pins the failed-start
// cleanup: this start's readiness file and pidfile are removed, while
// records owned by another start (foreign token or a legacy tokenless
// pidfile) are left alone.
func TestRunCleanupFailedStartRemovesOnlyOwnRecords(t *testing.T) {
	dataDir := t.TempDir()
	readyPath := filepath.Join(dataDir, runReadyName)
	writeReadiness := func(token string) {
		t.Helper()
		data, err := json.Marshal(runReadiness{Token: token, PID: 111, Port: 2222})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(readyPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeReadiness("foreign")
	cleanupFailedStart(0, dataDir, readyPath, "ours")
	if _, rerr := os.Stat(readyPath); os.IsNotExist(rerr) {
		t.Fatal("cleanup removed a foreign readiness record")
	}
	writeReadiness("ours")
	cleanupFailedStart(0, dataDir, readyPath, "ours")
	if _, rerr := os.Stat(readyPath); !os.IsNotExist(rerr) {
		t.Fatalf("own readiness record survived cleanup: %v", rerr)
	}

	for _, token := range []string{"foreign", ""} {
		if err := daemon.WritePidfile(daemon.PidfilePath(dataDir), daemon.PidRecord{Pid: 4242, Port: 7437, Token: token}); err != nil {
			t.Fatal(err)
		}
		cleanupFailedStart(0, dataDir, readyPath, "ours")
		if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(dataDir)); rerr != nil {
			t.Fatalf("cleanup removed a pidfile with token %q", token)
		}
	}
	if err := daemon.WritePidfile(daemon.PidfilePath(dataDir), daemon.PidRecord{Pid: 4242, Port: 7437, Token: "ours"}); err != nil {
		t.Fatal(err)
	}
	cleanupFailedStart(0, dataDir, readyPath, "ours")
	if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(dataDir)); !errors.Is(rerr, daemon.ErrNoPidfile) {
		t.Fatalf("own pidfile survived cleanup: %v", rerr)
	}
}

// TestRunDetachedWaitsForStartLock pins the per-data-dir start lock: while
// another start holds it, `engram run -d` fails cleanly without spawning a
// child or touching lifecycle records.
func TestRunDetachedWaitsForStartLock(t *testing.T) {
	clearRunEnv(t)
	cfg, dataDir := newRunTestConfig(t)
	lock, err := daemon.AcquireStartLock(dataDir, 0)
	if err != nil {
		t.Fatalf("hold start lock: %v", err)
	}
	defer func() { _ = lock.Release() }()
	spawned := false
	oldArgs, oldEnv := spawnDaemonArgs, spawnDaemonEnv
	spawnDaemonArgs = func(int) []string { spawned = true; return nil }
	spawnDaemonEnv = func(_, _ string) []string { return os.Environ() }
	t.Cleanup(func() { spawnDaemonArgs, spawnDaemonEnv = oldArgs, oldEnv })
	oldWait := runStartLockWait
	runStartLockWait = 100 * time.Millisecond
	t.Cleanup(func() { runStartLockWait = oldWait })

	err = runDetached(cfg, serveOptions{port: 7607})
	if err == nil || !strings.Contains(err.Error(), "another start is in progress") {
		t.Fatalf("locked start error = %v, want the held-lock failure", err)
	}
	if spawned {
		t.Fatal("locked start spawned a child")
	}
}
