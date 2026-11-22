package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func copyDowngradedFTSFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	return copyHistoricalCatalogFixture(t, "testdata/migrate_copy_downgraded_340_catalog.json", "21f4d581f6b8b1a1c4ce8646546143e0af261684769fb09368e4eca782752ade")
}

func TestMigrateCopyDowngradedFTSCatalog(t *testing.T) {
	for _, name := range []string{"checkpointed", "committed WAL"} {
		t.Run(name, func(t *testing.T) {
			src, db := copyDowngradedFTSFixture(t)
			if _, err := db.Exec(`INSERT INTO observations(sync_id,session_id,type,title,content,project,scope)
				VALUES('fts-live','historical-session','note','configuración','live fixture','fixture','project');
				PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
				t.Fatal(err)
			}
			checkpoint, err := os.ReadFile(filepath.Join(src, "engram.db"))
			if err != nil {
				t.Fatal(err)
			}
			if name == "committed WAL" {
				if _, err = db.Exec("INSERT INTO sync_state(target_key,last_error) VALUES('wal-only','preserve committed journal state')"); err != nil {
					t.Fatal(err)
				}
			}
			logical := copyLogicalState(t, db)
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			if name == "committed WAL" {
				if err := os.WriteFile(filepath.Join(src, "engram.db"), checkpoint, 0600); err != nil {
					t.Fatal(err)
				}
				wal, err := os.Stat(filepath.Join(src, "engram.db-wal"))
				if err != nil || wal.Size() <= 32 {
					t.Fatalf("missing committed WAL: %v", err)
				}
			}

			before := copySnapshot(t, src)
			out := filepath.Join(copyTempDir(t), "output")
			result, err := MigrateCopy(src, out, true)
			if err != nil {
				t.Fatal(err)
			}
			if !result.MigrationCompleted || result.InstanceID != "0123456789abcdef0123456789abcdef" {
				t.Fatalf("unexpected migration result: %+v", result)
			}
			if !reflect.DeepEqual(copySnapshot(t, src), before) {
				t.Fatal("migration changed source")
			}
			config, err := os.ReadFile(filepath.Join(out, "config.json"))
			if err != nil || string(config) != `{"historical":"preserve"}` {
				t.Fatalf("copied config = %q, error %v", config, err)
			}

			migrated, err := sql.Open("sqlite", filepath.Join(out, "engram.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = migrated.Close() })
			if !reflect.DeepEqual(copyLogicalState(t, migrated), logical) {
				t.Fatal("migration changed ordinary table contents")
			}
			for _, query := range []string{
				"SELECT count(*) FROM observations_fts WHERE observations_fts MATCH 'configuracion'",
				"SELECT count(*) FROM prompts_fts WHERE prompts_fts MATCH 'cafe'",
			} {
				var count int
				if err := migrated.QueryRow(query).Scan(&count); err != nil || count != 1 {
					t.Fatalf("rebuilt search count = %d, error %v (%s)", count, err, query)
				}
			}
			if _, err := migrated.Exec("UPDATE observations SET title='terminación' WHERE sync_id='fts-live'"); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := migrated.QueryRow("SELECT count(*) FROM observations_fts WHERE observations_fts MATCH 'terminacion'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("FTS update trigger count = %d, error %v", count, err)
			}
		})
	}
}

func TestMigrateCopyDowngradedFTSRejectsNearMisses(t *testing.T) {
	for _, tc := range []struct{ name, change string }{
		{"observations-only downgrade", "DROP TABLE prompts_fts;" + promptsFTSDefinition},
		{"prompts-only downgrade", "DROP TABLE observations_fts;" + observationsFTSDefinition},
		{"unexpected index", "CREATE INDEX unexpected_fixture_index ON observations(title)"},
		{"changed trigger", "DROP TRIGGER obs_fts_insert; CREATE TRIGGER obs_fts_insert AFTER INSERT ON observations BEGIN SELECT 1; END"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, db := copyDowngradedFTSFixture(t)
			if _, err := db.Exec(tc.change); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := copySnapshot(t, src)
			res, err := MigrateCopy(src, filepath.Join(copyTempDir(t), "output"), true)
			if res != nil || err == nil || !strings.Contains(err.Error(), "unsupported database catalog") {
				t.Fatalf("migration = (%+v, %v), want catalog rejection", res, err)
			}
			if !reflect.DeepEqual(copySnapshot(t, src), before) {
				t.Fatal("rejected migration changed source")
			}
		})
	}
}
