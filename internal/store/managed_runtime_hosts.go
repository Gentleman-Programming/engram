package store

import (
	"database/sql"
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

// ListManagedRuntimeHosts recovers the current generation and its outstanding
// memberships without issuing a new generation during supervisor recovery.
func (s *Store) ListManagedRuntimeHosts(scope ManagedRuntimeScope) (int64, []ManagedRuntimeHost, error) {
	scope, err := scope.normalized()
	if err != nil {
		return 0, nil, err
	}
	var generation int64
	var hosts []ManagedRuntimeHost
	err = s.withTx(func(tx *sql.Tx) error {
		hosts = make([]ManagedRuntimeHost, 0)
		if err := scope.latestOpenGeneration(tx, &generation); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT pid,start,domain FROM managed_runtime_hosts
			WHERE root=? AND effective_id=? AND retired=0 ORDER BY pid,start,domain`, scope.Root, scope.EffectiveID)
		if err != nil {
			return err
		}
		defer rows.Close()
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
		if !current || closed {
			return ErrManagedRuntimeGeneration
		}
		if err := scope.verifyLive(tx); err != nil {
			return err
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
