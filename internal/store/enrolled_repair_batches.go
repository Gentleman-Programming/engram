package store

import (
	"context"
	"database/sql"
	"maps"
)

// Row bounds limit payload materialization and transaction work, not elapsed time.
const enrolledRepairBatchSize = 64

type repairBatch struct {
	count   int
	cursors map[string]string
}

// Atomic callers pass nil and retain their existing ordering and full selection.
// Enrolled repair uses stable identity cursors, so concurrent acknowledgements
// cannot cause the same source row to be revisited forever within one attempt.
func (b *repairBatch) page(category, key, atomicOrder string) string {
	if b == nil {
		return atomicOrder
	}
	return " AND (? OR " + key + " > ?) ORDER BY " + key + " ASC LIMIT ?"
}

func (b *repairBatch) args(category string) []any {
	if b == nil {
		return nil
	}
	cursor, seen := b.cursors[category]
	return []any{!seen, cursor, enrolledRepairBatchSize - b.count}
}

func (b *repairBatch) record(category, key string) {
	if b != nil {
		b.count++
		b.cursors[category] = key
	}
}

func (s *Store) repairEnrolledProjectSyncMutationsContext(ctx context.Context) error {
	projects, err := s.enrolledProjectsNeedingBackfill()
	if err != nil {
		return err
	}
	for _, project := range projects {
		steps := []func(*sql.Tx, string, *repairBatch, ...string) error{
			s.backfillSessionSyncMutationsBatchTx,
			s.backfillObservationSyncMutationsBatchTx,
			s.backfillPromptSyncMutationsBatchTx,
			s.backfillSyncDeleteTombstonesBatchTx,
			s.backfillRelationSyncMutationsBatchTx,
		}
		cursors := map[string]string{}
		for _, step := range steps {
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				var batch *repairBatch
				err := s.withTx(func(tx *sql.Tx) error {
					// SQLite retries must start from the last committed cursor,
					// never from work that the failed transaction rolled back.
					batch = &repairBatch{cursors: maps.Clone(cursors)}
					if err := step(tx, project, batch); err != nil {
						return err
					}
					return ctx.Err()
				})
				if err != nil {
					return err
				}
				cursors = batch.cursors
				if batch.count == 0 {
					break
				}
			}
		}
	}
	return ctx.Err()
}
