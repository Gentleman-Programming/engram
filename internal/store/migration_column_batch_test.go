package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestMigrationColumnBatchDefaultsOrderAndRetry(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`CREATE TABLE column_batch(id INTEGER PRIMARY KEY, kept TEXT); INSERT INTO column_batch VALUES(1,'original')`); err != nil {
		t.Fatal(err)
	}
	columns := []migrationColumn{{"kept", "TEXT"}, {"first", "TEXT DEFAULT 'ok'"}, {"bad", "TEXT NOT NULL"}, {"never", "TEXT"}}
	if err := s.addColumnsIfNotExists("column_batch", columns...); err == nil {
		t.Fatal("expected missing-default failure on populated table")
	}
	var first string
	if err := s.db.QueryRow("SELECT first FROM column_batch WHERE id=1").Scan(&first); err != nil || first != "ok" {
		t.Fatalf("first ALTER was not preserved: %q/%v", first, err)
	}
	var later int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('column_batch') WHERE name IN ('bad','never')`).Scan(&later); err != nil || later != 0 {
		t.Fatalf("later ALTER ran: %d/%v", later, err)
	}
	columns[2].definition = "TEXT NOT NULL DEFAULT 'fixed'"
	columns = append(columns, migrationColumn{"first", "TEXT"})
	if err := s.addColumnsIfNotExists("column_batch", columns...); err != nil {
		t.Fatal(err)
	}
	if err := s.addColumnsIfNotExists("column_batch", columns...); err != nil {
		t.Fatalf("repeat batch: %v", err)
	}
	var kept, bad string
	if err := s.db.QueryRow("SELECT kept,bad FROM column_batch WHERE id=1").Scan(&kept, &bad); err != nil || kept != "original" || bad != "fixed" {
		t.Fatalf("data/defaults=%q/%q err=%v", kept, bad, err)
	}
	rows, err := s.db.Query("PRAGMA table_info(column_batch)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"id", "kept", "first", "bad", "never"}) {
		t.Fatalf("column order=%v", names)
	}
}

func TestMigrationColumnBatchRefreshesAfterRebuild(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec("CREATE TABLE column_batch(id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := s.addColumnsIfNotExists("column_batch", migrationColumn{"added", "TEXT"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP TABLE column_batch; CREATE TABLE column_batch(id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := s.addColumnsIfNotExists("column_batch", migrationColumn{"added", "TEXT"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('column_batch') WHERE name='added'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("batch reused stale columns: n=%d err=%v", n, err)
	}
	s.hooks.queryIt = func(queryer, string, ...any) (rowScanner, error) {
		t.Fatal("empty batch queried schema")
		return nil, nil
	}
	if err := s.addColumnsIfNotExists("column_batch"); err != nil {
		t.Fatal(err)
	}
}

type migrationBatchFaultRows struct {
	rowScanner
	kind string
	err  error
}

func (r migrationBatchFaultRows) Scan(dest ...any) error {
	if r.kind == "scan" {
		return r.err
	}
	return r.rowScanner.Scan(dest...)
}
func (r migrationBatchFaultRows) Err() error {
	if r.kind == "rows" {
		return r.err
	}
	return r.rowScanner.Err()
}
func (r migrationBatchFaultRows) Close() error {
	err := r.rowScanner.Close()
	if r.kind == "close" {
		return r.err
	}
	return err
}

func TestMigrationColumnBatchReadErrorsDoNotAlter(t *testing.T) {
	for _, kind := range []string{"query", "scan", "rows", "close"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestStore(t)
			if _, err := s.db.Exec("CREATE TABLE column_batch(id INTEGER)"); err != nil {
				t.Fatal(err)
			}
			original := s.hooks.queryIt
			sentinel := errors.New("column batch " + kind)
			s.hooks.queryIt = func(db queryer, q string, args ...any) (rowScanner, error) {
				if kind == "query" {
					return nil, sentinel
				}
				rows, err := original(db, q, args...)
				if err != nil {
					return nil, err
				}
				return migrationBatchFaultRows{rows, kind, sentinel}, nil
			}
			err := s.addColumnsIfNotExists("column_batch", migrationColumn{"added", "TEXT"})
			if !errors.Is(err, sentinel) {
				t.Fatalf("got%v want%v", err, sentinel)
			}
			s.hooks.queryIt = original
			var n int
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('column_batch') WHERE name='added'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("schema altered after read error: n=%d err=%v", n, err)
			}
		})
	}
}

func TestStartupSchemaBatchesReadMetadataOnce(t *testing.T) {
	s := newTestStore(t)
	original := s.hooks.queryIt
	calls := map[string]int{}
	s.hooks.queryIt = func(db queryer, q string, args ...any) (rowScanner, error) {
		if strings.HasPrefix(q, "PRAGMA table_info(") {
			calls[q]++
		}
		return original(db, q, args...)
	}
	if err := s.runStartupMigrations(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"observations", "sessions", "user_prompts", "prompt_tombstones", "sync_delete_tombstones", "sync_mutations", "sync_state", "sync_chunks", "sync_apply_deferred"} {
		want := 1
		if table == "observations" {
			want = 3
		} // Two column stages plus legacy-table inspection.
		query := fmt.Sprintf("PRAGMA table_info(%s)", table)
		if calls[query] != want {
			t.Errorf("%s: got%d queries want%d", table, calls[query], want)
		}
	}
}
