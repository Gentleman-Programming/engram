package store

// CheckReadiness checks that the core tables are readable through the existing
// generation-fenced connection. Empty tables are healthy. This is a bounded
// readiness read, not an integrity audit, a write probe, or a statistics query.
func (s *Store) CheckReadiness() error {
	var sessions, observations, prompts bool
	return s.db.QueryRow(`SELECT
  EXISTS(SELECT 1 FROM sessions),
  EXISTS(SELECT project, created_at, deleted_at FROM observations),
  EXISTS(SELECT 1 FROM user_prompts)
 `).Scan(&sessions, &observations, &prompts)
}
