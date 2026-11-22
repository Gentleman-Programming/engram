package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
)

// LocalStartup keeps one loopback listener from initial startup through serving.
// The command owns it until the store and its HTTP handler are ready.
type LocalStartup struct {
	listener *localStartupListener
	http     *http.Server
	ready    atomic.Pointer[http.Handler]
	ctx      context.Context
}

// net/http closes its listener before Serve reports an accept failure. Fence
// handler publication at that close, including for clients already connected.
type localStartupListener struct {
	net.Listener
	mu     sync.Mutex
	closed bool
}

func (l *localStartupListener) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return l.Listener.Close()
}

func ListenLocalStartup(port int) (*LocalStartup, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}

	ctx, stop := context.WithCancelCause(context.Background())
	startup := &LocalStartup{listener: &localStartupListener{Listener: listener}, ctx: ctx}
	startup.http = &http.Server{Handler: http.HandlerFunc(startup.serveHTTP)}
	go func() {
		stop(startup.http.Serve(startup.listener))
	}()
	return startup, nil
}

// Context ends with the serve error when the early listener stops.
func (s *LocalStartup) Context() context.Context { return s.ctx }

func (s *LocalStartup) Close() {
	_ = s.http.Close()
	_ = s.listener.Close()
	<-s.ctx.Done()
}

func (s *LocalStartup) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if ready := s.ready.Load(); ready != nil {
		(*ready).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"status": "starting", "service": "engram"})
}

// UseLocalStartup hands the established loopback listener to this server.
// Call it after configuring the server and before Start.
func (s *Server) UseLocalStartup(startup *LocalStartup) {
	s.startup = startup
	s.httpServer = startup.http
	s.serve = func(_ net.Listener, handler http.Handler) error {
		startup.publish(handler)
		<-startup.ctx.Done()
		return context.Cause(startup.ctx)
	}
}

func (s *LocalStartup) publish(handler http.Handler) {
	s.listener.mu.Lock()
	defer s.listener.mu.Unlock()
	if !s.listener.closed {
		s.ready.Store(&handler)
	}
}
