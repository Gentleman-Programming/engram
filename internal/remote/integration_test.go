//go:build integration

package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/types"
)

// ─── Integration Round-Trip (REQ-TEST-005) ───────────────────────────────────
//
// Flow:
//  1. Store A writes an observation → mutation queued
//  2. SyncClient A pushes to fake httptest server (channel-based, no sleep)
//  3. Fake server records pushed mutations and serves them on pull
//  4. SyncClient B pulls → mutations applied to Store B
//  5. Assert observation is visible in Store B
//
// Run 3 times to verify determinism.

func TestIntegration_RoundTrip(t *testing.T) {
	for run := 0; run < 3; run++ {
		run := run // capture
		t.Run("run", func(t *testing.T) {
			runRoundTrip(t, run)
		})
	}
}

func runRoundTrip(t *testing.T, run int) {
	t.Helper()

	// ── Fake cloud server ────────────────────────────────────────────────────
	// Shared mutation log protected by mutex.
	fakeCloud := newFakeCloud()
	srv := httptest.NewServer(fakeCloud)
	defer srv.Close()

	// ── Store A: writes observation, pushes ──────────────────────────────────
	storeA := newIntegrationStore("proj")

	// Inject the observation mutation directly into storeA's pending queue.
	obs := types.SyncMutation{
		Seq:       int64(run*10 + 1),
		TargetKey: "cloud",
		Entity:    "observation",
		EntityKey: "obs-integration-1",
		Op:        "upsert",
		Payload:   `{"sync_id":"obs-integration-1","title":"Integration Test","content":"hello world","type":"decision","scope":"project"}`,
		Source:    "local",
		Project:   "proj",
	}
	storeA.addPendingMutation(obs)

	clientA, err := NewClient(srv.URL, "tok", "test")
	if err != nil {
		t.Fatalf("run %d: NewClient A: %v", run, err)
	}

	syncA := NewSyncClient(clientA, storeA, CloudConfig{
		Project:      "proj",
		PushDebounce: 10 * time.Millisecond,
		PullInterval: time.Hour, // long — we drive manually
	})

	// Push once — channel-based wait for server receipt.
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()

	_, pushErr := syncA.PushOnce(ctx)
	if pushErr != nil {
		t.Fatalf("run %d: PushOnce: %v", run, pushErr)
	}

	// Wait for fake server to register at least 1 pushed mutation.
	if !waitFor(ctx, func() bool { return fakeCloud.mutationCount() >= 1 }) {
		t.Fatalf("run %d: timed out waiting for push to reach fake server", run)
	}

	// ── Store B: pulls, asserts observation ─────────────────────────────────
	storeB := newIntegrationStore("proj")

	clientB, err := NewClient(srv.URL, "tok", "test")
	if err != nil {
		t.Fatalf("run %d: NewClient B: %v", run, err)
	}

	syncB := NewSyncClient(clientB, storeB, CloudConfig{
		Project:      "proj",
		PushDebounce: time.Hour,
		PullInterval: time.Hour,
	})

	pulled, err := syncB.PullOnce(ctx)
	if err != nil {
		t.Fatalf("run %d: PullOnce: %v", run, err)
	}
	if pulled == 0 {
		t.Fatalf("run %d: expected at least 1 pulled mutation, got 0", run)
	}

	// Verify observation visible in Store B.
	if !storeB.hasObservation("obs-integration-1") {
		t.Fatalf("run %d: observation not found in store B after pull", run)
	}

	// Cleanup
	_ = syncA
	_ = syncB
}

// ─── waitFor polls fn every 5ms until true or ctx expires. No time.Sleep >10ms ─

func waitFor(ctx context.Context, fn func() bool) bool {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if fn() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// ─── fakeCloud: minimal in-memory push/pull server ──────────────────────────

type fakeCloud struct {
	mu        sync.Mutex
	mutations []cloudEntity
}

type cloudEntity struct {
	EntityType string         `json:"entity_type"`
	ServerSeq  int64          `json:"server_seq"`
	Data       map[string]any `json:"data"`
}

func newFakeCloud() *fakeCloud {
	return &fakeCloud{}
}

func (f *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sync/push":
		f.handlePush(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sync/pull":
		f.handlePull(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeCloud) handlePush(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID  string           `json:"device_id"`
		Project   string           `json:"project"`
		Mutations []map[string]any `json:"mutations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, m := range req.Mutations {
		seq := int64(len(f.mutations) + 1)
		entityType := "observation"
		if v, ok := m["entity"].(string); ok {
			entityType = v
		}
		payload, _ := m["payload"].(map[string]any)
		if payload == nil {
			payload = make(map[string]any)
		}
		if ek, ok := m["entity_key"].(string); ok {
			payload["sync_id"] = ek
		}
		f.mutations = append(f.mutations, cloudEntity{
			EntityType: entityType,
			ServerSeq:  seq,
			Data:       payload,
		})
	}

	var lastSeq int64
	if len(f.mutations) > 0 {
		lastSeq = f.mutations[len(f.mutations)-1].ServerSeq
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"acked_seq":  lastSeq,
		"server_seq": lastSeq,
	})
}

func (f *fakeCloud) handlePull(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sinceSeq := int64(0)
	if v := q.Get("since_seq"); v != "" {
		fmt.Sscanf(v, "%d", &sinceSeq)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	var entities []cloudEntity
	for _, m := range f.mutations {
		if m.ServerSeq > sinceSeq {
			entities = append(entities, m)
		}
	}

	var maxSeq int64
	if len(entities) > 0 {
		maxSeq = entities[len(entities)-1].ServerSeq
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"entities": entities,
		"max_seq":  maxSeq,
		"has_more": false,
	})
}

func (f *fakeCloud) mutationCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mutations)
}

// ─── integrationStore: mockSyncStore extended with observation tracking ──────

type integrationStore struct {
	mockSyncStore
	mu           sync.Mutex
	observations map[string]bool // sync_id → true
}

func newIntegrationStore(project string) *integrationStore {
	s := &integrationStore{
		mockSyncStore: *newMockSyncStore(),
		observations:  make(map[string]bool),
	}
	s.mockSyncStore.enrolledProjects = map[string]bool{project: true}

	// Override ApplyPulledMutation to record observations
	s.mockSyncStore.ApplyPulledMutationFunc = func(_ string, m types.SyncMutation) error {
		var data map[string]any
		if err := json.Unmarshal([]byte(m.Payload), &data); err == nil {
			if syncID, ok := data["sync_id"].(string); ok {
				s.mu.Lock()
				s.observations[syncID] = true
				s.mu.Unlock()
			}
		}
		return nil
	}
	return s
}

func (s *integrationStore) addPendingMutation(m types.SyncMutation) {
	s.mockSyncStore.mutations = append(s.mockSyncStore.mutations, m)
}

func (s *integrationStore) hasObservation(syncID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observations[syncID]
}
