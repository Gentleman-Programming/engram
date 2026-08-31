package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDefaultCloudDaemonProbeReturnsRunningOn200(t *testing.T) {
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
	res := defaultCloudDaemonProbe(context.Background(), port)
	if res.Status != daemonProbeRunning {
		t.Fatalf("expected daemonProbeRunning, got %q (err=%v)", res.Status, res.Err)
	}
	if res.Port != port {
		t.Fatalf("expected port %d, got %d", port, res.Port)
	}
}

func TestDefaultCloudDaemonProbeReturnsNotRunningOnRefused(t *testing.T) {
	port := allocateClosedPort(t)
	res := defaultCloudDaemonProbe(context.Background(), port)
	if res.Status != daemonProbeNotRunning {
		t.Fatalf("expected daemonProbeNotRunning on refused, got %q (err=%v)", res.Status, res.Err)
	}
	if res.Err == nil {
		t.Fatalf("expected non-nil err for refused dial")
	}
}

func TestDefaultCloudDaemonProbeReturnsUnreachableOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	port := portFromTestServer(t, srv)
	res := defaultCloudDaemonProbe(context.Background(), port)
	if res.Status != daemonProbeUnreachable {
		t.Fatalf("expected daemonProbeUnreachable on 500, got %q", res.Status)
	}
}

func TestDefaultCloudDaemonProbeReturnsUnreachableOnTimeout(t *testing.T) {
	prev := daemonProbeTimeout
	daemonProbeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { daemonProbeTimeout = prev })

	// Listener accepts but never reads/writes, forcing the client to time out.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		// Hold the connection until the listener closes; never write a response.
		<-done
		_ = conn.Close()
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	res := defaultCloudDaemonProbe(context.Background(), port)
	if res.Status != daemonProbeUnreachable {
		t.Fatalf("expected daemonProbeUnreachable on timeout, got %q (err=%v)", res.Status, res.Err)
	}
}

func TestResolveDaemonProbePortHonorsEnvAndDefaults(t *testing.T) {
	t.Run("defaults to 7437 when ENGRAM_PORT unset", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT", "")
		if got := resolveDaemonProbePort(); got != defaultDaemonProbePort {
			t.Fatalf("expected default %d, got %d", defaultDaemonProbePort, got)
		}
	})
	t.Run("honors valid ENGRAM_PORT", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT", "9999")
		if got := resolveDaemonProbePort(); got != 9999 {
			t.Fatalf("expected 9999, got %d", got)
		}
	})
	t.Run("falls back to default on invalid ENGRAM_PORT", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT", "not-a-number")
		if got := resolveDaemonProbePort(); got != defaultDaemonProbePort {
			t.Fatalf("expected default %d, got %d", defaultDaemonProbePort, got)
		}
	})
	t.Run("falls back to default on out-of-range ENGRAM_PORT", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT", "0")
		if got := resolveDaemonProbePort(); got != defaultDaemonProbePort {
			t.Fatalf("expected default %d, got %d", defaultDaemonProbePort, got)
		}
		t.Setenv("ENGRAM_PORT", "70000")
		if got := resolveDaemonProbePort(); got != defaultDaemonProbePort {
			t.Fatalf("expected default %d, got %d", defaultDaemonProbePort, got)
		}
	})
}

func TestPrintCloudStatusDaemonProbeFormatsEachState(t *testing.T) {
	cases := []struct {
		name      string
		stub      func(context.Context, int) daemonProbeResult
		wantLines []string
	}{
		{
			name: "running",
			stub: func(_ context.Context, port int) daemonProbeResult {
				return daemonProbeResult{Status: daemonProbeRunning, Port: port}
			},
			wantLines: []string{"Local daemon: running on port"},
		},
		{
			name: "not_running prints recovery hint",
			stub: func(_ context.Context, port int) daemonProbeResult {
				return daemonProbeResult{Status: daemonProbeNotRunning, Port: port}
			},
			wantLines: []string{
				"Local daemon: not running on port",
				"Hint: run `engram serve`",
				"launchd",
			},
		},
		{
			name: "unreachable surfaces probe error",
			stub: func(_ context.Context, port int) daemonProbeResult {
				return daemonProbeResult{Status: daemonProbeUnreachable, Port: port, Err: fmt.Errorf("simulated boom")}
			},
			wantLines: []string{
				"Local daemon: unreachable on port",
				"probe error: simulated boom",
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			prev := cloudDaemonProbe
			cloudDaemonProbe = tc.stub
			t.Cleanup(func() { cloudDaemonProbe = prev })

			stdout, _, recovered := captureOutputAndRecover(t, func() { printCloudStatusDaemonProbe() })
			if recovered != nil {
				t.Fatalf("unexpected panic: %v", recovered)
			}
			for _, want := range tc.wantLines {
				if !strings.Contains(stdout, want) {
					t.Fatalf("expected stdout to contain %q, got %q", want, stdout)
				}
			}
		})
	}
}

// portFromTestServer extracts the TCP port a httptest.Server is bound to.
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

// allocateClosedPort returns a TCP port number that is guaranteed to be
// closed: it binds, reads the assigned port, then closes the listener.
// On loopback this reliably surfaces "connection refused" on dial.
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
