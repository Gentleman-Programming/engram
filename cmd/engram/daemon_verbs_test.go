package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// ─── Test helpers (seams and fixtures for the daemon control verbs) ─────────

// swapStdoutPrinter redirects the daemon verbs' status-line seam into a
// buffer and restores it when the test ends.
func swapStdoutPrinter(t *testing.T) *strings.Builder {
	t.Helper()
	captured := &strings.Builder{}
	saved := stdoutPrinter
	stdoutPrinter = func(format string, a ...any) (int, error) {
		return fmt.Fprintf(captured, format, a...)
	}
	t.Cleanup(func() { stdoutPrinter = saved })
	return captured
}

// swapDaemonStatusProbe stubs the `engram ps` health probe so unit tests can
// pin status handling without a live daemon; it returns where the probed
// port is recorded so tests can assert the pidfile port was used.
func swapDaemonStatusProbe(t *testing.T, result daemon.ProbeResult) *int {
	t.Helper()
	probedPort := 0
	saved := daemonStatusProbe
	daemonStatusProbe = func(port int) daemon.ProbeResult {
		probedPort = port
		return result
	}
	t.Cleanup(func() { daemonStatusProbe = saved })
	return &probedPort
}

// captureStdoutPipe runs fn with os.Stdout piped and returns everything fn
// printed directly to stdout (cmdLogs writes with io.Copy, not the seam).
func captureStdoutPipe(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = saved }()
	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout capture writer: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stdout capture reader: %v", err)
	}
	return string(data)
}

// exitedChildPid returns the pid of a real process that already exited and
// was reaped: the only honest "definitely dead" pid a unit test can obtain.
func exitedChildPid(t *testing.T) int {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	child := exec.Command(executable, "-test.run=TestDaemonVerbsNoSuchChildTest")
	if err := child.Run(); err != nil {
		t.Fatalf("run sacrificial child process: %v", err)
	}
	if child.Process == nil || child.Process.Pid <= 0 {
		t.Fatal("sacrificial child left no pid behind")
	}
	return child.Process.Pid
}

// cleanupPidfileDaemon kills the pidfile-tracked daemon if one survives the
// test so a failing assertion never leaks a daemon process.
func cleanupPidfileDaemon(t *testing.T, dataDir string) {
	t.Helper()
	t.Cleanup(func() {
		record, err := daemon.ReadPidfile(daemon.PidfilePath(dataDir))
		if err != nil {
			return
		}
		// Never kill this test binary itself: a failed case can leave a
		// pidfile recording os.Getpid() behind (the product stop path refuses
		// the same pid).
		if record.Pid != os.Getpid() && daemon.ProcessAlive(record.Pid) {
			if process, perr := os.FindProcess(record.Pid); perr == nil {
				_ = process.Kill()
				_, _ = process.Wait()
			}
		}
		_ = daemon.RemovePidfile(daemon.PidfilePath(dataDir))
	})
}

// ─── Unit tests ─────────────────────────────────────────────────────────────

// TestCmdPsStatuses pins `engram ps` (spec S4): no pidfile, stale pidfile,
// live-and-answering, live-but-not-answering, and argument rejection.
func TestCmdPsStatuses(t *testing.T) {
	t.Run("no pidfile reports not running", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		out := swapStdoutPrinter(t)
		err := cmdPs(cfg, nil)
		if err == nil || !strings.Contains(err.Error(), "daemon is not running") {
			t.Fatalf("no-pidfile error = %v, want daemon is not running", err)
		}
		if !strings.Contains(out.String(), "not running") {
			t.Fatalf("no-pidfile output = %q, want not running", out.String())
		}
	})
	t.Run("stale pidfile reports not running and is removed", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		deadPid := exitedChildPid(t)
		if err := daemon.WritePidfile(daemon.PidfilePath(cfg.DataDir), daemon.PidRecord{Pid: deadPid, Port: 7437}); err != nil {
			t.Fatalf("write stale pidfile: %v", err)
		}
		out := swapStdoutPrinter(t)
		err := cmdPs(cfg, nil)
		if err == nil || !strings.Contains(err.Error(), "daemon is not running") {
			t.Fatalf("stale-pidfile error = %v, want daemon is not running", err)
		}
		if !strings.Contains(out.String(), "not running") {
			t.Fatalf("stale-pidfile output = %q, want not running", out.String())
		}
		if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(cfg.DataDir)); !errors.Is(rerr, daemon.ErrNoPidfile) {
			t.Fatalf("stale pidfile survived ps: %v", rerr)
		}
	})
	t.Run("live pid with answering probe reports running", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		if err := daemon.WritePidfile(daemon.PidfilePath(cfg.DataDir), daemon.PidRecord{Pid: os.Getpid(), Port: 7600}); err != nil {
			t.Fatalf("write pidfile: %v", err)
		}
		probedPort := swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: 7600})
		out := swapStdoutPrinter(t)
		if err := cmdPs(cfg, nil); err != nil {
			t.Fatalf("ps on running daemon: %v", err)
		}
		want := fmt.Sprintf("running (pid %d) on port 7600", os.Getpid())
		if !strings.Contains(out.String(), want) {
			t.Fatalf("running output = %q, want %q", out.String(), want)
		}
		if *probedPort != 7600 {
			t.Fatalf("probed port = %d, want the pidfile port 7600", *probedPort)
		}
	})
	t.Run("live pid with dead port reports unreachable", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		if err := daemon.WritePidfile(daemon.PidfilePath(cfg.DataDir), daemon.PidRecord{Pid: os.Getpid(), Port: 7601}); err != nil {
			t.Fatalf("write pidfile: %v", err)
		}
		swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusNotRunning, Port: 7601})
		out := swapStdoutPrinter(t)
		err := cmdPs(cfg, nil)
		if err == nil || !strings.Contains(err.Error(), "daemon is not answering on port 7601") {
			t.Fatalf("unreachable error = %v, want daemon is not answering on port 7601", err)
		}
		want := fmt.Sprintf("unreachable (pid %d) on port 7601", os.Getpid())
		if !strings.Contains(out.String(), want) {
			t.Fatalf("unreachable output = %q, want %q", out.String(), want)
		}
	})
	t.Run("unknown argument is rejected", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		err := cmdPs(cfg, []string{"-x"})
		if err == nil || !strings.Contains(err.Error(), "unknown ps argument: -x") {
			t.Fatalf("unknown arg error = %v, want unknown ps argument: -x", err)
		}
	})
}

// TestCmdStopCases pins `engram ps`-adjacent `engram stop` (spec S5): a
// missing pidfile is nothing to stop and a stale one is cleaned, not killed.
func TestCmdStopCases(t *testing.T) {
	t.Run("no pidfile is nothing to stop", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		out := swapStdoutPrinter(t)
		err := cmdStop(cfg, nil)
		if err == nil || !strings.Contains(err.Error(), "no daemon pidfile; nothing to stop") {
			t.Fatalf("no-pidfile stop error = %v, want nothing to stop", err)
		}
		if out.Len() != 0 {
			t.Fatalf("no-pidfile stop printed %q, want silence", out.String())
		}
	})
	t.Run("stale pidfile is removed without stopping", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		deadPid := exitedChildPid(t)
		if err := daemon.WritePidfile(daemon.PidfilePath(cfg.DataDir), daemon.PidRecord{Pid: deadPid, Port: 7437}); err != nil {
			t.Fatalf("write stale pidfile: %v", err)
		}
		out := swapStdoutPrinter(t)
		if err := cmdStop(cfg, nil); err != nil {
			t.Fatalf("stop with stale pidfile: %v", err)
		}
		want := fmt.Sprintf("Removed stale pidfile (pid %d was not running)", deadPid)
		if !strings.Contains(out.String(), want) {
			t.Fatalf("stale stop output = %q, want %q", out.String(), want)
		}
		if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(cfg.DataDir)); !errors.Is(rerr, daemon.ErrNoPidfile) {
			t.Fatalf("pidfile survived stale stop: %v", rerr)
		}
	})
}

// TestCmdStopRejectsUnknownArgs pins that `engram stop` rejects unknown
// arguments (a mistyped option or `stop --help`) before doing anything
// destructive: the pidfile is left in place and nothing is signaled, mirroring
// `engram ps` argument handling.
func TestCmdStopRejectsUnknownArgs(t *testing.T) {
	cfg := store.Config{DataDir: t.TempDir()}
	deadPid := exitedChildPid(t)
	if err := daemon.WritePidfile(daemon.PidfilePath(cfg.DataDir), daemon.PidRecord{Pid: deadPid, Port: 7437}); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	out := swapStdoutPrinter(t)
	for _, arg := range []string{"--help", "-h", "--bogus"} {
		err := cmdStop(cfg, []string{arg})
		if err == nil || !strings.Contains(err.Error(), "unknown stop argument: "+arg) {
			t.Fatalf("stop %s error = %v, want unknown stop argument: %s", arg, err, arg)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("stop with arguments printed %q, want silence", out.String())
	}
	if _, err := daemon.ReadPidfile(daemon.PidfilePath(cfg.DataDir)); err != nil {
		t.Fatalf("argument rejection must not touch the pidfile: %v", err)
	}
}

// TestCmdLogsBasics pins `engram logs` (spec S7): replay of the daemon log
// sink, the missing-log error, follow-flag parsing, and argument rejection.
func TestCmdLogsBasics(t *testing.T) {
	t.Run("prints the daemon log", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		file, err := daemon.OpenLog(daemon.LogPath(cfg.DataDir))
		if err != nil {
			t.Fatalf("open daemon log: %v", err)
		}
		if _, err := file.WriteString("daemon line one\ndaemon line two\n"); err != nil {
			t.Fatalf("seed daemon log: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close daemon log: %v", err)
		}
		var logsErr error
		output := captureStdoutPipe(t, func() { logsErr = cmdLogs(cfg, nil) })
		if logsErr != nil {
			t.Fatalf("logs: %v", logsErr)
		}
		if !strings.Contains(output, "daemon line one") || !strings.Contains(output, "daemon line two") {
			t.Fatalf("logs output = %q, want both seeded lines", output)
		}
	})
	t.Run("missing log is reported", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		err := cmdLogs(cfg, nil)
		if err == nil || !strings.Contains(err.Error(), "no daemon log yet at ") {
			t.Fatalf("missing log error = %v, want no daemon log yet at <path>", err)
		}
	})
	t.Run("follow flags parse before the log is read", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		for _, arg := range []string{"-f", "--follow"} {
			err := cmdLogs(cfg, []string{arg})
			if err == nil || !strings.Contains(err.Error(), "no daemon log yet at ") {
				t.Fatalf("logs %s error = %v, want the missing-log error after parsing", arg, err)
			}
		}
	})
	t.Run("unknown argument is rejected", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		err := cmdLogs(cfg, []string{"--bogus"})
		if err == nil || !strings.Contains(err.Error(), "unknown logs argument: --bogus") {
			t.Fatalf("unknown arg error = %v, want unknown logs argument: --bogus", err)
		}
	})
}

// TestCmdRestartPortResolution pins `engram restart`'s port resolution
// (spec S8): -p wins, the stopped record's port is the fallback, 7437 is the
// default, and malformed ports and unknown arguments are rejected.
func TestCmdRestartPortResolution(t *testing.T) {
	t.Run("explicit -p wins over the pidfile port", func(t *testing.T) {
		for _, args := range [][]string{{"-p", "9999"}, {"-p=9999"}} {
			port, err := resolveRestartPort(args, daemon.PidRecord{Pid: 4242, Port: 1234})
			if err != nil {
				t.Fatalf("resolve %v: %v", args, err)
			}
			if port != 9999 {
				t.Fatalf("resolve %v = %d, want 9999", args, port)
			}
		}
	})
	t.Run("stopped record port is the fallback", func(t *testing.T) {
		port, err := resolveRestartPort(nil, daemon.PidRecord{Pid: 4242, Port: 1234})
		if err != nil {
			t.Fatalf("resolve nil args: %v", err)
		}
		if port != 1234 {
			t.Fatalf("fallback port = %d, want the record port 1234", port)
		}
	})
	t.Run("default is 7437 without a usable record", func(t *testing.T) {
		clearRunEnv(t)
		for _, record := range []daemon.PidRecord{{}, {Pid: 4242}, {Pid: 4242, Port: 0}} {
			port, err := resolveRestartPort(nil, record)
			if err != nil {
				t.Fatalf("resolve with %+v: %v", record, err)
			}
			if port != 7437 {
				t.Fatalf("default port for %+v = %d, want 7437", record, port)
			}
		}
	})
	t.Run("ENGRAM_PORT is honored without a recorded port", func(t *testing.T) {
		clearRunEnv(t)
		t.Setenv("ENGRAM_PORT", "9100")
		port, err := resolveRestartPort(nil, daemon.PidRecord{})
		if err != nil {
			t.Fatalf("resolve with ENGRAM_PORT: %v", err)
		}
		if port != 9100 {
			t.Fatalf("ENGRAM_PORT port = %d, want 9100", port)
		}
	})
	t.Run("recorded port wins over ENGRAM_PORT", func(t *testing.T) {
		clearRunEnv(t)
		t.Setenv("ENGRAM_PORT", "9100")
		port, err := resolveRestartPort(nil, daemon.PidRecord{Pid: 4242, Port: 1234})
		if err != nil {
			t.Fatalf("resolve with a recorded port: %v", err)
		}
		if port != 1234 {
			t.Fatalf("recorded port = %d, want 1234 over ENGRAM_PORT", port)
		}
	})
	t.Run("explicit -p wins over ENGRAM_PORT", func(t *testing.T) {
		clearRunEnv(t)
		t.Setenv("ENGRAM_PORT", "9100")
		port, err := resolveRestartPort([]string{"-p", "9999"}, daemon.PidRecord{})
		if err != nil {
			t.Fatalf("resolve with -p over ENGRAM_PORT: %v", err)
		}
		if port != 9999 {
			t.Fatalf("explicit port = %d, want 9999 over ENGRAM_PORT", port)
		}
	})
	t.Run("invalid ENGRAM_PORT falls back to 7437", func(t *testing.T) {
		clearRunEnv(t)
		for _, value := range []string{"abc", "0", "70000", " "} {
			t.Setenv("ENGRAM_PORT", value)
			port, err := resolveRestartPort(nil, daemon.PidRecord{})
			if err != nil {
				t.Fatalf("resolve with ENGRAM_PORT %q: %v", value, err)
			}
			if port != 7437 {
				t.Fatalf("invalid ENGRAM_PORT %q port = %d, want the 7437 fallback", value, port)
			}
		}
	})
	t.Run("bad -p values are rejected", func(t *testing.T) {
		for _, args := range [][]string{{"-p"}, {"-p", "abc"}, {"-p", "0"}, {"-p=70000"}} {
			if _, err := resolveRestartPort(args, daemon.PidRecord{Pid: 4242, Port: 1234}); err == nil {
				t.Fatalf("resolve %v accepted a bad port", args)
			}
		}
	})
	t.Run("unknown argument is rejected", func(t *testing.T) {
		if _, err := resolveRestartPort([]string{"bogus"}, daemon.PidRecord{}); err == nil || !strings.Contains(err.Error(), "unknown restart argument: bogus") {
			t.Fatalf("unknown arg error = %v, want unknown restart argument: bogus", err)
		}
	})
}

// swapStopDaemonProcess swaps the signal seam with a recorder so tests can
// prove whether the signal path was (or was not) exercised without
// signaling a real process.
func swapStopDaemonProcess(t *testing.T) *[]int {
	t.Helper()
	signaled := &[]int{}
	saved := stopDaemonProcessNow
	stopDaemonProcessNow = func(pid int) error {
		*signaled = append(*signaled, pid)
		return nil
	}
	t.Cleanup(func() { stopDaemonProcessNow = saved })
	return signaled
}

// TestStopDaemonIdentityGate pins the pid-reuse guard behind `engram stop`
// and `engram restart`: a live pidfile pid is only signaled when the daemon
// identity answers — the recorded start token for token-bearing records, a
// plain running probe for legacy records. Anything else is reported as
// unconfirmed and never signaled.
func TestStopDaemonIdentityGate(t *testing.T) {
	livePid := os.Getpid()
	writeGatePidfile := func(t *testing.T, dataDir, token string) {
		t.Helper()
		if err := daemon.WritePidfile(daemon.PidfilePath(dataDir), daemon.PidRecord{Pid: livePid, Port: 7603, Token: token}); err != nil {
			t.Fatalf("write pidfile: %v", err)
		}
	}
	t.Run("token mismatch never signals", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		writeGatePidfile(t, cfg.DataDir, "ours")
		signaled := swapStopDaemonProcess(t)
		swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: 7603, Token: "foreign"})
		_, err := stopDaemon(cfg)
		if !errors.Is(err, errDaemonUnconfirmed) {
			t.Fatalf("mismatch stop error = %v, want errDaemonUnconfirmed", err)
		}
		if len(*signaled) != 0 {
			t.Fatalf("mismatch signaled pids %v, want none", *signaled)
		}
		if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(cfg.DataDir)); rerr != nil {
			t.Fatalf("mismatch removed the pidfile: %v", rerr)
		}
	})
	t.Run("no answer never signals", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		writeGatePidfile(t, cfg.DataDir, "ours")
		signaled := swapStopDaemonProcess(t)
		swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusNotRunning, Port: 7603})
		_, err := stopDaemon(cfg)
		if !errors.Is(err, errDaemonUnconfirmed) {
			t.Fatalf("no-answer stop error = %v, want errDaemonUnconfirmed", err)
		}
		if len(*signaled) != 0 {
			t.Fatalf("no-answer signaled pids %v, want none", *signaled)
		}
		if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(cfg.DataDir)); rerr != nil {
			t.Fatalf("no-answer removed the pidfile: %v", rerr)
		}
	})
	t.Run("matching token exercises the signal path", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		writeGatePidfile(t, cfg.DataDir, "ours")
		signaled := swapStopDaemonProcess(t)
		swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: 7603, Token: "ours"})
		out := swapStdoutPrinter(t)
		pid, err := stopDaemon(cfg)
		if err != nil {
			t.Fatalf("confirmed stop: %v", err)
		}
		if pid != livePid || len(*signaled) != 1 || (*signaled)[0] != livePid {
			t.Fatalf("confirmed stop pid/signals = %d/%v, want %d once", pid, *signaled, livePid)
		}
		if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(cfg.DataDir)); !errors.Is(rerr, daemon.ErrNoPidfile) {
			t.Fatalf("pidfile survived confirmed stop: %v", rerr)
		}
		if !strings.Contains(out.String(), "Stopped engram daemon") {
			t.Fatalf("confirmed stop output = %q, want Stopped engram daemon", out.String())
		}
	})
	t.Run("legacy record with answering probe still signals", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		writeGatePidfile(t, cfg.DataDir, "")
		signaled := swapStopDaemonProcess(t)
		swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: 7603})
		if _, err := stopDaemon(cfg); err != nil {
			t.Fatalf("legacy confirmed stop: %v", err)
		}
		if len(*signaled) != 1 || (*signaled)[0] != livePid {
			t.Fatalf("legacy stop signaled %v, want pid %d once", *signaled, livePid)
		}
	})
}

// TestStopDaemonPreservesReplacementPidfile pins the stop/restart cleanup
// race behind the revalidation gate: when the pidfile record is replaced
// between the stop decision and the record cleanup (a concurrent `run -d`
// publishing a fresh daemon after the old one exits), the replacement
// record is never removed.
func TestStopDaemonPreservesReplacementPidfile(t *testing.T) {
	cfg := store.Config{DataDir: t.TempDir()}
	if err := daemon.WritePidfile(daemon.PidfilePath(cfg.DataDir), daemon.PidRecord{Pid: os.Getpid(), Port: 7609, Token: "ours"}); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: 7609, Token: "ours"})
	replacement := daemon.PidRecord{Pid: 424242, Port: 7610, Token: "replacement"}
	saved := stopDaemonProcessNow
	stopDaemonProcessNow = func(pid int) error {
		// Simulate the concurrent start publishing its record after the stop
		// decision but before the cleanup runs.
		if err := daemon.WritePidfile(daemon.PidfilePath(cfg.DataDir), replacement); err != nil {
			t.Errorf("write replacement pidfile: %v", err)
		}
		return nil
	}
	t.Cleanup(func() { stopDaemonProcessNow = saved })
	out := swapStdoutPrinter(t)
	if _, err := stopDaemon(cfg); err != nil {
		t.Fatalf("stop with a replacement record: %v", err)
	}
	if !strings.Contains(out.String(), "Stopped engram daemon") {
		t.Fatalf("stop output = %q, want Stopped engram daemon", out.String())
	}
	current, err := daemon.ReadPidfile(daemon.PidfilePath(cfg.DataDir))
	if err != nil {
		t.Fatalf("replacement pidfile was removed by the stop cleanup: %v", err)
	}
	if current != replacement {
		t.Fatalf("pidfile record = %+v, want the replacement %+v", current, replacement)
	}
}

// TestRemovePidfileIfUnchanged pins the revalidation gate behind the stop
// and ps cleanups: only the exact record that was acted on (same pid and
// token) is removed; a changed record and an already-missing pidfile are
// left alone as success.
func TestRemovePidfileIfUnchanged(t *testing.T) {
	path := daemon.PidfilePath(t.TempDir())
	acted := daemon.PidRecord{Pid: 4242, Port: 7437, Token: "ours"}
	if err := daemon.WritePidfile(path, acted); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	for _, changed := range []daemon.PidRecord{
		{Pid: 5353, Port: 7437, Token: "ours"},
		{Pid: 4242, Port: 7437, Token: "theirs"},
	} {
		if err := removePidfileIfUnchanged(path, changed); err != nil {
			t.Fatalf("remove with %+v acted on: %v", changed, err)
		}
		if _, rerr := daemon.ReadPidfile(path); rerr != nil {
			t.Fatalf("changed record %+v was removed: %v", changed, rerr)
		}
	}
	if err := removePidfileIfUnchanged(path, acted); err != nil {
		t.Fatalf("remove the acted-on record: %v", err)
	}
	if _, rerr := daemon.ReadPidfile(path); !errors.Is(rerr, daemon.ErrNoPidfile) {
		t.Fatalf("acted-on record survived: %v", rerr)
	}
	if err := removePidfileIfUnchanged(path, acted); err != nil {
		t.Fatalf("remove an already-missing pidfile: %v", err)
	}
}

// TestStopDaemonProcessRefusesOwnProcess pins the self-kill guard in the
// platform stop path: a pidfile recording the CLI's own pid can never make
// engram signal itself, and stopDaemon keeps such a record (unmanaged)
// instead of removing it.
func TestStopDaemonProcessRefusesOwnProcess(t *testing.T) {
	t.Run("platform stop refuses the own pid", func(t *testing.T) {
		err := stopDaemonProcess(os.Getpid())
		if err == nil || !strings.Contains(err.Error(), "refusing to signal own process") {
			t.Fatalf("own-pid stop error = %v, want refusing to signal own process", err)
		}
	})
	t.Run("stopDaemon keeps the own-pid record unmanaged", func(t *testing.T) {
		cfg := store.Config{DataDir: t.TempDir()}
		if err := daemon.WritePidfile(daemon.PidfilePath(cfg.DataDir), daemon.PidRecord{Pid: os.Getpid(), Port: 7608, Token: "ours"}); err != nil {
			t.Fatalf("write pidfile: %v", err)
		}
		swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: 7608, Token: "ours"})
		swapStdoutPrinter(t)
		// No signal-seam swap: the real platform stop must refuse the own
		// pid before any signaling, and the record must stay tracked.
		_, err := stopDaemon(cfg)
		if err == nil || !strings.Contains(err.Error(), "refusing to signal own process") {
			t.Fatalf("own-pid record stop error = %v, want the own-process refusal", err)
		}
		if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(cfg.DataDir)); rerr != nil {
			t.Fatalf("own-pid record was removed: %v", rerr)
		}
	})
}

// TestCmdPsUnconfirmedDaemonReportsUnreachable pins `engram ps` on a live pid
// whose daemon identity does not answer: ps never vouches for a foreign
// process as the running daemon.
func TestCmdPsUnconfirmedDaemonReportsUnreachable(t *testing.T) {
	cfg := store.Config{DataDir: t.TempDir()}
	if err := daemon.WritePidfile(daemon.PidfilePath(cfg.DataDir), daemon.PidRecord{Pid: os.Getpid(), Port: 7605, Token: "ours"}); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: 7605, Token: "foreign"})
	out := swapStdoutPrinter(t)
	err := cmdPs(cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "daemon is not answering on port 7605") {
		t.Fatalf("unconfirmed ps error = %v, want not answering", err)
	}
	want := fmt.Sprintf("unreachable (pid %d) on port 7605", os.Getpid())
	if !strings.Contains(out.String(), want) {
		t.Fatalf("unconfirmed ps output = %q, want %q", out.String(), want)
	}
}

// TestCmdRestartRefusesSocketBeforeStop pins that `engram restart` rejects
// ENGRAM_SOCKET before stopping anything: a configuration error must never
// take a healthy daemon down (mirrors the `run -d` guard in parseRunArgs).
func TestCmdRestartRefusesSocketBeforeStop(t *testing.T) {
	clearRunEnv(t)
	t.Setenv("ENGRAM_SOCKET", "/tmp/engram.sock")
	cfg := store.Config{DataDir: t.TempDir()}
	if err := daemon.WritePidfile(daemon.PidfilePath(cfg.DataDir), daemon.PidRecord{Pid: os.Getpid(), Port: 7604, Token: "ours"}); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	signaled := swapStopDaemonProcess(t)
	swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: 7604, Token: "ours"})
	err := cmdRestart(cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "unset ENGRAM_SOCKET") {
		t.Fatalf("restart with socket error = %v, want unset ENGRAM_SOCKET guidance", err)
	}
	if len(*signaled) != 0 {
		t.Fatalf("restart with socket stopped the daemon first: signaled %v", *signaled)
	}
	if _, rerr := daemon.ReadPidfile(daemon.PidfilePath(cfg.DataDir)); rerr != nil {
		t.Fatalf("restart with socket removed the pidfile: %v", rerr)
	}
}

// ─── Integration tests (real detached daemon child) ─────────────────────────

// TestDaemonVerbLifecycle exercises the full docker-style loop (specs S4 and
// S5) against a real detached daemon: run -d, ps, stop, ps again.
func TestDaemonVerbLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	clearRunEnv(t)
	port := freeRunPort(t)
	cfg, dataDir := newRunTestConfig(t)
	swapRunDaemonSeams(t, port)
	cleanupPidfileDaemon(t, dataDir)
	out := swapStdoutPrinter(t)

	if err := cmdRun(cfg, []string{"-d", "-p", strconv.Itoa(port)}); err != nil {
		t.Fatalf("detached start: %v", err)
	}
	// S4: ps reports the live daemon with pid and port.
	out.Reset()
	if err := cmdPs(cfg, nil); err != nil {
		t.Fatalf("ps on live daemon: %v", err)
	}
	if !strings.Contains(out.String(), "running (pid") {
		t.Fatalf("ps output = %q, want running (pid ...)", out.String())
	}
	// S5: stop terminates it and clears the pidfile.
	out.Reset()
	if err := cmdStop(cfg, nil); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !strings.Contains(out.String(), "Stopped engram daemon") {
		t.Fatalf("stop output = %q, want Stopped engram daemon", out.String())
	}
	if _, err := daemon.ReadPidfile(daemon.PidfilePath(dataDir)); !errors.Is(err, daemon.ErrNoPidfile) {
		t.Fatalf("pidfile survived stop: %v", err)
	}
	// After the stop, ps reports not running as an error again.
	out.Reset()
	err := cmdPs(cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "daemon is not running") {
		t.Fatalf("post-stop ps error = %v, want daemon is not running", err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Fatalf("post-stop ps output = %q, want not running", out.String())
	}
}

// TestCmdRestartLifecycle pins `engram restart` (spec S8): a live daemon is
// stopped and replaced by a fresh one serving the same port.
func TestCmdRestartLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	clearRunEnv(t)
	port := freeRunPort(t)
	cfg, dataDir := newRunTestConfig(t)
	swapRunDaemonSeams(t, port)
	cleanupPidfileDaemon(t, dataDir)

	if err := cmdRun(cfg, []string{"-d", "-p", strconv.Itoa(port)}); err != nil {
		t.Fatalf("initial detached start: %v", err)
	}
	first, err := daemon.ReadPidfile(daemon.PidfilePath(dataDir))
	if err != nil {
		t.Fatalf("read pidfile after start: %v", err)
	}
	// The stop half goes through the status seam and the start half prints
	// directly to stdout, so capture the real stdout around the restart.
	var restartErr error
	output := captureStdoutPipe(t, func() { restartErr = cmdRestart(cfg, nil) })
	if restartErr != nil {
		t.Fatalf("restart: %v", restartErr)
	}
	if !strings.Contains(output, "Stopped engram daemon") {
		t.Fatalf("restart output = %q, want the stop half to report Stopped engram daemon", output)
	}
	if !strings.Contains(output, "Started engram daemon") {
		t.Fatalf("restart output = %q, want the start half to report Started engram daemon", output)
	}
	second, err := daemon.ReadPidfile(daemon.PidfilePath(dataDir))
	if err != nil {
		t.Fatalf("read pidfile after restart: %v", err)
	}
	if second.Pid == first.Pid {
		t.Fatalf("restart reused pid %d; want a fresh daemon process", first.Pid)
	}
	if second.Port != port {
		t.Fatalf("restarted daemon port = %d, want %d", second.Port, port)
	}
	if !daemon.ProcessAlive(second.Pid) {
		t.Fatalf("restarted daemon pid %d is not alive", second.Pid)
	}
	if err := cmdPs(cfg, nil); err != nil {
		t.Fatalf("ps after restart: %v", err)
	}
}

// TestCmdRestartProceedsWhenStopUnconfirmed pins restart's stale-record
// behavior: a live pid that is not confirmed as the daemon is never
// signaled; restart tolerates the unconfirmed stop and starts fresh through
// runDetached's admission, which clears the unmanaged pidfile.
func TestCmdRestartProceedsWhenStopUnconfirmed(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	clearRunEnv(t)
	port := freeRunPort(t)
	cfg, dataDir := newRunTestConfig(t)
	swapRunDaemonSeams(t, port)
	cleanupPidfileDaemon(t, dataDir)
	if err := daemon.WritePidfile(daemon.PidfilePath(dataDir), daemon.PidRecord{Pid: os.Getpid(), Port: port, Token: "stale"}); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	signaled := swapStopDaemonProcess(t)
	swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: port, Token: "foreign"})

	var restartErr error
	output := captureStdoutPipe(t, func() { restartErr = cmdRestart(cfg, nil) })
	if restartErr != nil {
		t.Fatalf("restart over unconfirmed pid: %v", restartErr)
	}
	if len(*signaled) != 0 {
		t.Fatalf("restart signaled the unconfirmed pid: %v", *signaled)
	}
	if !strings.Contains(output, "Started engram daemon") {
		t.Fatalf("restart output = %q, want Started engram daemon", output)
	}
	record, err := daemon.ReadPidfile(daemon.PidfilePath(dataDir))
	if err != nil {
		t.Fatalf("read pidfile after restart: %v", err)
	}
	if record.Pid == os.Getpid() || !daemon.ProcessAlive(record.Pid) {
		t.Fatalf("restarted daemon record = %+v, want a fresh live daemon pid", record)
	}
}

// TestCmdLogsPrintsDaemonLog pins `engram logs` against the real daemon log
// sink (spec S7): the detached child's startup announcement is replayed.
func TestCmdLogsPrintsDaemonLog(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	clearRunEnv(t)
	port := freeRunPort(t)
	cfg, dataDir := newRunTestConfig(t)
	swapRunDaemonSeams(t, port)
	cleanupPidfileDaemon(t, dataDir)

	if err := cmdRun(cfg, []string{"-d", "-p", strconv.Itoa(port)}); err != nil {
		t.Fatalf("detached start: %v", err)
	}
	// The readiness handshake can complete just before the startup log line
	// is flushed, so poll the sink until the listener announcement lands.
	wantLine := "HTTP server listening on"
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(daemon.LogPath(dataDir))
		if err == nil && strings.Contains(string(data), wantLine) {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("daemon log never contained %q (read error: %v)", wantLine, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var logsErr error
	output := captureStdoutPipe(t, func() { logsErr = cmdLogs(cfg, nil) })
	if logsErr != nil {
		t.Fatalf("logs: %v", logsErr)
	}
	if !strings.Contains(output, wantLine) {
		t.Fatalf("logs output = %q, want it to contain %q", output, wantLine)
	}
	if err := cmdStop(cfg, nil); err != nil {
		t.Fatalf("stop after logs: %v", err)
	}
}
