package sync

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Opt-in belongs to the transport, not to the oracle: arbitrary custom
// transports may reuse IDs for mutable payloads and must keep the fallback.
type evidenceHistoryTransport struct {
	*historyCountingTransport
	scope string
}

func (tr *evidenceHistoryTransport) ChunkEvidenceScope() string { return tr.scope }

func TestCloudImportDeleteEvidenceRepeatedImports(t *testing.T) {
	for _, kind := range []string{"unresolved", "historical-delete"} {
		t.Run(kind, func(t *testing.T) {
			s, _, tr, source := lazyHistoryFixture(t, 32, kind)
			// History predates the evidence index: backfill on the first
			// unresolved import, then reuse it even with a fresh Syncer.
			transport := &evidenceHistoryTransport{tr, "https://synthetic.invalid/audit"}
			for cycle := 0; cycle < 3; cycle++ {
				if cycle > 0 {
					id := fmt.Sprintf("next-relation-%d", cycle)
					appendHistoryChunk(t, tr, id, historyRelationChunk(id, source))
				}
				tr.readChunkCalls = 0
				clear(tr.reads)
				sy := NewCloudWithTransport(s, transport, "audit")
				result, err := sy.Import()
				if err != nil {
					t.Fatal(err)
				}
				wantReads := 1
				if cycle == 0 {
					wantReads += 32
				}
				if tr.readChunkCalls != wantReads {
					t.Fatalf("cycle %d: reads=%d want%d", cycle, tr.readChunkCalls, wantReads)
				}
				wantWarnings := 0
				if kind == "historical-delete" {
					wantWarnings = 1
				}
				if result.ChunksImported != 1 || len(result.SkippedRelations) != wantWarnings {
					t.Fatalf("cycle %d: result=%+v", cycle, result)
				}
				deferred, dead, err := s.CountDeferredAndDead()
				if err != nil || deferred+dead != cycle+1 {
					t.Fatalf("cycle %d: queue=%d/%d err=%v", cycle, deferred, dead, err)
				}
			}
		})
	}
}

func historyRelationChunk(id, source string) ChunkData {
	return ChunkData{Mutations: []store.SyncMutation{historyRelation(id, source, "absent-target")}}
}

func TestCloudImportDeleteEvidenceManifestAndScope(t *testing.T) {
	for _, change := range []string{"removed-delete", "other-server", "other-version", "other-project"} {
		t.Run(change, func(t *testing.T) {
			s, _, tr, source := lazyHistoryFixture(t, 4, "historical-delete")
			transport := &evidenceHistoryTransport{tr, "server-a/audit"}
			sy := NewCloudWithTransport(s, transport, "audit")
			if _, err := sy.Import(); err != nil {
				t.Fatal(err)
			}
			wantReads, wantWarnings := 1, 1
			switch change {
			case "removed-delete":
				tr.manifest.Chunks = append(tr.manifest.Chunks[:3], tr.manifest.Chunks[4:]...)
				wantWarnings = 0
			case "other-server":
				transport.scope = "server-b/audit"
				wantReads = 6 // four original chunks, prior relation and new relation
			case "other-version":
				tr.manifest.Version++
				wantReads = 6
			case "other-project":
				if err := s.EnrollProject("other"); err != nil {
					t.Fatal(err)
				}
				// Identical IDs in another project cannot inherit audit's
				// evidence, even if a custom transport returns the same scope.
				sy = NewCloudWithTransport(s, transport, "other")
				tr.manifest.Chunks = []ChunkEntry{{ID: "known-003"}}
				tr.chunks["known-003"] = mustJSONChunk(t, "known-003", ChunkData{})
				wantReads, wantWarnings = 2, 0
			}
			appendHistoryChunk(t, tr, "next", historyRelationChunk("next", source))
			tr.readChunkCalls = 0
			result, err := sy.Import()
			if err != nil || tr.readChunkCalls != wantReads || len(result.SkippedRelations) != wantWarnings {
				t.Fatalf("result=%+v reads=%d want%d warnings%d err=%v", result, tr.readChunkCalls, wantReads, wantWarnings, err)
			}
		})
	}
}

func TestCloudImportDeleteEvidenceUnavailableHistoryRecovery(t *testing.T) {
	for _, failure := range []string{"missing", "corrupt", "transport"} {
		t.Run(failure, func(t *testing.T) {
			s, _, tr, source := lazyHistoryFixture(t, 4, "historical-delete")
			original := tr.chunks["known-003"]
			switch failure {
			case "missing":
				delete(tr.chunks, "known-003")
			case "corrupt":
				tr.chunks["known-003"] = []byte("not-json")
			case "transport":
				tr.failures["known-003"] = errors.New("synthetic outage")
			}
			transport := &evidenceHistoryTransport{tr, "server/audit"}
			sy := NewCloudWithTransport(s, transport, "audit")
			result, err := sy.Import()
			if err != nil || len(result.SkippedRelations) != 0 {
				t.Fatalf("unavailable history invalidated import: %+v %v", result, err)
			}
			// Restore access to the same original payload. A failed read
			// must not have installed an authoritative empty summary.
			tr.chunks["known-003"] = original
			delete(tr.failures, "known-003")
			appendHistoryChunk(t, tr, "next", historyRelationChunk("next", source))
			tr.readChunkCalls = 0
			result, err = sy.Import()
			if err != nil || tr.readChunkCalls != 2 || len(result.SkippedRelations) != 1 {
				t.Fatalf("recovery: %+v reads=%d err=%v", result, tr.readChunkCalls, err)
			}
		})
	}
}

func TestCloudImportDeleteEvidenceNewChunksAndRestart(t *testing.T) {
	s, _, tr, source := lazyHistoryFixture(t, 0, "unresolved")
	transport := &evidenceHistoryTransport{tr, "server/audit"}
	sy := NewCloudWithTransport(s, transport, "audit")
	pending := tr.manifest.Chunks[0]
	tr.manifest.Chunks = nil
	appendHistoryChunk(t, tr, "empty-history", ChunkData{})
	appendHistoryChunk(t, tr, "delete-history", withObservationDelete(ChunkData{}, "absent-target"))
	if _, err := sy.Import(); err != nil {
		t.Fatal(err)
	}
	dir := s.DataDir()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.New(store.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	sy = NewCloudWithTransport(reopened, transport, "audit")
	tr.manifest.Chunks = append(tr.manifest.Chunks, pending)
	tr.readChunkCalls = 0
	result, err := sy.Import()
	if err != nil || tr.readChunkCalls != 1 || len(result.SkippedRelations) != 1 {
		t.Fatalf("restart: %+v reads=%d err=%v", result, tr.readChunkCalls, err)
	}
	// A future pending upsert takes precedence over persisted historical
	// deletion and lets both the old deferred and new relation heal.
	appendHistoryChunk(t, tr, "resurrection", ChunkData{Mutations: []store.SyncMutation{
		historyRelation("healed", source, "absent-target"), historyObservation("absent-target"),
	}})
	tr.readChunkCalls = 0
	result, err = sy.Import()
	if err != nil || tr.readChunkCalls != 1 || len(result.SkippedRelations) != 0 {
		t.Fatalf("resurrection: %+v reads=%d err=%v", result, tr.readChunkCalls, err)
	}
	for _, id := range []string{"relation-audit", "healed"} {
		if _, err := reopened.GetRelation(id); err != nil {
			t.Fatalf("relation %s did not heal: %v", id, err)
		}
	}
}

func TestCloudImportDeleteEvidenceKeepsPendingPreflight(t *testing.T) {
	s, _, tr, source := lazyHistoryFixture(t, 4, "historical-delete")
	transport := &evidenceHistoryTransport{tr, "server/audit"}
	sy := NewCloudWithTransport(s, transport, "audit")
	if _, err := sy.Import(); err != nil {
		t.Fatal(err)
	}
	appendHistoryChunk(t, tr, "next", historyRelationChunk("next", source))
	tr.manifest.Chunks = append(tr.manifest.Chunks, ChunkEntry{ID: "invalid-later"})
	tr.chunks["invalid-later"] = []byte("not-json")
	if _, err := sy.Import(); err == nil {
		t.Fatal("cached evidence bypassed pending validation")
	}
	known, err := s.GetSyncedChunksForTarget(sy.chunkTrackingTargetKey(""))
	if err != nil || known["next"] {
		t.Fatalf("relation tracked before later pending validation: %v %v", known, err)
	}
	deferred, dead, err := s.CountDeferredAndDead()
	if err != nil || deferred+dead != 1 {
		t.Fatalf("new relation queued before pending validation: %d/%d %v", deferred, dead, err)
	}
}
