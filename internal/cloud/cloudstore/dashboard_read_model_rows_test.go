package cloudstore

import "sort"

func buildDashboardReadModel(chunks []dashboardChunkRow) (dashboardReadModel, error) {
	return buildDashboardReadModelFromRows(chunks, nil)
}

// buildDashboardReadModelFromRows folds in-memory rows. It sorts both inputs into
// the canonical fold order; the production SQL path streams rows already in that
// order (see dashboardChunkStreamQuery and dashboardMutationStreamQuery). It is
// test-only so production cannot regress to materializing every decoded chunk.
func buildDashboardReadModelFromRows(chunks []dashboardChunkRow, mutationRows []dashboardMutationRow) (dashboardReadModel, error) {
	ordered := append([]dashboardChunkRow(nil), chunks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].createdAt.Equal(ordered[j].createdAt) {
			return ordered[i].createdAt.Before(ordered[j].createdAt)
		}
		if ordered[i].chunkID != ordered[j].chunkID {
			return ordered[i].chunkID < ordered[j].chunkID
		}
		if ordered[i].project != ordered[j].project {
			return ordered[i].project < ordered[j].project
		}
		return ordered[i].createdBy < ordered[j].createdBy
	})

	orderedMutations := append([]dashboardMutationRow(nil), mutationRows...)
	sort.SliceStable(orderedMutations, func(i, j int) bool {
		if orderedMutations[i].seq != orderedMutations[j].seq {
			return orderedMutations[i].seq < orderedMutations[j].seq
		}
		if !orderedMutations[i].occurredAt.Equal(orderedMutations[j].occurredAt) {
			return orderedMutations[i].occurredAt.Before(orderedMutations[j].occurredAt)
		}
		if orderedMutations[i].project != orderedMutations[j].project {
			return orderedMutations[i].project < orderedMutations[j].project
		}
		if orderedMutations[i].entity != orderedMutations[j].entity {
			return orderedMutations[i].entity < orderedMutations[j].entity
		}
		return orderedMutations[i].entityKey < orderedMutations[j].entityKey
	})

	fold := newDashboardReadModelFold()
	for _, chunk := range ordered {
		if err := fold.applyChunk(chunk); err != nil {
			return dashboardReadModel{}, err
		}
	}
	for _, mutationRow := range orderedMutations {
		if err := fold.applyMutation(mutationRow); err != nil {
			return dashboardReadModel{}, err
		}
	}
	return fold.finish(), nil
}
