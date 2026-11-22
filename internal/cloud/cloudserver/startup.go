package cloudserver

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// StartupListener serves a generic unavailable response until Start publishes
// a fully initialized CloudServer. The caller owns its lifetime.
type StartupListener struct {
	addr     string
	listener net.Listener
	server   http.Server
	ready    chan struct{}
	handler  http.Handler
	done     chan struct{}
	err      error
}

func ListenDuringStartup(host string, port int) (*StartupListener, error) {
	addr := cloudListenAddress(host, port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	startup := &StartupListener{
		addr: addr, listener: listener, ready: make(chan struct{}), done: make(chan struct{}),
	}
	startup.server = http.Server{Handler: http.HandlerFunc(startup.serveHTTP), DisableGeneralOptionsHandler: true}
	go func() {
		startup.err = startup.server.Serve(listener)
		close(startup.done)
	}()
	return startup, nil
}

func (s *StartupListener) Addr() net.Addr { return s.listener.Addr() }

func (s *StartupListener) Close() {
	_ = s.server.Close()
	_ = s.listener.Close()
	<-s.done
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
