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

	"github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	cfg, err := store.DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
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

func writeLocalChunkFile(t *testing.T, dir, id string, chunk ChunkData) {
	t.Helper()
	payload, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal chunk %s: %v", id, err)
	}
	chunksDir := filepath.Join(dir, "chunks")
	if err := os.MkdirAll(chunksDir, 0o755); err != nil {
		t.Fatalf("mkdir chunks: %v", err)
	}
	if err := writeGzip(filepath.Join(chunksDir, id+".jsonl.gz"), payload); err != nil {
		t.Fatalf("write gzip chunk %s: %v", id, err)
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
	origStoreExportDataForProject := storeExportDataForProject
	origStoreListMutationsAfterSeq := storeListMutationsAfterSeq
	origStoreAckMutationSeq := storeAckMutationSeq
	origStoreApplyPulledChunk := storeApplyPulledChunk
	origStoreRecordSynced := storeRecordSynced

	t.Cleanup(func() {
		jsonMarshalChunk = origJSONMarshalChunk
		jsonMarshalManifest = origJSONMarshalManifest
		osCreateFile = origOSCreateFile
		gzipWriterFactory = origGzipWriterFactory
		osHostname = origOSHostname
		storeGetSynced = origStoreGetSynced
		storeExportData = origStoreExportData
		storeExportDataForProject = origStoreExportDataForProject
		storeListMutationsAfterSeq = origStoreListMutationsAfterSeq
		storeAckMutationSeq = origStoreAckMutationSeq
		storeApplyPulledChunk = origStoreApplyPulledChunk
		storeRecordSynced = origStoreRecordSynced
	})
}

type fakeGzipWriter struct {
	writeErr error
	closeErr error
}

type fakeCloudTransport struct {
	manifest          *Manifest
	chunks            map[string][]byte
	lastCreatedBy     string
	readChunkErr      error
	readManifestCalls int
	writeChunkCalls   int
	readChunkCalls    int
}

type fakeUpgradeHooks struct {
	stopCalls   int
	resumeCalls int
	stopErr     error
	resumeErr   error
}

func (h *fakeUpgradeHooks) StopForUpgrade(_ string) error {
	h.stopCalls++
	return h.stopErr
}

func (h *fakeUpgradeHooks) ResumeAfterUpgrade(_ string) error {
	h.resumeCalls++
	return h.resumeErr
}

func newFakeCloudTransport() *fakeCloudTransport {
	return &fakeCloudTransport{
		manifest: &Manifest{Version: 1},
		chunks:   map[string][]byte{},
	}
}

func (f *fakeCloudTransport) ReadManifest() (*Manifest, error) {
	f.readManifestCalls++
	return f.manifest, nil
}

func (f *fakeCloudTransport) WriteManifest(m *Manifest) error {
	f.manifest = m
	return nil
}

func (f *fakeCloudTransport) WriteChunk(chunkID string, data []byte, entry ChunkEntry) error {
	f.writeChunkCalls++
	f.chunks[chunkID] = data
	f.lastCreatedBy = entry.CreatedBy
	return nil
}

type mutableUpgradeHooks struct {
	stopCalls   int
	resumeCalls int
	stopErr     error
	resumeErr   error
	onStop      func()
}

func (h *mutableUpgradeHooks) StopForUpgrade(_ string) error {
	h.stopCalls++
	if h.onStop != nil {
		h.onStop()
	}
	return h.stopErr
}

func (h *mutableUpgradeHooks) ResumeAfterUpgrade(_ string) error {
	h.resumeCalls++
	return h.resumeErr
}

func (f *fakeCloudTransport) ReadChunk(chunkID string) ([]byte, error) {
	f.readChunkCalls++
	if f.readChunkErr != nil {
		return nil, f.readChunkErr
	}
	data, ok := f.chunks[chunkID]
	if !ok {
		return nil, ErrChunkNotFound
	}
	return data, nil
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

func TestUpgradeDeterministicReasonCodes(t *testing.T) {
	tests := []struct {
		name            string
		project         string
		cloudConfigured bool
		enrolled        bool
		policyDenied    bool
		wantClass       string
		wantCode        string
		wantStatus      string
		wantErrContains string
	}{
		{
			name:            "ready when configured and enrolled",
			project:         "proj-a",
			cloudConfigured: true,
			enrolled:        true,
			wantClass:       UpgradeReasonClassReady,
			wantCode:        UpgradeReasonReady,
			wantStatus:      UpgradeStatusReady,
		},
		{
			name:            "repairable when unenrolled",
			project:         "proj-a",
			cloudConfigured: true,
			enrolled:        false,
			wantClass:       UpgradeReasonClassRepairable,
			wantCode:        UpgradeReasonRepairableUnenrolled,
			wantStatus:      UpgradeStatusBlocked,
		},
		{
			name:            "policy when cloud config missing",
			project:         "proj-a",
			cloudConfigured: false,
			enrolled:        false,
			wantClass:       UpgradeReasonClassPolicy,
			wantCode:        UpgradeReasonPolicyConfig,
			wantStatus:      UpgradeStatusBlocked,
		},
		{
			name:            "policy forbidden explicit",
			project:         "proj-a",
			cloudConfigured: true,
			enrolled:        false,
			policyDenied:    true,
			wantClass:       UpgradeReasonClassPolicy,
			wantCode:        UpgradeReasonPolicyForbidden,
			wantStatus:      UpgradeStatusBlocked,
		},
		{
			name:            "blocked project required fails loudly",
			project:         "   ",
			cloudConfigured: true,
			wantErrContains: "project is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report, err := DiagnoseCloudUpgrade(UpgradeDiagnosisInput{
				Project:         tc.project,
				CloudConfigured: tc.cloudConfigured,
				ProjectEnrolled: tc.enrolled,
				PolicyDenied:    tc.policyDenied,
			})
			if tc.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErrContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected diagnosis error: %v", err)
			}
			if report.Class != tc.wantClass || report.Code != tc.wantCode || report.Status != tc.wantStatus {
				t.Fatalf("unexpected report: %+v", report)
			}

			report2, err := DiagnoseCloudUpgrade(UpgradeDiagnosisInput{
				Project:         tc.project,
				CloudConfigured: tc.cloudConfigured,
				ProjectEnrolled: tc.enrolled,
				PolicyDenied:    tc.policyDenied,
			})
			if err != nil {
				t.Fatalf("second diagnosis error: %v", err)
			}
			if report != report2 {
				t.Fatalf("expected deterministic reports, got %+v and %+v", report, report2)
			}
		})
	}
}

func TestUpgradeBootstrapCheckpointResume(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("bootstrap-s1", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{SessionID: "bootstrap-s1", Type: "decision", Title: "seed", Content: "seed", Project: "proj-a", Scope: "project"}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{
		Project:     "proj-a",
		Stage:       store.UpgradeStageBootstrapEnrolled,
		RepairClass: store.UpgradeRepairClassRepairable,
	}); err != nil {
		t.Fatalf("seed checkpoint stage: %v", err)
	}

	transport := newFakeCloudTransport()
	result, err := BootstrapProject(s, transport, UpgradeBootstrapOptions{Project: "proj-a", CreatedBy: "upgrade-test"})
	if err != nil {
		t.Fatalf("bootstrap from checkpoint: %v", err)
	}
	if !result.Resumed || result.Stage != store.UpgradeStageBootstrapVerified {
		t.Fatalf("expected resumed verified bootstrap, got %+v", result)
	}
	if transport.writeChunkCalls == 0 {
		t.Fatal("expected first push to write at least one chunk")
	}

	writeCallsBefore := transport.writeChunkCalls
	result2, err := BootstrapProject(s, transport, UpgradeBootstrapOptions{Project: "proj-a", CreatedBy: "upgrade-test"})
	if err != nil {
		t.Fatalf("second bootstrap resume: %v", err)
	}
	if !result2.NoOp || result2.Stage != store.UpgradeStageBootstrapVerified {
		t.Fatalf("expected no-op verified bootstrap rerun, got %+v", result2)
	}
	if transport.writeChunkCalls != writeCallsBefore {
		t.Fatalf("expected no additional push writes on rerun, before=%d after=%d", writeCallsBefore, transport.writeChunkCalls)
	}
}

func TestRollbackProjectInvokesAutosyncHooksAndHonorsBoundary(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{
		Project:     "proj-a",
		Stage:       store.UpgradeStageBootstrapPushed,
		RepairClass: store.UpgradeRepairClassRepairable,
		Snapshot: store.CloudUpgradeSnapshot{
			CloudConfigPresent: true,
			ProjectEnrolled:    false,
		},
	}); err != nil {
		t.Fatalf("seed rollback state: %v", err)
	}

	hooks := &fakeUpgradeHooks{}
	result, err := RollbackProject(s, UpgradeRollbackOptions{Project: "proj-a", Hooks: hooks})
	if err != nil {
		t.Fatalf("rollback project: %v", err)
	}
	if result.Stage != store.UpgradeStageRolledBack {
		t.Fatalf("expected rolled_back stage, got %q", result.Stage)
	}
	if hooks.stopCalls != 1 || hooks.resumeCalls != 1 {
		t.Fatalf("expected one stop/resume call, got stop=%d resume=%d", hooks.stopCalls, hooks.resumeCalls)
	}

	if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{Project: "proj-a", Stage: store.UpgradeStageBootstrapVerified, RepairClass: store.UpgradeRepairClassReady}); err != nil {
		t.Fatalf("seed verified boundary: %v", err)
	}
	hooks = &fakeUpgradeHooks{}
	_, err = RollbackProject(s, UpgradeRollbackOptions{Project: "proj-a", Hooks: hooks})
	if err == nil || !strings.Contains(err.Error(), "rollback is unavailable post-bootstrap") {
		t.Fatalf("expected post-bootstrap rollback failure, got %v", err)
	}
	if hooks.stopCalls != 0 || hooks.resumeCalls != 0 {
		t.Fatalf("hooks must not run after rollback boundary, got stop=%d resume=%d", hooks.stopCalls, hooks.resumeCalls)
	}
}

func TestBootstrapProjectValidationAndCreatedByDefault(t *testing.T) {
	t.Run("store is required", func(t *testing.T) {
		_, err := BootstrapProject(nil, newFakeCloudTransport(), UpgradeBootstrapOptions{Project: "proj-a"})
		if err == nil || !strings.Contains(err.Error(), "requires store") {
			t.Fatalf("expected requires store error, got %v", err)
		}
	})

	t.Run("project is required", func(t *testing.T) {
		s := newTestStore(t)
		_, err := BootstrapProject(s, newFakeCloudTransport(), UpgradeBootstrapOptions{Project: "   "})
		if err == nil || !strings.Contains(err.Error(), "requires project") {
			t.Fatalf("expected requires project error, got %v", err)
		}
	})

	t.Run("blank createdBy defaults to upgrade-bootstrap", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("bootstrap-default-createdby", "proj-a", "/tmp/proj-a"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(store.AddObservationParams{SessionID: "bootstrap-default-createdby", Type: "decision", Title: "seed", Content: "seed", Project: "proj-a", Scope: "project"}); err != nil {
			t.Fatalf("add observation: %v", err)
		}

		transport := newFakeCloudTransport()
		result, err := BootstrapProject(s, transport, UpgradeBootstrapOptions{Project: "proj-a", CreatedBy: "   "})
		if err != nil {
			t.Fatalf("bootstrap project: %v", err)
		}
		if result.Project != "proj-a" || result.Stage != store.UpgradeStageBootstrapVerified || result.Resumed {
			t.Fatalf("unexpected bootstrap result: %+v", result)
		}
		if transport.writeChunkCalls == 0 {
			t.Fatal("expected first bootstrap push to write at least one chunk")
		}
		if transport.lastCreatedBy != "upgrade-bootstrap" {
			t.Fatalf("expected default createdBy upgrade-bootstrap, got %q", transport.lastCreatedBy)
		}
	})
}

func TestRollbackProjectHandlesHookFailures(t *testing.T) {
	t.Run("stop hook failure blocks rollback", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{
			Project:     "proj-a",
			Stage:       store.UpgradeStageBootstrapPushed,
			RepairClass: store.UpgradeRepairClassRepairable,
			Snapshot: store.CloudUpgradeSnapshot{
				ProjectEnrolled: false,
			},
		}); err != nil {
			t.Fatalf("seed rollback state: %v", err)
		}

		hooks := &mutableUpgradeHooks{stopErr: errors.New("stop failed")}
		_, err := RollbackProject(s, UpgradeRollbackOptions{Project: "proj-a", Hooks: hooks})
		if err == nil || !strings.Contains(err.Error(), "stop autosync") {
			t.Fatalf("expected stop autosync error, got %v", err)
		}
		if hooks.stopCalls != 1 || hooks.resumeCalls != 0 {
			t.Fatalf("unexpected hook call counts: stop=%d resume=%d", hooks.stopCalls, hooks.resumeCalls)
		}
	})

	t.Run("rollback failure attempts resume hook", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{
			Project:     "proj-a",
			Stage:       store.UpgradeStageBootstrapPushed,
			RepairClass: store.UpgradeRepairClassRepairable,
			Snapshot: store.CloudUpgradeSnapshot{
				ProjectEnrolled: false,
			},
		}); err != nil {
			t.Fatalf("seed rollback state: %v", err)
		}

		hooks := &mutableUpgradeHooks{
			onStop: func() {
				_ = s.ClearCloudUpgradeState("proj-a")
			},
		}
		_, err := RollbackProject(s, UpgradeRollbackOptions{Project: "proj-a", Hooks: hooks})
		if err == nil || !strings.Contains(err.Error(), "rollback requires existing upgrade checkpoint state") {
			t.Fatalf("expected rollback state missing error, got %v", err)
		}
		if hooks.stopCalls != 1 || hooks.resumeCalls != 1 {
			t.Fatalf("expected rollback failure to invoke resume once, got stop=%d resume=%d", hooks.stopCalls, hooks.resumeCalls)
		}
	})

	t.Run("resume hook failure is surfaced", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{
			Project:     "proj-a",
			Stage:       store.UpgradeStageBootstrapPushed,
			RepairClass: store.UpgradeRepairClassRepairable,
			Snapshot: store.CloudUpgradeSnapshot{
				ProjectEnrolled: false,
			},
		}); err != nil {
			t.Fatalf("seed rollback state: %v", err)
		}

		hooks := &mutableUpgradeHooks{resumeErr: errors.New("resume failed")}
		_, err := RollbackProject(s, UpgradeRollbackOptions{Project: "proj-a", Hooks: hooks})
		if err == nil || !strings.Contains(err.Error(), "resume autosync") {
			t.Fatalf("expected resume autosync error, got %v", err)
		}
		if hooks.stopCalls != 1 || hooks.resumeCalls != 1 {
			t.Fatalf("expected stop/resume once, got stop=%d resume=%d", hooks.stopCalls, hooks.resumeCalls)
		}
	})
}

func TestCloudSyncExportBehaviorUnchangedWhenUpgradeStateExists(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("sync-regression", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{SessionID: "sync-regression", Type: "decision", Title: "regression", Content: "keep legacy behavior", Project: "proj-a", Scope: "project"}); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{Project: "proj-a", Stage: store.UpgradeStageDoctorBlocked, RepairClass: store.UpgradeRepairClassRepairable, LastErrorCode: "upgrade_repairable_unenrolled", LastErrorMessage: "legacy drift"}); err != nil {
		t.Fatalf("seed upgrade state: %v", err)
	}

	transport := newFakeCloudTransport()
	cloudSyncer := NewCloudWithTransport(s, transport, "proj-a")
	result, err := cloudSyncer.Export("regression", "proj-a")
	if err != nil {
		t.Fatalf("cloud export: %v", err)
	}
	if result.IsEmpty {
		t.Fatalf("expected non-empty export to preserve legacy sync path, got %+v", result)
	}
	if transport.writeChunkCalls == 0 {
		t.Fatalf("expected cloud export to push at least one chunk")
	}

	state, err := s.GetCloudUpgradeState("proj-a")
	if err != nil {
		t.Fatalf("load upgrade state: %v", err)
	}
	if state == nil || state.Stage != store.UpgradeStageDoctorBlocked {
		t.Fatalf("legacy cloud export must not mutate upgrade stage, got %+v", state)
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

		storeRecordSynced = func(_ *store.Store, _, _ string) error {
			return errors.New("forced record failure")
		}

		if _, err := sy.Export("alice", ""); err == nil || !strings.Contains(err.Error(), "record synced chunk") {
			t.Fatalf("expected record synced chunk error, got %v", err)
		}
	})
}

func TestExportUsesProjectScopedStoreExportWhenProjectProvided(t *testing.T) {
	resetSyncTestHooks(t)
	s := newTestStore(t)
	sy := New(s, t.TempDir())

	projectExportCalled := 0
	storeExportData = func(_ *store.Store) (*store.ExportData, error) {
		return nil, errors.New("global export should not be called for project-scoped sync")
	}
	storeExportDataForProject = func(_ *store.Store, project string) (*store.ExportData, error) {
		projectExportCalled++
		if project != "proj-a" {
			t.Fatalf("expected project proj-a, got %q", project)
		}
		return &store.ExportData{Version: "0.1.0", ExportedAt: time.Now().UTC().Format(time.RFC3339)}, nil
	}

	res, err := sy.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !res.IsEmpty {
		t.Fatalf("expected empty export result for empty project dataset, got %+v", res)
	}
	if projectExportCalled != 1 {
		t.Fatalf("expected project-scoped export hook to be called once, got %d", projectExportCalled)
	}
}

func TestExportDoesNotReconcileLocallyMissingChunkByOwnershipHeuristic(t *testing.T) {
	resetSyncTestHooks(t)
	s := newTestStore(t)
	seedStoreForSync(t, s)
	transport := newFakeCloudTransport()
	sy := NewWithTransport(s, transport)

	var recordCalls int
	storeRecordSynced = func(_ *store.Store, _, _ string) error {
		recordCalls++
		if recordCalls == 1 {
			return errors.New("forced record failure")
		}
		return nil
	}

	first, err := sy.Export("alice", "")
	if err == nil || !strings.Contains(err.Error(), "record synced chunk") {
		t.Fatalf("expected record synced chunk error, got result=%+v err=%v", first, err)
	}
	if transport.writeChunkCalls != 1 {
		t.Fatalf("expected first export to write one chunk, got %d", transport.writeChunkCalls)
	}

	second, err := sy.Export("alice", "")
	if err != nil {
		t.Fatalf("second export should reconcile local sync tracking: %v", err)
	}
	if second == nil || !second.IsEmpty {
		t.Fatalf("expected second export to be empty after reconciliation, got %+v", second)
	}
	if recordCalls != 1 {
		t.Fatalf("expected no ownership-based reconciliation retries, got %d calls", recordCalls)
	}
	if transport.writeChunkCalls != 1 {
		t.Fatalf("expected no duplicate remote chunk writes, got %d", transport.writeChunkCalls)
	}
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
		if _, err := sy.Import(); err == nil || !strings.Contains(err.Error(), "dependency-safe local import stalled") || !strings.Contains(err.Error(), "missing-session") {
			t.Fatalf("expected dependency-safe local import error, got %v", err)
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

		storeApplyPulledChunk = func(_ *store.Store, _, _ string, _ []store.SyncMutation) error {
			return errors.New("forced apply pulled chunk fail")
		}

		sy := New(s, syncDir)
		if _, err := sy.Import(); err == nil || !strings.Contains(err.Error(), "dependency-safe local import stalled") || !strings.Contains(err.Error(), "forced apply pulled chunk fail") {
			t.Fatalf("expected apply pulled chunk import error, got %v", err)
		}
	})
}

func TestLocalImportDependencySafeAcrossChunksRegardlessManifestOrder(t *testing.T) {
	s := newTestStore(t)
	syncDir := t.TempDir()
	project := "proj-a"

	writeManifestFile(t, syncDir, &Manifest{Version: 1, Chunks: []ChunkEntry{
		{ID: "chunk-dependent", CreatedAt: "2025-01-02T00:00:00Z"},
		{ID: "chunk-session", CreatedAt: "2025-01-01T00:00:00Z"},
	}})
	writeLocalChunkFile(t, syncDir, "chunk-dependent", ChunkData{
		Observations: []store.Observation{{SyncID: "obs-cross-chunk", SessionID: "sess-cross-chunk", Type: "note", Title: "cross", Content: "cross chunk observation", Project: &project, Scope: "project", CreatedAt: "2025-01-02 00:00:00", UpdatedAt: "2025-01-02 00:00:00"}},
		Prompts:      []store.Prompt{{SyncID: "prompt-cross-chunk", SessionID: "sess-cross-chunk", Content: "cross chunk prompt", Project: project, CreatedAt: "2025-01-02 00:01:00"}},
	})
	writeLocalChunkFile(t, syncDir, "chunk-session", ChunkData{
		Sessions: []store.Session{{ID: "sess-cross-chunk", Project: project, Directory: "/tmp/proj-a", StartedAt: "2025-01-01 00:00:00"}},
	})

	res, err := New(s, syncDir).Import()
	if err != nil {
		t.Fatalf("local import should retry dependency chunks safely: %v", err)
	}
	if res.ChunksImported != 2 || res.SessionsImported != 1 || res.ObservationsImported != 1 || res.PromptsImported != 1 {
		t.Fatalf("unexpected import result: %+v", res)
	}
	if _, err := s.GetSession("sess-cross-chunk"); err != nil {
		t.Fatalf("expected session imported: %v", err)
	}
	results, err := s.Search("cross chunk observation", store.SearchOptions{Project: project, Limit: 5})
	if err != nil || len(results) != 1 {
		t.Fatalf("expected imported observation, results=%d err=%v", len(results), err)
	}
	prompts, err := s.RecentPrompts(project, 5)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("expected imported prompt, prompts=%d err=%v", len(prompts), err)
	}
}

func TestLocalImportOrdersExplicitMutationsAndDirectArraysSafely(t *testing.T) {
	s := newTestStore(t)
	syncDir := t.TempDir()
	project := "proj-a"
	chunkID := "mixed-local"

	writeManifestFile(t, syncDir, &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: chunkID, CreatedAt: "2025-01-01T00:00:00Z"}}})
	writeLocalChunkFile(t, syncDir, chunkID, ChunkData{
		Sessions:     []store.Session{{ID: "sess-mixed", Project: project, Directory: "/tmp/proj-a", StartedAt: "2025-01-01 00:00:00"}},
		Observations: []store.Observation{{SyncID: "obs-direct-mixed", SessionID: "sess-mixed", Type: "note", Title: "direct", Content: "direct local observation", Project: &project, Scope: "project", CreatedAt: "2025-01-01 00:01:00", UpdatedAt: "2025-01-01 00:01:00"}},
		Mutations: []store.SyncMutation{{
			Entity:    store.SyncEntityPrompt,
			EntityKey: "prompt-explicit-mixed",
			Op:        store.SyncOpUpsert,
			Payload:   `{"sync_id":"prompt-explicit-mixed","session_id":"sess-mixed","content":"explicit prompt after session","project":"proj-a","created_at":"2025-01-01 00:02:00"}`,
		}},
	})

	if _, err := New(s, syncDir).Import(); err != nil {
		t.Fatalf("local import should order synthesized sessions before direct and explicit dependents: %v", err)
	}
	if _, err := s.GetSession("sess-mixed"); err != nil {
		t.Fatalf("expected mixed session imported: %v", err)
	}
	results, err := s.Search("direct local observation", store.SearchOptions{Project: project, Limit: 5})
	if err != nil || len(results) != 1 {
		t.Fatalf("expected direct observation, results=%d err=%v", len(results), err)
	}
	prompts, err := s.RecentPrompts(project, 5)
	if err != nil || len(prompts) != 1 || prompts[0].SyncID != "prompt-explicit-mixed" {
		t.Fatalf("expected explicit prompt imported, prompts=%+v err=%v", prompts, err)
	}
}

func TestLocalImportMissingSessionStallsWithUsefulError(t *testing.T) {
	s := newTestStore(t)
	syncDir := t.TempDir()
	chunkID := "missing-session"
	writeManifestFile(t, syncDir, &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: chunkID, CreatedAt: "2025-01-01T00:00:00Z"}}})
	writeLocalChunkFile(t, syncDir, chunkID, ChunkData{Observations: []store.Observation{{SyncID: "obs-missing-session", SessionID: "does-not-exist", Type: "note", Title: "missing", Content: "missing dependency", Scope: "project", CreatedAt: "2025-01-01 00:00:00", UpdatedAt: "2025-01-01 00:00:00"}}})

	_, err := New(s, syncDir).Import()
	if err == nil {
		t.Fatal("expected missing session dependency to stall")
	}
	msg := err.Error()
	if !strings.Contains(msg, "dependency-safe local import stalled") || !strings.Contains(msg, chunkID) || !strings.Contains(msg, "does-not-exist") {
		t.Fatalf("expected useful dependency-safe stall error, got %v", err)
	}
}

func TestLocalImportSkipsAlreadyImportedChunksIdempotently(t *testing.T) {
	resetSyncTestHooks(t)
	s := newTestStore(t)
	syncDir := t.TempDir()
	chunkID := "idempotent-local"
	writeManifestFile(t, syncDir, &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: chunkID, CreatedAt: "2025-01-01T00:00:00Z"}}})
	writeLocalChunkFile(t, syncDir, chunkID, ChunkData{Sessions: []store.Session{{ID: "sess-idempotent", Project: "proj-a", Directory: "/tmp/proj-a", StartedAt: "2025-01-01 00:00:00"}}})

	if _, err := New(s, syncDir).Import(); err != nil {
		t.Fatalf("first import: %v", err)
	}
	applyCalls := 0
	storeApplyPulledChunk = func(_ *store.Store, _, _ string, _ []store.SyncMutation) error {
		applyCalls++
		return errors.New("already imported chunks should not be applied")
	}
	res, err := New(s, syncDir).Import()
	if err != nil {
		t.Fatalf("second import should skip known chunk: %v", err)
	}
	if res.ChunksImported != 0 || res.ChunksSkipped != 1 || applyCalls != 0 {
		t.Fatalf("expected idempotent skip without apply, result=%+v applyCalls=%d", res, applyCalls)
	}
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

func TestCloudSyncPreflightBlocksUnenrolledBeforeTransport(t *testing.T) {
	s := newTestStore(t)
	seedStoreForSync(t, s)

	transport := newFakeCloudTransport()
	sy := NewCloudWithTransport(s, transport, "proj-a")

	if _, err := sy.Export("alice", "proj-a"); err == nil || !strings.Contains(err.Error(), "blocked_unenrolled") {
		t.Fatalf("expected blocked_unenrolled error, got %v", err)
	}

	if transport.readManifestCalls != 0 || transport.writeChunkCalls != 0 {
		t.Fatalf("expected no transport calls before preflight passes, got readManifest=%d writeChunk=%d", transport.readManifestCalls, transport.writeChunkCalls)
	}

	state, err := s.GetSyncState("cloud:proj-a")
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.ReasonCode == nil || *state.ReasonCode != "blocked_unenrolled" {
		t.Fatalf("expected reason_code blocked_unenrolled, got %v", state.ReasonCode)
	}
}

func TestCloudSyncPreflightRequiresExplicitProjectScope(t *testing.T) {
	s := newTestStore(t)
	sy := NewCloudWithTransport(s, newFakeCloudTransport(), "")

	if _, err := sy.Import(); err == nil || !strings.Contains(err.Error(), "explicit --project") {
		t.Fatalf("expected explicit project scope error, got %v", err)
	}
}

func TestCloudSyncEnrolledExportImportAndIdempotentPull(t *testing.T) {
	srcStore := newTestStore(t)
	seedStoreForSync(t, srcStore)
	if err := srcStore.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll src project: %v", err)
	}

	transport := newFakeCloudTransport()
	exporter := NewCloudWithTransport(srcStore, transport, "proj-a")

	exportResult, err := exporter.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("cloud export: %v", err)
	}
	if exportResult.IsEmpty {
		t.Fatal("expected non-empty cloud export for enrolled project")
	}

	dstStore := newTestStore(t)
	if err := dstStore.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll dst project: %v", err)
	}
	importer := NewCloudWithTransport(dstStore, transport, "proj-a")

	importResult, err := importer.Import()
	if err != nil {
		t.Fatalf("cloud import: %v", err)
	}
	if importResult.ChunksImported != 1 {
		t.Fatalf("expected one imported chunk, got %+v", importResult)
	}

	importAgain, err := importer.Import()
	if err != nil {
		t.Fatalf("second cloud import: %v", err)
	}
	if importAgain.ChunksImported != 0 || importAgain.ChunksSkipped != 1 {
		t.Fatalf("expected idempotent second import, got %+v", importAgain)
	}
}

func TestCloudExportUsesMutationJournalForUpdatesAndDeletes(t *testing.T) {
	s := newTestStore(t)
	transport := newFakeCloudTransport()
	sy := NewCloudWithTransport(s, transport, "proj-a")

	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("sess-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-a",
		Type:      "decision",
		Title:     "initial",
		Content:   "v1",
		Project:   "proj-a",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	first, err := sy.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	if first.IsEmpty {
		t.Fatal("expected first export to create initial chunk")
	}

	updatedTitle := "updated"
	if _, err := s.UpdateObservation(obsID, store.UpdateObservationParams{Title: &updatedTitle}); err != nil {
		t.Fatalf("update observation: %v", err)
	}
	if err := s.DeleteObservation(obsID, false); err != nil {
		t.Fatalf("delete observation: %v", err)
	}

	result, err := sy.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("second export with mutation journal: %v", err)
	}
	if result.IsEmpty {
		t.Fatal("expected mutation-backed export to include updates/deletes")
	}

	payload, ok := transport.chunks[result.ChunkID]
	if !ok {
		t.Fatalf("expected chunk payload for id %s", result.ChunkID)
	}
	var chunk ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatalf("decode chunk payload: %v", err)
	}
	if len(chunk.Observations) != 1 {
		t.Fatalf("expected one mutated observation in chunk, got %d", len(chunk.Observations))
	}
	if chunk.Observations[0].DeletedAt == nil {
		t.Fatalf("expected deleted_at tombstone in exported observation, got %+v", chunk.Observations[0])
	}

	pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending mutations: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected mutation journal to be acked after cloud export, got %+v", pending)
	}
}

func TestCloudExportWritesMutationOnlyChunkForHardDeletes(t *testing.T) {
	s := newTestStore(t)
	transport := newFakeCloudTransport()
	sy := NewCloudWithTransport(s, transport, "proj-a")

	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("sess-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-a",
		Type:      "decision",
		Title:     "initial",
		Content:   "v1",
		Project:   "proj-a",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	first, err := sy.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	if first.IsEmpty {
		t.Fatal("expected first export to create initial chunk")
	}

	if err := s.DeleteObservation(obsID, true); err != nil {
		t.Fatalf("hard delete observation: %v", err)
	}

	second, err := sy.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if second.IsEmpty {
		t.Fatal("expected mutation-only cloud export to write a chunk")
	}
	if second.MutationsExported == 0 {
		t.Fatalf("expected mutation-backed export counter to be non-zero, got %+v", second)
	}
	if second.SessionsExported != 0 || second.ObservationsExported != 0 || second.PromptsExported != 0 {
		t.Fatalf("expected mutation-only export to keep entity snapshot counters at zero, got %+v", second)
	}

	payload, ok := transport.chunks[second.ChunkID]
	if !ok {
		t.Fatalf("expected chunk payload for id %s", second.ChunkID)
	}
	var chunk ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatalf("decode chunk payload: %v", err)
	}
	if len(chunk.Observations) != 0 {
		t.Fatalf("expected mutation-only chunk without observations, got %d", len(chunk.Observations))
	}
	if len(chunk.Mutations) == 0 {
		t.Fatalf("expected mutation-only chunk to include mutation journal payload")
	}

	pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending mutations: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected pending mutations to be acked only after chunk write, got %+v", pending)
	}
}

func TestCloudExportKnownChunkReconcileFailureDoesNotAckMutations(t *testing.T) {
	resetSyncTestHooks(t)
	s := newTestStore(t)
	transport := newFakeCloudTransport()
	sy := NewCloudWithTransport(s, transport, "proj-a")

	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("sess-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-a",
		Type:      "decision",
		Title:     "initial",
		Content:   "v1",
		Project:   "proj-a",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	first, err := sy.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	if first.IsEmpty {
		t.Fatal("expected first export to create initial chunk")
	}

	updatedTitle := "updated"
	if _, err := s.UpdateObservation(obsID, store.UpdateObservationParams{Title: &updatedTitle}); err != nil {
		t.Fatalf("update observation: %v", err)
	}

	projectData, err := s.ExportProject("proj-a")
	if err != nil {
		t.Fatalf("export project data: %v", err)
	}
	chunk, seqs, err := sy.filterByPendingMutations(projectData, "proj-a")
	if err != nil {
		t.Fatalf("filter by pending mutations: %v", err)
	}
	if len(seqs) == 0 {
		t.Fatal("expected pending mutation seqs")
	}

	chunkJSON, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	chunkJSON, err = chunkcodec.CanonicalizeForProject(chunkJSON, "proj-a")
	if err != nil {
		t.Fatalf("canonicalize chunk: %v", err)
	}
	knownChunkID := chunkcodec.ChunkID(chunkJSON)
	transport.manifest.Chunks = append(transport.manifest.Chunks, ChunkEntry{ID: knownChunkID, CreatedBy: "remote", CreatedAt: time.Now().UTC().Format(time.RFC3339)})

	ackCalls := 0
	storeAckMutationSeq = func(storeRef *store.Store, targetKey string, seqs []int64) error {
		ackCalls++
		return storeRef.AckSyncMutationSeqs(targetKey, seqs)
	}
	storeRecordSynced = func(_ *store.Store, targetKey, chunkID string) error {
		if chunkID == knownChunkID {
			return errors.New("forced record failure")
		}
		return s.RecordSyncedChunkForTarget(targetKey, chunkID)
	}

	_, err = sy.Export("alice", "proj-a")
	if err == nil || !strings.Contains(err.Error(), "reconcile synced chunk") {
		t.Fatalf("expected reconcile synced chunk error, got %v", err)
	}
	if ackCalls != 0 {
		t.Fatalf("expected mutation ack to be skipped when reconcile fails, got %d calls", ackCalls)
	}

	pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending mutations: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("expected pending mutation journal to remain after reconcile failure")
	}
}

func TestCloudExportHardDeleteWithEmptyEntityProjectUsesSessionProjectScope(t *testing.T) {
	s := newTestStore(t)
	transport := newFakeCloudTransport()
	sy := NewCloudWithTransport(s, transport, "proj-a")

	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("sess-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "sess-a",
		Type:      "decision",
		Title:     "initial",
		Content:   "v1",
		Project:   "",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	first, err := sy.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	if first.IsEmpty {
		t.Fatal("expected first export to create initial chunk")
	}

	if err := s.DeleteObservation(obsID, true); err != nil {
		t.Fatalf("hard delete observation: %v", err)
	}

	second, err := sy.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if second.IsEmpty {
		t.Fatal("expected hard delete mutation export even when entity project is empty")
	}

	payload, ok := transport.chunks[second.ChunkID]
	if !ok {
		t.Fatalf("expected chunk payload for id %s", second.ChunkID)
	}
	var chunk ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		t.Fatalf("decode chunk payload: %v", err)
	}
	if len(chunk.Mutations) == 0 {
		t.Fatalf("expected mutation-only chunk to include delete mutation")
	}
}

func TestCloudImportAppliesMutationReconciliationForUpdatesAndDeletes(t *testing.T) {
	src := newTestStore(t)
	transport := newFakeCloudTransport()
	exporter := NewCloudWithTransport(src, transport, "proj-a")

	if err := src.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll source project: %v", err)
	}
	if err := src.CreateSession("sess-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := src.AddObservation(store.AddObservationParams{
		SessionID: "sess-a",
		Type:      "decision",
		Title:     "v1",
		Content:   "original",
		Project:   "proj-a",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	promptID, err := src.AddPrompt(store.AddPromptParams{SessionID: "sess-a", Content: "to-delete", Project: "proj-a"})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	first, err := exporter.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("first cloud export: %v", err)
	}
	if first.IsEmpty {
		t.Fatal("expected first cloud export to write initial snapshot")
	}

	updatedTitle := "v2"
	if _, err := src.UpdateObservation(obsID, store.UpdateObservationParams{Title: &updatedTitle}); err != nil {
		t.Fatalf("update observation: %v", err)
	}
	if err := src.DeletePrompt(promptID); err != nil {
		t.Fatalf("delete prompt: %v", err)
	}

	second, err := exporter.Export("alice", "proj-a")
	if err != nil {
		t.Fatalf("second cloud export: %v", err)
	}
	if second.IsEmpty {
		t.Fatal("expected second cloud export to include follow-up mutations")
	}

	dst := newTestStore(t)
	if err := dst.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll destination project: %v", err)
	}
	importer := NewCloudWithTransport(dst, transport, "proj-a")

	if _, err := importer.Import(); err != nil {
		t.Fatalf("cloud import: %v", err)
	}

	// Resolve by content/title since sync id is generated internally.
	found, err := dst.Search("v2", store.SearchOptions{Project: "proj-a", Limit: 5})
	if err != nil {
		t.Fatalf("search updated observation: %v", err)
	}
	if len(found) == 0 || found[0].Title != "v2" {
		t.Fatalf("expected updated observation title after pull reconciliation, got %+v", found)
	}

	prompts, err := dst.RecentPrompts("proj-a", 10)
	if err != nil {
		t.Fatalf("recent prompts: %v", err)
	}
	for _, p := range prompts {
		if p.Content == "to-delete" {
			t.Fatalf("expected deleted prompt to remain deleted after cloud pull, prompts=%+v", prompts)
		}
	}
}

func TestCloudImportChunkApplyIsAtomicOnFailure(t *testing.T) {
	dst := newTestStore(t)
	if err := dst.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll destination project: %v", err)
	}

	transport := newFakeCloudTransport()
	chunkID := "chunk-atomic"
	transport.manifest = &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: chunkID, CreatedAt: time.Now().UTC().Format(time.RFC3339)}}}

	badChunk := ChunkData{Mutations: []store.SyncMutation{
		{
			Entity:    store.SyncEntitySession,
			EntityKey: "remote-sess",
			Op:        store.SyncOpUpsert,
			Payload:   `{"id":"remote-sess","project":"proj-a","directory":"/remote"}`,
		},
		{
			Entity:    store.SyncEntityObservation,
			EntityKey: "obs-bad",
			Op:        store.SyncOpUpsert,
			Payload:   `{"sync_id":"obs-bad","session_id":"missing-session","type":"note","title":"bad","content":"fails fk","project":"proj-a","scope":"project"}`,
		},
	}}
	badPayload, err := json.Marshal(badChunk)
	if err != nil {
		t.Fatalf("marshal bad chunk: %v", err)
	}
	transport.chunks[chunkID] = badPayload

	importer := NewCloudWithTransport(dst, transport, "proj-a")
	if _, err := importer.Import(); err == nil {
		t.Fatal("expected cloud import failure for invalid mutation chunk")
	}

	if _, err := dst.GetSession("remote-sess"); err == nil {
		t.Fatal("expected remote session mutation to be rolled back after failed chunk import")
	}
	synced, err := dst.GetSyncedChunks()
	if err != nil {
		t.Fatalf("get synced chunks: %v", err)
	}
	if synced[chunkID] {
		t.Fatalf("failed chunk %q must not be marked synced", chunkID)
	}
}

func TestCloudImportReordersChunksToEstablishSessionsBeforeDependents(t *testing.T) {
	dst := newTestStore(t)
	if err := dst.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll destination project: %v", err)
	}

	transport := newFakeCloudTransport()
	obsFirstID := "chunk-observation-first"
	sessionSecondID := "chunk-session-second"
	transport.manifest = &Manifest{Version: 1, Chunks: []ChunkEntry{
		{ID: obsFirstID, CreatedAt: "2026-04-10T10:00:00Z"},
		{ID: sessionSecondID, CreatedAt: "2026-04-10T11:00:00Z"},
	}}

	obsChunk := ChunkData{Mutations: []store.SyncMutation{
		{
			Entity:    store.SyncEntityObservation,
			EntityKey: "obs-needs-session",
			Op:        store.SyncOpUpsert,
			Payload:   `{"sync_id":"obs-needs-session","session_id":"sess-bootstrap","type":"note","title":"boot","content":"depends on session","project":"proj-a","scope":"project"}`,
		},
	}}
	obsPayload, err := json.Marshal(obsChunk)
	if err != nil {
		t.Fatalf("marshal observation-first chunk: %v", err)
	}
	transport.chunks[obsFirstID] = obsPayload

	sessionChunk := ChunkData{Mutations: []store.SyncMutation{
		{
			Entity:    store.SyncEntitySession,
			EntityKey: "sess-bootstrap",
			Op:        store.SyncOpUpsert,
			Payload:   `{"id":"sess-bootstrap","project":"proj-a","directory":"/tmp/proj-a"}`,
		},
	}}
	sessionPayload, err := json.Marshal(sessionChunk)
	if err != nil {
		t.Fatalf("marshal session chunk: %v", err)
	}
	transport.chunks[sessionSecondID] = sessionPayload

	importer := NewCloudWithTransport(dst, transport, "proj-a")
	result, err := importer.Import()
	if err != nil {
		t.Fatalf("cloud import should succeed after dependency-safe ordering: %v", err)
	}
	if result.ChunksImported != 2 {
		t.Fatalf("expected both chunks imported, got %+v", result)
	}

	if _, err := dst.GetSession("sess-bootstrap"); err != nil {
		t.Fatalf("expected session imported before dependent observation apply: %v", err)
	}
	results, err := dst.Search("depends on session", store.SearchOptions{Project: "proj-a", Limit: 5})
	if err != nil {
		t.Fatalf("search imported observation: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected dependent observation to import successfully")
	}
}

func TestCloudImportReordersMutationsWithinChunkToAvoidFKFailures(t *testing.T) {
	dst := newTestStore(t)
	if err := dst.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll destination project: %v", err)
	}

	transport := newFakeCloudTransport()
	chunkID := "chunk-mixed-order"
	transport.manifest = &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: chunkID, CreatedAt: "2026-04-10T10:00:00Z"}}}

	mixedChunk := ChunkData{Mutations: []store.SyncMutation{
		{
			Entity:    store.SyncEntityObservation,
			EntityKey: "obs-mixed",
			Op:        store.SyncOpUpsert,
			Payload:   `{"sync_id":"obs-mixed","session_id":"sess-mixed","type":"note","title":"mixed","content":"chunk needs reorder","project":"proj-a","scope":"project"}`,
		},
		{
			Entity:    store.SyncEntityPrompt,
			EntityKey: "prompt-mixed",
			Op:        store.SyncOpUpsert,
			Payload:   `{"sync_id":"prompt-mixed","session_id":"sess-mixed","content":"prompt depends on session","project":"proj-a"}`,
		},
		{
			Entity:    store.SyncEntitySession,
			EntityKey: "sess-mixed",
			Op:        store.SyncOpUpsert,
			Payload:   `{"id":"sess-mixed","project":"proj-a","directory":"/tmp/proj-a"}`,
		},
	}}
	chunkPayload, err := json.Marshal(mixedChunk)
	if err != nil {
		t.Fatalf("marshal mixed chunk: %v", err)
	}
	transport.chunks[chunkID] = chunkPayload

	importer := NewCloudWithTransport(dst, transport, "proj-a")
	result, err := importer.Import()
	if err != nil {
		t.Fatalf("cloud import should reorder mixed chunk mutations safely: %v", err)
	}
	if result.ChunksImported != 1 {
		t.Fatalf("expected one imported chunk, got %+v", result)
	}

	if _, err := dst.GetSession("sess-mixed"); err != nil {
		t.Fatalf("expected session to be imported: %v", err)
	}
	results, err := dst.Search("chunk needs reorder", store.SearchOptions{Project: "proj-a", Limit: 5})
	if err != nil {
		t.Fatalf("search imported observation: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected dependent observation to import successfully")
	}
}

func TestCloudImportMixedChunkAppliesDirectArrayDependenciesBeforeMutations(t *testing.T) {
	dst := newTestStore(t)
	if err := dst.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll destination project: %v", err)
	}

	transport := newFakeCloudTransport()
	chunkID := "chunk-mixed-direct-and-mutations"
	transport.manifest = &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: chunkID, CreatedAt: "2026-04-10T10:00:00Z"}}}

	mixedChunk := ChunkData{
		Sessions: []store.Session{{
			ID:        "sess-direct",
			Project:   "proj-a",
			Directory: "/tmp/proj-a",
			StartedAt: "2026-04-10 09:59:00",
		}},
		Mutations: []store.SyncMutation{{
			Entity:    store.SyncEntityObservation,
			EntityKey: "obs-direct-dep",
			Op:        store.SyncOpUpsert,
			Payload:   `{"sync_id":"obs-direct-dep","session_id":"sess-direct","type":"note","title":"mixed","content":"depends on direct session","project":"proj-a","scope":"project"}`,
		}},
	}
	chunkPayload, err := json.Marshal(mixedChunk)
	if err != nil {
		t.Fatalf("marshal mixed chunk: %v", err)
	}
	transport.chunks[chunkID] = chunkPayload

	importer := NewCloudWithTransport(dst, transport, "proj-a")
	if _, err := importer.Import(); err != nil {
		t.Fatalf("cloud import should apply direct-array dependencies before mutation replay: %v", err)
	}

	if _, err := dst.GetSession("sess-direct"); err != nil {
		t.Fatalf("expected direct-array session to be imported: %v", err)
	}
	results, err := dst.Search("depends on direct session", store.SearchOptions{Project: "proj-a", Limit: 5})
	if err != nil {
		t.Fatalf("search imported observation: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected observation mutation that depends on direct-array session to import successfully")
	}
}

func TestBuildImportMutationsSkipsClosureOnlyDirectSessionsWhenChunkHasExplicitMutations(t *testing.T) {
	chunk := ChunkData{
		Sessions: []store.Session{
			{ID: "sess-needed", Project: "proj-a", Directory: "/tmp/proj-a"},
			{ID: "sess-closure-only", Project: "proj-b", Directory: "/tmp/proj-b"},
		},
		Mutations: []store.SyncMutation{{
			Entity:    store.SyncEntityObservation,
			EntityKey: "obs-needs-session",
			Op:        store.SyncOpUpsert,
			Payload:   `{"sync_id":"obs-needs-session","session_id":"sess-needed","type":"note","title":"needed","content":"depends on direct session","project":"proj-a","scope":"project"}`,
		}},
	}

	mutations := buildImportMutations(chunk)

	seenNeededSession := false
	seenClosureOnlySession := false
	for _, mutation := range mutations {
		if mutation.Entity != store.SyncEntitySession || mutation.Op != store.SyncOpUpsert {
			continue
		}
		switch mutation.EntityKey {
		case "sess-needed":
			seenNeededSession = true
		case "sess-closure-only":
			seenClosureOnlySession = true
		}
	}

	if !seenNeededSession {
		t.Fatalf("expected direct session required by explicit mutation to be synthesized")
	}
	if seenClosureOnlySession {
		t.Fatalf("closure-only direct session must not be synthesized when explicit mutations exist")
	}
}

func TestBuildImportMutationsPreservesSessionsNeededByRetainedSynthesizedEntities(t *testing.T) {
	project := "proj-a"
	chunk := ChunkData{
		Sessions: []store.Session{{ID: "sess-direct", Project: project, Directory: "/tmp/proj-a"}},
		Observations: []store.Observation{{
			SyncID:    "obs-direct",
			SessionID: "sess-direct",
			Type:      "note",
			Title:     "direct",
			Content:   "direct-array observation",
			Project:   &project,
			Scope:     "project",
		}},
		Mutations: []store.SyncMutation{{
			Entity:    store.SyncEntityObservation,
			EntityKey: "obs-explicit",
			Op:        store.SyncOpUpsert,
			Payload:   `{"sync_id":"obs-explicit","session_id":"sess-explicit","type":"note","title":"explicit","content":"keeps explicit path","project":"proj-a","scope":"project"}`,
		}},
	}

	mutations := buildImportMutations(chunk)

	seenDirectSession := false
	seenDirectObservation := false
	for _, mutation := range mutations {
		switch {
		case mutation.Entity == store.SyncEntitySession && mutation.Op == store.SyncOpUpsert && mutation.EntityKey == "sess-direct":
			seenDirectSession = true
		case mutation.Entity == store.SyncEntityObservation && mutation.Op == store.SyncOpUpsert && mutation.EntityKey == "obs-direct":
			seenDirectObservation = true
		}
	}

	if !seenDirectObservation {
		t.Fatalf("expected retained synthesized observation from direct arrays")
	}
	if !seenDirectSession {
		t.Fatalf("expected synthesized session required by retained synthesized observation")
	}
}

func TestEstimateMutationImportResultDeduplicatesEffectiveMutations(t *testing.T) {
	chunk := ChunkData{Mutations: []store.SyncMutation{
		{Entity: store.SyncEntityObservation, EntityKey: "obs-a", Op: store.SyncOpUpsert},
		{Entity: store.SyncEntityObservation, EntityKey: "obs-a", Op: store.SyncOpUpsert},
		{Entity: store.SyncEntityPrompt, EntityKey: "prompt-a", Op: store.SyncOpUpsert},
		{Entity: store.SyncEntityPrompt, EntityKey: "prompt-a", Op: store.SyncOpDelete},
		{Entity: store.SyncEntitySession, EntityKey: "sess-a", Op: store.SyncOpUpsert},
		{Entity: store.SyncEntitySession, EntityKey: "sess-a", Op: store.SyncOpUpsert},
	}}

	res := estimateMutationImportResult(chunk)
	if res.SessionsImported != 1 {
		t.Fatalf("expected deduped session count=1, got %d", res.SessionsImported)
	}
	if res.ObservationsImported != 1 {
		t.Fatalf("expected deduped observation count=1, got %d", res.ObservationsImported)
	}
	if res.PromptsImported != 0 {
		t.Fatalf("expected prompt final delete to count as 0 imports, got %d", res.PromptsImported)
	}
}

func TestFilterByPendingMutationsPaginatesBeforeProjectFiltering(t *testing.T) {
	resetSyncTestHooks(t)

	s := newTestStore(t)
	sy := NewCloudWithTransport(s, newFakeCloudTransport(), "proj-a")

	projA := "proj-a"
	data := &store.ExportData{
		Sessions: []store.Session{{ID: "sess-a", Project: "proj-a"}},
		Observations: []store.Observation{{
			ID:        1,
			SyncID:    "obs-a",
			SessionID: "sess-a",
			Project:   &projA,
		}},
	}

	storeListMutationsAfterSeq = func(_ *store.Store, _ string, afterSeq int64, _ int) ([]store.SyncMutation, error) {
		switch afterSeq {
		case 0:
			batch := make([]store.SyncMutation, 0, 5000)
			for seq := int64(1); seq <= 5000; seq++ {
				batch = append(batch, store.SyncMutation{
					Seq:       seq,
					Entity:    store.SyncEntityObservation,
					EntityKey: fmt.Sprintf("obs-other-%d", seq),
					Op:        store.SyncOpUpsert,
					Project:   "proj-b",
				})
			}
			return batch, nil
		case 5000:
			return []store.SyncMutation{{
				Seq:       5001,
				Entity:    store.SyncEntityObservation,
				EntityKey: "obs-a",
				Op:        store.SyncOpUpsert,
				Project:   "proj-a",
			}}, nil
		default:
			return nil, nil
		}
	}

	chunk, seqs, err := sy.filterByPendingMutations(data, "proj-a")
	if err != nil {
		t.Fatalf("filter by pending mutations: %v", err)
	}
	if len(seqs) != 1 || seqs[0] != 5001 {
		t.Fatalf("expected project mutation from second page to be selected, got seqs=%v", seqs)
	}
	if len(chunk.Observations) != 1 || chunk.Observations[0].SyncID != "obs-a" {
		t.Fatalf("expected paginated project observation to be included, got %+v", chunk.Observations)
	}
}

func TestExportDoesNotReconcileUnsyncedChunksByCreatedByOnly(t *testing.T) {
	s := newTestStore(t)
	transport := newFakeCloudTransport()
	transport.manifest = &Manifest{
		Version: 1,
		Chunks: []ChunkEntry{{
			ID:        "foreign-like",
			CreatedBy: "alice",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}},
	}

	sy := NewWithTransport(s, transport)
	res, err := sy.Export("alice", "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !res.IsEmpty {
		t.Fatalf("expected empty export result, got %+v", res)
	}

	synced, err := s.GetSyncedChunks()
	if err != nil {
		t.Fatalf("get synced chunks: %v", err)
	}
	if synced["foreign-like"] {
		t.Fatal("chunk ownership must not be inferred from CreatedBy alone")
	}
}

func TestCloudImportTreatsMissingManifestChunkAsError(t *testing.T) {
	s := newTestStore(t)
	transport := newFakeCloudTransport()
	transport.manifest = &Manifest{Version: 1, Chunks: []ChunkEntry{{ID: "missing", CreatedAt: time.Now().UTC().Format(time.RFC3339)}}}
	sy := NewCloudWithTransport(s, transport, "proj-a")

	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Missing chunk referenced by manifest must fail loudly in cloud mode.
	if _, err := sy.Import(); err == nil || !strings.Contains(err.Error(), "manifest references missing remote chunk") {
		t.Fatalf("expected manifest missing chunk failure, got %v", err)
	}

	// now force a non-not-found read failure and ensure it propagates loudly
	transport.readChunkErr = errors.New("transport offline")
	if _, err := sy.Import(); err == nil || !strings.Contains(err.Error(), "read chunk") {
		t.Fatalf("expected read chunk failure to propagate, got %v", err)
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

	projectOnlyNormalized := filterByProject(data, " PROJ-A ")
	if len(projectOnlyNormalized.Sessions) != 1 || projectOnlyNormalized.Sessions[0].ID != "s1" {
		t.Fatalf("expected normalized project filter to match session s1, got %+v", projectOnlyNormalized.Sessions)
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

func TestFilterByProjectEntityLevel(t *testing.T) {
	projA := "proj-a"

	data := &store.ExportData{
		Version:    "0.1.0",
		ExportedAt: "2025-01-01 00:00:00",
		Sessions: []store.Session{
			{ID: "s-match", Project: "proj-a", StartedAt: "2025-01-01 10:00:00"},
			{ID: "s-empty", Project: "", StartedAt: "2025-01-01 11:00:00"},
			{ID: "s-other", Project: "proj-b", StartedAt: "2025-01-01 12:00:00"},
			{ID: "s-orphan", Project: "proj-c", StartedAt: "2025-01-01 13:00:00"},
		},
		Observations: []store.Observation{
			// obs in matching session — included via session
			{ID: 1, SessionID: "s-match", CreatedAt: "2025-01-01 10:00:00"},
			// obs with own project but session has empty project — included via entity project
			{ID: 2, SessionID: "s-empty", Project: &projA, CreatedAt: "2025-01-01 11:00:00"},
			// obs with own project but session has different project — included via entity project
			{ID: 3, SessionID: "s-other", Project: &projA, CreatedAt: "2025-01-01 12:00:00"},
			// obs with nil project in non-matching session — excluded
			{ID: 4, SessionID: "s-other", Project: nil, CreatedAt: "2025-01-01 12:30:00"},
		},
		Prompts: []store.Prompt{
			// prompt in matching session — included via session
			{ID: 1, SessionID: "s-match", CreatedAt: "2025-01-01 10:00:00"},
			// prompt with own project but session has empty project — included via entity project
			{ID: 2, SessionID: "s-empty", Project: "proj-a", CreatedAt: "2025-01-01 11:00:00"},
			// prompt with wrong project in non-matching session — excluded
			{ID: 3, SessionID: "s-other", Project: "proj-b", CreatedAt: "2025-01-01 12:00:00"},
		},
	}

	result := filterByProject(data, "proj-a")

	// Observations: IDs 1, 2, 3 should be included
	if len(result.Observations) != 3 {
		t.Fatalf("expected 3 observations, got %d: %+v", len(result.Observations), result.Observations)
	}
	obsIDs := map[int64]bool{}
	for _, o := range result.Observations {
		obsIDs[o.ID] = true
	}
	for _, id := range []int64{1, 2, 3} {
		if !obsIDs[id] {
			t.Errorf("expected observation %d to be included", id)
		}
	}
	if obsIDs[4] {
		t.Error("observation 4 (nil project, non-matching session) should be excluded")
	}

	// Prompts: IDs 1, 2 should be included
	if len(result.Prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d: %+v", len(result.Prompts), result.Prompts)
	}
	promptIDs := map[int64]bool{}
	for _, p := range result.Prompts {
		promptIDs[p.ID] = true
	}
	if !promptIDs[1] || !promptIDs[2] {
		t.Error("expected prompts 1 and 2 to be included")
	}
	if promptIDs[3] {
		t.Error("prompt 3 (wrong project, non-matching session) should be excluded")
	}

	// Sessions: s-match (direct), s-empty (referenced by obs 2), s-other (referenced by obs 3)
	// s-orphan should be excluded (not referenced by any included entity)
	if len(result.Sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d: %+v", len(result.Sessions), result.Sessions)
	}
	sessIDs := map[string]bool{}
	for _, s := range result.Sessions {
		sessIDs[s.ID] = true
	}
	if !sessIDs["s-match"] || !sessIDs["s-empty"] || !sessIDs["s-other"] {
		t.Error("expected sessions s-match, s-empty, s-other to be included")
	}
	if sessIDs["s-orphan"] {
		t.Error("session s-orphan should be excluded (no referenced entities)")
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

	t.Run("truncated gzip propagates decompression error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "truncated.gz")
		if err := os.WriteFile(path, []byte{0x1f, 0x8b, 0x08, 0x00}, 0o644); err != nil {
			t.Fatalf("write truncated gzip: %v", err)
		}

		if _, err := readGzip(path); err == nil {
			t.Fatal("expected readGzip error for truncated gzip payload")
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

func TestChunkTrackingTargetKeyScopesBySyncTarget(t *testing.T) {
	local := &Syncer{cloudMode: false}
	if got := local.chunkTrackingTargetKey(""); got != store.LocalChunkTargetKey {
		t.Fatalf("expected local chunk target key %q, got %q", store.LocalChunkTargetKey, got)
	}

	cloud := &Syncer{cloudMode: true, project: "proj-a"}
	if got := cloud.chunkTrackingTargetKey(""); got != "cloud:proj-a" {
		t.Fatalf("expected cloud project target key, got %q", got)
	}
	if got := cloud.chunkTrackingTargetKey("PROJ-B"); got != "cloud:proj-b" {
		t.Fatalf("expected explicit normalized cloud project target key, got %q", got)
	}
}
