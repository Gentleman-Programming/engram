package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
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

func localVersionFixture() *ChunkData {
	project := "proj-a"
	return &ChunkData{
		Sessions: []store.Session{{ID: "session-local", Project: project, Directory: "/tmp/proj-a", StartedAt: "2026-01-01 00:00:00"}},
		Observations: []store.Observation{{SyncID: "observation-local", SessionID: "session-local", Type: "note", Title: "current", Content: "current", Project: &project, Scope: "project", CreatedAt: "2026-01-01 00:00:00", UpdatedAt: "2026-01-01 00:00:00"}},
		ObservationVersions: []store.ObservationVersion{
			{VersionID: "version-local-1", ObservationSyncID: "observation-local", SessionID: "session-local", Type: "note", Title: "old", Content: strings.Repeat("a", 400), Project: &project, Scope: "project", CapturedAt: "2026-01-01 00:00:01"},
			{VersionID: "version-local-2", ObservationSyncID: "observation-local", SessionID: "session-local", Type: "note", Title: "old", Content: strings.Repeat("b", 400), Project: &project, Scope: "project", CapturedAt: "2026-01-01 00:00:02"},
			{VersionID: "version-local-3", ObservationSyncID: "observation-local", SessionID: "session-local", Type: "note", Title: "old", Content: strings.Repeat("c", 400), Project: &project, Scope: "project", CapturedAt: "2026-01-01 00:00:03"},
		},
	}
}

func serializedChunkSize(t *testing.T, chunk *ChunkData) int {
	t.Helper()
	payload, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	return len(payload)
}

func versionIDs(parts []*ChunkData) []string {
	var ids []string
	for _, part := range parts {
		for _, version := range part.ObservationVersions {
			ids = append(ids, version.VersionID)
		}
	}
	return ids
}

func TestSplitLocalExportChunkBoundsVersionsAndPreservesDependencyOrder(t *testing.T) {
	chunk := localVersionFixture()
	first := *chunk
	first.ObservationVersions = chunk.ObservationVersions[:1]
	maxBytes := serializedChunkSize(t, &first)

	parts, err := splitLocalExportChunk(chunk, maxBytes)
	if err != nil {
		t.Fatalf("split local chunk: %v", err)
	}
	if len(parts) < 2 {
		t.Fatalf("parts = %d, want multiple bounded chunks", len(parts))
	}
	for i, part := range parts {
		if got := serializedChunkSize(t, part); got > maxBytes {
			t.Fatalf("part %d serialized size = %d, exceeds %d", i, got, maxBytes)
		}
		if i == 0 {
			if len(part.Observations) != 1 {
				t.Fatalf("first part observations = %#v, want parent observation", part.Observations)
			}
		} else if len(part.Sessions) != 0 || len(part.Observations) != 0 {
			t.Fatalf("part %d repeated parent data: %#v", i, part)
		}
	}
	want := []string{"version-local-1", "version-local-2", "version-local-3"}
	if got := versionIDs(parts); !reflect.DeepEqual(got, want) {
		t.Fatalf("version IDs = %v, want %v", got, want)
	}
	repeat, err := splitLocalExportChunk(chunk, maxBytes)
	if err != nil {
		t.Fatalf("repeat split local chunk: %v", err)
	}
	if !reflect.DeepEqual(parts, repeat) {
		t.Fatalf("same local chunk produced non-deterministic partitions: %#v != %#v", parts, repeat)
	}
}

func seedLocalVersionHistory(t *testing.T, s *store.Store, count, contentBytes int) (*store.Observation, []string) {
	t.Helper()
	const project = "proj-a"
	if err := s.CreateSession("session-version-split", project, "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{SessionID: "session-version-split", Type: "note", Title: "current", Content: "current", Project: project, Scope: "project"})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	observation, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	for i := 0; i < count; i++ {
		versionID := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		if _, err := s.DB().Exec(`INSERT INTO observation_versions (version_id, observation_id, observation_sync_id, session_id, type, title, content, project, scope, revision_count) SELECT ?, id, sync_id, session_id, type, title, ?, project, scope, revision_count FROM observations WHERE id = ?`, versionID, strings.Repeat(string(rune('a'+i)), contentBytes), id); err != nil {
			t.Fatalf("insert version %d: %v", i, err)
		}
	}
	data, err := s.ExportProject(project)
	if err != nil {
		t.Fatalf("export source data: %v", err)
	}
	ids := make([]string, 0, len(data.ObservationVersions))
	for _, version := range data.ObservationVersions {
		ids = append(ids, version.VersionID)
	}
	return observation, ids
}

func TestLocalVersionExportSplitsFreshAndVersionOnlyBackfill(t *testing.T) {
	source := newTestStore(t)
	observation, wantVersions := seedLocalVersionHistory(t, source, 4, 600)
	data, err := source.ExportProject("proj-a")
	if err != nil {
		t.Fatalf("export source data: %v", err)
	}
	budget := 0
	for _, version := range data.ObservationVersions {
		candidate := &ChunkData{Sessions: data.Sessions, Observations: data.Observations, ObservationVersions: []store.ObservationVersion{version}}
		if size := serializedChunkSize(t, candidate); size > budget {
			budget = size
		}
	}
	withLocalExportBudget(t, budget)

	freshDir := filepath.Join(t.TempDir(), ".engram")
	fresh, err := New(source, freshDir).Export("alice", "proj-a")
	if err != nil || fresh.IsEmpty || fresh.ChunksExported < 2 {
		t.Fatalf("fresh split export = %#v, %v", fresh, err)
	}
	manifest, err := New(source, freshDir).readManifest()
	if err != nil {
		t.Fatalf("read fresh manifest: %v", err)
	}
	parts := make([]*ChunkData, 0, len(manifest.Chunks))
	for _, entry := range manifest.Chunks {
		payload, err := readGzip(filepath.Join(freshDir, "chunks", entry.ID+".jsonl.gz"))
		if err != nil {
			t.Fatalf("read fresh chunk %s: %v", entry.ID, err)
		}
		var part ChunkData
		if err := json.Unmarshal(payload, &part); err != nil {
			t.Fatalf("decode fresh chunk %s: %v", entry.ID, err)
		}
		if len(payload) > localExportMaxChunkBytes {
			t.Fatalf("fresh chunk %s = %d bytes, exceeds %d", entry.ID, len(payload), localExportMaxChunkBytes)
		}
		parts = append(parts, &part)
	}
	if got := versionIDs(parts); !reflect.DeepEqual(got, wantVersions) {
		t.Fatalf("fresh version IDs = %v, want %v", got, wantVersions)
	}
	destination := newTestStore(t)
	if result, err := New(destination, freshDir).Import(); err != nil || result.ChunksImported != len(parts) {
		t.Fatalf("fresh split import = %#v, %v", result, err)
	}
	if got, err := destination.ObservationVersions(observation.SyncID, 10); err != nil || len(got) != len(wantVersions) {
		t.Fatalf("fresh split versions = %#v, %v", got, err)
	}

	backfillSource := newTestStore(t)
	_, backfillVersions := seedLocalVersionHistory(t, backfillSource, 4, 600)
	backfillData, err := backfillSource.ExportProject("proj-a")
	if err != nil {
		t.Fatalf("export backfill source data: %v", err)
	}
	versionOnlyBudget := 0
	for _, version := range backfillData.ObservationVersions {
		if size := serializedChunkSize(t, &ChunkData{ObservationVersions: []store.ObservationVersion{version}}); size > versionOnlyBudget {
			versionOnlyBudget = size
		}
	}
	localExportMaxChunkBytes = versionOnlyBudget

	legacyDir := filepath.Join(t.TempDir(), ".engram")
	writeLocalChunkFile(t, legacyDir, "legacy-parent", ChunkData{Sessions: backfillData.Sessions, Observations: backfillData.Observations})
	writeManifestFile(t, legacyDir, &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: "legacy-parent", CreatedAt: "2099-01-01T00:00:00Z"}}})
	backfill, err := New(backfillSource, legacyDir).Export("alice", "proj-a")
	if err != nil || backfill.IsEmpty || backfill.ChunksExported < 2 {
		t.Fatalf("version-only backfill = %#v, %v", backfill, err)
	}
	manifest, err = New(source, legacyDir).readManifest()
	if err != nil {
		t.Fatalf("read backfill manifest: %v", err)
	}
	parts = parts[:0]
	for _, entry := range manifest.Chunks[1:] {
		payload, err := readGzip(filepath.Join(legacyDir, "chunks", entry.ID+".jsonl.gz"))
		if err != nil {
			t.Fatalf("read backfill chunk %s: %v", entry.ID, err)
		}
		var part ChunkData
		if err := json.Unmarshal(payload, &part); err != nil {
			t.Fatalf("decode backfill chunk %s: %v", entry.ID, err)
		}
		if len(part.Sessions) != 0 || len(part.Observations) != 0 || len(payload) > localExportMaxChunkBytes {
			t.Fatalf("invalid bounded version-only chunk: %#v (%d bytes)", part, len(payload))
		}
		parts = append(parts, &part)
	}
	if got := versionIDs(parts); !reflect.DeepEqual(got, backfillVersions) {
		t.Fatalf("backfill version IDs = %v, want %v", got, backfillVersions)
	}
}

func TestLocalVersionExportRejectsOversizedVersionBeforeWritingChunks(t *testing.T) {
	source := newTestStore(t)
	seedLocalVersionHistory(t, source, 1, 1200)
	data, err := source.ExportProject("proj-a")
	if err != nil {
		t.Fatalf("export source data: %v", err)
	}
	budget := 0
	oversizedID := ""
	for _, version := range data.ObservationVersions {
		if size := serializedChunkSize(t, &ChunkData{ObservationVersions: []store.ObservationVersion{version}}); size > budget {
			budget = size
			oversizedID = version.VersionID
		}
	}
	withLocalExportBudget(t, budget-1)

	syncDir := filepath.Join(t.TempDir(), ".engram")
	result, err := New(source, syncDir).Export("alice", "proj-a")
	if err == nil || !strings.Contains(err.Error(), "oversized observation version "+oversizedID) {
		t.Fatalf("oversized version export = %#v, %v", result, err)
	}
	manifest, manifestErr := New(source, syncDir).readManifest()
	if manifestErr != nil || len(manifest.Chunks) != 0 {
		t.Fatalf("manifest after rejected export = %#v, %v", manifest, manifestErr)
	}
	entries, readErr := os.ReadDir(filepath.Join(syncDir, "chunks"))
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("chunks after rejected export = %#v, %v", entries, readErr)
	}
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

func withLocalExportBudget(t *testing.T, budget int) {
	t.Helper()
	orig := localExportMaxChunkBytes
	localExportMaxChunkBytes = budget
	t.Cleanup(func() { localExportMaxChunkBytes = orig })
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
