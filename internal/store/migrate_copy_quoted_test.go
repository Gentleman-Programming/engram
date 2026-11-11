package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func copyQuotedChunks(t *testing.T, historical bool, space string, mutation func(string) string) (string, *sql.DB) {
	t.Helper()
	var src string
	var db *sql.DB
	var err error
	if historical {
		src, db = copyV360Fixture(t)
	} else {
		src = copyFixture(t)
		db, err = sql.Open("sqlite", filepath.Join(src, "engram.db"))
	}
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = primeConnection(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`INSERT INTO sync_chunks(target_key,chunk_id) VALUES('local','quoted-header-chunk')`); err != nil {
		t.Fatal(err)
	}
	original, err := copyCatalog(db)
	if err != nil {
		t.Fatal(err)
	}
	ddl := strings.Replace(original["sync_chunks"].SQL, "CREATE TABLE sync_chunks (", "CREATE TABLE sync_chunks_rebuilt"+space+"(", 1)
	if ddl == original["sync_chunks"].SQL {
		t.Fatal("unexpected reference header")
	}
	if mutation != nil {
		ddl = mutation(ddl)
	}
	// SQLite itself generates the quoted header; never edit sqlite_schema.
	if _, err = db.Exec(ddl + `; INSERT INTO sync_chunks_rebuilt SELECT * FROM sync_chunks;
DROP TABLE sync_chunks; ALTER TABLE sync_chunks_rebuilt RENAME TO sync_chunks;`); err != nil {
		t.Fatal(err)
	}
	actual, err := copyCatalog(db)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(actual["sync_chunks"].SQL, `CREATE TABLE "sync_chunks"`+space+`(`) {
		t.Fatal("SQLite did not produce quoted header")
	}
	if mutation == nil {
		object := actual["sync_chunks"]
		object.SQL = strings.Replace(object.SQL, `CREATE TABLE "sync_chunks"`+space+`(`, "CREATE TABLE sync_chunks (", 1)
		actual["sync_chunks"] = object
		if !reflect.DeepEqual(actual, original) {
			t.Fatal("rebuild changed more than the table header")
		}
	}
	return src, db
}

func TestMigrateCopySQLiteQuotedHeader(t *testing.T) {
	for _, historical := range []bool{false, true} {
		for _, space := range []string{" ", ""} {
			for _, wal := range []bool{false, true} {
				t.Run(fmt.Sprintf("historical=%v/space=%v/wal=%v", historical, space != "", wal), func(t *testing.T) {
					src, db := copyQuotedChunks(t, historical, space, nil)
					if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
						t.Fatal(err)
					}
					base, err := os.ReadFile(filepath.Join(src, "engram.db"))
					if err != nil {
						t.Fatal(err)
					}
					if wal {
						if _, err = db.Exec(`INSERT INTO sync_chunks(target_key,chunk_id) VALUES('local','wal-only-chunk')`); err != nil {
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
						info, e := os.Stat(filepath.Join(src, "engram.db-wal"))
						if e != nil || info.Size() <= 32 {
							t.Fatalf("missing committed WAL: %v", e)
						}
					}
					before := copySnapshot(t, src)
					dst := filepath.Join(copyTempDir(t), "output")
					result, err := MigrateCopy(src, dst, true)
					if !reflect.DeepEqual(before, copySnapshot(t, src)) {
						t.Fatal("source changed")
					}
					if err != nil || result == nil || !result.MigrationCompleted {
						t.Fatalf("quoted header rejected: %+v %v", result, err)
					}
					for _, name := range []string{".instance-id", "config.json"} {
						source, e := os.ReadFile(filepath.Join(src, name))
						if e != nil {
							t.Fatal(e)
						}
						copied, e := os.ReadFile(filepath.Join(dst, name))
						if e != nil || string(copied) != string(source) {
							t.Fatalf("%s changed: %v", name, e)
						}
					}
					copied, err := sql.Open("sqlite", migrateCopyReadOnlyDSN(filepath.Join(dst, "engram.db"), filepath.Separator))
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = copied.Close() }()
					after := copyLogicalState(t, copied)
					if historical {
						if after["sync_chunk_delete_evidence"] != `{"Columns":["target_key","scope","manifest_version","chunk_id","deleted_ids"],"Rows":null}` {
							t.Fatal("unexpected evidence rows")
						}
						delete(after, "sync_chunk_delete_evidence")
						after = stripObservationsOrgColumn(t, after)
					}
					if !reflect.DeepEqual(state, after) {
						t.Fatal("logical state changed")
					}
				})
			}
		}
	}
}

func TestMigrateCopyQuotedRejectsNearMisses(t *testing.T) {
	for name, change := range map[string]string{
		"default": "DEFAULT 'other'", "literal-whitespace": "DEFAULT 'two  spaces'",
		"primary-key": "PRIMARY KEY (chunk_id, target_key)", "check": "CHECK(length(chunk_id)>0)",
		"missing-index": "DROP INDEX idx_obs_session", "trigger": `DROP TRIGGER obs_fts_insert; CREATE TRIGGER obs_fts_insert AFTER INSERT ON observations BEGIN SELECT 'changed  literal'; END`,
		"unknown-object": "CREATE TABLE unexpected(id TEXT)",
	} {
		t.Run(name, func(t *testing.T) {
			mutation := func(ddl string) string {
				if strings.HasPrefix(change, "DEFAULT") {
					return strings.Replace(ddl, "DEFAULT 'local'", change, 1)
				}
				if strings.HasPrefix(change, "PRIMARY") {
					return strings.Replace(ddl, "PRIMARY KEY (target_key, chunk_id)", change, 1)
				}
				if strings.HasPrefix(change, "CHECK") {
					return strings.TrimSuffix(ddl, ")") + ", " + change + ")"
				}
				return ddl
			}
			src, db := copyQuotedChunks(t, false, "", mutation)
			if name == "missing-index" || name == "trigger" || name == "unknown-object" {
				if _, err := db.Exec(change); err != nil {
					t.Fatal(err)
				}
			}
			catalog, err := copyCatalog(db)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			before := copySnapshot(t, src)
			dst := filepath.Join(copyTempDir(t), "output")
			result, err := MigrateCopy(src, dst, true)
			if err == nil || result != nil {
				t.Fatalf("near miss admitted: %+v %v", result, err)
			}
			if !reflect.DeepEqual(before, copySnapshot(t, src)) {
				t.Fatal("source changed")
			}
			copied, err := sql.Open("sqlite", migrateCopyReadOnlyDSN(filepath.Join(dst, "engram.db"), filepath.Separator))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = copied.Close() }()
			retained, err := copyCatalog(copied)
			if err != nil || !reflect.DeepEqual(catalog, retained) {
				t.Fatal("failed output repaired")
			}
		})
	}
}

func TestCopyNormalizeQuotedHeaderScope(t *testing.T) {
	const body = `(id TEXT DEFAULT 'CREATE TABLE "sync_chunks" (two  spaces)',PRIMARY KEY(id))`
	expected := copyNormalizeDDL("CREATE TABLE sync_chunks " + body)
	for _, space := range []string{"", " "} {
		if got := copyNormalizeDDL(`CREATE TABLE "sync_chunks"` + space + body); got != expected {
			t.Fatalf("header not equivalent: %s", got)
		}
	}
	for _, header := range []string{`CREATE TABLE "other" `, `CREATE TABLE main."sync_chunks" `, `CREATE TABLE "sync_chunks" extra `, `CREATE TABLE "sync_chunks""suffix" `} {
		if got := copyNormalizeDDL(header + body); !strings.HasPrefix(got, header) || !strings.Contains(got, `'CREATE TABLE "sync_chunks" (two  spaces)'`) {
			t.Fatalf("unrelated syntax/literal changed: %s", got)
		}
	}
	for _, constraint := range []string{`PRIMARY KEY(chunk_id,target_key)`, `UNIQUE(chunk_id)`, `FOREIGN KEY(target_key) REFERENCES sessions(id)`, `CHECK(length(chunk_id)>0)`} {
		ddl := `CREATE TABLE "sync_chunks" (target_key TEXT DEFAULT 'two  spaces',chunk_id TEXT,` + constraint + `)`
		got := copyNormalizeDDL(ddl)
		if !strings.Contains(got, constraint) || !strings.Contains(got, `DEFAULT 'two  spaces'`) {
			t.Fatal("body semantics changed")
		}
	}
}
