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
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/constants"
	"github.com/Gentleman-Programming/engram/internal/cloud/syncguidance"
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
	PhaseDisabled   = "disabled"
)

// ─── Transport types ─────────────────────────────────────────────────────────
// These mirror remote.MutationEntry / remote.PushMutationsResult / remote.PullMutationsResponse
// to avoid a circular import between autosync and remote.

type MutationEntry struct {
	Project   string          `json:"project"`
	Entity    string          `json:"entity"`
	EntityKey string          `json:"entity_key"`
	Op        string          `json:"op"`
	Payload   json.RawMessage `json:"payload"`
}

type PushMutationsResult struct {
	AcceptedSeqs []int64 `json:"accepted_seqs"`
}

type PulledMutation struct {
	Seq        int64           `json:"seq"`
	Entity     string          `json:"entity"`
	EntityKey  string          `json:"entity_key"`
	Op         string          `json:"op"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt string          `json:"occurred_at"`
}

type PullMutationsResponse struct {
	Mutations []PulledMutation `json:"mutations"`
	HasMore   bool             `json:"has_more"`
	LatestSeq int64            `json:"latest_seq"`
}

// ─── Interfaces ──────────────────────────────────────────────────────────────

// LocalStore is the subset of store.Store methods the manager needs.
type LocalStore interface {
	GetSyncState(targetKey string) (*store.SyncState, error)
	ListPendingSyncMutations(targetKey string, limit int) ([]store.SyncMutation, error)
	CountPendingNonEnrolledSyncMutations(targetKey string) ([]store.PendingSyncMutationProjectCount, error)
	AckSyncMutations(targetKey string, lastAckedSeq int64) error
	AckSyncMutationSeqs(targetKey string, seqs []int64) error
	AcquireSyncLease(targetKey, owner string, ttl time.Duration, now time.Time) (bool, error)
	ReleaseSyncLease(targetKey, owner string) error
	ApplyPulledMutation(targetKey string, mutation store.SyncMutation) error
	MarkSyncFailure(targetKey, message string, backoffUntil time.Time) error
	MarkSyncBlocked(targetKey, reasonCode, message string) error
	MarkSyncHealthy(targetKey string) error
	// Phase E: deferred relation retry.
	ReplayDeferred() (store.ReplayDeferredResult, error)
	CountDeferredAndDead() (deferred, dead int, err error)
}

type nonEnrolledPendingError struct {
	counts []store.PendingSyncMutationProjectCount
}

func (e *nonEnrolledPendingError) Error() string {
	return nonEnrolledPendingMessage(e.counts)
}

// CloudTransport is the subset of remote.MutationTransport methods the manager needs.
type CloudTransport interface {
	PushMutations(mutations []MutationEntry) (*PushMutationsResult, error)
	PullMutations(sinceSeq int64, limit int) (*PullMutationsResponse, error)
}

// transportStatusError is an optional interface that transport errors may implement.
// BW5: Allows Manager to detect 401 (auth_required) vs 403 (policy_forbidden)
// vs generic transport failures without importing the remote package.
type transportStatusError interface {
	IsAuthFailure() bool
	IsPolicyFailure() bool
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
	ReasonCode          string     `json:"reason_code,omitempty"`
	ReasonMessage       string     `json:"reason_message,omitempty"`
	// Phase E: deferred relation retry counts from sync_apply_deferred.
	DeferredCount int `json:"deferred_count"`
	DeadCount     int `json:"dead_count"`
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
	disabled  bool // set by StopForUpgrade, cleared by ResumeAfterUpgrade
	wg        sync.WaitGroup
	cancelFn  context.CancelFunc
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
// Includes live counts of deferred and dead rows from sync_apply_deferred.
func (m *Manager) Status() Status {
	m.mu.RLock()
	st := m.status
	m.mu.RUnlock()

	// Phase E: populate deferred/dead counts from store (live query, best-effort).
	if deferred, dead, err := m.store.CountDeferredAndDead(); err == nil {
		st.DeferredCount = deferred
		st.DeadCount = dead
	}
	return st
}

// Stop cancels the internal context and waits for all goroutines to exit.
// Safe to call before Run — returns immediately in that case.
func (m *Manager) Stop() {
	m.mu.Lock()
	fn := m.cancelFn
	m.mu.Unlock()

	if fn != nil {
		fn()
	}
	m.wg.Wait()
}

// StopForUpgrade sets PhaseDisabled and prevents further cycles.
// The sync lease is NOT released so no other worker picks it up during upgrade.
func (m *Manager) StopForUpgrade(project string) error {
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("autosync stop requires project")
	}
	m.mu.Lock()
	m.disabled = true
	m.status.Phase = PhaseDisabled
	m.status.ReasonCode = constants.ReasonPaused
	m.status.ReasonMessage = fmt.Sprintf("autosync paused for cloud upgrade rollback on project %q", project)
	m.mu.Unlock()
	return nil
}

// ResumeAfterUpgrade clears the disabled flag and sets phase to PhaseIdle,
// re-arming the run loop without requiring a full Manager restart.
// If the Manager was not disabled, this is a no-op.
func (m *Manager) ResumeAfterUpgrade(project string) error {
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("autosync resume requires project")
	}
	m.mu.Lock()
	if !m.disabled {
		m.mu.Unlock()
		return nil
	}
	m.disabled = false
	m.status.Phase = PhaseIdle
	m.status.ReasonCode = ""
	m.status.ReasonMessage = ""
	m.mu.Unlock()
	// Send a dirty signal to wake up the run loop.
	m.NotifyDirty()
	return nil
}

// Run is the main loop. It blocks until the context is cancelled or Stop() is called.
// On shutdown it releases the lease and returns.
// The run body is wrapped in recover() — a panic inside cycle() sets PhaseBackoff
// with reason_code=internal_error and logs the stack trace.
// BW4: Re-entry guard — a second concurrent Run call returns immediately.
func (m *Manager) Run(ctx context.Context) {
	// BW4: Guard against concurrent Run calls. If cancelFn is already set,
	// the manager is already running — return immediately.
	m.mu.Lock()
	if m.cancelFn != nil {
		m.mu.Unlock()
		return
	}
	innerCtx, cancel := context.WithCancel(ctx)
	m.cancelFn = cancel
	m.mu.Unlock()

	m.wg.Add(1)
	defer m.wg.Done()
	defer cancel()
	defer m.releaseLease()

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
		case <-innerCtx.Done():
			return
		case <-m.dirtyCh:
			if !debounce.Stop() {
				select {
				case <-debounce.C:
				default:
				}
			}
			debounce.Reset(m.cfg.DebounceDuration)
		case <-debounce.C:
			m.safeRun(innerCtx)
		case <-poll.C:
			m.safeRun(innerCtx)
		}
	}
}

// safeRun wraps cycle() in a recover() to prevent goroutine crashes.
func (m *Manager) safeRun(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			log.Printf("[autosync] PANIC in cycle: %v\n%s", r, stack)
			m.mu.Lock()
			m.status.Phase = PhaseBackoff
			m.status.ReasonCode = "internal_error"
			m.status.ReasonMessage = fmt.Sprintf("panic: %v", r)
			m.status.ConsecutiveFailures++
			bu := time.Now().Add(m.computeBackoff(m.status.ConsecutiveFailures))
			m.status.BackoffUntil = &bu
			m.mu.Unlock()
		}
	}()
	m.cycle(ctx)
}

// ─── Core Cycle ──────────────────────────────────────────────────────────────

func (m *Manager) cycle(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	// Don't run if disabled by StopForUpgrade.
	m.mu.RLock()
	isDisabled := m.disabled
	failures := m.status.ConsecutiveFailures
	backoffUntil := m.status.BackoffUntil
	m.mu.RUnlock()

	if isDisabled {
		return
	}

	// Check if we've exceeded the failure ceiling — enters PhaseBackoff.
	if failures >= m.cfg.MaxConsecutiveFailures {
		m.setPhase(PhaseBackoff)
		return
	}

	// Respect backoff timing — skip cycle without changing phase.
	if backoffUntil != nil && time.Now().Before(*backoffUntil) {
		return
	}

	// Acquire lease.
	now := time.Now().UTC()
	acquired, err := m.store.AcquireSyncLease(m.cfg.TargetKey, m.cfg.LeaseOwner, m.cfg.LeaseInterval, now)
	if err != nil || !acquired {
		return
	}
	m.mu.Lock()
	m.leaseHeld = true
	m.mu.Unlock()

	// Push, then pull.
	if err := m.push(ctx); err != nil {
		var blocked *nonEnrolledPendingError
		if errors.As(err, &blocked) {
			m.recordBlocked(err.Error(), constants.ReasonNonEnrolledPendingMutations)
			return
		}
		reasonCode := classifyTransportError(err)
		m.recordFailureWithReason(autosyncFailureMessage(m.cfg.TargetKey, fmt.Sprintf("push: %v", err), err), reasonCode)
		return
	}

	if err := m.pull(ctx); err != nil {
		reasonCode := classifyTransportError(err)
		m.recordFailureWithReason(autosyncFailureMessage(m.cfg.TargetKey, fmt.Sprintf("pull: %v", err), err), reasonCode)
		return
	}

	m.recordSuccess()
}

// classifyTransportError inspects an error and returns the appropriate reason_code.
// BW5: 401 → "auth_required", 403 → "policy_forbidden", otherwise "transport_failed".
func classifyTransportError(err error) string {
	if err == nil {
		return ""
	}
	var statusErr transportStatusError
	// Walk the error chain looking for a transportStatusError.
	// We use errors.As with interface assertion since errors.As works on interfaces in Go 1.20+.
	if te, ok := unwrapTransportStatusError(err); ok {
		statusErr = te
	}
	if statusErr != nil {
		if statusErr.IsAuthFailure() {
			return "auth_required"
		}
		if statusErr.IsPolicyFailure() {
			return "policy_forbidden"
		}
	}
	return "transport_failed"
}

func autosyncFailureMessage(targetKey, message string, err error) string {
	project := syncguidance.ProjectFromError(err)
	if project == "" {
		project = syncguidance.ProjectFromTargetKey(targetKey)
	}
	return syncguidance.AppendGuidance(message, project, err)
}

// unwrapTransportStatusError walks the error chain looking for transportStatusError.
func unwrapTransportStatusError(err error) (transportStatusError, bool) {
	for err != nil {
		if te, ok := err.(transportStatusError); ok {
			return te, true
		}
		err = errors.Unwrap(err)
	}
	return nil, false
}

// ─── Push ────────────────────────────────────────────────────────────────────

func (m *Manager) push(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	m.setPhase(PhasePushing)

	pending, err := m.store.ListPendingSyncMutations(m.cfg.TargetKey, m.cfg.PushBatchSize)
	if err != nil {
		return fmt.Errorf("list pending: %w", err)
	}
	if len(pending) == 0 {
		counts, err := m.store.CountPendingNonEnrolledSyncMutations(m.cfg.TargetKey)
		if err != nil {
			return fmt.Errorf("count pending non-enrolled mutations: %w", err)
		}
		if len(counts) > 0 {
			return &nonEnrolledPendingError{counts: counts}
		}
		return nil
	}

	// Group by project (preserve order).
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
		entries := make([]MutationEntry, len(batch))
		seqs := make([]int64, len(batch))
		for i, mut := range batch {
			entries[i] = MutationEntry{
				Project:   mut.Project,
				Entity:    mut.Entity,
				EntityKey: mut.EntityKey,
				Op:        mut.Op,
				Payload:   json.RawMessage(mut.Payload),
			}
			seqs[i] = mut.Seq
		}

		_, err := m.transport.PushMutations(entries)
		if err != nil {
			return fmt.Errorf("transport push project %q: %w", project, err)
		}
		if err := m.store.AckSyncMutationSeqs(m.cfg.TargetKey, seqs); err != nil {
			return fmt.Errorf("ack project %q: %w", project, err)
		}
	}

	return nil
}

// ─── Pull ────────────────────────────────────────────────────────────────────

func (m *Manager) pull(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	m.setPhase(PhasePulling)

	// Phase E: replay deferred relation rows before fetching new mutations.
	// This gives previously-deferred rows a chance to apply now that their
	// referenced observations may have arrived.
	if res, err := m.store.ReplayDeferred(); err != nil {
		log.Printf("[autosync] replayDeferred error: %v", err)
		// Non-fatal: log and continue — deferred replay failures must not halt pulls.
	} else if res.Retried > 0 {
		log.Printf("[autosync] replayDeferred: retried=%d succeeded=%d failed=%d dead=%d",
			res.Retried, res.Succeeded, res.Failed, res.Dead)
	}

	state, err := m.store.GetSyncState(m.cfg.TargetKey)
	if err != nil {
		return fmt.Errorf("get sync state: %w", err)
	}

	sinceSeq := state.LastPulledSeq

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
			// Phase E: per-entity error policy (design §9).
			// ApplyPulledMutation handles relation FK misses internally by writing
			// to sync_apply_deferred and returning nil — the cursor advances normally.
			// All other errors (legacy entities, decode errors) propagate and halt the pull.
			if err := m.store.ApplyPulledMutation(m.cfg.TargetKey, localMut); err != nil {
				return fmt.Errorf("apply pulled mutation seq=%d: %w", rm.Seq, err)
			}
			if rm.Seq > sinceSeq {
				sinceSeq = rm.Seq
			}
		}

		if !resp.HasMore {
			break
		}
	}

	return nil
}

// ─── State Tracking ──────────────────────────────────────────────────────────

func (m *Manager) setPhase(phase string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Phase = phase
}

// recordFailureWithReason records a failure with an explicit reason code.
// BW5: Allows specific reason codes (auth_required, policy_forbidden) to surface
// in Manager.Status() so callers can distinguish auth errors from transport errors.
func (m *Manager) recordFailureWithReason(msg, reasonCode string) {
	m.mu.Lock()
	failures := m.status.ConsecutiveFailures + 1
	m.status.ConsecutiveFailures = failures
	m.status.LastError = msg
	m.status.ReasonCode = reasonCode

	backoff := m.computeBackoff(failures)
	bu := time.Now().Add(backoff)
	m.status.BackoffUntil = &bu

	if m.status.Phase == PhasePushing {
		m.status.Phase = PhasePushFailed
	} else {
		m.status.Phase = PhasePullFailed
	}
	m.mu.Unlock()

	_ = m.store.MarkSyncFailure(m.cfg.TargetKey, msg, bu)
}

func (m *Manager) recordBlocked(msg, reasonCode string) {
	m.mu.Lock()
	m.status.Phase = PhasePushFailed
	m.status.LastError = msg
	m.status.ReasonCode = reasonCode
	m.status.ReasonMessage = msg
	m.status.BackoffUntil = nil
	m.mu.Unlock()

	_ = m.store.MarkSyncBlocked(m.cfg.TargetKey, reasonCode, msg)
}

func (m *Manager) recordSuccess() {
	now := time.Now()
	m.mu.Lock()
	m.status.Phase = PhaseHealthy
	m.status.ConsecutiveFailures = 0
	m.status.LastError = ""
	m.status.BackoffUntil = nil
	m.status.LastSyncAt = &now
	m.status.ReasonCode = ""
	m.status.ReasonMessage = ""
	m.mu.Unlock()

	_ = m.store.MarkSyncHealthy(m.cfg.TargetKey)
}

func nonEnrolledPendingMessage(counts []store.PendingSyncMutationProjectCount) string {
	parts := make([]string, 0, len(counts))
	for _, count := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", count.Project, count.Count))
	}
	return fmt.Sprintf("pending cloud sync mutations are blocked because project(s) are not enrolled: %s. Run `engram cloud enroll <project>` for each intended project or review enrollment.", strings.Join(parts, ", "))
}

// computeBackoff returns exponential backoff with ±25% jitter.
// Formula: min(base * 2^(failures-1), maxBackoff) ± jitter where jitter ∈ [-base*0.25, +base*0.25]
// BW1: ±25% means jitter can be negative, so result ∈ [base*0.75, base*1.25].
func (m *Manager) computeBackoff(failures int) time.Duration {
	if failures <= 0 {
		return m.cfg.BaseBackoff
	}
	exp := math.Pow(2, float64(failures-1))
	base := time.Duration(float64(m.cfg.BaseBackoff) * exp)
	if base > m.cfg.MaxBackoff {
		base = m.cfg.MaxBackoff
	}
	// ±25% jitter: uniform in [-base/4, +base/4].
	// rand.Int63n(int64(base/2)+1) gives [0, base/2]; subtracting base/4 shifts to [-base/4, +base/4].
	jitter := time.Duration(rand.Int63n(int64(base/2)+1)) - time.Duration(base/4)
	result := base + jitter
	if result > m.cfg.MaxBackoff {
		result = m.cfg.MaxBackoff
	}
	// Floor at BaseBackoff/2 to avoid extremely short intervals on large negative jitter.
	if result < m.cfg.BaseBackoff/2 {
		result = m.cfg.BaseBackoff / 2
	}
	return result
}

func (m *Manager) releaseLease() {
	m.mu.Lock()
	m.leaseHeld = false
	m.mu.Unlock()
	_ = m.store.ReleaseSyncLease(m.cfg.TargetKey, m.cfg.LeaseOwner)
}
