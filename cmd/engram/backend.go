package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Gentleman-Programming/engram/internal/remote"
	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/Gentleman-Programming/engram/internal/types"
)

// syncClientIface abstracts remote.SyncClient for testing.
// SchedulePush signals the debounce relay to push dirty local data to cloud.
type syncClientIface interface {
	Start(ctx context.Context)
	Stop()
	SchedulePush()
}

// syncClientFactory creates a SyncClient for local-sync mode.
// Injectable for testing.
var syncClientFactory = func(s *store.Store, cfg store.Config) (syncClientIface, error) {
	cloudCfg, err := remote.LoadFromStore(s)
	if err != nil {
		return nil, fmt.Errorf("local-sync requires cloud config: %w", err)
	}
	client, err := remote.NewClient(cloudCfg.ServerURL, cloudCfg.APIKey, version)
	if err != nil {
		return nil, fmt.Errorf("local-sync: create client: %w", err)
	}
	return remote.NewSyncClient(client, s, cloudCfg), nil
}

// syncClientStart starts the sync client. Injectable for testing.
var syncClientStart = func(sc syncClientIface, ctx context.Context) {
	sc.Start(ctx)
}

// syncClientStop stops the sync client. Injectable for testing.
var syncClientStop = func(sc syncClientIface) {
	sc.Stop()
}

// newRemoteStore creates a RemoteStore from CloudConfig loaded from the local store.
// Injectable for testing.
var newRemoteStore = func(s *store.Store) (types.StoreInterface, error) {
	cloudCfg, err := remote.LoadFromStore(s)
	if err != nil {
		return nil, fmt.Errorf("--backend cloud requires cloud config (run 'engram cloud setup'): %w", err)
	}
	client, err := remote.NewClient(cloudCfg.ServerURL, cloudCfg.APIKey, version)
	if err != nil {
		return nil, fmt.Errorf("--backend cloud: create client: %w", err)
	}
	return remote.NewRemoteStore(client, cloudCfg.Project), nil
}

// validBackends is the set of accepted --backend values.
var validBackends = map[string]bool{
	"local":      true,
	"cloud":      true,
	"local-sync": true,
}

// parseBackendFlag extracts the --backend value from os.Args starting at position startIdx.
// Returns "" if not found (meaning "local" default).
func parseBackendFlag(startIdx int) string {
	for i := startIdx; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--backend" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
		if len(arg) > 10 && arg[:10] == "--backend=" {
			return arg[10:]
		}
	}
	return ""
}

// validateBackend checks that the backend value is valid.
// Returns an error if invalid. Empty string is treated as "local".
func validateBackend(backend string) error {
	if backend == "" {
		return nil
	}
	if !validBackends[backend] {
		return fmt.Errorf("unknown --backend value %q: must be one of local, cloud, local-sync", backend)
	}
	return nil
}
