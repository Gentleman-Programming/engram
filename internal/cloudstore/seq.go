package cloudstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// NextSeq acquires a per-project advisory lock, increments the server_seq_counter, and
// returns the new value. MUST be called inside a transaction — the advisory lock is
// released automatically on commit/rollback.
//
// This guarantees:
// - Monotonic ordering: if seq N is visible, all seq < N are also committed
// - No gaps: rolled-back transactions don't consume sequence values
// - No NULL window: seq is assigned before commit, not after
// - Per-project locks: concurrent pushes to different projects don't block each other
func NextSeq(ctx context.Context, tx pgx.Tx, project string) (int64, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1)::bigint)", project); err != nil {
		return 0, fmt.Errorf("advisory lock: %w", err)
	}

	// Ensure row exists for this project (no-op if already present)
	if _, err := tx.Exec(ctx,
		"INSERT INTO server_seq_counter (project, value) VALUES ($1, 0) ON CONFLICT DO NOTHING",
		project,
	); err != nil {
		return 0, fmt.Errorf("ensure seq row: %w", err)
	}

	var seq int64
	err := tx.QueryRow(ctx,
		"UPDATE server_seq_counter SET value = value + 1 WHERE project = $1 RETURNING value",
		project,
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("increment seq: %w", err)
	}

	return seq, nil
}
