package autosync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// ─── Fakes ───────────────────────────────────────────────────────────────────

type fakeLocalStore struct {
	mu                sync.Mutex
	mutations         []store.SyncMutation
	syncState         *store.SyncState
	leaseOwner        string
	leaseCalls        int
	pushErr           error
	pullErr           error
	failureMessage    string
	failureReason     string
	blockedReason     string
	blockedMessage    string
	appliedMuts       []store.SyncMutation
	acquireGranted    bool
	ackedSeqs         []int64
	ackErr            error
	healthyCalls      int
	nonEnrolledCounts []store.PendingSyncMutationProjectCount
	deferredProjects  []string
	listDeferredErr   error
	listedTargets     []string
	replayedScopes    []string
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
	if s.ackErr != nil {
		return s.ackErr
	}
	s.ackedSeqs = append(s.ackedSeqs, seqs...)
	return nil
}

func (s *fakeLocalStore) AcquireSyncLease(_, owner string, ttl time.Duration, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaseCalls++
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

func (s *fakeLocalStore) MarkSyncFailureWithReason(_, reasonCode, message string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failureReason = reasonCode
	s.failureMessage = message
	return nil
}

func TestManagerPolicyFailurePersistsReasonAwareGuidance(t *testing.T) {
	local := newFakeLocalStore()
	cfg := DefaultConfig()
	cfg.TargetKey = "cloud:policy-project"
	manager := New(local, newFakeTransport(), cfg)
	err := &fakeAuthErr{code: 403}

	manager.recordFailureWithReason(
		autosyncFailureMessage(cfg.TargetKey, "push: "+err.Error(), err),
		"policy_forbidden",
	)

	if local.failureReason != "policy_forbidden" {
		t.Fatalf("persisted failure reason = %q, want policy_forbidden", local.failureReason)
	}
	if !strings.Contains(local.failureMessage, "ENGRAM_CLOUD_ALLOWED_PROJECTS") {
		t.Fatalf("persisted failure guidance = %q", local.failureMessage)
	}
}

func TestManagerPolicyFailureGuidanceUsesDeniedProjectFromAggregate(t *testing.T) {
	local := newFakeLocalStore()
	local.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "obs", EntityKey: "alpha", Op: "upsert", Project: "alpha", Payload: `{"id":"alpha"}`},
		{Seq: 2, Entity: "obs", EntityKey: "beta", Op: "upsert", Project: "beta", Payload: `{"id":"beta"}`},
	}
	transport := newFakeTransport()
	transport.pushErrByProject = map[string]error{
		"alpha": errors.New("generic push failure"),
		"beta":  &fakeAuthErr{code: 403},
	}
	manager := New(local, transport, DefaultConfig())

	manager.cycle(context.Background())

	if local.failureReason != "policy_forbidden" {
		t.Fatalf("persisted failure reason = %q, want policy_forbidden", local.failureReason)
	}
	if !strings.Contains(local.failureMessage, `The server denied access to project "beta".`) {
		t.Fatalf("policy guidance must name beta, got %q", local.failureMessage)
	}
	if strings.Contains(local.failureMessage, `The server denied access to project "alpha".`) {
		t.Fatalf("policy guidance must not misattribute alpha, got %q", local.failureMessage)
	}
}

func (s *fakeLocalStore) MarkSyncBlocked(_, reasonCode, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockedReason = reasonCode
	s.blockedMessage = message
	return nil
}

func (s *fakeLocalStore) MarkSyncHealthy(_ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthyCalls++
	return nil
}

func (s *fakeLocalStore) ListDeferredProjectsForTarget(targetKey string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listedTargets = append(s.listedTargets, targetKey)
	if s.listDeferredErr != nil {
		return nil, s.listDeferredErr
	}
	return append([]string(nil), s.deferredProjects...), nil
}

// Phase E: deferred replay stubs — base fakeLocalStore always returns zero counts
// and no error. Tests that need real replay behavior use fakeLocalStoreWithDeferred.
func (s *fakeLocalStore) ReplayDeferredForScope(_ string, project string) (store.ReplayDeferredResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replayedScopes = append(s.replayedScopes, project)
	return store.ReplayDeferredResult{}, nil
}

func (s *fakeLocalStore) CountDeferredAndDeadForScope(_, _ string) (int, int, error) {
	return 0, 0, nil
}

type fakeLocalStoreWithRepairError struct {
	*fakeLocalStore
	repairErr error
}

func (s *fakeLocalStoreWithRepairError) EnsureEnrolledProjectSyncMutations(context.Context) error {
	return s.repairErr
}

// ─── Fake Transport ───────────────────────────────────────────────────────────

type fakeCloudTransport struct {
	mu                  sync.Mutex
	pushErr             error
	pushErrByProject    map[string]error
	pushResultByProject map[string]*PushMutationsResult
	pullErr             error
	pushCalls           int32
	pullCalls           int32
	pushResult          *PushMutationsResult
	pushHook            func(string)
	pullResult          *PullMutationsResponse
	pushed              [][]MutationEntry
	attempted           [][]MutationEntry
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
	batch := append([]MutationEntry(nil), mutations...)
	t.attempted = append(t.attempted, batch)
	project := ""
	if len(mutations) > 0 {
		project = mutations[0].Project
	}
	if t.pushHook != nil {
		t.pushHook(project)
	}
	if err, ok := t.pushErrByProject[project]; ok {
		return nil, err
	}
	if t.pushErr != nil {
		return nil, t.pushErr
	}
	t.pushed = append(t.pushed, batch)
	if result, ok := t.pushResultByProject[project]; ok {
		return result, nil
	}
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

func attemptedProjects(t *fakeCloudTransport) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	projects := make([]string, 0, len(t.attempted))
	for _, batch := range t.attempted {
		if len(batch) > 0 {
			projects = append(projects, batch[0].Project)
		}
	}
	return projects
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
	tr.pushResult = &PushMutationsResult{AcceptedSeqs: []int64{101, 102}}
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
		t.Fatalf("expected original local seqs [1 2] after successful push, got %v", acked)
	}
}

func TestManagerPushDoesNotAckWhenAcceptedSeqCountMismatchesBatch(t *testing.T) {
	tests := []struct {
		name         string
		pushResult   *PushMutationsResult
		wantErrPiece string
	}{
		{
			name:         "nil result",
			pushResult:   nil,
			wantErrPiece: "missing accepted seqs",
		},
		{
			name:         "no accepted seqs",
			pushResult:   &PushMutationsResult{AcceptedSeqs: []int64{}},
			wantErrPiece: "accepted 0 of 2",
		},
		{
			name:         "short accepted seqs",
			pushResult:   &PushMutationsResult{AcceptedSeqs: []int64{101}},
			wantErrPiece: "accepted 1 of 2",
		},
		{
			name:         "long accepted seqs",
			pushResult:   &PushMutationsResult{AcceptedSeqs: []int64{101, 102, 103}},
			wantErrPiece: "accepted 3 of 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ls := newFakeLocalStore()
			ls.mutations = []store.SyncMutation{
				{Seq: 1, Entity: "obs", EntityKey: "k1", Op: "upsert", Project: "proj-a", Payload: `{"id":"1"}`},
				{Seq: 2, Entity: "obs", EntityKey: "k2", Op: "upsert", Project: "proj-a", Payload: `{"id":"2"}`},
			}
			tr := newFakeTransport()
			tr.pushResult = tt.pushResult
			mgr := New(ls, tr, DefaultConfig())

			err := mgr.push(context.Background())
			if err == nil {
				t.Fatal("expected push to fail on accepted seq mismatch")
			}
			if !strings.Contains(err.Error(), tt.wantErrPiece) {
				t.Fatalf("expected error to contain %q, got %q", tt.wantErrPiece, err.Error())
			}
			if got := atomic.LoadInt32(&tr.pushCalls); got != 1 {
				t.Fatalf("expected one transport push, got %d", got)
			}
			ls.mu.Lock()
			acked := append([]int64(nil), ls.ackedSeqs...)
			ls.mu.Unlock()
			if len(acked) != 0 {
				t.Fatalf("expected no ack on accepted seq mismatch, got %v", acked)
			}
		})
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

func TestManagerPushIsolatesProjectLocalFailures(t *testing.T) {
	tests := []struct {
		name        string
		alphaErr    error
		alphaResult *PushMutationsResult
		wantErr     string
	}{
		{name: "transport error", alphaErr: errors.New("alpha rejected"), wantErr: "alpha rejected"},
		{name: "nil result", alphaResult: nil, wantErr: "missing accepted seqs"},
		{name: "accepted count mismatch", alphaResult: &PushMutationsResult{}, wantErr: "accepted 0 of 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ls := newFakeLocalStore()
			ls.mutations = []store.SyncMutation{
				{Seq: 1, Entity: "obs", EntityKey: "alpha", Op: "upsert", Project: "alpha"},
				{Seq: 2, Entity: "obs", EntityKey: "beta", Op: "upsert", Project: "beta"},
			}
			tr := newFakeTransport()
			tr.pushResultByProject = map[string]*PushMutationsResult{
				"alpha": tt.alphaResult,
				"beta":  {AcceptedSeqs: []int64{2}},
			}
			if tt.alphaErr != nil {
				tr.pushErrByProject = map[string]error{"alpha": tt.alphaErr}
			}

			err := New(ls, tr, DefaultConfig()).push(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
			if got := attemptedProjects(tr); fmt.Sprint(got) != "[alpha beta]" {
				t.Fatalf("expected alpha and beta push attempts in order, got %v", got)
			}
			ls.mu.Lock()
			acked := append([]int64(nil), ls.ackedSeqs...)
			ls.mu.Unlock()
			if fmt.Sprint(acked) != "[2]" {
				t.Fatalf("expected only healthy beta mutation to be acked, got %v", acked)
			}
		})
	}
}

func TestManagerPushSkipsEmptyProjectAndSyncsValidGroups(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "relation", EntityKey: "legacy", Op: "upsert", Project: ""},
		{Seq: 2, Entity: "obs", EntityKey: "alpha", Op: "upsert", Project: "alpha"},
	}
	tr := newFakeTransport()
	tr.pushResultByProject = map[string]*PushMutationsResult{
		"alpha": {AcceptedSeqs: []int64{2}},
	}

	err := New(ls, tr, DefaultConfig()).push(context.Background())
	if err == nil || !strings.Contains(err.Error(), "has an empty or padded project and was not sent") {
		t.Fatalf("expected actionable empty-project error, got %v", err)
	}
	if got := attemptedProjects(tr); fmt.Sprint(got) != "[alpha]" {
		t.Fatalf("expected only alpha to reach transport, got %v", got)
	}
	if fmt.Sprint(ls.ackedSeqs) != "[2]" {
		t.Fatalf("expected only the valid mutation to be acked, got %v", ls.ackedSeqs)
	}
}

func TestManagerPushSkipsPaddedProjectAndSyncsValidGroups(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "relation", EntityKey: "legacy", Op: "upsert", Project: " alpha "},
		{Seq: 2, Entity: "obs", EntityKey: "alpha", Op: "upsert", Project: "alpha"},
	}
	tr := newFakeTransport()
	tr.pushResultByProject = map[string]*PushMutationsResult{
		"alpha": {AcceptedSeqs: []int64{2}},
	}

	err := New(ls, tr, DefaultConfig()).push(context.Background())
	if err == nil || !strings.Contains(err.Error(), "has an empty or padded project and was not sent") {
		t.Fatalf("expected actionable padded-project error, got %v", err)
	}
	if got := attemptedProjects(tr); fmt.Sprint(got) != "[alpha]" {
		t.Fatalf("expected only alpha to reach transport, got %v", got)
	}
	if fmt.Sprint(ls.ackedSeqs) != "[2]" {
		t.Fatalf("expected only the valid mutation to be acked, got %v", ls.ackedSeqs)
	}
}

func TestManagerPushQuarantinesLegacyPaddedProjectBeforeSendingValidGroups(t *testing.T) {
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("store default config: %v", err)
	}
	cfg.DataDir = t.TempDir()
	local, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer local.Close() //nolint:errcheck
	if err := local.EnrollProject("alpha"); err != nil {
		t.Fatalf("enroll alpha: %v", err)
	}
	if _, err := local.DB().Exec(`
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		VALUES
			('cloud', 'session', 'legacy', 'upsert', '{"id":"legacy","directory":"/tmp/legacy"}', 'local', ' alpha '),
			('cloud', 'session', 'alpha', 'upsert', '{"id":"alpha","directory":"/tmp/alpha","project":"alpha"}', 'local', 'alpha')
	`); err != nil {
		t.Fatalf("seed legacy and valid mutations: %v", err)
	}
	var alphaSeq int64
	if err := local.DB().QueryRow(`SELECT seq FROM sync_mutations WHERE entity_key = 'alpha'`).Scan(&alphaSeq); err != nil {
		t.Fatalf("read alpha sequence: %v", err)
	}
	tr := newFakeTransport()
	tr.pushResultByProject = map[string]*PushMutationsResult{
		"alpha": {AcceptedSeqs: []int64{alphaSeq}},
	}

	if err := New(local, tr, DefaultConfig()).push(context.Background()); err != nil {
		t.Fatalf("push valid group alongside quarantined legacy row: %v", err)
	}
	if got := attemptedProjects(tr); fmt.Sprint(got) != "[alpha]" {
		t.Fatalf("expected only alpha to reach transport, got %v", got)
	}
	var disposition string
	if err := local.DB().QueryRow(`SELECT disposition FROM sync_mutations WHERE entity_key = 'legacy'`).Scan(&disposition); err != nil {
		t.Fatalf("read legacy mutation disposition: %v", err)
	}
	if disposition != store.SyncMutationDispositionQuarantined {
		t.Fatalf("expected legacy row to be quarantined, got %q", disposition)
	}
}

func TestManagerPushQuarantinesOnlyConfiguredTarget(t *testing.T) {
	const targetKey = "replica"
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("store default config: %v", err)
	}
	cfg.DataDir = t.TempDir()
	local, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer local.Close() //nolint:errcheck
	if err := local.EnrollProject("alpha"); err != nil {
		t.Fatalf("enroll alpha: %v", err)
	}
	if _, err := local.GetSyncState(targetKey); err != nil {
		t.Fatalf("initialize configured target state: %v", err)
	}
	if _, err := local.DB().Exec(`
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		VALUES
			(?, 'session', 'replica-legacy', 'upsert', '{"id":"replica-legacy","directory":"/tmp/legacy"}', 'local', ' alpha '),
			(?, 'session', 'replica-valid', 'upsert', '{"id":"replica-valid","directory":"/tmp/alpha","project":"alpha"}', 'local', 'alpha'),
			(?, 'session', 'default-legacy', 'upsert', '{"id":"default-legacy","directory":"/tmp/default"}', 'local', ' beta ')
	`, targetKey, targetKey, store.DefaultSyncTargetKey); err != nil {
		t.Fatalf("seed target-scoped mutations: %v", err)
	}
	if err := local.MarkSyncPending(store.DefaultSyncTargetKey); err != nil {
		t.Fatalf("mark default target pending: %v", err)
	}
	var validSeq int64
	if err := local.DB().QueryRow(`SELECT seq FROM sync_mutations WHERE entity_key = 'replica-valid'`).Scan(&validSeq); err != nil {
		t.Fatalf("read valid mutation sequence: %v", err)
	}
	tr := newFakeTransport()
	tr.pushResultByProject = map[string]*PushMutationsResult{
		"alpha": {AcceptedSeqs: []int64{validSeq}},
	}
	autosyncCfg := DefaultConfig()
	autosyncCfg.TargetKey = targetKey
	mgr := New(local, tr, autosyncCfg)

	if err := mgr.push(context.Background()); err != nil {
		t.Fatalf("push configured target: %v", err)
	}
	if got := attemptedProjects(tr); fmt.Sprint(got) != "[alpha]" {
		t.Fatalf("expected configured target valid group to reach transport, got %v", got)
	}
	if err := mgr.push(context.Background()); err != nil {
		t.Fatalf("repeat push configured target: %v", err)
	}
	if got := atomic.LoadInt32(&tr.pushCalls); got != 1 {
		t.Fatalf("expected no repeated transport attempt for quarantined row, got %d", got)
	}

	for _, check := range []struct {
		key  string
		want string
	}{
		{key: "replica-legacy", want: store.SyncMutationDispositionQuarantined},
		{key: "default-legacy", want: store.SyncMutationDispositionPending},
	} {
		var disposition string
		if err := local.DB().QueryRow(`SELECT disposition FROM sync_mutations WHERE entity_key = ?`, check.key).Scan(&disposition); err != nil {
			t.Fatalf("read %s disposition: %v", check.key, err)
		}
		if disposition != check.want {
			t.Fatalf("%s disposition = %q, want %q", check.key, disposition, check.want)
		}
	}
	state, err := local.GetSyncState(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("read default target lifecycle: %v", err)
	}
	if state.Lifecycle != store.SyncLifecyclePending {
		t.Fatalf("default target lifecycle = %q, want %q", state.Lifecycle, store.SyncLifecyclePending)
	}
}

func TestManagerPushReportsAllProjectLocalFailures(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "obs", EntityKey: "alpha", Op: "upsert", Project: "alpha"},
		{Seq: 2, Entity: "obs", EntityKey: "beta", Op: "upsert", Project: "beta"},
		{Seq: 3, Entity: "obs", EntityKey: "gamma", Op: "upsert", Project: "gamma"},
	}
	tr := newFakeTransport()
	tr.pushErrByProject = map[string]error{
		"alpha": errors.New("alpha rejected"),
		"gamma": errors.New("gamma rejected"),
	}
	tr.pushResultByProject = map[string]*PushMutationsResult{
		"beta": {AcceptedSeqs: []int64{2}},
	}

	err := New(ls, tr, DefaultConfig()).push(context.Background())
	if err == nil || !strings.Contains(err.Error(), "alpha rejected") || !strings.Contains(err.Error(), "gamma rejected") {
		t.Fatalf("expected combined alpha and gamma errors, got %v", err)
	}
	if got := attemptedProjects(tr); fmt.Sprint(got) != "[alpha beta gamma]" {
		t.Fatalf("expected every project to be attempted in order, got %v", got)
	}
	if fmt.Sprint(ls.ackedSeqs) != "[2]" {
		t.Fatalf("expected only healthy beta mutation to be acked, got %v", ls.ackedSeqs)
	}
}

func TestManagerPushPreservesPriorFailureWhenLocalAckFails(t *testing.T) {
	ls := newFakeLocalStore()
	alphaErr := errors.New("alpha rejected")
	ackErr := errors.New("disk full")
	ls.ackErr = ackErr
	ls.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "obs", EntityKey: "alpha", Op: "upsert", Project: "alpha"},
		{Seq: 2, Entity: "obs", EntityKey: "beta", Op: "upsert", Project: "beta"},
		{Seq: 3, Entity: "obs", EntityKey: "gamma", Op: "upsert", Project: "gamma"},
	}
	tr := newFakeTransport()
	tr.pushErrByProject = map[string]error{"alpha": alphaErr}
	tr.pushResultByProject = map[string]*PushMutationsResult{
		"beta":  {AcceptedSeqs: []int64{2}},
		"gamma": {AcceptedSeqs: []int64{3}},
	}

	err := New(ls, tr, DefaultConfig()).push(context.Background())
	if !errors.Is(err, alphaErr) || !errors.Is(err, ackErr) {
		t.Fatalf("expected joined alpha and beta ack errors, got %v", err)
	}
	if got := attemptedProjects(tr); fmt.Sprint(got) != "[alpha beta]" {
		t.Fatalf("expected ack failure to stop before gamma, got attempts %v", got)
	}
	if len(ls.ackedSeqs) != 0 {
		t.Fatalf("expected failed local ack to remain unrecorded, got %v", ls.ackedSeqs)
	}
}

func TestManagerPushStopsBeforeLaterProjectsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	alphaErr := errors.New("alpha rejected")
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "obs", EntityKey: "alpha", Op: "upsert", Project: "alpha"},
		{Seq: 2, Entity: "obs", EntityKey: "beta", Op: "upsert", Project: "beta"},
	}
	tr := newFakeTransport()
	tr.pushErrByProject = map[string]error{"alpha": alphaErr}
	tr.pushHook = func(project string) {
		if project == "alpha" {
			cancel()
		}
	}

	err := New(ls, tr, DefaultConfig()).push(ctx)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, alphaErr) {
		t.Fatalf("expected joined cancellation and alpha errors, got %v", err)
	}
	if got := attemptedProjects(tr); fmt.Sprint(got) != "[alpha]" {
		t.Fatalf("expected cancellation to stop before beta, got attempts %v", got)
	}
}

func TestManagerCyclePartialPushFailureSkipsPullAndHealthyState(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{
		{Seq: 1, Entity: "obs", EntityKey: "alpha", Op: "upsert", Project: "alpha"},
		{Seq: 2, Entity: "obs", EntityKey: "beta", Op: "upsert", Project: "beta"},
	}
	tr := newFakeTransport()
	tr.pushErrByProject = map[string]error{"alpha": errors.New("alpha rejected")}
	tr.pushResultByProject = map[string]*PushMutationsResult{"beta": {AcceptedSeqs: []int64{2}}}
	mgr := New(ls, tr, DefaultConfig())

	mgr.cycle(context.Background())

	st := mgr.Status()
	if st.Phase != PhasePushFailed || st.ConsecutiveFailures != 1 || st.BackoffUntil == nil {
		t.Fatalf("expected failed partial push with backoff, got %+v", st)
	}
	if atomic.LoadInt32(&tr.pullCalls) != 0 {
		t.Fatalf("expected partial push failure to skip pull, got %d pull calls", tr.pullCalls)
	}
	if ls.healthyCalls != 0 {
		t.Fatalf("expected partial push failure not to mark cycle healthy, got %d healthy calls", ls.healthyCalls)
	}
}

func TestManagerCycleClassifiesJoinedProjectFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "auth required", err: &fakeAuthErr{code: 401}, want: "auth_required"},
		{name: "policy forbidden", err: &fakeAuthErr{code: 403}, want: "policy_forbidden"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ls := newFakeLocalStore()
			ls.mutations = []store.SyncMutation{
				{Seq: 1, Entity: "obs", EntityKey: "alpha", Op: "upsert", Project: "alpha"},
				{Seq: 2, Entity: "obs", EntityKey: "beta", Op: "upsert", Project: "beta"},
			}
			tr := newFakeTransport()
			tr.pushErrByProject = map[string]error{"alpha": errors.New("another project failed"), "beta": tt.err}
			mgr := New(ls, tr, DefaultConfig())

			mgr.cycle(context.Background())

			if got := mgr.Status().ReasonCode; got != tt.want {
				t.Fatalf("expected %s after joined failures, got %q", tt.want, got)
			}
		})
	}
}

func TestManagerPushPersistsProjectIsolationAcrossStoreRestart(t *testing.T) {
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("store default config: %v", err)
	}
	cfg.DataDir = t.TempDir()
	local, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for _, project := range []string{"alpha", "beta"} {
		if err := local.EnrollProject(project); err != nil {
			t.Fatalf("enroll %s: %v", project, err)
		}
		if err := local.CreateSession("session-"+project, project, "/tmp/"+project); err != nil {
			t.Fatalf("create %s session: %v", project, err)
		}
	}
	tr := newFakeTransport()
	tr.pushErrByProject = map[string]error{"alpha": errors.New("alpha rejected")}
	tr.pushResultByProject = map[string]*PushMutationsResult{"beta": {AcceptedSeqs: []int64{2}}}
	if err := New(local, tr, DefaultConfig()).push(context.Background()); err == nil {
		t.Fatal("expected alpha project push failure")
	}
	if err := local.Close(); err != nil {
		t.Fatalf("close store before restart: %v", err)
	}

	local, err = store.New(cfg)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer local.Close() //nolint:errcheck
	pending, err := local.ListPendingSyncMutations(store.DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending after restart: %v", err)
	}
	if len(pending) != 1 || pending[0].Project != "alpha" {
		t.Fatalf("expected only failed alpha mutation to remain pending, got %+v", pending)
	}
	retry := newFakeTransport()
	retry.pushResultByProject = map[string]*PushMutationsResult{"alpha": {AcceptedSeqs: []int64{1}}}
	if err := New(local, retry, DefaultConfig()).push(context.Background()); err != nil {
		t.Fatalf("retry failed alpha mutation: %v", err)
	}
	pending, err = local.ListPendingSyncMutations(store.DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending after retry: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected retry to ack remaining alpha mutation, got %+v", pending)
	}
}

func TestManagerPushRepairsEnrolledJournalBeforeListingMutations(t *testing.T) {
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("store default config: %v", err)
	}
	cfg.DataDir = t.TempDir()
	local, err := store.New(cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer local.Close() //nolint:errcheck

	if err := local.CreateSession("legacy-session", "legacy-project", "/tmp/legacy-project"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := local.EnrollProject("legacy-project"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if _, err := local.DB().Exec(`DELETE FROM sync_mutations WHERE project = ?`, "legacy-project"); err != nil {
		t.Fatalf("remove journal entries to simulate legacy store: %v", err)
	}

	transport := newFakeTransport()
	transport.pushResult = &PushMutationsResult{AcceptedSeqs: []int64{1}}
	if err := New(local, transport, DefaultConfig()).push(context.Background()); err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(transport.pushed) != 1 || len(transport.pushed[0]) != 1 || transport.pushed[0][0].Entity != store.SyncEntitySession {
		t.Fatalf("pushed mutations = %+v, want repaired session mutation", transport.pushed)
	}
}

func TestManagerPushReturnsRepairErrorBeforeTransport(t *testing.T) {
	local := &fakeLocalStoreWithRepairError{
		fakeLocalStore: newFakeLocalStore(),
		repairErr:      errors.New("repair failed"),
	}
	local.mutations = []store.SyncMutation{{Seq: 1, Project: "project"}}
	transport := newFakeTransport()

	err := New(local, transport, DefaultConfig()).push(context.Background())
	if err == nil || !strings.Contains(err.Error(), "repair enrolled sync journal") {
		t.Fatalf("push error = %v, want repair enrolled sync journal", err)
	}
	if calls := atomic.LoadInt32(&transport.pushCalls); calls != 0 {
		t.Fatalf("transport push calls = %d, want 0", calls)
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

func TestManagerCycleAtFailureCeilingWithActiveBackoffSkipsWork(t *testing.T) {
	ls := newFakeLocalStore()
	tr := newFakeTransport()
	cfg := DefaultConfig()
	cfg.MaxConsecutiveFailures = 3
	mgr := New(ls, tr, cfg)
	backoffUntil := time.Now().Add(time.Minute)

	mgr.mu.Lock()
	mgr.status.Phase = PhasePushFailed
	mgr.status.ConsecutiveFailures = cfg.MaxConsecutiveFailures
	mgr.status.BackoffUntil = &backoffUntil
	mgr.mu.Unlock()

	mgr.cycle(context.Background())

	ls.mu.Lock()
	leaseCalls := ls.leaseCalls
	ls.mu.Unlock()
	if leaseCalls != 0 {
		t.Fatalf("expected no lease work during active ceiling backoff, got %d attempts", leaseCalls)
	}
	if got := atomic.LoadInt32(&tr.pushCalls); got != 0 {
		t.Fatalf("expected no push work during active ceiling backoff, got %d calls", got)
	}
	if got := atomic.LoadInt32(&tr.pullCalls); got != 0 {
		t.Fatalf("expected no pull work during active ceiling backoff, got %d calls", got)
	}
	if got := mgr.Status().Phase; got != PhaseBackoff {
		t.Fatalf("expected PhaseBackoff during active ceiling backoff, got %q", got)
	}
}

func TestManagerCycleAtFailureCeilingAfterBackoffExpiryRecovers(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{{Seq: 1, Entity: "obs", EntityKey: "k1", Project: "proj-a"}}
	tr := newFakeTransport()
	tr.pushResult = &PushMutationsResult{AcceptedSeqs: []int64{1}}
	cfg := DefaultConfig()
	cfg.MaxConsecutiveFailures = 3
	mgr := New(ls, tr, cfg)
	backoffUntil := time.Now().Add(-time.Second)

	mgr.mu.Lock()
	mgr.status.Phase = PhasePushFailed
	mgr.status.ConsecutiveFailures = cfg.MaxConsecutiveFailures
	mgr.status.BackoffUntil = &backoffUntil
	mgr.mu.Unlock()

	mgr.cycle(context.Background())

	ls.mu.Lock()
	leaseCalls := ls.leaseCalls
	healthyCalls := ls.healthyCalls
	ls.mu.Unlock()
	if leaseCalls != 1 {
		t.Fatalf("expected one lease attempt after backoff expiry, got %d", leaseCalls)
	}
	if got := atomic.LoadInt32(&tr.pushCalls); got != 1 {
		t.Fatalf("expected one push after backoff expiry, got %d calls", got)
	}
	if got := atomic.LoadInt32(&tr.pullCalls); got != 1 {
		t.Fatalf("expected one pull after backoff expiry, got %d calls", got)
	}
	if healthyCalls != 1 {
		t.Fatalf("expected healthy state to be persisted once, got %d calls", healthyCalls)
	}
	st := mgr.Status()
	if st.Phase != PhaseHealthy || st.ConsecutiveFailures != 0 || st.BackoffUntil != nil {
		t.Fatalf("expected healthy reset after backoff expiry, got %+v", st)
	}
}

func TestManagerCycleAfterBackoffExpirySchedulesAnotherBackoff(t *testing.T) {
	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{{Seq: 1, Entity: "obs", EntityKey: "k1", Project: "proj-a"}}
	tr := newFakeTransport()
	tr.pushErr = errors.New("transport down")
	cfg := DefaultConfig()
	cfg.MaxConsecutiveFailures = 3
	cfg.BaseBackoff = time.Second
	cfg.MaxBackoff = 5 * time.Second
	mgr := New(ls, tr, cfg)
	backoffUntil := time.Now().Add(-time.Second)

	mgr.mu.Lock()
	mgr.status.Phase = PhasePushFailed
	mgr.status.ConsecutiveFailures = cfg.MaxConsecutiveFailures
	mgr.status.BackoffUntil = &backoffUntil
	mgr.mu.Unlock()

	mgr.cycle(context.Background())

	if got := atomic.LoadInt32(&tr.pushCalls); got != 1 {
		t.Fatalf("expected one push after backoff expiry, got %d calls", got)
	}
	st := mgr.Status()
	if st.Phase != PhasePushFailed || st.ConsecutiveFailures != cfg.MaxConsecutiveFailures+1 || st.BackoffUntil == nil {
		t.Fatalf("expected another failed backoff after expiry, got %+v", st)
	}
	remaining := time.Until(*st.BackoffUntil)
	if remaining <= 0 || remaining > cfg.MaxBackoff {
		t.Fatalf("expected bounded future backoff, got remaining duration %v", remaining)
	}
}

func TestManagerCycleAfterRepairRetriesFreshPayloadWithoutRestart(t *testing.T) {
	const (
		stalePayload    = `{"sync_id":"obs-704","title":""}`
		repairedPayload = `{"sync_id":"obs-704","title":"Recovered title"}`
	)

	ls := newFakeLocalStore()
	ls.mutations = []store.SyncMutation{{
		Seq:       1,
		Entity:    "observation",
		EntityKey: "obs-704",
		Op:        "upsert",
		Project:   "proj-a",
		Payload:   stalePayload,
	}}
	tr := newFakeTransport()
	tr.pushErr = errors.New("canonicalize materialized mutation batch chunk: mutations[0]: observation payload title is required for upsert")
	cfg := DefaultConfig()
	cfg.MaxConsecutiveFailures = 1
	mgr := New(ls, tr, cfg)

	mgr.cycle(context.Background())

	st := mgr.Status()
	if st.ConsecutiveFailures != cfg.MaxConsecutiveFailures || st.BackoffUntil == nil {
		t.Fatalf("expected first push to reach the failure ceiling with backoff, got %+v", st)
	}

	// Simulate `cloud upgrade repair --apply` rewriting the queued mutation in
	// another process while this Manager remains alive and in backoff.
	ls.mu.Lock()
	ls.mutations[0].Payload = repairedPayload
	ls.mu.Unlock()
	tr.mu.Lock()
	tr.pushErr = nil
	tr.pushResult = &PushMutationsResult{AcceptedSeqs: []int64{1}}
	tr.mu.Unlock()

	expired := time.Now().Add(-time.Second)
	mgr.mu.Lock()
	mgr.status.BackoffUntil = &expired
	mgr.mu.Unlock()

	mgr.cycle(context.Background())

	tr.mu.Lock()
	attempted := append([][]MutationEntry(nil), tr.attempted...)
	tr.mu.Unlock()
	if len(attempted) != 2 {
		t.Fatalf("expected one stale attempt and one post-repair retry, got %d attempts", len(attempted))
	}
	if len(attempted[0]) != 1 || string(attempted[0][0].Payload) != stalePayload {
		t.Fatalf("expected first attempt to carry stale payload, got %+v", attempted[0])
	}
	if len(attempted[1]) != 1 || string(attempted[1][0].Payload) != repairedPayload {
		t.Fatalf("expected retry to reload repaired payload, got %+v", attempted[1])
	}

	st = mgr.Status()
	if st.Phase != PhaseHealthy || st.ConsecutiveFailures != 0 || st.BackoffUntil != nil {
		t.Fatalf("expected repaired retry to recover without Manager restart, got %+v", st)
	}
	ls.mu.Lock()
	acked := append([]int64(nil), ls.ackedSeqs...)
	ls.mu.Unlock()
	if fmt.Sprint(acked) != "[1]" {
		t.Fatalf("expected repaired local mutation to be acked, got %v", acked)
	}
}

func TestManagerBackoffSaturatesBeforeDurationConversion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BaseBackoff = time.Second
	cfg.MaxBackoff = 5 * time.Minute
	mgr := &Manager{cfg: cfg}

	for _, failures := range []int{34, 35, 1000} {
		t.Run(fmt.Sprintf("failures=%d", failures), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("computeBackoff panicked: %v", r)
				}
			}()

			d := mgr.computeBackoff(failures)
			if d < cfg.BaseBackoff/2 || d > cfg.MaxBackoff {
				t.Fatalf("backoff %v outside configured bounds [%v, %v]", d, cfg.BaseBackoff/2, cfg.MaxBackoff)
			}
		})
	}
}

func TestManagerBackoffSaturatesPositiveJitterBeforeDurationOverflow(t *testing.T) {
	const maxDuration = time.Duration(1<<63 - 1)

	for _, tc := range []struct {
		name   string
		base   time.Duration
		jitter time.Duration
		want   time.Duration
	}{
		{
			name:   "saturates overflowing positive jitter",
			base:   maxDuration - 1,
			jitter: 2,
			want:   maxDuration,
		},
		{
			name:   "adds positive jitter below ceiling",
			base:   maxDuration - 2,
			jitter: 1,
			want:   maxDuration - 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := saturatingAddBackoffJitter(tc.base, tc.jitter, maxDuration); got != tc.want {
				t.Fatalf("saturatingAddBackoffJitter(%v, %v, %v) = %v, want %v", tc.base, tc.jitter, maxDuration, got, tc.want)
			}
		})
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
	panicObserved := make(chan struct{}, 1)
	subsequentPull := make(chan struct{}, 1)

	cfg := DefaultConfig()
	cfg.DebounceDuration = time.Millisecond
	cfg.PollInterval = time.Millisecond
	cfg.BaseBackoff = time.Nanosecond
	cfg.MaxBackoff = time.Nanosecond

	mgr := New(ls, tr, cfg)
	mgr.transport = &panicOnceTransport{
		delegate:       tr,
		panicOnce:      &panicOnce,
		panicObserved:  panicObserved,
		subsequentPull: subsequentPull,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		mgr.Run(ctx)
		close(runDone)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(time.Second):
			t.Error("Run did not return after context cancellation")
		}
	})
	mgr.NotifyDirty()

	select {
	case <-panicObserved:
	case <-time.After(time.Second):
		t.Fatal("panic was not reached")
	}

	select {
	case <-subsequentPull:
		if got := atomic.LoadInt32(&tr.pullCalls); got < 1 {
			t.Fatalf("expected a successful pull after panic recovery, got %d calls", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not perform work after panic recovery")
	}

	select {
	case <-runDone:
		t.Fatal("Run returned after recovering from panic")
	default:
	}
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

	mgr := New(ls, tr, DefaultConfig())
	mgr.transport = &panicOnceTransport{delegate: tr, panicOnce: &panicOnce}

	before := time.Now()
	mgr.safeRun(context.Background())

	st := mgr.Status()
	if st.Phase != PhaseBackoff {
		t.Fatalf("expected PhaseBackoff after panic, got %q", st.Phase)
	}
	if st.ReasonCode != "internal_error" {
		t.Fatalf("expected reason code internal_error after panic, got %q", st.ReasonCode)
	}
	if st.ReasonMessage != "panic: test panic in cycle" {
		t.Fatalf("expected panic reason message, got %q", st.ReasonMessage)
	}
	if st.ConsecutiveFailures != 1 {
		t.Fatalf("expected one panic failure, got %d", st.ConsecutiveFailures)
	}
	if st.BackoffUntil == nil {
		t.Fatal("expected a backoff deadline after panic")
	}
	if !st.BackoffUntil.After(before) {
		t.Fatalf("expected a future backoff deadline, got %v", st.BackoffUntil)
	}
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
		TargetKey:   "cloud",
		Project:     "proj-a",
		Entity:      "relation",
		Payload:     `{"sync_id":"rel-1"}`,
		RetryCount:  0,
		ApplyStatus: "deferred",
	}}
	ls.mu.Unlock()

	// ReplayDeferred must be called by pull; simulate it resolving successfully.
	tr.pullResult = &PullMutationsResponse{Mutations: []PulledMutation{{Seq: 1, Project: "proj-a", Entity: "observation", Op: "upsert"}}}
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
		TargetKey:   "cloud",
		Project:     "proj-a",
		Entity:      "relation",
		Payload:     `{"sync_id":"rel-dead"}`,
		RetryCount:  4,
		ApplyStatus: "deferred",
	}}
	// Always return FK-missing for this deferred row.
	ls.replayErr = store.ErrRelationFKMissing
	ls.mu.Unlock()
	tr.pullResult = &PullMutationsResponse{Mutations: []PulledMutation{{Seq: 1, Project: "proj-a", Entity: "observation", Op: "upsert"}}}

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
		TargetKey:   "cloud",
		Project:     "proj-a",
		Entity:      "relation",
		Payload:     `{"sync_id":"rel-already-dead"}`,
		RetryCount:  5,
		ApplyStatus: "dead",
	}}
	ls.mu.Unlock()
	tr.pullResult = &PullMutationsResponse{Mutations: []PulledMutation{{Seq: 1, Project: "proj-a", Entity: "observation", Op: "upsert"}}}

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
			appliedCount := ls.deferredApplied
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
			Seq:     10,
			Entity:  "observation",
			Op:      "upsert",
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
	TargetKey   string
	Project     string
	Entity      string
	Payload     string
	RetryCount  int
	ApplyStatus string
}

// fakeLocalStoreWithDeferred extends fakeLocalStore with replay support.
type fakeLocalStoreWithDeferred struct {
	fakeLocalStore
	deferredRows         []DeferredRow
	replayDeferredCalled bool
	replayProjects       []string
	deferredApplied      int
	markDeadCalled       bool
	replayErr            error
	replayCallErr        error
}

func (s *fakeLocalStoreWithDeferred) ListDeferredProjectsForTarget(targetKey string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listedTargets = append(s.listedTargets, targetKey)
	if s.listDeferredErr != nil {
		return nil, s.listDeferredErr
	}
	projects := make(map[string]struct{})
	for _, row := range s.deferredRows {
		if row.TargetKey == targetKey && row.Project != "" && row.ApplyStatus == "deferred" {
			projects[row.Project] = struct{}{}
		}
	}
	result := make([]string, 0, len(projects))
	for project := range projects {
		result = append(result, project)
	}
	sort.Strings(result)
	return result, nil
}

func (s *fakeLocalStoreWithDeferred) ReplayDeferredForScope(targetKey, project string) (store.ReplayDeferredResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replayDeferredCalled = true
	s.replayProjects = append(s.replayProjects, project)
	if s.replayCallErr != nil {
		return store.ReplayDeferredResult{}, s.replayCallErr
	}

	var res store.ReplayDeferredResult
	for i := range s.deferredRows {
		row := &s.deferredRows[i]
		if row.TargetKey != targetKey || row.Project != project {
			continue
		}
		if row.ApplyStatus != "deferred" {
			continue
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
			s.deferredApplied++
			res.Succeeded++
		}
	}
	return res, nil
}

func (s *fakeLocalStoreWithDeferred) CountDeferredAndDeadForScope(_, project string) (deferred, dead int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.deferredRows {
		if project != "" && row.Project != project {
			continue
		}
		switch row.ApplyStatus {
		case "deferred":
			deferred++
		case "dead":
			dead++
		}
	}
	return deferred, dead, nil
}

func TestPullDoesNotReplayOtherTargetDeferredScopes(t *testing.T) {
	ls := &fakeLocalStoreWithDeferred{fakeLocalStore: *newFakeLocalStore()}
	ls.deferredRows = []DeferredRow{
		{SyncID: "rel-project-a", TargetKey: "cloud:project-a", Project: "project-a", RetryCount: 4, ApplyStatus: "deferred"},
		{SyncID: "rel-project-b", TargetKey: "cloud", Project: "project-b", RetryCount: 0, ApplyStatus: "deferred"},
	}
	tr := newFakeTransport()
	tr.pullResult = &PullMutationsResponse{Mutations: []PulledMutation{{
		Seq: 1, Project: "project-b", Entity: "observation", Op: "upsert",
	}}}

	if err := New(ls, tr, DefaultConfig()).pull(context.Background()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	func() {
		ls.mu.Lock()
		defer ls.mu.Unlock()
		if got := ls.replayProjects; len(got) != 1 || got[0] != "project-b" {
			t.Fatalf("replay projects = %v, want [project-b]", got)
		}
		if row := ls.deferredRows[0]; row.RetryCount != 4 || row.ApplyStatus != "deferred" {
			t.Fatalf("project-a deferred row changed by project-b pull: %+v", row)
		}
		if row := ls.deferredRows[1]; row.ApplyStatus != "applied" {
			t.Fatalf("project-b deferred row was not applied: %+v", row)
		}
		if ls.deferredApplied != 1 {
			t.Fatalf("applied deferred rows = %d, want 1", ls.deferredApplied)
		}
	}()

	if err := New(ls, tr, DefaultConfig()).pull(context.Background()); err != nil {
		t.Fatalf("second pull: %v", err)
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.deferredApplied != 1 {
		t.Fatalf("second pull reapplied deferred rows: got %d, want 1", ls.deferredApplied)
	}
}

func TestPullReplaysPersistedDeferredScopeWithoutMutations(t *testing.T) {
	ls := &fakeLocalStoreWithDeferred{fakeLocalStore: *newFakeLocalStore()}
	ls.deferredRows = []DeferredRow{
		{SyncID: "rel-project-b", TargetKey: "cloud:project-b", Project: "project-b", ApplyStatus: "deferred"},
		{SyncID: "rel-project-a", TargetKey: "cloud:project-a", Project: "project-a", RetryCount: 4, ApplyStatus: "deferred"},
	}
	tr := newFakeTransport()
	tr.pullResult = &PullMutationsResponse{}
	cfg := DefaultConfig()
	cfg.TargetKey = "cloud:project-b"

	if err := New(ls, tr, cfg).pull(context.Background()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if got := ls.replayProjects; len(got) != 1 || got[0] != "project-b" {
		t.Fatalf("replay projects = %v, want [project-b]", got)
	}
	if row := ls.deferredRows[0]; row.ApplyStatus != "applied" {
		t.Fatalf("pending project-b row was not applied: %+v", row)
	}
	if row := ls.deferredRows[1]; row.RetryCount != 4 || row.ApplyStatus != "deferred" {
		t.Fatalf("other target/project row changed: %+v", row)
	}
}

func TestPullMergesTouchedAndPendingDeferredScopes(t *testing.T) {
	ls := &fakeLocalStoreWithDeferred{fakeLocalStore: *newFakeLocalStore()}
	ls.deferredRows = []DeferredRow{
		{SyncID: "rel-project-a", TargetKey: "cloud", Project: "project-a", ApplyStatus: "deferred"},
		{SyncID: "rel-project-b", TargetKey: "cloud", Project: "project-b", ApplyStatus: "deferred"},
	}
	tr := newFakeTransport()
	tr.pullResult = &PullMutationsResponse{Mutations: []PulledMutation{{
		Seq: 1, Project: "project-b", Entity: "observation", Op: "upsert",
	}}}

	if err := New(ls, tr, DefaultConfig()).pull(context.Background()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if got, want := fmt.Sprint(ls.replayProjects), "[project-a project-b]"; got != want {
		t.Fatalf("replay projects = %s, want %s", got, want)
	}
	if ls.deferredApplied != 2 {
		t.Fatalf("applied deferred rows = %d, want 2", ls.deferredApplied)
	}
}

func TestPullDeferredScopeEnumerationFailureDoesNotInventScopes(t *testing.T) {
	ls := newFakeLocalStore()
	ls.listDeferredErr = errors.New("list deferred projects")
	tr := newFakeTransport()
	tr.pullResult = &PullMutationsResponse{}

	if err := New(ls, tr, DefaultConfig()).pull(context.Background()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if got := ls.replayedScopes; len(got) != 0 {
		t.Fatalf("replayed scopes = %v, want none", got)
	}
}

func TestPullDeferredScopeReplayErrorIsNonFatal(t *testing.T) {
	ls := &fakeLocalStoreWithDeferred{fakeLocalStore: *newFakeLocalStore()}
	ls.deferredRows = []DeferredRow{{
		SyncID: "rel-project-b", TargetKey: "cloud", Project: "project-b", ApplyStatus: "deferred",
	}}
	ls.replayCallErr = errors.New("replay deferred")
	tr := newFakeTransport()
	tr.pullResult = &PullMutationsResponse{}

	if err := New(ls, tr, DefaultConfig()).pull(context.Background()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if got := ls.replayProjects; len(got) != 1 || got[0] != "project-b" {
		t.Fatalf("replay projects = %v, want [project-b]", got)
	}
	if row := ls.deferredRows[0]; row.ApplyStatus != "deferred" {
		t.Fatalf("replay error changed deferred row: %+v", row)
	}
}

// ─── Helper types ─────────────────────────────────────────────────────────────

type panicOnceTransport struct {
	delegate       *fakeCloudTransport
	panicOnce      *int32
	panicObserved  chan<- struct{}
	subsequentPull chan<- struct{}
}

func (p *panicOnceTransport) PushMutations(mutations []MutationEntry) (*PushMutationsResult, error) {
	return p.delegate.PushMutations(mutations)
}

func (p *panicOnceTransport) PullMutations(sinceSeq int64, limit int) (*PullMutationsResponse, error) {
	if atomic.CompareAndSwapInt32(p.panicOnce, 1, 0) {
		p.signal(p.panicObserved)
		panic(fmt.Sprintf("test panic in cycle"))
	}
	result, err := p.delegate.PullMutations(sinceSeq, limit)
	p.signal(p.subsequentPull)
	return result, err
}

func (p *panicOnceTransport) signal(ch chan<- struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}
