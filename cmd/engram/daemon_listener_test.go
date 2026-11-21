package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestUnmanagedListenerDiagnostics(t *testing.T) {
	installed := version
	version = "3.7.2"
	t.Cleanup(func() { version = installed })
	for _, recordState := range []string{"missing", "dead", "live mismatch"} {
		for _, service := range []string{"engram", "other"} {
			for _, verb := range []string{"ps", "stop", "run"} {
				t.Run(fmt.Sprintf("record=%s/service=%s/%s", recordState, service, verb), func(t *testing.T) {
					var probes atomic.Int64
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method != http.MethodGet || r.URL.Path != "/health" {
							t.Errorf("unexpected probe %s %s", r.Method, r.URL.Path)
						}
						probes.Add(1)
						_, _ = fmt.Fprintf(w, `{"status":"ok","service":%q,"version":"0.1.0"}`, service)
					}))
					defer server.Close()
					port := server.Listener.Addr().(*net.TCPAddr).Port
					t.Setenv("ENGRAM_PORT", strconv.Itoa(port))
					cfg := store.Config{DataDir: t.TempDir()}
					if recordState != "missing" {
						pid := os.Getpid()
						if recordState == "dead" {
							pid = exitedChildPid(t)
						}
						if err := daemon.WritePidfile(daemon.PidfilePath(cfg.DataDir), daemon.PidRecord{Pid: pid, Port: port, Token: "expired-start"}); err != nil {
							t.Fatal(err)
						}
					}
					out := swapStdoutPrinter(t)
					signals := 0
					saved := stopDaemonProcessNow
					stopDaemonProcessNow = func(int) error { signals++; return nil }
					t.Cleanup(func() { stopDaemonProcessNow = saved })
					var err error
					switch verb {
					case "ps":
						err = cmdPs(cfg, nil)
					case "stop":
						err = cmdStop(cfg, nil)
					case "run":
						err = assertDaemonStartable(cfg.DataDir, port)
					}
					if err == nil {
						t.Fatal("occupied listener must not be represented as a managed/stopped/startable daemon")
					}
					diagnostic := out.String() + err.Error()
					want := "unmanaged Engram listener"
					if service != "engram" {
						want = "unknown listener"
					}
					if !strings.Contains(diagnostic, want) || !strings.Contains(diagnostic, strconv.Itoa(port)) {
						t.Fatalf("diagnostic = %q, want %q and actual port", diagnostic, want)
					}
					if verb == "ps" && probes.Load() != 1 {
						t.Fatalf("ps made %d health requests, want one", probes.Load())
					}
					if verb == "ps" && service == "engram" {
						if !strings.Contains(diagnostic, "server 0.1.0") ||
							!strings.Contains(diagnostic, "installed CLI 3.7.2") {
							t.Errorf("missing diagnostic-only versions: %q", diagnostic)
						}
					} else if strings.Contains(diagnostic, "server 0.1.0") {
						t.Errorf("unexpected version metadata: %q", diagnostic)
					}
					if runtime.GOOS == "windows" && !strings.Contains(diagnostic, fmt.Sprintf("observed owning pid %d", os.Getpid())) {
						t.Fatalf("diagnostic omits actual fixture listener owner: %q", diagnostic)
					}
					if strings.Contains(diagnostic, "not running") || strings.Contains(diagnostic, "nothing to stop") {
						t.Fatalf("listener hidden by misleading status: %q", diagnostic)
					}
					if signals != 0 {
						t.Fatalf("unverified listener signaled %d times", signals)
					}
					if recordState == "live mismatch" && verb != "run" {
						if record, err := daemon.ReadPidfile(daemon.PidfilePath(cfg.DataDir)); err != nil || record.Pid != os.Getpid() || record.Token != "expired-start" {
							t.Fatalf("unconfirmed live record changed: %v/%v", record, err)
						}
					} else if _, err := os.Stat(daemon.PidfilePath(cfg.DataDir)); !os.IsNotExist(err) {
						t.Fatalf("no listener may be adopted: pidfile stat = %v", err)
					}
				})
			}
		}
	}
}

func TestPsOmitsInvalidUnmanagedListenerVersion(t *testing.T) {
	for _, reported := range []string{"dev", "private\nmetadata", "0.1.0-" + strings.Repeat("a", 64)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `{"status":"ok","service":"engram","version":%q}`, reported)
		}))
		t.Setenv("ENGRAM_PORT", strconv.Itoa(server.Listener.Addr().(*net.TCPAddr).Port))
		err := cmdPs(store.Config{DataDir: t.TempDir()}, nil)
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "unmanaged Engram listener") || strings.Contains(err.Error(), "Versions:") {
			t.Errorf("invalid observed release produced a version diagnostic: %v", err)
		}
	}
}

func TestRedirectCannotHideOccupiedListener(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Port zero cannot be a bound listening endpoint: bind(0) allocates
		// another port. Following this redirect must not make this server absent.
		http.Redirect(w, r, "http://127.0.0.1:0/health", http.StatusFound)
	}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	t.Setenv("ENGRAM_PORT", strconv.Itoa(port))
	for _, verb := range []string{"ps", "stop", "run"} {
		t.Run(verb, func(t *testing.T) {
			cfg := store.Config{DataDir: t.TempDir()}
			var err error
			switch verb {
			case "ps":
				err = cmdPs(cfg, nil)
			case "stop":
				err = cmdStop(cfg, nil)
			case "run":
				err = assertDaemonStartable(cfg.DataDir, port)
			}
			if err == nil || !strings.Contains(err.Error(), "unknown listener") {
				t.Fatalf("redirecting occupied port must remain unknown, got %v", err)
			}
		})
	}
}
