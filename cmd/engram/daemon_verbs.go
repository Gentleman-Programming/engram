package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// The docker-style daemon control verbs (issue #2228): `engram ps`, `stop`,
// `logs [-f]`, and `restart`. They manage ONLY the pidfile-tracked daemon
// started by `engram run -d` — never a session-owned MCP serve and never any
// process beyond the pidfile pid. No port scanning, no kill-by-name.

const (
	// stopTermGrace bounds the wait after SIGTERM before escalating to a
	// forced kill (docker stop's grace-period analog).
	stopTermGrace = 10 * time.Second
	// stopKillGrace bounds the wait after the forced kill.
	stopKillGrace = 5 * time.Second
	// stopPollInterval is the daemon-exit poll cadence.
	stopPollInterval = 100 * time.Millisecond
	// logsFollowInterval is the `engram logs -f` append-poll cadence.
	logsFollowInterval = 100 * time.Millisecond
)

// stdoutPrinter is the output seam for the daemon verbs' status lines:
// fmt.Printf by default, swapped in tests.
var stdoutPrinter = fmt.Printf

// daemonStatusProbe is the health-probe seam behind `engram ps`: a var so
// tests can stub the probe without a live daemon, mirroring the
// cloudDaemonProbe seam shape. Default wraps internal/daemon.Probe with the
// shared probe timeout.
var daemonStatusProbe = defaultDaemonStatusProbe

// defaultDaemonStatusProbe probes the daemon's /health endpoint with the
// CLI-wide daemon probe timeout.
func defaultDaemonStatusProbe(port int) daemon.ProbeResult {
	ctx, cancel := context.WithTimeout(context.Background(), daemonProbeTimeout)
	defer cancel()
	return daemon.Probe(ctx, port, daemonProbeTimeout)
}

// cmdPs implements `engram ps` (spec S4): report whether the pidfile-tracked
// daemon is up, docker `ps` style. A missing or stale pidfile, or a recorded
// process that no longer answers, all surface as an error after printing the
// status line.
func cmdPs(cfg store.Config, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown ps argument: %s", args[0])
	}
	pidPath := daemon.PidfilePath(cfg.DataDir)
	record, err := daemon.ReadPidfile(pidPath)
	switch {
	case errors.Is(err, daemon.ErrNoPidfile):
		stdoutPrinter("not running\n")
		return fmt.Errorf("daemon is not running")
	case err != nil:
		return err
	}
	if !daemon.ProcessAlive(record.Pid) {
		stdoutPrinter("not running\n")
		if err := daemon.RemovePidfile(pidPath); err != nil {
			return err
		}
		return fmt.Errorf("daemon is not running")
	}
	if res := daemonStatusProbe(record.Port); res.Status == daemon.StatusRunning {
		stdoutPrinter("running (pid %d) on port %d\n", record.Pid, record.Port)
		return nil
	}
	stdoutPrinter("unreachable (pid %d) on port %d\n", record.Pid, record.Port)
	return fmt.Errorf("daemon is not answering on port %d", record.Port)
}

// cmdStop implements `engram stop` (spec S5): stop the pidfile-tracked
// daemon gently, then force-kill it if needed.
func cmdStop(cfg store.Config, args []string) error {
	_, err := stopDaemon(cfg)
	return err
}

// stopDaemon is the shared stop logic behind `engram stop` and
// `engram restart`: a missing pidfile is nothing to stop, a stale one is
// cleaned up, and a live daemon is terminated via stopDaemonProcess before
// the pidfile is removed. It returns the pid from the pidfile record (0 only
// when the pidfile could not be read at all).
func stopDaemon(cfg store.Config) (int, error) {
	pidPath := daemon.PidfilePath(cfg.DataDir)
	record, err := daemon.ReadPidfile(pidPath)
	if errors.Is(err, daemon.ErrNoPidfile) {
		return 0, fmt.Errorf("no daemon pidfile; nothing to stop")
	}
	if err != nil {
		return 0, err
	}
	if !daemon.ProcessAlive(record.Pid) {
		if err := daemon.RemovePidfile(pidPath); err != nil {
			return record.Pid, err
		}
		stdoutPrinter("Removed stale pidfile (pid %d was not running)\n", record.Pid)
		return record.Pid, nil
	}
	if err := stopDaemonProcess(record.Pid); err != nil {
		// Keep the pidfile: the daemon may still be alive.
		return record.Pid, err
	}
	if err := daemon.RemovePidfile(pidPath); err != nil {
		return record.Pid, err
	}
	stdoutPrinter("Stopped engram daemon (pid %d)\n", record.Pid)
	return record.Pid, nil
}

// cmdLogs implements `engram logs [-f|--follow]` (spec S7): replay the
// daemon log sink to stdout, optionally following appends like
// `docker logs -f`.
func cmdLogs(cfg store.Config, args []string) error {
	follow := false
	for _, arg := range args {
		switch arg {
		case "-f", "--follow":
			follow = true
		default:
			return fmt.Errorf("unknown logs argument: %s", arg)
		}
	}
	path := daemon.LogPath(cfg.DataDir)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no daemon log yet at %s", path)
		}
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := io.Copy(os.Stdout, file); err != nil {
		return fmt.Errorf("replay daemon log: %w", err)
	}
	if !follow {
		return nil
	}
	return followLogs(file)
}

// followLogs keeps printing daemon log appends until the CLI is interrupted
// (docker `logs -f` semantics; Ctrl+C terminates the CLI). The file stays
// open and each Read resumes at the offset the previous pass left behind.
// Rotation is not tracked: the sink is daemon-owned and only ever appended.
func followLogs(file *os.File) error {
	chunk := make([]byte, 4096)
	for {
		time.Sleep(logsFollowInterval)
		n, err := file.Read(chunk)
		if n > 0 {
			if _, werr := os.Stdout.Write(chunk[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				continue
			}
			return err
		}
	}
}

// cmdRestart implements `engram restart` (spec S8): stop a live pidfile
// daemon through the same logic as `engram stop`, then start a fresh one.
// With a stale or missing pidfile it just starts, matching docker restart
// semantics on a stopped container. Auto-restart across reboots stays with
// launchd/systemd.
func cmdRestart(cfg store.Config, args []string) error {
	record, readErr := daemon.ReadPidfile(daemon.PidfilePath(cfg.DataDir))
	// Resolve the port before stopping anything so a typo can never stop a
	// healthy daemon: -p wins, then the stopped record's port, then 7437.
	port, err := resolveRestartPort(args, record)
	if err != nil {
		return err
	}
	if readErr == nil && daemon.ProcessAlive(record.Pid) {
		if _, err := stopDaemon(cfg); err != nil {
			return fmt.Errorf("stop daemon for restart: %w", err)
		}
	}
	// A stale pidfile left here is cleared by assertDaemonStartable inside
	// runDetached.
	return runDetached(cfg, serveOptions{port: port})
}

// resolveRestartPort parses `engram restart [-p PORT]` and resolves the port
// for the new daemon: an explicit -p wins, then the stopped daemon's
// recorded port when usable, then the 7437 default. Port parsing follows the
// `engram run -p` rules (both spellings, 1..65535).
func resolveRestartPort(args []string, record daemon.PidRecord) (int, error) {
	port := 0
	explicit := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-p" || strings.HasPrefix(arg, "-p="):
			value := ""
			if arg == "-p" {
				if i+1 >= len(args) {
					return 0, fmt.Errorf("-p requires a port")
				}
				i++
				value = args[i]
			} else {
				value = strings.TrimPrefix(arg, "-p=")
			}
			n, err := strconv.ParseUint(value, 10, 16)
			if err != nil || n == 0 {
				return 0, fmt.Errorf("-p requires a port between 1 and 65535, got %q", value)
			}
			port = int(n)
			explicit = true
		default:
			return 0, fmt.Errorf("unknown restart argument: %s", arg)
		}
	}
	if explicit {
		return port, nil
	}
	if record.Port > 0 {
		return record.Port, nil
	}
	return 7437, nil
}
