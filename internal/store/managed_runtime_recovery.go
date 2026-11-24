package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

const managedRuntimeRecoveryBatch = 32

// RecoverManagedRuntimeObservations replays a bounded batch of already
// authorized writes for the exact current cohort. Older opaque intents still
// require their original caller payload and continue to fence closure.
func (s *Store) RecoverManagedRuntimeObservations(scope ManagedRuntimeScope, generation int64) (applied int, more bool, err error) {
	scope, err = scope.normalized()
	if err != nil {
		return 0, false, err
	}
	pending := make([]string, 0, managedRuntimeRecoveryBatch+1)
	err = s.withReadTx(func(tx *sql.Tx) error {
		pending = pending[:0]
		closed, current, err := scope.generationState(tx, generation)
		if err != nil {
			return err
		}
		if !current || closed {
			return ErrManagedRuntimeGeneration
		}
		if err := scope.verifyLiveOrEnded(tx); err != nil {
			return err
		}
		_, hasPayload, err := managedRuntimeWriteColumns(tx)
		if err != nil || !hasPayload {
			return err
		}
		rows, err := tx.Query(`SELECT operation_id,observation_payload IS NOT NULL FROM managed_runtime_writes
			WHERE root=? AND effective_id=? AND confirmed=0 ORDER BY rowid LIMIT ?`,
			scope.Root, scope.EffectiveID, managedRuntimeRecoveryBatch+1)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var operationID string
			var recoverable bool
			if err := rows.Scan(&operationID, &recoverable); err != nil {
				return err
			}
			if !recoverable {
				// Preserve admission order behind a legacy, opaque intent.
				break
			}
			pending = append(pending, operationID)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, false, err
	}
	more = len(pending) > managedRuntimeRecoveryBatch
	if more {
		pending = pending[:managedRuntimeRecoveryBatch]
	}
	for _, operationID := range pending {
		// Fetch one payload at a time so a batch holds only one observation body.
		var payload []byte
		err := s.withReadTx(func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT observation_payload FROM managed_runtime_writes
				WHERE operation_id=? AND root=? AND effective_id=? AND confirmed=0`,
				operationID, scope.Root, scope.EffectiveID).Scan(&payload)
		})
		if errors.Is(err, sql.ErrNoRows) {
			continue // Another recovery or an explicit save already resolved it.
		}
		if err != nil {
			return applied, false, err
		}
		var observation AddObservationParams
		if err := json.Unmarshal(payload, &observation); err != nil {
			return applied, false, errors.New("invalid recoverable managed observation")
		}
		if observation.OperationID != operationID || observation.Project != scope.Project || observation.SessionID != scope.EffectiveID {
			return applied, false, ErrObservationOperationConflict
		}
		if err := ValidateObservationTitle(observation.Title); err != nil {
			return applied, false, err
		}
		if observation.Content == "" {
			return applied, false, ErrObservationContentRequired
		}
		observation.managedRuntime = &managedRuntimeWrite{scope: scope, generation: generation}
		result, err := s.addPreparedObservation(observation, observation.Title, observation.Content)
		if err != nil {
			return applied, false, err
		}
		if !result.Replayed {
			applied++
		}
	}
	return applied, more, nil
}
