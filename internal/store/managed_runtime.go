package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
)

const managedRuntimeSchema = `
CREATE TABLE IF NOT EXISTS managed_runtime_generations (
	root TEXT NOT NULL,
	generation INTEGER PRIMARY KEY AUTOINCREMENT,
	project TEXT NOT NULL,
	directory TEXT NOT NULL,
	mode TEXT NOT NULL,
	effective_id TEXT NOT NULL,
	closed INTEGER NOT NULL DEFAULT 0 CHECK (closed IN (0, 1)),
	FOREIGN KEY (root) REFERENCES sessions(id) ON DELETE CASCADE,
	FOREIGN KEY (effective_id) REFERENCES sessions(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS managed_runtime_writes (
	operation_id TEXT NOT NULL PRIMARY KEY,
	root TEXT NOT NULL,
	effective_id TEXT NOT NULL,
	confirmed INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0, 1)),
	FOREIGN KEY (root) REFERENCES sessions(id) ON DELETE CASCADE,
	FOREIGN KEY (effective_id) REFERENCES sessions(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_managed_runtime_generation_root ON managed_runtime_generations(root, generation);
CREATE INDEX IF NOT EXISTS idx_managed_runtime_effective ON managed_runtime_generations(effective_id, root, closed);
CREATE INDEX IF NOT EXISTS idx_managed_runtime_pending ON managed_runtime_writes(root, effective_id, confirmed);
`

var (
	ErrManagedRuntimeGeneration       = errors.New("managed runtime generation is not current")
	ErrManagedRuntimeWriteUnresolved  = errors.New("managed runtime has an unresolved observation write")
	errManagedRuntimeSchemaIncomplete = errors.New("managed runtime schema is incomplete")
)

// ManagedRuntimeScope is the exact runtime identity already verified by an
// owned supervisor. This Store API does not establish host membership.
type ManagedRuntimeScope struct {
	Root, EffectiveID, Project, Directory, Mode string
}

func (scope ManagedRuntimeScope) normalized() (ManagedRuntimeScope, error) {
	if err := validateSessionID(scope.Root); err != nil {
		return scope, err
	}
	if err := validateSessionID(scope.EffectiveID); err != nil {
		return scope, err
	}
	if !validSessionOwnershipMode(scope.Mode) {
		return scope, ErrInvalidSessionOwnershipMode
	}
	scope.Project, _ = NormalizeProject(scope.Project)
	if scope.Project == "" {
		return scope, ErrProjectRequired
	}
	if strings.TrimSpace(scope.Directory) == "" {
		return scope, ErrRuntimeSessionScopeConflict
	}
	scope.Directory = projectpkg.RuntimeWorktreeDirectory(scope.Directory)
	return scope, nil
}

func (scope ManagedRuntimeScope) verifyLive(tx *sql.Tx) error {
	id, err := runtimeSessionTx(context.Background(), tx, scope.Root, scope.Project, scope.Directory, scope.Mode)
	if err != nil {
		return err
	}
	if id != scope.EffectiveID {
		return ErrRuntimeSessionEffectiveIDMismatch
	}
	var matches int
	want := 2
	if scope.Root == scope.EffectiveID {
		want = 1
	}
	if err := tx.QueryRow(`SELECT count(*) FROM sessions
		WHERE id IN (?, ?) AND project = ?`, scope.Root, scope.EffectiveID, scope.Project).Scan(&matches); err != nil {
		return err
	}
	if matches != want {
		return ErrRuntimeSessionScopeConflict
	}
	return nil
}

// AdmitManagedRuntime advances the generation for a verified live binding.
// The owned supervisor is responsible for authorizing each admission and for
// retaining the current generation across changes to its set of live hosts.
func (s *Store) AdmitManagedRuntime(scope ManagedRuntimeScope) (int64, error) {
	scope, err := scope.normalized()
	if err != nil {
		return 0, err
	}
	var generation int64
	err = s.withTx(func(tx *sql.Tx) error {
		if err := scope.verifyLive(tx); err != nil {
			return err
		}
		var tables int
		if err := tx.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table'
			AND name IN ('managed_runtime_generations', 'managed_runtime_writes')`).Scan(&tables); err != nil {
			return err
		}
		switch tables {
		case 0:
			if _, err := s.execHook(tx, managedRuntimeSchema); err != nil {
				return err
			}
		case 1:
			return errManagedRuntimeSchemaIncomplete
		}
		var otherRoot bool
		if err := tx.QueryRow(`SELECT EXISTS(
			SELECT 1 FROM managed_runtime_generations WHERE effective_id=? AND root<>? AND closed=0
		)`, scope.EffectiveID, scope.Root).Scan(&otherRoot); err != nil {
			return err
		}
		if otherRoot {
			return ErrRuntimeSessionScopeConflict
		}
		return tx.QueryRow(`
INSERT INTO managed_runtime_generations(root, project, directory, mode, effective_id)
VALUES (?, ?, ?, ?, ?) RETURNING generation`, scope.Root, scope.Project, scope.Directory, scope.Mode, scope.EffectiveID).Scan(&generation)
	})
	if err != nil {
		return 0, err
	}
	return generation, nil
}

func (scope ManagedRuntimeScope) generationState(tx *sql.Tx, generation int64) (closed, current bool, err error) {
	err = tx.QueryRow(`
SELECT closed, generation = (SELECT max(generation) FROM managed_runtime_generations WHERE root=?)
FROM managed_runtime_generations
WHERE root=? AND generation=? AND project=? AND directory=? AND mode=? AND effective_id=?`,
		scope.Root, scope.Root, generation, scope.Project, scope.Directory, scope.Mode, scope.EffectiveID).Scan(&closed, &current)
	return
}

// RecordManagedRuntimeWrite persists intent before dispatch. Outstanding
// writes remain attached to the effective session across admission changes.
func (s *Store) RecordManagedRuntimeWrite(scope ManagedRuntimeScope, generation int64, operationID string) error {
	scope, err := scope.normalized()
	if err != nil {
		return err
	}
	if strings.TrimSpace(operationID) == "" {
		return ErrManagedRuntimeWriteUnresolved
	}
	return s.withTx(func(tx *sql.Tx) error {
		closed, current, err := scope.generationState(tx, generation)
		if err != nil {
			return err
		}
		if !current || closed {
			return ErrManagedRuntimeGeneration
		}
		if err := scope.verifyLive(tx); err != nil {
			return err
		}
		result, err := s.execHook(tx, `
INSERT INTO managed_runtime_writes(operation_id,root,effective_id)
SELECT ?,?,? WHERE NOT EXISTS (
	SELECT 1 FROM observation_save_operations WHERE operation_id=?
) ON CONFLICT(operation_id) DO NOTHING`, operationID, scope.Root, scope.EffectiveID, operationID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err == nil && count != 1 {
			return ErrManagedRuntimeWriteUnresolved
		}
		return err
	})
}

type managedRuntimeWrite struct {
	scope      ManagedRuntimeScope
	generation int64
}

// SaveManagedRuntimeObservation requires a recorded write intent. The intent is
// confirmed inside the same transaction as the operation ledger and observation.
func (s *Store) SaveManagedRuntimeObservation(scope ManagedRuntimeScope, generation int64, params AddObservationParams) (AddObservationResult, error) {
	scope, err := scope.normalized()
	if err != nil {
		return AddObservationResult{}, err
	}
	params.Project, params.SessionID = scope.Project, scope.EffectiveID
	params.managedRuntime = &managedRuntimeWrite{scope: scope, generation: generation}
	return s.AddObservationWithResult(params)
}

func (w managedRuntimeWrite) intentState(tx *sql.Tx, operationID string) (bool, error) {
	closed, current, err := w.scope.generationState(tx, w.generation)
	if err != nil {
		return false, err
	}
	if !current || closed {
		return false, ErrManagedRuntimeGeneration
	}
	if err := w.scope.verifyLive(tx); err != nil {
		return false, err
	}
	var confirmed bool
	err = tx.QueryRow(`SELECT confirmed FROM managed_runtime_writes
		WHERE operation_id=? AND root=? AND effective_id=?`, operationID, w.scope.Root, w.scope.EffectiveID).Scan(&confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrManagedRuntimeWriteUnresolved
	}
	return confirmed, err
}

// CloseManagedRuntime atomically verifies write receipts, closes the exact
// session, journals the end, and records the closure for repeat calls. The
// owned supervisor calls it only after it independently verifies terminality.
func (s *Store) CloseManagedRuntime(scope ManagedRuntimeScope, generation int64) (bool, error) {
	scope, err := scope.normalized()
	if err != nil {
		return false, err
	}
	var applied bool
	err = s.withTx(func(tx *sql.Tx) error {
		applied = false
		closed, current, err := scope.generationState(tx, generation)
		if err != nil || closed {
			return err
		}
		if !current {
			return ErrManagedRuntimeGeneration
		}
		if err := scope.verifyLive(tx); err != nil {
			return err
		}
		var pending bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM managed_runtime_writes WHERE root=? AND effective_id=? AND confirmed=0)`,
			scope.Root, scope.EffectiveID).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return ErrManagedRuntimeWriteUnresolved
		}
		var summary *string
		if err := tx.QueryRow("SELECT summary FROM sessions WHERE id=?", scope.EffectiveID).Scan(&summary); err != nil {
			return err
		}
		if err := s.endSessionSummaryOperation(scope.EffectiveID, summary)(tx); err != nil {
			return err
		}
		_, err = s.execHook(tx, `UPDATE managed_runtime_generations SET closed=1 WHERE root=? AND generation=?`, scope.Root, generation)
		applied = err == nil
		return err
	})
	return applied && err == nil, err
}
