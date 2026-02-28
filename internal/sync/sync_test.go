package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg := store.DefaultConfig()
	cfg.DataDir = t.TempDir()

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

func seedStoreForSync(t *testing.T, s *store.Store) {
	t.Helper()

	if err := s.CreateSession("s-proj", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session proj-a: %v", err)
	}
	if err := s.CreateSession("s-other", "proj-b", "/tmp/proj-b"); err != nil {
		t.Fatalf("create session proj-b: %v", err)
	}

	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-proj",
		Type:      "decision",
		Title:     "project observation",
		Content:   "project scoped content",
		Project:   "proj-a",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add proj-a observation: %v", err)
	}

	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-other",
		Type:      "decision",
		Title:     "other observation",
		Content:   "other scoped content",
		Project:   "proj-b",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add proj-b observation: %v", err)
	}

	if _, err := s.AddPrompt(store.AddPromptParams{SessionID: "s-proj", Content: "prompt-a", Project: "proj-a"}); err != nil {
		t.Fatalf("add proj-a prompt: %v", err)
	}
	if _, err := s.AddPrompt(store.AddPromptParams{SessionID: "s-other", Content: "prompt-b", Project: "proj-b"}); err != nil {
		t.Fatalf("add proj-b prompt: %v", err)
	}
}

func writeManifestFile(t *testing.T, dir string, m *Manifest) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sync dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func resetSyncTestHooks(t *testing.T) {
	t.Helper()
	origJSONMarshalChunk := jsonMarshalChunk
	origJSONMarshalManifest := jsonMarshalManifest
	origOSCreateFile := osCreateFile
	origGzipWriterFactory := gzipWriterFactory
	origOSHostname := osHostname
	origStoreGetSynced := storeGetSynced
	origStoreExportData := storeExportData
	origStoreImportData := storeImportData
	origStoreRecordSynced := storeRecordSynced

	t.Cleanup(func() {
		jsonMarshalChunk = origJSONMarshalChunk
		jsonMarshalManifest = origJSONMarshalManifest
		osCreateFile = origOSCreateFile
		gzipWriterFactory = origGzipWriterFactory
		osHostname = origOSHostname
		storeGetSynced = origStoreGetSynced
		storeExportData = origStoreExportData
		storeImportData = origStoreImportData
		storeRecordSynced = origStoreRecordSynced
	})
}

type fakeGzipWriter struct {
	writeErr error
	closeErr error
}

func (f *fakeGzipWriter) Write(_ []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return 1, nil
}

func (f *fakeGzipWriter) Close() error {
	return f.closeErr
}

func TestNew(t *testing.T) {
	s := newTestStore(t)
	syncDir := filepath.Join(t.TempDir(), ".engram")
	sy := New(s, syncDir)

	if sy == nil {
		t.Fatal("expected non-nil syncer")
	}
	if sy.store != s {
		t.Fatal("store pointer not preserved")
	}
	if sy.syncDir != syncDir {
		t.Fatalf("sync dir mismatch: got %q want %q", sy.syncDir, syncDir)
	}
}

func TestExportImportFlowWithProjectFilter(t *testing.T) {
	srcStore := newTestStore(t)
	seedStoreForSync(t, srcStore)

	syncDir := filepath.Join(t.TempDir(), ".engram")
	exporter := New(srcStore, syncDir)

	exportResult, err := exporter.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if exportResult.IsEmpty {
		t.Fatal("expected non-empty export")
	}
	if exportResult.SessionsExported != 1 || exportResult.ObservationsExported != 1 || exportResult.PromptsExported != 1 {
		t.Fatalf("unexpected export counts: %+v", exportResult)
	}

	chunkPath := filepath.Join(syncDir, "chunks", exportResult.ChunkID+".jsonl.gz")
	if _, err := os.Stat(chunkPath); err != nil {
		t.Fatalf("chunk file missing: %v", err)
	}

	manifest, err := exporter.readManifest()
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(manifest.Chunks) != 1 || manifest.Chunks[0].ID != exportResult.ChunkID {
		t.Fatalf("unexpected manifest after export: %+v", manifest.Chunks)
	}

	secondExport, err := exporter.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if !secondExport.IsEmpty {
		t.Fatalf("expected second export to be empty, got %+v", secondExport)
	}

	dstStore := newTestStore(t)
	importer := New(dstStore, syncDir)

	importResult, err := importer.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if importResult.ChunksImported != 1 || importResult.ChunksSkipped != 0 {
		t.Fatalf("unexpected chunk import counts: %+v", importResult)
	}
	if importResult.SessionsImported != 1 || importResult.ObservationsImported != 1 || importResult.PromptsImported != 1 {
		t.Fatalf("unexpected imported row counts: %+v", importResult)
	}

	importAgain, err := importer.Import()
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if importAgain.ChunksImported != 0 || importAgain.ChunksSkipped != 1 {
		t.Fatalf("unexpected second import result: %+v", importAgain)
	}

	dstData, err := dstStore.Export()
	if err != nil {
		t.Fatalf("export destination data: %v", err)
	}
	if len(dstData.Sessions) != 1 || dstData.Sessions[0].Project != "proj-a" {
		t.Fatalf("unexpected destination sessions: %+v", dstData.Sessions)
	}
}

func TestExportErrors(t *testing.T) {
	t.Run("create chunks dir", func(t *testing.T) {
		s := newTestStore(t)
		badPath := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(badPath, []byte("x"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}

		sy := New(s, badPath)
		if _, err := sy.Export("alice", ""); err == nil || !strings.Contains(err.Error(), "create chunks dir") {
			t.Fatalf("expected create chunks dir error, got %v", err)
		}
	})

	t.Run("invalid manifest", func(t *testing.T) {
		s := newTestStore(t)
		syncDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(syncDir, "manifest.json"), []byte("not-json"), 0o644); err != nil {
			t.Fatalf("write invalid manifest: %v", err)
		}

		sy := New(s, syncDir)
		if _, err := sy.Export("alice", ""); err == nil || !strings.Contains(err.Error(), "parse manifest") {
			t.Fatalf("expected parse manifest error, got %v", err)
		}
	})

	t.Run("get synced chunks", func(t *testing.T) {
		s := newTestStore(t)
		syncDir := t.TempDir()
		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}

		sy := New(s, syncDir)
		if _, err := sy.Export("alice", ""); err == nil || !strings.Contains(err.Error(), "get synced chunks") {
			t.Fatalf("expected get synced chunks error, got %v", err)
		}
	})

	t.Run("already known chunk id", func(t *testing.T) {
		s := newTestStore(t)
		seedStoreForSync(t, s)
		sy := New(s, t.TempDir())

		data, err := s.Export()
		if err != nil {
			t.Fatalf("store export: %v", err)
		}
		chunk := sy.filterNewData(data, "")
		chunkJSON, err := json.Marshal(chunk)
		if err != nil {
			t.Fatalf("marshal chunk: %v", err)
		}
		hash := sha256.Sum256(chunkJSON)
		chunkID := hex.EncodeToString(hash[:])[:8]

		writeManifestFile(t, sy.syncDir, &Manifest{
			Version: 1,
			Chunks: []ChunkEntry{{
				ID:        chunkID,
				CreatedBy: "alice",
				CreatedAt: "2000-01-01T00:00:00Z",
			}},
		})

		res, err := sy.Export("alice", "")
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		if !res.IsEmpty {
			t.Fatalf("expected empty export for known chunk hash, got %+v", res)
		}
	})

	t.Run("store export error", func(t *testing.T) {
		resetSyncTestHooks(t)
		s := newTestStore(t)
		sy := New(s, t.TempDir())

		storeExportData = func(_ *store.Store) (*store.ExportData, error) {
			return nil, errors.New("boom export")
		}

		if _, err := sy.Export("alice", ""); err == nil || !strings.Contains(err.Error(), "export data") {
			t.Fatalf("expected export data error, got %v", err)
		}
	})

	t.Run("marshal chunk error", func(t *testing.T) {
		resetSyncTestHooks(t)
		s := newTestStore(t)
		seedStoreForSync(t, s)
		sy := New(s, t.TempDir())

		jsonMarshalChunk = func(v any) ([]byte, error) {
			return nil, fmt.Errorf("forced marshal error: %T", v)
		}

		if _, err := sy.Export("alice", ""); err == nil || !strings.Contains(err.Error(), "marshal chunk") {
			t.Fatalf("expected marshal chunk error, got %v", err)
		}
	})

	t.Run("write chunk error", func(t *testing.T) {
		resetSyncTestHooks(t)
		s := newTestStore(t)
		seedStoreForSync(t, s)
		sy := New(s, t.TempDir())

		gzipWriterFactory = func(_ *os.File) gzipWriter {
			return &fakeGzipWriter{writeErr: errors.New("forced gzip write")}
		}

		if _, err := sy.Export("alice", ""); err == nil || !strings.Contains(err.Error(), "write chunk") {
			t.Fatalf("expected write chunk error, got %v", err)
		}
	})

	t.Run("write manifest error", func(t *testing.T) {
		resetSyncTestHooks(t)
		s := newTestStore(t)
		seedStoreForSync(t, s)
		syncDir := t.TempDir()
		jsonMarshalManifest = func(v any, prefix, indent string) ([]byte, error) {
			_ = v
			_ = prefix
			_ = indent
			return nil, errors.New("forced manifest marshal failure")
		}

		sy := New(s, syncDir)
		if _, err := sy.Export("alice", ""); err == nil || !strings.Contains(err.Error(), "write manifest") {
			t.Fatalf("expected write manifest error, got %v", err)
		}
	})

	t.Run("record synced chunk error", func(t *testing.T) {
		resetSyncTestHooks(t)
		s := newTestStore(t)
		seedStoreForSync(t, s)
		sy := New(s, t.TempDir())

		storeRecordSynced = func(_ *store.Store, _ string) error {
			return errors.New("forced record failure")
		}

		if _, err := sy.Export("alice", ""); err == nil || !strings.Contains(err.Error(), "record synced chunk") {
			t.Fatalf("expected record synced chunk error, got %v", err)
		}
	})
}

func TestImportBranches(t *testing.T) {
	t.Run("read manifest error", func(t *testing.T) {
		s := newTestStore(t)
		syncDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(syncDir, "manifest.json"), []byte("{bad"), 0o644); err != nil {
			t.Fatalf("write invalid manifest: %v", err)
		}

		sy := New(s, syncDir)
		if _, err := sy.Import(); err == nil || !strings.Contains(err.Error(), "parse manifest") {
			t.Fatalf("expected parse manifest error, got %v", err)
		}
	})

	t.Run("empty manifest", func(t *testing.T) {
		s := newTestStore(t)
		sy := New(s, t.TempDir())

		res, err := sy.Import()
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if *res != (ImportResult{}) {
			t.Fatalf("expected empty result, got %+v", res)
		}
	})

	t.Run("missing chunk file is skipped", func(t *testing.T) {
		s := newTestStore(t)
		syncDir := t.TempDir()
		writeManifestFile(t, syncDir, &Manifest{
			Version: 1,
			Chunks:  []ChunkEntry{{ID: "missing", CreatedBy: "alice", CreatedAt: time.Now().UTC().Format(time.RFC3339)}},
		})

		sy := New(s, syncDir)
		res, err := sy.Import()
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if res.ChunksImported != 0 || res.ChunksSkipped != 1 {
			t.Fatalf("expected one skipped chunk, got %+v", res)
		}
	})

	t.Run("invalid chunk json", func(t *testing.T) {
		s := newTestStore(t)
		syncDir := t.TempDir()
		id := "badjson"
		writeManifestFile(t, syncDir, &Manifest{
			Version: 1,
			Chunks:  []ChunkEntry{{ID: id, CreatedBy: "alice", CreatedAt: time.Now().UTC().Format(time.RFC3339)}},
		})

		chunksDir := filepath.Join(syncDir, "chunks")
		if err := os.MkdirAll(chunksDir, 0o755); err != nil {
			t.Fatalf("mkdir chunks: %v", err)
		}
		if err := writeGzip(filepath.Join(chunksDir, id+".jsonl.gz"), []byte("{not-valid-json")); err != nil {
			t.Fatalf("write bad gzip chunk: %v", err)
		}

		sy := New(s, syncDir)
		if _, err := sy.Import(); err == nil || !strings.Contains(err.Error(), "parse chunk") {
			t.Fatalf("expected parse chunk error, got %v", err)
		}
	})

	t.Run("store import error", func(t *testing.T) {
		s := newTestStore(t)
		syncDir := t.TempDir()
		id := "broken"
		writeManifestFile(t, syncDir, &Manifest{
			Version: 1,
			Chunks:  []ChunkEntry{{ID: id, CreatedBy: "alice", CreatedAt: time.Now().UTC().Format(time.RFC3339)}},
		})

		chunk := ChunkData{
			Observations: []store.Observation{{
				ID:        1,
				SessionID: "missing-session",
				Type:      "bugfix",
				Title:     "broken",
				Content:   "missing session should violate FK",
				Scope:     "project",
				CreatedAt: "2025-01-01 00:00:01",
				UpdatedAt: "2025-01-01 00:00:01",
			}},
		}
		payload, err := json.Marshal(chunk)
		if err != nil {
			t.Fatalf("marshal chunk: %v", err)
		}

		chunksDir := filepath.Join(syncDir, "chunks")
		if err := os.MkdirAll(chunksDir, 0o755); err != nil {
			t.Fatalf("mkdir chunks: %v", err)
		}
		if err := writeGzip(filepath.Join(chunksDir, id+".jsonl.gz"), payload); err != nil {
			t.Fatalf("write gzip chunk: %v", err)
		}

		sy := New(s, syncDir)
		if _, err := sy.Import(); err == nil || !strings.Contains(err.Error(), "import chunk") {
			t.Fatalf("expected import chunk error, got %v", err)
		}
	})

	t.Run("get synced chunks", func(t *testing.T) {
		s := newTestStore(t)
		syncDir := t.TempDir()
		writeManifestFile(t, syncDir, &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: "c1", CreatedAt: time.Now().UTC().Format(time.RFC3339)}}})
		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}

		sy := New(s, syncDir)
		if _, err := sy.Import(); err == nil || !strings.Contains(err.Error(), "get synced chunks") {
			t.Fatalf("expected get synced chunks error, got %v", err)
		}
	})

	t.Run("record chunk error", func(t *testing.T) {
		resetSyncTestHooks(t)
		s := newTestStore(t)
		syncDir := t.TempDir()
		id := "okchunk"
		writeManifestFile(t, syncDir, &Manifest{
			Version: 1,
			Chunks:  []ChunkEntry{{ID: id, CreatedBy: "alice", CreatedAt: "2025-01-01T00:00:00Z"}},
		})

		chunk := ChunkData{
			Sessions: []store.Session{{ID: "s1", Project: "p", Directory: "/tmp", StartedAt: "2025-01-01 00:00:00"}},
		}
		payload, err := json.Marshal(chunk)
		if err != nil {
			t.Fatalf("marshal chunk: %v", err)
		}
		chunksDir := filepath.Join(syncDir, "chunks")
		if err := os.MkdirAll(chunksDir, 0o755); err != nil {
			t.Fatalf("mkdir chunks: %v", err)
		}
		if err := writeGzip(filepath.Join(chunksDir, id+".jsonl.gz"), payload); err != nil {
			t.Fatalf("write gzip chunk: %v", err)
		}

		storeRecordSynced = func(_ *store.Store, _ string) error {
			return errors.New("forced import record fail")
		}

		sy := New(s, syncDir)
		if _, err := sy.Import(); err == nil || !strings.Contains(err.Error(), "record chunk") {
			t.Fatalf("expected record chunk error, got %v", err)
		}
	})
}

func TestManifestReadWrite(t *testing.T) {
	syncDir := t.TempDir()
	sy := New(nil, syncDir)

	missing, err := sy.readManifest()
	if err != nil {
		t.Fatalf("read missing manifest: %v", err)
	}
	if missing.Version != 1 || len(missing.Chunks) != 0 {
		t.Fatalf("unexpected default manifest: %+v", missing)
	}

	want := &Manifest{
		Version: 1,
		Chunks:  []ChunkEntry{{ID: "abc12345", CreatedBy: "alice", CreatedAt: "2025-01-01T00:00:00Z", Sessions: 1, Memories: 2, Prompts: 3}},
	}
	if err := sy.writeManifest(want); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	got, err := sy.readManifest()
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(got.Chunks) != 1 || got.Chunks[0].ID != want.Chunks[0].ID || got.Chunks[0].Memories != 2 {
		t.Fatalf("manifest roundtrip mismatch: %+v", got)
	}

	if err := os.WriteFile(filepath.Join(syncDir, "manifest.json"), []byte("{"), 0o644); err != nil {
		t.Fatalf("write invalid manifest: %v", err)
	}
	if _, err := sy.readManifest(); err == nil || !strings.Contains(err.Error(), "parse manifest") {
		t.Fatalf("expected parse manifest error, got %v", err)
	}

	badSyncPath := filepath.Join(t.TempDir(), "not-dir")
	if err := os.WriteFile(badSyncPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write non-dir sync path: %v", err)
	}
	syBad := New(nil, badSyncPath)
	if _, err := syBad.readManifest(); err == nil || !strings.Contains(err.Error(), "read manifest") {
		t.Fatalf("expected read manifest error, got %v", err)
	}
	if err := syBad.writeManifest(&Manifest{Version: 1}); err == nil {
		t.Fatal("expected write manifest error for non-directory sync path")
	}

	t.Run("marshal manifest error", func(t *testing.T) {
		resetSyncTestHooks(t)
		sy := New(nil, t.TempDir())
		jsonMarshalManifest = func(v any, prefix, indent string) ([]byte, error) {
			_ = v
			_ = prefix
			_ = indent
			return nil, errors.New("forced manifest marshal error")
		}

		if err := sy.writeManifest(&Manifest{Version: 1}); err == nil || !strings.Contains(err.Error(), "marshal manifest") {
			t.Fatalf("expected marshal manifest error, got %v", err)
		}
	})
}

func TestStatus(t *testing.T) {
	t.Run("read manifest error", func(t *testing.T) {
		s := newTestStore(t)
		syncDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(syncDir, "manifest.json"), []byte("not-json"), 0o644); err != nil {
			t.Fatalf("write invalid manifest: %v", err)
		}

		sy := New(s, syncDir)
		if _, _, _, err := sy.Status(); err == nil {
			t.Fatal("expected status to fail on invalid manifest")
		}
	})

	s := newTestStore(t)
	syncDir := t.TempDir()
	sy := New(s, syncDir)

	if err := sy.writeManifest(&Manifest{
		Version: 1,
		Chunks:  []ChunkEntry{{ID: "c1", CreatedAt: "2025-01-01T00:00:00Z"}, {ID: "c2", CreatedAt: "2025-01-02T00:00:00Z"}},
	}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := s.RecordSyncedChunk("c1"); err != nil {
		t.Fatalf("record synced chunk: %v", err)
	}

	local, remote, pending, err := sy.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if local != 1 || remote != 2 || pending != 1 {
		t.Fatalf("unexpected status values: local=%d remote=%d pending=%d", local, remote, pending)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, _, _, err := sy.Status(); err == nil {
		t.Fatal("expected status error with closed store")
	}
}

func TestFilterFunctionsAndTimeNormalization(t *testing.T) {
	data := &store.ExportData{
		Version:    "0.1.0",
		ExportedAt: "2025-01-01 00:00:00",
		Sessions: []store.Session{
			{ID: "s1", Project: "proj-a", StartedAt: "2025-01-01 10:00:00"},
			{ID: "s2", Project: "proj-b", StartedAt: "2025-01-01 11:00:00"},
		},
		Observations: []store.Observation{
			{ID: 1, SessionID: "s1", CreatedAt: "2025-01-01 10:00:00"},
			{ID: 2, SessionID: "s2", CreatedAt: "2025-01-01 11:00:00"},
		},
		Prompts: []store.Prompt{
			{ID: 1, SessionID: "s1", CreatedAt: "2025-01-01 10:00:00"},
			{ID: 2, SessionID: "s2", CreatedAt: "2025-01-01 11:00:00"},
		},
	}

	projectOnly := filterByProject(data, "proj-a")
	if len(projectOnly.Sessions) != 1 || projectOnly.Sessions[0].ID != "s1" {
		t.Fatalf("unexpected filtered sessions: %+v", projectOnly.Sessions)
	}
	if len(projectOnly.Observations) != 1 || projectOnly.Observations[0].SessionID != "s1" {
		t.Fatalf("unexpected filtered observations: %+v", projectOnly.Observations)
	}
	if len(projectOnly.Prompts) != 1 || projectOnly.Prompts[0].SessionID != "s1" {
		t.Fatalf("unexpected filtered prompts: %+v", projectOnly.Prompts)
	}

	sy := New(nil, t.TempDir())
	all := sy.filterNewData(data, "")
	if len(all.Sessions) != 2 || len(all.Observations) != 2 || len(all.Prompts) != 2 {
		t.Fatalf("expected first sync to include all data, got %+v", all)
	}

	newOnly := sy.filterNewData(data, "2025-01-01T10:30:00Z")
	if len(newOnly.Sessions) != 1 || newOnly.Sessions[0].ID != "s2" {
		t.Fatalf("unexpected new sessions: %+v", newOnly.Sessions)
	}
	if len(newOnly.Observations) != 1 || newOnly.Observations[0].ID != 2 {
		t.Fatalf("unexpected new observations: %+v", newOnly.Observations)
	}
	if len(newOnly.Prompts) != 1 || newOnly.Prompts[0].ID != 2 {
		t.Fatalf("unexpected new prompts: %+v", newOnly.Prompts)
	}

	if got := normalizeTime("2025-01-01T15:04:05Z"); got != "2025-01-01 15:04:05" {
		t.Fatalf("unexpected RFC3339 normalization: %q", got)
	}
	if got := normalizeTime(" 2025-01-01 15:04:05 "); got != "2025-01-01 15:04:05" {
		t.Fatalf("unexpected plain normalization: %q", got)
	}

	m := &Manifest{Chunks: []ChunkEntry{{ID: "old", CreatedAt: "2025-01-01T00:00:00Z"}, {ID: "new", CreatedAt: "2025-02-01T00:00:00Z"}}}
	if got := sy.lastChunkTime(m); got != "2025-02-01T00:00:00Z" {
		t.Fatalf("unexpected last chunk time: %q", got)
	}
}

func TestGzipHelpers(t *testing.T) {
	t.Run("roundtrip", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "chunk.jsonl.gz")
		payload := []byte(`{"sessions":1,"observations":2}`)

		if err := writeGzip(path, payload); err != nil {
			t.Fatalf("write gzip: %v", err)
		}

		got, err := readGzip(path)
		if err != nil {
			t.Fatalf("read gzip: %v", err)
		}
		if string(got) != string(payload) {
			t.Fatalf("gzip mismatch: got %q want %q", got, payload)
		}
	})

	t.Run("write error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", "chunk.gz")
		if err := writeGzip(path, []byte("x")); err == nil {
			t.Fatal("expected writeGzip error for missing parent dir")
		}
	})

	t.Run("read error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-gzip")
		if err := os.WriteFile(path, []byte("plain text"), 0o644); err != nil {
			t.Fatalf("write plain file: %v", err)
		}

		if _, err := readGzip(path); err == nil {
			t.Fatal("expected readGzip error for non-gzip file")
		}
	})

	t.Run("gzip write and close errors", func(t *testing.T) {
		resetSyncTestHooks(t)
		path := filepath.Join(t.TempDir(), "chunk.gz")

		gzipWriterFactory = func(_ *os.File) gzipWriter {
			return &fakeGzipWriter{writeErr: errors.New("forced write error")}
		}
		if err := writeGzip(path, []byte("x")); err == nil {
			t.Fatal("expected forced gzip write error")
		}

		gzipWriterFactory = func(_ *os.File) gzipWriter {
			return &fakeGzipWriter{closeErr: errors.New("forced close error")}
		}
		if err := writeGzip(path, []byte("x")); err == nil {
			t.Fatal("expected forced gzip close error")
		}
	})
}

func TestGetUsernameAndManifestSummary(t *testing.T) {
	t.Run("username precedence", func(t *testing.T) {
		t.Setenv("USER", "")
		t.Setenv("USERNAME", "windows-user")
		if got := GetUsername(); got != "windows-user" {
			t.Fatalf("expected USERNAME fallback, got %q", got)
		}

		t.Setenv("USER", "unix-user")
		t.Setenv("USERNAME", "windows-user")
		if got := GetUsername(); got != "unix-user" {
			t.Fatalf("expected USER to win, got %q", got)
		}

		t.Setenv("USER", "")
		t.Setenv("USERNAME", "")
		if got := GetUsername(); got == "" {
			t.Fatal("expected hostname or unknown fallback")
		}

		resetSyncTestHooks(t)
		osHostname = func() (string, error) {
			return "", errors.New("forced no hostname")
		}
		if got := GetUsername(); got != "unknown" {
			t.Fatalf("expected unknown fallback, got %q", got)
		}
	})

	t.Run("manifest summary", func(t *testing.T) {
		empty := ManifestSummary(&Manifest{Version: 1})
		if empty != "No chunks synced yet." {
			t.Fatalf("unexpected empty summary: %q", empty)
		}

		summary := ManifestSummary(&Manifest{Chunks: []ChunkEntry{
			{ID: "1", CreatedBy: "bob", Sessions: 1, Memories: 2},
			{ID: "2", CreatedBy: "alice", Sessions: 2, Memories: 3},
			{ID: "3", CreatedBy: "alice", Sessions: 1, Memories: 1},
		}})

		if !strings.Contains(summary, "3 chunks") || !strings.Contains(summary, "6 memories") || !strings.Contains(summary, "4 sessions") {
			t.Fatalf("summary totals missing: %q", summary)
		}
		if !strings.Contains(summary, "alice (2 chunks), bob (1 chunks)") {
			t.Fatalf("summary contributors not sorted or counted: %q", summary)
		}
	})
}
