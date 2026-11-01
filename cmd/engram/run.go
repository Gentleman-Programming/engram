package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// runReadyName is the readiness handshake file the detached child writes
// under the data dir after its HTTP listener binds.
const runReadyName = ".serve-ready.json"

// runReadyTimeout bounds how long `engram run -d` waits for the detached child
// to bind and report readiness before surfacing the child's log tail.
var runReadyTimeout = 15 * time.Second

// runReadyPollInterval is the readiness file poll cadence.
var runReadyPollInterval = 100 * time.Millisecond

// runStartLockWait bounds how long a second lifecycle command on the same
// data directory — another `engram run -d` start, or the `engram stop`/`ps`
// cleanup racing it — waits for the per-dir start lock before failing
// cleanly with a held-lock error.
var runStartLockWait = 20 * time.Second

// runOptions are the parsed arguments of `engram run` (docker-style verbs, issue #2228).
type runOptions struct {
	detach       bool
	port         int
	portExplicit bool
}

// parseRunArgs parses `engram run [-d] [-p PORT]`. Port precedence mirrors
// serve: -p beats ENGRAM_PORT beats the 7437 default.
func parseRunArgs(args []string) (runOptions, error) {
	options := runOptions{port: 7437}
	if p := strings.TrimSpace(os.Getenv("ENGRAM_PORT")); p != "" {
		if n, err := strconv.ParseUint(p, 10, 16); err == nil && n > 0 {
			options.port = int(n)
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-d" || arg == "--detach":
			options.detach = true
		case arg == "-p" || strings.HasPrefix(arg, "-p="):
			value := ""
			if arg == "-p" {
				if i+1 >= len(args) {
					return runOptions{}, fmt.Errorf("-p requires a port")
				}
				i++
				value = args[i]
			} else {
				value = strings.TrimPrefix(arg, "-p=")
			}
			n, err := strconv.ParseUint(value, 10, 16)
			if err != nil || n == 0 {
				return runOptions{}, fmt.Errorf("-p requires a port between 1 and 65535, got %q", value)
			}
			options.port = int(n)
			options.portExplicit = true
		default:
			return runOptions{}, fmt.Errorf("unknown run argument: %s", arg)
		}
	}
	socket := strings.TrimSpace(os.Getenv("ENGRAM_SOCKET"))
	if socket != "" {
		if options.detach {
			return runOptions{}, fmt.Errorf("run -d manages a TCP daemon; unset ENGRAM_SOCKET")
		}
		if options.portExplicit {
			return runOptions{}, fmt.Errorf("socket transport cannot be combined with an explicit TCP port")
		}
	}
	return options, nil
}

// cmdRun implements `engram run`: without -d it serves in the foreground
// exactly like `engram serve`; with -d it starts the detached daemon.
func cmdRun(cfg store.Config, args []string) error {
	options, err := parseRunArgs(args)
	if err != nil {
		return err
	}
	serve := serveOptions{port: options.port, socketPath: strings.TrimSpace(os.Getenv("ENGRAM_SOCKET"))}
	if !options.detach {
		return serveWith(cfg, serve)
	}
	return runDetached(cfg, serve)
}

// assertDaemonStartable rejects a second daemon start while a live one holds
// the pidfile, mirroring docker's refusal to double-start one container. A
// live pid only blocks the start when the daemon identity answers; a record
// answered as not-running or as a foreign daemon is stale and cleared
// instead of blocking. A record whose port is bound but does not answer
// /health (StatusUnreachable) is preserved and the start rejected: the
// daemon may be alive and merely not answering, and an unreachable probe is
// not evidence the record is stale.
func assertDaemonStartable(dataDir string, port int) error {
	pidPath := daemon.PidfilePath(dataDir)
	record, err := daemon.ReadPidfile(pidPath)
	if errors.Is(err, daemon.ErrNoPidfile) {
		return nil
	}
	if err != nil {
		return err
	}
	if !daemon.ProcessAlive(record.Pid) {
		// Stale pidfile from a dead daemon: clear it and start fresh.
		return daemon.RemovePidfile(pidPath)
	}
	probePort := record.Port
	if probePort == 0 {
		probePort = port
	}
	res := daemonStatusProbe(probePort)
	if daemonIdentityConfirmed(record, res) {
		return fmt.Errorf("already running (pid %d) on port %d; use `engram stop` before starting again", record.Pid, probePort)
	}
	if res.Status == daemon.StatusUnreachable {
		// The port is bound but /health did not answer with the daemon
		// identity: the daemon may be alive and merely unhealthy (a busy
		// store, a timed-out probe). That is not evidence the record is
		// stale, so preserve it and reject the start — clearing it could
		// unmanage a live daemon. `engram stop` cannot recover this state
		// either (it refuses to signal an unconfirmed pid), so the error
		// names the manual resolution instead of pointing at it.
		return fmt.Errorf("port %d is bound but /health did not confirm the daemon (pid %d may still be alive); record kept at %s; retry when it answers, or verify pid %d and stop it manually, or remove %s if the record is stale", probePort, record.Pid, pidPath, record.Pid, pidPath)
	}
	// The pid is alive but the daemon identity does not answer: nothing owns
	// the recorded port (not running) or a foreign daemon answered the
	// identity probe (token mismatch). The record is stale — the same rule
	// `engram stop` applies before any signal — so clear it and start fresh
	// rather than reporting a phantom "already running".
	_, _ = stdoutPrinter("Clearing unmanaged pidfile (pid %d not confirmed as the daemon on port %d)\n", record.Pid, probePort)
	return daemon.RemovePidfile(pidPath)
}

// runDetached spawns the daemon child, waits for its readiness handshake, and
// prints the daemon pid and port (docker `run -d` semantics).
func runDetached(cfg store.Config, serve serveOptions) error {
	dataDir := cfg.DataDir
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	// Serialize concurrent starts per data directory: two `run -d` parents
	// could otherwise both pass admission and overwrite each other's pidfile
	// and readiness records. The lock spans admission, readiness publication,
	// and failed-start cleanup; it releases at return, and the daemon itself
	// outlives it without holding anything.
	startLock, err := daemon.AcquireStartLock(dataDir, runStartLockWait)
	if err != nil {
		return err
	}
	defer func() { _ = startLock.Release() }()
	if err := assertDaemonStartable(dataDir, serve.port); err != nil {
		return err
	}
	token, err := runToken()
	if err != nil {
		return err
	}
	readyPath := filepath.Join(dataDir, runReadyName)
	if err := os.Remove(readyPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear stale readiness file: %w", err)
	}
	logPath := daemon.LogPath(dataDir)
	logFile, err := daemon.OpenLog(logPath)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	childPid, err := spawnDaemon(executable, spawnDaemonArgs(serve.port), spawnDaemonEnv(readyPath, token), logFile)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(runReadyTimeout)
	for time.Now().Before(deadline) {
		report, err := readRunReadiness(readyPath, token)
		if err == nil {
			_ = os.Remove(readyPath)
			fmt.Printf("Started engram daemon: pid %d on port %d\n", report.PID, report.Port)
			return nil
		}
		if childExited(childPid) || !daemon.ProcessAlive(childPid) {
			break
		}
		time.Sleep(runReadyPollInterval)
	}
	// The start failed: own the aftermath (terminate and reap the child, then
	// remove only this start's records) so a timed-out child can never
	// survive as an orphan that later publishes itself as the daemon.
	cleanupFailedStart(childPid, dataDir, readyPath, token)
	return fmt.Errorf("daemon did not start; recent log lines from %s:\n%s", logPath, tailFile(logPath, 15))
}

// spawnDaemonArgs builds the detached child command line: the child is a
// plain `engram serve <port>` that honors the readiness env markers.
var spawnDaemonArgs = func(port int) []string {
	return []string{"serve", strconv.Itoa(port)}
}

// spawnDaemonEnv extends the environment with the readiness handshake markers.
var spawnDaemonEnv = func(readyPath, token string) []string {
	return append(os.Environ(),
		"ENGRAM_RUN_READY_FILE="+readyPath,
		"ENGRAM_RUN_READY_TOKEN="+token,
	)
}

// runReadiness is the handshake record the daemon child writes after binding.
type runReadiness struct {
	Token string `json:"token"`
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
}

// runToken returns a random hex token tying the readiness file to this start,
// so a stale record from a previous run can never satisfy the handshake.
func runToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate readiness token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// readRunReadiness returns the handshake record only when it carries this
// start's token; anything else counts as "not ready yet".
func readRunReadiness(path, token string) (runReadiness, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return runReadiness{}, err
	}
	var report runReadiness
	if err := json.Unmarshal(data, &report); err != nil {
		return runReadiness{}, err
	}
	if report.Token != token || report.PID <= 0 || report.Port <= 0 {
		return runReadiness{}, fmt.Errorf("readiness record does not match this start")
	}
	return report, nil
}

// writeRunReadiness publishes the daemon pidfile and the parent handshake
// record once the HTTP listener is bound. It runs inside the daemon child as
// the server onReady hook; an error aborts serving so a half-started daemon
// never reports success.
func writeRunReadiness(dataDir, readyPath, token string, addr net.Addr) error {
	port := 0
	if tcp, ok := addr.(*net.TCPAddr); ok {
		port = tcp.Port
	}
	if port <= 0 {
		return fmt.Errorf("daemon listener is not TCP: %s", addr)
	}
	if err := daemon.WritePidfile(daemon.PidfilePath(dataDir), daemon.PidRecord{Pid: os.Getpid(), Port: port, Token: token}); err != nil {
		return err
	}
	data, err := json.Marshal(runReadiness{Token: token, PID: os.Getpid(), Port: port})
	if err != nil {
		return err
	}
	if err := os.WriteFile(readyPath, data, 0o600); err != nil {
		return fmt.Errorf("write readiness record: %w", err)
	}
	return nil
}

// cleanupFailedStart owns the aftermath of a failed `engram run -d`: the
// spawned child is terminated and reaped so a timed-out start can never
// leave an orphan that later publishes itself as the daemon (a store
// migration can legitimately outlast the readiness deadline), and this
// start's records are removed. The pidfile is only removed when it carries
// this start's token, so a record owned by another start is never deleted.
func cleanupFailedStart(childPid int, dataDir, readyPath, token string) {
	if childPid > 0 && !childExited(childPid) && daemon.ProcessAlive(childPid) {
		// Graceful-then-force termination, the same lever `engram stop`
		// uses; on unix awaitDaemonExit also reaps the child.
		_ = stopDaemonProcess(childPid)
	}
	if _, err := readRunReadiness(readyPath, token); err == nil {
		// The readiness record answers to this start's token: ours to remove.
		_ = os.Remove(readyPath)
	}
	pidPath := daemon.PidfilePath(dataDir)
	if record, err := daemon.ReadPidfile(pidPath); err == nil && token != "" && record.Token == token {
		_ = daemon.RemovePidfile(pidPath)
	}
}

// tailFile returns the last n lines of a file for failure diagnostics.
func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(no log available: %v)", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
