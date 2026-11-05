package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// ChunkDeleteEvidence is a complete summary of a validated immutable chunk.
// Empty DeletedIDs means a complete empty result, not missing evidence. Scope
// identifies the remote source/project; ManifestVersion binds admission rules.
type ChunkDeleteEvidence struct {
	Scope           string
	ManifestVersion int
	DeletedIDs      []string
}

// ApplyPulledChunkWithDeleteEvidence commits derived evidence together with the
// chunk's mutations and tracking marker. Callers must validate the original
// payload and supply its complete delete set, even when filtering relations.
func (s *Store) ApplyPulledChunkWithDeleteEvidence(targetKey, chunkID string, mutations []SyncMutation, evidence ChunkDeleteEvidence) error {
	return s.applyPulledChunkForDomain(targetKey, chunkID, mutations, true, &evidence)
}

// RecordChunkDeleteEvidence backfills a successfully validated known chunk.
// The FK prevents recording evidence for a chunk that has not been tracked.
func (s *Store) RecordChunkDeleteEvidence(targetKey, chunkID string, evidence ChunkDeleteEvidence) error {
	return s.withTx(func(tx *sql.Tx) error {
		return s.recordChunkDeleteEvidenceTx(tx, normalizeChunkTargetKey(targetKey), strings.TrimSpace(chunkID), evidence)
	})
}

func (s *Store) recordChunkDeleteEvidenceTx(tx *sql.Tx, targetKey, chunkID string, evidence ChunkDeleteEvidence) error {
	if strings.TrimSpace(evidence.Scope) == "" {
		return fmt.Errorf("chunk delete evidence scope is required")
	}
	ids := evidence.DeletedIDs
	if ids == nil {
		ids = []string{}
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = s.execHook(tx, `INSERT INTO sync_chunk_delete_evidence
		(target_key, scope, manifest_version, chunk_id, deleted_ids) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(target_key, scope, manifest_version, chunk_id) DO UPDATE SET deleted_ids = excluded.deleted_ids`,
		targetKey, evidence.Scope, evidence.ManifestVersion, chunkID, string(raw))
	return err
}

// GetChunkDeleteEvidence loads a source's summaries in one query. Callers must
// intersect the result with the current manifest and known-chunk set; a summary
// is evidence about a chunk, never a global assertion about an endpoint.
func (s *Store) GetChunkDeleteEvidence(targetKey, scope string, manifestVersion int) (map[string][]string, error) {
	// Return one aggregate row through the generation-fenced driver. Returning
	// one driver row per historical chunk would repeat filesystem identity
	// checks for every summary and replace network overhead with local I/O.
	// Keep each stored value as a JSON string so a damaged derived value can
	// still be treated independently as a miss below.
	var raw string
	err := s.db.QueryRow(`SELECT json_group_object(e.chunk_id, e.deleted_ids) FROM sync_chunk_delete_evidence e
		JOIN sync_chunks c ON c.target_key = e.target_key AND c.chunk_id = e.chunk_id
		WHERE e.target_key = ? AND e.scope = ? AND e.manifest_version = ?`, normalizeChunkTargetKey(targetKey), scope, manifestVersion).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var summaries map[string]string
	if err := json.Unmarshal([]byte(raw), &summaries); err != nil {
		return nil, err
	}
	result := make(map[string][]string, len(summaries))
	for id, encoded := range summaries {
		var ids []string
		if err := json.Unmarshal([]byte(encoded), &ids); err != nil || ids == nil {
			// A damaged derived row is a miss, not a complete-empty answer.
			continue
		}
		result[id] = ids
	}
	return result, nil
}
