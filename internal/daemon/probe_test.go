package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeReportsRunningOnHealthyServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	res := Probe(context.Background(), port, time.Second)
	if res.Status != StatusRunning {
		t.Fatalf("probe status = %q, want running (err=%v)", res.Status, res.Err)
	}
	if res.Port != port {
		t.Fatalf("probe port = %d, want %d", res.Port, port)
	}
}

func TestProbeReportsNotRunningOnRefusedDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	res := Probe(context.Background(), port, time.Second)
	if res.Status != StatusNotRunning {
		t.Fatalf("probe status = %q, want not_running (err=%v)", res.Status, res.Err)
	}
	if res.Err == nil {
		t.Fatal("probe err = nil, want dial error")
	}
}

func TestProbeReadsDaemonTokenFromHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok","service":"engram","instance_id":"x","daemon_token":"tok-123"}`)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	res := Probe(context.Background(), port, time.Second)
	if res.Status != StatusRunning || res.Token != "tok-123" || res.Service != "engram" {
		t.Fatalf("probe = status %q token %q, want running with token tok-123", res.Status, res.Token)
	}
}

func TestProbeTokenEmptyWithoutDaemonIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok","service":"engram","instance_id":"x"}`)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	res := Probe(context.Background(), port, time.Second)
	if res.Status != StatusRunning || res.Token != "" {
		t.Fatalf("probe = status %q token %q, want running with an empty token", res.Status, res.Token)
	}
}

func TestProbeDoesNotFollowRedirectedIdentity(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		_, _ = fmt.Fprint(w, `{"service":"engram","daemon_token":"unrelated-token","version":"3.0.0"}`)
	}))
	defer target.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/health", status)
			}))
			defer redirect.Close()
			port := redirect.Listener.Addr().(*net.TCPAddr).Port
			result := Probe(context.Background(), port, time.Second)
			if result.Status != StatusUnreachable || result.Port != port || result.Token != "" || result.Service != "" || result.Version != "" {
				t.Fatalf("redirect must not substitute another endpoint's identity: %+v", result)
			}
		})
	}
	if calls := targetCalls.Load(); calls != 0 {
		t.Fatalf("redirect destination received %d requests", calls)
	}
}
