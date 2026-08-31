package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// daemonProbeStatus describes the outcome of probing the local engram daemon.
type daemonProbeStatus string

const (
	daemonProbeRunning     daemonProbeStatus = "running"
	daemonProbeNotRunning  daemonProbeStatus = "not_running"
	daemonProbeUnreachable daemonProbeStatus = "unreachable"
)

// daemonProbeResult captures the outcome of a single probe.
type daemonProbeResult struct {
	Status daemonProbeStatus
	Port   int
	Err    error
}

const defaultDaemonProbePort = 7437

// daemonProbeTimeout is a var (not const) so tests can shorten it when
// exercising the "server accepts but never replies" path.
var daemonProbeTimeout = time.Second

// cloudDaemonProbe issues a short timeout GET to /health on the local engram
// HTTP server. Exposed as a variable so tests can stub it.
var cloudDaemonProbe = defaultCloudDaemonProbe

// defaultCloudDaemonProbe performs a real HTTP GET against the local daemon.
// A dial error to 127.0.0.1 is interpreted as "not running"; any other error
// (timeout, non-2xx response, malformed reply) maps to "unreachable" so the
// user can distinguish "the daemon is gone" from "the daemon is misbehaving".
func defaultCloudDaemonProbe(ctx context.Context, port int) daemonProbeResult {
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	client := &http.Client{Timeout: daemonProbeTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return daemonProbeResult{Status: daemonProbeUnreachable, Port: port, Err: err}
	}
	resp, err := client.Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return daemonProbeResult{Status: daemonProbeNotRunning, Port: port, Err: err}
		}
		return daemonProbeResult{Status: daemonProbeUnreachable, Port: port, Err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return daemonProbeResult{Status: daemonProbeRunning, Port: port}
	}
	return daemonProbeResult{Status: daemonProbeUnreachable, Port: port}
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
		fmt.Println("Hint: run `engram serve` to resume autosync; on macOS see DOCS.md launchd template to keep it alive across upgrades")
	default:
		if res.Err != nil {
			fmt.Printf("Local daemon: unreachable on port %d (probe error: %v)\n", res.Port, res.Err)
		} else {
			fmt.Printf("Local daemon: unreachable on port %d\n", res.Port)
		}
	}
}
