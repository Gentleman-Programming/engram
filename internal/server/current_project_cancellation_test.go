package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const currentProjectGitFixture = "ENGRAM_TEST_CURRENT_PROJECT_GIT_ADDRESS"

func TestHTTPCurrentProjectCancelsActiveGit(t *testing.T) {
	dir := t.TempDir()
	installCurrentProjectGitFixture(t, dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ENGRAM_HTTP_TOKEN", "")

	for _, tc := range []struct{ name, project string }{
		{"Git detection", ""},
		{"process override org inspection", "operator-project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_PROJECT", tc.project)
			repo := t.TempDir()
			if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}

			handler := New(newServerTestStore(t), 0).Handler()
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()

			gate, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = gate.Close() }()
			// Keep the entire assertion inside Git's ordinary two-second timeout,
			// so a root subprocess timing out on its own cannot satisfy it.
			deadline := time.Now().Add(1500 * time.Millisecond)
			if err := gate.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			t.Setenv(currentProjectGitFixture, gate.Addr().String())

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/project/current?cwd="+url.QueryEscape(repo), nil)
			if err != nil {
				t.Fatal(err)
			}
			clientDone := make(chan error, 1)
			go func() {
				res, err := server.Client().Do(req)
				if res != nil {
					_ = res.Body.Close()
				}
				clientDone <- err
			}()

			// Accepting the fixture connection proves Git has started. Closing
			// that connection would release it, so leave it open until assertions finish.
			blockedGit, err := gate.AcceptTCP()
			if err != nil {
				t.Fatalf("Git fixture did not start: %v", err)
			}
			defer func() { _ = blockedGit.Close() }()
			cancel()
			timeout := time.NewTimer(time.Until(deadline))
			defer timeout.Stop()
			select {
			case err := <-clientDone:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("HTTP client returned %v, want cancellation", err)
				}
			case <-timeout.C:
				t.Fatal("HTTP client did not finish after cancellation")
			}
			select {
			case <-done:
			case <-timeout.C:
				t.Fatal("current-project handler kept waiting for Git after cancellation")
			}
			if err := blockedGit.SetReadDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			// A killed peer may give EOF or a TCP reset depending on the OS.
			if n, err := blockedGit.Read(make([]byte, 1)); n != 0 || err == nil || os.IsTimeout(err) {
				t.Fatalf("blocked Git process was not closed by cancellation: read %d bytes, %v", n, err)
			}
		})
	}
}

func installCurrentProjectGitFixture(t *testing.T, dir string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	if err := errors.Join(copyErr, dst.Close()); err != nil {
		t.Fatal(err)
	}
}

// waitForCurrentProjectGitRelease runs only in the copied test executable.
func waitForCurrentProjectGitRelease(address string) {
	conn, err := net.DialTimeout("tcp4", address, time.Second)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err == nil {
		_, _ = conn.Read(make([]byte, 1))
	}
}
