package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type freshSyncSchemaRows struct {
	rowScanner
	afterClose func()
	closed     bool
}

func (r *freshSyncSchemaRows) Close() error {
	err := r.rowScanner.Close()
	if err == nil && !r.closed {
		r.closed = true
		r.afterClose()
	}
	return err
}

func TestFreshSyncColumnBatches(t *testing.T) {
	for _, route := range []string{"fresh", "legacy", "partial"} {
		t.Run(route, func(t *testing.T) {
			cfg := FallbackConfig(t.TempDir())
			db, err := sql.Open("sqlite", storeDSN(filepath.Join(cfg.DataDir, "engram.db")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			db.SetMaxOpenConns(1)
			exec := func(query string) {
				t.Helper()
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if route != "fresh" {
				exec(`CREATE TABLE sync_mutations (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, target_key TEXT NOT NULL,
 entity TEXT NOT NULL, entity_key TEXT NOT NULL, op TEXT NOT NULL,
 payload TEXT NOT NULL, source TEXT NOT NULL DEFAULT 'local',
 occurred_at TEXT NOT NULL DEFAULT (datetime('now')), acked_at TEXT
);
CREATE TABLE sync_delete_tombstones (
 entity TEXT NOT NULL, entity_key TEXT NOT NULL, session_id TEXT,
 project TEXT NOT NULL DEFAULT '', deleted_at TEXT NOT NULL DEFAULT (datetime('now')),
 hard_delete BOOLEAN NOT NULL DEFAULT 1, active BOOLEAN NOT NULL DEFAULT 1,
 last_mutation_seq INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(entity,entity_key)
)`)
				if route == "partial" {
					exec(`ALTER TABLE sync_mutations ADD COLUMN project TEXT NOT NULL DEFAULT '';
ALTER TABLE sync_mutations ADD COLUMN disposition TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE sync_mutations ADD COLUMN disposition_reason TEXT;
ALTER TABLE sync_mutations ADD COLUMN disposition_evidence TEXT;
ALTER TABLE sync_delete_tombstones ADD COLUMN superseded_by_sync_id TEXT`)
				}
			}
			seed := func() {
				t.Helper()
				exec(`INSERT INTO sync_mutations(seq,target_key,entity,entity_key,op,payload,source,occurred_at)
 VALUES(9,'cloud','observation','fixture','delete','{"project":"payload-project"}','fixture','2026-01-01');
INSERT INTO sync_delete_tombstones(entity,entity_key,session_id,project,deleted_at,last_mutation_seq)
 VALUES('observation','fixture','fixture-session','payload-project','2026-01-01',11)`)
				if route == "partial" {
					exec(`UPDATE sync_mutations SET project='kept-project',disposition='quarantined',disposition_reason='kept reason',disposition_evidence='kept evidence';
UPDATE sync_delete_tombstones SET superseded_by_sync_id='kept-replacement'`)
				}
				if route == "fresh" {
					exec(`UPDATE sync_mutations SET project='payload-project';
UPDATE sync_delete_tombstones SET last_remote_mutation_seq=12`)
				}
			}
			if route != "fresh" {
				seed()
			}
			s := &Store{db: db, cfg: cfg, hooks: defaultStoreHooks()}
			version := func(q queryer) int {
				t.Helper()
				rows, err := q.Query("PRAGMA schema_version")
				if err != nil {
					t.Fatal(err)
				}
				var value int
				if !rows.Next() {
					t.Fatal("missing schema version")
				}
				if err := rows.Scan(&value); err != nil {
					t.Fatal(err)
				}
				if err := rows.Close(); err != nil {
					t.Fatal(err)
				}
				return value
			}
			last := map[string]int{}
			additions := map[string][]int{}
			query := s.hooks.queryIt
			s.hooks.queryIt = func(q queryer, sql string, args ...any) (rowScanner, error) {
				table := ""
				if sql == "PRAGMA table_info(sync_state)" {
					table = "sync_mutations"
				} else if strings.Contains(sql, "name GLOB 'obs_fts_*'") {
					// Trigger inspection precedes any FTS rebuild DDL.
					table = "sync_delete_tombstones"
				}
				if table != "" {
					before, ok := last[table]
					if !ok {
						t.Fatalf("missing %s batch inspection", table)
					}
					additions[table] = append(additions[table], version(q)-before)
				}
				rows, err := query(q, sql, args...)
				for _, table := range []string{"sync_mutations", "sync_delete_tombstones"} {
					if err == nil && sql == "PRAGMA table_info("+table+")" {
						return &freshSyncSchemaRows{rowScanner: rows, afterClose: func() { last[table] = version(q) }}, nil
					}
				}
				return rows, err
			}
			if err := s.migrate(); err != nil {
				t.Fatal(err)
			}
			want := map[string]map[string]int{
				"fresh":   {"sync_mutations": 0, "sync_delete_tombstones": 0},
				"legacy":  {"sync_mutations": 5, "sync_delete_tombstones": 2},
				"partial": {"sync_mutations": 1, "sync_delete_tombstones": 1},
			}[route]
			for table, count := range want {
				if !reflect.DeepEqual(additions[table], []int{count}) {
					t.Errorf("%s schema changes=%v, want [%d]", table, additions[table], count)
				}
			}
			if route == "fresh" {
				seed()
			}
			// Omitted columns must keep the same default/NULL behavior as ALTER.
			exec(`INSERT INTO sync_mutations(seq,target_key,entity,entity_key,op,payload,occurred_at)
 VALUES(10,'cloud','observation','default-fixture','delete','{}','2026-01-01');
INSERT INTO sync_delete_tombstones(entity,entity_key,deleted_at) VALUES('session','default-fixture','2026-01-01')`)
			project, disposition := "payload-project", "pending"
			var reason, evidence, replacement any
			var remote any
			if route == "partial" {
				project, disposition = "kept-project", "quarantined"
				reason, evidence, replacement = "kept reason", "kept evidence", "kept-replacement"
			}
			if route == "fresh" {
				remote = int64(12)
			}
			readRow := func(db *sql.DB, query string) map[string]any {
				t.Helper()
				rows, err := db.Query(query)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = rows.Close() }()
				columns, err := rows.Columns()
				if err != nil || !rows.Next() {
					t.Fatalf("read fixture: columns error=%v, row error=%v", err, rows.Err())
				}
				values, dest := make([]any, len(columns)), make([]any, len(columns))
				for i := range values {
					dest[i] = &values[i]
				}
				if err := rows.Scan(dest...); err != nil {
					t.Fatal(err)
				}
				result := map[string]any{}
				for i, name := range columns {
					result[name] = values[i]
				}
				if rows.Next() || rows.Err() != nil {
					t.Fatal("fixture query did not return exactly one row")
				}
				return result
			}
			checks := map[string]map[string]any{
				"SELECT * FROM sync_mutations WHERE seq=9": {
					"seq": int64(9), "target_key": "cloud", "entity": "observation", "entity_key": "fixture", "op": "delete", "payload": `{"project":"payload-project"}`, "source": "fixture", "occurred_at": "2026-01-01", "acked_at": nil,
					"project": project, "disposition": disposition, "disposition_reason": reason, "disposition_evidence": evidence, "disposition_at": nil,
				},
				"SELECT * FROM sync_delete_tombstones WHERE entity='observation' AND entity_key='fixture'": {
					"entity": "observation", "entity_key": "fixture", "session_id": "fixture-session", "project": "payload-project", "deleted_at": "2026-01-01", "hard_delete": int64(1), "active": int64(1), "last_mutation_seq": int64(11), "last_remote_mutation_seq": remote, "superseded_by_sync_id": replacement,
				},
			}
			defaults := map[string]map[string]any{
				"SELECT * FROM sync_mutations WHERE seq=10":                                                    {"project": "", "disposition": "pending", "disposition_reason": nil, "disposition_evidence": nil, "disposition_at": nil},
				"SELECT * FROM sync_delete_tombstones WHERE entity='session' AND entity_key='default-fixture'": {"project": "", "superseded_by_sync_id": nil, "last_remote_mutation_seq": nil},
			}
			checkData := func(db *sql.DB) {
				t.Helper()
				for query, want := range checks {
					if got := readRow(db, query); !reflect.DeepEqual(got, want) {
						t.Errorf("preserved row: got %#v want %#v", got, want)
					}
				}
				for query, fields := range defaults {
					got := readRow(db, query)
					for name, want := range fields {
						if value, ok := got[name]; !ok || !reflect.DeepEqual(value, want) {
							t.Errorf("default %s=%#v, want %#v", name, value, want)
						}
					}
				}
			}
			metadata := func(db *sql.DB, table string) []syncStateSchemaColumn {
				t.Helper()
				rows, err := db.Query("PRAGMA table_info(" + table + ")")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = rows.Close() }()
				var columns []syncStateSchemaColumn
				for rows.Next() {
					var c syncStateSchemaColumn
					if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.NotNull, &c.Default, &c.Primary); err != nil {
						t.Fatal(err)
					}
					columns = append(columns, c)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return columns
			}
			before := map[string][]syncStateSchemaColumn{}
			for _, table := range []string{"sync_mutations", "sync_delete_tombstones"} {
				before[table] = metadata(db, table)
				fields := map[string]syncStateSchemaColumn{}
				for _, c := range before[table] {
					fields[c.Name] = c
				}
				name, previous := "project", "disposition_at"
				if table == "sync_delete_tombstones" {
					name, previous = "superseded_by_sync_id", "last_remote_mutation_seq"
				}
				c, ok := fields[name]
				if !ok || c.Type != "TEXT" || c.Primary != 0 || c.NotNull != map[string]int{"project": 1}[name] || c.Default != (sql.NullString{String: map[string]string{"project": "''"}[name], Valid: name == "project"}) {
					t.Errorf("unexpected metadata for %s.%s: %+v", table, name, c)
				}
				if route == "fresh" && c.ID != fields[previous].ID+1 {
					t.Errorf("fresh column order for %s.%s: %+v", table, name, c)
				}
			}
			checkData(db)
			last = map[string]int{}
			if err := s.migrate(); err != nil {
				t.Fatal(err)
			}
			for table, count := range want {
				if !reflect.DeepEqual(additions[table], []int{count, 0}) {
					t.Errorf("repeat %s schema changes=%v", table, additions[table])
				}
				if !reflect.DeepEqual(before[table], metadata(db, table)) {
					t.Errorf("repeat changed %s metadata", table)
				}
			}
			checkData(db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			checkData(reopened.db)
			for table := range want {
				if !reflect.DeepEqual(before[table], metadata(reopened.db, table)) {
					t.Errorf("reopen changed %s metadata", table)
				}
			}
		})
	}
}
