package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/cloudconfig"
)

// TestResolveDaemonProbePortHonorsEnvAndDefaults pins the
// contract of the local resolveDaemonProbePort wrapper
// (cmd/engram/cloud_daemon_probe.go). The wrapper is a thin
// shim over cloudconfig.ResolvePort but the env-var name
// (cloudconfig.EnvProbePort = "ENGRAM_PORT") and the default
// (cloudconfig.DefaultProbePort = 7437) are part of the
// public surface and must remain stable for cmdCloudStatus
// and any future TUI parity work.
func TestResolveDaemonProbePortHonorsEnvAndDefaults(t *testing.T) {
	t.Run("defaults to 7437 when ENGRAM_PORT unset", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT", "")
		if got := resolveDaemonProbePort(); got != cloudconfig.DefaultProbePort {
			t.Fatalf("expected default %d, got %d", cloudconfig.DefaultProbePort, got)
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
		if got := resolveDaemonProbePort(); got != cloudconfig.DefaultProbePort {
			t.Fatalf("expected default %d, got %d", cloudconfig.DefaultProbePort, got)
		}
	})
	t.Run("falls back to default on out-of-range ENGRAM_PORT", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT", "0")
		if got := resolveDaemonProbePort(); got != cloudconfig.DefaultProbePort {
			t.Fatalf("expected default %d, got %d", cloudconfig.DefaultProbePort, got)
		}
		t.Setenv("ENGRAM_PORT", "70000")
		if got := resolveDaemonProbePort(); got != cloudconfig.DefaultProbePort {
			t.Fatalf("expected default %d, got %d", cloudconfig.DefaultProbePort, got)
		}
	})
}

// TestPrintCloudStatusDaemonProbeFormatsEachState pins the
// per-state stdout output of printCloudStatusDaemonProbe.
// The test stubs cloudconfig.LocalDaemonProbe directly via
// the new var seam (T-608.15's porting step) — the local
// cloudDaemonProbe wrapper in cmd/engram/cloud_daemon_probe.go
// is still the caller's seam, but since the wrapper is a
// thin pass-through to cloudconfig.LocalDaemonProbe, stubbing
// the upstream var is equivalent to stubbing the wrapper and
// matches the design's ADR-1 pattern (vars in cloudconfig, not
// const funcs).
func TestPrintCloudStatusDaemonProbeFormatsEachState(t *testing.T) {
	cases := []struct {
		name      string
		stub      func(context.Context, int) cloudconfig.Result
		wantLines []string
	}{
		{
			name: "running",
			stub: func(_ context.Context, port int) cloudconfig.Result {
				return cloudconfig.Result{Status: cloudconfig.ProbeRunning, Port: port}
			},
			wantLines: []string{"Local daemon: running on port"},
		},
		{
			name: "not_running prints recovery hint",
			stub: func(_ context.Context, port int) cloudconfig.Result {
				return cloudconfig.Result{Status: cloudconfig.ProbeNotRunning, Port: port}
			},
			wantLines: []string{
				"Local daemon: not running on port",
				"Hint: run `engram serve`",
				"launchd",
			},
		},
		{
			name: "unreachable surfaces probe error",
			stub: func(_ context.Context, port int) cloudconfig.Result {
				return cloudconfig.Result{Status: cloudconfig.ProbeUnreachable, Port: port, Err: fmt.Errorf("simulated boom")}
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
			// Stub the cloudconfig package's LocalDaemonProbe var
			// (the new seam introduced in T-608.15). The local
			// cloudDaemonProbe wrapper in cmd/engram defaults to
			// cloudconfig.LocalDaemonProbe, so stubbing the
			// upstream var reaches printCloudStatusDaemonProbe
			// without needing a separate local seam.
			prev := cloudconfig.LocalDaemonProbe
			t.Cleanup(func() { cloudconfig.LocalDaemonProbe = prev })
			cloudconfig.LocalDaemonProbe = tc.stub

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
