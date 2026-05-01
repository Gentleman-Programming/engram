// Package sync implements git-friendly memory synchronization for Engram.
//
// Instead of a single large JSON file, memories are stored as compressed
// JSONL chunks with a manifest index. This design:
//
//   - Avoids git merge conflicts (each sync creates a NEW chunk, never modifies old ones)
//   - Keeps files small (each chunk is gzipped JSONL)
//   - Tracks what's been imported via chunk IDs (no duplicates)
//   - Works for teams (multiple devs create independent chunks)
//
// Directory structure:
//
//	.engram/
//	├── manifest.json          ← index of all chunks (small, mergeable)
//	├── chunks/
//	│   ├── a3f8c1d2.jsonl.gz ← chunk 1 (compressed)
//	│   ├── b7d2e4f1.jsonl.gz ← chunk 2
//	│   └── ...
//	└── engram.db              ← local working DB (gitignored)
package sync

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/internal/store"
)

var (
	jsonMarshalChunk    = json.Marshal
	jsonMarshalManifest = json.MarshalIndent
	osCreateFile        = os.Create
	gzipWriterFactory   = func(f *os.File) gzipWriter { return gzip.NewWriter(f) }
	osHostname          = os.Hostname
	storeGetSynced      = func(s *store.Store, targetKey string) (map[string]bool, error) {
		return s.GetSyncedChunksForTarget(targetKey)
	}
	storeExportData            = func(s *store.Store) (*store.ExportData, error) { return s.Export() }
	storeExportDataForProject  = func(s *store.Store, project string) (*store.ExportData, error) { return s.ExportProject(project) }
	storeListMutationsAfterSeq = func(s *store.Store, targetKey string, afterSeq int64, limit int) ([]store.SyncMutation, error) {
		return s.ListPendingSyncMutationsAfterSeq(targetKey, afterSeq, limit)
	}
	storeAckMutationSeq = func(s *store.Store, targetKey string, seqs []int64) error {
		return s.AckSyncMutationSeqs(targetKey, seqs)
	}
	storeApplyPulledChunk = func(s *store.Store, targetKey, chunkID string, mutations []store.SyncMutation) error {
		return s.ApplyPulledChunk(targetKey, chunkID, mutations)
	}
	storeRecordSynced = func(s *store.Store, targetKey, chunkID string) error {
		return s.RecordSyncedChunkForTarget(targetKey, chunkID)
	}
)

type gzipWriter interface {
	Write(p []byte) (n int, err error)
	Close() error
}

// ─── Manifest ────────────────────────────────────────────────────────────────

// Manifest is the index file that lists all chunks.
// This is the only file git needs to diff/merge — it's small and append-only.
type Manifest struct {
	Version int          `json:"version"`
	Chunks  []ChunkEntry `json:"chunks"`
}

// ChunkEntry describes a single chunk in the manifest.
type ChunkEntry struct {
	ID        string `json:"id"`         // SHA-256 hash prefix (8 chars) of content
	CreatedBy string `json:"created_by"` // Username or machine identifier
	CreatedAt string `json:"created_at"` // ISO timestamp
	Sessions  int    `json:"sessions"`   // Number of sessions in chunk
	Memories  int    `json:"memories"`   // Number of observations in chunk
	Prompts   int    `json:"prompts"`    // Number of prompts in chunk
}

// ChunkData is the content of a single chunk file (JSONL entries).
type ChunkData struct {
	Sessions     []store.Session      `json:"sessions"`
	Observations []store.Observation  `json:"observations"`
	Prompts      []store.Prompt       `json:"prompts"`
	Mutations    []store.SyncMutation `json:"mutations,omitempty"`
}

// SyncResult is returned after a sync operation.
type SyncResult struct {
	ChunkID              string `json:"chunk_id,omitempty"`
	SessionsExported     int    `json:"sessions_exported"`
	ObservationsExported int    `json:"observations_exported"`
	PromptsExported      int    `json:"prompts_exported"`
	MutationsExported    int    `json:"mutations_exported"`
	IsEmpty              bool   `json:"is_empty"` // true if nothing new to sync
}

// ImportResult is returned after importing chunks.
type ImportResult struct {
	ChunksImported       int `json:"chunks_imported"`
	ChunksSkipped        int `json:"chunks_skipped"` // Already imported
	SessionsImported     int `json:"sessions_imported"`
	ObservationsImported int `json:"observations_imported"`
	PromptsImported      int `json:"prompts_imported"`
}

// ─── Syncer ──────────────────────────────────────────────────────────────────

// Syncer handles exporting and importing memory chunks.
type Syncer struct {
	store     *store.Store
	syncDir   string    // Path to .engram/ in the project repo (kept for backward compat)
	transport Transport // Pluggable I/O backend (filesystem, remote, etc.)
	cloudMode bool
	project   string
}

type UpgradeBootstrapOptions struct {
	Project   string
	CreatedBy string
}

type UpgradeBootstrapResult struct {
	Project string
	Stage   string
	Resumed bool
	NoOp    bool
}

type UpgradeLifecycleHooks interface {
	StopForUpgrade(project string) error
	ResumeAfterUpgrade(project string) error
}

type UpgradeRollbackOptions struct {
	Project string
	Hooks   UpgradeLifecycleHooks
}

func RollbackProject(s *store.Store, opts UpgradeRollbackOptions) (*store.CloudUpgradeState, error) {
	if s == nil {
		return nil, fmt.Errorf("cloud upgrade rollback requires store")
	}
	project, _ := store.NormalizeProject(opts.Project)
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("cloud upgrade rollback requires project")
	}

	canRollback, err := s.CanRollbackCloudUpgrade(project)
	if err != nil {
		return nil, fmt.Errorf("cloud upgrade rollback boundary check: %w", err)
	}
	if !canRollback {
		return nil, fmt.Errorf("rollback is unavailable post-bootstrap; use explicit disconnect/unenroll flows")
	}

	if opts.Hooks != nil {
		if err := opts.Hooks.StopForUpgrade(project); err != nil {
			return nil, fmt.Errorf("cloud upgrade rollback stop autosync: %w", err)
		}
	}

	rolledBackState, rollbackErr := s.RollbackCloudUpgrade(project)
	if rollbackErr != nil {
		if opts.Hooks != nil {
			_ = opts.Hooks.ResumeAfterUpgrade(project)
		}
		return nil, rollbackErr
	}

	if opts.Hooks != nil {
		if err := opts.Hooks.ResumeAfterUpgrade(project); err != nil {
			return nil, fmt.Errorf("cloud upgrade rollback resume autosync: %w", err)
		}
	}

	return &rolledBackState, nil
}

// New creates a Syncer with a FileTransport rooted at syncDir.
// This preserves the original constructor signature for backward compatibility.
func New(s *store.Store, syncDir string) *Syncer {
	return &Syncer{
		store:     s,
		syncDir:   syncDir,
		transport: NewFileTransport(syncDir),
	}
}

// NewLocal is an alias for New — creates a Syncer backed by the local filesystem.
// Preferred in call sites where the name makes the intent clearer.
func NewLocal(s *store.Store, syncDir string) *Syncer {
	return New(s, syncDir)
}

// NewWithTransport creates a Syncer with a custom Transport implementation.
// This is used for remote (cloud) sync where chunks travel over HTTP.
func NewWithTransport(s *store.Store, transport Transport) *Syncer {
	return &Syncer{
		store:     s,
		transport: transport,
	}
}

// NewCloudWithTransport creates a cloud-mode Syncer that enforces enrollment
// preflight checks before any transport/network operations.
func NewCloudWithTransport(s *store.Store, transport Transport, project string) *Syncer {
	project, _ = store.NormalizeProject(project)
	return &Syncer{
		store:     s,
		transport: transport,
		cloudMode: true,
		project:   strings.TrimSpace(project),
	}
}

func BootstrapProject(s *store.Store, transport Transport, opts UpgradeBootstrapOptions) (*UpgradeBootstrapResult, error) {
	if s == nil {
		return nil, fmt.Errorf("cloud upgrade bootstrap requires store")
	}
	project, _ := store.NormalizeProject(opts.Project)
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("cloud upgrade bootstrap requires project")
	}
	createdBy := strings.TrimSpace(opts.CreatedBy)
	if createdBy == "" {
		createdBy = "upgrade-bootstrap"
	}

	state, err := s.GetCloudUpgradeState(project)
	if err != nil {
		return nil, fmt.Errorf("read cloud upgrade checkpoint: %w", err)
	}
	currentStage := store.UpgradeStagePlanned
	if state != nil {
		currentStage = state.Stage
	}
	if currentStage == store.UpgradeStageBootstrapVerified {
		return &UpgradeBootstrapResult{Project: project, Stage: currentStage, Resumed: true, NoOp: true}, nil
	}

	resumed := currentStage != store.UpgradeStagePlanned

	if upgradeStageOrder(currentStage) < upgradeStageOrder(store.UpgradeStageBootstrapEnrolled) {
		if err := s.EnrollProject(project); err != nil {
			return nil, fmt.Errorf("bootstrap enroll project: %w", err)
		}
		if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{
			Project:     project,
			Stage:       store.UpgradeStageBootstrapEnrolled,
			RepairClass: store.UpgradeRepairClassRepairable,
		}); err != nil {
			return nil, fmt.Errorf("persist bootstrap checkpoint enrolled: %w", err)
		}
		currentStage = store.UpgradeStageBootstrapEnrolled
	}

	sy := NewCloudWithTransport(s, transport, project)
	enrolled, err := s.IsProjectEnrolled(project)
	if err != nil {
		return nil, fmt.Errorf("bootstrap enrollment verify: %w", err)
	}
	if !enrolled {
		if err := s.EnrollProject(project); err != nil {
			return nil, fmt.Errorf("bootstrap enrollment repair: %w", err)
		}
	}
	if upgradeStageOrder(currentStage) < upgradeStageOrder(store.UpgradeStageBootstrapPushed) {
		if _, err := sy.Export(createdBy, project); err != nil {
			return nil, fmt.Errorf("bootstrap first push: %w", err)
		}
		if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{
			Project:     project,
			Stage:       store.UpgradeStageBootstrapPushed,
			RepairClass: store.UpgradeRepairClassRepairable,
		}); err != nil {
			return nil, fmt.Errorf("persist bootstrap checkpoint pushed: %w", err)
		}
		currentStage = store.UpgradeStageBootstrapPushed
	}

	if upgradeStageOrder(currentStage) < upgradeStageOrder(store.UpgradeStageBootstrapVerified) {
		if _, err := sy.Import(); err != nil {
			return nil, fmt.Errorf("bootstrap verification pull: %w", err)
		}
		if _, _, _, err := sy.Status(); err != nil {
			return nil, fmt.Errorf("bootstrap verification status: %w", err)
		}
		if err := s.SaveCloudUpgradeState(store.CloudUpgradeState{
			Project:     project,
			Stage:       store.UpgradeStageBootstrapVerified,
			RepairClass: store.UpgradeRepairClassReady,
		}); err != nil {
			return nil, fmt.Errorf("persist bootstrap checkpoint verified: %w", err)
		}
		currentStage = store.UpgradeStageBootstrapVerified
	}

	return &UpgradeBootstrapResult{
		Project: project,
		Stage:   currentStage,
		Resumed: resumed,
		NoOp:    false,
	}, nil
}

func upgradeStageOrder(stage string) int {
	switch strings.TrimSpace(stage) {
	case store.UpgradeStageBootstrapEnrolled:
		return 1
	case store.UpgradeStageBootstrapPushed:
		return 2
	case store.UpgradeStageBootstrapVerified:
		return 3
	default:
		return 0
	}
}

// ─── Export (DB → chunks) ────────────────────────────────────────────────────

// Export creates a new chunk with memories not yet in any chunk.
// It reads the manifest to know what's already exported, then creates
// a new chunk with only the new data.
func (sy *Syncer) Export(createdBy string, project string) (*SyncResult, error) {
	project, _ = store.NormalizeProject(project)
	if err := sy.ensureCloudPreflight(project); err != nil {
		return nil, err
	}

	// Pre-flight: ensure the sync directory structure exists for filesystem transports.
	// This preserves the original error ordering where "create chunks dir" was the
	// first check in Export, before manifest reading.
	if sy.syncDir != "" {
		chunksDir := filepath.Join(sy.syncDir, "chunks")
		if err := os.MkdirAll(chunksDir, 0755); err != nil {
			return nil, fmt.Errorf("create chunks dir: %w", err)
		}
	}

	// Read current manifest (or create empty one)
	manifest, err := sy.readManifest()
	if err != nil {
		return nil, err
	}

	chunkTargetKey := sy.chunkTrackingTargetKey(project)

	// Get chunk IDs already recorded locally.
	locallySyncedChunks, err := storeGetSynced(sy.store, chunkTargetKey)
	if err != nil {
		return nil, fmt.Errorf("get synced chunks: %w", err)
	}
	knownChunks := make(map[string]bool, len(locallySyncedChunks))
	for chunkID, ok := range locallySyncedChunks {
		if ok {
			knownChunks[chunkID] = true
		}
	}

	// Also consider chunks in the manifest as known
	for _, c := range manifest.Chunks {
		knownChunks[c.ID] = true
	}

	// Export data from DB (project-scoped in cloud mode/project syncs to avoid global dumps)
	var data *store.ExportData
	if strings.TrimSpace(project) != "" {
		data, err = storeExportDataForProject(sy.store, project)
	} else {
		data, err = storeExportData(sy.store)
	}
	if err != nil {
		return nil, fmt.Errorf("export data: %w", err)
	}

	chunk := &ChunkData{}
	mutationSeqs := []int64{}
	if sy.cloudMode {
		chunk, mutationSeqs, err = sy.filterByPendingMutations(data, project)
		if err != nil {
			return nil, fmt.Errorf("build mutation-backed export: %w", err)
		}
	} else {
		// Get the timestamp of the last chunk to filter "new" data
		lastChunkTime := sy.lastChunkTime(manifest)

		// Filter to only new data (created after last chunk)
		chunk = sy.filterNewData(data, lastChunkTime)
	}

	// Nothing new to export
	if len(chunk.Sessions) == 0 && len(chunk.Observations) == 0 && len(chunk.Prompts) == 0 && len(chunk.Mutations) == 0 {
		if sy.cloudMode && len(mutationSeqs) > 0 {
			if err := storeAckMutationSeq(sy.store, store.DefaultSyncTargetKey, mutationSeqs); err != nil {
				return nil, fmt.Errorf("ack synced mutations: %w", err)
			}
		}
		return &SyncResult{IsEmpty: true}, nil
	}

	// Serialize and compress the chunk
	chunkJSON, err := jsonMarshalChunk(chunk)
	if err != nil {
		return nil, fmt.Errorf("marshal chunk: %w", err)
	}
	if sy.cloudMode {
		projectName := strings.TrimSpace(project)
		if projectName == "" {
			projectName = sy.project
		}
		projectName, _ = store.NormalizeProject(projectName)
		chunkJSON, err = chunkcodec.CanonicalizeForProject(chunkJSON, projectName)
		if err != nil {
			return nil, fmt.Errorf("canonicalize cloud chunk: %w", err)
		}
	}

	// Generate chunk ID from content hash
	chunkID := chunkcodec.ChunkID(chunkJSON)

	// Check if this exact chunk already exists
	if _, exists := knownChunks[chunkID]; exists {
		if !locallySyncedChunks[chunkID] {
			if err := storeRecordSynced(sy.store, chunkTargetKey, chunkID); err != nil {
				return nil, fmt.Errorf("reconcile synced chunk %s: %w", chunkID, err)
			}
		}
		if sy.cloudMode && len(mutationSeqs) > 0 {
			if err := storeAckMutationSeq(sy.store, store.DefaultSyncTargetKey, mutationSeqs); err != nil {
				return nil, fmt.Errorf("ack synced mutations: %w", err)
			}
		}
		return &SyncResult{IsEmpty: true}, nil
	}

	// Build manifest entry
	entry := ChunkEntry{
		ID:        chunkID,
		CreatedBy: createdBy,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Sessions:  len(chunk.Sessions),
		Memories:  len(chunk.Observations),
		Prompts:   len(chunk.Prompts),
	}

	// Write chunk via transport
	if err := sy.transport.WriteChunk(chunkID, chunkJSON, entry); err != nil {
		return nil, fmt.Errorf("write chunk: %w", err)
	}

	// Update manifest
	manifest.Chunks = append(manifest.Chunks, entry)

	if err := sy.writeManifest(manifest); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	// Record this chunk as synced in the local DB
	if err := storeRecordSynced(sy.store, chunkTargetKey, chunkID); err != nil {
		return nil, fmt.Errorf("record synced chunk: %w", err)
	}
	if sy.cloudMode && len(mutationSeqs) > 0 {
		if err := storeAckMutationSeq(sy.store, store.DefaultSyncTargetKey, mutationSeqs); err != nil {
			return nil, fmt.Errorf("ack synced mutations: %w", err)
		}
	}

	return &SyncResult{
		ChunkID:              chunkID,
		SessionsExported:     len(chunk.Sessions),
		ObservationsExported: len(chunk.Observations),
		PromptsExported:      len(chunk.Prompts),
		MutationsExported:    len(chunk.Mutations),
	}, nil
}

// ─── Import (chunks → DB) ────────────────────────────────────────────────────

// Import reads the manifest and imports any chunks not yet in the local DB.
func (sy *Syncer) Import() (*ImportResult, error) {
	if err := sy.ensureCloudPreflight(""); err != nil {
		return nil, err
	}

	manifest, err := sy.readManifest()
	if err != nil {
		return nil, err
	}

	if len(manifest.Chunks) == 0 {
		return &ImportResult{}, nil
	}

	// Get chunks we've already imported
	knownChunks, err := storeGetSynced(sy.store, sy.chunkTrackingTargetKey(""))
	if err != nil {
		return nil, fmt.Errorf("get synced chunks: %w", err)
	}

	entries := manifest.Chunks
	if sy.cloudMode {
		return sy.importEntriesDependencySafe(entries, knownChunks, importModeCloud)
	}
	return sy.importEntriesDependencySafe(entries, knownChunks, importModeLocal)
}

type importMode string

const (
	importModeLocal importMode = "local"
	importModeCloud importMode = "cloud"
)

func (sy *Syncer) importEntriesDependencySafe(entries []ChunkEntry, knownChunks map[string]bool, mode importMode) (*ImportResult, error) {
	result := &ImportResult{}
	pendingEntries := make([]ChunkEntry, 0, len(entries))
	for _, entry := range entries {
		// Skip already-imported chunks
		if knownChunks[entry.ID] {
			result.ChunksSkipped++
			continue
		}
		pendingEntries = append(pendingEntries, entry)
	}

	if len(pendingEntries) == 0 {
		return result, nil
	}

	lastErrors := map[string]error{}
	for pass := 1; len(pendingEntries) > 0; pass++ {
		progress := false
		nextPending := make([]ChunkEntry, 0, len(pendingEntries))

		for _, entry := range pendingEntries {
			// Read the chunk via transport
			chunkJSON, err := sy.transport.ReadChunk(entry.ID)
			if err != nil {
				if errors.Is(err, ErrChunkNotFound) {
					if mode == importModeCloud {
						return nil, fmt.Errorf("read chunk %s: manifest references missing remote chunk", entry.ID)
					}
					result.ChunksSkipped++
					continue
				}
				return nil, fmt.Errorf("read chunk %s: %w", entry.ID, err)
			}

			var chunk ChunkData
			if err := json.Unmarshal(chunkJSON, &chunk); err != nil {
				return nil, fmt.Errorf("parse chunk %s: %w", entry.ID, err)
			}

			if err := sy.importMutationChunk(entry.ID, chunk); err != nil {
				lastErrors[entry.ID] = importDependencyError(chunk, err)
				nextPending = append(nextPending, entry)
				continue
			}

			importResult := estimateMutationImportResult(chunk)
			knownChunks[entry.ID] = true
			delete(lastErrors, entry.ID)

			result.ChunksImported++
			result.SessionsImported += importResult.SessionsImported
			result.ObservationsImported += importResult.ObservationsImported
			result.PromptsImported += importResult.PromptsImported
			progress = true
		}

		if !progress {
			if len(nextPending) == 0 {
				return result, nil
			}
			stalled := nextPending[0]
			return nil, fmt.Errorf("dependency-safe %s import stalled after %d pass(es); chunk %s: %w", mode, pass, stalled.ID, lastErrors[stalled.ID])
		}

		pendingEntries = nextPending
	}

	return result, nil
}

func (sy *Syncer) importMutationChunk(chunkID string, chunk ChunkData) error {
	mutations := buildImportMutations(chunk)
	mutations = orderMutationsForApply(mutations)
	return storeApplyPulledChunk(sy.store, sy.chunkTrackingTargetKey(""), chunkID, mutations)
}

func importDependencyError(chunk ChunkData, err error) error {
	mutations := buildImportMutations(chunk)
	referenced := referencedSessionIDsFromNonSessionUpserts(mutations)
	if len(referenced) == 0 {
		return err
	}
	sessions := make([]string, 0, len(referenced))
	for sessionID := range referenced {
		sessions = append(sessions, sessionID)
	}
	sort.Strings(sessions)
	return fmt.Errorf("%w; pending session dependencies: %s", err, strings.Join(sessions, ", "))
}

func buildImportMutations(chunk ChunkData) []store.SyncMutation {
	if len(chunk.Mutations) == 0 {
		return synthesizeMutationsFromChunk(chunk)
	}

	explicit := chunk.Mutations
	requiredSessionIDs := referencedSessionIDsFromMutations(explicit)
	synthesized := synthesizeMutationsFromChunk(chunk)
	if len(synthesized) == 0 {
		return explicit
	}

	explicitKeys := make(map[string]struct{}, len(explicit))
	for _, mutation := range explicit {
		explicitKeys[mutationIdentityKey(mutation)] = struct{}{}
	}

	retainedSynthesized := make([]store.SyncMutation, 0, len(synthesized))
	for _, mutation := range synthesized {
		if _, exists := explicitKeys[mutationIdentityKey(mutation)]; exists {
			continue
		}
		retainedSynthesized = append(retainedSynthesized, mutation)
	}
	for sessionID := range referencedSessionIDsFromNonSessionUpserts(retainedSynthesized) {
		requiredSessionIDs[sessionID] = struct{}{}
	}

	merged := make([]store.SyncMutation, 0, len(synthesized)+len(explicit))
	for _, mutation := range retainedSynthesized {
		if mutation.Entity == store.SyncEntitySession && mutation.Op == store.SyncOpUpsert {
			if _, required := requiredSessionIDs[strings.TrimSpace(mutation.EntityKey)]; !required {
				continue
			}
		}
		merged = append(merged, mutation)
	}
	merged = append(merged, explicit...)
	return merged
}

func referencedSessionIDsFromMutations(mutations []store.SyncMutation) map[string]struct{} {
	required := make(map[string]struct{})
	for _, mutation := range mutations {
		if mutation.Op != store.SyncOpUpsert {
			continue
		}
		switch mutation.Entity {
		case store.SyncEntitySession:
			sessionID := strings.TrimSpace(mutation.EntityKey)
			if sessionID != "" {
				required[sessionID] = struct{}{}
			}
		case store.SyncEntityObservation, store.SyncEntityPrompt:
			var payload struct {
				SessionID string `json:"session_id"`
			}
			if err := decodeSyncPayloadForProject([]byte(mutation.Payload), &payload); err != nil {
				continue
			}
			sessionID := strings.TrimSpace(payload.SessionID)
			if sessionID != "" {
				required[sessionID] = struct{}{}
			}
		}
	}
	return required
}

func referencedSessionIDsFromNonSessionUpserts(mutations []store.SyncMutation) map[string]struct{} {
	required := make(map[string]struct{})
	for _, mutation := range mutations {
		if mutation.Op != store.SyncOpUpsert {
			continue
		}
		switch mutation.Entity {
		case store.SyncEntityObservation, store.SyncEntityPrompt:
			var payload struct {
				SessionID string `json:"session_id"`
			}
			if err := decodeSyncPayloadForProject([]byte(mutation.Payload), &payload); err != nil {
				continue
			}
			sessionID := strings.TrimSpace(payload.SessionID)
			if sessionID != "" {
				required[sessionID] = struct{}{}
			}
		}
	}
	return required
}

func mutationIdentityKey(mutation store.SyncMutation) string {
	return fmt.Sprintf("%s:%s", mutation.Entity, strings.TrimSpace(mutation.EntityKey))
}

func (sy *Syncer) chunkTrackingTargetKey(project string) string {
	if !sy.cloudMode {
		return store.LocalChunkTargetKey
	}
	projectName := strings.TrimSpace(project)
	if projectName == "" {
		projectName = sy.project
	}
	projectName, _ = store.NormalizeProject(projectName)
	return cloudTargetKey(projectName)
}

func orderMutationsForApply(mutations []store.SyncMutation) []store.SyncMutation {
	if len(mutations) <= 1 {
		return mutations
	}
	sessionUpserts := make([]store.SyncMutation, 0, len(mutations))
	otherUpserts := make([]store.SyncMutation, 0, len(mutations))
	otherDeletes := make([]store.SyncMutation, 0, len(mutations))
	sessionDeletes := make([]store.SyncMutation, 0, len(mutations))

	for _, mutation := range mutations {
		switch {
		case mutation.Entity == store.SyncEntitySession && mutation.Op == store.SyncOpUpsert:
			sessionUpserts = append(sessionUpserts, mutation)
		case mutation.Entity == store.SyncEntitySession && mutation.Op == store.SyncOpDelete:
			sessionDeletes = append(sessionDeletes, mutation)
		case mutation.Op == store.SyncOpDelete:
			otherDeletes = append(otherDeletes, mutation)
		default:
			otherUpserts = append(otherUpserts, mutation)
		}
	}

	ordered := make([]store.SyncMutation, 0, len(mutations))
	ordered = append(ordered, sessionUpserts...)
	ordered = append(ordered, otherUpserts...)
	ordered = append(ordered, otherDeletes...)
	ordered = append(ordered, sessionDeletes...)
	return ordered
}

func synthesizeMutationsFromChunk(chunk ChunkData) []store.SyncMutation {
	mutations := make([]store.SyncMutation, 0, len(chunk.Sessions)+len(chunk.Observations)+len(chunk.Prompts))
	for _, session := range chunk.Sessions {
		payload, err := json.Marshal(map[string]any{
			"id":         session.ID,
			"project":    session.Project,
			"directory":  session.Directory,
			"started_at": session.StartedAt,
			"ended_at":   session.EndedAt,
			"summary":    session.Summary,
		})
		if err != nil {
			continue
		}
		mutations = append(mutations, store.SyncMutation{
			Entity:    store.SyncEntitySession,
			EntityKey: strings.TrimSpace(session.ID),
			Op:        store.SyncOpUpsert,
			Payload:   string(payload),
		})
	}
	for _, obs := range chunk.Observations {
		op := store.SyncOpUpsert
		if obs.DeletedAt != nil {
			op = store.SyncOpDelete
		}
		payload, err := json.Marshal(map[string]any{
			"sync_id":         obs.SyncID,
			"session_id":      obs.SessionID,
			"type":            obs.Type,
			"title":           obs.Title,
			"content":         obs.Content,
			"tool_name":       obs.ToolName,
			"project":         obs.Project,
			"scope":           obs.Scope,
			"topic_key":       obs.TopicKey,
			"revision_count":  obs.RevisionCount,
			"duplicate_count": obs.DuplicateCount,
			"last_seen_at":    obs.LastSeenAt,
			"created_at":      obs.CreatedAt,
			"updated_at":      obs.UpdatedAt,
			"deleted":         obs.DeletedAt != nil,
			"deleted_at":      obs.DeletedAt,
			"hard_delete":     false,
		})
		if err != nil {
			continue
		}
		mutations = append(mutations, store.SyncMutation{
			Entity:    store.SyncEntityObservation,
			EntityKey: strings.TrimSpace(obs.SyncID),
			Op:        op,
			Payload:   string(payload),
		})
	}
	for _, prompt := range chunk.Prompts {
		payload, err := json.Marshal(map[string]any{
			"sync_id":    prompt.SyncID,
			"session_id": prompt.SessionID,
			"content":    prompt.Content,
			"project":    prompt.Project,
			"created_at": prompt.CreatedAt,
		})
		if err != nil {
			continue
		}
		mutations = append(mutations, store.SyncMutation{
			Entity:    store.SyncEntityPrompt,
			EntityKey: strings.TrimSpace(prompt.SyncID),
			Op:        store.SyncOpUpsert,
			Payload:   string(payload),
		})
	}
	return mutations
}

func estimateMutationImportResult(chunk ChunkData) *store.ImportResult {
	mutations := effectiveMutationsForImport(chunk)
	res := &store.ImportResult{}
	for _, mutation := range mutations {
		if mutation.Op == store.SyncOpDelete {
			continue
		}
		switch mutation.Entity {
		case store.SyncEntitySession:
			res.SessionsImported++
		case store.SyncEntityObservation:
			res.ObservationsImported++
		case store.SyncEntityPrompt:
			res.PromptsImported++
		}
	}
	return res
}

func effectiveMutationsForImport(chunk ChunkData) []store.SyncMutation {
	mutations := buildImportMutations(chunk)
	if len(mutations) <= 1 {
		return mutations
	}

	lastByIdentity := make(map[string]int, len(mutations))
	for idx, mutation := range mutations {
		lastByIdentity[mutationIdentityKey(mutation)] = idx
	}

	effective := make([]store.SyncMutation, 0, len(lastByIdentity))
	for idx, mutation := range mutations {
		if lastByIdentity[mutationIdentityKey(mutation)] != idx {
			continue
		}
		effective = append(effective, mutation)
	}

	return effective
}

// Status returns information about what would be synced.
func (sy *Syncer) Status() (localChunks int, remoteChunks int, pendingImport int, err error) {
	manifest, err := sy.readManifest()
	if err != nil {
		return 0, 0, 0, err
	}

	known, err := storeGetSynced(sy.store, sy.chunkTrackingTargetKey(""))
	if err != nil {
		return 0, 0, 0, err
	}

	remoteChunks = len(manifest.Chunks)
	localChunks = len(known)

	for _, entry := range manifest.Chunks {
		if !known[entry.ID] {
			pendingImport++
		}
	}

	return localChunks, remoteChunks, pendingImport, nil
}

func (sy *Syncer) ensureCloudPreflight(project string) error {
	if !sy.cloudMode {
		return nil
	}

	if sy.store == nil {
		return fmt.Errorf("cloud sync blocked: store is required")
	}

	projectName := strings.TrimSpace(project)
	if projectName == "" {
		projectName = sy.project
	}
	projectName, _ = store.NormalizeProject(projectName)
	if projectName == "" {
		return fmt.Errorf("cloud sync requires an explicit --project scope; --all is not supported in cloud mode")
	}

	enrolled, err := sy.store.IsProjectEnrolled(projectName)
	if err != nil {
		return fmt.Errorf("cloud sync enrollment preflight: %w", err)
	}
	if enrolled {
		return nil
	}

	message := fmt.Sprintf("project %q is not enrolled for cloud sync", projectName)
	_ = sy.store.MarkSyncBlocked(cloudTargetKey(projectName), "blocked_unenrolled", message)
	return fmt.Errorf("cloud sync blocked_unenrolled: %s", message)
}

func cloudTargetKey(project string) string {
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return store.DefaultSyncTargetKey
	}
	return fmt.Sprintf("%s:%s", store.DefaultSyncTargetKey, project)
}

// ─── Manifest I/O ────────────────────────────────────────────────────────────

func (sy *Syncer) readManifest() (*Manifest, error) {
	return sy.transport.ReadManifest()
}

func (sy *Syncer) writeManifest(m *Manifest) error {
	return sy.transport.WriteManifest(m)
}

func (sy *Syncer) lastChunkTime(m *Manifest) string {
	if len(m.Chunks) == 0 {
		return ""
	}
	// Find the most recent chunk
	latest := m.Chunks[0].CreatedAt
	for _, c := range m.Chunks[1:] {
		if c.CreatedAt > latest {
			latest = c.CreatedAt
		}
	}
	return latest
}

// ─── Filtering ───────────────────────────────────────────────────────────────

// filterNewData returns only data created after the given timestamp.
// If lastChunkTime is empty, returns everything (first sync).
func (sy *Syncer) filterNewData(data *store.ExportData, lastChunkTime string) *ChunkData {
	chunk := &ChunkData{}

	if lastChunkTime == "" {
		// First sync — everything is new
		chunk.Sessions = data.Sessions
		chunk.Observations = data.Observations
		chunk.Prompts = data.Prompts
		return chunk
	}

	// Parse the last chunk time for comparison.
	// Normalize: DB times are "2006-01-02 15:04:05", manifest times are RFC3339.
	// We compare as strings since both sort lexicographically.
	cutoff := normalizeTime(lastChunkTime)

	for _, s := range data.Sessions {
		if normalizeTime(s.StartedAt) > cutoff {
			chunk.Sessions = append(chunk.Sessions, s)
		}
	}

	for _, o := range data.Observations {
		if normalizeTime(o.CreatedAt) > cutoff {
			chunk.Observations = append(chunk.Observations, o)
		}
	}

	for _, p := range data.Prompts {
		if normalizeTime(p.CreatedAt) > cutoff {
			chunk.Prompts = append(chunk.Prompts, p)
		}
	}

	return chunk
}

func filterByProject(data *store.ExportData, project string) *store.ExportData {
	targetProject, _ := store.NormalizeProject(project)
	result := &store.ExportData{
		Version:    data.Version,
		ExportedAt: data.ExportedAt,
	}

	// Step 1: index sessions that match by their own project
	sessionIDs := make(map[string]bool)
	for _, s := range data.Sessions {
		sessionProject, _ := store.NormalizeProject(s.Project)
		if sessionProject == targetProject {
			sessionIDs[s.ID] = true
		}
	}

	// Step 2: observations — match by own project OR by session
	referencedSessionIDs := make(map[string]bool)
	for _, o := range data.Observations {
		match := sessionIDs[o.SessionID]
		if !match && o.Project != nil {
			observationProject, _ := store.NormalizeProject(*o.Project)
			if observationProject == targetProject {
				match = true
			}
		}
		if match {
			result.Observations = append(result.Observations, o)
			referencedSessionIDs[o.SessionID] = true
		}
	}

	// Step 3: prompts — match by own project OR by session
	for _, p := range data.Prompts {
		match := sessionIDs[p.SessionID]
		if !match {
			promptProject, _ := store.NormalizeProject(p.Project)
			if promptProject == targetProject {
				match = true
			}
		}
		if match {
			result.Prompts = append(result.Prompts, p)
			referencedSessionIDs[p.SessionID] = true
		}
	}

	// Step 4: include sessions that matched directly or are referenced by included entities
	for _, s := range data.Sessions {
		if sessionIDs[s.ID] || referencedSessionIDs[s.ID] {
			result.Sessions = append(result.Sessions, s)
		}
	}

	return result
}

func (sy *Syncer) filterByPendingMutations(data *store.ExportData, project string) (*ChunkData, []int64, error) {
	project, _ = store.NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return &ChunkData{}, nil, nil
	}

	mutations, err := sy.listPendingMutationsForExport()
	if err != nil {
		return nil, nil, err
	}
	availableSessionIDs := make(map[string]struct{}, len(data.Sessions))
	sessionProjectByID := make(map[string]string, len(data.Sessions))
	availableObservationSyncIDs := make(map[string]struct{}, len(data.Observations))
	availablePromptSyncIDs := make(map[string]struct{}, len(data.Prompts))
	for _, session := range data.Sessions {
		availableSessionIDs[session.ID] = struct{}{}
		normalizedSessionProject, _ := store.NormalizeProject(session.Project)
		sessionProjectByID[session.ID] = strings.TrimSpace(normalizedSessionProject)
	}
	for _, observation := range data.Observations {
		availableObservationSyncIDs[observation.SyncID] = struct{}{}
	}
	for _, prompt := range data.Prompts {
		availablePromptSyncIDs[prompt.SyncID] = struct{}{}
	}

	sessionKeys := make(map[string]struct{})
	observationSyncIDs := make(map[string]struct{})
	promptSyncIDs := make(map[string]struct{})
	seqs := make([]int64, 0, len(mutations))
	selectedMutations := make([]store.SyncMutation, 0, len(mutations))

	for _, mutation := range mutations {
		mutationProject := resolveMutationProject(mutation, sessionProjectByID)
		if mutationProject != project {
			if mutationProject != "" {
				continue
			}
			switch mutation.Entity {
			case store.SyncEntitySession:
				if _, ok := availableSessionIDs[mutation.EntityKey]; !ok {
					continue
				}
			case store.SyncEntityObservation:
				if _, ok := availableObservationSyncIDs[mutation.EntityKey]; !ok {
					continue
				}
			case store.SyncEntityPrompt:
				if _, ok := availablePromptSyncIDs[mutation.EntityKey]; !ok {
					continue
				}
			default:
				continue
			}
		}
		seqs = append(seqs, mutation.Seq)
		selectedMutations = append(selectedMutations, mutation)
		switch mutation.Entity {
		case store.SyncEntitySession:
			sessionKeys[mutation.EntityKey] = struct{}{}
		case store.SyncEntityObservation:
			observationSyncIDs[mutation.EntityKey] = struct{}{}
		case store.SyncEntityPrompt:
			promptSyncIDs[mutation.EntityKey] = struct{}{}
		}
	}

	chunk := &ChunkData{}
	if len(seqs) == 0 {
		return chunk, nil, nil
	}

	referencedSessionIDs := make(map[string]struct{})
	for _, observation := range data.Observations {
		if _, ok := observationSyncIDs[observation.SyncID]; !ok {
			continue
		}
		chunk.Observations = append(chunk.Observations, observation)
		referencedSessionIDs[observation.SessionID] = struct{}{}
	}

	for _, prompt := range data.Prompts {
		if _, ok := promptSyncIDs[prompt.SyncID]; !ok {
			continue
		}
		chunk.Prompts = append(chunk.Prompts, prompt)
		referencedSessionIDs[prompt.SessionID] = struct{}{}
	}

	for _, session := range data.Sessions {
		if _, ok := sessionKeys[session.ID]; ok {
			chunk.Sessions = append(chunk.Sessions, session)
			continue
		}
		if _, ok := referencedSessionIDs[session.ID]; ok {
			chunk.Sessions = append(chunk.Sessions, session)
		}
	}
	chunk.Mutations = selectedMutations

	return chunk, seqs, nil
}

func (sy *Syncer) listPendingMutationsForExport() ([]store.SyncMutation, error) {
	const pageSize = 5000
	afterSeq := int64(0)
	mutations := make([]store.SyncMutation, 0, pageSize)

	for {
		batch, err := storeListMutationsAfterSeq(sy.store, store.DefaultSyncTargetKey, afterSeq, pageSize)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		mutations = append(mutations, batch...)
		lastSeq := batch[len(batch)-1].Seq
		if lastSeq <= afterSeq {
			return nil, fmt.Errorf("pending mutation pagination did not advance (after_seq=%d, last_seq=%d)", afterSeq, lastSeq)
		}
		afterSeq = lastSeq
		if len(batch) < pageSize {
			break
		}
	}

	return mutations, nil
}

func resolveMutationProject(mutation store.SyncMutation, sessionProjectByID map[string]string) string {
	mutationProject, _ := store.NormalizeProject(mutation.Project)
	mutationProject = strings.TrimSpace(mutationProject)
	if mutationProject != "" {
		return mutationProject
	}

	type payloadProject struct {
		Project   *string `json:"project"`
		SessionID string  `json:"session_id"`
	}
	var payload payloadProject
	if err := decodeSyncPayloadForProject([]byte(mutation.Payload), &payload); err != nil {
		return ""
	}
	if payload.Project != nil {
		if normalized, _ := store.NormalizeProject(strings.TrimSpace(*payload.Project)); strings.TrimSpace(normalized) != "" {
			return strings.TrimSpace(normalized)
		}
	}
	if normalized, _ := store.NormalizeProject(strings.TrimSpace(sessionProjectByID[strings.TrimSpace(payload.SessionID)])); strings.TrimSpace(normalized) != "" {
		return strings.TrimSpace(normalized)
	}
	return ""
}

func decodeSyncPayloadForProject(payload []byte, dest any) error {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return fmt.Errorf("empty payload")
	}
	if trimmed[0] != '"' {
		return json.Unmarshal([]byte(trimmed), dest)
	}
	var encoded string
	if err := json.Unmarshal([]byte(trimmed), &encoded); err != nil {
		return err
	}
	return json.Unmarshal([]byte(encoded), dest)
}

// normalizeTime converts various time formats to a comparable string.
func normalizeTime(t string) string {
	// Try RFC3339 first
	if parsed, err := time.Parse(time.RFC3339, t); err == nil {
		return parsed.UTC().Format("2006-01-02 15:04:05")
	}
	// Already in "2006-01-02 15:04:05" format
	return strings.TrimSpace(t)
}

// ─── Gzip I/O ────────────────────────────────────────────────────────────────

func writeGzip(path string, data []byte) error {
	f, err := osCreateFile(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzipWriterFactory(f)
	if _, err := gz.Write(data); err != nil {
		return err
	}
	return gz.Close()
}

func readGzip(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	data, err := io.ReadAll(gz)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// GetUsername returns the current username for chunk attribution.
func GetUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	hostname, _ := osHostname()
	if hostname != "" {
		return hostname
	}
	return "unknown"
}

// ManifestSummary returns a human-readable summary of the manifest.
func ManifestSummary(m *Manifest) string {
	if len(m.Chunks) == 0 {
		return "No chunks synced yet."
	}

	totalMemories := 0
	totalSessions := 0
	authors := make(map[string]int)

	for _, c := range m.Chunks {
		totalMemories += c.Memories
		totalSessions += c.Sessions
		authors[c.CreatedBy]++
	}

	// Sort authors for consistent output
	authorList := make([]string, 0, len(authors))
	for a := range authors {
		authorList = append(authorList, a)
	}
	sort.Strings(authorList)

	authorStrs := make([]string, 0, len(authorList))
	for _, a := range authorList {
		authorStrs = append(authorStrs, fmt.Sprintf("%s (%d chunks)", a, authors[a]))
	}

	return fmt.Sprintf(
		"%d chunks, %d memories, %d sessions — contributors: %s",
		len(m.Chunks), totalMemories, totalSessions,
		strings.Join(authorStrs, ", "),
	)
}
