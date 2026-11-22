package cloudserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// StartupListener serves a generic unavailable response until Start publishes
// a fully initialized CloudServer. The caller owns its lifetime.
type StartupListener struct {
	addr           string
	listener       net.Listener
	server         http.Server
	initialization context.Context
	mu             sync.Mutex
	ready          chan struct{}
	handler        http.Handler
}

func ListenDuringStartup(host string, port int, listen func(string, string) (net.Listener, error)) (*StartupListener, error) {
	addr := cloudListenAddress(host, port)
	listener, err := listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	startup := &StartupListener{
		addr: addr, listener: listener, initialization: ctx, ready: make(chan struct{}),
	}
	startup.server = http.Server{Handler: http.HandlerFunc(startup.serveHTTP), DisableGeneralOptionsHandler: true}
	go func() {
		err := startup.server.Serve(listener)
		startup.mu.Lock()
		cancel(err)
		startup.mu.Unlock()
	}()
	return startup, nil
}

func (s *StartupListener) Addr() net.Addr { return s.listener.Addr() }

// InitializationContext ends with the HTTP serve error when the listener stops.
func (s *StartupListener) InitializationContext() context.Context { return s.initialization }

func (s *StartupListener) Close() {
	_ = s.server.Close()
	_ = s.listener.Close()
	<-s.initialization.Done()
}

func (s *StartupListener) publish(handler http.Handler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := context.Cause(s.initialization); err != nil {
		return err
	}
	s.handler = handler
	close(s.ready)
	return nil
}

func (s *StartupListener) serveHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case <-s.ready:
		// Preserve net/http's general OPTIONS response once the server is ready.
		if r.Method == http.MethodOptions && r.RequestURI == "*" {
			w.Header().Set("Content-Length", "0")
			if r.ContentLength != 0 {
				_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 4<<10))
			}
			return
		}
		s.handler.ServeHTTP(w, r)
	default:
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "cloud service is starting", http.StatusServiceUnavailable)
	}
}

func cloudListenAddress(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = defaultHost
	}
	return fmt.Sprintf("%s:%d", host, port)
}
