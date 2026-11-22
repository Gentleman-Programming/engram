package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"golang.org/x/sys/windows"
)

func TestHookManagedWindowsConfirmationWithIdentityLockHeld(t *testing.T) {
	for _, tc := range []struct {
		name      string
		marker    string
		wantPaths string
	}{
		{name: "matching", wantPaths: "GET /health,GET /sessions/live-host,POST /sessions"},
		{name: "foreign", wantPaths: "GET /health"},
		{name: "malformed", marker: "incomplete"},
		{name: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "store")
			db, err := store.New(store.FallbackConfig(dir))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			const sessionID = "live-host"
			if err := db.StartSessionWithOwnershipMode(sessionID, "owner", root, store.SessionOwnershipProjectOwned); err != nil {
				t.Fatal(err)
			}
			_, readerErr := store.ReadPublishedInstanceID(dir)
			fallbackOnly := errors.Is(readerErr, errors.ErrUnsupported)
			if readerErr != nil && !fallbackOnly {
				t.Fatal(readerErr)
			}

			holdHookWindowsIdentityLock(t, dir)
			markerPath := filepath.Join(dir, ".instance-id")
			marker := tc.marker
			if tc.name == "foreign" {
				marker = "a" + db.InstanceID()[1:]
				if marker == db.InstanceID() {
					marker = "b" + marker[1:]
				}
			}
			switch tc.name {
			case "foreign", "malformed":
				if err := os.WriteFile(markerPath, []byte(marker), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(markerPath); err != nil {
					t.Fatal(err)
				}
			}
			var paths []string
			production := server.New(db, 0).Handler()
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.Method+" "+r.URL.Path)
				production.ServeHTTP(w, r)
			}))
			defer endpoint.Close()
			port, ok := strings.CutPrefix(endpoint.URL, "http://127.0.0.1:")
			if !ok {
				t.Fatalf("listener is outside the managed loopback route: %s", endpoint.URL)
			}
			t.Setenv("ENGRAM_DATA_DIR", dir)
			t.Setenv("ENGRAM_URL", "")
			t.Setenv("ENGRAM_SOCKET", "")
			t.Setenv("ENGRAM_PORT", port)

			id, err := confirmHookSessionWithin(sessionID, root, true, "windows", time.Second)
			endpoint.Close()
			wantPaths := tc.wantPaths
			switch {
			case !fallbackOnly && tc.name == "matching":
				if err != nil || id != sessionID {
					t.Errorf("matching ready listener = %q, %v", id, err)
				}
			case !fallbackOnly && tc.name == "foreign":
				if id != "" || err == nil || !strings.Contains(err.Error(), "identity unconfirmed") {
					t.Errorf("foreign ready listener = %q, %v", id, err)
				}
			default:
				wantPaths = ""
				if id != "" || !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("identity initialization under lock = %q, %v", id, err)
				}
			}
			if got := strings.Join(paths, ","); got != wantPaths {
				t.Errorf("requests = %q, want %q", got, wantPaths)
			}
		})
	}
}

func holdHookWindowsIdentityLock(t *testing.T, dir string) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dir, ".instance-id.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	handle := windows.Handle(file.Fd())
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, new(windows.Overlapped)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.UnlockFileEx(handle, 0, 1, 0, new(windows.Overlapped)); err != nil {
			t.Error(err)
		}
	})
}
