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
	if err := ctx.Err(); err != nil {
		return err
	}
	db, err := s.readinessDatabase()
	if err != nil {
		return err
	}
	var sessions, observations, prompts bool
	return db.QueryRowContext(ctx, `SELECT
  EXISTS(SELECT 1 FROM sessions),
  EXISTS(SELECT project, created_at, deleted_at FROM observations),
  EXISTS(SELECT 1 FROM user_prompts)
 `).Scan(&sessions, &observations, &prompts)
}

func (s *Store) readinessDatabase() (*sql.DB, error) {
	s.readinessMu.Lock()
	defer s.readinessMu.Unlock()
	if s.compatibilityClosed.Load() {
		return nil, sql.ErrConnDone
	}
	if s.readinessDB != nil {
		return s.readinessDB, nil
	}
	db, err := openGenerationDB(readinessDSN(s.generation.paths[0]), s.generation, true)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s.readinessDB = db
	return db, nil
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
