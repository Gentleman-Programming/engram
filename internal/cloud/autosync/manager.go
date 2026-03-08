// Package autosync implements a lease-guarded background sync manager
// for Engram's local-first cloud replication.
//
// The manager runs in long-lived local processes (serve, mcp) and:
//   - Acquires a SQLite-backed lease to prevent duplicate workers.
//   - Pushes pending local mutations to the cloud server.
//   - Pulls remote mutations by cursor and applies them locally.
//   - Supports debounced wake on dirty state and periodic freshness checks.
//   - Uses exponential backoff with jitter on failures, bounded by max retries.
//   - Tracks degraded state (phase, last error, backoff timing).
//   - Shuts down gracefully via context cancellation.
package autosync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── Phase Constants ─────────────────────────────────────────────────────────

const (
	PhaseIdle       = "idle"
	PhasePushing    = "pushing"
	PhasePulling    = "pulling"
	PhasePushFailed = "push_failed"
	PhasePullFailed = "pull_failed"
	PhaseBackoff    = "backoff"
	PhaseHealthy    = "healthy"
)

// ─── Interfaces ──────────────────────────────────────────────────────────────

// LocalStore is the subset of store.Store methods the manager needs.
type LocalStore interface {
	GetSyncState(targetKey string) (*store.SyncState, error)
	ListPendingSyncMutations(targetKey string, limit int) ([]store.SyncMutation, error)
	AckSyncMutations(targetKey string, lastAckedSeq int64) error
	AckSyncMutationSeqs(targetKey string, seqs []int64) error
	SkipAckNonEnrolledMutations(targetKey string) (int64, error)
	AcquireSyncLease(targetKey, owner string, ttl time.Duration, now time.Time) (bool, error)
	ReleaseSyncLease(targetKey, owner string) error
	ApplyPulledMutation(targetKey string, mutation store.SyncMutation) error
	MarkSyncFailure(targetKey, message string, backoffUntil time.Time) error
	MarkSyncHealthy(targetKey string) error
}

// CloudTransport is the subset of remote.RemoteTransport methods the manager needs.
type CloudTransport interface {
	PushMutations(mutations []remote.MutationEntry) (*remote.PushMutationsResult, error)
	PullMutations(sinceSeq int64, limit int) (*remote.PullMutationsResponse, error)
}

// ─── Config ──────────────────────────────────────────────────────────────────

// Config holds tuning parameters for the background sync manager.
type Config struct {
	TargetKey              string        // sync_state target key (default: "cloud")
	LeaseOwner             string        // unique owner identity for lease
	LeaseInterval          time.Duration // how long to hold the lease each cycle
	DebounceDuration       time.Duration // debounce window for dirty notifications
	PollInterval           time.Duration // periodic freshness check while idle
	PushBatchSize          int           // max mutations per push request
	PullBatchSize          int           // max mutations per pull request
	MaxConsecutiveFailures int           // stop retrying after this many consecutive failures
	BaseBackoff            time.Duration // base duration for exponential backoff
	MaxBackoff             time.Duration // ceiling for backoff duration
}

// DefaultConfig returns sensible production defaults.
func DefaultConfig() Config {
	return Config{
		TargetKey:              store.DefaultSyncTargetKey,
		LeaseOwner:             fmt.Sprintf("autosync-%d", time.Now().UnixNano()),
		LeaseInterval:          60 * time.Second,
		DebounceDuration:       500 * time.Millisecond,
		PollInterval:           30 * time.Second,
		PushBatchSize:          100,
		PullBatchSize:          100,
		MaxConsecutiveFailures: 10,
		BaseBackoff:            1 * time.Second,
		MaxBackoff:             5 * time.Minute,
	}
}

// ─── Status ──────────────────────────────────────────────────────────────────

// Status represents the current degraded-state snapshot of the manager.
type Status struct {
	Phase               string     `json:"phase"`
	LastError           string     `json:"last_error,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	BackoffUntil        *time.Time `json:"backoff_until,omitempty"`
	LastSyncAt          *time.Time `json:"last_sync_at,omitempty"`
}

// ─── Manager ─────────────────────────────────────────────────────────────────

// Manager coordinates background push/pull sync between local SQLite
// and the cloud server. It is safe for concurrent use.
type Manager struct {
	store     LocalStore
	transport CloudTransport
	cfg       Config

	mu        sync.RWMutex
	status    Status
	dirtyCh   chan struct{}
	leaseHeld bool
}

// New creates a new background sync manager.
func New(localStore LocalStore, transport CloudTransport, cfg Config) *Manager {
	if cfg.TargetKey == "" {
		cfg.TargetKey = store.DefaultSyncTargetKey
	}
	if cfg.PushBatchSize <= 0 {
		cfg.PushBatchSize = 100
	}
	if cfg.PullBatchSize <= 0 {
		cfg.PullBatchSize = 100
	}
	if cfg.MaxConsecutiveFailures <= 0 {
		cfg.MaxConsecutiveFailures = 10
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 5 * time.Minute
	}
	if cfg.DebounceDuration <= 0 {
		cfg.DebounceDuration = 500 * time.Millisecond
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.LeaseInterval <= 0 {
		cfg.LeaseInterval = 60 * time.Second
	}
	return &Manager{
		store:     localStore,
		transport: transport,
		cfg:       cfg,
		status:    Status{Phase: PhaseIdle},
		dirtyCh:   make(chan struct{}, 1),
	}
}

// NotifyDirty signals the manager that local state has changed.
// Non-blocking; coalesces multiple calls via a buffered channel.
func (m *Manager) NotifyDirty() {
	select {
	case m.dirtyCh <- struct{}{}:
	default:
		// Already signaled, skip.
	}
}

// Status returns the current degraded-state snapshot. Thread-safe.
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

// Run is the main loop. It blocks until the context is cancelled.
// On shutdown it releases the lease and returns.
func (m *Manager) Run(ctx context.Context) {
	slog.Info("autosync: manager started",
		"target_key", m.cfg.TargetKey,
		"poll_interval", m.cfg.PollInterval,
		"push_batch", m.cfg.PushBatchSize,
		"pull_batch", m.cfg.PullBatchSize,
	)
	defer func() {
		slog.Info("autosync: manager stopped", "target_key", m.cfg.TargetKey)
		m.releaseLease()
	}()

	debounce := time.NewTimer(m.cfg.DebounceDuration)
	if !debounce.Stop() {
		select {
		case <-debounce.C:
		default:
		}
	}

	poll := time.NewTicker(m.cfg.PollInterval)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.dirtyCh:
			// Reset debounce timer to coalesce rapid notifications.
			if !debounce.Stop() {
				select {
				case <-debounce.C:
				default:
				}
			}
			debounce.Reset(m.cfg.DebounceDuration)
		case <-debounce.C:
			m.cycle(ctx)
		case <-poll.C:
			m.cycle(ctx)
		}
	}
}

// ─── Core Cycle ──────────────────────────────────────────────────────────────

func (m *Manager) cycle(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	// Check if we've exceeded the failure ceiling.
	m.mu.RLock()
	failures := m.status.ConsecutiveFailures
	backoffUntil := m.status.BackoffUntil
	m.mu.RUnlock()

	if failures >= m.cfg.MaxConsecutiveFailures {
		slog.Warn("autosync: max failures reached, entering backoff",
			"consecutive_failures", failures,
			"max", m.cfg.MaxConsecutiveFailures,
		)
		m.setPhase(PhaseBackoff)
		return
	}

	// Respect backoff timing.
	if backoffUntil != nil && time.Now().Before(*backoffUntil) {
		slog.Debug("autosync: skipping cycle, in backoff",
			"backoff_until", backoffUntil,
			"consecutive_failures", failures,
		)
		m.setPhase(PhaseBackoff)
		return
	}

	// Acquire lease.
	now := time.Now().UTC()
	acquired, err := m.store.AcquireSyncLease(m.cfg.TargetKey, m.cfg.LeaseOwner, m.cfg.LeaseInterval, now)
	if err != nil {
		slog.Warn("autosync: lease acquisition error", "error", err)
		return
	}
	if !acquired {
		slog.Debug("autosync: lease held by another worker, skipping cycle")
		return
	}
	m.mu.Lock()
	m.leaseHeld = true
	m.mu.Unlock()

	// Push, then pull.
	start := time.Now()
	if err := m.push(ctx); err != nil {
		slog.Warn("autosync: push failed", "error", err, "elapsed", time.Since(start))
		m.recordFailure(fmt.Sprintf("push: %v", err))
		return
	}

	if err := m.pull(ctx); err != nil {
		slog.Warn("autosync: pull failed", "error", err, "elapsed", time.Since(start))
		m.recordFailure(fmt.Sprintf("pull: %v", err))
		return
	}

	// Success — mark healthy.
	slog.Debug("autosync: cycle complete", "elapsed", time.Since(start))
	m.recordSuccess()
}

// ─── Push ────────────────────────────────────────────────────────────────────

func (m *Manager) push(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	m.setPhase(PhasePushing)

	// Skip-ack mutations for non-enrolled projects before listing pending.
	// This prevents journal bloat and ensures ListPendingSyncMutations only
	// returns mutations we actually intend to push.
	if _, err := m.store.SkipAckNonEnrolledMutations(m.cfg.TargetKey); err != nil {
		return fmt.Errorf("skip-ack non-enrolled: %w", err)
	}

	pending, err := m.store.ListPendingSyncMutations(m.cfg.TargetKey, m.cfg.PushBatchSize)
	if err != nil {
		return fmt.Errorf("list pending: %w", err)
	}
	if len(pending) == 0 {
		slog.Debug("autosync: push skipped, no pending mutations")
		return nil
	}
	slog.Debug("autosync: pushing mutations", "count", len(pending))

	groups := make(map[string][]store.SyncMutation)
	order := make([]string, 0)
	for _, mut := range pending {
		project := mut.Project
		if _, ok := groups[project]; !ok {
			order = append(order, project)
		}
		groups[project] = append(groups[project], mut)
	}

	for _, project := range order {
		batch := groups[project]
		entries := make([]remote.MutationEntry, len(batch))
		seqs := make([]int64, len(batch))
		for i, mut := range batch {
			entries[i] = remote.MutationEntry{
				Entity:    mut.Entity,
				EntityKey: mut.EntityKey,
				Op:        mut.Op,
				Payload:   json.RawMessage(mut.Payload),
			}
			seqs[i] = mut.Seq
		}

		result, err := m.transport.PushMutations(entries)
		if err != nil {
			return fmt.Errorf("transport push project %q: %w", project, err)
		}
		_ = result
		if err := m.store.AckSyncMutationSeqs(m.cfg.TargetKey, seqs); err != nil {
			return fmt.Errorf("ack project %q: %w", project, err)
		}
		slog.Debug("autosync: pushed batch", "project", project, "mutations", len(entries))
	}

	return nil
}

// ─── Pull ────────────────────────────────────────────────────────────────────

func (m *Manager) pull(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	m.setPhase(PhasePulling)

	state, err := m.store.GetSyncState(m.cfg.TargetKey)
	if err != nil {
		return fmt.Errorf("get sync state: %w", err)
	}

	sinceSeq := state.LastPulledSeq
	totalPulled := 0

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		resp, err := m.transport.PullMutations(sinceSeq, m.cfg.PullBatchSize)
		if err != nil {
			return fmt.Errorf("transport pull: %w", err)
		}

		for _, rm := range resp.Mutations {
			localMut := store.SyncMutation{
				Seq:        rm.Seq,
				TargetKey:  m.cfg.TargetKey,
				Entity:     rm.Entity,
				EntityKey:  rm.EntityKey,
				Op:         rm.Op,
				Payload:    string(rm.Payload),
				Source:     store.SyncSourceRemote,
				OccurredAt: rm.OccurredAt,
			}
			if err := m.store.ApplyPulledMutation(m.cfg.TargetKey, localMut); err != nil {
				return fmt.Errorf("apply pulled mutation seq=%d: %w", rm.Seq, err)
			}
			if rm.Seq > sinceSeq {
				sinceSeq = rm.Seq
			}
			totalPulled++
		}

		if !resp.HasMore {
			break
		}
	}

	if totalPulled > 0 {
		slog.Debug("autosync: pull complete", "mutations_applied", totalPulled, "last_seq", sinceSeq)
	} else {
		slog.Debug("autosync: pull skipped, already up to date", "last_seq", sinceSeq)
	}

	return nil
}

// ─── State Tracking ──────────────────────────────────────────────────────────

func (m *Manager) setPhase(phase string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Phase = phase
}

func (m *Manager) recordFailure(msg string) {
	m.mu.Lock()
	failures := m.status.ConsecutiveFailures + 1
	m.status.ConsecutiveFailures = failures
	m.status.LastError = msg

	backoff := m.computeBackoff(failures)
	bu := time.Now().Add(backoff)
	m.status.BackoffUntil = &bu

	if m.status.Phase == PhasePushing {
		m.status.Phase = PhasePushFailed
	} else {
		m.status.Phase = PhasePullFailed
	}
	m.mu.Unlock()

	slog.Warn("autosync: sync failure recorded",
		"error", msg,
		"consecutive_failures", failures,
		"backoff_until", bu,
	)

	// Persist degraded state to store (best-effort).
	_ = m.store.MarkSyncFailure(m.cfg.TargetKey, msg, bu)
}

func (m *Manager) recordSuccess() {
	now := time.Now()
	m.mu.Lock()
	m.status.Phase = PhaseHealthy
	m.status.ConsecutiveFailures = 0
	m.status.LastError = ""
	m.status.BackoffUntil = nil
	m.status.LastSyncAt = &now
	m.mu.Unlock()

	slog.Info("autosync: sync healthy", "last_sync_at", now)

	// Persist healthy state to store (best-effort).
	_ = m.store.MarkSyncHealthy(m.cfg.TargetKey)
}

// computeBackoff returns exponential backoff with jitter.
// Formula: min(base * 2^(failures-1) + jitter, maxBackoff)
func (m *Manager) computeBackoff(failures int) time.Duration {
	if failures <= 0 {
		return m.cfg.BaseBackoff
	}
	exp := math.Pow(2, float64(failures-1))
	base := time.Duration(float64(m.cfg.BaseBackoff) * exp)
	if base > m.cfg.MaxBackoff {
		base = m.cfg.MaxBackoff
	}
	// Add up to 25% jitter.
	jitter := time.Duration(rand.Int63n(int64(base/4) + 1))
	result := base + jitter
	if result > m.cfg.MaxBackoff {
		result = m.cfg.MaxBackoff
	}
	return result
}

// SyncStatus returns a server-compatible status snapshot. This method satisfies
// the server.SyncStatusProvider interface via structural typing.
func (m *Manager) SyncStatus() Status {
	return m.Status()
}

func (m *Manager) releaseLease() {
	m.mu.Lock()
	m.leaseHeld = false
	m.mu.Unlock()

	// Always attempt to release — the store ignores mismatched owners.
	_ = m.store.ReleaseSyncLease(m.cfg.TargetKey, m.cfg.LeaseOwner)
}
