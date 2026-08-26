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

// DefaultProbePort is the port used by engram serve when ENGRAM_PORT is unset.
const DefaultProbePort = 7437

// EnvProbePort is the environment variable that overrides the local daemon port.
const EnvProbePort = "ENGRAM_PORT"

// ProbeStatus describes a local daemon health probe outcome.
type ProbeStatus int

const (
	ProbeRunning ProbeStatus = iota
	ProbeNotRunning
	ProbeUnreachable
)

// String returns the user-facing status label.
func (status ProbeStatus) String() string {
	switch status {
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

// Result is one local daemon health probe result.
type Result struct {
	Status ProbeStatus
	Port   int
	Err    error
}

// ProbeTimeout bounds the daemon health request and can be shortened by tests.
var ProbeTimeout = time.Second

// ProbeTransport is replaceable so probe error paths remain deterministic in tests.
var ProbeTransport http.RoundTripper = http.DefaultTransport

// LocalDaemonProbe is replaceable to keep TUI status loading deterministic in tests.
var LocalDaemonProbe = probeLocalDaemon

func probeLocalDaemon(ctx context.Context, port int) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", port), nil)
	if err != nil {
		return Result{Status: ProbeUnreachable, Port: port, Err: err}
	}
	resp, err := (&http.Client{Timeout: ProbeTimeout, Transport: ProbeTransport}).Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return Result{Status: ProbeNotRunning, Port: port, Err: err}
		}
		return Result{Status: ProbeUnreachable, Port: port, Err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return Result{Status: ProbeRunning, Port: port}
	}
	return Result{Status: ProbeUnreachable, Port: port, Err: fmt.Errorf("unexpected status %d", resp.StatusCode)}
}

// ResolvePort returns ENGRAM_PORT when valid, otherwise the serve default.
func ResolvePort() int {
	value := strings.TrimSpace(os.Getenv(EnvProbePort))
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return DefaultProbePort
	}
	return port
}
