package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/autosync"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// cmdCloudPullMutations implements `engram cloud pull-mutations`: an explicit
// operator action that pulls remote cloud mutations since the local cursor and
// applies them locally (issue #327). It mirrors the autosync pull path by
// delegating to the shared autosync.PullMutations implementation, so cursor and
// apply semantics never diverge between the background manager and the manual
// command. It never changes the automatic sync policy.
func cmdCloudPullMutations(cfg store.Config) {
	s, err := storeNew(cfg)
	if err != nil {
		fatal(err)
		return
	}
	defer func() { _ = s.Close() }()

	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		fatal(fmt.Errorf("cloud pull-mutations config error: %w", err))
		return
	}
	serverURL := strings.TrimSpace(cc.ServerURL)
	if serverURL == "" {
		fatal(fmt.Errorf("cloud server is missing: configure server URL with `engram cloud config --server <url>`"))
		return
	}
	token := strings.TrimSpace(cc.Token)
	if token == "" {
		fatal(fmt.Errorf("cloud token is missing: set ENGRAM_CLOUD_TOKEN or store it with `engram cloud config --token <token>`"))
		return
	}

	remoteMT, err := remote.NewMutationTransport(serverURL, token)
	if err != nil {
		fatal(err)
		return
	}
	transport := &mutationTransportAdapter{remote: remoteMT}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	targetKey := store.DefaultSyncTargetKey
	report, err := autosync.PullMutations(ctx, s, transport, targetKey, autosync.DefaultConfig().PullBatchSize)
	if err != nil {
		markCloudSyncFailure(s, targetKey, err)
		fatal(errors.New(cloudSyncFailureMessage("", err)))
		return
	}

	if err := s.MarkSyncHealthy(targetKey); err != nil {
		fatal(fmt.Errorf("cloud pull-mutations health update: %w", err))
		return
	}

	if report.Applied == 0 {
		fmt.Println("No new cloud mutations to pull.")
		return
	}
	fmt.Printf("Pulled %d cloud mutation(s) from %d project(s).\n", report.Applied, len(report.ProjectsTouched))
	if len(report.ProjectsTouched) > 0 {
		fmt.Printf("  Projects: %s\n", strings.Join(report.ProjectsTouched, ", "))
	}
	for _, replay := range report.Replays {
		if replay.Retried > 0 {
			fmt.Printf("  Deferred relations replayed for %s: %d retried, %d succeeded, %d failed, %d dead\n",
				replay.Project, replay.Retried, replay.Succeeded, replay.Failed, replay.Dead)
		}
	}
}
