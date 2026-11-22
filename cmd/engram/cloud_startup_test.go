package main

import (
	"context"
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
	listenCloudDuringStartup = func(network, _ string) (net.Listener, error) {
		return net.Listen(network, "127.0.0.1:0")
	}
}

func stubCloudStartupRuntime(t *testing.T, build func(context.Context, cloud.Config) (cloudServerRuntime, error)) {
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
	opened := make(chan net.Listener, 1)
	originalListen := listenCloudDuringStartup
	t.Cleanup(func() { listenCloudDuringStartup = originalListen })
	listenCloudDuringStartup = func(network, addr string) (net.Listener, error) {
		startup, err := net.Listen(network, addr)
		opened <- startup
		return startup, err
	}

	auth := &cloudStartupTestAuthenticator{}
	building := make(chan struct{})
	release := make(chan struct{})
	stubCloudStartupRuntime(t, func(_ context.Context, cfg cloud.Config) (cloudServerRuntime, error) {
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

	var startup net.Listener
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
		_ = startup.Close()
		if err := <-done; !errors.Is(err, net.ErrClosed) {
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
		defer func() { _ = res.Body.Close() }()
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
	defer func() { _ = listener.Close() }()
	cfg := cloud.DefaultConfig()
	cfg.Port = listener.Addr().(*net.TCPAddr).Port
	stubCloudStartupRuntime(t, func(context.Context, cloud.Config) (cloudServerRuntime, error) {
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
	listenCloudDuringStartup = func(network, address string) (net.Listener, error) {
		startup, err := net.Listen(network, address)
		if err == nil {
			addr = startup.Addr().String()
		}
		return startup, err
	}
	want := errors.New("startup repair failed")
	stubCloudStartupRuntime(t, func(context.Context, cloud.Config) (cloudServerRuntime, error) {
		client := &http.Client{Timeout: 2 * time.Second}
		res, err := client.Get("http://" + addr + "/health")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
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
	defer func() { _ = listener.Close() }()
}

// startupFailureListener accepts one HTTP connection, then waits for the
// test to inject a permanent accept error while the initializer is active.
type startupFailureListener struct {
	net.Listener
	accepted bool
	fail     <-chan struct{}
	err      error
}

func (l *startupFailureListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.Listener.Accept()
	}
	<-l.fail
	return nil, l.err
}

func TestRunCloudServeCancelsInitializationOnAcceptFailure(t *testing.T) {
	cfg := cloud.DefaultConfig()
	cfg.Port = 0
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	var failOnce, cleanupOnce sync.Once
	acceptFailed := make(chan struct{})
	unblockCleanup := make(chan struct{})
	failAccept := func() { failOnce.Do(func() { close(acceptFailed) }) }
	finishCleanup := func() { cleanupOnce.Do(func() { close(unblockCleanup) }) }
	want := errors.New("owned startup listener failed to accept")
	originalListen := listenCloudDuringStartup
	t.Cleanup(func() { listenCloudDuringStartup = originalListen })
	listenCloudDuringStartup = func(string, string) (net.Listener, error) {
		return &startupFailureListener{Listener: listener, fail: acceptFailed, err: want}, nil
	}

	initStarted := make(chan struct{})
	canceled := make(chan error, 1)
	stubCloudStartupRuntime(t, func(ctx context.Context, _ cloud.Config) (cloudServerRuntime, error) {
		close(initStarted)
		<-ctx.Done()
		canceled <- context.Cause(ctx)
		<-unblockCleanup
		return nil, ctx.Err()
	})
	done := make(chan struct{})
	var serveErr error
	go func() {
		defer close(done)
		serveErr = runCloudServe(cfg)
	}()
	t.Cleanup(func() {
		failAccept()
		finishCleanup()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("startup runner did not finish during cleanup")
		}
	})
	select {
	case <-initStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime init did not start")
	}

	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	address := "http://" + listener.Addr().String() + "/health"
	checkStarting := func() {
		t.Helper()
		res, err := client.Get(address)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		if _, err := io.Copy(io.Discard, res.Body); err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("startup health returned %d", res.StatusCode)
		}
	}
	checkStarting()
	failAccept()
	select {
	case err := <-canceled:
		if !errors.Is(err, want) {
			t.Fatalf("initializer cancellation cause = %v, want %v", err, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("accept failure did not cancel runtime initialization")
	}
	select {
	case <-done:
		t.Fatal("startup returned before initializer cleanup")
	default:
	}
	checkStarting()

	finishCleanup()
	select {
	case <-done:
		if !errors.Is(serveErr, want) {
			t.Fatalf("startup returned %v, want accept error %v", serveErr, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("startup did not return after initializer cleanup")
	}
	rebound, err := net.Listen("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("listener not released after accept failure: %v", err)
	}
	_ = rebound.Close()
}
