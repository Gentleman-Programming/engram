package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/cloud/cloudserver"
)

func stubCloudStartupPort(t *testing.T) {
	t.Helper()
	original := listenCloudDuringStartup
	t.Cleanup(func() { listenCloudDuringStartup = original })
	listenCloudDuringStartup = func(_ string, _ int) (*cloudserver.StartupListener, error) {
		return cloudserver.ListenDuringStartup("127.0.0.1", 0)
	}
}

func stubCloudStartupRuntime(t *testing.T, build func(cloud.Config) (cloudServerRuntime, error)) {
	t.Helper()
	original := newCloudRuntime
	t.Cleanup(func() { newCloudRuntime = original })
	newCloudRuntime = build
}

type cloudStartupTestAuthenticator struct{ calls atomic.Int64 }

func (a *cloudStartupTestAuthenticator) Authorize(*http.Request) error {
	a.calls.Add(1)
	return errors.New("unauthorized")
}

func TestRunCloudServeGatesRoutesUntilRuntimeIsReady(t *testing.T) {
	cfg := cloud.DefaultConfig()
	cfg.Port = 0
	opened := make(chan *cloudserver.StartupListener, 1)
	originalListen := listenCloudDuringStartup
	t.Cleanup(func() { listenCloudDuringStartup = originalListen })
	listenCloudDuringStartup = func(host string, port int) (*cloudserver.StartupListener, error) {
		startup, err := cloudserver.ListenDuringStartup(host, port)
		opened <- startup
		return startup, err
	}

	auth := &cloudStartupTestAuthenticator{}
	building := make(chan struct{})
	release := make(chan struct{})
	stubCloudStartupRuntime(t, func(cfg cloud.Config) (cloudServerRuntime, error) {
		close(building)
		<-release
		return &defaultCloudRuntime{
			server: cloudserver.New(nil, auth, cfg.Port, cloudserver.WithHost(cfg.BindHost)),
		}, nil
	})
	done := make(chan error, 1)
	go func() { done <- runCloudServe(cfg) }()
	var releaseOnce sync.Once
	allowBuild := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(allowBuild)

	var startup *cloudserver.StartupListener
	select {
	case startup = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not open its listener")
	}
	if startup == nil {
		t.Fatalf("listener failed: %v", <-done)
	}
	t.Cleanup(func() {
		allowBuild()
		startup.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve failure: %v", err)
		}
	})
	select {
	case <-building:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime initialization did not start")
	}

	base := "http://" + startup.Addr().String()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 2 * time.Second, Transport: transport}
	var lastConn net.Conn
	inspect := func(method, path string, generalOptions bool) (string, http.Header, int) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if generalOptions {
			req.URL = &url.URL{Scheme: "http", Host: startup.Addr().String(), Path: "*"}
		}
		req.Header.Set("Authorization", "Bearer fixture-secret")
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { lastConn = info.Conn }}
		res, err := client.Do(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(body), res.Header, res.StatusCode
	}

	for _, route := range []struct {
		method, path   string
		generalOptions bool
	}{
		{http.MethodGet, "/health", false},
		{http.MethodGet, "/dashboard/login", false},
		{http.MethodGet, "/admin/users", false},
		{http.MethodGet, "/sync/pull?project=secret", false},
		{http.MethodPost, "/sync/push", false},
		{http.MethodGet, "/nonexistent", false},
		{http.MethodOptions, "", true},
	} {
		body, headers, status := inspect(route.method, route.path, route.generalOptions)
		if status != http.StatusServiceUnavailable || headers.Get("Cache-Control") != "no-store" || body != "cloud service is starting\n" {
			t.Errorf("%s %s got %d, headers %v and body %q", route.method, route.path, status, headers, body)
		}
	}
	if calls := auth.calls.Load(); calls != 0 {
		t.Fatalf("initializing runtime called authenticator %d times", calls)
	}

	startupConn := lastConn
	allowBuild()
	deadline := time.Now().Add(5 * time.Second)
	for {
		body, _, status := inspect(http.MethodGet, "/health", false)
		if status == http.StatusOK && strings.Contains(body, `"service":"engram-cloud"`) {
			if startupConn == nil || startupConn != lastConn {
				t.Fatal("health did not cross readiness on the same TCP connection")
			}
			break
		}
		if status != http.StatusServiceUnavailable || time.Now().After(deadline) {
			t.Fatalf("same listener did not become healthy: %d %s", status, body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, route := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/dashboard/login", http.StatusOK},
		{http.MethodGet, "/admin/users", http.StatusUnauthorized},
		{http.MethodGet, "/sync/pull?project=secret", http.StatusUnauthorized},
		{http.MethodPost, "/sync/push", http.StatusUnauthorized},
		{http.MethodGet, "/nonexistent", http.StatusNotFound},
	} {
		if _, _, status := inspect(route.method, route.path, false); status != route.want {
			t.Errorf("%s %s got %d, want %d", route.method, route.path, status, route.want)
		}
	}
	if body, _, status := inspect(http.MethodOptions, "", true); status != http.StatusOK || body != "" {
		t.Errorf("ready OPTIONS * got %d %q", status, body)
	}
}

func TestRunCloudServeBindFailureDoesNotInitialize(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := cloud.DefaultConfig()
	cfg.Port = listener.Addr().(*net.TCPAddr).Port
	stubCloudStartupRuntime(t, func(cloud.Config) (cloudServerRuntime, error) {
		t.Fatal("runtime initialized despite bind failure")
		return nil, nil
	})
	var bindErr *net.OpError
	if err := runCloudServe(cfg); !errors.As(err, &bindErr) || bindErr.Op != "listen" {
		t.Fatalf("startup returned %v, want listen failure", err)
	}
}

func TestRunCloudServeClosesListenerOnInitializationFailure(t *testing.T) {
	cfg := cloud.DefaultConfig()
	cfg.Port = 0
	var addr string
	originalListen := listenCloudDuringStartup
	t.Cleanup(func() { listenCloudDuringStartup = originalListen })
	listenCloudDuringStartup = func(host string, port int) (*cloudserver.StartupListener, error) {
		startup, err := cloudserver.ListenDuringStartup(host, port)
		if err == nil {
			addr = startup.Addr().String()
		}
		return startup, err
	}
	want := errors.New("startup repair failed")
	stubCloudStartupRuntime(t, func(cloud.Config) (cloudServerRuntime, error) {
		client := &http.Client{Timeout: 2 * time.Second}
		res, err := client.Get("http://" + addr + "/health")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("starting health returned %d", res.StatusCode)
		}
		return nil, want
	})
	if err := runCloudServe(cfg); !errors.Is(err, want) {
		t.Fatalf("startup returned %v, want %v", err, want)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("cannot rebind startup address %s: %v", addr, err)
	}
	defer listener.Close()
}
