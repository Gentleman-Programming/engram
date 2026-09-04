package sync_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/remote"
	engramsync "github.com/Gentleman-Programming/engram/v2/internal/sync"
)

func TestRemoteTransportImplementsTransportContract(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/pull":
			if got := r.URL.Query().Get("project"); got != "proj-a" {
				http.Error(w, "project required", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":1,"chunks":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	oldDefaultTransport := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldDefaultTransport })

	rt, err := remote.NewRemoteTransport(srv.URL, "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	var tr engramsync.Transport = rt
	if err := tr.WriteManifest(&engramsync.Manifest{Version: 1}); err != nil {
		t.Fatalf("WriteManifest should be accepted for remote transport: %v", err)
	}

	m, err := tr.ReadManifest()
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.Version != 1 {
		t.Fatalf("expected version 1, got %d", m.Version)
	}
}
