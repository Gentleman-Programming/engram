package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestResolveServeOptions(t *testing.T) {
	veryLongPort := strings.Repeat("9", 1_000)

	for _, tt := range []struct {
		name       string
		envPort    string
		envSocket  string
		args       []string
		wantPort   int
		wantSocket string
		wantErr    bool
	}{
		{name: "default TCP", wantPort: 7437},
		{name: "environment TCP port", envPort: "8090", wantPort: 8090},
		{name: "zero environment port falls back to default", envPort: "0", wantPort: 7437},
		{name: "all-zero environment port falls back to default", envPort: "0000", wantPort: 7437},
		{name: "leading-zero environment port is valid", envPort: "00080", wantPort: 80},
		{name: "negative environment port falls back to default", envPort: "-1", wantPort: 7437},
		{name: "plus-signed environment port falls back to default", envPort: "+7437", wantPort: 7437},
		{name: "maximum environment port is valid", envPort: "65535", wantPort: 65535},
		{name: "out-of-range environment port falls back to default", envPort: "65536", wantPort: 7437},
		{name: "very long environment port falls back to default", envPort: veryLongPort, wantPort: 7437},
		{name: "positional port overrides environment", envPort: "8090", args: []string{"9000"}, wantPort: 9000},
		{name: "environment socket uses default port without ambiguity", envSocket: "/tmp/engram.sock", wantPort: 7437, wantSocket: "/tmp/engram.sock"},
		{name: "socket flag overrides environment socket", envSocket: "/tmp/environment.sock", args: []string{"--socket", "/tmp/flag.sock"}, wantPort: 7437, wantSocket: "/tmp/flag.sock"},
		{name: "socket equals flag", args: []string{"--socket=/tmp/engram.sock"}, wantPort: 7437, wantSocket: "/tmp/engram.sock"},
		{name: "socket and environment port are ambiguous", envPort: "8090", args: []string{"--socket", "/tmp/engram.sock"}, wantErr: true},
		{name: "environment socket and positional port are ambiguous", envSocket: "/tmp/engram.sock", args: []string{"8090"}, wantErr: true},
		{name: "socket requires path", args: []string{"--socket"}, wantErr: true},
		{name: "socket equals flag requires path", args: []string{"--socket="}, wantErr: true},
		{name: "unknown argument is rejected", args: []string{"--unknown"}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENGRAM_PORT", tt.envPort)
			t.Setenv("ENGRAM_SOCKET", tt.envSocket)
			options, err := resolveServeOptions(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve options: %v", err)
			}
			if options.port != tt.wantPort || options.socketPath != tt.wantSocket {
				t.Fatalf("options = %+v, want port=%d socket=%q", options, tt.wantPort, tt.wantSocket)
			}
		})
	}
}

func TestCmdServeSignalClosesUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-domain sockets are not supported on Windows")
	}
	socketPath := filepath.Join(t.TempDir(), "engram.sock")
	t.Setenv("ENGRAM_SOCKET", "")
	t.Setenv("ENGRAM_PORT", "")
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "")
	withArgs(t, "engram", "serve", "--socket", socketPath)
	cfg := testConfig(t)

	oldNotify, oldStop := notifySignals, stopSignals
	registered := make(chan chan<- os.Signal, 1)
	notifySignals = func(ch chan<- os.Signal, _ ...os.Signal) { registered <- ch }
	stopSignals = func(chan<- os.Signal) {}
	t.Cleanup(func() {
		notifySignals = oldNotify
		stopSignals = oldStop
	})

	done := make(chan struct{})
	go func() {
		cmdServe(cfg)
		close(done)
	}()

	var signalCh chan<- os.Signal
	select {
	case signalCh = <-registered:
	case <-time.After(2 * time.Second):
		t.Fatal("cmdServe did not register signal handling")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(socketPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("socket was not created: %v", err)
	}

	signalCh <- syscall.SIGTERM
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cmdServe did not return through cleanup after signal")
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket remains after signal cleanup: %v", err)
	}
}
