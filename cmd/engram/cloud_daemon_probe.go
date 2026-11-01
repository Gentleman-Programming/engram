package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
)

const (
	daemonProbeRunning     = daemon.StatusRunning
	daemonProbeNotRunning  = daemon.StatusNotRunning
	daemonProbeUnreachable = daemon.StatusUnreachable
)

// daemonProbeResult captures the outcome of a single probe.
type daemonProbeResult = daemon.ProbeResult

const defaultDaemonProbePort = 7437

// daemonProbeTimeout is a var (not const) so tests can shorten it when
// exercising the "server accepts but never replies" path.
var daemonProbeTimeout = time.Second

// cloudDaemonProbe issues a short timeout GET to /health on the local engram
// HTTP server. Exposed as a variable so tests can stub it.
var cloudDaemonProbe = defaultCloudDaemonProbe

// defaultCloudDaemonProbe performs a real HTTP GET against the local daemon
// through the shared implementation in internal/daemon, which also backs the
// daemon control verbs (`engram ps`).
func defaultCloudDaemonProbe(ctx context.Context, port int) daemonProbeResult {
	return daemon.Probe(ctx, port, daemonProbeTimeout)
}

// resolveDaemonProbePort mirrors the port resolution used by cmdServe so the
// probe targets the same address the user's serve process is bound to.
func resolveDaemonProbePort() int {
	if p := strings.TrimSpace(os.Getenv("ENGRAM_PORT")); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	return defaultDaemonProbePort
}

// printCloudStatusDaemonProbe prints a single line describing whether the
// local engram daemon answers /health, plus a short hint when it is down.
// Exit code is unchanged: this is informational so cloud status remains a
// non-failing diagnostic surface.
func printCloudStatusDaemonProbe() {
	port := resolveDaemonProbePort()
	ctx, cancel := context.WithTimeout(context.Background(), daemonProbeTimeout)
	defer cancel()
	res := cloudDaemonProbe(ctx, port)
	switch res.Status {
	case daemonProbeRunning:
		fmt.Printf("Local daemon: running on port %d\n", res.Port)
	case daemonProbeNotRunning:
		fmt.Printf("Local daemon: not running on port %d\n", res.Port)
		fmt.Println("Hint: run `engram run -d` to start the daemon (or `engram serve` in the foreground); on macOS see DOCS.md launchd template to keep it alive across upgrades")
	default:
		if res.Err != nil {
			fmt.Printf("Local daemon: unreachable on port %d (probe error: %v)\n", res.Port, res.Err)
		} else {
			fmt.Printf("Local daemon: unreachable on port %d\n", res.Port)
		}
	}
}
