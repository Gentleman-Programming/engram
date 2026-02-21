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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alanbuscaglia/engram/internal/store"
)

var (
	jsonMarshalChunk    = json.Marshal
	jsonMarshalManifest = json.MarshalIndent
	osCreateFile        = os.Create
	gzipWriterFactory   = func(f *os.File) gzipWriter { return gzip.NewWriter(f) }
	osHostname          = os.Hostname
	storeGetSynced      = func(s *store.Store) (map[string]bool, error) { return s.GetSyncedChunks() }
	storeExportData     = func(s *store.Store) (*store.ExportData, error) { return s.Export() }
	storeImportData     = func(s *store.Store, d *store.ExportData) (*store.ImportResult, error) { return s.Import(d) }
	storeRecordSynced   = func(s *store.Store, chunkID string) error { return s.RecordSyncedChunk(chunkID) }
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
	Sessions     []store.Session     `json:"sessions"`
	Observations []store.Observation `json:"observations"`
	Prompts      []store.Prompt      `json:"prompts"`
}

// SyncResult is returned after a sync operation.
type SyncResult struct {
	ChunkID              string `json:"chunk_id,omitempty"`
	SessionsExported     int    `json:"sessions_exported"`
	ObservationsExported int    `json:"observations_exported"`
	PromptsExported      int    `json:"prompts_exported"`
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
	store   *store.Store
	syncDir string // Path to .engram/ in the project repo
}

// New creates a Syncer. syncDir is the .engram/ directory in the project.
func New(s *store.Store, syncDir string) *Syncer {
	return &Syncer{store: s, syncDir: syncDir}
}

// ─── Export (DB → chunks) ────────────────────────────────────────────────────

// Export creates a new chunk with memories not yet in any chunk.
// It reads the manifest to know what's already exported, then creates
// a new chunk with only the new data.
func (sy *Syncer) Export(createdBy string, project string) (*SyncResult, error) {
	// Ensure directories exist
	chunksDir := filepath.Join(sy.syncDir, "chunks")
	if err := os.MkdirAll(chunksDir, 0755); err != nil {
		return nil, fmt.Errorf("create chunks dir: %w", err)
	}

	// Read current manifest (or create empty one)
	manifest, err := sy.readManifest()
	if err != nil {
		return nil, err
	}

	// Get known chunk IDs from the store's sync tracking
	knownChunks, err := storeGetSynced(sy.store)
	if err != nil {
		return nil, fmt.Errorf("get synced chunks: %w", err)
	}

	// Also consider chunks in the manifest as known
	for _, c := range manifest.Chunks {
		knownChunks[c.ID] = true
	}

	// Export all data from DB
	data, err := storeExportData(sy.store)
	if err != nil {
		return nil, fmt.Errorf("export data: %w", err)
	}

	// Filter by project if specified
	if project != "" {
		data = filterByProject(data, project)
	}

	// Get the timestamp of the last chunk to filter "new" data
	lastChunkTime := sy.lastChunkTime(manifest)

	// Filter to only new data (created after last chunk)
	chunk := sy.filterNewData(data, lastChunkTime)

	// Nothing new to export
	if len(chunk.Sessions) == 0 && len(chunk.Observations) == 0 && len(chunk.Prompts) == 0 {
		return &SyncResult{IsEmpty: true}, nil
	}

	// Serialize and compress the chunk
	chunkJSON, err := jsonMarshalChunk(chunk)
	if err != nil {
		return nil, fmt.Errorf("marshal chunk: %w", err)
	}

	// Generate chunk ID from content hash
	hash := sha256.Sum256(chunkJSON)
	chunkID := hex.EncodeToString(hash[:])[:8]

	// Check if this exact chunk already exists
	if _, exists := knownChunks[chunkID]; exists {
		return &SyncResult{IsEmpty: true}, nil
	}

	// Write compressed chunk
	chunkPath := filepath.Join(chunksDir, chunkID+".jsonl.gz")
	if err := writeGzip(chunkPath, chunkJSON); err != nil {
		return nil, fmt.Errorf("write chunk: %w", err)
	}

	// Update manifest
	entry := ChunkEntry{
		ID:        chunkID,
		CreatedBy: createdBy,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Sessions:  len(chunk.Sessions),
		Memories:  len(chunk.Observations),
		Prompts:   len(chunk.Prompts),
	}
	manifest.Chunks = append(manifest.Chunks, entry)

	if err := sy.writeManifest(manifest); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	// Record this chunk as synced in the local DB
	if err := storeRecordSynced(sy.store, chunkID); err != nil {
		return nil, fmt.Errorf("record synced chunk: %w", err)
	}

	return &SyncResult{
		ChunkID:              chunkID,
		SessionsExported:     len(chunk.Sessions),
		ObservationsExported: len(chunk.Observations),
		PromptsExported:      len(chunk.Prompts),
	}, nil
}

// ─── Import (chunks → DB) ────────────────────────────────────────────────────

// Import reads the manifest and imports any chunks not yet in the local DB.
func (sy *Syncer) Import() (*ImportResult, error) {
	manifest, err := sy.readManifest()
	if err != nil {
		return nil, err
	}

	if len(manifest.Chunks) == 0 {
		return &ImportResult{}, nil
	}

	// Get chunks we've already imported
	knownChunks, err := storeGetSynced(sy.store)
	if err != nil {
		return nil, fmt.Errorf("get synced chunks: %w", err)
	}

	result := &ImportResult{}
	chunksDir := filepath.Join(sy.syncDir, "chunks")

	for _, entry := range manifest.Chunks {
		// Skip already-imported chunks
		if knownChunks[entry.ID] {
			result.ChunksSkipped++
			continue
		}

		// Read and decompress the chunk
		chunkPath := filepath.Join(chunksDir, entry.ID+".jsonl.gz")
		chunkJSON, err := readGzip(chunkPath)
		if err != nil {
			// Chunk file missing — skip (maybe deleted or not yet pulled)
			result.ChunksSkipped++
			continue
		}

		var chunk ChunkData
		if err := json.Unmarshal(chunkJSON, &chunk); err != nil {
			return nil, fmt.Errorf("parse chunk %s: %w", entry.ID, err)
		}

		// Import into DB
		exportData := &store.ExportData{
			Version:      "0.1.0",
			ExportedAt:   entry.CreatedAt,
			Sessions:     chunk.Sessions,
			Observations: chunk.Observations,
			Prompts:      chunk.Prompts,
		}

		importResult, err := storeImportData(sy.store, exportData)
		if err != nil {
			return nil, fmt.Errorf("import chunk %s: %w", entry.ID, err)
		}

		// Record this chunk as imported
		if err := storeRecordSynced(sy.store, entry.ID); err != nil {
			return nil, fmt.Errorf("record chunk %s: %w", entry.ID, err)
		}

		result.ChunksImported++
		result.SessionsImported += importResult.SessionsImported
		result.ObservationsImported += importResult.ObservationsImported
		result.PromptsImported += importResult.PromptsImported
	}

	return result, nil
}

// Status returns information about what would be synced.
func (sy *Syncer) Status() (localChunks int, remoteChunks int, pendingImport int, err error) {
	manifest, err := sy.readManifest()
	if err != nil {
		return 0, 0, 0, err
	}

	known, err := storeGetSynced(sy.store)
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

// ─── Manifest I/O ────────────────────────────────────────────────────────────

func (sy *Syncer) readManifest() (*Manifest, error) {
	path := filepath.Join(sy.syncDir, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Manifest{Version: 1}, nil
		}
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

func (sy *Syncer) writeManifest(m *Manifest) error {
	path := filepath.Join(sy.syncDir, "manifest.json")
	data, err := jsonMarshalManifest(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return os.WriteFile(path, data, 0644)
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
	result := &store.ExportData{
		Version:    data.Version,
		ExportedAt: data.ExportedAt,
	}

	sessionIDs := make(map[string]bool)
	for _, s := range data.Sessions {
		if s.Project == project {
			result.Sessions = append(result.Sessions, s)
			sessionIDs[s.ID] = true
		}
	}
	for _, o := range data.Observations {
		if sessionIDs[o.SessionID] {
			result.Observations = append(result.Observations, o)
		}
	}
	for _, p := range data.Prompts {
		if sessionIDs[p.SessionID] {
			result.Prompts = append(result.Prompts, p)
		}
	}
	return result
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

	var buf strings.Builder
	data := make([]byte, 4096)
	for {
		n, err := gz.Read(data)
		if n > 0 {
			buf.Write(data[:n])
		}
		if err != nil {
			break
		}
	}
	return []byte(buf.String()), nil
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
