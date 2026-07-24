package cloudconfig

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// TestLocalDaemonProbe200 is the happy-path test for LocalDaemonProbe:
// a 2xx response on the test server's /health endpoint classifies as
// ProbeRunning with a nil error. The returned port must match the one
// supplied to the probe.
func TestLocalDaemonProbe200(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	port := portFromTestServer(t, srv)
	res := LocalDaemonProbe(context.Background(), port)

	if res.Status != ProbeRunning {
		t.Fatalf("expected ProbeRunning, got %q (err=%v)", res.Status, res.Err)
	}
	if res.Err != nil {
		t.Fatalf("expected nil err on 200, got %v", res.Err)
	}
	if res.Port != port {
		t.Fatalf("expected port %d, got %d", port, res.Port)
	}
}

// TestLocalDaemonProbeNotRunning covers the "daemon is not listening"
// branch: dial to a closed port on 127.0.0.1 must classify as
// ProbeNotRunning with a non-nil error. The port is allocated by
// binding then closing a listener so the test is deterministic across
// CI runs.
func TestLocalDaemonProbeNotRunning(t *testing.T) {
	t.Parallel()

	port := allocateClosedPort(t)
	res := LocalDaemonProbe(context.Background(), port)

	if res.Status != ProbeNotRunning {
		t.Fatalf("expected ProbeNotRunning on closed port, got %q (err=%v)", res.Status, res.Err)
	}
	if res.Err == nil {
		t.Fatalf("expected non-nil err for refused dial")
	}
	if res.Port != port {
		t.Fatalf("expected port %d, got %d", port, res.Port)
	}
}

// TestLocalDaemonProbeNotRunningIsNetOpError is the TRIANGULATE pin
// for the dial classification: the spec mandates that connection
// refused be classified as ProbeNotRunning via *net.OpError{Op:"dial"}.
// This test makes the contract explicit so a future refactor cannot
// silently change the error type without breaking the test.
func TestLocalDaemonProbeNotRunningIsNetOpError(t *testing.T) {
	t.Parallel()

	port := allocateClosedPort(t)
	res := LocalDaemonProbe(context.Background(), port)

	if res.Status != ProbeNotRunning {
		t.Fatalf("expected ProbeNotRunning on closed port, got %q", res.Status)
	}
	var opErr *net.OpError
	if !errors.As(res.Err, &opErr) {
		t.Fatalf("expected error to unwrap to *net.OpError, got %T (%v)", res.Err, res.Err)
	}
	if opErr.Op != "dial" {
		t.Fatalf("expected *net.OpError.Op == \"dial\", got %q", opErr.Op)
	}
}

// TestLocalDaemonProbeUnreachable500 covers the "server answers but
// the answer is broken" branch: a 5xx response classifies as
// ProbeUnreachable with a non-nil error that explains the bad status.
func TestLocalDaemonProbeUnreachable500(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	port := portFromTestServer(t, srv)
	res := LocalDaemonProbe(context.Background(), port)

	if res.Status != ProbeUnreachable {
		t.Fatalf("expected ProbeUnreachable on 500, got %q (err=%v)", res.Status, res.Err)
	}
	if res.Err == nil {
		t.Fatalf("expected non-nil err for 500 response")
	}
	if res.Port != port {
		t.Fatalf("expected port %d, got %d", port, res.Port)
	}
}

// slowTransport is a RoundTripper that blocks until either the
// request context is canceled (e.g. by http.Client.Timeout) or a
// fallback delay elapses. Used to exercise the timeout branch of
// LocalDaemonProbe deterministically without a real network.
type slowTransport struct {
	delay time.Duration
}

func (s *slowTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case <-time.After(s.delay):
		return nil, fmt.Errorf("slow transport: request not canceled within %v", s.delay)
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

// TestLocalDaemonProbeTimeout covers the "server never responds"
// branch: when the request exceeds ProbeTimeout, the probe must
// classify as ProbeUnreachable. The test uses a custom slowTransport
// that blocks until the request context is canceled; the 50ms
// ProbeTimeout fires before the 200ms transport delay completes.
//
// NOTE: this test mutates the package-level ProbeTimeout and
// ProbeTransport vars, so it must run serially (no t.Parallel).
// Otherwise parallel probe tests would observe the modified
// transport and timeout, surfacing flaky "context deadline exceeded"
// errors instead of the intended status. Mirrors the pattern in
// cmd/engram/cloud_daemon_probe_test.go:62-89.
func TestLocalDaemonProbeTimeout(t *testing.T) {

	prevTimeout := ProbeTimeout
	prevTransport := ProbeTransport
	ProbeTimeout = 50 * time.Millisecond
	ProbeTransport = &slowTransport{delay: 200 * time.Millisecond}
	t.Cleanup(func() {
		ProbeTimeout = prevTimeout
		ProbeTransport = prevTransport
	})

	port := allocateClosedPort(t)
	res := LocalDaemonProbe(context.Background(), port)

	if res.Status != ProbeUnreachable {
		t.Fatalf("expected ProbeUnreachable on timeout, got %q (err=%v)", res.Status, res.Err)
	}
	if res.Err == nil {
		t.Fatalf("expected non-nil err for timeout")
	}
}

// TestLocalDaemonProbeContextCanceled covers the "caller cancels
// the probe" branch: a pre-canceled context surfaces as
// ProbeUnreachable (not ProbeNotRunning) because the cancellation
// wraps a context error, not a *net.OpError dial error.
func TestLocalDaemonProbeContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	port := allocateClosedPort(t)
	res := LocalDaemonProbe(ctx, port)

	if res.Status != ProbeUnreachable {
		t.Fatalf("expected ProbeUnreachable on canceled context, got %q (err=%v)", res.Status, res.Err)
	}
	if res.Err == nil {
		t.Fatalf("expected non-nil err for canceled context")
	}
	if res.Port != port {
		t.Fatalf("expected port %d, got %d", port, res.Port)
	}
}

// TestResolvePort covers the ResolvePort contract: ENGRAM_PORT
// takes precedence when valid (1-65535 inclusive), and the function
// falls back to DefaultProbePort (7437) for unset, empty,
// whitespace-only, non-numeric, zero, negative, or out-of-range
// values. The implementation must trim whitespace before parsing.
//
// NOTE: t.Setenv is incompatible with t.Parallel, so this test runs
// serially. Each subtest sets a fresh t.Setenv and relies on t.Setenv's
// automatic Cleanup to restore the parent value.
func TestResolvePort(t *testing.T) {

	cases := []struct {
		name   string
		envVal string
		want   int
	}{
		{"unset", "", 7437},
		{"valid_low", "1", 1},
		{"valid_high", "65535", 65535},
		{"valid_typical", "8080", 8080},
		{"valid_7437", "7437", 7437},
		{"invalid_nonnumeric", "abc", 7437},
		{"invalid_zero", "0", 7437},
		{"invalid_negative", "-1", 7437},
		{"invalid_too_high", "99999", 7437},
		{"empty_value", "", 7437},
		{"whitespace", "   ", 7437},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_PORT", tc.envVal)
			if got := ResolvePort(); got != tc.want {
				t.Fatalf("ENGRAM_PORT=%q: got %d, want %d", tc.envVal, got, tc.want)
			}
		})
	}
}

// TestResolvePortTrimsWhitespace is the TRIANGULATE pin for
// whitespace handling: a value like "  8080  " must parse to 8080
// because the function trims surrounding whitespace before calling
// strconv.Atoi. Matches the existing CLI's resolveDaemonProbePort
// behavior in cmd/engram/cloud_daemon_probe.go.
//
// NOTE: t.Setenv is incompatible with t.Parallel, so this test runs
// serially.
func TestResolvePortTrimsWhitespace(t *testing.T) {
	t.Setenv("ENGRAM_PORT", "  8080  ")
	if got := ResolvePort(); got != 8080 {
		t.Fatalf("expected 8080 (whitespace-trimmed), got %d", got)
	}
}

// TestProbeStatusStringer pins the user-facing label of each
// ProbeStatus value. The view layer (TUI/CLI) renders these labels
// directly, so a silent rename would be a user-visible regression.
func TestProbeStatusStringer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status ProbeStatus
		want   string
	}{
		{ProbeRunning, "running"},
		{ProbeNotRunning, "not running"},
		{ProbeUnreachable, "unreachable"},
		{ProbeStatus(99), "unknown"},
	}

	for _, tc := range cases {
		if got := tc.status.String(); got != tc.want {
			t.Fatalf("ProbeStatus(%d).String() = %q, want %q", tc.status, got, tc.want)
		}
	}
}

// portFromTestServer extracts the TCP port a httptest.Server is
// bound to. Mirrors the helper in cmd/engram/cloud_daemon_probe_test.go
// (private to that package).
func portFromTestServer(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return port
}

// allocateClosedPort returns a TCP port number that is guaranteed
// to be closed: it binds, reads the assigned port, then closes the
// listener. On loopback this reliably surfaces "connection refused"
// on dial, which is the path the ProbeNotRunning classification
// hinges on.
func allocateClosedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return port
}
