package sync

// Issue #1163: the local import path must fetch and decode each pending chunk
// once, sharing the decoded value between session dependency classification
// and apply, while keeping every observable import behavior unchanged. These
// tests drive the real local Import() flow over a FileTransport wrapped only to
// count ReadChunk calls; one json.Unmarshal follows every successful read, so
// the read count is a faithful proxy for decode work. Import is sequential, so
// the counter needs no locking.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

type readCountingTransport struct {
	*FileTransport
	reads map[string]int
}

func (c *readCountingTransport) ReadChunk(chunkID string) ([]byte, error) {
	data, err := c.FileTransport.ReadChunk(chunkID)
	if err == nil {
		c.reads[chunkID]++
	}
	return data, err
}

func (c *readCountingTransport) readsFor(chunkID string) int { return c.reads[chunkID] }

// newReadCountingLocalSyncer builds the same local-mode Syncer New returns and
// swaps in a counting wrapper around the same sync directory.
func newReadCountingLocalSyncer(s *store.Store, syncDir string) (*Syncer, *readCountingTransport) {
	sy := New(s, syncDir)
	counting := &readCountingTransport{FileTransport: NewFileTransport(syncDir), reads: map[string]int{}}
	sy.transport = counting
	return sy, counting
}

func reuseSessionChunk(id, project string) ChunkData {
	return ChunkData{
		Sessions: []store.Session{{ID: id, Project: project, Directory: "/tmp/" + project, StartedAt: "2025-01-01 00:00:00"}},
	}
}

func reuseObservationChunk(syncID, sessionID, project string) ChunkData {
	return ChunkData{
		Observations: []store.Observation{{SyncID: syncID, SessionID: sessionID, Type: "note", Title: syncID, Content: "content " + syncID, Project: &project, Scope: "project", CreatedAt: "2025-01-01 00:00:01", UpdatedAt: "2025-01-01 00:00:01"}},
	}
}

func writeCorruptLocalChunkFile(t *testing.T, dir, id string) {
	t.Helper()
	chunksDir := filepath.Join(dir, "chunks")
	if err := os.MkdirAll(chunksDir, 0o755); err != nil {
		t.Fatalf("mkdir chunks: %v", err)
	}
	if err := writeGzip(filepath.Join(chunksDir, id+".jsonl.gz"), []byte("{not json")); err != nil {
		t.Fatalf("write corrupt chunk %s: %v", id, err)
	}
}

func TestLocalImportDecodesEachPendingCurrentVersionChunkOnce(t *testing.T) {
	s := newTestStore(t)
	syncDir := t.TempDir()
	const project = "proj-reuse"
	entries := make([]ChunkEntry, 0, 6)
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("chunk-reuse-%d", i)
		sessionID := fmt.Sprintf("sess-reuse-%d", i)
		chunk := reuseSessionChunk(sessionID, project)
		chunk.Observations = reuseObservationChunk("obs-reuse-"+sessionID, sessionID, project).Observations
		writeLocalChunkFile(t, syncDir, id, chunk)
		entries = append(entries, ChunkEntry{ID: id, CreatedAt: fmt.Sprintf("2025-01-01T00:00:0%dZ", i)})
	}
	writeManifestFile(t, syncDir, &Manifest{Version: ownershipModeManifestVersion, Chunks: entries})

	sy, counting := newReadCountingLocalSyncer(s, syncDir)
	res, err := sy.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.ChunksImported != 6 || res.ChunksSkipped != 0 || res.SessionsImported != 6 || res.ObservationsImported != 6 {
		t.Fatalf("unexpected import result: %+v", res)
	}
	for _, entry := range entries {
		if got := counting.readsFor(entry.ID); got != 1 {
			t.Fatalf("pending chunk %s read %d times, want 1 (classification and apply must share one decode)", entry.ID, got)
		}
	}
}

func TestLocalImportReusesDecodedChunkAcrossRetryPasses(t *testing.T) {
	resetSyncTestHooks(t)
	s := newTestStore(t)
	syncDir := t.TempDir()
	const project = "proj-retry"
	writeManifestFile(t, syncDir, &Manifest{Version: ownershipModeManifestVersion, Chunks: []ChunkEntry{
		{ID: "chunk-flaky", CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "chunk-steady", CreatedAt: "2025-01-01T00:00:01Z"},
	}})
	writeLocalChunkFile(t, syncDir, "chunk-flaky", reuseSessionChunk("sess-flaky", project))
	writeLocalChunkFile(t, syncDir, "chunk-steady", reuseSessionChunk("sess-steady", project))

	realApply := storeApplyPulledChunk
	applyCalls := map[string]int{}
	storeApplyPulledChunk = func(st *store.Store, targetKey, chunkID string, mutations []store.SyncMutation, cloud bool) error {
		applyCalls[chunkID]++
		if chunkID == "chunk-flaky" && applyCalls[chunkID] == 1 {
			return errors.New("transient apply failure")
		}
		return realApply(st, targetKey, chunkID, mutations, cloud)
	}

	sy, counting := newReadCountingLocalSyncer(s, syncDir)
	res, err := sy.Import()
	if err != nil {
		t.Fatalf("import should succeed on the retry pass: %v", err)
	}
	if res.ChunksImported != 2 || res.SessionsImported != 2 {
		t.Fatalf("unexpected import result: %+v", res)
	}
	if applyCalls["chunk-flaky"] != 2 || applyCalls["chunk-steady"] != 1 {
		t.Fatalf("expected flaky chunk applied on a second pass, apply calls=%v", applyCalls)
	}
	if got := counting.readsFor("chunk-flaky"); got != 1 {
		t.Fatalf("retried chunk read %d times, want 1", got)
	}
	if got := counting.readsFor("chunk-steady"); got != 1 {
		t.Fatalf("steady chunk read %d times, want 1", got)
	}
	if _, err := s.GetSession("sess-flaky"); err != nil {
		t.Fatalf("expected retried session imported: %v", err)
	}
}

func TestLocalImportKeepsScanningAlreadyKnownEntries(t *testing.T) {
	// An intact known chunk is still read once for classification evidence; a
	// missing or corrupt one is ignored and must not fail the new import.
	for damage, wantKnownReads := range map[string]int{"intact": 1, "missing": 0, "corrupt": 1} {
		t.Run(damage, func(t *testing.T) {
			s := newTestStore(t)
			syncDir := t.TempDir()
			const project = "proj-known"
			known := ChunkEntry{ID: "chunk-known", CreatedAt: "2025-01-01T00:00:00Z"}
			writeLocalChunkFile(t, syncDir, known.ID, reuseSessionChunk("sess-known", project))
			writeManifestFile(t, syncDir, &Manifest{Version: ownershipModeManifestVersion, Chunks: []ChunkEntry{known}})
			if _, err := New(s, syncDir).Import(); err != nil {
				t.Fatalf("first import: %v", err)
			}
			switch damage {
			case "missing":
				if err := os.Remove(filepath.Join(syncDir, "chunks", known.ID+".jsonl.gz")); err != nil {
					t.Fatalf("remove known chunk: %v", err)
				}
			case "corrupt":
				writeCorruptLocalChunkFile(t, syncDir, known.ID)
			}

			writeLocalChunkFile(t, syncDir, "chunk-pending", reuseObservationChunk("obs-known-parent", "sess-known", project))
			writeManifestFile(t, syncDir, &Manifest{Version: ownershipModeManifestVersion, Chunks: []ChunkEntry{
				known, {ID: "chunk-pending", CreatedAt: "2025-01-01T00:00:01Z"},
			}})

			sy, counting := newReadCountingLocalSyncer(s, syncDir)
			res, err := sy.Import()
			if err != nil {
				t.Fatalf("second import: %v", err)
			}
			if res.ChunksImported != 1 || res.ChunksSkipped != 1 || res.ObservationsImported != 1 || res.SessionsImported != 0 {
				t.Fatalf("unexpected import result: %+v", res)
			}
			if got := counting.readsFor(known.ID); got != wantKnownReads {
				t.Fatalf("known chunk read %d times, want %d (classification keeps its full manifest evidence)", got, wantKnownReads)
			}
			if got := counting.readsFor("chunk-pending"); got != 1 {
				t.Fatalf("pending chunk read %d times, want 1", got)
			}
			sess, err := s.GetSession("sess-known")
			if err != nil || sess.Directory != "/tmp/"+project {
				t.Fatalf("expected the real known session, not a recovered stub: %+v err=%v", sess, err)
			}
		})
	}
}

func TestLocalImportSkipsMissingPendingChunk(t *testing.T) {
	s := newTestStore(t)
	syncDir := t.TempDir()
	const project = "proj-missing"
	writeLocalChunkFile(t, syncDir, "chunk-present", reuseSessionChunk("sess-present", project))
	writeManifestFile(t, syncDir, &Manifest{Version: ownershipModeManifestVersion, Chunks: []ChunkEntry{
		{ID: "chunk-absent", CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "chunk-present", CreatedAt: "2025-01-01T00:00:01Z"},
	}})

	sy, counting := newReadCountingLocalSyncer(s, syncDir)
	res, err := sy.Import()
	if err != nil {
		t.Fatalf("a missing local chunk must be skipped, not fail the import: %v", err)
	}
	if res.ChunksImported != 1 || res.ChunksSkipped != 1 || res.SessionsImported != 1 {
		t.Fatalf("unexpected import result: %+v", res)
	}
	if got := counting.readsFor("chunk-present"); got != 1 {
		t.Fatalf("present chunk read %d times, want 1", got)
	}
	synced, err := s.GetSyncedChunks()
	if err != nil {
		t.Fatalf("get synced chunks: %v", err)
	}
	if synced["chunk-absent"] || !synced["chunk-present"] {
		t.Fatalf("expected only the present chunk marked synced, got %v", synced)
	}
}

func TestLocalImportCorruptPendingChunkFailsBeforeAnyApply(t *testing.T) {
	s := newTestStore(t)
	syncDir := t.TempDir()
	writeLocalChunkFile(t, syncDir, "chunk-good", reuseSessionChunk("sess-before-corrupt", "proj-corrupt"))
	writeCorruptLocalChunkFile(t, syncDir, "chunk-corrupt")
	writeManifestFile(t, syncDir, &Manifest{Version: ownershipModeManifestVersion, Chunks: []ChunkEntry{
		{ID: "chunk-good", CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "chunk-corrupt", CreatedAt: "2025-01-01T00:00:01Z"},
	}})

	res, err := New(s, syncDir).Import()
	if err == nil || !strings.Contains(err.Error(), "parse chunk chunk-corrupt") {
		t.Fatalf("expected parse error for the corrupt pending chunk, got result=%+v err=%v", res, err)
	}
	if res != nil {
		t.Fatalf("expected no result on failure, got %+v", res)
	}
	if _, err := s.GetSession("sess-before-corrupt"); err == nil {
		t.Fatalf("a corrupt pending chunk must abort before any chunk applies")
	}
	synced, err := s.GetSyncedChunks()
	if err != nil {
		t.Fatalf("get synced chunks: %v", err)
	}
	if len(synced) != 0 {
		t.Fatalf("expected no chunk marked synced, got %v", synced)
	}
}

func TestLocalImportLegacyManifestDecodesEachPendingChunkOnce(t *testing.T) {
	s := newTestStore(t)
	syncDir := t.TempDir()
	const project = "proj-legacy"
	writeLocalChunkFile(t, syncDir, "chunk-legacy-dependent", reuseObservationChunk("obs-legacy", "sess-legacy", project))
	writeLocalChunkFile(t, syncDir, "chunk-legacy-session", reuseSessionChunk("sess-legacy", project))
	writeManifestFile(t, syncDir, &Manifest{Version: 1, Chunks: []ChunkEntry{
		{ID: "chunk-legacy-dependent", CreatedAt: "2025-01-01T00:00:00Z"},
		{ID: "chunk-legacy-session", CreatedAt: "2025-01-01T00:00:01Z"},
	}})

	sy, counting := newReadCountingLocalSyncer(s, syncDir)
	res, err := sy.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.ChunksImported != 2 || res.SessionsImported != 1 || res.ObservationsImported != 1 {
		t.Fatalf("unexpected import result: %+v", res)
	}
	for _, id := range []string{"chunk-legacy-dependent", "chunk-legacy-session"} {
		if got := counting.readsFor(id); got != 1 {
			t.Fatalf("legacy chunk %s read %d times, want 1 (ownership preflight, classification, and apply share one decode)", id, got)
		}
	}
	sess, err := s.GetSession("sess-legacy")
	if err != nil || sess.Directory != "/tmp/"+project {
		t.Fatalf("expected the real session chunk, not a recovered stub: %+v err=%v", sess, err)
	}
}

func TestLocalImportLegacyManifestStillChecksChunkOwnership(t *testing.T) {
	s := newTestStore(t)
	syncDir := t.TempDir()
	writeLocalChunkFile(t, syncDir, "chunk-legacy-owned", ChunkData{Mutations: []store.SyncMutation{{
		Entity:    store.SyncEntitySession,
		EntityKey: "sess-legacy-owned",
		Op:        store.SyncOpUpsert,
		Payload:   `{"id":"sess-legacy-owned","project":"proj-a","ownership_mode":"project_owned","directory":"/tmp/proj-a"}`,
	}}})
	writeManifestFile(t, syncDir, &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: "chunk-legacy-owned", CreatedAt: "2025-01-01T00:00:00Z"}}})

	sy, counting := newReadCountingLocalSyncer(s, syncDir)
	res, err := sy.Import()
	if err == nil || !strings.Contains(err.Error(), "downgrade is unsupported") {
		t.Fatalf("expected the legacy ownership downgrade guard to fail the import, got result=%+v err=%v", res, err)
	}
	if got := counting.readsFor("chunk-legacy-owned"); got != 1 {
		t.Fatalf("ownership-rejected chunk read %d times, want 1", got)
	}
	synced, err := s.GetSyncedChunks()
	if err != nil {
		t.Fatalf("get synced chunks: %v", err)
	}
	if synced["chunk-legacy-owned"] {
		t.Fatalf("a downgrade-incompatible chunk must never be marked synced")
	}
	if _, err := s.GetSession("sess-legacy-owned"); err == nil {
		t.Fatalf("a downgrade-incompatible chunk must not persist its session")
	}
}

func TestLocalImportRecoversMissingSessionFromReusedChunk(t *testing.T) {
	s := newTestStore(t)
	syncDir := t.TempDir()
	const project = "proj-recover"
	writeLocalChunkFile(t, syncDir, "chunk-orphan", reuseObservationChunk("obs-orphan", "sess-nowhere", project))
	writeManifestFile(t, syncDir, &Manifest{Version: ownershipModeManifestVersion, Chunks: []ChunkEntry{
		{ID: "chunk-orphan", CreatedAt: "2025-01-01T00:00:00Z"},
	}})

	sy, counting := newReadCountingLocalSyncer(s, syncDir)
	res, err := sy.Import()
	if err != nil {
		t.Fatalf("import should recover the missing session stub: %v", err)
	}
	if res.ChunksImported != 1 || res.SessionsImported != 1 || res.ObservationsImported != 1 {
		t.Fatalf("unexpected import result: %+v", res)
	}
	if got := counting.readsFor("chunk-orphan"); got != 1 {
		t.Fatalf("recovered chunk read %d times, want 1", got)
	}
	sess, err := s.GetSession("sess-nowhere")
	if err != nil || sess.Directory != recoveredMissingSessionDirectory || sess.Project != project {
		t.Fatalf("expected recovered stub session: %+v err=%v", sess, err)
	}
}
