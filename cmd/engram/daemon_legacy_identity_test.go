package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/daemon"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestStopDaemonRequiresPositiveIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, token, body string
		confirmed         bool
	}{
		{"legacy HTML listener", "", `<html>another service</html>`, false},
		{"legacy empty response", "", ``, false},
		{"legacy foreign daemon", "", `{"daemon_token":"foreign"}`, false},
		{"legacy missing identity", "", `{"status":"ok","service":"engram"}`, false},
		{"missing response token", "ours", `{"status":"ok"}`, false},
		{"mismatched response token", "ours", `{"daemon_token":"foreign"}`, false},
		{"malformed response", "ours", `{"daemon_token":"ours"`, false},
		{"matching identity", "ours", `{"daemon_token":"ours"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/health" {
					http.NotFound(w, r)
					return
				}
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			cfg := store.Config{DataDir: t.TempDir()}
			path := daemon.PidfilePath(cfg.DataDir)
			record := daemon.PidRecord{Pid: os.Getpid(), Port: srv.Listener.Addr().(*net.TCPAddr).Port, Token: tc.token}
			if err := daemon.WritePidfile(path, record); err != nil {
				t.Fatal(err)
			}
			oldProbe := daemonStatusProbe
			daemonStatusProbe = defaultDaemonStatusProbe
			t.Cleanup(func() { daemonStatusProbe = oldProbe })
			signaled := swapStopDaemonProcess(t)
			swapStdoutPrinter(t)
			_, err := stopDaemon(cfg)
			if tc.confirmed {
				if err != nil || len(*signaled) != 1 {
					t.Fatalf("confirmed stop: signals=%v err=%v", *signaled, err)
				}
				if _, err := daemon.ReadPidfile(path); !errors.Is(err, daemon.ErrNoPidfile) {
					t.Fatalf("confirmed record survived: %v", err)
				}
				return
			}
			if !errors.Is(err, errDaemonUnconfirmed) || len(*signaled) != 0 {
				t.Errorf("unconfirmed stop: signals=%v err=%v; want zero signals and unconfirmed", *signaled, err)
			}
			if current, err := daemon.ReadPidfile(path); err != nil || current != record {
				t.Errorf("unconfirmed record changed: %+v %v", current, err)
			}
		})
	}
}

func TestLegacyDaemonRecordRemainsUnmanaged(t *testing.T) {
	for _, command := range []string{"ps", "start", "restart"} {
		t.Run(command, func(t *testing.T) {
			clearRunEnv(t)
			cfg := store.Config{DataDir: t.TempDir()}
			path := daemon.PidfilePath(cfg.DataDir)
			record := daemon.PidRecord{Pid: os.Getpid(), Port: 7603}
			if err := daemon.WritePidfile(path, record); err != nil {
				t.Fatal(err)
			}
			swapDaemonStatusProbe(t, daemon.ProbeResult{Status: daemon.StatusRunning, Port: record.Port, Token: "some-other-start"})
			signaled := swapStopDaemonProcess(t)
			swapStdoutPrinter(t)
			oldArgs := spawnDaemonArgs
			spawnDaemonArgs = func(int) []string { t.Fatal("unidentified legacy record reached child spawn"); return nil }
			t.Cleanup(func() { spawnDaemonArgs = oldArgs })
			var err error
			switch command {
			case "ps":
				err = cmdPs(cfg, nil)
			case "start":
				err = assertDaemonStartable(cfg.DataDir, record.Port)
			case "restart":
				err = cmdRestart(cfg, nil)
			}
			if err == nil {
				t.Error("legacy record treated as an identified daemon")
			}
			if command != "ps" && !errors.Is(err, errDaemonUnconfirmed) {
				t.Errorf("error=%v, want unconfirmed", err)
			}
			if len(*signaled) != 0 {
				t.Errorf("legacy record signaled: %v", *signaled)
			}
			if current, err := daemon.ReadPidfile(path); err != nil || current != record {
				t.Errorf("legacy record changed: %+v %v", current, err)
			}
		})
	}
}
