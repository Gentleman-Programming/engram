package server

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestServerDefaultsToLoopbackTCP(t *testing.T) {
	srv := New(nil, 7437)
	var network, address string
	srv.listen = func(gotNetwork, gotAddress string) (net.Listener, error) {
		network, address = gotNetwork, gotAddress
		return stubListener{}, nil
	}
	srv.serve = func(net.Listener, http.Handler) error { return errors.New("serve stopped") }
	if err := srv.Start(); err == nil || err.Error() != "serve stopped" {
		t.Fatalf("Start() error = %v, want serve stopped", err)
	}
	if network != "tcp" || address != "127.0.0.1:7437" {
		t.Fatalf("listener = %s %s, want tcp 127.0.0.1:7437", network, address)
	}
}

func requireUnixSockets(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix-domain sockets are not supported on Windows")
	}
}

func startUnixSocketServer(t *testing.T, srv *Server, socketPath string) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- srv.Start() }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("server exited before Unix socket readiness: %v", err)
		default:
		}
		info, err := os.Lstat(socketPath)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return done
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Unix socket was not created")
	return nil
}

func TestUnixSocketRejectsActiveSocketWithoutDisturbingListener(t *testing.T) {
	requireUnixSockets(t)
	socketPath := filepath.Join(t.TempDir(), "engram.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}
	active := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = active.Serve(listener) }()
	t.Cleanup(func() {
		_ = active.Close()
		_ = listener.Close()
		_ = os.Remove(socketPath)
	})

	candidate := New(newServerTestStore(t), 0)
	candidate.SetSocketPath(socketPath)
	if err := candidate.Start(); err == nil {
		t.Fatal("expected active Unix socket to be rejected")
	}

	client := unixSocketClient(socketPath)
	t.Cleanup(client.CloseIdleConnections)
	response, err := client.Get("http://localhost/health")
	if err != nil {
		t.Fatalf("active listener no longer serves requests: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("active listener status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
}

func unixSocketClient(socketPath string) *http.Client {
	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}
	return &http.Client{Transport: transport}
}

func TestUnixSocketServesHTTPWithRestrictivePermissions(t *testing.T) {
	requireUnixSockets(t)
	socketPath := filepath.Join(t.TempDir(), "engram.sock")
	srv := New(newServerTestStore(t), 0)
	srv.SetSocketPath(socketPath)
	done := startUnixSocketServer(t, srv, socketPath)

	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket permissions = %o, want 600", got)
	}

	client := unixSocketClient(socketPath)
	t.Cleanup(func() { client.CloseIdleConnections() })
	response, err := client.Get("http://localhost/health")
	if err != nil {
		t.Fatalf("GET /health over Unix socket: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /health = %d, want 200", response.StatusCode)
	}
	_ = response.Body.Close()

	response, err = client.Post("http://localhost/sessions", "application/json", bytes.NewBufferString(`{"id":"uds-session","project":"engram","directory":"/tmp/engram"}`))
	if err != nil {
		t.Fatalf("POST /sessions over Unix socket: %v", err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("POST /sessions = %d, want 201", response.StatusCode)
	}
	_ = response.Body.Close()

	if err := srv.Close(); err != nil {
		t.Fatalf("close server: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("server returned %v after close", err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still exists after close: %v", err)
	}
}

func TestUnixSocketReplacesOnlyStaleSockets(t *testing.T) {
	requireUnixSockets(t)
	socketPath := filepath.Join(t.TempDir(), "engram.sock")
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}

	srv := New(newServerTestStore(t), 0)
	srv.SetSocketPath(socketPath)
	done := startUnixSocketServer(t, srv, socketPath)
	if err := srv.Close(); err != nil {
		t.Fatalf("close replacement server: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("replacement server returned %v", err)
	}

	filePath := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(filePath, []byte("do not remove"), 0o600); err != nil {
		t.Fatalf("create ordinary file: %v", err)
	}
	srv = New(nil, 0)
	srv.SetSocketPath(filePath)
	if err := srv.Start(); err == nil {
		t.Fatal("expected regular file socket path to be rejected")
	}
	contents, err := os.ReadFile(filePath)
	if err != nil || string(contents) != "do not remove" {
		t.Fatalf("ordinary file was changed: contents=%q err=%v", contents, err)
	}
}

func TestUnixSocketCloseIsIdempotent(t *testing.T) {
	requireUnixSockets(t)
	socketPath := filepath.Join(t.TempDir(), "engram.sock")
	srv := New(newServerTestStore(t), 0)
	srv.SetSocketPath(socketPath)
	done := startUnixSocketServer(t, srv, socketPath)

	if err := srv.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("server returned %v after close", err)
	}
}
