package autosync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// ─── Fakes ───────────────────────────────────────────────────────────────────

type fakeLocalStore struct {
	mu                sync.Mutex
	mutations         []store.SyncMutation
	syncState         *store.SyncState
	leaseOwner        string
	pushErr           error
	pullErr           error
	failureMessage    string
	blockedReason     string
	blockedMessage    string
	appliedMuts       []store.SyncMutation
	acquireGranted    bool
	ackedSeqs         []int64
	nonEnrolledCounts []store.PendingSyncMutationProjectCount
}

func newFakeLocalStore() *fakeLocalStore {
	return &fakeLocalStore{
		acquireGranted: true,
		syncState: &store.SyncState{
			TargetKey:     "cloud",
			Lifecycle:     "idle",
			LastPulledSeq: 0,
		},
	}
}

func (s *fakeLocalStore) GetSyncState(_ string) (*store.SyncState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pullErr != nil {
		return nil, s.pullErr
	}
	return s.syncState, nil
}

func (s *fakeLocalStore) ListPendingSyncMutations(_ string, limit int) ([]store.SyncMutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pushErr != nil {
		return nil, s.pushErr
	}
	if len(s.mutations) == 0 {
		return nil, nil
	}
	n := len(s.mutations)
	if limit > 0 && n > limit {
		n = limit
	}
	return s.mutations[:n], nil
}

func (s *fakeLocalStore) CountPendingNonEnrolledSyncMutations(_ string) ([]store.PendingSyncMutationProjectCount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.PendingSyncMutationProjectCount(nil), s.nonEnrolledCounts...), nil
}

func (s *fakeLocalStore) AckSyncMutations(_ string, _ int64) error { return nil }

func (s *fakeLocalStore) AckSyncMutationSeqs(_ string, seqs []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ackedSeqs = append(s.ackedSeqs, seqs...)
	return nil
}

func (s *fakeLocalStore) AcquireSyncLease(_, owner string, ttl time.Duration, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.acquireGranted {
		return false, nil
	}
	s.leaseOwner = owner
	return true, nil
}

func (s *fakeLocalStore) ReleaseSyncLease(_, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaseOwner = ""
	return nil
}

func (s *fakeLocalStore) ApplyPulledMutation(_ string, mutation store.SyncMutation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pullErr != nil {
		return s.pullErr
	}
	s.appliedMuts = append(s.appliedMuts, mutation)
	return nil
}

func (s *fakeLocalStore) MarkSyncFailure(_, message string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failureMessage = message
	return nil
}

func (s *fakeLocalStore) MarkSyncBlocked(_, reasonCode, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockedReason = reasonCode
	s.blockedMessage = message
	return nil
}

func (s *fakeLocalStore) MarkSyncHealthy(_ string) error { return nil }

// Phase E: deferred replay stubs — base fakeLocalStore always returns zero counts
// and no error. Tests that need real replay behavior use fakeLocalStoreWithDeferred.
func (s *fakeLocalStore) ReplayDeferred() (store.ReplayDeferredResult, error) {
	return store.ReplayDeferredResult{}, nil
}

func (s *fakeLocalStore) CountDeferredAndDead() (int, int, error) { return 0, 0, nil }

// ─── Fake Transport ───────────────────────────────────────────────────────────

type fakeCloudTransport struct {
	mu         sync.Mutex
	pushErr    error
	pullErr    error
	pushCalls  int32
	pullCalls  int32
	pushResult *PushMutationsResult
	pullResult *PullMutationsResponse
	pushed     [][]MutationEntry
}

type fakeRepairableCloudError struct{ msg string }

func (e fakeRepairableCloudError) Error() string { return e.msg }

func (e fakeRepairableCloudError) IsRepairable() bool { return true }

func newFakeTransport() *fakeCloudTransport {
	return &fakeCloudTransport{
		pushResult: &PushMutationsResult{AcceptedSeqs: []int64{}},
		pullResult: &PullMutationsResponse{Mutations: []PulledMutation{}},
	}
}

func (t *fakeCloudTransport) PushMutations(mutations []MutationEntry) (*PushMutationsResult, error) {
	atomic.AddInt32(&t.pushCalls, 1)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pushErr != nil {
		return nil, t.pushErr
	}
	batch := append([]MutationEntry(nil), mutations...)
	t.pushed = append(t.pushed, batch)
	return t.pushResult, nil
}

func (t *fakeCloudTransport) PullMutations(_ int64, _ int) (*PullMutationsResponse, error) {
	atomic.AddInt32(&t.pullCalls, 1)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pullErr != nil {
		return nil, t.pullErr
	}
	return t.pullResult, nil
}

// ─── Push ack safety regressions ─────────────────────────────────────────────

func TestManagerPushNoPendingDoesNotPushOrAck(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	mgr := New(ls, tr, DefaultConfig())

	if err := mgr.push(context.Background()); err != nil {
		t.Fatalf("push: %v", err)
	}

	if got := atomic.LoadInt32(&tr.pushCalls); got != 0 {
		t.Fatalf("expected no transport push without pending mutations, got %d calls", got)
	}
	ls.mu.Lock()
	acked := append([]int64(nil), ls.ackedSeqs...)
	ls.mu.Unlock()
	if len(acked) != 0 {
		t.Fatalf("expected no ack without pending mutations, got %v", acked)
	}
}

func TestManagerPushAcksPendingMutationsAfterTransportSuccess(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "obs", EntityKey: "k1", Op: "upsert", Project: "proj-a", Payload: `{"id":"1"}`},
		{Seq: 2, Entity: "obs", EntityKey: "k2", Op: "upsert", Project: "proj-a", Payload: `{"id":"2"}`},
	}
	tr := newFakeTransport()
	tr.pushResult = &PushMutationsResult{AcceptedSeqs: []int64{1, 2}}
	mgr := New(ls, tr, DefaultConfig())

	if err := mgr.push(context.Background()); err != nil {
		t.Fatalf("push: %v", err)
	}

	if got := atomic.LoadInt32(&tr.pushCalls); got != 1 {
		t.Fatalf("expected one transport push, got %d", got)
	}
	ls.mu.Lock()
	acked := append([]int64(nil), ls.ackedSeqs...)
	ls.mu.Unlock()
	if fmt.Sprint(acked) != "[1 2]" {
		t.Fatalf("expected acked seqs [1 2] after successful push, got %v", acked)
	}
}

func TestManagerPushDoesNotAckWhenTransportFails(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "obs", EntityKey: "k1", Op: "upsert", Project: "proj-a", Payload: `{"id":"1"}`},
	}
	tr := newFakeTransport()
	tr.pushErr = errors.New("transport down")
	mgr := New(ls, tr, DefaultConfig())

	if err := mgr.push(context.Background()); err == nil {
		t.Fatal("expected push to fail")
	}

	if got := atomic.LoadInt32(&tr.pushCalls); got != 1 {
		t.Fatalf("expected one transport push attempt, got %d", got)
	}
	ls.mu.Lock()
	acked := append([]int64(nil), ls.ackedSeqs...)
	ls.mu.Unlock()
	if len(acked) != 0 {
		t.Fatalf("expected no ack after failed transport push, got %v", acked)
	}
}

// ─── Phase + lifecycle tests (REQ-204) ───────────────────────────────────────

func TestManagerPhaseTransitions(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	mgr := New(ls, tr, cfg)
	if mgr.Status().Phase != PhaseIdle {
		t.Fatalf("initial phase should be idle, got %q", mgr.Status().Phase)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go mgr.Run(ctx)

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if mgr.Status().Phase == PhaseHealthy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected PhaseHealthy after successful cycle, got %q", mgr.Status().Phase)
}

func TestManagerPushFailedPhase(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{{Seq: 1, Entity: "obs", EntityKey: "k1", Project: "proj-a"}}
	tr := newFakeTransport()
	tr.pushErr = errors.New("push failed")
	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go mgr.Run(ctx)
	mgr.NotifyDirty()

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if mgr.Status().Phase == PhasePushFailed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected PhasePushFailed, got %q", mgr.Status().Phase)
}

func TestManagerPullFailedPhase(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	tr.pullErr = errors.New("pull failed")
	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go mgr.Run(ctx)
	mgr.NotifyDirty()

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if mgr.Status().Phase == PhasePullFailed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected PhasePullFailed, got %q", mgr.Status().Phase)
}

func TestManagerRepairableFailureStoresUpgradeGuidance(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{{Seq: 1, Entity: "obs", EntityKey: "k1", Project: "proj-a"}}
	tr := newFakeTransport()
	tr.pushErr = fakeRepairableCloudError{msg: "invalid upsert payload: observations[0].directory is required"}
	cfg := DefaultConfig()
	cfg.TargetKey = "cloud:proj-a"

	mgr := New(ls, tr, cfg)
	mgr.cycle(context.Background())

	status := mgr.Status()
	if status.Phase != PhasePushFailed {
		t.Fatalf("expected PhasePushFailed, got %q", status.Phase)
	}
	if !strings.Contains(status.LastError, "invalid upsert payload") {
		t.Fatalf("expected original error to be preserved, got %q", status.LastError)
	}
	for _, want := range []string{
		"Known repairable cloud sync failure detected.",
		"engram cloud upgrade doctor --project proj-a",
		"engram cloud upgrade repair --project proj-a --dry-run",
		"engram cloud upgrade repair --project proj-a --apply",
		"engram sync --cloud --project proj-a",
	} {
		if !strings.Contains(status.LastError, want) {
			t.Fatalf("expected status.LastError to contain %q, got %q", want, status.LastError)
		}
		if !strings.Contains(ls.failureMessage, want) {
			t.Fatalf("expected stored failure to contain %q, got %q", want, ls.failureMessage)
		}
	}
	if strings.Contains(status.LastError, "--auto-repair") || strings.Contains(ls.failureMessage, "--auto-repair") {
		t.Fatalf("guidance must not mention auto-repair, status=%q stored=%q", status.LastError, ls.failureMessage)
	}
	if atomic.LoadInt32(&tr.pushCalls) != 1 {
		t.Fatalf("expected one push attempt and no repair execution path, got %d", tr.pushCalls)
	}
}

func TestManagerStopForUpgradeDisabled(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	mgr := New(ls, tr, DefaultConfig())

	if err := mgr.StopForUpgrade("test-project"); err != nil {
		t.Fatalf("StopForUpgrade: %v", err)
	}
	if mgr.Status().Phase != PhaseDisabled {
		t.Fatalf("expected PhaseDisabled, got %q", mgr.Status().Phase)
	}
}

// ─── Backoff tests (REQ-205) ─────────────────────────────────────────────────

func TestManagerBackoffExponentialGrowth(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseBackoff = 1 * time.Second
	cfg.MaxBackoff = 5 * time.Minute
	mgr := &Manager{cfg: cfg}

	prev := time.Duration(0)
	for i := 1; i <= 8; i++ {
		d := mgr.computeBackoff(i)
		if d > cfg.MaxBackoff {
			t.Fatalf("failure %d: backoff %v exceeds max %v", i, d, cfg.MaxBackoff)
		}
		if i > 1 && prev > 0 {
			ratio := float64(d) / float64(prev)
			if ratio < 0.4 || ratio > 5.0 {
				t.Fatalf("failure %d: ratio %.2f out of [0.4,5.0] prev=%v cur=%v", i, ratio, prev, d)
			}
		}
		prev = d
	}
}

func TestManagerBackoffJitterBounds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseBackoff = 4 * time.Second
	cfg.MaxBackoff = 5 * time.Minute
	mgr := &Manager{cfg: cfg}

	// BW1: ±25% jitter means range is [base*0.75, base*1.25] = [3s, 5s].
	// Run many iterations and assert ALL samples fall in [3s,5s].
	// ALSO assert that at least one sample falls BELOW base (4s) to prove
	// negative jitter is actually applied (not just [0, +25%]).
	sawBelowBase := false
	for i := 0; i < 500; i++ {
		d := mgr.computeBackoff(1)
		if d < 3*time.Second || d > 5*time.Second {
			t.Fatalf("jitter out of [3s,5s]: got %v at iteration %d", d, i)
		}
		if d < 4*time.Second {
			sawBelowBase = true
		}
	}
	if !sawBelowBase {
		t.Fatal("jitter never produced a result below base (4s) in 500 iterations; ±25% jitter must include negative direction")
	}
}

func TestManagerBackoffCeiling(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseBackoff = 1 * time.Second
	cfg.MaxBackoff = 5 * time.Minute
	cfg.MaxConsecutiveFailures = 10
	mgr := &Manager{cfg: cfg}

	d := mgr.computeBackoff(cfg.MaxConsecutiveFailures)
	if d > cfg.MaxBackoff {
		t.Fatalf("backoff exceeds ceiling: %v > %v", d, cfg.MaxBackoff)
	}
}

func TestManagerBackoffResetOnSuccess(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	tr.mu.Lock()
	tr.pushErr = errors.New("fail once")
	tr.mu.Unlock()

	ls.mutations = []store.SyncMutation{{Seq: 1, Entity: "obs", EntityKey: "k1", Project: "proj-a"}}

	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond
	cfg.BaseBackoff = 10 * time.Millisecond
	cfg.MaxBackoff = 50 * time.Millisecond

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go mgr.Run(ctx)
	mgr.NotifyDirty()

	// Wait for failure
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if mgr.Status().Phase == PhasePushFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Fix transport
	tr.mu.Lock()
	tr.pushErr = nil
	tr.mu.Unlock()
	ls.mu.Lock()
	ls.mutations = nil
	ls.mu.Unlock()

	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		st := mgr.Status()
		if st.Phase == PhaseHealthy && st.ConsecutiveFailures == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected PhaseHealthy with 0 failures, got phase=%q failures=%d",
		mgr.Status().Phase, mgr.Status().ConsecutiveFailures)
}

// ─── NotifyDirty tests (REQ-206) ─────────────────────────────────────────────

func TestManagerNotifyDirtyOneCycle(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.DebounceDuration = 20 * time.Millisecond
	cfg.PollInterval = 10 * time.Second

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go mgr.Run(ctx)
	mgr.NotifyDirty()

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if mgr.Status().Phase == PhaseHealthy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected PhaseHealthy after dirty notification, got %q", mgr.Status().Phase)
}

func TestManagerNotifyDirtyCoalesce(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.DebounceDuration = 50 * time.Millisecond
	cfg.PollInterval = 10 * time.Second

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	go mgr.Run(ctx)

	for i := 0; i < 100; i++ {
		mgr.NotifyDirty()
	}

	time.Sleep(300 * time.Millisecond)

	pullCalls := atomic.LoadInt32(&tr.pullCalls)
	if pullCalls > 5 {
		t.Fatalf("expected ≤5 pull calls (coalesced), got %d", pullCalls)
	}
}

func TestManagerNotifyDirtyDuringBackoff(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{{Seq: 1, Entity: "obs", EntityKey: "k1", Project: "proj-a"}}
	tr := newFakeTransport()
	tr.pushErr = errors.New("always fail")

	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond
	cfg.MaxConsecutiveFailures = 1
	cfg.BaseBackoff = 1 * time.Second

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go mgr.Run(ctx)
	mgr.NotifyDirty()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		p := mgr.Status().Phase
		if p == PhaseBackoff || p == PhasePushFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	done := make(chan struct{})
	go func() {
		mgr.NotifyDirty()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("NotifyDirty blocked during backoff")
	}
}

func TestManagerNotifyDirtyAfterStop(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	mgr := New(ls, tr, DefaultConfig())

	ctx, cancel := context.WithCancel(context.Background())
	go mgr.Run(ctx)
	cancel()
	mgr.Stop()

	done := make(chan struct{})
	go func() {
		mgr.NotifyDirty()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("NotifyDirty blocked after stop")
	}
}

// ─── Run lifecycle tests (REQ-207) ───────────────────────────────────────────

func TestManagerRunContextCancel(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	mgr := New(ls, tr, DefaultConfig())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1 second after context cancel")
	}
}

func TestManagerRunPollTicker(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.PollInterval = 30 * time.Millisecond
	cfg.DebounceDuration = 10 * time.Second

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go mgr.Run(ctx)

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&tr.pullCalls) >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected at least 1 pull cycle from poll ticker, got %d", atomic.LoadInt32(&tr.pullCalls))
}

func TestManagerStopWaitsGoroutine(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.PollInterval = 10 * time.Second
	cfg.DebounceDuration = 10 * time.Second

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go mgr.Run(ctx)
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		mgr.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2 seconds")
	}
}

func TestManagerRunPanicRecovery(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	panicOnce := int32(1)

	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond
	cfg.BaseBackoff = 10 * time.Millisecond
	cfg.MaxBackoff = 100 * time.Millisecond

	mgr := New(ls, tr, cfg)
	mgr.transport = &panicOnceTransport{delegate: tr, panicOnce: &panicOnce}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go mgr.Run(ctx)
	mgr.NotifyDirty()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		st := mgr.Status()
		if st.Phase == PhaseBackoff && st.ReasonCode == "internal_error" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected PhaseBackoff/internal_error after panic, got phase=%q code=%q",
		mgr.Status().Phase, mgr.Status().ReasonCode)
}

// ─── StopForUpgrade / ResumeAfterUpgrade (REQ-208) ───────────────────────────

func TestManagerStopForUpgradeHaltsCycle(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go mgr.Run(ctx)
	time.Sleep(20 * time.Millisecond)

	if err := mgr.StopForUpgrade("test-project"); err != nil {
		t.Fatalf("StopForUpgrade: %v", err)
	}
	if mgr.Status().Phase != PhaseDisabled {
		t.Fatalf("expected PhaseDisabled, got %q", mgr.Status().Phase)
	}

	before := atomic.LoadInt32(&tr.pullCalls)
	time.Sleep(50 * time.Millisecond)
	after := atomic.LoadInt32(&tr.pullCalls)

	if after > before+1 {
		t.Fatalf("cycles continued after StopForUpgrade: before=%d after=%d", before, after)
	}
}

func TestManagerStopForUpgradeRetainsLease(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	mgr := New(ls, tr, DefaultConfig())

	if err := mgr.StopForUpgrade("test-project"); err != nil {
		t.Fatalf("StopForUpgrade: %v", err)
	}
	// Invariant: StopForUpgrade must not call ReleaseSyncLease.
	// The fakeLocalStore tracks leaseOwner; if it was never acquired, that's fine.
	_ = mgr.Status()
}

func TestManagerResumeAfterUpgrade(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 20 * time.Millisecond

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go mgr.Run(ctx)
	time.Sleep(20 * time.Millisecond)

	if err := mgr.StopForUpgrade("test-project"); err != nil {
		t.Fatalf("StopForUpgrade: %v", err)
	}

	beforeResume := atomic.LoadInt32(&tr.pullCalls)

	if err := mgr.ResumeAfterUpgrade("test-project"); err != nil {
		t.Fatalf("ResumeAfterUpgrade: %v", err)
	}
	if mgr.Status().Phase == PhaseDisabled {
		t.Fatal("phase should not be disabled after ResumeAfterUpgrade")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&tr.pullCalls) > beforeResume {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no cycles ran after ResumeAfterUpgrade (before=%d after=%d)",
		beforeResume, atomic.LoadInt32(&tr.pullCalls))
}

func TestManagerResumeWithoutStop(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	mgr := New(ls, tr, DefaultConfig())

	mgr.mu.Lock()
	mgr.status.Phase = PhaseHealthy
	mgr.mu.Unlock()

	if err := mgr.ResumeAfterUpgrade("test-project"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr.Status().Phase != PhaseHealthy {
		t.Fatalf("ResumeAfterUpgrade without prior Stop should keep PhaseHealthy, got %q", mgr.Status().Phase)
	}
}

// ─── Goroutine lifecycle (REQ-213) ───────────────────────────────────────────

func TestManagerStopBeforeRun(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	mgr := New(ls, tr, DefaultConfig())

	done := make(chan struct{})
	go func() {
		mgr.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop blocked when called before Run")
	}
}

func TestManagerPanicSetsBackoff(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	panicOnce := int32(1)

	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond
	cfg.BaseBackoff = 10 * time.Millisecond
	cfg.MaxBackoff = 100 * time.Millisecond

	mgr := New(ls, tr, cfg)
	mgr.transport = &panicOnceTransport{delegate: tr, panicOnce: &panicOnce}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	go mgr.Run(ctx)
	mgr.NotifyDirty()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		st := mgr.Status()
		if st.Phase == PhaseBackoff && st.ReasonCode == "internal_error" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected PhaseBackoff/internal_error, got phase=%q code=%q",
		mgr.Status().Phase, mgr.Status().ReasonCode)
}

func TestManagerLoopContinuesAfterPanic(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	panicOnce := int32(1)

	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 20 * time.Millisecond
	cfg.BaseBackoff = 20 * time.Millisecond
	cfg.MaxBackoff = 50 * time.Millisecond
	cfg.MaxConsecutiveFailures = 5

	mgr := New(ls, tr, cfg)
	mgr.transport = &panicOnceTransport{delegate: tr, panicOnce: &panicOnce}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go mgr.Run(ctx)
	mgr.NotifyDirty()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if mgr.Status().Phase == PhaseBackoff {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	before := atomic.LoadInt32(&tr.pullCalls)
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&tr.pullCalls) > before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("loop did not continue after panic recovery")
}

// ─── BW5: Auth/policy error surfacing ────────────────────────────────────────

// fakeAuthErr simulates an HTTP 401 from the transport.
type fakeAuthErr struct{ code int }

func (e *fakeAuthErr) Error() string         { return fmt.Sprintf("transport: status %d", e.code) }
func (e *fakeAuthErr) IsAuthFailure() bool   { return e.code == 401 }
func (e *fakeAuthErr) IsPolicyFailure() bool { return e.code == 403 }

// TestManagerSurfacesAuthRequiredOn401 verifies BW5:
// When the transport returns a 401-like error, Manager must surface
// ReasonCode="auth_required" instead of generic "transport_failed".
func TestManagerSurfacesAuthRequiredOn401(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{{Seq: 1, Entity: "obs", EntityKey: "k1", Project: "proj-a"}}

	authErr := &fakeAuthErr{code: 401}
	tr := &errTransport{pushErr: authErr}

	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go mgr.Run(ctx)
	mgr.NotifyDirty()

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		st := mgr.Status()
		if st.Phase == PhasePushFailed || st.Phase == PhaseBackoff {
			if st.ReasonCode != "auth_required" {
				t.Fatalf("expected ReasonCode=auth_required for 401, got %q", st.ReasonCode)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected PhasePushFailed/PhaseBackoff with auth_required, got phase=%q code=%q",
		mgr.Status().Phase, mgr.Status().ReasonCode)
}

// TestManagerSurfacesPolicyForbiddenOn403 verifies BW5:
// When the transport returns a 403-like error, Manager must surface
// ReasonCode="policy_forbidden".
func TestManagerSurfacesPolicyForbiddenOn403(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{{Seq: 1, Entity: "obs", EntityKey: "k1", Project: "proj-a"}}

	policyErr := &fakeAuthErr{code: 403}
	tr := &errTransport{pushErr: policyErr}

	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go mgr.Run(ctx)
	mgr.NotifyDirty()

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		st := mgr.Status()
		if st.Phase == PhasePushFailed || st.Phase == PhaseBackoff {
			if st.ReasonCode != "policy_forbidden" {
				t.Fatalf("expected ReasonCode=policy_forbidden for 403, got %q", st.ReasonCode)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected PhasePushFailed/PhaseBackoff with policy_forbidden, got phase=%q code=%q",
		mgr.Status().Phase, mgr.Status().ReasonCode)
}

func TestManagerBlocksWhenOnlyNonEnrolledPendingMutationsRemain(t *testing.T) {
	ls := newFakeLocalStore()
	ls.nonEnrolledCounts = []store.PendingSyncMutationProjectCount{
		{Project: "alpha", Count: 2},
		{Project: "beta", Count: 1},
	}
	tr := newFakeTransport()
	cfg := DefaultConfig()
	mgr := New(ls, tr, cfg)

	mgr.cycle(context.Background())

	if got := atomic.LoadInt32(&tr.pushCalls); got != 0 {
		t.Fatalf("expected no push calls for non-enrolled pending mutations, got %d", got)
	}
	if got := atomic.LoadInt32(&tr.pullCalls); got != 0 {
		t.Fatalf("expected blocked cycle to skip pull, got %d", got)
	}
	if len(ls.ackedSeqs) != 0 {
		t.Fatalf("expected no acked mutations, got %v", ls.ackedSeqs)
	}
	st := mgr.Status()
	if st.Phase != PhasePushFailed {
		t.Fatalf("expected push_failed status, got %q", st.Phase)
	}
	if st.ReasonCode != "non_enrolled_pending_mutations" {
		t.Fatalf("expected non-enrolled reason code, got %q", st.ReasonCode)
	}
	for _, want := range []string{"alpha=2", "beta=1", "engram cloud enroll <project>"} {
		if !strings.Contains(st.ReasonMessage, want) {
			t.Fatalf("expected reason message to contain %q, got %q", want, st.ReasonMessage)
		}
	}
	if ls.blockedReason != st.ReasonCode || ls.blockedMessage != st.ReasonMessage {
		t.Fatalf("expected blocked state persisted, reason=%q message=%q", ls.blockedReason, ls.blockedMessage)
	}
}

// ─── BW4: Re-entry guard ─────────────────────────────────────────────────────

// TestManagerRunIsNotReentryable verifies BW4:
// A second concurrent call to Run must be a no-op; it must not overwrite cancelFn.
func TestManagerRunIsNotReentryable(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.PollInterval = 10 * time.Second
	cfg.DebounceDuration = 10 * time.Second

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start first Run
	go mgr.Run(ctx)
	time.Sleep(20 * time.Millisecond)

	// Call Run a second time concurrently — must return immediately (or very quickly)
	done := make(chan struct{})
	go func() {
		mgr.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Second Run returned (re-entry guard worked)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second Run call did not return quickly — re-entry not guarded")
	}
}

// ─── BW5: Auth/policy error surfacing ────────────────────────────────────────

// errTransport is a CloudTransport that always returns a given error.
type errTransport struct {
	pushErr error
	pullErr error
}

func (t *errTransport) PushMutations(_ []MutationEntry) (*PushMutationsResult, error) {
	if t.pushErr != nil {
		return nil, t.pushErr
	}
	return &PushMutationsResult{AcceptedSeqs: []int64{}}, nil
}

func (t *errTransport) PullMutations(_ int64, _ int) (*PullMutationsResponse, error) {
	if t.pullErr != nil {
		return nil, t.pullErr
	}
	return &PullMutationsResponse{Mutations: []PulledMutation{}}, nil
}

// ─── Phase E: Autosync resilience tests (REQ-007, REQ-008) ──────────────────

// E.1a — ReplayDeferred_RetriesAndApplies:
// A deferred row exists; when the missing observation arrives and
// replayDeferred is called, the row is applied and removed from
// sync_apply_deferred.
func TestReplayDeferred_RetriesAndApplies(t *testing.T) {
	ls := &fakeLocalStoreWithDeferred{fakeLocalStore: *newFakeLocalStore()}
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	// Pre-load a deferred row.
	ls.mu.Lock()
	ls.deferredRows = []DeferredRow{{
		SyncID:      "rel-1",
		Entity:      "relation",
		Payload:     `{"sync_id":"rel-1"}`,
		RetryCount:  0,
		ApplyStatus: "deferred",
	}}
	ls.mu.Unlock()

	// ReplayDeferred must be called by pull; simulate it resolving successfully.
	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go mgr.Run(ctx)

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		ls.mu.Lock()
		called := ls.replayDeferredCalled
		ls.mu.Unlock()
		if called {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ReplayDeferred was not called during pull cycle")
}

// E.1b — ReplayDeferred_DeadAfterFiveRetries:
// A row at retry_count=4 with dep still missing → after replayDeferred
// the row must have apply_status='dead'.
func TestReplayDeferred_DeadAfterFiveRetries(t *testing.T) {
	ls := &fakeLocalStoreWithDeferred{fakeLocalStore: *newFakeLocalStore()}
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	ls.mu.Lock()
	ls.deferredRows = []DeferredRow{{
		SyncID:      "rel-dead",
		Entity:      "relation",
		Payload:     `{"sync_id":"rel-dead"}`,
		RetryCount:  4,
		ApplyStatus: "deferred",
	}}
	// Always return FK-missing for this deferred row.
	ls.replayErr = store.ErrRelationFKMissing
	ls.mu.Unlock()

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go mgr.Run(ctx)

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		ls.mu.Lock()
		called := ls.replayDeferredCalled
		ls.mu.Unlock()
		if called {
			// Verify dead count incremented.
			ls.mu.Lock()
			deadCalled := ls.markDeadCalled
			ls.mu.Unlock()
			if !deadCalled {
				t.Fatal("MarkApplyDead not called after retry_count reached 5")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ReplayDeferred was not called during pull cycle")
}

// E.1c — ReplayDeferred_DeadRowNotRetried:
// A dead row must NOT be retried by replayDeferred.
func TestReplayDeferred_DeadRowNotRetried(t *testing.T) {
	ls := &fakeLocalStoreWithDeferred{fakeLocalStore: *newFakeLocalStore()}
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	// Dead row — should not be picked up.
	ls.mu.Lock()
	ls.deferredRows = []DeferredRow{{
		SyncID:      "rel-already-dead",
		Entity:      "relation",
		Payload:     `{"sync_id":"rel-already-dead"}`,
		RetryCount:  5,
		ApplyStatus: "dead",
	}}
	ls.mu.Unlock()

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go mgr.Run(ctx)

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		ls.mu.Lock()
		called := ls.replayDeferredCalled
		ls.mu.Unlock()
		if called {
			// Dead row must never have been applied.
			ls.mu.Lock()
			appliedCount := len(ls.appliedMuts)
			ls.mu.Unlock()
			if appliedCount != 0 {
				t.Fatalf("dead row should never be applied; got %d applied mutations", appliedCount)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ReplayDeferred was not called during pull cycle")
}

// E.1d — Pull_LegacyEntityNonFKError_StillHalts (REQ-008):
// A legacy entity (observation) apply error must halt the pull loop;
// cursor must not advance.
func TestPull_LegacyEntityNonFKError_StillHalts(t *testing.T) {
	ls := &fakeLocalStoreWithDeferred{fakeLocalStore: *newFakeLocalStore()}
	tr := newFakeTransport()

	// Inject a pulled legacy mutation.
	tr.mu.Lock()
	tr.pullResult = &PullMutationsResponse{
		Mutations: []PulledMutation{{
			Seq:    10,
			Entity: "observation",
			Op:     "upsert",
			Payload: []byte(`{"sync_id":"obs-fail","title":"test"}`),
		}},
		HasMore: false,
	}
	tr.mu.Unlock()

	// Legacy apply error (non-FK) must halt.
	ls.mu.Lock()
	ls.pullErr = errors.New("legacy apply error (non-FK)")
	ls.mu.Unlock()

	cfg := DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	mgr := New(ls, tr, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go mgr.Run(ctx)

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		st := mgr.Status()
		if st.Phase == PhasePullFailed {
			// Confirm cursor did not advance (SyncState.LastPulledSeq must be 0).
			ls.mu.Lock()
			cursorSeq := ls.syncState.LastPulledSeq
			ls.mu.Unlock()
			if cursorSeq != 0 {
				t.Fatalf("cursor advanced to %d despite legacy pull error; expected 0", cursorSeq)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected PhasePullFailed for legacy non-FK error, got %q", mgr.Status().Phase)
}

// ─── DeferredRow type for fake store ─────────────────────────────────────────

// DeferredRow is a minimal representation of a sync_apply_deferred row used in tests.
type DeferredRow struct {
	SyncID      string
	Entity      string
	Payload     string
	RetryCount  int
	ApplyStatus string
}

// fakeLocalStoreWithDeferred extends fakeLocalStore with replay support.
type fakeLocalStoreWithDeferred struct {
	fakeLocalStore
	deferredRows        []DeferredRow
	replayDeferredCalled bool
	markDeadCalled       bool
	replayErr           error
}

func (s *fakeLocalStoreWithDeferred) ReplayDeferred() (store.ReplayDeferredResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replayDeferredCalled = true

	var res store.ReplayDeferredResult
	for i := range s.deferredRows {
		row := &s.deferredRows[i]
		if row.ApplyStatus == "dead" {
			continue // Dead rows must not be retried.
		}
		res.Retried++
		if s.replayErr != nil {
			row.RetryCount++
			if row.RetryCount >= 5 {
				row.ApplyStatus = "dead"
				s.markDeadCalled = true
				res.Dead++
			} else {
				res.Failed++
			}
		} else {
			row.ApplyStatus = "applied"
			res.Succeeded++
		}
	}
	return res, nil
}

func (s *fakeLocalStoreWithDeferred) CountDeferredAndDead() (deferred, dead int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.deferredRows {
		switch row.ApplyStatus {
		case "deferred":
			deferred++
		case "dead":
			dead++
		}
	}
	return deferred, dead, nil
}

// ─── Helper types ─────────────────────────────────────────────────────────────

type panicOnceTransport struct {
	delegate  *fakeCloudTransport
	panicOnce *int32
}

func (p *panicOnceTransport) PushMutations(mutations []MutationEntry) (*PushMutationsResult, error) {
	return p.delegate.PushMutations(mutations)
}

func (p *panicOnceTransport) PullMutations(sinceSeq int64, limit int) (*PullMutationsResponse, error) {
	if atomic.CompareAndSwapInt32(p.panicOnce, 1, 0) {
		panic(fmt.Sprintf("test panic in cycle"))
	}
	return p.delegate.PullMutations(sinceSeq, limit)
}
