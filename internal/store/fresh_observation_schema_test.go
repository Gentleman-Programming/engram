package store

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Close the real result before inspecting schema_version on the one-connection DB.
type observationSchemaRows struct {
	rowScanner
	afterClose func()
	closed     bool
}

func (r *observationSchemaRows) Close() error {
	err := r.rowScanner.Close()
	if !r.closed && err == nil {
		r.closed = true
		r.afterClose()
	}
	return err
}

type observationSchemaColumn struct {
	ID      int
	Name    string
	Type    string
	NotNull int
	Default sql.NullString
	Primary int
}

func observationSchemaMetadata(t *testing.T, db *sql.DB) []observationSchemaColumn {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(observations)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var result []observationSchemaColumn
	for rows.Next() {
		var c observationSchemaColumn
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.NotNull, &c.Default, &c.Primary); err != nil {
			t.Fatal(err)
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestObservationSchemaInitialColumnBatch(t *testing.T) {
	columns := []struct{ name, kind string }{
		{"review_after", "TEXT"}, {"superseded_by_sync_id", "TEXT"},
		{"expires_at", "TEXT"}, {"embedding", "BLOB"},
		{"embedding_model", "TEXT"}, {"embedding_created_at", "TEXT"},
	}
	for _, route := range []string{"fresh", "legacy", "partial"} {
		t.Run(route, func(t *testing.T) {
			cfg := FallbackConfig(t.TempDir())
			db, err := sql.Open("sqlite", storeDSN(filepath.Join(cfg.DataDir, "engram.db")))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			db.SetMaxOpenConns(1)
			if route != "fresh" {
				if _, err := db.Exec(legacyDDLPreMemoryConflictSurfacing); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("INSERT INTO sessions(id,project,directory) VALUES('legacy','engram','/synthetic'); INSERT INTO observations(sync_id,session_id,type,title,content,project) VALUES('legacy-observation','legacy','decision','kept','café preserved','engram')"); err != nil {
					t.Fatal(err)
				}
			}
			if route == "partial" {
				if _, err := db.Exec("ALTER TABLE observations ADD COLUMN review_after TEXT; ALTER TABLE observations ADD COLUMN embedding BLOB"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE observations SET review_after=?, embedding=?", "2030-01-01", []byte("opaque-vector")); err != nil {
					t.Fatal(err)
				}
			}
			s := &Store{db: db, cfg: cfg, hooks: defaultStoreHooks()}
			version := func() int {
				t.Helper()
				var n int
				if err := db.QueryRow("PRAGMA schema_version").Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			lastInspection := -1
			var additions []int
			query := s.hooks.queryIt
			s.hooks.queryIt = func(db queryer, q string, args ...any) (rowScanner, error) {
				rows, err := query(db, q, args...)
				if err == nil && q == "PRAGMA table_info(observations)" {
					return &observationSchemaRows{rowScanner: rows, afterClose: func() { lastInspection = version() }}, nil
				}
				return rows, err
			}
			exec := s.hooks.exec
			s.hooks.exec = func(db execer, q string, args ...any) (sql.Result, error) {
				// The six-column batch is between its metadata read and this next DDL.
				if strings.Contains(q, "CREATE TABLE IF NOT EXISTS memory_relations (") {
					if lastInspection < 0 {
						t.Fatal("column batch was not inspected")
					}
					additions = append(additions, version()-lastInspection)
				}
				return exec(db, q, args...)
			}
			if err := s.migrate(); err != nil {
				t.Fatal(err)
			}
			want := map[string]int{"fresh": 0, "legacy": 6, "partial": 4}[route]
			if !reflect.DeepEqual(additions, []int{want}) {
				t.Errorf("column ALTER count=%v, want [%d]", additions, want)
			}
			before := observationSchemaMetadata(t, db)
			deletedID := -1
			found := map[string]observationSchemaColumn{}
			for _, c := range before {
				found[c.Name] = c
				if c.Name == "deleted_at" {
					deletedID = c.ID
				}
			}
			for i, expected := range columns {
				c, ok := found[expected.name]
				if !ok || c.Type != expected.kind || c.NotNull != 0 || c.Default.Valid || c.Primary != 0 {
					t.Errorf("unexpected column %s: %+v", expected.name, c)
				}
				if route == "fresh" && (deletedID < 0 || c.ID != deletedID+i+1) {
					t.Errorf("fresh column order %s: %+v after deleted_at=%d", expected.name, c, deletedID)
				}
			}
			if route != "fresh" {
				var title, content string
				var review, superseded, expires, model, created sql.NullString
				var embedding []byte
				if err := db.QueryRow("SELECT title,content,review_after,superseded_by_sync_id,expires_at,embedding,embedding_model,embedding_created_at FROM observations WHERE sync_id='legacy-observation'").Scan(&title, &content, &review, &superseded, &expires, &embedding, &model, &created); err != nil {
					t.Fatal(err)
				}
				if title != "kept" || content != "café preserved" || superseded.Valid || expires.Valid || model.Valid || created.Valid {
					t.Fatal("legacy data/defaults changed")
				}
				if route == "partial" {
					if !review.Valid || review.String != "2030-01-01" || !bytes.Equal(embedding, []byte("opaque-vector")) {
						t.Fatal("partial migration values changed")
					}
				} else if review.Valid || embedding != nil {
					t.Fatal("legacy nullable fields were backfilled")
				}
			}
			assertFTSContract(t, s, "observations_fts", []string{"title", "content", "tool_name", "type", "project", "topic_key"}, "observations")
			lastInspection = -1
			if err := s.migrate(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(additions, []int{want, 0}) {
				t.Errorf("repeat column ALTER count=%v", additions)
			}
			if after := observationSchemaMetadata(t, db); !reflect.DeepEqual(before, after) {
				t.Fatal("repeat migration changed column metadata")
			}
		})
	}
}

func BenchmarkFreshStoreInitialization(b *testing.B) {
	root := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, err := New(FallbackConfig(filepath.Join(root, strconv.Itoa(i))))
		if err != nil {
			b.Fatal(err)
		}
		if err := s.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
