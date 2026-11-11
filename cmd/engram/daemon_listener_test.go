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
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestUnmanagedListenerDiagnostics(t *testing.T) {
	for _, recordState := range []string{"missing", "dead", "live mismatch"} {
		for _, service := range []string{"engram", "other"} {
			for _, verb := range []string{"ps", "stop", "run"} {
				t.Run(fmt.Sprintf("record=%s/service=%s/%s", recordState, service, verb), func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						_, _ = fmt.Fprintf(w, `{"status":"ok","service":%q,"version":"test"}`, service)
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
