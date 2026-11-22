package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
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

			srv := New(newServerTestStore(t), 0)
			cancelHTTPDuringGit(t, srv.Handler(), http.MethodGet, "/project/current?cwd="+url.QueryEscape(repo), nil)
		})
	}
}

func cancelHTTPDuringGit(t *testing.T, handler http.Handler, method, path string, body io.Reader) {
	t.Helper()
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
	req, err := http.NewRequestWithContext(ctx, method, server.URL+path, body)
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
		t.Error("HTTP handler kept waiting for Git after cancellation")
		// Let a regressing handler finish so callers can observe late writes.
		_ = gate.Close()
		_ = blockedGit.Close()
		select {
		case <-done:
			return
		case <-time.After(4 * time.Second):
			t.Fatal("HTTP handler did not finish after fixture released Git")
		}
	}
	if err := blockedGit.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	// A killed peer may give EOF or a TCP reset depending on the OS.
	if n, err := blockedGit.Read(make([]byte, 1)); n != 0 || err == nil || os.IsTimeout(err) {
		t.Fatalf("blocked Git process was not closed by cancellation: read %d bytes, %v", n, err)
	}
}

func TestHTTPSessionRegistrationCancelsActiveGitBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	installCurrentProjectGitFixture(t, dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ENGRAM_HTTP_TOKEN", "")

	for _, existing := range []bool{false, true} {
		name := "new session"
		if existing {
			name = "existing session"
		}
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			st := newServerTestStore(t)
			const id, project = "session-fixture", "project-fixture"
			var before *store.Session
			if existing {
				if err := st.StartSession(id, project, repo); err != nil {
					t.Fatal(err)
				}
				if _, err := st.DB().Exec("UPDATE sessions SET runtime_lease_expires_at = ? WHERE id = ?", "2001-02-03 04:05:06", id); err != nil {
					t.Fatal(err)
				}
				var err error
				before, err = st.GetSession(id)
				if err != nil {
					t.Fatal(err)
				}
			}

			srv := New(st, 0)
			var notified atomic.Int64
			srv.SetOnWrite(func() { notified.Add(1) })
			body, err := json.Marshal(map[string]string{"id": id, "project": project, "directory": repo})
			if err != nil {
				t.Fatal(err)
			}
			cancelHTTPDuringGit(t, srv.Handler(), http.MethodPost, "/sessions", bytes.NewReader(body))

			after, err := st.GetSession(id)
			if existing {
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Errorf("canceled registration changed the session: before=%+v after=%+v error=%v", before, after, err)
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("canceled registration unexpectedly created a session: %+v, %v", after, err)
			}
			if writes := notified.Load(); writes != 0 {
				t.Errorf("canceled registration published %d write notifications", writes)
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
