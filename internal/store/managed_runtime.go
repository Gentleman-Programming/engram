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
CREATE TABLE IF NOT EXISTS managed_runtime_hosts (
	root TEXT NOT NULL,
	effective_id TEXT NOT NULL,
	pid INTEGER NOT NULL CHECK (pid > 1),
	start TEXT NOT NULL,
	domain TEXT NOT NULL,
	retired INTEGER NOT NULL DEFAULT 0 CHECK (retired IN (0, 1)),
	PRIMARY KEY (root, effective_id, pid, start, domain),
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
	ErrManagedRuntimeHostsRemain      = errors.New("managed runtime hosts have not been retired")
	ErrManagedRuntimeHostIdentity     = errors.New("invalid managed runtime host identity")
	errManagedRuntimeSchemaIncomplete = errors.New("managed runtime schema is incomplete")
)

// ManagedRuntimeScope is the exact runtime identity expected by an owned
// supervisor.
type ManagedRuntimeScope struct {
	Root, EffectiveID, Project, Directory, Mode string
}

func (scope ManagedRuntimeScope) normalized() (ManagedRuntimeScope, error) {
	if err := validateSessionID(scope.EffectiveID); err != nil {
		return scope, err
	}
	return scope.normalizedBinding()
}

func (scope ManagedRuntimeScope) normalizedBinding() (ManagedRuntimeScope, error) {
	if err := validateSessionID(scope.Root); err != nil {
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

type managedRuntimeAdmission struct {
	scope      ManagedRuntimeScope
	host       ManagedRuntimeHost
	generation int64
}

func newManagedRuntimeAdmission(scope ManagedRuntimeScope, host ManagedRuntimeHost) (managedRuntimeAdmission, error) {
	if !host.valid() {
		return managedRuntimeAdmission{}, ErrManagedRuntimeHostIdentity
	}
	scope, err := scope.normalized()
	return managedRuntimeAdmission{scope: scope, host: host}, err
}

// AttachManagedRuntime registers the live session and admits the verified host
// in one writer transaction. If the caller supplies an expected effective ID,
// selecting a different continuation rolls back. A host previously attached to
// this root cannot attach itself to a later continuation.
func (s *Store) AttachManagedRuntime(scope ManagedRuntimeScope, host ManagedRuntimeHost, resume bool) (string, int64, error) {
	if !host.valid() {
		return "", 0, ErrManagedRuntimeHostIdentity
	}
	if scope.EffectiveID != "" {
		if err := validateSessionID(scope.EffectiveID); err != nil {
			return "", 0, err
		}
	}
	scope, err := scope.normalizedBinding()
	if err != nil {
		return "", 0, err
	}
	admission := managedRuntimeAdmission{scope: scope, host: host}
	effective := scope.Root
	err = s.startSessionRegistration(scope.Root, scope.Project, scope.Directory, scope.Mode, resume, &effective, false, nil, nil, &admission)
	if err != nil {
		return "", 0, err
	}
	return effective, admission.generation, nil
}

// AdmitManagedRuntime advances the generation for an already registered binding.
// The owned supervisor is responsible for authorizing each admission.
func (s *Store) AdmitManagedRuntime(scope ManagedRuntimeScope, host ManagedRuntimeHost) (int64, error) {
	admission, err := newManagedRuntimeAdmission(scope, host)
	if err != nil {
		return 0, err
	}
	err = s.withTx(func(tx *sql.Tx) error {
		return s.admitManagedRuntimeTx(tx, &admission)
	})
	if err != nil {
		return 0, err
	}
	return admission.generation, nil
}

func (s *Store) admitManagedRuntimeTx(tx *sql.Tx, admission *managedRuntimeAdmission) error {
	scope, host := admission.scope, admission.host
	if err := scope.verifyLive(tx); err != nil {
		return err
	}
	var tables int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table'
		AND name IN ('managed_runtime_generations', 'managed_runtime_writes', 'managed_runtime_hosts')`).Scan(&tables); err != nil {
		return err
	}
	switch tables {
	case 0:
		if _, err := s.execHook(tx, managedRuntimeSchema); err != nil {
			return err
		}
	case 3:
	default:
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
	var otherBinding bool
	if err := tx.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM managed_runtime_hosts WHERE root=? AND effective_id<>? AND pid=? AND start=? AND domain=?
	)`, scope.Root, scope.EffectiveID, host.PID, host.Start, host.Domain).Scan(&otherBinding); err != nil {
		return err
	}
	if otherBinding {
		return ErrManagedRuntimeGeneration
	}
	var retired bool
	err := tx.QueryRow(`SELECT retired FROM managed_runtime_hosts
		WHERE root=? AND effective_id=? AND pid=? AND start=? AND domain=?`,
		scope.Root, scope.EffectiveID, host.PID, host.Start, host.Domain).Scan(&retired)
	if err == nil {
		if retired {
			return ErrManagedRuntimeGeneration
		}
		return scope.latestOpenGeneration(tx, &admission.generation)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := s.execHook(tx, `INSERT INTO managed_runtime_hosts(root,effective_id,pid,start,domain)
		VALUES (?,?,?,?,?)`, scope.Root, scope.EffectiveID, host.PID, host.Start, host.Domain); err != nil {
		return err
	}
	return tx.QueryRow(`
INSERT INTO managed_runtime_generations(root, project, directory, mode, effective_id)
VALUES (?, ?, ?, ?, ?) RETURNING generation`, scope.Root, scope.Project, scope.Directory, scope.Mode, scope.EffectiveID).Scan(&admission.generation)
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
		var enrolled bool
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM managed_runtime_hosts
			WHERE root=? AND effective_id=? AND retired=0)`, scope.Root, scope.EffectiveID).Scan(&enrolled); err != nil {
			return err
		}
		if !enrolled {
			return ErrManagedRuntimeHostIdentity
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
		var hostsRemain bool
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM managed_runtime_hosts
			WHERE root=? AND effective_id=? AND retired=0)`, scope.Root, scope.EffectiveID).Scan(&hostsRemain); err != nil {
			return err
		}
		if hostsRemain {
			return ErrManagedRuntimeHostsRemain
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
