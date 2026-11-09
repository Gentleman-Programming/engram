package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

func copyV340Fixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	return copyHistoricalCatalogFixture(t, "testdata/migrate_copy_v3.4.0_catalog.json", "56f7bd60f1a2ef4b82076dfbe825f1b4ab1ad74b375578aeedf2674026d0d562")
}

func TestMigrateCopyV340Admission(t *testing.T) {
	src, db := copyV340Fixture(t)
	catalog, err := copyCatalog(db)
	if err != nil {
		t.Fatal(err)
	}
	for name, object := range catalog {
		object.SQL = copyNormalizeDDL(object.SQL)
		catalog[name] = object
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "01b3bb3b2c114bd2ddf0ddb7ccdc40b664afb2ef4490de583739f414b3e07037" {
		t.Fatalf("historical normalized catalog changed: %s", got)
	}
	if len(catalog) != 83 {
		t.Fatal("historical catalog must retain all 83 objects")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := MigrateCopy(src, filepath.Join(copyTempDir(t), "output"), true)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.MigrationCompleted {
		t.Fatalf("migration not completed: %+v", result)
	}
}

// Assert the exact additive column delta before comparing every historical row.
func copyV340OriginalColumns(t *testing.T, state map[string]string) {
	t.Helper()
	for table, added := range map[string][]string{
		"observations":           {"org", "superseded_by_sync_id"},
		"sync_delete_tombstones": {"superseded_by_sync_id"},
	} {
		var record struct{ Columns, Rows []string }
		if err := json.Unmarshal([]byte(state[table]), &record); err != nil {
			t.Fatal(err)
		}
		removed := 0
		for i := len(record.Columns) - 1; i >= 0; i-- {
			if !slices.Contains(added, record.Columns[i]) {
				continue
			}
			for j, row := range record.Rows {
				var values []json.RawMessage
				if err := json.Unmarshal([]byte(row), &values); err != nil {
					t.Fatal(err)
				}
				if i >= len(values) || string(values[i]) != "null" {
					t.Fatalf("%s.%s must be added as NULL: %s", table, record.Columns[i], row)
				}
				values = append(values[:i], values[i+1:]...)
				encoded, err := json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				record.Rows[j] = string(encoded)
			}
			record.Columns = append(record.Columns[:i], record.Columns[i+1:]...)
			removed++
		}
		if removed != len(added) {
			t.Fatalf("missing additive columns in %s", table)
		}
		sort.Strings(record.Rows)
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		state[table] = string(encoded)
	}
}

func TestMigrateCopyV340CompleteState(t *testing.T) {
	for _, wal := range []bool{false, true} {
		t.Run(fmt.Sprintf("wal=%v", wal), func(t *testing.T) {
			src, db := copyV340Fixture(t)
			if _, err := db.Exec(`
INSERT INTO observation_versions(observation_id,title,content,version) SELECT id,'old title','old revision',1 FROM observations;
INSERT INTO observation_save_operations(operation_id,fingerprint,observation_id) SELECT 'historical-operation','opaque-fingerprint',id FROM observations;
INSERT INTO memory_relations(sync_id,source_id,target_id,relation,reason,judgment_status) VALUES('historical-relation','obs-historical','obs-historical','related','preserve reasoning','judged');
INSERT INTO sync_enrolled_projects(project) VALUES('fixture');
INSERT INTO sync_chunks(target_key,chunk_id) VALUES('historical-hidden','opaque-old-chunk');
INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,project) VALUES('historical-hidden','session','historical-session','upsert','{"id":"historical-session","project":"fixture"}','fixture');
INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,project,acked_at) VALUES('historical-hidden','session','acked-session','upsert','{"id":"acked-session","project":"fixture"}','fixture','2026-01-02');
INSERT INTO sync_delete_tombstones(entity,entity_key,session_id,project,deleted_at,hard_delete) VALUES('observation','old-delete','historical-session','fixture','2026-01-02',1);
INSERT INTO prompt_tombstones(sync_id,session_id,project) VALUES('old-prompt-delete','historical-session','fixture');
PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
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
				info, err := os.Stat(filepath.Join(src, "engram.db-wal"))
				if err != nil || info.Size() <= 32 {
					t.Fatalf("missing committed WAL: %v", err)
				}
			}
			before := copySnapshot(t, src)
			dst := filepath.Join(copyTempDir(t), "output")
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
			after := copyLogicalState(t, migrated)
			for _, table := range []string{"project_aliases", "capture_project_provenance", "session_finalizations", "sync_chunk_delete_evidence"} {
				var record struct{ Columns, Rows []string }
				if err := json.Unmarshal([]byte(after[table]), &record); err != nil {
					t.Fatal(err)
				}
				if len(record.Rows) != 0 {
					t.Fatalf("unexpected new rows in %s: %s", table, after[table])
				}
				delete(after, table)
			}
			copyV340OriginalColumns(t, after)
			if !reflect.DeepEqual(state, after) {
				t.Fatalf("logical state changed during migration\nbefore: %v\nafter: %v", state, after)
			}
			var count int
			if err = migrated.QueryRow(`SELECT count(*) FROM prompts_fts WHERE prompts_fts MATCH 'cafe'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("accent-insensitive FTS not migrated: %d %v", count, err)
			}
		})
	}
}

func TestMigrateCopyV340RejectsNearMisses(t *testing.T) {
	for name, ddl := range map[string]string{
		"one-index":      `CREATE INDEX idx_sessions_project_lower ON sessions(LOWER(project))`,
		"changed-index":  `DROP INDEX idx_obs_session; CREATE INDEX idx_obs_session ON observations(project)`,
		"extra-table":    `CREATE TABLE unexpected(id TEXT)`,
		"missing-index":  `DROP INDEX idx_obs_session`,
		"extra-column":   `ALTER TABLE observations ADD COLUMN unexpected TEXT`,
		"duplicate-name": `CREATE TRIGGER idx_obs_session AFTER INSERT ON sessions BEGIN SELECT 1; END`,
	} {
		t.Run(name, func(t *testing.T) {
			src, db := copyV340Fixture(t)
			if _, err := db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := copySnapshot(t, src)
			result, err := MigrateCopy(src, filepath.Join(copyTempDir(t), "output"), true)
			if err == nil || result != nil || !strings.Contains(err.Error(), "unsupported database catalog") {
				t.Fatalf("near-miss admitted: %+v %v", result, err)
			}
			if !reflect.DeepEqual(before, copySnapshot(t, src)) {
				t.Fatal("rejected source changed")
			}
		})
	}
}
