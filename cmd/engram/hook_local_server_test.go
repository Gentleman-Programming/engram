package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestHookManagedRecoveryConfirmation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows recovery only")
	}
	for _, ended := range []bool{false, true} {
		t.Run(fmt.Sprint("ended=", ended), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("ENGRAM_DATA_DIR", dir)
			t.Setenv("ENGRAM_URL", "")
			t.Setenv("ENGRAM_SOCKET", "")
			expected, err := store.EnsureInstanceID(dir)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			t.Setenv("ENGRAM_PORT", strconv.Itoa(port))
			var launches, owners, registrations, mutations atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/health":
					_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "engram", "instance_id": expected})
				case r.URL.Path == "/sessions/host":
					owners.Add(1)
					row := map[string]any{"id": "host", "project": "persisted-owner", "directory": ""}
					if ended {
						row["ended_at"] = "ended"
					}
					_ = json.NewEncoder(w).Encode(row)
				case r.URL.Path == "/sessions" && r.Method == "POST":
					registrations.Add(1)
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["project"] != "persisted-owner" {
						t.Errorf("lost persisted owner: %v", body)
					}
					w.WriteHeader(201)
					_, _ = w.Write([]byte(`{"id":"host","status":"created"}`))
				default:
					mutations.Add(1)
					w.WriteHeader(500)
				}
			}))
			_ = server.Listener.Close()
			old := hookLocalServerLaunch
			defer func() { hookLocalServerLaunch = old }()
			hookLocalServerLaunch = func(ctx context.Context, executable, log string, env []string) error {
				launches.Add(1)
				l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					return err
				}
				server.Listener = l
				server.Start()
				return nil
			}
			defer server.Close()
			effective, err := confirmHookSession("host", "untrusted-cwd", true)
			if ended {
				if err == nil || effective != "" {
					t.Fatalf("ended session permitted: %q %v", effective, err)
				}
			} else if err != nil || effective != "host" {
				t.Fatalf("recovery: %q %v", effective, err)
			}
			wantRegistrations := int32(1)
			if ended {
				wantRegistrations = 0
			}
			if launches.Load() != 1 || owners.Load() != 1 || registrations.Load() != wantRegistrations || mutations.Load() != 0 {
				t.Fatalf("launch=%d owner=%d registration=%d unexpected=%d", launches.Load(), owners.Load(), registrations.Load(), mutations.Load())
			}
		})
	}
}

func TestHookRecoveryDataDirectoryBytes(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(fmt.Sprint("override=", override), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), " meaningful directory ")
			if runtime.GOOS == "windows" {
				dir = `\\?\` + dir
			}
			oldConfig := storeDefaultConfig
			defer func() { storeDefaultConfig = oldConfig }()
			storeDefaultConfig = func() (store.Config, error) { return store.Config{DataDir: dir}, nil }
			t.Setenv("ENGRAM_DATA_DIR", " \t ")
			if override {
				t.Setenv("ENGRAM_DATA_DIR", dir)
			}
			oldLaunch := hookLocalServerLaunch
			defer func() { hookLocalServerLaunch = oldLaunch }()
			sentinel := errors.New("synthetic launch stopped")
			hookLocalServerLaunch = func(ctx context.Context, executable, log string, env []string) error {
				if log != filepath.Join(dir, "serve.err.log") {
					t.Errorf("log path lost bytes: %q", log)
				}
				found := false
				for _, entry := range env {
					if entry == "ENGRAM_DATA_DIR="+dir {
						found = true
					}
				}
				if !found {
					t.Error("child data directory lost meaningful bytes")
				}
				return sentinel
			}
			if err := recoverHookLocalServer(context.Background(), &http.Client{}, "http://127.0.0.1:7437"); !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
		})
	}
}

func TestHookLocalReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       bool
	}{
		{"matching", `{"status":"ok","service":"engram","instance_id":"expected"}`, 200, true},
		{"foreign", `{"status":"ok","service":"engram","instance_id":"other"}`, 200, false},
		{"other service", `{"status":"ok","service":"other","instance_id":"expected"}`, 200, false},
		{"malformed", `{`, 200, false}, {"HTTP", `{}`, 503, false}, {"redirect", `{}`, 302, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := hookLocalServerReady(ctx, server.Client(), server.URL, "expected"); (err == nil) != tc.want {
				t.Fatalf("readiness: %v", err)
			}
		})
	}
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := dead.URL
	dead.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := hookLocalServerReady(ctx, &http.Client{}, endpoint, "expected"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestHookRecoveryExpiredContext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ENGRAM_DATA_DIR", filepath.Join(dir, "untouched"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := recoverHookLocalServer(ctx, &http.Client{}, "http://127.0.0.1:1"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "untouched")); !os.IsNotExist(err) {
		t.Fatalf("created recovery state: %v", err)
	}
	if err := launchServeBackgroundContext(ctx, "unused", filepath.Join(dir, "log"), nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "log")); !os.IsNotExist(err) {
		t.Fatalf("opened log: %v", err)
	}
}

func TestHookRecoveryEligibility(t *testing.T) {
	t.Setenv("ENGRAM_URL", "")
	t.Setenv("ENGRAM_SOCKET", "")
	t.Setenv("ENGRAM_PORT", "7437")
	endpoint := "http://127.0.0.1:7437/sessions/host"
	refusal := func(endpoint string, cause error) error {
		return &url.Error{Op: "Get", URL: endpoint, Err: &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7437}, Err: cause}}
	}
	refused := hookConnectionRefused
	for _, tc := range []struct {
		name, platform, explicitURL, socket, endpoint string
		cause                                         error
		want                                          bool
	}{
		{name: "direct", platform: "windows", endpoint: endpoint, cause: refusal(endpoint, refused), want: true},
		{name: "non Windows", platform: "linux", endpoint: endpoint, cause: refusal(endpoint, refused)},
		{name: "explicit URL", platform: "windows", explicitURL: "http://127.0.0.1:7437", endpoint: endpoint, cause: refusal(endpoint, refused)},
		{name: "socket", platform: "windows", socket: "synthetic", endpoint: endpoint, cause: refusal(endpoint, refused)},
		{name: "redirect path", platform: "windows", endpoint: endpoint, cause: refusal("http://127.0.0.1:7437/other", refused)},
		{name: "redirect host", platform: "windows", endpoint: endpoint, cause: refusal("http://127.0.0.1:7438/sessions/host", refused)},
		{name: "cancel", platform: "windows", endpoint: endpoint, cause: refusal(endpoint, context.Canceled)},
		{name: "timeout", platform: "windows", endpoint: endpoint, cause: refusal(endpoint, context.DeadlineExceeded)},
		{name: "other transport", platform: "windows", endpoint: endpoint, cause: refusal(endpoint, errors.New("failure"))},
		{name: "HTTP or JSON", platform: "windows", endpoint: endpoint},
		{name: "different dial target", platform: "windows", endpoint: endpoint, cause: &url.Error{Op: "Get", URL: endpoint, Err: &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 7437}, Err: refused}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_URL", tc.explicitURL)
			t.Setenv("ENGRAM_SOCKET", tc.socket)
			if got := hookLocalRecoveryEligible(tc.platform, "http://127.0.0.1:7437", tc.endpoint, tc.cause); got != tc.want {
				t.Fatalf("eligible=%v want %v", got, tc.want)
			}
		})
	}
}

func TestHookLookupCauseAndRedirect(t *testing.T) {
	t.Setenv("ENGRAM_URL", "")
	t.Setenv("ENGRAM_SOCKET", "")
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := dead.URL
	dead.Close()
	_, _, err := hookSessionOwnerProject(context.Background(), &http.Client{}, endpoint, "host")
	var transport *url.Error
	if err == nil || err.Error() != "host session lookup failed" || !errors.As(err, &transport) {
		t.Fatalf("original cause missing: %v", err)
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, endpoint+r.URL.Path, http.StatusFound)
	}))
	defer redirect.Close()
	_, _, err = hookSessionOwnerProject(context.Background(), &http.Client{}, redirect.URL, "host")
	var denial *hookDenialError
	if err == nil || !errors.As(err, &transport) || !errors.As(err, &denial) || !denial.redirected {
		t.Fatalf("redirect cause/classification missing: %v", err)
	}
}
