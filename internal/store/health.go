package store

import "context"

// CheckReadiness checks that the core tables are readable through the existing
// generation-fenced connection. Empty tables are healthy. This is a bounded
// readiness read that uses ctx while waiting for or using a connection.
func (s *Store) CheckReadiness(ctx context.Context) error {
	var sessions, observations, prompts bool
	return s.db.QueryRowContext(ctx, `SELECT
  EXISTS(SELECT 1 FROM sessions),
  EXISTS(SELECT project, created_at, deleted_at FROM observations),
  EXISTS(SELECT 1 FROM user_prompts)
 `).Scan(&sessions, &observations, &prompts)
}
