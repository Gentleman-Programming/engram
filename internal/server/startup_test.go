package server

import (
	"context"
	"net"
	"net/http"
	"testing"
)

func TestLocalStartupDoesNotPublishAfterAdmissionCloses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancelCause(t.Context())
	defer stop(nil)
	startup := &LocalStartup{listener: &localStartupListener{Listener: listener}, ctx: ctx}
	// Close admission before net/http's terminal cause reaches the startup owner.
	if err := startup.listener.Close(); err != nil {
		t.Fatal(err)
	}
	startup.publish(http.NotFoundHandler())
	if startup.ready.Load() != nil {
		t.Fatal("handler published after admission closed")
	}
}
