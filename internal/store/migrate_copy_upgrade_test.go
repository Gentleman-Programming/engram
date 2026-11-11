package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Complete output of unchanged official-tag source upgraded through three
// releases. Provenance is beside the fixture; this is regression evidence,
// never a new production release-profile digest.
func copyUpgradedFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	src, db := copyHistoricalCatalogFixture(t, "testdata/migrate_copy_upgraded_catalog.json", "f15fea5067e274b010bd7a4b41d4d3ea596160b978698771500867fc5c5b2a04")
	if _, err := db.Exec(`INSERT INTO sync_chunks(target_key,chunk_id) VALUES('local','historical-chunk');
INSERT INTO sync_delete_tombstones(entity,entity_key,session_id,project) VALUES('observation','deleted-sync-id','historical-session','fixture');
INSERT INTO prompt_tombstones(sync_id,session_id,project) VALUES('deleted-prompt','historical-session','fixture');`); err != nil {
		t.Fatal(err)
	}
	return src, db
}

func TestMigrateCopyUpgradedCatalog(t *testing.T) {
	src, db := copyUpgradedFixture(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := copySnapshot(t, src)
	dst := filepath.Join(copyTempDir(t), "output")
	result, err := MigrateCopy(src, dst, true)
	if !reflect.DeepEqual(before, copySnapshot(t, src)) {
		t.Fatal("source changed")
	}
	if err != nil || result == nil || !result.MigrationCompleted {
		t.Fatalf("historical upgrade rejected: %+v %v", result, err)
	}
	migrated, err := sql.Open("sqlite", migrateCopyReadOnlyDSN(filepath.Join(dst, "engram.db"), filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migrated.Close() }()
	reference, err := New(FallbackConfig(filepath.Join(copyTempDir(t), "reference")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reference.Close() }()
	actual, err := copyCatalog(migrated)
	if err != nil {
		t.Fatal(err)
	}
	current, err := copyCatalog(reference.db)
	if err != nil {
		t.Fatal(err)
	}
	for name, o := range actual {
		o.SQL = copyNormalizeDDL(o.SQL)
		actual[name] = o
	}
	for name, o := range current {
		o.SQL = copyNormalizeDDL(o.SQL)
		current[name] = o
	}
	if !reflect.DeepEqual(actual, current) {
		t.Fatal("post-migration catalog differs from fresh current")
	}
}

func TestMigrateCopyUpgradedRejectsNearMisses(t *testing.T) {
	for name, ddl := range map[string]string{
		"missing-stable-index": `DROP INDEX idx_obs_session`,
		"changed-index":        `DROP INDEX idx_obs_session; CREATE INDEX idx_obs_session ON observations(project)`,
		"extra-column":         `ALTER TABLE observations ADD COLUMN unexpected TEXT`,
		"extra-object":         `CREATE TABLE unexpected(id TEXT)`,
		"partial-org":          `ALTER TABLE observations ADD COLUMN org TEXT`,
		"changed-constraint":   `PRAGMA writable_schema=ON; UPDATE sqlite_schema SET sql=replace(sql,'project    TEXT,','project TEXT CHECK(length(project)>0),') WHERE name='observations'; PRAGMA writable_schema=OFF`,
		"changed-trigger":      `DROP TRIGGER obs_fts_insert; CREATE TRIGGER obs_fts_insert AFTER INSERT ON observations BEGIN SELECT 'changed  literal'; END`,
		"partial-evidence":     `CREATE TABLE sync_chunk_delete_evidence(target_key TEXT NOT NULL, scope TEXT NOT NULL, manifest_version INTEGER NOT NULL, chunk_id TEXT NOT NULL, deleted_ids TEXT NOT NULL)`,
		"duplicate-name":       `CREATE TRIGGER idx_obs_session AFTER INSERT ON sessions BEGIN SELECT 1; END`,
	} {
		t.Run(name, func(t *testing.T) {
			src, db := copyUpgradedFixture(t)
			beforeCatalog, err := copyCatalog(db)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			afterCatalog, err := copyCatalog(db)
			if name != "duplicate-name" && err != nil {
				t.Fatal(err)
			}
			if err == nil && reflect.DeepEqual(beforeCatalog, afterCatalog) {
				t.Fatal("negative control did not alter catalog")
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			before := copySnapshot(t, src)
			dst := filepath.Join(copyTempDir(t), "output")
			result, err := MigrateCopy(src, dst, true)
			if err == nil || result != nil || !strings.Contains(err.Error(), "unsupported database catalog") {
				t.Fatalf("near miss admitted: %+v %v", result, err)
			}
			if !reflect.DeepEqual(before, copySnapshot(t, src)) {
				t.Fatal("rejected source changed")
			}
			if _, err = os.Stat(dst); err != nil {
				t.Fatal(err)
			}
			if name != "duplicate-name" {
				copied, openErr := sql.Open("sqlite", migrateCopyReadOnlyDSN(filepath.Join(dst, "engram.db"), filepath.Separator))
				if openErr != nil {
					t.Fatal(openErr)
				}
				retained, catalogErr := copyCatalog(copied)
				closeErr := copied.Close()
				if catalogErr != nil || closeErr != nil {
					t.Fatalf("inspect failed output: %v %v", catalogErr, closeErr)
				}
				if !reflect.DeepEqual(afterCatalog, retained) {
					t.Fatal("rejected catalog repaired before rejection")
				}
			}
		})
	}
}
