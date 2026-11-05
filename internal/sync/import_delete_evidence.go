package sync

import (
	"sort"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func (sy *Syncer) chunkEvidenceScope() string {
	if transport, ok := sy.transport.(ChunkEvidenceTransport); sy.cloudMode && ok {
		return strings.TrimSpace(transport.ChunkEvidenceScope())
	}
	return ""
}

func chunkDeleteEvidence(scope string, version int, chunk ChunkData) store.ChunkDeleteEvidence {
	deletes := make(map[string]struct{})
	addObservationEvidence(chunk, nil, deletes)
	ids := make([]string, 0, len(deletes))
	for id := range deletes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return store.ChunkDeleteEvidence{Scope: scope, ManifestVersion: version, DeletedIDs: ids}
}

func (sy *Syncer) importChunkWithEvidence(id string, chunk ChunkData, mode importMode, version int) error {
	if scope := sy.chunkEvidenceScope(); mode == importModeCloud && scope != "" {
		mutations := orderMutationsForApply(buildImportMutations(chunk))
		return sy.store.ApplyPulledChunkWithDeleteEvidence(sy.chunkTrackingTargetKey(""), id, mutations, chunkDeleteEvidence(scope, version, chunk))
	}
	return sy.importMutationChunk(id, chunk, mode == importModeCloud)
}
