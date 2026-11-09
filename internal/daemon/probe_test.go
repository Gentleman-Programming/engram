package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
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
	if res.Status != StatusRunning || res.Token != "tok-123" {
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
