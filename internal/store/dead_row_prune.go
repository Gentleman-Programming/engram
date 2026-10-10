package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Dead-row retention for sync_apply_deferred (issue #849).
//
// Dead rows are permanent evidence that a pulled mutation was discarded, and
// their content-derived identities (#798, #845) keep distinct evidence apart.
// The cost is that a peer re-emitting one failing mutation with a moving
// timestamp adds a new row on every delivery. PruneDeadRows bounds that growth
// with two limits applied to apply_status='dead' rows only:
//
//   - an age limit on first_seen_at, which bounds stale evidence, and
//   - a per-scope cap (target_key + project) that keeps the newest rows, which
//     bounds a churning peer whose rows are all fresh without letting it evict
//     another scope's evidence.
//
// Deferred rows are retry state governed by ReplayDeferredForScope and are never
// touched. Pruning runs in its own transaction, never inside pull-apply.

// DefaultDeadRowMaxAgeDays and DefaultDeadRowMaxPerScope are the nominated
// retention defaults. They are a product call, kept as named constants so they
// can be surfaced and revisited in one place.
const (
	DefaultDeadRowMaxAgeDays  = 30
	DefaultDeadRowMaxPerScope = 1000
)

// Age buckets used to summarize evicted evidence.
const (
	DeadRowAgeBucket0To6Days   = "0-6d"
	DeadRowAgeBucket7To29Days  = "7-29d"
	DeadRowAgeBucket30DaysPlus = "30d+"
	DeadRowAgeBucketUnknown    = "unknown"
)

var deadRowAgeBucketOrder = map[string]int{
	DeadRowAgeBucket0To6Days:   0,
	DeadRowAgeBucket7To29Days:  1,
	DeadRowAgeBucket30DaysPlus: 2,
	DeadRowAgeBucketUnknown:    3,
}

// pruneDeleteChunk bounds the number of bound parameters per DELETE.
const pruneDeleteChunk = 500

// PruneDeadRowsOptions selects the retention limits. Apply false is a dry run
// that reports exactly what Apply true would delete.
type PruneDeadRowsOptions struct {
	MaxAgeDays  int
	MaxPerScope int
	Apply       bool
}

// PruneDeadRowsScope counts the rows one scope loses, split by the limit that
// selected them. A row past the age limit counts as ByAge even when it is also
// beyond the cap.
type PruneDeadRowsScope struct {
	TargetKey string
	Project   string
	ByAge     int
	ByCap     int
}

// PruneDeadRowsReason counts the selected rows per entity, reason_code, and age
// bucket, so eviction is never silent about what evidence it removes.
type PruneDeadRowsReason struct {
	Entity     string
	ReasonCode string
	AgeBucket  string
	Count      int
}

// PruneDeadRowsResult reports what was selected and whether it was deleted.
type PruneDeadRowsResult struct {
	Applied bool
	Total   int
	Scopes  []PruneDeadRowsScope
	Reasons []PruneDeadRowsReason
}

// pruneDeadRowCandidatesSQL selects the dead rows past either limit. The date
// arithmetic stays in SQL so callers and tests never fake a clock.
//
// Arguments: age modifier (e.g. "-30 days"), the same modifier, per-scope cap.
const pruneDeadRowCandidatesSQL = `
	SELECT sync_id, target_key, project, entity, reason_code,
		first_seen_at < datetime('now', ?) AS expired,
		ifnull(CAST(julianday('now') - julianday(first_seen_at) AS INTEGER), -1) AS age_days
	FROM (
		SELECT sync_id, target_key, project, entity, reason_code, first_seen_at,
			ROW_NUMBER() OVER (
				PARTITION BY target_key, project
				ORDER BY first_seen_at DESC, sync_id DESC
			) AS scope_rank
		FROM sync_apply_deferred
		WHERE apply_status = 'dead'
	)
	WHERE first_seen_at < datetime('now', ?) OR scope_rank > ?
`

type pruneDeadRowCandidate struct {
	syncID     string
	targetKey  string
	project    string
	entity     string
	reasonCode string
	expired    bool
	ageDays    int
}

// PruneDeadRows selects dead rows past the age limit or beyond the per-scope cap
// and, when opts.Apply is true, deletes them in one transaction.
func (s *Store) PruneDeadRows(opts PruneDeadRowsOptions) (PruneDeadRowsResult, error) {
	if opts.MaxAgeDays <= 0 {
		return PruneDeadRowsResult{}, errors.New("prune dead rows: max age days must be positive")
	}
	if opts.MaxPerScope <= 0 {
		return PruneDeadRowsResult{}, errors.New("prune dead rows: max per scope must be positive")
	}

	if !opts.Apply {
		candidates, err := selectPruneDeadRowCandidates(s.db, opts)
		if err != nil {
			return PruneDeadRowsResult{}, err
		}
		return summarizePruneDeadRows(candidates, false), nil
	}

	var candidates []pruneDeadRowCandidate
	err := s.withTx(func(tx *sql.Tx) error {
		var err error
		candidates, err = selectPruneDeadRowCandidates(tx, opts)
		if err != nil {
			return err
		}
		for start := 0; start < len(candidates); start += pruneDeleteChunk {
			end := min(start+pruneDeleteChunk, len(candidates))
			chunk := candidates[start:end]
			args := make([]any, len(chunk))
			for i, c := range chunk {
				args[i] = c.syncID
			}
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
			// The status guard keeps a row re-armed to deferred out of reach.
			if _, err := s.execHook(tx, `DELETE FROM sync_apply_deferred
				WHERE apply_status = 'dead' AND sync_id IN (`+placeholders+`)`, args...); err != nil {
				return fmt.Errorf("prune dead rows: delete: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return PruneDeadRowsResult{}, err
	}
	return summarizePruneDeadRows(candidates, true), nil
}

func selectPruneDeadRowCandidates(db queryer, opts PruneDeadRowsOptions) ([]pruneDeadRowCandidate, error) {
	modifier := fmt.Sprintf("-%d days", opts.MaxAgeDays)
	rows, err := db.Query(pruneDeadRowCandidatesSQL, modifier, modifier, opts.MaxPerScope)
	if err != nil {
		return nil, fmt.Errorf("prune dead rows: select: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var candidates []pruneDeadRowCandidate
	for rows.Next() {
		var c pruneDeadRowCandidate
		if err := rows.Scan(&c.syncID, &c.targetKey, &c.project, &c.entity, &c.reasonCode, &c.expired, &c.ageDays); err != nil {
			return nil, fmt.Errorf("prune dead rows: scan: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("prune dead rows: rows: %w", err)
	}
	return candidates, nil
}

func deadRowAgeBucket(ageDays int) string {
	switch {
	case ageDays < 0:
		return DeadRowAgeBucketUnknown
	case ageDays < 7:
		return DeadRowAgeBucket0To6Days
	case ageDays < 30:
		return DeadRowAgeBucket7To29Days
	default:
		return DeadRowAgeBucket30DaysPlus
	}
}

func summarizePruneDeadRows(candidates []pruneDeadRowCandidate, applied bool) PruneDeadRowsResult {
	type scopeKey struct{ targetKey, project string }
	type reasonKey struct{ entity, reasonCode, bucket string }
	scopes := map[scopeKey]*PruneDeadRowsScope{}
	reasons := map[reasonKey]*PruneDeadRowsReason{}
	for _, c := range candidates {
		sk := scopeKey{c.targetKey, c.project}
		scope, ok := scopes[sk]
		if !ok {
			scope = &PruneDeadRowsScope{TargetKey: c.targetKey, Project: c.project}
			scopes[sk] = scope
		}
		if c.expired {
			scope.ByAge++
		} else {
			scope.ByCap++
		}
		rk := reasonKey{c.entity, c.reasonCode, deadRowAgeBucket(c.ageDays)}
		reason, ok := reasons[rk]
		if !ok {
			reason = &PruneDeadRowsReason{Entity: rk.entity, ReasonCode: rk.reasonCode, AgeBucket: rk.bucket}
			reasons[rk] = reason
		}
		reason.Count++
	}

	result := PruneDeadRowsResult{Applied: applied, Total: len(candidates)}
	for _, scope := range scopes {
		result.Scopes = append(result.Scopes, *scope)
	}
	sort.Slice(result.Scopes, func(i, j int) bool {
		a, b := result.Scopes[i], result.Scopes[j]
		if a.TargetKey != b.TargetKey {
			return a.TargetKey < b.TargetKey
		}
		return a.Project < b.Project
	})
	for _, reason := range reasons {
		result.Reasons = append(result.Reasons, *reason)
	}
	sort.Slice(result.Reasons, func(i, j int) bool {
		a, b := result.Reasons[i], result.Reasons[j]
		if a.Entity != b.Entity {
			return a.Entity < b.Entity
		}
		if a.ReasonCode != b.ReasonCode {
			return a.ReasonCode < b.ReasonCode
		}
		return deadRowAgeBucketOrder[a.AgeBucket] < deadRowAgeBucketOrder[b.AgeBucket]
	})
	return result
}
