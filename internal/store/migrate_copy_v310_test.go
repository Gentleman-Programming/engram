package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func copyV310Fixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	return copyHistoricalCatalogFixture(t, "testdata/migrate_copy_v3.1.0_catalog.json", "74aa776165ebf3be2de09a0449b9171a45b8524aa05fccad33b11771b621f33f")
}

func TestMigrateCopyV310Admission(t *testing.T) {
	src, db := copyV310Fixture(t)
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
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != "a87cdab7fc92a46ba15f45cf985d09afc45e0f93e34fc39bf01bacd63803e811" {
		t.Fatalf("historical normalized catalog changed: %s", got)
	}
	if len(catalog) != 78 {
		t.Fatal("historical catalog must retain all 78 objects")
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

func TestMigrateCopyV310CompleteState(t *testing.T) {
	for _, wal := range []bool{false, true} {
		t.Run(fmt.Sprintf("wal=%v", wal), func(t *testing.T) {
			src, db := copyV310Fixture(t)
			if _, err := db.Exec(`
INSERT INTO observation_versions(observation_id,title,content,version) SELECT id,'old title','old revision',1 FROM observations;
INSERT INTO memory_relations(sync_id,source_id,target_id,relation,reason,judgment_status) VALUES('historical-relation','obs-historical','obs-historical','related','preserve reasoning','judged');
INSERT INTO sync_enrolled_projects(project) VALUES('fixture');
INSERT INTO sync_chunks(target_key,chunk_id) VALUES('historical-hidden','opaque-old-chunk');
INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,project) VALUES('historical-hidden','session','historical-session','upsert','{"id":"historical-session","project":"fixture"}','fixture');
INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,project,acked_at) VALUES('historical-hidden','session','acked-session','upsert','{"id":"acked-session","project":"fixture"}','fixture','2026-01-02');
INSERT INTO sync_delete_tombstones(entity,entity_key,session_id,project,deleted_at,hard_delete) VALUES('observation','old-delete','historical-session','fixture','2026-01-02',1);
INSERT INTO prompt_tombstones(sync_id,session_id,project) VALUES('old-prompt-delete','historical-session','fixture');
INSERT INTO cloud_upgrade_state(project,snapshot_json) VALUES('fixture','{"captured":true,"project_enrolled":true}');
INSERT INTO prompt_source_confirmations(remote_target,sync_id,session_id,source_inbox_id,prompt_project,kind,asserted_owner_project,remote_attestation_id) VALUES('historical-hidden','prompt-historical','historical-session','synthetic-inbox','fixture','live','fixture',1);
INSERT INTO sync_apply_deferred(sync_id,entity,payload,target_key,remote_seq,entity_key,op,project,scope_class,payload_sync_id) VALUES('deferred-relation','relation','{"synthetic":"deferred"}','historical-hidden',7,'deferred-relation','upsert','fixture','project','deferred-relation');
INSERT INTO sync_delete_tombstone_remote_floors(target_key,entity,entity_key,last_mutation_seq) VALUES('historical-hidden','observation','old-delete',5);
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
			for _, table := range []string{"project_aliases", "capture_project_provenance", "session_finalizations", "sync_chunk_delete_evidence", "observation_save_operations", "review_clear_events"} {
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
			// The complete post-migration catalog must be current, not merely an
			// admitted historical alternative with its data left behind.
			current, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = current.Close() }()
			current.SetMaxOpenConns(1)
			if err = (&Store{db: current, hooks: defaultStoreHooks()}).migrate(); err != nil {
				t.Fatal(err)
			}
			want, err := copyCatalog(current)
			if err != nil {
				t.Fatal(err)
			}
			got, err := copyCatalog(migrated)
			if err != nil {
				t.Fatal(err)
			}
			for _, catalog := range []map[string]copyCatalogObject{want, got} {
				for name, object := range catalog {
					object.SQL = copyNormalizeDDL(object.SQL)
					catalog[name] = object
				}
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatal("destination did not reach the complete current catalog")
			}
			var count int
			if err = migrated.QueryRow(`SELECT count(*) FROM prompts_fts WHERE prompts_fts MATCH 'cafe'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("accent-insensitive FTS not migrated: %d %v", count, err)
			}
		})
	}
}

func TestMigrateCopyV310RejectsNearMisses(t *testing.T) {
	for name, ddl := range map[string]string{
		"one-index":        `CREATE INDEX idx_sessions_project_lower ON sessions(LOWER(project))`,
		"changed-index":    `DROP INDEX idx_obs_session; CREATE INDEX idx_obs_session ON observations(project)`,
		"extra-table":      `CREATE TABLE unexpected(id TEXT)`,
		"missing-index":    `DROP INDEX idx_obs_session`,
		"extra-column":     `ALTER TABLE observations ADD COLUMN unexpected TEXT`,
		"duplicate-name":   `CREATE TRIGGER idx_obs_session AFTER INSERT ON sessions BEGIN SELECT 1; END`,
		"partial-profile":  "",
		"changed-default":  `DROP TABLE sync_chunks; CREATE TABLE sync_chunks(target_key TEXT NOT NULL DEFAULT 'remote',chunk_id TEXT NOT NULL,imported_at TEXT NOT NULL DEFAULT (datetime('now')),PRIMARY KEY(target_key,chunk_id))`,
		"extra-constraint": `DROP TABLE sync_chunks; CREATE TABLE sync_chunks(target_key TEXT NOT NULL DEFAULT 'local',chunk_id TEXT NOT NULL CHECK(length(chunk_id)>0),imported_at TEXT NOT NULL DEFAULT (datetime('now')),PRIMARY KEY(target_key,chunk_id))`,
		"pk-order":         `DROP TABLE sync_chunks; CREATE TABLE sync_chunks(target_key TEXT NOT NULL DEFAULT 'local',chunk_id TEXT NOT NULL,imported_at TEXT NOT NULL DEFAULT (datetime('now')),PRIMARY KEY(chunk_id,target_key))`,
		"trigger-literal":  "",
	} {
		t.Run(name, func(t *testing.T) {
			src, db := copyV310Fixture(t)
			if name == "partial-profile" {
				// Use the exact newer table, not an independently guessed schema.
				raw, err := os.ReadFile("testdata/migrate_copy_v3.4.0_catalog.json")
				if err != nil {
					t.Fatal(err)
				}
				var newer map[string]copyCatalogObject
				if err = json.Unmarshal(raw, &newer); err != nil {
					t.Fatal(err)
				}
				ddl = newer["review_clear_events"].SQL
			}
			if name == "trigger-literal" {
				catalog, err := copyCatalog(db)
				if err != nil {
					t.Fatal(err)
				}
				original := catalog["obs_fts_delete"].SQL
				changed := strings.Replace(original, "BEGIN", "BEGIN SELECT 'unexpected  literal';", 1)
				if changed == original {
					t.Fatal("fixture lacks the expected trigger body")
				}
				ddl = "DROP TRIGGER obs_fts_delete; " + changed
			}
			if _, err := db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			var rejectedCatalog map[string]copyCatalogObject
			if name != "duplicate-name" {
				var err error
				rejectedCatalog, err = copyCatalog(db)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := copySnapshot(t, src)
			dst := filepath.Join(copyTempDir(t), "output")
			result, err := MigrateCopy(src, dst, true)
			if err == nil || result != nil || !strings.Contains(err.Error(), "unsupported database catalog") {
				t.Fatalf("near-miss admitted: %+v %v", result, err)
			}
			if !reflect.DeepEqual(before, copySnapshot(t, src)) {
				t.Fatal("rejected source changed")
			}
			if rejectedCatalog != nil {
				copied, err := sql.Open("sqlite", migrateCopyReadOnlyDSN(filepath.Join(dst, "engram.db"), filepath.Separator))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = copied.Close() }()
				retained, err := copyCatalog(copied)
				if err != nil || !reflect.DeepEqual(rejectedCatalog, retained) {
					t.Fatal("failed output was repaired before rejection")
				}
			}
		})
	}
}
