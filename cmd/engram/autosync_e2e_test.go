package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/autosync"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/v2/internal/cloud/remote"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/v2/internal/sync"
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
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	trustTLSServer(t, srv)

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
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()
	trustTLSServer(t, srv)

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
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	trustTLSServer(t, srv)

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

func TestAutosyncVersionSidecarAcknowledgesThenRetries(t *testing.T) {
	cfg, err := store.DefaultConfig()
	if err != nil { t.Fatal(err) }
	cfg.DataDir = t.TempDir()
	s, err := store.New(cfg)
	if err != nil { t.Fatal(err) }
	defer s.Close() //nolint:errcheck
	const project = "autosync-history"
	if err := s.EnrollProject(project); err != nil { t.Fatal(err) }
	if err := s.CreateSession("history-session", project, "/tmp/history"); err != nil { t.Fatal(err) }
	id, err := s.AddObservation(store.AddObservationParams{SessionID: "history-session", Type: "note", Title: "history", Content: "content", Project: project, Scope: "project"})
	if err != nil { t.Fatal(err) }
	observation, err := s.GetObservation(id)
	if err != nil { t.Fatal(err) }
	parent, err := json.Marshal(engramsync.ChunkData{Observations: []store.Observation{*observation}})
	if err != nil { t.Fatal(err) }

	var mu sync.Mutex
	mutationPushes, versionPushes := 0, 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/mutations/push":
			var request struct{ Entries []json.RawMessage `json:"entries"` }
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil { http.Error(w, err.Error(), 400); return }
			mu.Lock(); mutationPushes++; mu.Unlock()
			seqs := make([]int64, len(request.Entries)); for i := range seqs { seqs[i] = int64(i + 1) }
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted_seqs": seqs})
		case "/sync/mutations/pull":
			_ = json.NewEncoder(w).Encode(map[string]any{"mutations": []any{}, "has_more": false})
		case "/sync/pull":
			_ = json.NewEncoder(w).Encode(engramsync.Manifest{Version: 2, Chunks: []engramsync.ChunkEntry{{ID: "accepted-parent"}}})
		case "/sync/pull/accepted-parent":
			_, _ = w.Write(parent)
		case "/sync/push":
			body, _ := io.ReadAll(r.Body)
			decoded, err := chunkcodec.DecodeCompressedEnvelope(body, chunkcodec.DefaultMaxDecodedBytes)
			if err != nil { http.Error(w, err.Error(), 400); return }
			var request struct{ Data json.RawMessage `json:"data"` }
			var chunk engramsync.ChunkData
			if json.Unmarshal(decoded, &request) != nil || json.Unmarshal(request.Data, &chunk) != nil || len(chunk.ObservationVersions) == 0 { http.Error(w, "expected version sidecar", 400); return }
			mu.Lock(); versionPushes++; attempt := versionPushes; mu.Unlock()
			if attempt == 1 { http.Error(w, "sidecar unavailable", 500); return }
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	trustTLSServer(t, srv)

	transport, err := remote.NewMutationTransport(srv.URL, "test-token")
	if err != nil { t.Fatal(err) }
	managerCfg := autosync.DefaultConfig()
	managerCfg.DebounceDuration, managerCfg.PollInterval = time.Millisecond, 5*time.Millisecond
	managerCfg.BaseBackoff, managerCfg.MaxBackoff = 5*time.Millisecond, 10*time.Millisecond
	manager := autosync.New(s, &mutationTransportAdapter{remote: transport}, managerCfg)
	manager.SetObservationVersionReconciler(ackCheckingReconciler{store: s, delegate: cloudObservationVersionReconciler{store: s, serverURL: srv.URL, token: "test-token"}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go manager.Run(ctx)
	defer manager.Stop()
	manager.NotifyDirty()
	deadline := time.Now().Add(800 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock(); mutations, versions := mutationPushes, versionPushes; mu.Unlock()
		if mutations == 1 && versions == 2 { return }
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock(); defer mu.Unlock()
	t.Fatalf("mutation pushes=%d version pushes=%d; want parent ACK then one sidecar retry", mutationPushes, versionPushes)
}

type ackCheckingReconciler struct {
	store *store.Store
	delegate autosync.ObservationVersionReconciler
}

func (r ackCheckingReconciler) ReconcileObservationVersions(ctx context.Context, project string) error {
	pending, err := r.store.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1)
	if err != nil { return err }
	if len(pending) != 0 { return fmt.Errorf("sidecar attempted before parent mutation ACK") }
	return r.delegate.ReconcileObservationVersions(ctx, project)
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

func (s *autosyncFakeStore) ListDeferredProjectsForTarget(_ string) ([]string, error) {
	return nil, nil
}

// Phase E: deferred replay stubs — no-ops for the E2E fake store.
func (s *autosyncFakeStore) ReplayDeferredForScope(_, _ string) (store.ReplayDeferredResult, error) {
	return store.ReplayDeferredResult{}, nil
}

func (s *autosyncFakeStore) CountDeferredAndDeadForScope(_, _ string) (int, int, error) {
	return 0, 0, nil
}

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

func trustTLSServer(t *testing.T, server *httptest.Server) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
}
