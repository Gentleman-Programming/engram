package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type quarantineCountingRows struct {
	rowScanner
	count *int
}

func (r *quarantineCountingRows) Next() bool {
	ok := r.rowScanner.Next()
	if ok {
		*r.count++
	}
	return ok
}

func TestQuarantinePagesBoundLegacyBacklogAndReleaseWriter(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetSyncState("other"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`WITH RECURSIVE ids(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM ids WHERE n<1000)
		INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project)
		SELECT 'cloud','session',printf('legacy-%d',n),'upsert',json_object('id',printf('legacy-%d',n),'project','legacy','directory','/legacy','started_at','2026-10-01 00:00:00'),'local','legacy' FROM ids`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','session','bad','upsert','{}','local',' legacy '),('other','session','other-bad','upsert','{}','local',' legacy ')`); err != nil {
		t.Fatal(err)
	}
	count := 0
	original := s.hooks.queryIt
	s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
		rows, err := original(db, query, args...)
		if err == nil && strings.Contains(query, "FROM sync_mutations WHERE target_key = ? AND acked_at IS NULL AND disposition = 'pending'") {
			return &quarantineCountingRows{rowScanner: rows, count: &count}, nil
		}
		return rows, err
	}
	other, err := sql.Open("sqlite", filepath.Join(s.cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	started := time.Now()
	var cursor int64
	actions := 0
	for page := 0; page < 11; page++ {
		before := count
		report, next, err := s.QuarantineIrreparableSyncMutationsPage(DefaultSyncTargetKey, cursor, 10000)
		if err != nil {
			t.Fatal(err)
		}
		if count-before > 100 {
			t.Fatalf("page scanned %d rows", count-before)
		}
		actions += len(report.Actions)
		if page < 10 && next <= cursor {
			t.Fatalf("cursor did not advance: %d -> %d", cursor, next)
		}
		cursor = next
		if _, err := other.Exec("BEGIN IMMEDIATE"); err != nil {
			t.Fatalf("writer blocked between pages: %v", err)
		}
		if _, err := other.Exec("ROLLBACK"); err != nil {
			t.Fatal(err)
		}
	}
	if cursor != 0 || actions != 1 || count != 1001 {
		t.Fatalf("cursor=%d actions=%d scanned=%d", cursor, actions, count)
	}
	if got := scalarInt(t, s, `SELECT COUNT(*) FROM sync_mutations WHERE project='legacy' AND disposition='pending' AND acked_at IS NULL`); got != 1000 {
		t.Fatalf("valid backlog changed: %d", got)
	}
	if got := scalarInt(t, s, `SELECT COUNT(*) FROM sync_mutations WHERE target_key='other' AND disposition='pending'`); got != 1 {
		t.Fatalf("other target changed: %d", got)
	}
	t.Logf("1001 rows across 11 bounded transactions: %s; writer admitted between every page", time.Since(started))
}
