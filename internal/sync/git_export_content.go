package sync

import (
	"encoding/json"
	"fmt"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// gitExportInputs contains the already-read local intent and manifest history.
// The builder does not access a Store, transport or clock.
type gitExportInputs struct {
	lastChunkTime                             string
	includePrompts                            bool
	historicalPrompts, historicalObservations map[string]struct{}
	observationUpdates                        map[string]string
	exportedRelations, exportedDeletes        map[string]struct{}
	reviewDates                               map[string]historicalReviewState
	exportedClears                            map[string]bool
	relationMutations, localDeletes           []store.SyncMutation
	events                                    []store.ReviewClearEvent
}

func buildGitExportChunk(data *store.ExportData, inputs gitExportInputs) (*ChunkData, error) {
	chunk := filterNewData(data, inputs.lastChunkTime)
	// Prompt identity history, including deletes, overrides the watermark.
	// Omitted prompts remain eligible later; shared or tombstoned identities do not.
	chunk.Prompts = nil
	if inputs.includePrompts {
		for _, prompt := range data.Prompts {
			if _, shared := inputs.historicalPrompts[prompt.SyncID]; !shared {
				chunk.Prompts = append(chunk.Prompts, prompt)
			}
		}
	}
	chunk.Observations = filterObservationsForExport(data.Observations, inputs.historicalObservations, inputs.lastChunkTime, inputs.observationUpdates)
	selected := make(map[string]bool)
	for _, observation := range chunk.Observations {
		selected[observation.SyncID] = true
	}
	for _, observation := range data.Observations {
		prior, authoritative := inputs.reviewDates[observation.SyncID]
		if observation.ReviewAfter != nil && authoritative && !prior.deleted && (prior.date == nil || *prior.date != *observation.ReviewAfter) && !selected[observation.SyncID] {
			chunk.Observations = append(chunk.Observations, observation)
		}
	}
	includeObservationParentSessions(chunk, data.Sessions)
	chunk.Mutations = append(filterRelationMutationsForExport(inputs.relationMutations, inputs.exportedRelations, inputs.lastChunkTime), filterUnexportedDeleteMutations(inputs.localDeletes, inputs.exportedDeletes)...)
	observations := make(map[string]store.Observation)
	for _, observation := range data.Observations {
		observations[observation.SyncID] = observation
	}
	for _, event := range inputs.events {
		receipt := fmt.Sprintf("%s:%d:%s", event.SyncID, event.ID, event.Key)
		observation, present := observations[event.SyncID]
		if !present || inputs.exportedClears[receipt] {
			continue
		}
		chunk.ReviewClears = append(chunk.ReviewClears, receipt)
		mutations := synthesizeMutationsFromChunk(ChunkData{Observations: []store.Observation{observation}})
		var fields map[string]any
		if err := json.Unmarshal([]byte(mutations[0].Payload), &fields); err != nil {
			return nil, fmt.Errorf("encode review reset: %w", err)
		}
		fields["review_after"] = nil
		payload, err := json.Marshal(fields)
		if err != nil {
			return nil, fmt.Errorf("encode review reset: %w", err)
		}
		mutations[0].Payload = string(payload)
		chunk.Mutations = append(chunk.Mutations, mutations[0])
	}
	return chunk, nil
}
