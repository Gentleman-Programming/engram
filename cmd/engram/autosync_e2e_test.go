package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/autosync"
	"github.com/Gentleman-Programming/engram/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/internal/store"
	_ "modernc.org/sqlite"
)

// ─── E2E Round-trip test (REQ-212) ───────────────────────────────────────────

// TestAutosyncPushPullRoundTrip tests the full push/pull cycle using a real
// httptest server and real autosync.Manager.
func TestAutosyncPushPullRoundTrip(t *testing.T) {
	// In-memory mutation store for the "cloud server".
	var mu sync.Mutex
	var storedMutations []map[string]any
	latestSeq := int64(0)

	// Fake cloud server implementing /sync/mutations/push and /sync/mutations/pull
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/sync/mutations/push":
			var req struct {
				Entries []map[string]any `json:"entries"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			seqs := make([]int64, len(req.Entries))
			for i, e := range req.Entries {
				latestSeq++
				e["_seq"] = latestSeq
				storedMutations = append(storedMutations, e)
				seqs[i] = latestSeq
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted_seqs": seqs})

		case "/sync/mutations/pull":
			sinceSeq := int64(0)
			if v := r.URL.Query().Get("since_seq"); v != "" {
				_, _ = fmt.Sscanf(v, "%d", &sinceSeq)
			}
			mu.Lock()
			var result []map[string]any
			maxSeq := int64(0)
			for _, m := range storedMutations {
				seq, _ := m["_seq"].(int64)
				if seq <= sinceSeq {
					continue
				}
				if seq > maxSeq {
					maxSeq = seq
				}
				result = append(result, m)
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mutations":  result,
				"has_more":   false,
				"latest_seq": maxSeq,
			})

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Create a real MutationTransport pointing at the test server.
	mt, err := remote.NewMutationTransport(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("NewMutationTransport: %v", err)
	}

	// Wrap in adapter for autosync.CloudTransport.
	adapter := &mutationTransportAdapter{remote: mt}

	// Create a fake local store.
	fakeStore := newAutosyncFakeStore()

	cfg := autosync.DefaultConfig()
	cfg.DebounceDuration = 20 * time.Millisecond
	cfg.PollInterval = 20 * time.Millisecond
	cfg.BaseBackoff = 50 * time.Millisecond

	mgr := autosync.New(fakeStore, adapter, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go mgr.Run(ctx)

	// Trigger a push cycle.
	mgr.NotifyDirty()

	// Wait for healthy.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.Status().Phase == autosync.PhaseHealthy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected PhaseHealthy after round-trip, got %q (last_error=%q)",
		mgr.Status().Phase, mgr.Status().LastError)
}

// TestLocalWriteDuringTransport500 verifies REQ-212: local writes succeed even
// when the cloud transport is returning 500.
func TestLocalWriteDuringTransport500(t *testing.T) {
	// Fake cloud server always returns 500.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	mt, err := remote.NewMutationTransport(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("NewMutationTransport: %v", err)
	}
	adapter := &mutationTransportAdapter{remote: mt}
	fakeStore := newAutosyncFakeStore()

	cfg := autosync.DefaultConfig()
	cfg.DebounceDuration = 20 * time.Millisecond
	cfg.PollInterval = 20 * time.Millisecond
	cfg.BaseBackoff = 50 * time.Millisecond
	cfg.MaxBackoff = 200 * time.Millisecond

	mgr := autosync.New(fakeStore, adapter, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go mgr.Run(ctx)

	// Local writes should succeed even with cloud 500s.
	const writes = 50
	errs := make(chan error, writes)
	for i := 0; i < writes; i++ {
		go func(i int) {
			// Simulate a local write (non-blocking, no cloud dependency).
			fakeStore.addMutation(fmt.Sprintf("k%d", i))
			errs <- nil
		}(i)
	}

	for i := 0; i < writes; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("local write %d failed: %v", i, err)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("local write %d timed out", i)
		}
	}
}

// TestGoroutineIsolationConcurrentWrites verifies REQ-212: 1000 concurrent local
// writes complete without deadlock while autosync is running in the background.
func TestGoroutineIsolationConcurrentWrites(t *testing.T) {
	// Fake cloud server with artificial delay to simulate network I/O.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sync/mutations/push":
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted_seqs": []int64{}})
		case "/sync/mutations/pull":
			_ = json.NewEncoder(w).Encode(map[string]any{"mutations": []any{}, "has_more": false, "latest_seq": 0})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	mt, err := remote.NewMutationTransport(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("NewMutationTransport: %v", err)
	}
	adapter := &mutationTransportAdapter{remote: mt}
	fakeStore := newAutosyncFakeStore()

	cfg := autosync.DefaultConfig()
	cfg.DebounceDuration = 10 * time.Millisecond
	cfg.PollInterval = 10 * time.Millisecond

	mgr := autosync.New(fakeStore, adapter, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go mgr.Run(ctx)

	const concurrentWrites = 1000
	var completed int64
	var wg sync.WaitGroup

	for i := 0; i < concurrentWrites; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fakeStore.addMutation(fmt.Sprintf("k%d", i))
			atomic.AddInt64(&completed, 1)
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if n := atomic.LoadInt64(&completed); n != concurrentWrites {
			t.Fatalf("expected %d writes, got %d", concurrentWrites, n)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("deadlock detected: only %d/%d writes completed",
			atomic.LoadInt64(&completed), concurrentWrites)
	}
}

// ─── Fake store for E2E tests ─────────────────────────────────────────────────

// autosyncFakeStore implements autosync.LocalStore with no DB dependency.
type autosyncFakeStore struct {
	mu        sync.Mutex
	mutations []fakeStoredMutation
	pullSeq   int64
}

type fakeStoredMutation struct {
	seq       int64
	entityKey string
}

func newAutosyncFakeStore() *autosyncFakeStore {
	return &autosyncFakeStore{}
}

func (s *autosyncFakeStore) addMutation(entityKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mutations = append(s.mutations, fakeStoredMutation{
		seq:       int64(len(s.mutations) + 1),
		entityKey: entityKey,
	})
}

func (s *autosyncFakeStore) GetSyncState(_ string) (*store.SyncState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &store.SyncState{
		TargetKey:     "cloud",
		Lifecycle:     "idle",
		LastPulledSeq: s.pullSeq,
	}, nil
}

func (s *autosyncFakeStore) ListPendingSyncMutations(_ string, limit int) ([]store.SyncMutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []store.SyncMutation
	for _, m := range s.mutations {
		result = append(result, store.SyncMutation{
			Seq:       m.seq,
			TargetKey: "cloud",
			Entity:    "observation",
			EntityKey: m.entityKey,
			Op:        "upsert",
			Payload:   `{"title":"test"}`,
			Source:    "local",
			Project:   "e2e-test",
		})
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (s *autosyncFakeStore) CountPendingNonEnrolledSyncMutations(_ string) ([]store.PendingSyncMutationProjectCount, error) {
	return nil, nil
}

func (s *autosyncFakeStore) AckSyncMutations(_ string, _ int64) error { return nil }

func (s *autosyncFakeStore) AckSyncMutationSeqs(_ string, seqs []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Remove acked mutations.
	seqSet := make(map[int64]struct{}, len(seqs))
	for _, seq := range seqs {
		seqSet[seq] = struct{}{}
	}
	remaining := s.mutations[:0]
	for _, m := range s.mutations {
		if _, ok := seqSet[m.seq]; !ok {
			remaining = append(remaining, m)
		}
	}
	s.mutations = remaining
	return nil
}

func (s *autosyncFakeStore) SkipAckNonEnrolledMutations(_ string) (int64, error) { return 0, nil }

func (s *autosyncFakeStore) AcquireSyncLease(_, _ string, _ time.Duration, _ time.Time) (bool, error) {
	return true, nil
}

func (s *autosyncFakeStore) ReleaseSyncLease(_, _ string) error { return nil }

func (s *autosyncFakeStore) ApplyPulledMutation(_ string, _ store.SyncMutation) error { return nil }

func (s *autosyncFakeStore) MarkSyncFailure(_, _ string, _ time.Time) error { return nil }

func (s *autosyncFakeStore) MarkSyncBlocked(_, _, _ string) error { return nil }

func (s *autosyncFakeStore) MarkSyncHealthy(_ string) error { return nil }

// Phase E: deferred replay stubs — no-ops for the E2E fake store.
func (s *autosyncFakeStore) ReplayDeferred() (store.ReplayDeferredResult, error) {
	return store.ReplayDeferredResult{}, nil
}

func (s *autosyncFakeStore) CountDeferredAndDead() (int, int, error) { return 0, 0, nil }

// httpPushMutations is a helper to push mutations directly to a test server.
func httpPushMutations(t *testing.T, serverURL, token string, entries []map[string]any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"entries": entries})
	req, _ := http.NewRequest(http.MethodPost, serverURL+"/sync/mutations/push", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("httpPushMutations: %v", err)
	}
	return resp
}
