package cloudstore

import (
	"context"
	"fmt"
	"log"
	"time"
)

// MaintenanceResult reports what each cleanup job did.
type MaintenanceResult struct {
	IdempotencyKeysDeleted int64 `json:"idempotency_keys_deleted"`
	RateLimitsDeleted      int64 `json:"rate_limits_deleted"`
	RevisionsPruned        int64 `json:"revisions_pruned"`
}

// RunMaintenance executes all cleanup jobs once.
func (s *Store) RunMaintenance(ctx context.Context) (*MaintenanceResult, error) {
	result := &MaintenanceResult{}

	// Clean idempotency keys older than 24h
	n, err := s.CleanupIdempotencyKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("cleanup idempotency: %w", err)
	}
	result.IdempotencyKeysDeleted = n

	// Clean rate limit windows older than 1h
	tag, err := s.pool.Exec(ctx,
		"DELETE FROM rate_limits WHERE window_start < now() - interval '1 hour'",
	)
	if err != nil {
		return nil, fmt.Errorf("cleanup rate limits: %w", err)
	}
	result.RateLimitsDeleted = tag.RowsAffected()

	// Prune revisions: keep last 50 per observation
	tag, err = s.pool.Exec(ctx, `
		DELETE FROM observation_revisions
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY observation_id ORDER BY revision_number DESC
				) AS rn
				FROM observation_revisions
			) ranked
			WHERE rn > 50
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("prune revisions: %w", err)
	}
	result.RevisionsPruned = tag.RowsAffected()

	return result, nil
}

// StartMaintenanceLoop runs cleanup jobs on a ticker. Call in a goroutine.
func (s *Store) StartMaintenanceLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			result, err := s.RunMaintenance(ctx)
			if err != nil {
				log.Printf("maintenance error: %v", err)
				continue
			}
			if result.IdempotencyKeysDeleted > 0 || result.RateLimitsDeleted > 0 || result.RevisionsPruned > 0 {
				log.Printf("maintenance: keys=%d, rates=%d, revisions=%d",
					result.IdempotencyKeysDeleted, result.RateLimitsDeleted, result.RevisionsPruned)
			}
		case <-ctx.Done():
			return
		}
	}
}
