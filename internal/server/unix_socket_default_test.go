package server

import (
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestServerDefaultsToLoopbackTCP(t *testing.T) {
	srv := New(nil, 7437)
	var network, address string
	srv.listen = func(gotNetwork, gotAddress string) (net.Listener, error) {
		network, address = gotNetwork, gotAddress
		return stubListener{}, nil
	}
	srv.serve = func(net.Listener, http.Handler) error { return errors.New("serve stopped") }
	if err := srv.Start(); err == nil || err.Error() != "serve stopped" {
		t.Fatalf("Start() error = %v, want serve stopped", err)
	}
	if network != "tcp" || address != "127.0.0.1:7437" {
		t.Fatalf("listener = %s %s, want tcp 127.0.0.1:7437", network, address)
	}
}
