package store

import (
	"errors"
	"testing"
)

type statsTestRows struct {
	next       bool
	count      int
	project    string
	scanErr    error
	iterateErr error
	closeErr   error
}

func (r *statsTestRows) Next() bool {
	if r.next {
		r.next = false
		return true
	}
	return false
}

func (r *statsTestRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	switch value := dest[0].(type) {
	case *int:
		*value = r.count
	case *string:
		*value = r.project
	}
	return nil
}

func (r *statsTestRows) Err() error   { return r.iterateErr }
func (r *statsTestRows) Close() error { return r.closeErr }

func statsCountRows() rowScanner { return &statsTestRows{next: true, count: 1} }

func TestStatsNeverReturnsPartialResultsOnQueryErrors(t *testing.T) {
	queryErr := errors.New("query failed")
	for _, stat := range []struct {
		name string
		run  func(*Store) (*Stats, error)
	}{
		{name: "all projects", run: func(s *Store) (*Stats, error) { return s.Stats() }},
		{name: "project", run: func(s *Store) (*Stats, error) { return s.StatsProject("engram") }},
	} {
		t.Run(stat.name, func(t *testing.T) {
			s := newTestStore(t)
			s.hooks.queryIt = func(queryer, string, ...any) (rowScanner, error) {
				return nil, queryErr
			}
			stats, err := stat.run(s)
			if stats != nil || !errors.Is(err, queryErr) {
				t.Fatalf("Stats() = (%+v, %v), want nil and query error", stats, err)
			}
		})
	}
}

func TestStatsPropagatesCountAndProjectRowErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		rows []rowScanner
	}{
		{
			name: "count scan",
			rows: []rowScanner{&statsTestRows{next: true, scanErr: errors.New("count scan failed")}},
		},
		{
			name: "count close",
			rows: []rowScanner{&statsTestRows{next: true, count: 1, closeErr: errors.New("count close failed")}},
		},
		{
			name: "project scan",
			rows: []rowScanner{statsCountRows(), statsCountRows(), statsCountRows(), &statsTestRows{next: true, scanErr: errors.New("project scan failed")}},
		},
		{
			name: "project iteration",
			rows: []rowScanner{statsCountRows(), statsCountRows(), statsCountRows(), &statsTestRows{iterateErr: errors.New("project iteration failed")}},
		},
		{
			name: "project close",
			rows: []rowScanner{statsCountRows(), statsCountRows(), statsCountRows(), &statsTestRows{closeErr: errors.New("project close failed")}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newTestStore(t)
			rows := append([]rowScanner(nil), test.rows...)
			s.hooks.queryIt = func(queryer, string, ...any) (rowScanner, error) {
				row := rows[0]
				rows = rows[1:]
				return row, nil
			}
			stats, err := s.Stats()
			if stats != nil || err == nil {
				t.Fatalf("Stats() = (%+v, %v), want nil and row error", stats, err)
			}
		})
	}
}

func TestStatsPropagatesProjectQueryError(t *testing.T) {
	s := newTestStore(t)
	queryErr := errors.New("project query failed")
	queries := 0
	s.hooks.queryIt = func(queryer, string, ...any) (rowScanner, error) {
		queries++
		if queries == 4 {
			return nil, queryErr
		}
		return statsCountRows(), nil
	}
	stats, err := s.StatsProject("engram")
	if stats != nil || !errors.Is(err, queryErr) {
		t.Fatalf("StatsProject() = (%+v, %v), want nil and project query error", stats, err)
	}
}

func TestStatsPropagatesStickyGenerationError(t *testing.T) {
	s := newTestStore(t)
	s.generation.changed = true
	stats, err := s.Stats()
	if stats != nil || !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("Stats() = (%+v, %v), want nil and ErrDatabaseGenerationChanged", stats, err)
	}
}
