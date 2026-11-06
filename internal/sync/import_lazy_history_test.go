package sync

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Private synthetic fixture through the real import/store paths. The transport
// is the existing in-memory fake; no HTTP, account, or real cloud is accessed.
func lazyHistoryFixture(t *testing.T, history int, kind string) (*store.Store, *Syncer, *historyCountingTransport, string) {
	t.Helper()
	s := newTestStore(t)
	if err := s.EnrollProject("audit"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("audit-session", "audit", "/synthetic/audit"); err != nil {
		t.Fatal(err)
	}
	endpoints := make([]string, 2)
	for i := range endpoints {
		if i == 1 && (kind == "unresolved" || kind == "pending-upsert" || kind == "historical-delete") {
			endpoints[i] = "absent-target"
			continue
		}
		id, err := s.AddObservation(store.AddObservationParams{SessionID: "audit-session", Project: "audit", Type: "decision", Title: fmt.Sprintf("endpoint-%d", i), Content: fmt.Sprintf("synthetic endpoint %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		obs, err := s.GetObservation(id)
		if err != nil {
			t.Fatal(err)
		}
		endpoints[i] = obs.SyncID
		if i == 1 && kind == "tombstone" {
			if err := s.DeleteObservation(id, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	transport := &historyCountingTransport{fakeCloudTransport: newFakeCloudTransport(), reads: make(map[string]int), failures: make(map[string]error)}
	transport.manifest = &Manifest{Version: ownershipModeManifestVersion}
	for i := 0; i < history; i++ {
		id := fmt.Sprintf("known-%03d", i)
		transport.manifest.Chunks = append(transport.manifest.Chunks, ChunkEntry{ID: id})
		chunk := ChunkData{}
		if kind == "historical-delete" && i == history-1 {
			chunk = withObservationDelete(chunk, "absent-target")
		}
		transport.chunks[id] = mustJSONChunk(t, id, chunk)
	}
	sy := NewCloudWithTransport(s, transport, "audit")
	initial, err := sy.Import()
	if err != nil {
		t.Fatal(err)
	}
	if initial.ChunksImported != history {
		t.Fatalf("fixture known chunks=%d want%d", initial.ChunksImported, history)
	}
	chunk := ChunkData{Mutations: []store.SyncMutation{{Entity: store.SyncEntityRelation, EntityKey: "relation-audit", Op: store.SyncOpUpsert, Payload: fmt.Sprintf(`{"sync_id":"relation-audit","source_id":%q,"target_id":%q,"relation":"related","judgment_status":"judged","marked_by_actor":"audit","marked_by_kind":"test","project":"audit"}`, endpoints[0], endpoints[1])}}}
	if kind == "no-relation" {
		chunk = ChunkData{Mutations: []store.SyncMutation{{Entity: store.SyncEntityObservation, EntityKey: "control-observation", Op: store.SyncOpUpsert, Payload: `{"sync_id":"control-observation","session_id":"audit-session","project":"audit","type":"decision","title":"control","content":"synthetic control","scope":"project"}`}}}
	}
	if kind == "pending-upsert" {
		chunk.Mutations = append(chunk.Mutations, store.SyncMutation{Entity: store.SyncEntityObservation, EntityKey: "absent-target", Op: store.SyncOpUpsert, Payload: `{"sync_id":"absent-target","session_id":"audit-session","project":"audit","type":"decision","title":"incoming target","content":"synthetic pending target","scope":"project"}`})
	}
	transport.manifest.Chunks = append(transport.manifest.Chunks, ChunkEntry{ID: "pending-relation"})
	transport.chunks["pending-relation"] = mustJSONChunk(t, "pending-relation", chunk)
	transport.readChunkCalls = 0
	clear(transport.reads)
	return s, sy, transport, endpoints[0]
}

func TestCloudImportLazyHistoryReadBudget(t *testing.T) {
	for _, history := range []int{16, 64} {
		for _, kind := range []string{"no-relation", "present", "tombstone", "pending-upsert", "unresolved", "historical-delete"} {
			t.Run(fmt.Sprintf("H%d/%s", history, kind), func(t *testing.T) {
				s, sy, transport, _ := lazyHistoryFixture(t, history, kind)
				result, err := sy.Import()
				if err != nil {
					t.Fatal(err)
				}
				if result.ChunksImported != 1 || result.ChunksSkipped != history {
					t.Fatalf("unexpected import accounting: %+v", result)
				}
				deferred, dead, err := s.CountDeferredAndDead()
				if err != nil {
					t.Fatal(err)
				}
				wantReads := 1
				if kind == "unresolved" || kind == "historical-delete" {
					wantReads = history + 1
				}
				for id, reads := range transport.reads {
					if reads != 1 {
						t.Fatalf("chunk %s read %d times", id, reads)
					}
				}
				if transport.readChunkCalls != wantReads {
					t.Fatalf("payload reads=%d want%d", transport.readChunkCalls, wantReads)
				}
				if kind == "present" || kind == "tombstone" || kind == "pending-upsert" {
					if _, err := s.GetRelation("relation-audit"); err != nil {
						t.Fatal(err)
					}
					if deferred != 0 || dead != 0 {
						t.Fatalf("unexpected queue: %d/%d", deferred, dead)
					}
				}
				if kind == "unresolved" && deferred != 1 {
					t.Fatalf("missing endpoint did not retain relation: %+v", result)
				}
				if kind == "historical-delete" && (len(result.SkippedRelations) != 1 || deferred != 1) {
					t.Fatalf("historical delete lost visible replayable relation: %+v", result)
				}
				t.Logf("MEASURE H=%d pending=1 kind=%s reads=%d imported=%d skipped=%d deferred=%d dead=%d warnings=%d", history, kind, transport.readChunkCalls, result.ChunksImported, result.ChunksSkipped, deferred, dead, len(result.SkippedRelations))
			})
		}
	}
}

// Count reads by identity, including failed historical reads. Total reads alone
// would miss a duplicate read offset by an accidentally omitted chunk.
type historyCountingTransport struct {
	*fakeCloudTransport
	reads    map[string]int
	failures map[string]error
}

func (tr *historyCountingTransport) ReadChunk(id string) ([]byte, error) {
	tr.reads[id]++
	if err := tr.failures[id]; err != nil {
		return nil, err
	}
	return tr.fakeCloudTransport.ReadChunk(id)
}

func TestCloudImportPendingFailureBeforeLocalEvidence(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "transport", "legacy-ownership"} {
		t.Run(kind, func(t *testing.T) {
			s, sy, tr, _ := lazyHistoryFixture(t, 4, "present")
			tr.manifest.Chunks = append(tr.manifest.Chunks, ChunkEntry{ID: "later-invalid"})
			switch kind {
			case "corrupt":
				tr.chunks["later-invalid"] = []byte("not-json")
			case "transport":
				tr.failures["later-invalid"] = errors.New("synthetic read failure")
			case "legacy-ownership":
				tr.manifest.Version = 1
				tr.chunks["later-invalid"] = mustJSONChunk(t, "later-invalid", ChunkData{Mutations: []store.SyncMutation{{Entity: store.SyncEntitySession, EntityKey: "owned-session", Op: store.SyncOpUpsert, Payload: `{"id":"owned-session","project":"audit","directory":"/synthetic/audit","ownership_mode":"project_owned"}`}}})
			}
			if _, err := sy.Import(); err == nil {
				t.Fatal("invalid pending chunk accepted")
			}
			if _, err := s.GetRelation("relation-audit"); err == nil {
				t.Fatal("relation applied before pending validation")
			}
			known, err := storeGetSynced(s, sy.chunkTrackingTargetKey(""))
			if err != nil {
				t.Fatal(err)
			}
			if known["pending-relation"] {
				t.Fatal("relation chunk tracked before pending validation")
			}
			deferred, dead, err := s.CountDeferredAndDead()
			if err != nil || deferred != 0 || dead != 0 {
				t.Fatalf("unexpected deferred side effect %d/%d: %v", deferred, dead, err)
			}
		})
	}
}

func TestCloudImportRelationFreeKeepsIncrementalValidation(t *testing.T) {
	s, sy, tr, _ := lazyHistoryFixture(t, 4, "no-relation")
	tr.manifest.Chunks = append(tr.manifest.Chunks, ChunkEntry{ID: "later-invalid"})
	tr.chunks["later-invalid"] = []byte("not-json")
	if _, err := sy.Import(); err == nil || !strings.Contains(err.Error(), "parse chunk later-invalid") {
		t.Fatalf("import error=%v", err)
	}
	if _, err := s.GetObservationBySyncID("control-observation"); err != nil {
		t.Fatal("relation-free earlier chunk must still commit", err)
	}
	known, err := storeGetSynced(s, sy.chunkTrackingTargetKey(""))
	if err != nil {
		t.Fatal(err)
	}
	if !known["pending-relation"] {
		t.Fatal("earlier relation-free chunk not tracked")
	}
}

func historyObservation(id string) store.SyncMutation {
	return store.SyncMutation{Entity: store.SyncEntityObservation, EntityKey: id, Op: store.SyncOpUpsert, Payload: fmt.Sprintf(`{"sync_id":%q,"session_id":"audit-session","project":"audit","type":"decision","title":"incoming","content":"synthetic target","scope":"project"}`, id)}
}
func historyRelation(id, source, target string) store.SyncMutation {
	return store.SyncMutation{Entity: store.SyncEntityRelation, EntityKey: id, Op: store.SyncOpUpsert, Payload: fmt.Sprintf(`{"sync_id":%q,"source_id":%q,"target_id":%q,"relation":"related","judgment_status":"judged","marked_by_actor":"audit","marked_by_kind":"test","project":"audit"}`, id, source, target)}
}
func appendHistoryChunk(t *testing.T, tr *historyCountingTransport, id string, chunk ChunkData) {
	t.Helper()
	tr.manifest.Chunks = append(tr.manifest.Chunks, ChunkEntry{ID: id})
	tr.chunks[id] = mustJSONChunk(t, id, chunk)
}

// A chunk becomes known only after successful apply. The pending upsert snapshot
// must use the first classification moment, then remain frozen through apply.
func TestCloudImportLazyHistoryKnownMapGrowth(t *testing.T) {
	for _, classifyFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("classify-first-%t", classifyFirst), func(t *testing.T) {
			s, sy, tr, source := lazyHistoryFixture(t, 4, "unresolved")
			original := tr.chunks["pending-relation"]
			tr.manifest.Chunks = tr.manifest.Chunks[:4]
			if classifyFirst {
				appendHistoryChunk(t, tr, "first-relation", ChunkData{Mutations: []store.SyncMutation{historyRelation("first-relation", source, source)}})
			}
			appendHistoryChunk(t, tr, "upsert", ChunkData{Mutations: []store.SyncMutation{historyObservation("absent-target")}})
			appendHistoryChunk(t, tr, "delete", withObservationDelete(ChunkData{}, "absent-target"))
			tr.manifest.Chunks = append(tr.manifest.Chunks, ChunkEntry{ID: "pending-relation"})
			tr.chunks["pending-relation"] = original
			result, err := sy.Import()
			if err != nil {
				t.Fatal(err)
			}
			wantWarnings, wantReads := 1, 7
			if classifyFirst {
				wantWarnings, wantReads = 0, 4
				if _, err := s.GetRelation("first-relation"); err != nil {
					t.Fatal(err)
				}
			}
			if len(result.SkippedRelations) != wantWarnings {
				t.Fatalf("warnings=%d want%d", len(result.SkippedRelations), wantWarnings)
			}
			if tr.readChunkCalls != wantReads {
				t.Fatalf("reads=%d want%d", tr.readChunkCalls, wantReads)
			}
			deferred, dead, err := s.CountDeferredAndDead()
			if err != nil || deferred != 1 || dead != 0 {
				t.Fatalf("queue=%d/%d %v", deferred, dead, err)
			}
			for id, reads := range tr.reads {
				if reads != 1 {
					t.Fatalf("chunk %s read%d times", id, reads)
				}
			}
		})
	}
}

func TestCloudImportLazyHistoryFallbackOnceDespiteUnavailableHistory(t *testing.T) {
	s, sy, tr, source := lazyHistoryFixture(t, 4, "unresolved")
	delete(tr.chunks, "known-000")
	tr.chunks["known-001"] = []byte("not-json")
	appendHistoryChunk(t, tr, "second-relation", ChunkData{Mutations: []store.SyncMutation{historyRelation("second-relation", source, "another-absent-target")}})
	result, err := sy.Import()
	if err != nil {
		t.Fatal(err)
	}
	if result.ChunksImported != 2 || len(result.SkippedRelations) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	deferred, dead, err := s.CountDeferredAndDead()
	if err != nil || deferred != 2 || dead != 0 {
		t.Fatalf("queue=%d/%d %v", deferred, dead, err)
	}
	if len(tr.reads) != 6 {
		t.Fatalf("read identities=%d want6", len(tr.reads))
	}
	for id, reads := range tr.reads {
		if reads != 1 {
			t.Fatalf("chunk %s read%d times", id, reads)
		}
	}
}

// A later historical fallback must merge into the pending delete set, even
// while the chunk carrying that delete is still pending in the apply loop.
func TestCloudImportLazyHistoryRetainsPendingDeleteEvidence(t *testing.T) {
	s, sy, tr, source := lazyHistoryFixture(t, 4, "unresolved")
	original := tr.chunks["pending-relation"]
	tr.manifest.Chunks = tr.manifest.Chunks[:4]
	appendHistoryChunk(t, tr, "first-relation", ChunkData{Mutations: []store.SyncMutation{historyRelation("first-relation", source, source)}})
	tr.manifest.Chunks = append(tr.manifest.Chunks, ChunkEntry{ID: "pending-relation"})
	tr.chunks["pending-relation"] = original
	appendHistoryChunk(t, tr, "later-delete", withObservationDelete(ChunkData{}, "absent-target"))
	result, err := sy.Import()
	if err != nil {
		t.Fatal(err)
	}
	if result.ChunksImported != 3 || len(result.SkippedRelations) != 1 {
		t.Fatalf("pending delete evidence lost: %+v", result)
	}
	if _, err := s.GetRelation("first-relation"); err != nil {
		t.Fatal(err)
	}
	deferred, dead, err := s.CountDeferredAndDead()
	if err != nil || deferred != 1 || dead != 0 {
		t.Fatalf("queue=%d/%d %v", deferred, dead, err)
	}
	if len(tr.reads) != 7 {
		t.Fatalf("read identities=%d want7", len(tr.reads))
	}
	for id, reads := range tr.reads {
		if reads != 1 {
			t.Fatalf("chunk %s read%d times", id, reads)
		}
	}
}
