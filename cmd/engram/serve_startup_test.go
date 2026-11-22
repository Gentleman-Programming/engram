package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestLocalServeStartupRespondsBeforeStoreReady(t *testing.T) {
	stubRuntimeHooks(t)
	cfg := testConfig(t)
	t.Setenv("ENGRAM_SOCKET", "")
	t.Setenv("ENGRAM_RUN_READY_FILE", "")
	t.Setenv("ENGRAM_RUN_READY_TOKEN", "")
	t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "0")
	t.Setenv("ENGRAM_HTTP_TOKEN", "fixture-local-startup-token")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	oldNotify, oldStop := notifySignals, stopSignals
	t.Cleanup(func() { notifySignals, stopSignals = oldNotify, oldStop })
	signals := make(chan chan<- os.Signal, 1)
	notifySignals = func(c chan<- os.Signal, _ ...os.Signal) { signals <- c }
	stopSignals = func(chan<- os.Signal) {}

	opening := make(chan struct{})
	allowOpen := make(chan struct{})
	releaseStore := sync.OnceFunc(func() { close(allowOpen) })
	oldOpen := storeNewForLocalStartup
	t.Cleanup(func() { storeNewForLocalStartup = oldOpen })
	storeNewForLocalStartup = func(ctx context.Context, cfg store.Config) (*store.Store, error) {
		close(opening)
		<-allowOpen
		return store.NewWithStartupContext(ctx, cfg)
	}
	newHTTPServer = server.New
	listenLocalStartup = server.ListenLocalStartup
	startHTTP = (*server.Server).Start
	finished := make(chan error, 1)
	go func() { finished <- serveWith(cfg, serveOptions{port: port}) }()
	defer func() {
		releaseStore()
		select {
		case c := <-signals:
			c <- os.Interrupt
		case err := <-finished:
			finished <- err
		case <-time.After(5 * time.Second):
			t.Error("serve failed to register signals")
			return
		}
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("serve failed to stop")
		}
	}()
	select {
	case <-opening:
	case <-time.After(5 * time.Second):
		t.Fatal("store initialization did not start")
	}

	transport := &http.Transport{MaxConnsPerHost: 1}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	query := func(path, token string) (int, map[string]any, net.Conn) {
		t.Helper()
		var conn net.Conn
		ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { conn = info.Conn },
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, payload, conn
	}
	status, health, before := query("/health", "")
	if status != http.StatusServiceUnavailable || health["status"] != "starting" || health["service"] != "engram" || health["instance_id"] != nil {
		t.Fatalf("pre-ready health: %d, %v", status, health)
	}
	for _, path := range []string{"/compatibility", "/observations"} {
		if status, _, _ := query(path, ""); status != http.StatusServiceUnavailable {
			t.Fatalf("pre-ready %s: %d", path, status)
		}
	}
	contenderStore, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = contenderStore.Close() }()
	if err := server.New(contenderStore, port).Start(); err == nil || !strings.Contains(err.Error(), "server is starting on the port") {
		t.Fatalf("overlapping bind: %v", err)
	}

	releaseStore()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, health, after := query("/health", "")
		if status == http.StatusOK {
			if health["status"] != "ok" || health["instance_id"] == nil || before != after {
				t.Fatalf("ready health: %d, %v, connection reused: %t", status, health, before == after)
			}
			if status, _, _ := query("/compatibility", ""); status != http.StatusUnauthorized {
				t.Fatalf("unauthed ready compatibility: %d", status)
			}
			status, compatibility, _ := query("/compatibility", "fixture-local-startup-token")
			if status != http.StatusOK || compatibility["active_reuse_authorized"] != true || compatibility["instance_id"] != health["instance_id"] {
				t.Fatalf("ready compatibility: %d, %v", status, compatibility)
			}
			return
		}
		if status != http.StatusServiceUnavailable {
			t.Fatalf("unexpected status during startup: %d", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
}
