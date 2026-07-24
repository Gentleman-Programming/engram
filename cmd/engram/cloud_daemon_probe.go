package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/cloudconfig"
)

// cloudDaemonProbe issues a short timeout GET to /health on the
// local engram HTTP server. It is a thin wrapper around
// cloudconfig.LocalDaemonProbe so cmdCloudStatus can stub the
// probe in tests without importing the cloudconfig package's
// var seam directly. The wrapper preserves the existing
// `func(context.Context, int) cloudconfig.Result` signature
// used by callers; future tests should stub the cloudconfig
// package's LocalDaemonProbe var instead of this local
// variable (T-608.15 will rewrite the print-test to use the
// new seam).
var cloudDaemonProbe = cloudconfig.LocalDaemonProbe

// resolveDaemonProbePort mirrors the port resolution used by
// cmdServe so the probe targets the same address the user's
// serve process is bound to. The function delegates to
// cloudconfig.ResolvePort (added in T-608.4) but is kept here
// as a thin wrapper to preserve the existing call site in
// printCloudStatusDaemonProbe. The default port (7437) and
// the env var name ("ENGRAM_PORT") are exported from the
// cloudconfig package as DefaultProbePort and EnvProbePort.
func resolveDaemonProbePort() int {
	if p := strings.TrimSpace(os.Getenv(cloudconfig.EnvProbePort)); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	return cloudconfig.DefaultProbePort
}

// printCloudStatusDaemonProbe prints a single line describing
// whether the local engram daemon answers /health, plus a
// short hint when it is down. Exit code is unchanged: this is
// informational so cloud status remains a non-failing
// diagnostic surface.
//
// The function uses cloudconfig.ProbeStatus to classify the
// result; the labels rendered to stdout ("running", "not
// running", "unreachable") match ProbeStatus.String() so the
// output is byte-identical to the pre-migration behavior. The
// per-state hint lines (recovery hint for ProbeNotRunning,
// probe-error message for ProbeUnreachable) are also preserved.
func printCloudStatusDaemonProbe() {
	port := resolveDaemonProbePort()
	ctx, cancel := context.WithTimeout(context.Background(), cloudconfig.ProbeTimeout)
	defer cancel()
	res := cloudDaemonProbe(ctx, port)
	switch res.Status {
	case cloudconfig.ProbeRunning:
		fmt.Printf("Local daemon: running on port %d\n", res.Port)
	case cloudconfig.ProbeNotRunning:
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
