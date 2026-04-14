package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/internal/types"
)

const (
	syncTargetKey  = "cloud"
	pushBatchLimit = 100
	pullPageLimit  = 500
)

// SyncStore is the interface the SyncClient needs from the local store.
// Implemented by *store.Store.
type SyncStore interface {
	ConfigStore
	ListPendingSyncMutations(targetKey string, limit int) ([]types.SyncMutation, error)
	AckSyncMutations(targetKey string, lastAckedSeq int64) error
	AcquireSyncLease(targetKey, owner string, ttl time.Duration, now time.Time) (bool, error)
	ReleaseSyncLease(targetKey, owner string) error
	MarkSyncFailure(targetKey, message string, backoffUntil time.Time) error
	MarkSyncHealthy(targetKey string) error
	ApplyPulledMutation(targetKey string, mutation types.SyncMutation) error
	GetSyncState(targetKey string) (*types.SyncState, error)
	IsProjectEnrolled(project string) (bool, error)
	ListEnrolledProjects() ([]types.EnrolledProject, error)
}

// SyncClient handles bidirectional sync between local SQLite and the cloud server.
// It runs background goroutines for push (debounced) and pull (periodic).
type SyncClient struct {
	client *Client
	store  SyncStore
	config CloudConfig
	owner  string // lease owner ID

	// Debounce
	pushCh chan struct{} // signal channel for debounced push
	mu     sync.Mutex

	// Lifecycle
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewSyncClient creates a new sync client. Call Start() to begin background sync.
func NewSyncClient(client *Client, store SyncStore, config CloudConfig) *SyncClient {
	return &SyncClient{
		client: client,
		store:  store,
		config: config,
		owner:  fmt.Sprintf("sync-%d", time.Now().UnixNano()),
		pushCh: make(chan struct{}, 1),
	}
}

// Start launches background push and pull goroutines. Non-blocking.
func (sc *SyncClient) Start(ctx context.Context) {
	ctx, sc.cancel = context.WithCancel(ctx)

	sc.wg.Add(2)
	go sc.pushLoop(ctx)
	go sc.pullLoop(ctx)
}

// Stop cancels background goroutines, flushes pending push (best-effort 5s), and releases lease.
func (sc *SyncClient) Stop() {
	if sc.cancel != nil {
		sc.cancel()
	}

	done := make(chan struct{})
	go func() {
		sc.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(6 * time.Second):
	}

	_ = sc.store.ReleaseSyncLease(syncTargetKey, sc.owner)
}

// SchedulePush signals the debounce relay to trigger a push cycle.
func (sc *SyncClient) SchedulePush() {
	select {
	case sc.pushCh <- struct{}{}:
	default:
		// already scheduled
	}
}

// PushOnce runs a single push cycle: acquire lease, drain pending mutations, release.
func (sc *SyncClient) PushOnce(ctx context.Context) (int, error) {
	acquired, err := sc.store.AcquireSyncLease(syncTargetKey, sc.owner, 60*time.Second, time.Now())
	if err != nil {
		return 0, fmt.Errorf("acquire lease: %w", err)
	}
	if !acquired {
		return 0, nil // another sync in progress
	}
	defer sc.store.ReleaseSyncLease(syncTargetKey, sc.owner)

	totalPushed := 0
	for {
		mutations, err := sc.store.ListPendingSyncMutations(syncTargetKey, pushBatchLimit)
		if err != nil {
			return totalPushed, fmt.Errorf("list mutations: %w", err)
		}
		if len(mutations) == 0 {
			break
		}

		// Filter by enrollment
		enrolled := sc.filterEnrolled(ctx, mutations)
		if len(enrolled) == 0 {
			// ACK all as skipped
			lastSeq := mutations[len(mutations)-1].Seq
			if err := sc.store.AckSyncMutations(syncTargetKey, lastSeq); err != nil {
				return totalPushed, fmt.Errorf("ack skipped: %w", err)
			}
			continue
		}

		// Convert to cloud mutations and push
		cloudMutations := toCloudMutations(enrolled)
		body, err := json.Marshal(map[string]any{
			"device_id": sc.owner,
			"project":   enrolled[0].Project,
			"mutations": cloudMutations,
		})
		if err != nil {
			return totalPushed, fmt.Errorf("marshal push: %w", err)
		}

		_, err = sc.client.Post(ctx, "/api/v1/sync/push", body)
		if err != nil {
			sc.markFailure(fmt.Sprintf("push: %v", err))
			return totalPushed, fmt.Errorf("push: %w", err)
		}

		// ACK pushed mutations
		lastSeq := enrolled[len(enrolled)-1].Seq
		if err := sc.store.AckSyncMutations(syncTargetKey, lastSeq); err != nil {
			return totalPushed, fmt.Errorf("ack: %w", err)
		}

		totalPushed += len(enrolled)

		if len(mutations) < pushBatchLimit {
			break // no more
		}
	}

	if totalPushed > 0 {
		_ = sc.store.MarkSyncHealthy(syncTargetKey)
	}
	return totalPushed, nil
}

// PullOnce runs a single paginated pull cycle.
func (sc *SyncClient) PullOnce(ctx context.Context) (int, error) {
	state, err := sc.store.GetSyncState(syncTargetKey)
	if err != nil {
		return 0, fmt.Errorf("get state: %w", err)
	}

	totalPulled := 0
	cursor := state.LastPulledSeq

	for {
		path := fmt.Sprintf("/api/v1/sync/pull?project=%s&since_seq=%d&limit=%d",
			sc.config.Project, cursor, pullPageLimit)

		respBody, err := sc.client.Get(ctx, path)
		if err != nil {
			sc.markFailure(fmt.Sprintf("pull: %v", err))
			return totalPulled, fmt.Errorf("pull: %w", err)
		}

		var pullResp struct {
			Entities []struct {
				EntityType string         `json:"entity_type"`
				ServerSeq  int64          `json:"server_seq"`
				Data       map[string]any `json:"data"`
			} `json:"entities"`
			MaxSeq  int64 `json:"max_seq"`
			HasMore bool  `json:"has_more"`
		}
		if err := json.Unmarshal(respBody, &pullResp); err != nil {
			return totalPulled, fmt.Errorf("decode pull: %w", err)
		}

		for _, entity := range pullResp.Entities {
			payload, _ := json.Marshal(entity.Data)
			mutation := types.SyncMutation{
				Seq:       entity.ServerSeq,
				TargetKey: syncTargetKey,
				Entity:    entity.EntityType,
				EntityKey: entityKeyFromData(entity.Data),
				Op:        opFromData(entity.Data),
				Payload:   string(payload),
				Source:     "cloud",
			}

			if err := sc.store.ApplyPulledMutation(syncTargetKey, mutation); err != nil {
				log.Printf("engram-sync: apply mutation seq=%d: %v (skipping)", entity.ServerSeq, err)
				continue
			}
			totalPulled++
		}

		cursor = pullResp.MaxSeq

		if !pullResp.HasMore || len(pullResp.Entities) < pullPageLimit {
			break
		}

		// Brief pause between pages
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return totalPulled, ctx.Err()
		}
	}

	if totalPulled > 0 {
		_ = sc.store.MarkSyncHealthy(syncTargetKey)
	}
	return totalPulled, nil
}

// pushLoop runs the debounced push goroutine.
func (sc *SyncClient) pushLoop(ctx context.Context) {
	defer sc.wg.Done()

	var timer *time.Timer
	for {
		select {
		case <-ctx.Done():
			// Flush on shutdown (best-effort 5s)
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			sc.PushOnce(flushCtx)
			cancel()
			return
		case <-sc.pushCh:
			// Reset debounce timer
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(sc.config.PushDebounce, func() {
				sc.PushOnce(ctx)
			})
		}
	}
}

// pullLoop runs the periodic pull goroutine.
func (sc *SyncClient) pullLoop(ctx context.Context) {
	defer sc.wg.Done()

	// Initial pull on start
	sc.PullOnce(ctx)

	ticker := time.NewTicker(sc.config.PullInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sc.PullOnce(ctx)
		}
	}
}

func (sc *SyncClient) filterEnrolled(ctx context.Context, mutations []types.SyncMutation) []types.SyncMutation {
	var result []types.SyncMutation
	for _, m := range mutations {
		if m.Project == "" {
			result = append(result, m) // global mutations always included
			continue
		}
		enrolled, _ := sc.store.IsProjectEnrolled(m.Project)
		if enrolled {
			result = append(result, m)
		}
	}
	return result
}

func (sc *SyncClient) markFailure(msg string) {
	state, _ := sc.store.GetSyncState(syncTargetKey)
	failures := 1
	if state != nil {
		failures = state.ConsecutiveFailures + 1
	}

	// Backoff: 30s, 60s, 120s, 300s max
	backoffSecs := 30 * (1 << min(failures-1, 3))
	if backoffSecs > 300 {
		backoffSecs = 300
	}

	_ = sc.store.MarkSyncFailure(syncTargetKey, msg, time.Now().Add(time.Duration(backoffSecs)*time.Second))
}

// helpers

func toCloudMutations(mutations []types.SyncMutation) []map[string]any {
	result := make([]map[string]any, len(mutations))
	for i, m := range mutations {
		var payload map[string]any
		json.Unmarshal([]byte(m.Payload), &payload)
		result[i] = map[string]any{
			"seq":         m.Seq,
			"entity":      m.Entity,
			"entity_key":  m.EntityKey,
			"op":          m.Op,
			"payload":     payload,
			"occurred_at": m.OccurredAt,
		}
	}
	return result
}

func entityKeyFromData(data map[string]any) string {
	if v, ok := data["sync_id"].(string); ok {
		return v
	}
	if v, ok := data["id"].(string); ok {
		return v
	}
	return ""
}

func opFromData(data map[string]any) string {
	if _, ok := data["deleted_at"]; ok {
		return "delete"
	}
	return "upsert"
}
