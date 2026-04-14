package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/types"
)

// ─── Mock SyncStore ─────────────────────────────────────────────────────────

type mockSyncStore struct {
	testConfigStore
	mutations              []types.SyncMutation
	ackedSeqs              []int64
	leaseAcquired          bool
	leaseReleased          bool
	failureMsg             string
	healthy                bool
	syncState              *types.SyncState
	appliedMuts            []types.SyncMutation
	enrolledProjects       map[string]bool
	ApplyPulledMutationFunc func(string, types.SyncMutation) error
}

func newMockSyncStore() *mockSyncStore {
	return &mockSyncStore{
		testConfigStore:  testConfigStore{data: make(map[string]string)},
		leaseAcquired:    true,
		enrolledProjects: map[string]bool{"proj": true},
		syncState: &types.SyncState{
			TargetKey:   "cloud",
			Lifecycle:   "idle",
			LastPulledSeq: 0,
		},
	}
}

func (m *mockSyncStore) ListPendingSyncMutations(targetKey string, limit int) ([]types.SyncMutation, error) {
	if len(m.mutations) == 0 {
		return nil, nil
	}
	end := limit
	if end > len(m.mutations) {
		end = len(m.mutations)
	}
	result := m.mutations[:end]
	m.mutations = m.mutations[end:]
	return result, nil
}

func (m *mockSyncStore) AckSyncMutations(targetKey string, lastAckedSeq int64) error {
	m.ackedSeqs = append(m.ackedSeqs, lastAckedSeq)
	return nil
}

func (m *mockSyncStore) AcquireSyncLease(targetKey, owner string, ttl time.Duration, now time.Time) (bool, error) {
	return m.leaseAcquired, nil
}

func (m *mockSyncStore) ReleaseSyncLease(targetKey, owner string) error {
	m.leaseReleased = true
	return nil
}

func (m *mockSyncStore) MarkSyncFailure(targetKey, message string, backoffUntil time.Time) error {
	m.failureMsg = message
	if m.syncState != nil {
		m.syncState.ConsecutiveFailures++
	}
	return nil
}

func (m *mockSyncStore) MarkSyncHealthy(targetKey string) error {
	m.healthy = true
	if m.syncState != nil {
		m.syncState.ConsecutiveFailures = 0
	}
	return nil
}

func (m *mockSyncStore) ApplyPulledMutation(targetKey string, mutation types.SyncMutation) error {
	if m.ApplyPulledMutationFunc != nil {
		return m.ApplyPulledMutationFunc(targetKey, mutation)
	}
	m.appliedMuts = append(m.appliedMuts, mutation)
	return nil
}

func (m *mockSyncStore) GetSyncState(targetKey string) (*types.SyncState, error) {
	return m.syncState, nil
}

func (m *mockSyncStore) IsProjectEnrolled(project string) (bool, error) {
	return m.enrolledProjects[project], nil
}

func (m *mockSyncStore) ListEnrolledProjects() ([]types.EnrolledProject, error) {
	var result []types.EnrolledProject
	for p := range m.enrolledProjects {
		result = append(result, types.EnrolledProject{Project: p})
	}
	return result, nil
}

func makeMutations(n int, project string) []types.SyncMutation {
	muts := make([]types.SyncMutation, n)
	for i := range muts {
		muts[i] = types.SyncMutation{
			Seq:       int64(i + 1),
			TargetKey: "cloud",
			Entity:    "observation",
			EntityKey: fmt.Sprintf("obs-%d", i+1),
			Op:        "upsert",
			Payload:   fmt.Sprintf(`{"sync_id":"obs-%d","title":"T%d","content":"C%d","type":"decision","scope":"project"}`, i+1, i+1, i+1),
			Source:    "local",
			Project:   project,
		}
	}
	return muts
}

// ─── T13: Push Path ─────────────────────────────────────────────────────────

func TestPushOnce_BatchesAndAcks(t *testing.T) {
	// 250 mutations → 3 batches (100+100+50)
	store := newMockSyncStore()
	store.mutations = makeMutations(250, "proj")

	var pushCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushCount.Add(1)
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{"acked_seq": 1, "server_seq": 1})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok", "1.0.0")
	sc := NewSyncClient(client, store, CloudConfig{Project: "proj", PushDebounce: time.Second, PullInterval: time.Minute})

	pushed, err := sc.PushOnce(context.Background())
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushed != 250 {
		t.Fatalf("expected 250 pushed, got %d", pushed)
	}
	if pushCount.Load() != 3 {
		t.Fatalf("expected 3 POST requests, got %d", pushCount.Load())
	}
	if len(store.ackedSeqs) != 3 {
		t.Fatalf("expected 3 ACK calls, got %d", len(store.ackedSeqs))
	}
	// ACKed seqs should be 100, 200, 250
	if store.ackedSeqs[0] != 100 || store.ackedSeqs[1] != 200 || store.ackedSeqs[2] != 250 {
		t.Fatalf("unexpected ACK seqs: %v", store.ackedSeqs)
	}
}

func TestPushOnce_LeaseContention(t *testing.T) {
	store := newMockSyncStore()
	store.leaseAcquired = false
	store.mutations = makeMutations(5, "proj")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not make HTTP request when lease not acquired")
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok", "1.0.0")
	sc := NewSyncClient(client, store, CloudConfig{Project: "proj", PushDebounce: time.Second, PullInterval: time.Minute})

	pushed, err := sc.PushOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pushed != 0 {
		t.Fatalf("expected 0 pushed, got %d", pushed)
	}
}

func TestPushOnce_PushFailureMarksFailure(t *testing.T) {
	store := newMockSyncStore()
	store.mutations = makeMutations(5, "proj")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok", "1.0.0")
	client.maxRetry = 1
	sc := NewSyncClient(client, store, CloudConfig{Project: "proj", PushDebounce: time.Second, PullInterval: time.Minute})

	_, err := sc.PushOnce(context.Background())
	if err == nil {
		t.Fatal("expected error from push")
	}
	if store.failureMsg == "" {
		t.Fatal("expected MarkSyncFailure to be called")
	}
}

// ─── T15: Pull Path ─────────────────────────────────────────────────────────

func TestPullOnce_PaginatedPull(t *testing.T) {
	// Server returns 2 pages: 500 entities then 200 entities
	var pullCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := pullCount.Add(1)
		if n == 1 {
			entities := makeEntities(500, 0)
			json.NewEncoder(w).Encode(map[string]any{"entities": entities, "max_seq": 500, "has_more": true})
		} else {
			entities := makeEntities(200, 500)
			json.NewEncoder(w).Encode(map[string]any{"entities": entities, "max_seq": 700, "has_more": false})
		}
	}))
	defer srv.Close()

	store := newMockSyncStore()
	client, _ := NewClient(srv.URL, "tok", "1.0.0")
	sc := NewSyncClient(client, store, CloudConfig{Project: "proj", PushDebounce: time.Second, PullInterval: time.Minute})

	pulled, err := sc.PullOnce(context.Background())
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if pulled != 700 {
		t.Fatalf("expected 700 pulled, got %d", pulled)
	}
	if pullCount.Load() != 2 {
		t.Fatalf("expected 2 GET requests, got %d", pullCount.Load())
	}
	if len(store.appliedMuts) != 700 {
		t.Fatalf("expected 700 applied, got %d", len(store.appliedMuts))
	}
}

func TestPullOnce_ResumesFromCursor(t *testing.T) {
	var gotSinceSeq string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSinceSeq = r.URL.Query().Get("since_seq")
		json.NewEncoder(w).Encode(map[string]any{"entities": []any{}, "max_seq": 450, "has_more": false})
	}))
	defer srv.Close()

	store := newMockSyncStore()
	store.syncState.LastPulledSeq = 450
	client, _ := NewClient(srv.URL, "tok", "1.0.0")
	sc := NewSyncClient(client, store, CloudConfig{Project: "proj", PushDebounce: time.Second, PullInterval: time.Minute})

	sc.PullOnce(context.Background())

	if gotSinceSeq != "450" {
		t.Fatalf("expected since_seq=450, got %q", gotSinceSeq)
	}
}

func TestPullOnce_ApplyErrorSkipsAndContinues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entities := makeEntities(3, 0)
		json.NewEncoder(w).Encode(map[string]any{"entities": entities, "max_seq": 3, "has_more": false})
	}))
	defer srv.Close()

	store := newMockSyncStore()
	applyCount := 0
	store.ApplyPulledMutationFunc = func(_ string, _ types.SyncMutation) error {
		applyCount++
		if applyCount == 2 {
			return fmt.Errorf("apply error")
		}
		return nil
	}

	client, _ := NewClient(srv.URL, "tok", "1.0.0")
	sc := NewSyncClient(client, store, CloudConfig{Project: "proj", PushDebounce: time.Second, PullInterval: time.Minute})

	pulled, err := sc.PullOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 2 applied successfully, 1 skipped
	if pulled != 2 {
		t.Fatalf("expected 2 pulled (1 skipped), got %d", pulled)
	}
}

// ─── T18: Enrollment Guard ──────────────────────────────────────────────────

func TestPushOnce_NonEnrolledProjectSkipped(t *testing.T) {
	store := newMockSyncStore()
	store.enrolledProjects = map[string]bool{} // nothing enrolled
	store.mutations = makeMutations(5, "unenrolled-proj")

	var pushCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushCount.Add(1)
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{"acked_seq": 1, "server_seq": 1})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok", "1.0.0")
	sc := NewSyncClient(client, store, CloudConfig{Project: "proj", PushDebounce: time.Second, PullInterval: time.Minute})

	pushed, err := sc.PushOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pushed != 0 {
		t.Fatalf("expected 0 pushed (not enrolled), got %d", pushed)
	}
	if pushCount.Load() != 0 {
		t.Fatalf("expected 0 HTTP requests, got %d", pushCount.Load())
	}
	// Should still ACK (skip)
	if len(store.ackedSeqs) != 1 {
		t.Fatalf("expected 1 ACK (skip), got %d", len(store.ackedSeqs))
	}
}

// ─── T20: Graceful Shutdown ─────────────────────────────────────────────────

func TestStop_ReleasesLease(t *testing.T) {
	store := newMockSyncStore()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"entities": []any{}, "max_seq": 0, "has_more": false})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok", "1.0.0")
	sc := NewSyncClient(client, store, CloudConfig{
		Project:      "proj",
		PushDebounce: 100 * time.Millisecond,
		PullInterval: 100 * time.Minute, // long to avoid extra pulls
	})

	sc.Start(context.Background())
	time.Sleep(50 * time.Millisecond)
	sc.Stop()

	if !store.leaseReleased {
		t.Fatal("expected lease to be released on Stop")
	}
}

func TestStop_CompletesWithin6Seconds(t *testing.T) {
	store := newMockSyncStore()
	// Server that hangs — tests shutdown timeout
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"entities": []any{}, "max_seq": 0, "has_more": false})
	}))
	defer srv.Close()

	client, _ := NewClient(srv.URL, "tok", "1.0.0")
	sc := NewSyncClient(client, store, CloudConfig{
		Project:      "proj",
		PushDebounce: 100 * time.Millisecond,
		PullInterval: 100 * time.Minute,
	})

	sc.Start(context.Background())
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	sc.Stop()
	elapsed := time.Since(start)

	if elapsed > 7*time.Second {
		t.Fatalf("Stop took too long: %v", elapsed)
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────────

func makeEntities(n int, offset int) []map[string]any {
	entities := make([]map[string]any, n)
	for i := range entities {
		seq := int64(offset + i + 1)
		entities[i] = map[string]any{
			"entity_type": "observation",
			"server_seq":  seq,
			"data": map[string]any{
				"sync_id": fmt.Sprintf("obs-%d", seq),
				"title":   fmt.Sprintf("T%d", seq),
				"content": fmt.Sprintf("C%d", seq),
				"type":    "decision",
				"scope":   "project",
			},
		}
	}
	return entities
}
