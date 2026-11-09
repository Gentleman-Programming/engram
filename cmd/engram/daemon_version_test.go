package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestIssue2242CloudStatusWarnsAboutStaleServer(t *testing.T) {
	oldVersion, oldProbe := version, cloudDaemonProbe
	version = "3.6.2"
	cloudDaemonProbe = defaultCloudDaemonProbe
	t.Cleanup(func() { version = oldVersion; cloudDaemonProbe = oldProbe })
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/health" {
			t.Errorf("unexpected lifecycle request %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"status":"ok","service":"engram","instance_id":"fixture","version":"3.0.0"}`)
	}))
	defer srv.Close()
	t.Setenv("ENGRAM_PORT", fmt.Sprint(portFromTestServer(t, srv)))
	out, _, recovered := captureOutputAndRecover(t, printCloudStatusDaemonProbe)
	if recovered != nil {
		t.Fatal(recovered)
	}
	if !strings.Contains(out, "3.0.0") || !strings.Contains(out, "3.6.2") || !strings.Contains(strings.ToLower(out), "restart") {
		t.Fatalf("missing version/restart diagnostic: %q", out)
	}
	if requests.Load() != 1 {
		t.Fatalf("status performed %d requests, want one read-only probe", requests.Load())
	}
}

func TestDaemonVersionStatus(t *testing.T) {
	for _, tc := range []struct {
		name, server, installed string
		restart, unknown        bool
	}{
		{"same", "3.6.2", "3.6.2", false, false},
		{"prefix", "v3.6.2", "3.6.2", false, false},
		{"metadata", "3.6.2+server", "v3.6.2+cli", false, false},
		{"different", "3.0.0", "3.6.2", true, false},
		{"reverse", "3.6.2", "3.0.0", true, false},
		{"prerelease", "3.7.0-rc.1", "3.7.0", true, false},
		{"missing", "", "3.6.2", false, true},
		{"dev server", "dev", "3.6.2", false, true},
		{"dev cli", "3.6.2", "dev", false, true},
		{"malformed", "3.6.2\nFORGED", "3.6.2", false, true},
		{"both unknown", "bad", "bad", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := daemonVersionStatus(tc.server, tc.installed)
			if strings.Contains(out, "Restart pending") != tc.restart || strings.Contains(out, "comparison unavailable") != tc.unknown {
				t.Fatalf("version result: %q", out)
			}
			if strings.Contains(out, "older") || strings.Contains(out, "newer") || strings.Contains(out, "FORGED") {
				t.Fatalf("misleading/untrusted output: %q", out)
			}
		})
	}
}

func TestPsVersionDiagnosticPreservesIdentityAndPidfile(t *testing.T) {
	for _, token := range []string{"ours", "foreign"} {
		t.Run(token, func(t *testing.T) {
			oldVersion, oldProbe := version, daemonStatusProbe
			version = "3.6.2"
			daemonStatusProbe = defaultDaemonStatusProbe
			t.Cleanup(func() { version = oldVersion; daemonStatusProbe = oldProbe })
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/health" {
					t.Errorf("unexpected lifecycle request %s %s", r.Method, r.URL.Path)
				}
				_, _ = fmt.Fprintf(w, `{"status":"ok","service":"engram","daemon_token":%q,"version":"3.0.0"}`, token)
			}))
			defer srv.Close()
			cfg := store.Config{DataDir: t.TempDir()}
			record := daemon.PidRecord{Pid: os.Getpid(), Port: portFromTestServer(t, srv), Token: "ours"}
			path := daemon.PidfilePath(cfg.DataDir)
			if err := daemon.WritePidfile(path, record); err != nil {
				t.Fatal(err)
			}
			out := swapStdoutPrinter(t)
			err := cmdPs(cfg, nil)
			if token == "ours" {
				if err != nil || !strings.Contains(out.String(), "Restart pending") || !strings.Contains(out.String(), "3.6.2") || !strings.Contains(out.String(), "3.0.0") {
					t.Fatalf("ps: %q %v", out.String(), err)
				}
			} else if err == nil || strings.Contains(out.String(), "Restart pending") {
				t.Fatalf("version bypassed identity: %q %v", out.String(), err)
			}
			after, err := daemon.ReadPidfile(path)
			if err != nil || !reflect.DeepEqual(after, record) || requests.Load() != 1 {
				t.Fatalf("status mutated ownership or repeated probe: %+v %v requests=%d", after, err, requests.Load())
			}
		})
	}
}
