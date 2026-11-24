package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ManagedRuntimeHost identifies a process in its kernel's boot and PID
// namespace. The owned Core verifies the identity at the process boundary.
type ManagedRuntimeHost struct {
	PID    int    `json:"pid"`
	Start  string `json:"start"`
	Domain string `json:"domain,omitempty"`
}

func (host ManagedRuntimeHost) valid() bool {
	return host.PID > 1 && strings.TrimSpace(host.Start) != "" && strings.TrimSpace(host.Domain) != ""
}

func (scope ManagedRuntimeScope) latestOpenGeneration(tx *sql.Tx, generation *int64) error {
	return tx.QueryRow(`SELECT generation FROM managed_runtime_generations
		WHERE root=? AND project=? AND directory=? AND mode=? AND effective_id=? AND closed=0
		AND generation=(SELECT max(generation) FROM managed_runtime_generations WHERE root=?)`,
		scope.Root, scope.Project, scope.Directory, scope.Mode, scope.EffectiveID, scope.Root).Scan(generation)
}

func (scope ManagedRuntimeScope) verifyAttachment(ctx context.Context, tx *sql.Tx, host ManagedRuntimeHost) error {
	if err := scope.verifyLiveContext(ctx, tx); err != nil {
		return err
	}
	var attached bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM managed_runtime_generations AS g
		JOIN managed_runtime_hosts AS h ON h.root = g.root AND h.effective_id = g.effective_id
		WHERE g.root = ? AND g.effective_id = ? AND g.project = ? AND g.directory = ?
		AND g.mode = ? AND g.closed = 0 AND h.pid = ? AND h.start = ? AND h.domain = ?
		AND h.retired = 0 AND g.generation = (
			SELECT max(generation) FROM managed_runtime_generations WHERE root = g.root
		)
	)`, scope.Root, scope.EffectiveID, scope.Project, scope.Directory, scope.Mode, host.PID, host.Start, host.Domain).Scan(&attached)
	if err != nil {
		return err
	}
	if !attached {
		return ErrManagedRuntimeHostIdentity
	}
	return nil
}

// ListManagedRuntimeScopes enumerates the current open binding for each root.
// An explicit session end leaves that binding open for host reconciliation.
func (s *Store) ListManagedRuntimeScopes(ctx context.Context) ([]ManagedRuntimeScope, error) {
	scopes := make([]ManagedRuntimeScope, 0)
	err := s.withReadTxContext(ctx, func(tx *sql.Tx) error {
		tables, err := managedRuntimeTableCount(tx)
		if err != nil || tables == 0 {
			return err
		}
		if tables != 3 {
			return errManagedRuntimeSchemaIncomplete
		}
		rows, err := tx.QueryContext(ctx, `SELECT root, effective_id, project, directory, mode
			FROM managed_runtime_generations AS current
			WHERE closed=0 AND generation=(SELECT max(generation) FROM managed_runtime_generations WHERE root=current.root)
			ORDER BY root`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var scope ManagedRuntimeScope
			if err := rows.Scan(&scope.Root, &scope.EffectiveID, &scope.Project, &scope.Directory, &scope.Mode); err != nil {
				return err
			}
			scopes = append(scopes, scope)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return scopes, nil
}

func (scope ManagedRuntimeScope) latestOpenBindingGeneration(tx *sql.Tx, generation *int64) error {
	return tx.QueryRow(`SELECT generation FROM managed_runtime_generations
		WHERE root=? AND project=? AND directory=? AND mode=? AND effective_id=? AND closed=0
		AND generation=(SELECT max(generation) FROM managed_runtime_generations WHERE root=? AND effective_id=?)`,
		scope.Root, scope.Project, scope.Directory, scope.Mode, scope.EffectiveID, scope.Root, scope.EffectiveID).Scan(generation)
}

// ListManagedRuntimeHosts recovers the current generation and its outstanding
// memberships without issuing a new generation during supervisor recovery.
func (s *Store) ListManagedRuntimeHosts(scope ManagedRuntimeScope) (int64, []ManagedRuntimeHost, error) {
	return s.listManagedRuntimeHosts(scope, false)
}

// ListManagedRuntimeRetirementHosts can also find the latest open generation
// of an exact ended binding for a later, separately verified host retirement.
func (s *Store) ListManagedRuntimeRetirementHosts(scope ManagedRuntimeScope) (int64, []ManagedRuntimeHost, error) {
	return s.listManagedRuntimeHosts(scope, true)
}

func (s *Store) listManagedRuntimeHosts(scope ManagedRuntimeScope, forRetirement bool) (int64, []ManagedRuntimeHost, error) {
	scope, err := scope.normalized()
	if err != nil {
		return 0, nil, err
	}
	var generation int64
	var hosts []ManagedRuntimeHost
	err = s.withTx(func(tx *sql.Tx) error {
		hosts = make([]ManagedRuntimeHost, 0)
		if err := scope.latestOpenGeneration(tx, &generation); err != nil {
			if !forRetirement || !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := scope.latestOpenBindingGeneration(tx, &generation); err != nil {
				return err
			}
			scopeErr := scope.verifyLiveOrEnded(tx)
			if scopeErr == nil {
				return sql.ErrNoRows
			}
			if !errors.Is(scopeErr, ErrManagedRuntimeScopeEnded) {
				return scopeErr
			}
		}
		rows, err := tx.Query(`SELECT pid,start,domain FROM managed_runtime_hosts
			WHERE root=? AND effective_id=? AND retired=0 ORDER BY pid,start,domain`, scope.Root, scope.EffectiveID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var host ManagedRuntimeHost
			if err := rows.Scan(&host.PID, &host.Start, &host.Domain); err != nil {
				return err
			}
			hosts = append(hosts, host)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, nil, err
	}
	return generation, hosts, nil
}

// RetireManagedRuntimeHost persists the observed exit for a recorded host.
// Only callers that establish terminality in the recorded process domain may
// call this method. Retiring a host never clears an outstanding write intent.
func (s *Store) RetireManagedRuntimeHost(scope ManagedRuntimeScope, generation int64, host ManagedRuntimeHost) error {
	scope, err := scope.normalized()
	if err != nil {
		return err
	}
	if !host.valid() {
		return ErrManagedRuntimeHostIdentity
	}
	return s.withTx(func(tx *sql.Tx) error {
		closed, current, err := scope.generationState(tx, generation)
		if err != nil {
			return err
		}
		if closed {
			return ErrManagedRuntimeGeneration
		}
		scopeErr := scope.verifyLiveOrEnded(tx)
		ended := errors.Is(scopeErr, ErrManagedRuntimeScopeEnded)
		if !current {
			if !ended {
				return ErrManagedRuntimeGeneration
			}
			var latest int64
			if err := scope.latestOpenBindingGeneration(tx, &latest); err != nil {
				return err
			}
			if generation != latest {
				return ErrManagedRuntimeGeneration
			}
		}
		if scopeErr != nil && !ended {
			return scopeErr
		}
		result, err := s.execHook(tx, `UPDATE managed_runtime_hosts SET retired=1
			WHERE root=? AND effective_id=? AND pid=? AND start=? AND domain=?`,
			scope.Root, scope.EffectiveID, host.PID, host.Start, host.Domain)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err == nil && count != 1 {
			return sql.ErrNoRows
		}
		return err
	})
}
