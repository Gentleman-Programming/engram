package store

import (
	"context"
	"database/sql"
)

type contextSectionQueryer interface {
	queryer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) queryContextSection(ctx context.Context, db contextSectionQueryer, query string, args ...any) (rowScanner, error) {
	if ctx.Done() == nil {
		return s.queryItHook(db, query, args...)
	}
	if db == s.db {
		return s.queryItContextHook(ctx, query, args...)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return sqlRowScanner{rows: rows}, nil
}

// Compact context renders only type and title. Keep full observations for all
// other callers, including the public pinned-observation API.
func contextObservationColumns(compact bool) string {
	if compact {
		return "type, title"
	}
	return observationSelectColumns
}

// db is the caller's existing connection or history transaction; selecting a
// smaller projection must not move recent membership outside that snapshot.
func (s *Store) queryContextObservations(ctx context.Context, db contextSectionQueryer, query string, args []any, compact bool) ([]Observation, error) {
	rows, err := s.queryContextSection(ctx, db, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var results []Observation
	for rows.Next() {
		var obs Observation
		if compact {
			err = rows.Scan(&obs.Type, &obs.Title)
		} else {
			err = scanObservationRow(rows, &obs)
		}
		if err != nil {
			return nil, err
		}
		results = append(results, obs)
	}
	return results, rows.Err()
}
