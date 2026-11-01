package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Status describes the outcome of probing the local daemon.
type Status string

const (
	// StatusRunning means /health answered 2xx.
	StatusRunning Status = "running"
	// StatusNotRunning means the TCP dial was refused: nothing is listening.
	StatusNotRunning Status = "not_running"
	// StatusUnreachable means the port answers but not with a healthy 2xx
	// reply (timeout, non-2xx status, or malformed response).
	StatusUnreachable Status = "unreachable"
)

// ProbeResult captures the outcome of a single daemon health probe. Token
// is the daemon identity the server reported on /health ("daemon_token"):
// set only when the probed server is a token-carrying daemon, empty for a
// foreground serve or any non-engram listener, so callers can compare it
// with the pidfile record before trusting the pid.
type ProbeResult struct {
	Status Status
	Port   int
	Token  string
	Err    error
}

// Probe issues a bounded GET to /health on the local engram daemon. A dial
// error to 127.0.0.1 means "not running"; any other error (timeout, non-2xx
// response, malformed reply) maps to "unreachable" so callers can tell "the
// daemon is gone" from "the daemon is misbehaving".
func Probe(ctx context.Context, port int, timeout time.Duration) ProbeResult {
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ProbeResult{Status: StatusUnreachable, Port: port, Err: err}
	}
	resp, err := client.Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return ProbeResult{Status: StatusNotRunning, Port: port, Err: err}
		}
		return ProbeResult{Status: StatusUnreachable, Port: port, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	var health struct {
		DaemonToken string `json:"daemon_token"`
	}
	// A body that is not the expected JSON still classifies by status code:
	// the token simply stays empty, which token-checking callers treat as
	// "identity did not answer".
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&health)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return ProbeResult{Status: StatusRunning, Port: port, Token: health.DaemonToken}
	}
	return ProbeResult{Status: StatusUnreachable, Port: port}
}
