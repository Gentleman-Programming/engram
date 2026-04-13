package cloudstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// NextSeq acquires an advisory lock, increments the server_seq_counter, and returns
// the new value. MUST be called inside a transaction — the advisory lock is released
// automatically on commit/rollback.
//
// This guarantees:
// - Monotonic ordering: if seq N is visible, all seq < N are also committed
// - No gaps: rolled-back transactions don't consume sequence values
// - No NULL window: seq is assigned before commit, not after
func NextSeq(ctx context.Context, tx pgx.Tx) (int64, error) {
	// Serialize all seq assignments globally.
	// At >100 concurrent writers, partition by project using:
	//   SELECT pg_advisory_xact_lock(hashtext($project)::bigint)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(1)"); err != nil {
		return 0, fmt.Errorf("advisory lock: %w", err)
	}

	var seq int64
	err := tx.QueryRow(ctx,
		"UPDATE server_seq_counter SET value = value + 1 RETURNING value",
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("increment seq: %w", err)
	}

	return seq, nil
}
