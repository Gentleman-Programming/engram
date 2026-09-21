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

// cloudPullMutationsStore is the store surface the pull command needs: the
// shared autosync pull path plus degraded-state marking and shutdown.
type cloudPullMutationsStore interface {
	autosync.LocalStore
	MarkSyncAuthRequired(targetKey, message string) error
	MarkSyncBlocked(targetKey, reasonCode, message string) error
	MarkSyncFailure(targetKey, message string, backoffUntil time.Time) error
	Close() error
}

// closeCloudPullMutationsStore is a deterministic seam for the store close at
// the end of the command, so tests can inject a close failure and assert the
// command reports it instead of discarding it.
var closeCloudPullMutationsStore = func(s cloudPullMutationsStore) error { return s.Close() }

// newCloudPullMutationsStore opens the local store for the pull command. It is
// a deterministic seam so tests can inject a store whose deferred-replay
// infrastructure fails; runtime behavior never changes.
var newCloudPullMutationsStore = func(cfg store.Config) (cloudPullMutationsStore, error) {
	return storeNew(cfg)
}

// cmdCloudPullMutations implements `engram cloud pull-mutations`: an explicit
// operator action that pulls remote cloud mutations since the local cursor and
// applies them locally (issue #327). It mirrors the autosync pull path by
// delegating to the shared autosync.PullMutations implementation, so cursor and
// apply semantics never diverge between the background manager and the manual
// command. It never changes the automatic sync policy.
func cmdCloudPullMutations(cfg store.Config) {
	s, err := newCloudPullMutationsStore(cfg)
	if err != nil {
		fatal(err)
		return
	}

	report, runErr := executeCloudPullMutations(s, cfg)
	// Preserve the primary failure together with any shutdown damage: a close
	// error must stay inspectable even when the pull/apply already failed.
	closeErr := closeCloudPullMutationsStore(s)
	if closeErr != nil {
		closeErr = fmt.Errorf("cloud pull-mutations store close: %w", closeErr)
	}
	if err := errors.Join(runErr, closeErr); err != nil {
		fatal(err)
		return
	}

	printCloudPullMutationsResult(report)
}

// executeCloudPullMutations performs the pull and applies remote mutations to the
// local store. It returns the pull report so the caller can close the store and
// report success before any output is emitted.
func executeCloudPullMutations(s cloudPullMutationsStore, cfg store.Config) (autosync.PullReport, error) {
	cc, err := resolveCloudRuntimeConfig(cfg)
	if err != nil {
		return autosync.PullReport{}, fmt.Errorf("cloud pull-mutations config error: %w", err)
	}
	serverURL := strings.TrimSpace(cc.ServerURL)
	if serverURL == "" {
		return autosync.PullReport{}, fmt.Errorf("cloud server is missing: configure server URL with `engram cloud config --server <url>`")
	}

	// An empty token is allowed: ENGRAM_CLOUD_INSECURE_NO_AUTH servers accept
	// unauthenticated mutation pull. A server that does require auth rejects
	// the request with 401, which surfaces through the existing auth guidance.
	token := strings.TrimSpace(cc.Token)

	remoteMT, err := remote.NewMutationTransport(serverURL, token)
	if err != nil {
		return autosync.PullReport{}, err
	}
	transport := &mutationTransportAdapter{remote: remoteMT}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	targetKey := store.DefaultSyncTargetKey
	report, err := autosync.PullMutations(ctx, s, transport, targetKey, autosync.DefaultConfig().PullBatchSize)
	if err != nil {
		markCloudSyncFailure(s, targetKey, err)
		return autosync.PullReport{}, errors.New(cloudSyncFailureMessage("", err))
	}

	if err := s.MarkSyncHealthy(targetKey); err != nil {
		return autosync.PullReport{}, fmt.Errorf("cloud pull-mutations health update: %w", err)
	}

	return report, nil
}

// printCloudPullMutationsResult renders the pull report for the operator.
// Deferred replays are always reported, even when no new mutations were
// applied: PullMutations replays pending deferred work for touched projects,
// so an empty remote response can still change local state.
func printCloudPullMutationsResult(report autosync.PullReport) {
	if report.Applied == 0 {
		fmt.Println("No new cloud mutations to pull.")
	} else {
		fmt.Printf("Pulled %d cloud mutation(s) from %d project(s).\n", report.Applied, len(report.ProjectsTouched))
		if len(report.ProjectsTouched) > 0 {
			fmt.Printf("  Projects: %s\n", strings.Join(report.ProjectsTouched, ", "))
		}
	}
	for _, replay := range report.Replays {
		if replay.Retried > 0 {
			fmt.Printf("  Deferred relations replayed for %s: %d retried, %d succeeded, %d failed, %d dead\n",
				replay.Project, replay.Retried, replay.Succeeded, replay.Failed, replay.Dead)
		}
	}
}
