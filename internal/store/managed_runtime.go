package store

import (
	"context"
	"database/sql"
	"encoding/json"
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

const managedRuntimeFingerprintMigration = "ALTER TABLE managed_runtime_writes ADD COLUMN request_fingerprint TEXT NOT NULL DEFAULT ''"
const managedRuntimePayloadMigration = "ALTER TABLE managed_runtime_writes ADD COLUMN observation_payload BLOB"

var (
	ErrManagedRuntimeGeneration              = errors.New("managed runtime generation is not current")
	ErrManagedRuntimeScopeEnded              = errors.New("managed runtime effective session has ended")
	ErrManagedRuntimeCloseRequiresGeneration = errors.New("managed runtime close requires a cohort generation")
	ErrManagedRuntimeWriteUnresolved         = errors.New("managed runtime has an unresolved observation write")
	ErrManagedRuntimeHostsRemain             = errors.New("managed runtime hosts have not been retired")
	ErrManagedRuntimeHostIdentity            = errors.New("invalid managed runtime host identity")
	errManagedRuntimeSchemaIncomplete        = errors.New("managed runtime schema is incomplete")
)

// ManagedRuntimeScope is the exact runtime identity expected by an owned
// supervisor.
type ManagedRuntimeScope struct {
	Root        string `json:"root"`
	EffectiveID string `json:"effective_id,omitempty"`
	Project     string `json:"project"`
	Directory   string `json:"directory"`
	Mode        string `json:"ownership_mode"`
}

func (scope ManagedRuntimeScope) normalized() (ManagedRuntimeScope, error) {
	return scope.normalizedContext(context.Background())
}

func (scope ManagedRuntimeScope) normalizedContext(ctx context.Context) (ManagedRuntimeScope, error) {
	if err := validateSessionID(scope.EffectiveID); err != nil {
		return scope, err
	}
	return scope.normalizedBindingContext(ctx)
}

func (scope ManagedRuntimeScope) normalizedBinding() (ManagedRuntimeScope, error) {
	return scope.normalizedBindingContext(context.Background())
}

func (scope ManagedRuntimeScope) normalizedBindingContext(ctx context.Context) (ManagedRuntimeScope, error) {
	if err := ctx.Err(); err != nil {
		return scope, err
	}
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
	scope.Directory = projectpkg.RuntimeWorktreeDirectoryContext(ctx, scope.Directory)
	return scope, ctx.Err()
}

func (scope ManagedRuntimeScope) verifyLive(tx *sql.Tx) error {
	return scope.verifyLiveContext(context.Background(), tx)
}

func (scope ManagedRuntimeScope) verifyLiveContext(ctx context.Context, tx *sql.Tx) error {
	id, err := runtimeSessionTx(ctx, tx, scope.Root, scope.Project, scope.Directory, scope.Mode)
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
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions
		WHERE id IN (?, ?) AND project = ?`, scope.Root, scope.EffectiveID, scope.Project).Scan(&matches); err != nil {
		return err
	}
	if matches != want {
		return ErrRuntimeSessionScopeConflict
	}
	return nil
}

// verifyLiveOrEnded distinguishes a positively identified ended session
// from a missing or different runtime binding.
func (scope ManagedRuntimeScope) verifyLiveOrEnded(tx *sql.Tx) error {
	liveErr := scope.verifyLive(tx)
	if !errors.Is(liveErr, sql.ErrNoRows) && !errors.Is(liveErr, ErrRuntimeSessionEffectiveIDMismatch) {
		return liveErr
	}
	var directory string
	err := tx.QueryRow(`SELECT ifnull(directory, '') FROM sessions
		WHERE id=? AND project=? AND ownership_mode=? AND ended_at IS NOT NULL`,
		scope.EffectiveID, scope.Project, scope.Mode).Scan(&directory)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if strings.TrimSpace(directory) != "" && projectpkg.RuntimeWorktreeDirectory(directory) == scope.Directory {
		return ErrManagedRuntimeScopeEnded
	}
	return liveErr
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
	tables, err := managedRuntimeTableCount(tx)
	if err != nil {
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
	var otherBinding, previousHosts bool
	if err := tx.QueryRow(`SELECT
		EXISTS(SELECT 1 FROM managed_runtime_hosts WHERE root=? AND effective_id<>? AND pid=? AND start=? AND domain=?),
		EXISTS(SELECT 1 FROM managed_runtime_hosts WHERE root=? AND effective_id<>? AND retired=0)`,
		scope.Root, scope.EffectiveID, host.PID, host.Start, host.Domain,
		scope.Root, scope.EffectiveID).Scan(&otherBinding, &previousHosts); err != nil {
		return err
	}
	if otherBinding {
		return ErrManagedRuntimeGeneration
	}
	var retired bool
	err = tx.QueryRow(`SELECT retired FROM managed_runtime_hosts
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
	// Only a new binding would strand hosts in the previous generation.
	if previousHosts {
		var current int64
		err := scope.latestOpenGeneration(tx, &current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrManagedRuntimeHostsRemain
		}
		if err != nil {
			return err
		}
	}
	if _, err := s.execHook(tx, `INSERT INTO managed_runtime_hosts(root,effective_id,pid,start,domain)
		VALUES (?,?,?,?,?)`, scope.Root, scope.EffectiveID, host.PID, host.Start, host.Domain); err != nil {
		return err
	}
	return tx.QueryRow(`
INSERT INTO managed_runtime_generations(root, project, directory, mode, effective_id)
VALUES (?, ?, ?, ?, ?) RETURNING generation`, scope.Root, scope.Project, scope.Directory, scope.Mode, scope.EffectiveID).Scan(&admission.generation)
}

func managedRuntimeTableCount(tx *sql.Tx) (int, error) {
	var tables int
	err := tx.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table'
		AND name IN ('managed_runtime_generations', 'managed_runtime_writes', 'managed_runtime_hosts')`).Scan(&tables)
	return tables, err
}

func requireUnmanagedRuntimeCloseTx(tx *sql.Tx, effectiveID string) error {
	tables, err := managedRuntimeTableCount(tx)
	if err != nil || tables == 0 {
		return err
	}
	if tables != 3 {
		return errManagedRuntimeSchemaIncomplete
	}
	var managed bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM managed_runtime_generations
		WHERE effective_id = ? AND closed = 0)`, effectiveID).Scan(&managed); err != nil {
		return err
	}
	if managed {
		return ErrManagedRuntimeCloseRequiresGeneration
	}
	return nil
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
	return s.recordManagedRuntimeWrite(scope, generation, operationID, nil)
}

// RecordManagedRuntimeObservation validates the requested observation before
// creating a save fence. Only validated, redacted content is recoverable.
func (s *Store) RecordManagedRuntimeObservation(scope ManagedRuntimeScope, generation int64, operationID string, observation AddObservationParams) error {
	return s.recordManagedRuntimeWrite(scope, generation, operationID, &observation)
}

func (s *Store) recordManagedRuntimeWrite(scope ManagedRuntimeScope, generation int64, operationID string, observation *AddObservationParams) error {
	scope, err := scope.normalized()
	if err != nil {
		return err
	}
	if strings.TrimSpace(operationID) == "" {
		return ErrManagedRuntimeWriteUnresolved
	}
	fingerprint := ""
	var payload []byte
	if observation != nil {
		observation.Project, observation.SessionID = scope.Project, scope.EffectiveID
		if err := observation.ProjectProvenance.Validate(); err != nil {
			return err
		}
		title, content, err := s.prepareObservationBody(*observation)
		if err != nil {
			return err
		}
		fingerprint = managedObservationFingerprint(*observation, title, content, normalizeScope(observation.Scope), normalizeTopicKey(observation.TopicKey))
		observation.Title, observation.Content = title, content
		observation.OperationID = operationID
		payload, err = json.Marshal(observation)
		if err != nil {
			return err
		}
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
		if err := s.ensureManagedRuntimeWriteColumns(tx); err != nil {
			return err
		}
		var root, effective, prior string
		err = tx.QueryRow(`SELECT root,effective_id,request_fingerprint FROM managed_runtime_writes WHERE operation_id=?`,
			operationID).Scan(&root, &effective, &prior)
		if err == nil {
			if root != scope.Root || effective != scope.EffectiveID || prior != fingerprint {
				return ErrObservationOperationConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
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
		var opaquePending bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM managed_runtime_writes
			WHERE root=? AND effective_id=? AND confirmed=0 AND observation_payload IS NULL)`,
			scope.Root, scope.EffectiveID).Scan(&opaquePending); err != nil {
			return err
		}
		if opaquePending {
			return ErrManagedRuntimeWriteUnresolved
		}
		result, err := s.execHook(tx, `
INSERT INTO managed_runtime_writes(operation_id,root,effective_id,request_fingerprint,observation_payload)
SELECT ?,?,?,?,? WHERE NOT EXISTS (
	SELECT 1 FROM observation_save_operations WHERE operation_id=?
) ON CONFLICT(operation_id) DO NOTHING`, operationID, scope.Root, scope.EffectiveID, fingerprint, payload, operationID)
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

func managedRuntimeWriteColumns(tx *sql.Tx) (fingerprint, payload bool, err error) {
	err = tx.QueryRow(`SELECT
		EXISTS(SELECT 1 FROM pragma_table_info('managed_runtime_writes') WHERE name='request_fingerprint'),
		EXISTS(SELECT 1 FROM pragma_table_info('managed_runtime_writes') WHERE name='observation_payload')
	`).Scan(&fingerprint, &payload)
	return
}

func (s *Store) ensureManagedRuntimeWriteColumns(tx *sql.Tx) error {
	fingerprint, payload, err := managedRuntimeWriteColumns(tx)
	if err != nil {
		return err
	}
	if !fingerprint {
		if _, err := s.execHook(tx, managedRuntimeFingerprintMigration); err != nil {
			return err
		}
	}
	if !payload {
		_, err := s.execHook(tx, managedRuntimePayloadMigration)
		return err
	}
	return nil
}

func managedObservationFingerprint(p AddObservationParams, title, content, scope, topicKey string) string {
	provenance := p.ProjectProvenance
	if provenance == nil {
		return observationSaveFingerprint(p, title, content, p.Project, scope, topicKey, p.Org)
	}
	return observationSaveFingerprint(p, title, content, p.Project, scope, topicKey,
		p.Org, provenance.Source, provenance.Directory, provenance.RawProject)
}

type managedRuntimeWrite struct {
	scope      ManagedRuntimeScope
	generation int64
}

// SaveManagedRuntimeObservation requires a recorded write intent. The intent is
// confirmed inside the same transaction as the operation ledger and observation.
// Deduplication and topic updates use the Store's usual project boundaries.
func (s *Store) SaveManagedRuntimeObservation(scope ManagedRuntimeScope, generation int64, params AddObservationParams) (AddObservationResult, error) {
	scope, err := scope.normalized()
	if err != nil {
		return AddObservationResult{}, err
	}
	params.Project, params.SessionID = scope.Project, scope.EffectiveID
	params.managedRuntime = &managedRuntimeWrite{scope: scope, generation: generation}
	return s.AddObservationWithResult(params)
}

func (w managedRuntimeWrite) intentState(tx *sql.Tx, operationID, fingerprint string) (bool, error) {
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
	var prior string
	var earlier bool
	err = tx.QueryRow(`SELECT current.confirmed,current.request_fingerprint, EXISTS(
		SELECT 1 FROM managed_runtime_writes AS pending
		WHERE pending.root=current.root AND pending.effective_id=current.effective_id
		AND pending.confirmed=0 AND pending.rowid<current.rowid
	) FROM managed_runtime_writes AS current
		WHERE current.operation_id=? AND current.root=? AND current.effective_id=?`, operationID, w.scope.Root, w.scope.EffectiveID).Scan(&confirmed, &prior, &earlier)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrManagedRuntimeWriteUnresolved
	}
	if err == nil && prior != "" && prior != fingerprint {
		return false, ErrObservationOperationConflict
	}
	if err == nil && !confirmed && earlier {
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
		scopeErr := scope.verifyLiveOrEnded(tx)
		if scopeErr != nil && !errors.Is(scopeErr, ErrManagedRuntimeScopeEnded) {
			return scopeErr
		}
		var hostsRemain bool
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM managed_runtime_hosts
			WHERE root=? AND effective_id=? AND retired=0)`, scope.Root, scope.EffectiveID).Scan(&hostsRemain); err != nil {
			return err
		}
		if hostsRemain {
			return ErrManagedRuntimeHostsRemain
		}
		if scopeErr != nil {
			return scopeErr
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
