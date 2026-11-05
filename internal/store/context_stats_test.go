package store

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

type contextStatsRows struct {
	rowScanner
	scans *int
	fault string
	err   error
}

func (r contextStatsRows) Scan(dest ...any) error {
	if r.fault == "scan" {
		return r.err
	}
	err := r.rowScanner.Scan(dest...)
	if err == nil && r.scans != nil {
		*r.scans++
	}
	return err
}
func (r contextStatsRows) Err() error {
	if r.fault == "rows" {
		return r.err
	}
	return r.rowScanner.Err()
}
func (r contextStatsRows) Close() error {
	err := r.rowScanner.Close()
	if r.fault == "close" {
		return r.err
	}
	return err
}

func TestContextStatsHydratesOnlyRequestedNames(t *testing.T) {
	s := contextStatsFixture(t, 100)
	original := s.hooks.queryIt
	scans := 0
	s.hooks.queryIt = func(db queryer, q string, args ...any) (rowScanner, error) {
		r, e := original(db, q, args...)
		if e != nil {
			return nil, e
		}
		return contextStatsRows{rowScanner: r, scans: &scans}, nil
	}
	got, total, err := s.ContextStats("", 8)
	if err != nil {
		t.Fatal(err)
	}
	if scans != 8 || len(got.Projects) != 8 || total != 102 {
		t.Fatalf("scans=%d names=%d total=%d", scans, len(got.Projects), total)
	}
	scans = 0
	full, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if scans != 102 || len(full.Projects) != 102 {
		t.Fatal("public Stats lost complete project list")
	}
}

func TestContextStatsFailures(t *testing.T) {
	for _, kind := range []string{"query", "scan", "rows", "close", "closed", "generation"} {
		t.Run(kind, func(t *testing.T) {
			s := contextStatsFixture(t, 9)
			sentinel := errors.New("summary " + kind)
			switch kind {
			case "closed":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			case "generation":
				s.generation.mu.Lock()
				s.generation.err = ErrDatabaseGenerationChanged
				s.generation.mu.Unlock()
				sentinel = ErrDatabaseGenerationChanged
			default:
				original := s.hooks.queryIt
				s.hooks.queryIt = func(db queryer, q string, args ...any) (rowScanner, error) {
					if kind == "query" {
						return nil, sentinel
					}
					r, e := original(db, q, args...)
					if e != nil {
						return nil, e
					}
					return contextStatsRows{rowScanner: r, fault: kind, err: sentinel}, nil
				}
			}
			stats, total, err := s.ContextStats("", 8)
			if err == nil || stats != nil || total != 0 {
				t.Fatalf("partial/failure stats=%#v total=%d err=%v", stats, total, err)
			}
			if kind != "closed" && !errors.Is(err, sentinel) {
				t.Fatalf("error=%v want%v", err, sentinel)
			}
		})
	}
	s := contextStatsFixture(t, 0)
	for _, limit := range []int{0, -1} {
		if _, _, err := s.ContextStats("", limit); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	if _, _, err := s.ContextStats("  ", 8); err == nil {
		t.Fatal("blank scoped project accepted")
	}
}

type contextStatsReader interface {
	ContextStats(string, int) (*Stats, int, error)
}

func TestContextStatsRecencyOrder(t *testing.T) {
	for _, dates := range []string{"unique", "mixed", "empty"} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%t", dates, reverse), func(t *testing.T) {
				s := contextStatsFixture(t, 20)
				s.db.SetMaxOpenConns(1)
				dateExpr := "printf('2026-01-%02d', id)"
				if dates == "mixed" {
					dateExpr = "CASE WHEN id%3=0 THEN '' WHEN id%3=1 THEN '2026-01-08T00:00:00Z' ELSE '2026-01-09 00:00:00' END"
				}
				if dates == "empty" {
					dateExpr = "''"
				}
				if _, err := s.db.Exec("UPDATE observations SET created_at=" + dateExpr); err != nil {
					t.Fatal(err)
				}
				if reverse {
					if _, err := s.db.Exec("PRAGMA reverse_unordered_selects=ON"); err != nil {
						t.Fatal(err)
					}
				}
				full, err := s.Stats()
				if err != nil {
					t.Fatal(err)
				}
				for _, limit := range []int{1, 8, 50} {
					got, total, e := s.ContextStats("", limit)
					if e != nil {
						t.Fatal(e)
					}
					if total != len(full.Projects) || len(got.Projects) != min(limit, len(full.Projects)) {
						t.Fatalf("limit=%d full=%v bounded=%v total=%d", limit, full.Projects, got.Projects, total)
					}
					if dates == "unique" {
						if !reflect.DeepEqual(got.Projects, full.Projects[:len(got.Projects)]) {
							t.Fatalf("unique-date head differs: full=%v bounded=%v", full.Projects, got.Projects)
						}
					} else {
						assertContextStatsRecencyGroups(t, s, got.Projects, full.Projects)
					}
				}
			})
		}
	}
}

func assertContextStatsRecencyGroups(t *testing.T, s *Store, names, fullNames []string) {
	t.Helper()
	rows, err := s.db.Query(`SELECT project, MAX(created_at) FROM observations
WHERE project IS NOT NULL AND deleted_at IS NULL GROUP BY project`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	latest := make(map[string]string)
	for rows.Next() {
		var name, created string
		if err = rows.Scan(&name, &created); err != nil {
			t.Fatal(err)
		}
		latest[name] = created
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	expected := make([]string, len(fullNames))
	for i, name := range fullNames {
		expected[i] = latest[name]
	}
	sort.Sort(sort.Reverse(sort.StringSlice(expected)))
	actual := make([]string, len(names))
	seen := make(map[string]bool)
	for i, name := range names {
		created, ok := latest[name]
		if !ok || seen[name] {
			t.Fatalf("unknown or duplicated project %q", name)
		}
		seen[name] = true
		actual[i] = created
	}
	if !reflect.DeepEqual(actual, expected[:len(names)]) {
		t.Fatalf("wrong recency groups: got=%v want=%v", actual, expected[:len(names)])
	}
}

func contextStatsFixture(t *testing.T, projects int) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := s.CreateSession("summary", "session-only", ""); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`INSERT INTO user_prompts(session_id,project,content) VALUES('summary','p000','prompt')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < projects; i++ {
		created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).Format("2006-01-02 15:04:05")
		if _, err = tx.Exec(`INSERT INTO observations(session_id,project,type,title,content,created_at) VALUES('summary',?,'decision','title','content',?)`, fmt.Sprintf("p%03d", i), created); err != nil {
			t.Fatal(err)
		}
	}
	if projects > 0 {
		if _, err = tx.Exec(`INSERT INTO observations(session_id,project,type,title,content,created_at,deleted_at) VALUES
('summary',NULL,'decision','null','content','2026-02-01',NULL),
('summary','','decision','empty','content','2026-02-01',NULL),
('summary','P000','decision','legacy','content','2026-02-02',NULL),
('summary','deleted-only','decision','deleted','content','2026-02-01','2026-02-02')`); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestContextStatsBoundedProjects(t *testing.T) {
	for _, n := range []int{0, 1, 8, 9, 100} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := contextStatsFixture(t, n)
			reader, ok := any(s).(contextStatsReader)
			if !ok {
				t.Fatal("store lacks bounded ContextStats with explicit total")
			}
			for _, project := range []string{"", "p000", " P000 ", "missing"} {
				var want *Stats
				var err error
				if project == "" {
					want, err = s.Stats()
				} else {
					want, err = s.StatsProject(project)
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, limit := range []int{1, 8, 200} {
					got, total, e := reader.ContextStats(project, limit)
					if e != nil {
						t.Fatal(e)
					}
					if total != len(want.Projects) {
						t.Fatalf("total=%d want%d", total, len(want.Projects))
					}
					head := *want
					head.Projects = want.Projects[:min(len(want.Projects), limit)]
					if !reflect.DeepEqual(&head, got) {
						t.Fatalf("project=%q limit=%d got%#v want%#v", project, limit, got, &head)
					}
				}
			}
		})
	}
}
