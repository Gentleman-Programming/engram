package sync

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func jsonUnmarshalChunkForTest(data []byte, chunk *ChunkData) error {
	return json.Unmarshal(data, chunk)
}

// ─── Splitter fixtures ───────────────────────────────────────────────────────

func splitFixtureChunk() (*ChunkData, []int64) {
	sessions := []store.Session{
		{ID: "sess-1", Project: "proj-a", Directory: "/tmp/proj-a", StartedAt: "2026-01-01 00:00:00"},
		{ID: "sess-2", Project: "proj-a", Directory: "/tmp/proj-a", StartedAt: "2026-01-01 00:00:01"},
	}
	observations := []store.Observation{
		{SyncID: "obs-1", SessionID: "sess-1", Type: "decision", Title: "o1", Content: strings.Repeat("a", 200), Scope: "project", CreatedAt: "2026-01-01 00:00:02"},
		{SyncID: "obs-2", SessionID: "sess-1", Type: "decision", Title: "o2", Content: strings.Repeat("b", 200), Scope: "project", CreatedAt: "2026-01-01 00:00:03"},
		{SyncID: "obs-3", SessionID: "sess-2", Type: "decision", Title: "o3", Content: strings.Repeat("c", 200), Scope: "project", CreatedAt: "2026-01-01 00:00:04"},
		{SyncID: "obs-4", SessionID: "sess-2", Type: "decision", Title: "o4", Content: strings.Repeat("d", 200), Scope: "project", CreatedAt: "2026-01-01 00:00:05"},
	}
	mutations := []store.SyncMutation{
		{Seq: 1, Entity: store.SyncEntitySession, EntityKey: "sess-1", Op: store.SyncOpUpsert, Payload: `{"id":"sess-1"}`, Project: "proj-a"},
		{Seq: 2, Entity: store.SyncEntityObservation, EntityKey: "obs-1", Op: store.SyncOpUpsert, Payload: `{"sync_id":"obs-1","session_id":"sess-1"}`, Project: "proj-a"},
		{Seq: 3, Entity: store.SyncEntityObservation, EntityKey: "obs-2", Op: store.SyncOpUpsert, Payload: `{"sync_id":"obs-2","session_id":"sess-1"}`, Project: "proj-a"},
		{Seq: 4, Entity: store.SyncEntitySession, EntityKey: "sess-2", Op: store.SyncOpUpsert, Payload: `{"id":"sess-2"}`, Project: "proj-a"},
		{Seq: 5, Entity: store.SyncEntityObservation, EntityKey: "obs-3", Op: store.SyncOpUpsert, Payload: `{"sync_id":"obs-3","session_id":"sess-2"}`, Project: "proj-a"},
		{Seq: 6, Entity: store.SyncEntityObservation, EntityKey: "obs-4", Op: store.SyncOpUpsert, Payload: `{"sync_id":"obs-4","session_id":"sess-2"}`, Project: "proj-a"},
	}
	chunk := &ChunkData{
		Sessions:     sessions,
		Observations: observations,
		Mutations:    mutations,
	}
	seqs := []int64{1, 2, 3, 4, 5, 6}
	return chunk, seqs
}

func TestSplitCloudExportChunkEmptyInput(t *testing.T) {
	if parts := splitCloudExportChunk(nil, nil, 1024); len(parts) != 0 {
		t.Fatalf("nil chunk: parts = %d, want 0", len(parts))
	}
	if parts := splitCloudExportChunk(&ChunkData{}, nil, 1024); len(parts) != 0 {
		t.Fatalf("empty chunk: parts = %d, want 0", len(parts))
	}
}

func TestSplitCloudExportChunkSinglePartWhenEverythingFits(t *testing.T) {
	chunk, seqs := splitFixtureChunk()
	parts := splitCloudExportChunk(chunk, seqs, 1<<20)
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(parts))
	}
	if !reflect.DeepEqual(parts[0].seqs, seqs) {
		t.Fatalf("seqs = %v, want %v", parts[0].seqs, seqs)
	}
	if len(parts[0].chunk.Mutations) != len(chunk.Mutations) {
		t.Fatalf("mutations = %d, want %d", len(parts[0].chunk.Mutations), len(chunk.Mutations))
	}
	if len(parts[0].chunk.Sessions) != 2 || len(parts[0].chunk.Observations) != 4 {
		t.Fatalf("part content = %d sessions / %d observations, want 2/4", len(parts[0].chunk.Sessions), len(parts[0].chunk.Observations))
	}
}

func TestSplitCloudExportChunkBoundedPartsPreserveOrderAndDependencies(t *testing.T) {
	chunk, seqs := splitFixtureChunk()
	parts := splitCloudExportChunk(chunk, seqs, 900)
	if len(parts) < 2 {
		t.Fatalf("parts = %d, want at least 2 under a tight budget", len(parts))
	}

	var gotSeqs []int64
	var gotMutations []store.SyncMutation
	for _, part := range parts {
		if len(part.chunk.Mutations) == 0 {
			t.Fatal("part without mutations")
		}
		if len(part.seqs) != len(part.chunk.Mutations) {
			t.Fatalf("part seqs/mutations misaligned: %d vs %d", len(part.seqs), len(part.chunk.Mutations))
		}
		for i, mutation := range part.chunk.Mutations {
			if part.seqs[i] != mutation.Seq {
				t.Fatalf("seq alignment broken: seqs[%d]=%d, mutation seq %d", i, part.seqs[i], mutation.Seq)
			}
		}

		sessionsInPart := map[string]struct{}{}
		for _, session := range part.chunk.Sessions {
			sessionsInPart[session.ID] = struct{}{}
		}
		for _, observation := range part.chunk.Observations {
			if _, ok := sessionsInPart[observation.SessionID]; !ok {
				t.Fatalf("part is not dependency-complete: observation %s misses session %s", observation.SyncID, observation.SessionID)
			}
		}
		for _, prompt := range part.chunk.Prompts {
			if _, ok := sessionsInPart[prompt.SessionID]; !ok {
				t.Fatalf("part is not dependency-complete: prompt %s misses session %s", prompt.SyncID, prompt.SessionID)
			}
		}

		gotSeqs = append(gotSeqs, part.seqs...)
		gotMutations = append(gotMutations, part.chunk.Mutations...)
	}

	if !reflect.DeepEqual(gotSeqs, seqs) {
		t.Fatalf("concatenated seqs = %v, want %v (order preserved, nothing dropped)", gotSeqs, seqs)
	}
	if !reflect.DeepEqual(gotMutations, chunk.Mutations) {
		t.Fatal("concatenated mutations differ from input mutations")
	}
}

func TestSplitCloudExportChunkIsDeterministic(t *testing.T) {
	chunkA, seqsA := splitFixtureChunk()
	chunkB, seqsB := splitFixtureChunk()
	partsA := splitCloudExportChunk(chunkA, seqsA, 900)
	partsB := splitCloudExportChunk(chunkB, seqsB, 900)
	if !reflect.DeepEqual(partsA, partsB) {
		t.Fatal("same input produced different partitions")
	}
}

func TestSplitCloudExportChunkOversizedMutationShipsAlone(t *testing.T) {
	chunk, seqs := splitFixtureChunk()
	chunk.Mutations[2].Payload = `{"sync_id":"obs-2","content":"` + strings.Repeat("x", 5000) + `"}`

	parts := splitCloudExportChunk(chunk, seqs, 900)

	var oversizedPart *cloudExportPart
	var gotSeqs []int64
	for i := range parts {
		for _, seq := range parts[i].seqs {
			if seq == 3 {
				oversizedPart = &parts[i]
			}
		}
		gotSeqs = append(gotSeqs, parts[i].seqs...)
	}
	if oversizedPart == nil {
		t.Fatal("oversized mutation was dropped")
	}
	if len(oversizedPart.seqs) != 1 {
		t.Fatalf("oversized mutation should ship alone, part seqs = %v", oversizedPart.seqs)
	}
	if !reflect.DeepEqual(gotSeqs, seqs) {
		t.Fatalf("concatenated seqs = %v, want %v", gotSeqs, seqs)
	}
}

// ─── Export integration ──────────────────────────────────────────────────────

type flakyCloudTransport struct {
	*fakeCloudTransport
	failOnCall int // 1-based WriteChunk call number that fails; 0 = never fail
	calls      int
}

func (f *flakyCloudTransport) WriteChunk(chunkID string, data []byte, entry ChunkEntry) error {
	f.calls++
	if f.failOnCall != 0 && f.calls == f.failOnCall {
		return fmt.Errorf("simulated push failure")
	}
	return f.fakeCloudTransport.WriteChunk(chunkID, data, entry)
}

func withCloudExportBudget(t *testing.T, budget int) {
	t.Helper()
	orig := cloudExportMaxChunkBytes
	cloudExportMaxChunkBytes = budget
	t.Cleanup(func() { cloudExportMaxChunkBytes = orig })
}

func seedLargeCloudProject(t *testing.T, s *store.Store, observations int, contentSize int) {
	t.Helper()
	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("sess-large", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i := 0; i < observations; i++ {
		if _, err := s.AddObservation(store.AddObservationParams{
			SessionID: "sess-large",
			Type:      "decision",
			Title:     fmt.Sprintf("large observation %03d", i),
			Content:   strings.Repeat("x", contentSize),
			Project:   "proj-a",
			Scope:     "project",
		}); err != nil {
			t.Fatalf("add observation %d: %v", i, err)
		}
	}
}

func pendingMutationCount(t *testing.T, s *store.Store) int {
	t.Helper()
	pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1_000_000)
	if err != nil {
		t.Fatalf("list pending mutations: %v", err)
	}
	return len(pending)
}

func TestCloudExportSplitsLargeReplayIntoBoundedChunks(t *testing.T) {
	s := newTestStore(t)
	seedLargeCloudProject(t, s, 12, 2048)
	withCloudExportBudget(t, 8*1024)

	transport := newFakeCloudTransport()
	sy := NewCloudWithTransport(s, transport, "proj-a")

	result, err := sy.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("cloud export: %v", err)
	}
	if result.IsEmpty {
		t.Fatal("expected non-empty export")
	}
	if transport.writeChunkCalls < 2 {
		t.Fatalf("write chunk calls = %d, want multiple bounded chunks", transport.writeChunkCalls)
	}
	if result.ChunksExported != transport.writeChunkCalls {
		t.Fatalf("chunks exported = %d, want %d", result.ChunksExported, transport.writeChunkCalls)
	}
	for chunkID, data := range transport.chunks {
		if len(data) > cloudExportMaxChunkBytes+4096 {
			t.Fatalf("chunk %s is %d bytes, exceeds budget %d plus slack", chunkID, len(data), cloudExportMaxChunkBytes)
		}
	}
	if got := pendingMutationCount(t, s); got != 0 {
		t.Fatalf("pending mutations after export = %d, want 0", got)
	}

	again, err := sy.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("second cloud export: %v", err)
	}
	if !again.IsEmpty {
		t.Fatalf("second export should be empty, got %+v", again)
	}
}

func TestCloudExportAcksPerChunkAndResumesAfterFailure(t *testing.T) {
	s := newTestStore(t)
	seedLargeCloudProject(t, s, 12, 2048)
	withCloudExportBudget(t, 8*1024)

	transport := &flakyCloudTransport{fakeCloudTransport: newFakeCloudTransport(), failOnCall: 2}
	sy := NewCloudWithTransport(s, transport, "proj-a")

	totalPending := pendingMutationCount(t, s)
	if totalPending == 0 {
		t.Fatal("fixture produced no pending mutations")
	}

	if _, err := sy.Export("alice", "proj-a"); err == nil {
		t.Fatal("expected export to fail on the second chunk")
	}

	remaining := pendingMutationCount(t, s)
	if remaining == 0 {
		t.Fatal("failure on chunk two must leave later mutations pending")
	}
	if remaining == totalPending {
		t.Fatal("first successful chunk must ack its own mutations")
	}
	if len(transport.chunks) != 1 {
		t.Fatalf("stored chunks = %d, want exactly the first successful one", len(transport.chunks))
	}

	transport.failOnCall = 0
	result, err := sy.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("resume export: %v", err)
	}
	if result.IsEmpty {
		t.Fatal("resume export should push the remaining mutations")
	}
	if got := pendingMutationCount(t, s); got != 0 {
		t.Fatalf("pending mutations after resume = %d, want 0", got)
	}

	mutationsAcrossChunks := 0
	for _, data := range transport.chunks {
		var chunk ChunkData
		if err := jsonUnmarshalChunkForTest(data, &chunk); err != nil {
			t.Fatalf("decode stored chunk: %v", err)
		}
		mutationsAcrossChunks += len(chunk.Mutations)
	}
	if mutationsAcrossChunks != totalPending {
		t.Fatalf("mutations across chunks = %d, want %d (no drops, no duplicates)", mutationsAcrossChunks, totalPending)
	}
}
