package main

import (
	"context"
	"fmt"

	"github.com/Gentleman-Programming/engram/internal/cloudconfig"
)

// daemonProbeStatus describes the outcome of probing the local engram daemon.
type daemonProbeStatus = cloudconfig.ProbeStatus

const (
	daemonProbeRunning     = cloudconfig.ProbeRunning
	daemonProbeNotRunning  = cloudconfig.ProbeNotRunning
	daemonProbeUnreachable = cloudconfig.ProbeUnreachable
)

// daemonProbeResult captures the outcome of a single probe.
type daemonProbeResult = cloudconfig.Result

const defaultDaemonProbePort = cloudconfig.DefaultProbePort

// cloudDaemonProbe issues a short timeout GET to /health on the local engram
// HTTP server. Exposed as a variable so tests can stub it.
var cloudDaemonProbe = defaultCloudDaemonProbe

// defaultCloudDaemonProbe performs a real HTTP GET against the local daemon.
// A dial error to 127.0.0.1 is interpreted as "not running"; any other error
// (timeout, non-2xx response, malformed reply) maps to "unreachable" so the
// user can distinguish "the daemon is gone" from "the daemon is misbehaving".
func defaultCloudDaemonProbe(ctx context.Context, port int) daemonProbeResult {
	return cloudconfig.LocalDaemonProbe(ctx, port)
}

// resolveDaemonProbePort mirrors the port resolution used by cmdServe so the
// probe targets the same address the user's serve process is bound to.
func resolveDaemonProbePort() int {
	return cloudconfig.ResolvePort()
}

// printCloudStatusDaemonProbe prints a single line describing whether the
// local engram daemon answers /health, plus a short hint when it is down.
// Exit code is unchanged: this is informational so cloud status remains a
// non-failing diagnostic surface.
func printCloudStatusDaemonProbe() {
	port := resolveDaemonProbePort()
	ctx, cancel := context.WithTimeout(context.Background(), cloudconfig.ProbeTimeout)
	defer cancel()
	res := cloudDaemonProbe(ctx, port)
	switch res.Status {
	case daemonProbeRunning:
		fmt.Printf("Local daemon: running on port %d\n", res.Port)
	case daemonProbeNotRunning:
		fmt.Printf("Local daemon: not running on port %d\n", res.Port)
		fmt.Println("Hint: run `engram serve` to resume autosync; on macOS see DOCS.md launchd template to keep it alive across upgrades")
	default:
		if res.Err != nil {
			fmt.Printf("Local daemon: unreachable on port %d (probe error: %v)\n", res.Port, res.Err)
		} else {
			fmt.Printf("Local daemon: unreachable on port %d\n", res.Port)
		}
	}
}
