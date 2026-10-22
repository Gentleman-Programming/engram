package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHookManagedRecoveryConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, platform                                  string
		ended, launchFailure, retryFailure, expireRetry bool
		initialFailure                                  bool
	}{
		{name: "live session"},
		{name: "ended session", ended: true},
		{name: "recovery failure", launchFailure: true},
		{name: "retry lookup failure", retryFailure: true},
		{name: "context expiry after recovery", expireRetry: true},
		{name: "initial HTTP failure never launches", initialFailure: true},
		{name: "non-Windows never launches", platform: "linux"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platform := tc.platform
			if platform == "" {
				platform = "windows"
			}
			dir := t.TempDir()
			t.Setenv("ENGRAM_DATA_DIR", dir)
			t.Setenv("ENGRAM_URL", "")
			t.Setenv("ENGRAM_SOCKET", "")
			t.Setenv("ENGRAM_HOOK_SESSION_CONFIRMATION_TIMEOUT", "2s")
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
			var launches, health, owners, registrations, mutations atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/health":
					health.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "engram", "instance_id": expected})
				case r.URL.Path == "/sessions/host":
					owners.Add(1)
					if tc.initialFailure || tc.retryFailure {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if tc.expireRetry {
						<-r.Context().Done()
						return
					}
					row := map[string]any{"id": "host", "project": "persisted-owner", "directory": ""}
					if tc.ended {
						row["ended_at"] = "ended"
					}
					_ = json.NewEncoder(w).Encode(row)
				case r.URL.Path == "/sessions" && r.Method == "POST":
					registrations.Add(1)
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["project"] != "persisted-owner" || body["id"] != "host" {
						t.Errorf("lost persisted identity: %v", body)
					}
					w.WriteHeader(201)
					_, _ = w.Write([]byte(`{"id":"host","status":"created"}`))
				default:
					mutations.Add(1)
					w.WriteHeader(500)
				}
			}))
			_ = server.Listener.Close()
			defer server.Close()
			startServer := func() error {
				l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					return err
				}
				server.Listener = l
				server.Start()
				return nil
			}
			old := hookLocalServerLaunch
			defer func() { hookLocalServerLaunch = old }()
			hookLocalServerLaunch = func(context.Context, string, string, []string) error {
				launches.Add(1)
				if tc.launchFailure {
					return errors.New("synthetic launch failure")
				}
				return startServer()
			}
			if tc.initialFailure {
				if err := startServer(); err != nil {
					t.Fatal(err)
				}
			}
			effective, err := confirmHookSessionForPlatform("host", "untrusted-cwd", true, platform)
			wantSuccess := tc.name == "live session"
			if (err == nil) != wantSuccess || (wantSuccess && effective != "host") || (!wantSuccess && effective != "") {
				t.Fatalf("confirmation: effective=%q error=%v want success=%v", effective, err, wantSuccess)
			}
			if tc.expireRetry && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("confirmation lost deadline: %v", err)
			}
			wantLaunches, wantHealth, wantOwners, wantRegistrations := int32(1), int32(1), int32(1), int32(0)
			if tc.launchFailure {
				wantHealth, wantOwners = 0, 0
			}
			if tc.initialFailure || platform != "windows" {
				wantLaunches, wantHealth = 0, 0
			}
			if platform != "windows" {
				wantOwners = 0
			}
			if wantSuccess {
				wantRegistrations = 1
			}
			if launches.Load() != wantLaunches || health.Load() != wantHealth || owners.Load() != wantOwners || registrations.Load() != wantRegistrations || mutations.Load() != 0 {
				t.Fatalf("launch=%d health=%d owner=%d registration=%d unexpected=%d; want %d/%d/%d/%d/0", launches.Load(), health.Load(), owners.Load(), registrations.Load(), mutations.Load(), wantLaunches, wantHealth, wantOwners, wantRegistrations)
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
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := hookLocalServerReady(ctx, server.Client(), server.URL, "expected"); (err == nil) != tc.want {
				t.Fatalf("readiness: %v", err)
			}
			if requests.Load() != 1 {
				t.Fatalf("non-500 response retried: %d", requests.Load())
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

type hookHealthTestBody struct {
	io.Reader
	closed bool
}

func (b *hookHealthTestBody) Close() error {
	b.closed = true
	return nil
}

func TestHookLocalReadinessTransient500(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       bool
	}{
		{"matching identity", `{"status":"ok","service":"engram","instance_id":"expected"}`, 200, true},
		{"foreign identity", `{"status":"ok","service":"engram","instance_id":"other"}`, 200, false},
		{"other HTTP status", `{}`, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bodies []*hookHealthTestBody
			client := &http.Client{Transport: codexRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Path != "/health" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
				status, body := 500, "temporary stats failure"
				if len(bodies) != 0 {
					status, body = tc.status, tc.body
				}
				b := &hookHealthTestBody{Reader: strings.NewReader(body)}
				bodies = append(bodies, b)
				return &http.Response{StatusCode: status, Body: b, Header: make(http.Header), Request: r}, nil
			})}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := hookLocalServerReady(ctx, client, "http://127.0.0.1:7437", "expected")
			if (err == nil) != tc.want || len(bodies) != 2 {
				t.Fatalf("readiness=%v requests=%d want success=%v requests=2", err, len(bodies), tc.want)
			}
			for i, body := range bodies {
				if !body.closed {
					t.Errorf("response %d not closed", i)
				}
			}
		})
	}
}

func TestHookLocalReadinessPersistent500Deadline(t *testing.T) {
	var bodies []*hookHealthTestBody
	client := &http.Client{Transport: codexRoundTripper(func(r *http.Request) (*http.Response, error) {
		b := &hookHealthTestBody{Reader: strings.NewReader("temporary stats failure")}
		bodies = append(bodies, b)
		return &http.Response{StatusCode: 500, Body: b, Header: make(http.Header), Request: r}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := hookLocalServerReady(ctx, client, "http://127.0.0.1:7437", "expected"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("persistent 500: %v", err)
	}
	if len(bodies) < 2 {
		t.Fatalf("500 was not retried: %d requests", len(bodies))
	}
	for i, body := range bodies {
		if !body.closed {
			t.Errorf("response %d not closed", i)
		}
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
