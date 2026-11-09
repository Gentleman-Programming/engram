package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

var observationNormalizationUpdates = []string{
	"UPDATE observations SET scope = 'project'",
	"UPDATE observations SET topic_key = NULL",
	"UPDATE observations SET revision_count = 1",
	"UPDATE observations SET duplicate_count = 1",
	"UPDATE observations SET updated_at = created_at",
	"UPDATE observations SET sync_id = 'obs-'",
}

func newLegacyNormalizationStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	// Nullable columns model legacy schemas that predate current constraints.
	if _, err := db.Exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY, scope TEXT, topic_key TEXT, revision_count INTEGER,
		duplicate_count INTEGER, created_at TEXT, updated_at TEXT, sync_id TEXT, content TEXT
	); INSERT INTO observations VALUES (1, 'project', NULL, 1, 1, '2026-01-01', '2026-01-01', 'obs-kept', 'untouched')`); err != nil {
		t.Fatal(err)
	}
	return &Store{db: db, hooks: defaultStoreHooks()}
}

func TestNormalizeLegacyObservationsPredicates(t *testing.T) {
	for _, change := range []string{
		"scope = NULL", "scope = ''", "topic_key = ''",
		"revision_count = NULL", "revision_count = 0", "revision_count = -2",
		"duplicate_count = NULL", "duplicate_count = 0", "duplicate_count = -2",
		"updated_at = NULL", "updated_at = ''", "sync_id = NULL", "sync_id = ''",
	} {
		t.Run(change, func(t *testing.T) {
			s := newLegacyNormalizationStore(t)
			if _, err := s.db.Exec("UPDATE observations SET " + change); err != nil {
				t.Fatal(err)
			}
			if err := s.normalizeLegacyObservations(); err != nil {
				t.Fatal(err)
			}
			var scope, updated, syncID, content string
			var topic sql.NullString
			var revision, duplicate int
			if err := s.db.QueryRow(`SELECT scope, topic_key, revision_count, duplicate_count, updated_at, sync_id, content FROM observations`).Scan(&scope, &topic, &revision, &duplicate, &updated, &syncID, &content); err != nil {
				t.Fatal(err)
			}
			if scope != "project" || topic.Valid || revision != 1 || duplicate != 1 || updated != "2026-01-01" || content != "untouched" {
				t.Fatalf("unexpected normalized row: %q, %+v, %d, %d, %q, %q", scope, topic, revision, duplicate, updated, content)
			}
			if strings.HasPrefix(change, "sync_id") {
				if !strings.HasPrefix(syncID, "obs-") || len(syncID) != 36 {
					t.Fatalf("generated sync ID = %q", syncID)
				}
			} else if syncID != "obs-kept" {
				t.Fatalf("healthy sync ID changed to %q", syncID)
			}
			if err := s.normalizeLegacyObservations(); err != nil {
				t.Fatal(err)
			}
			var repeatedID string
			if err := s.db.QueryRow("SELECT sync_id FROM observations").Scan(&repeatedID); err != nil || repeatedID != syncID {
				t.Fatalf("repeat normalization changed ID: %q, %v", repeatedID, err)
			}
		})
	}
}

func TestNormalizeLegacyObservationsPreservesTriggerOrder(t *testing.T) {
	s := newLegacyNormalizationStore(t)
	if _, err := s.db.Exec(`UPDATE observations SET scope = '', topic_key = '', revision_count = 0, duplicate_count = -1, updated_at = '', sync_id = ''; CREATE TABLE normalization_events(field TEXT)`); err != nil {
		t.Fatal(err)
	}
	fields := []string{"scope", "topic_key", "revision_count", "duplicate_count", "updated_at", "sync_id"}
	for _, field := range fields {
		if _, err := s.db.Exec("CREATE TRIGGER audit_" + field + " AFTER UPDATE OF " + field + " ON observations BEGIN INSERT INTO normalization_events VALUES ('" + field + "'); END"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.normalizeLegacyObservations(); err != nil {
		t.Fatal(err)
	}
	var events string
	if err := s.db.QueryRow("SELECT group_concat(field, ',') FROM (SELECT field FROM normalization_events ORDER BY rowid)").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != strings.Join(fields, ",") {
		t.Fatalf("trigger events = %q, want %v", events, fields)
	}
	if err := s.normalizeLegacyObservations(); err != nil {
		t.Fatal(err)
	}
	// A later dirty write must be detected; the first healthy result is not cached.
	if _, err := s.db.Exec("UPDATE observations SET scope = ''"); err != nil {
		t.Fatal(err)
	}
	if err := s.normalizeLegacyObservations(); err != nil {
		t.Fatal(err)
	}
	var scope string
	if err := s.db.QueryRow("SELECT scope FROM observations").Scan(&scope); err != nil || scope != "project" {
		t.Fatalf("later dirty scope = %q, %v", scope, err)
	}
}

func TestNormalizeLegacyObservationsPreservesHealthyValues(t *testing.T) {
	s := newLegacyNormalizationStore(t)
	if _, err := s.db.Exec(`UPDATE observations SET scope = 'global', topic_key = 'kept-topic', revision_count = 7, duplicate_count = 9, updated_at = '2026-02-01'`); err != nil {
		t.Fatal(err)
	}
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		t.Fatal("healthy non-default values triggered a repair")
		return nil, nil
	}
	if err := s.normalizeLegacyObservations(); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE scope = 'global' AND topic_key = 'kept-topic' AND revision_count = 7 AND duplicate_count = 9 AND updated_at = '2026-02-01' AND sync_id = 'obs-kept' AND content = 'untouched'`).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("healthy row changed: count = %d, err = %v", kept, err)
	}
}

func TestNormalizeLegacyObservationsRepairErrors(t *testing.T) {
	for index, prefix := range observationNormalizationUpdates {
		t.Run(prefix, func(t *testing.T) {
			s := newLegacyNormalizationStore(t)
			if _, err := s.db.Exec("UPDATE observations SET scope = ''"); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("forced observation repair failure")
			calls := 0
			original := s.hooks.exec
			s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
				calls++
				if strings.HasPrefix(query, prefix) {
					return nil, failure
				}
				return original(db, query, args...)
			}
			if err := s.normalizeLegacyObservations(); !errors.Is(err, failure) || calls != index+1 {
				t.Fatalf("repair error = %v, statements = %d; want sentinel after %d", err, calls, index+1)
			}
			wantScope := "project"
			if index == 0 {
				wantScope = ""
			}
			var scope string
			if err := s.db.QueryRow("SELECT scope FROM observations").Scan(&scope); err != nil || scope != wantScope {
				t.Fatalf("earlier statement commit changed: scope = %q, err = %v, want %q", scope, err, wantScope)
			}
		})
	}
}

func TestNormalizeLegacyObservationsInspectionError(t *testing.T) {
	s := newLegacyNormalizationStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		t.Fatal("repair executed after inspection failed")
		return nil, nil
	}
	if err := s.normalizeLegacyObservations(); err == nil || !strings.Contains(err.Error(), "inspect legacy observations") {
		t.Fatalf("inspection error = %v", err)
	}
}

func seedDirtyObservationForMigrationTest(t *testing.T, s *Store) {
	t.Helper()
	if err := s.CreateSession("normalization-error", "engram", "/tmp/engram"); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "normalization-error", Type: "discovery", Title: "legacy repair", Content: "legacyrepairmarker", Project: "engram"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE observations SET scope = '' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateSkipsHealthyObservationNormalization(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("healthy-normalization", "engram", "/tmp/engram"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "healthy-normalization", Type: "discovery", Title: "healthy", Content: "normalizationmarker", Project: "engram"}); err != nil {
		t.Fatal(err)
	}
	updates := 0
	original := s.hooks.exec
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		for _, prefix := range observationNormalizationUpdates {
			if strings.Contains(query, prefix) {
				updates++
			}
		}
		return original(db, query, args...)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	if updates != 0 {
		t.Fatalf("healthy startup issued %d observation normalization UPDATEs, want zero", updates)
	}
	assertSearchCount(t, s, "normalizationmarker", SearchOptions{Project: "engram", Limit: 10}, 1)
}
