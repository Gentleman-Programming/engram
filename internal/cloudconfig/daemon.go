package cloudconfig

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

// DefaultProbePort is the local port the probe targets when
// ENGRAM_PORT is unset or carries an invalid value. Matches the
// default used by cmd/engram's serve command.
const DefaultProbePort = 7437

// EnvProbePort is the environment variable consulted by
// ResolvePort. Defining it as a constant keeps the contract
// discoverable and prevents typos at call sites; the CLI and the
// TUI both read the same variable.
const EnvProbePort = "ENGRAM_PORT"

// ProbeStatus describes the outcome of probing the local engram
// daemon. The zero value is ProbeRunning (the happy path), but
// callers should not rely on the zero value: always compare
// against the named constants.
type ProbeStatus int

const (
	// ProbeRunning indicates the local daemon answered /health
	// with a 2xx response. The Result.Err is always nil.
	ProbeRunning ProbeStatus = iota

	// ProbeNotRunning indicates the local daemon is not
	// listening on the probed port. The Result.Err unwraps to
	// *net.OpError{Op: "dial"}.
	ProbeNotRunning

	// ProbeUnreachable indicates the probe could not complete
	// for a reason other than "not listening": timeout, context
	// cancellation, 5xx response, or transport error. The
	// Result.Err is always non-nil.
	ProbeUnreachable
)

// String returns the user-facing label for the status. The view
// layer (TUI/CLI) renders these labels directly, so a silent
// rename would be a user-visible regression. Pinned by
// TestProbeStatusStringer.
func (s ProbeStatus) String() string {
	switch s {
	case ProbeRunning:
		return "running"
	case ProbeNotRunning:
		return "not running"
	case ProbeUnreachable:
		return "unreachable"
	default:
		return "unknown"
	}
}

// Result captures the outcome of a single LocalDaemonProbe call.
// Status is always populated. Port echoes the port that was
// probed. Err is non-nil whenever Status is ProbeNotRunning or
// ProbeUnreachable, and nil when Status is ProbeRunning.
type Result struct {
	Status ProbeStatus
	Port   int
	Err    error
}

// ProbeTimeout bounds the duration of a single LocalDaemonProbe
// call. It is a var (not const) so tests can shorten it when
// exercising the timeout branch, per ADR-1 of the design. The
// default of 1 second matches the existing CLI's
// daemonProbeTimeout in cmd/engram/cloud_daemon_probe.go.
var ProbeTimeout = time.Second

// ProbeTransport is the http.RoundTripper used by LocalDaemonProbe.
// It is a var so tests can substitute a slow or failing transport
// to deterministically exercise the timeout and error branches
// without a real network. Defaults to http.DefaultTransport.
var ProbeTransport http.RoundTripper = http.DefaultTransport

// LocalDaemonProbe issues a short-timeout HTTP GET to /health on
// the local engram daemon. The classification rules per REQ-1
// of the spec:
//
//   - 2xx response on /health -> ProbeRunning (Err == nil)
//   - *net.OpError{Op: "dial"} on connect -> ProbeNotRunning
//   - any other error (timeout, context cancellation, transport
//     error) -> ProbeUnreachable
//   - non-2xx response -> ProbeUnreachable with an "unexpected
//     status" error
//
// The function uses ProbeTimeout for the request timeout and
// ProbeTransport for the underlying transport; both are package
// vars so tests can override them.
//
// LocalDaemonProbe is exposed as a var (not a const func) so
// callers and tests can substitute a fake implementation. The
// default is the real network probe below; tests in this
// package and cmd/engram/cloud_daemon_probe_test.go stub the
// var to return canned results. This pattern mirrors the
// existing CLI/TUI `var ProbeTimeout` and `var ProbeTransport`
// seams (per design ADR-1).
var LocalDaemonProbe = localDaemonProbe

func localDaemonProbe(ctx context.Context, port int) Result {
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{Status: ProbeUnreachable, Port: port, Err: err}
	}
	client := &http.Client{
		Timeout:   ProbeTimeout,
		Transport: ProbeTransport,
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Status: classifyDialErr(err), Port: port, Err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return Result{Status: ProbeRunning, Port: port}
	}
	return Result{
		Status: ProbeUnreachable,
		Port:   port,
		Err:    fmt.Errorf("unexpected status %d", resp.StatusCode),
	}
}

// classifyDialErr maps a non-nil error from http.Client.Do to a
// ProbeStatus. The split between ProbeNotRunning and
// ProbeUnreachable is the load-bearing detail of the probe:
//
//   - *net.OpError{Op: "dial"} means the daemon is not listening
//     on the probed port; this is the "daemon is down" signal.
//   - any other error (timeout, context cancellation, transport
//     error) is "the daemon might be there but something else
//     went wrong"; the user-facing label is "unreachable".
//
// A nil error returns ProbeRunning so callers can use the helper
// unconditionally; in practice the only caller only invokes it
// on a non-nil err.
func classifyDialErr(err error) ProbeStatus {
	if err == nil {
		return ProbeRunning
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return ProbeNotRunning
	}
	return ProbeUnreachable
}

// ResolvePort returns the local port the probe should target.
// The ENGRAM_PORT environment variable takes precedence when it
// parses to a valid port in [1, 65535]; otherwise the default
// 7437 is returned. Whitespace-only and out-of-range values fall
// back to the default. Surrounding whitespace on a valid value
// is trimmed before parsing, matching the existing CLI's
// resolveDaemonProbePort in cmd/engram/cloud_daemon_probe.go.
func ResolvePort() int {
	v := strings.TrimSpace(os.Getenv(EnvProbePort))
	if v == "" {
		return DefaultProbePort
	}
	port, err := strconv.Atoi(v)
	if err != nil {
		return DefaultProbePort
	}
	if port < 1 || port > 65535 {
		return DefaultProbePort
	}
	return port
}
