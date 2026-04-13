package cloudstore

import (
	"context"
	"encoding/json"
	"fmt"
)

// CheckIdempotencyKey checks if a push was already processed.
// Returns the cached response if found, nil otherwise.
func (s *Store) CheckIdempotencyKey(ctx context.Context, key string) (json.RawMessage, error) {
	var response json.RawMessage
	err := s.pool.QueryRow(ctx,
		"SELECT response FROM idempotency_keys WHERE key = $1", key,
	).Scan(&response)
	if err != nil {
		return nil, nil // not found — not an error
	}
	return response, nil
}

// SaveIdempotencyKey caches a push response for deduplication.
func (s *Store) SaveIdempotencyKey(ctx context.Context, key string, response any) error {
	data, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	_, err = s.pool.Exec(ctx,
		"INSERT INTO idempotency_keys (key, response) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		key, data,
	)
	return err
}

// CleanupIdempotencyKeys deletes keys older than 24 hours.
func (s *Store) CleanupIdempotencyKeys(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		"DELETE FROM idempotency_keys WHERE created_at < now() - interval '24 hours'",
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
