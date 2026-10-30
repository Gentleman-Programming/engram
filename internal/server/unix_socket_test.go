//go:build unix

package server

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// privateUnixSocketPath avoids t.TempDir's potentially writable hierarchy and
// keeps the path short enough for Unix sockets. Never repair unsafe permissions.
func privateUnixSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "engram-uds-")
	if err != nil {
		t.Fatalf("create private socket directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove private socket directory: %v", err)
		}
	})
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("stat private socket directory: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("private socket directory mode = %v, want directory with permissions 700", info.Mode())
	}
	socketPath, err := secureUnixSocketPath(filepath.Join(dir, "engram.sock"))
	if err != nil {
		t.Fatalf("private socket fixture does not satisfy directory trust policy: %v", err)
	}
	return socketPath
}

func TestUnixSocketPrivateFixtureTrustCompatibility(t *testing.T) {
	socketPath := privateUnixSocketPath(t)
	if err := validateUnixSocketParent(filepath.Dir(socketPath)); err != nil {
		t.Fatalf("private socket hierarchy is not trusted: %v", err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture should not create the socket: %v", err)
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

func TestUnixSocketDirectoryTrustPolicy(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		mode                 os.FileMode
		ownerUID, currentUID int
		want                 bool
	}{
		{name: "private user directory", mode: 0o700, ownerUID: 1000, currentUID: 1000, want: true},
		{name: "root-owned sticky temp directory", mode: os.ModeSticky | 0o777, ownerUID: 0, currentUID: 1000, want: true},
		{name: "user-owned sticky temp directory", mode: os.ModeSticky | 0o777, ownerUID: 1000, currentUID: 1000, want: true},
		{name: "foreign-owned sticky directory", mode: os.ModeSticky | 0o777, ownerUID: 2000, currentUID: 1000, want: false},
		{name: "world-writable non-sticky directory", mode: 0o777, ownerUID: 0, currentUID: 1000, want: false},
		{name: "group-writable non-sticky directory", mode: 0o770, ownerUID: 1000, currentUID: 1000, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := trustedUnixSocketDirectory(tt.mode, tt.ownerUID, tt.currentUID); got != tt.want {
				t.Fatalf("trustedUnixSocketDirectory(%#o, %d, %d) = %t, want %t", tt.mode, tt.ownerUID, tt.currentUID, got, tt.want)
			}
		})
	}
}

func TestUnixSocketRejectsActiveSocketWithoutDisturbingListener(t *testing.T) {
	socketPath := privateUnixSocketPath(t)
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
	socketPath := privateUnixSocketPath(t)
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
	socketPath := privateUnixSocketPath(t)
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

	filePath := filepath.Join(filepath.Dir(socketPath), "not-a-socket")
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
	socketPath := privateUnixSocketPath(t)
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
