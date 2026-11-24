package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ManagedObservationUpdate struct {
	ID              int64                   `json:"id"`
	ExpectedProject string                  `json:"expected_project"`
	Changes         UpdateObservationParams `json:"changes"`
}

type ManagedObservationUpdateReceipt struct {
	ID         int64               `json:"id"`
	Project    string              `json:"project"`
	Replayed   bool                `json:"replayed"`
	Truncation *TruncationMetadata `json:"truncation,omitempty"`
}

const managedUpdateFingerprintPrefix = "update:"

var ErrManagedObservationUpdateEmpty = errors.New("managed observation update requires at least one changed field")

// UpdateManagedRuntimeObservation commits the edit and its receipt together.
// A lost response can be retried by the live attachment with the same key.
func (s *Store) UpdateManagedRuntimeObservation(ctx context.Context, scope ManagedRuntimeScope, host ManagedRuntimeHost, operationID string, update ManagedObservationUpdate) (ManagedObservationUpdateReceipt, error) {
	var zero ManagedObservationUpdateReceipt
	if !host.valid() {
		return zero, ErrManagedRuntimeHostIdentity
	}
	scope, err := scope.normalizedContext(ctx)
	if err != nil {
		return zero, err
	}
	project, err := ValidateExpectedProject(update.ExpectedProject)
	if err != nil {
		return zero, err
	}
	if project != scope.Project {
		return zero, ErrObservationProjectMismatch
	}
	update.ExpectedProject = project
	if update.Changes.Project != nil {
		return zero, ErrObservationProjectImmutable
	}
	if update.ID <= 0 || operationID == "" || len(operationID) > 256 || strings.TrimSpace(operationID) != operationID {
		return zero, ErrObservationOperationConflict
	}
	if update.Changes == (UpdateObservationParams{}) {
		return zero, ErrManagedObservationUpdateEmpty
	}
	// Full title and content edits use the same redaction as storage.
	fingerprintInput := update
	if title := update.Changes.Title; title != nil {
		redacted := stripPrivateTags(*title)
		fingerprintInput.Changes.Title = &redacted
	}
	if content := update.Changes.Content; content != nil {
		redacted := stripPrivateTags(*content)
		fingerprintInput.Changes.Content = &redacted
	}
	payload, err := json.Marshal(fingerprintInput)
	if err != nil {
		return zero, err
	}
	fingerprint := fmt.Sprintf("%s%x", managedUpdateFingerprintPrefix, sha256.Sum256(payload))
	reply := ManagedObservationUpdateReceipt{ID: update.ID, Project: project}
	err = s.withTx(func(tx *sql.Tx) error {
		reply.Replayed = false
		reply.Truncation = nil
		if err := scope.verifyAttachment(ctx, tx, host); err != nil {
			return err
		}
		if err := s.ensureManagedRuntimeWriteColumns(tx); err != nil {
			return err
		}
		var root, effective, prior string
		var confirmed bool
		var receiptMetadata []byte
		err := tx.QueryRow(`SELECT root,effective_id,request_fingerprint,confirmed,observation_payload
			FROM managed_runtime_writes WHERE operation_id=?`, operationID).Scan(&root, &effective, &prior, &confirmed, &receiptMetadata)
		if err == nil {
			if root != scope.Root || effective != scope.EffectiveID || prior != fingerprint || !confirmed {
				return ErrObservationOperationConflict
			}
			if len(receiptMetadata) != 0 {
				if err := json.Unmarshal(receiptMetadata, &reply.Truncation); err != nil {
					return err
				}
			}
			reply.Replayed = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := s.validateObservationUpdate(update.Changes); err != nil {
			return err
		}
		var pending bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM managed_runtime_writes
			WHERE root=? AND effective_id=? AND confirmed=0)`, scope.Root, scope.EffectiveID).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return ErrManagedRuntimeWriteUnresolved
		}
		// A confirmed edit stores only receipt metadata in this column.
		// Recovery still selects unconfirmed staged save payloads.
		if update.Changes.Content != nil {
			metadata := s.ContentTruncation(*update.Changes.Content)
			reply.Truncation = &metadata
			receiptMetadata, err = json.Marshal(metadata)
			if err != nil {
				return err
			}
		}
		receipt, err := s.execHook(tx, `INSERT INTO managed_runtime_writes(operation_id,root,effective_id,request_fingerprint,confirmed,observation_payload)
			SELECT ?,?,?,?,1,? WHERE NOT EXISTS(SELECT 1 FROM observation_save_operations WHERE operation_id=?)`,
			operationID, scope.Root, scope.EffectiveID, fingerprint, receiptMetadata, operationID)
		if err != nil {
			return err
		}
		count, err := receipt.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrObservationOperationConflict
		}
		_, err = s.updateObservationTx(tx, update.ID, update.Changes, &project)
		return err
	})
	if err != nil {
		return zero, err
	}
	return reply, nil
}

func rejectManagedUpdateOperation(tx *sql.Tx, operationID string) error {
	hasFingerprint, _, err := managedRuntimeWriteColumns(tx)
	if err != nil || !hasFingerprint {
		return err
	}
	var isUpdate bool
	err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM managed_runtime_writes
		WHERE operation_id=? AND request_fingerprint GLOB ?)`, operationID, managedUpdateFingerprintPrefix+"*").Scan(&isUpdate)
	if err == nil && isUpdate {
		return ErrObservationOperationConflict
	}
	return err
}
