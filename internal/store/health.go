package store

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
)

// CheckReadiness checks that the core tables are readable. Its own read-only
// connection uses the same generation fence as the rest of the store. Empty
// tables are healthy, and ctx bounds acquisition and execution.
func (s *Store) CheckReadiness(ctx context.Context) error {
	if s.compatibilityClosed.Load() {
		return sql.ErrConnDone
	}
	var sessions, observations, prompts bool
	return s.readinessDB.QueryRowContext(ctx, `SELECT
  EXISTS(SELECT 1 FROM sessions),
  EXISTS(SELECT project, created_at, deleted_at FROM observations),
  EXISTS(SELECT 1 FROM user_prompts)
 `).Scan(&sessions, &observations, &prompts)
}

func readinessDSN(dbPath string) string {
	// SQLite honors mode=ro only in a file URI. A leading slash keeps a
	// Windows drive letter in the path instead of the URI authority.
	path := filepath.ToSlash(dbPath)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	query := url.Values{"mode": {"ro"}, "_pragma": {sqliteBusyTimeout}}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}
