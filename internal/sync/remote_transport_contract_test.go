package sync_test

import (
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/remote"
	engramsync "github.com/Gentleman-Programming/engram/v2/internal/sync"
)

func TestRemoteTransportImplementsTransportContract(t *testing.T) {
	rt, err := remote.NewRemoteTransport("https://example.com", "token", "proj-a")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	var _ engramsync.Transport = rt
}
