package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrChunkNotFound = errors.New("sync: chunk not found")

// Transport defines how chunks are read and written during sync.
// This is the abstraction that allows the same Syncer to work with
// both local filesystem (.engram/ directory) and remote cloud server.
type Transport interface {
	// ReadManifest returns the manifest (chunk index).
	// Returns an empty manifest if none exists yet.
	ReadManifest() (*Manifest, error)

	// WriteManifest persists the manifest.
	WriteManifest(m *Manifest) error

	// WriteChunk writes a compressed chunk to the transport.
	// chunkID is the content-addressed ID (8 hex chars).
	// data is the raw JSON bytes (FileTransport gzips them; remote sends as-is).
	// entry contains metadata about the chunk.
	WriteChunk(chunkID string, data []byte, entry ChunkEntry) error

	// ReadChunk reads a compressed chunk from the transport.
	// Returns the raw bytes (gzipped for FileTransport, JSON for remote).
	ReadChunk(chunkID string) ([]byte, error)
}

// ─── FileTransport ──────────────────────────────────────────────────────────

// FileTransport reads/writes chunks to the local filesystem.
// This encapsulates all filesystem operations that were previously
// inline in the Syncer methods.
type FileTransport struct {
	syncDir string // Path to .engram/ directory
}

// NewFileTransport creates a FileTransport rooted at the given sync directory.
func NewFileTransport(syncDir string) *FileTransport {
	return &FileTransport{syncDir: syncDir}
}

// ReadManifest reads the manifest.json from the sync directory.
// Returns an empty manifest (Version=1) if the file does not exist.
func (ft *FileTransport) ReadManifest() (*Manifest, error) {
	path := filepath.Join(ft.syncDir, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if info, statErr := os.Stat(ft.syncDir); statErr == nil && !info.IsDir() {
				return nil, fmt.Errorf("read manifest: sync path %q is not a directory", ft.syncDir)
			}
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

// WriteManifest writes the manifest to manifest.json in the sync directory.
func (ft *FileTransport) WriteManifest(m *Manifest) error {
	path := filepath.Join(ft.syncDir, "manifest.json")
	data, err := jsonMarshalManifest(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// WriteChunk writes gzipped chunk data to the chunks/ subdirectory.
func (ft *FileTransport) WriteChunk(chunkID string, data []byte, _ ChunkEntry) error {
	chunksDir := filepath.Join(ft.syncDir, "chunks")
	if err := os.MkdirAll(chunksDir, 0755); err != nil {
		return fmt.Errorf("create chunks dir: %w", err)
	}

	chunkPath := filepath.Join(chunksDir, chunkID+".jsonl.gz")
	return writeGzip(chunkPath, data)
}

// ReadChunk reads gzipped chunk data from the chunks/ subdirectory.
func (ft *FileTransport) ReadChunk(chunkID string) ([]byte, error) {
	chunksDir := filepath.Join(ft.syncDir, "chunks")
	chunkPath := filepath.Join(chunksDir, chunkID+".jsonl.gz")
	data, err := readGzip(chunkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrChunkNotFound
		}
		return nil, err
	}
	return data, nil
}
