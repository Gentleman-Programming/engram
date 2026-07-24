package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/internal/cloudconfig"
)

// cloudDaemonProbe is a thin wrapper that calls
// cloudconfig.LocalDaemonProbe at invocation time so test
// stubs of the cloudconfig package's var seam (per ADR-1)
// reach printCloudStatusDaemonProbe without needing a
// separate local var seam. The wrapper is a function (not
// a var assigned at package init) so a later assignment to
// cloudconfig.LocalDaemonProbe is observed by the next call
// to printCloudStatusDaemonProbe.
func cloudDaemonProbe(ctx context.Context, port int) cloudconfig.Result {
	return cloudconfig.LocalDaemonProbe(ctx, port)
}

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
