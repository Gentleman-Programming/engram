package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This is the complete catalog emitted by unchanged v3.6.0 migration source,
// not a current database with selected DDL removed. See the provenance fixture.
func copyV360Fixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	raw, err := os.ReadFile("testdata/migrate_copy_v3.6.0_catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "7eeef253728d505221a2cd38ddd579f4b1e66d4eab50b6433fb0bae756d55535" {
		t.Fatal("historical raw catalog changed")
	}
	var expected map[string]copyCatalogObject
	if err = json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(copyTempDir(t), "source")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	names := make([]string, 0, len(expected))
	for n := range expected {
		names = append(names, n)
	}
	sort.Strings(names)
	// SQLite creates autoindexes, sqlite_sequence and FTS shadow tables itself.
	for _, kind := range []string{"ordinary", "virtual", "index", "trigger"} {
		for _, name := range names {
			o := expected[name]
			if o.SQL == "" || strings.HasPrefix(name, "sqlite_") || strings.HasPrefix(name, "observations_fts_") || strings.HasPrefix(name, "prompts_fts_") {
				continue
			}
			category := o.Kind
			if category == "table" {
				category = "ordinary"
				if strings.HasPrefix(o.SQL, "CREATE VIRTUAL TABLE ") {
					category = "virtual"
				}
			}
			if category == kind {
				if _, err = db.Exec(o.SQL); err != nil {
					t.Fatalf("construct %s: %v", name, err)
				}
			}
		}
	}
	if _, err = db.Exec("PRAGMA user_version=1; PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if err = primeConnection(db); err != nil {
		t.Fatal(err)
	}
	actual, err := copyCatalog(db)
	if err != nil {
		t.Fatal(err)
	}
	for name, o := range expected {
		o.SQL = copySQLWhitespace(o.SQL)
		expected[name] = o
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("reconstructed catalog differs from complete historical migration output")
	}
	if _, err = db.Exec(`INSERT INTO sessions(id,project,ownership_mode,directory,started_at) VALUES('historical-session','fixture','shared','/synthetic','2026-01-01');
 INSERT INTO observations(sync_id,session_id,type,title,content,project,scope,deleted_at) VALUES('obs-historical','historical-session','note','café','configuración preserved','fixture','project','2026-01-02');
 INSERT INTO user_prompts(sync_id,session_id,content,project) VALUES('prompt-historical','historical-session','pregunta café','fixture');
 INSERT INTO sync_state(target_key,last_error) VALUES('historical-hidden','preserve non-exported state');
 INSERT INTO sync_state(target_key,lifecycle) VALUES('cloud','idle'),('cloud:inbox','inbox');`); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, ".instance-id"), []byte("0123456789abcdef0123456789abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"historical":"preserve"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, db
}
func copyLogicalState(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_list WHERE schema='main' AND type='table' AND name<>'sqlite_schema' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	state := map[string]string{}
	for _, name := range names {
		rows, err = db.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var records []string
		for rows.Next() {
			values := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range values {
				ptr[i] = &values[i]
			}
			if err = rows.Scan(ptr...); err != nil {
				t.Fatal(err)
			}
			record, e := json.Marshal(values)
			if e != nil {
				t.Fatal(e)
			}
			records = append(records, string(record))
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(records)
		record, err := json.Marshal(struct{ Columns, Rows []string }{cols, records})
		if err != nil {
			t.Fatal(err)
		}
		state[name] = string(record)
	}
	return state
}
func TestMigrateCopyV360CompleteCatalog(t *testing.T) {
	for _, wal := range []bool{false, true} {
		t.Run(fmt.Sprintf("wal=%v", wal), func(t *testing.T) {
			src, db := copyV360Fixture(t)
			if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				t.Fatal(err)
			}
			base, err := os.ReadFile(filepath.Join(src, "engram.db"))
			if err != nil {
				t.Fatal(err)
			}
			if wal {
				if _, err = db.Exec(`INSERT INTO sync_state(target_key,last_error) VALUES('wal-only','historical WAL state')`); err != nil {
					t.Fatal(err)
				}
			}
			state := copyLogicalState(t, db)
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			if wal {
				if err = os.WriteFile(filepath.Join(src, "engram.db"), base, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := copySnapshot(t, src)
			dst := filepath.Join(copyTempDir(t), "copy")
			result, err := MigrateCopy(src, dst, true)
			if err != nil {
				t.Fatal(err)
			}
			if !result.MigrationCompleted || result.InstanceID != "0123456789abcdef0123456789abcdef" {
				t.Fatalf("unexpected result: %+v", result)
			}
			if !reflect.DeepEqual(before, copySnapshot(t, src)) {
				t.Fatal("historical source mutated")
			}
			config, err := os.ReadFile(filepath.Join(dst, "config.json"))
			if err != nil || string(config) != `{"historical":"preserve"}` {
				t.Fatalf("configuration changed: %s %v", config, err)
			}
			migrated, err := sql.Open("sqlite", migrateCopyReadOnlyDSN(filepath.Join(dst, "engram.db"), filepath.Separator))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = migrated.Close() }()
			if !reflect.DeepEqual(state, copyLogicalState(t, migrated)) {
				t.Fatal("logical state changed during migration")
			}
			for _, name := range []string{"idx_prompts_project_lower", "idx_sessions_project_lower"} {
				var count int
				if err = migrated.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, name).Scan(&count); err != nil || count != 1 {
					t.Fatalf("missing migrated index %s: %v", name, err)
				}
			}
			var count int
			if err = migrated.QueryRow(`SELECT count(*) FROM prompts_fts WHERE prompts_fts MATCH 'cafe'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("accent-insensitive FTS not migrated: %d %v", count, err)
			}
		})
	}
}
func TestMigrateCopyV360RejectsNearMisses(t *testing.T) {
	for _, rule := range []string{"one-index", "both-indexes", "one-new-fts", "both-new-fts", "extra-table", "missing-index", "changed-index", "extra-trigger", "extra-column", "literal-trigger", "duplicate-name", "duplicate-name-reverse", "legacy-mix"} {
		t.Run(rule, func(t *testing.T) {
			src, db := copyV360Fixture(t)
			statements := map[string]string{
				"one-index":    `CREATE INDEX idx_sessions_project_lower ON sessions(LOWER(project))`,
				"both-indexes": `CREATE INDEX idx_sessions_project_lower ON sessions(LOWER(project)); CREATE INDEX idx_prompts_project_lower ON user_prompts(LOWER(project))`,
				"one-new-fts": `DROP TABLE prompts_fts; CREATE VIRTUAL TABLE prompts_fts USING fts5(
		content,
		project,
		tokenize='trigram remove_diacritics 1',
		content='user_prompts',
		content_rowid='id'
	)`,
				"both-new-fts": `DROP TABLE prompts_fts; CREATE VIRTUAL TABLE prompts_fts USING fts5(
		content,
		project,
		tokenize='trigram remove_diacritics 1',
		content='user_prompts',
		content_rowid='id'
	); DROP TABLE observations_fts; CREATE VIRTUAL TABLE observations_fts USING fts5(
		title,
		content,
		tool_name,
		type,
		project,
		topic_key,
		tokenize='trigram remove_diacritics 1',
		content='observations',
		content_rowid='id'
	)`,
				"extra-table":            `CREATE TABLE unexpected(id TEXT)`,
				"missing-index":          `DROP INDEX idx_obs_session`,
				"changed-index":          `DROP INDEX idx_obs_session; CREATE INDEX idx_obs_session ON observations(project)`,
				"extra-trigger":          `CREATE TRIGGER unexpected AFTER INSERT ON sessions BEGIN SELECT 1; END`,
				"extra-column":           `ALTER TABLE sessions ADD COLUMN unexpected TEXT`,
				"literal-trigger":        `DROP TRIGGER obs_fts_delete; CREATE TRIGGER obs_fts_delete BEFORE DELETE ON observations WHEN old.deleted_at IS NULL BEGIN DELETE FROM observations_fts WHERE rowid=old.id AND 'two  spaces'='two spaces'; END`,
				"duplicate-name":         `CREATE TRIGGER idx_obs_session AFTER INSERT ON sessions BEGIN SELECT 1; END; DROP INDEX idx_obs_session; CREATE INDEX idx_obs_session ON observations(session_id)`,
				"duplicate-name-reverse": `CREATE TRIGGER idx_obs_session AFTER INSERT ON sessions BEGIN SELECT 1; END`,
				"legacy-mix":             `DROP TABLE session_finalizations; ALTER TABLE observations DROP COLUMN superseded_by_sync_id; ALTER TABLE sync_delete_tombstones DROP COLUMN superseded_by_sync_id;`,
			}
			if _, err := db.Exec(statements[rule]); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := copySnapshot(t, src)
			dst := filepath.Join(copyTempDir(t), "copy")
			result, err := MigrateCopy(src, dst, true)
			if err == nil || result != nil || !strings.Contains(err.Error(), "unsupported database catalog") {
				t.Fatalf("near-miss admitted: %+v %v", result, err)
			}
			if !reflect.DeepEqual(before, copySnapshot(t, src)) {
				t.Fatal("rejected source changed")
			}
		})
	}
}
