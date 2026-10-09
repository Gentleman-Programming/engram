package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// The fault is local to this fixture, not a registered/global SQLite driver.
type candidateFallbackConnector struct {
	driver      driver.Driver
	dsn         string
	failures    atomic.Int32
	legacyCalls atomic.Int32
	injected    error
}

func (c *candidateFallbackConnector) Driver() driver.Driver { return c.driver }
func (c *candidateFallbackConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return candidateFallbackConn{Conn: conn, fault: c}, nil
}

type candidateFallbackConn struct {
	driver.Conn
	fault *candidateFallbackConnector
}

func (c candidateFallbackConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	normalized := strings.Join(strings.Fields(query), " ")
	if normalized == "SELECT rowid FROM observations_fts WHERE observations_fts MATCH ?" && c.fault.failures.CompareAndSwap(0, 1) {
		return nil, c.fault.injected
	}
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := q.QueryContext(ctx, query, args)
	if err == nil && strings.Contains(normalized, "FROM observations_fts fts") && strings.Contains(normalized, "fts.rank AS score") {
		c.fault.legacyCalls.Add(1)
	}
	return rows, err
}
func (c candidateFallbackConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := c.Conn.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}
func (c candidateFallbackConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, query)
	}
	return c.Prepare(query)
}
func (c candidateFallbackConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return driver.ErrSkip
}
func (c candidateFallbackConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}
func (c candidateFallbackConn) IsValid() bool {
	v, ok := c.Conn.(driver.Validator)
	return !ok || v.IsValid()
}
func (c candidateFallbackConn) CheckNamedValue(v *driver.NamedValue) error {
	if n, ok := c.Conn.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(v)
	}
	return driver.ErrSkip
}

func TestScanProject_BatchFailurePreservesLegacyCandidates(t *testing.T) {
	for _, apply := range []bool{false, true} {
		name := "dry-run"
		if apply {
			name = "apply"
		}
		t.Run(name, func(t *testing.T) {
			s := setupRelationsStore(t)
			sourceID, sourceSync := addTestObs(t, s, "anchor beacon canyon", "decision", "testproject", "project")
			allowed := map[int64]bool{}
			for _, title := range []string{"anchor", "anchor beacon", "anchor beacon canyon canyon"} {
				id, _ := addTestObs(t, s, title, "decision", "testproject", "project")
				allowed[id] = true
			}
			addTestObs(t, s, "anchor elsewhere", "decision", "otherproject", "project")
			addTestObs(t, s, "anchor private", "decision", "testproject", "personal")
			deleted, _ := addTestObs(t, s, "anchor deleted", "decision", "testproject", "project")
			if err := s.DeleteObservation(deleted, false); err != nil {
				t.Fatal(err)
			}
			opts := CandidateOptions{Project: "testproject", Scope: "project", Limit: 10, SkipInsert: true}
			expected, err := s.FindCandidates(sourceID, opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(expected) != len(allowed) {
				t.Fatalf("expected nonempty filtered legacy candidates: %+v", expected)
			}
			varied := false
			for _, c := range expected {
				if !allowed[c.ID] {
					t.Fatalf("excluded candidate returned: %+v", c)
				}
				if c.Score != expected[0].Score {
					varied = true
				}
			}
			if !varied {
				t.Fatal("fixture must exercise varied BM25 ranks")
			}
			beforeStats, err := s.Stats()
			if err != nil {
				t.Fatal(err)
			}
			beforeObs := map[int64]*Observation{}
			for id := range allowed {
				obs, err := s.GetObservation(id)
				if err != nil {
					t.Fatal(err)
				}
				beforeObs[id] = obs
			}
			obs, err := s.GetObservation(sourceID)
			if err != nil {
				t.Fatal(err)
			}
			beforeObs[sourceID] = obs
			fault := &candidateFallbackConnector{driver: s.db.Driver(), dsn: storeDSN(filepath.Join(s.cfg.DataDir, "engram.db")), injected: errors.New("test-only batch query failure")}
			replacement := sql.OpenDB(fault)
			replacement.SetMaxOpenConns(1)
			if err := replacement.Ping(); err != nil {
				_ = replacement.Close()
				t.Fatal(err)
			}
			old := s.db
			s.db = replacement
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			result, err := s.ScanProject(ScanOptions{Project: "testproject", Limit: 1, Apply: apply})
			if err != nil {
				t.Fatal(err)
			}
			if fault.failures.Load() != 1 || fault.legacyCalls.Load() != 1 {
				t.Fatalf("fault/fallback trace: failures=%d successful legacy calls=%d", fault.failures.Load(), fault.legacyCalls.Load())
			}
			if result.Inspected != 1 || result.RankedQueries != 1 || result.CandidatesFound != len(expected) || result.Capped || result.NextCursor == nil || *result.NextCursor != sourceID || result.DryRun == apply || result.AlreadyRelated != 0 {
				t.Fatalf("fallback counters/pagination: %+v", result)
			}
			relations, err := s.ListRelations(ListRelationsOptions{})
			if err != nil {
				t.Fatal(err)
			}
			wantInserted := 0
			if apply {
				wantInserted = len(expected)
			}
			if result.RelationsInserted != wantInserted || len(relations) != wantInserted {
				t.Fatalf("unexpected inserts: %+v; relations=%+v", result, relations)
			}
			for _, candidate := range expected {
				if !apply {
					break
				}
				found := false
				for _, relation := range relations {
					if relation.SourceID == sourceSync && relation.TargetID == candidate.SyncID && relation.JudgmentStatus == "pending" {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing actual legacy candidate: %+v; relations=%+v", candidate, relations)
				}
			}
			// SkipInsert still returns the exact legacy payload after the one-shot fault.
			got, err := s.FindCandidates(sourceID, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("legacy lookup unhealthy: got %+v, want %+v", got, expected)
			}
			if apply {
				opts.SkipInsert = false
				if _, err := s.FindCandidates(sourceID, opts); err != nil {
					t.Fatal(err)
				}
			}
			count, err := s.CountRelations(ListRelationsOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if count != wantInserted {
				t.Fatalf("dry-run write or duplicate pending insertion: %d", count)
			}
			afterStats, err := s.Stats()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeStats, afterStats) {
				t.Fatalf("observation counters changed: before=%+v after=%+v", beforeStats, afterStats)
			}
			for id, before := range beforeObs {
				after, err := s.GetObservation(id)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("observation %d changed", id)
				}
			}
			if fault.failures.Load() != 1 {
				t.Fatal("batch fault fired more than once")
			}
		})
	}
}
