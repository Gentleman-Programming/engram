package autosync

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// ProjectReplay describes the replay outcome for one project whose deferred
// relations were replayed after a mutation pull.
type ProjectReplay struct {
	Project   string
	Retried   int
	Succeeded int
	Failed    int
	Dead      int
}

// PullReport describes the outcome of one mutation pull operation. It is the
// single result shape shared by the background autosync manager and the manual
// `engram cloud pull-mutations` command.
type PullReport struct {
	Applied         int             // mutations applied (including deferred relations)
	LastPulledSeq   int64           // sequence consumed on the success path
	ProjectsTouched []string        // projects whose mutations were pulled (sorted)
	Replays         []ProjectReplay // replay outcomes per touched project
}

// PullMutations pulls remote mutations for targetKey since the persisted
// last_pulled_seq cursor until the server reports no more, applies each via
// ApplyPulledMutation, then replays deferred relations for touched projects.
//
// This is the single implementation shared by the autosync Manager and the
// operator-facing `engram cloud pull-mutations` command (issue #327). The
// command differs only in reporting and state marking — never in cursor or
// apply semantics. Per-entity error policy (design §9): relation FK misses are
// handled inside ApplyPulledMutation by writing to sync_apply_deferred and
// returning nil (cursor advances normally); other errors halt the pull.
func PullMutations(ctx context.Context, localStore LocalStore, transport CloudTransport, targetKey string, batchSize int) (PullReport, error) {
	var report PullReport

	if ctx.Err() != nil {
		return report, ctx.Err()
	}

	state, err := localStore.GetSyncState(targetKey)
	if err != nil {
		return report, fmt.Errorf("get sync state: %w", err)
	}

	sinceSeq := state.LastPulledSeq

	touchedProjects := make(map[string]struct{})
	projectOrder := make([]string, 0)
	for {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}

		resp, err := transport.PullMutations(sinceSeq, batchSize)
		if err != nil {
			return report, fmt.Errorf("transport pull: %w", err)
		}

		for _, rm := range resp.Mutations {
			localMut := store.SyncMutation{
				Seq:        rm.Seq,
				TargetKey:  targetKey,
				Project:    rm.Project,
				Entity:     rm.Entity,
				EntityKey:  rm.EntityKey,
				Op:         rm.Op,
				Payload:    string(rm.Payload),
				Source:     store.SyncSourceRemote,
				OccurredAt: rm.OccurredAt,
			}
			if err := localStore.ApplyPulledMutation(targetKey, localMut); err != nil {
				return report, fmt.Errorf("apply pulled mutation seq=%d: %w", rm.Seq, err)
			}
			report.Applied++
			project := strings.TrimSpace(rm.Project)
			if project != "" {
				if _, seen := touchedProjects[project]; !seen {
					touchedProjects[project] = struct{}{}
					projectOrder = append(projectOrder, project)
				}
			}
			if rm.Seq > sinceSeq {
				sinceSeq = rm.Seq
			}
		}

		if !resp.HasMore {
			break
		}
	}
	report.LastPulledSeq = sinceSeq

	pendingProjects, err := localStore.ListDeferredProjectsForTarget(targetKey)
	if err != nil {
		log.Printf("[autosync] list deferred projects target=%q error: %v", targetKey, err)
	} else {
		for _, project := range pendingProjects {
			project = strings.TrimSpace(project)
			if project == "" {
				continue
			}
			if _, seen := touchedProjects[project]; seen {
				continue
			}
			touchedProjects[project] = struct{}{}
			projectOrder = append(projectOrder, project)
		}
	}
	sort.Strings(projectOrder)
	report.ProjectsTouched = projectOrder

	for _, project := range projectOrder {
		res, err := localStore.ReplayDeferredForScope(targetKey, project)
		if err != nil {
			log.Printf("[autosync] replayDeferred project=%q error: %v", project, err)
			continue
		}
		report.Replays = append(report.Replays, ProjectReplay{
			Project:   project,
			Retried:   res.Retried,
			Succeeded: res.Succeeded,
			Failed:    res.Failed,
			Dead:      res.Dead,
		})
	}

	return report, nil
}
