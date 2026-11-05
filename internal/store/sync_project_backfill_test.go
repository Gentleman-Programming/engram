package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMigrateSkipsUnresolvedSyncProjectUpdates(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,project)
		VALUES ('cloud','session','unknown-a','upsert','{"session_id":"unknown-a"}',''),
		       ('cloud','session','unknown-b','upsert','{"project":null,"session_id":"unknown-b"}','')`); err != nil {
		t.Fatal(err)
	}
	affected := int64(0)
	original := s.hooks.exec
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		result, err := original(db, query, args...)
		if err == nil && strings.Contains(query, "UPDATE sync_mutations") && strings.Contains(query, "SET project = COALESCE") {
			n, rowsErr := result.RowsAffected()
			if rowsErr != nil {
				t.Fatal(rowsErr)
			}
			affected += n
		}
		return result, err
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("startup matched %d unresolved project rows without repairing them, want zero", affected)
	}
}

func newSyncProjectBackfillStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE sessions(id TEXT PRIMARY KEY, project TEXT);
		CREATE TABLE sync_mutations(seq INTEGER PRIMARY KEY, project TEXT, payload TEXT, marker TEXT);
		CREATE INDEX idx_sync_mutations_project ON sync_mutations(project);
		INSERT INTO sessions VALUES ('known', 'session-project'), ('0', 'zero-project'), ('blank', ''), ('null', NULL), ('space', ' ');`); err != nil {
		t.Fatal(err)
	}
	return &Store{db: db, hooks: defaultStoreHooks()}
}

func TestLegacySyncProjectBackfillValues(t *testing.T) {
	for _, tc := range []struct {
		name, project, payload, want string
	}{
		{"payload priority", "", `{"project":"Payload Raw ","session_id":"known"}`, "Payload Raw "},
		{"missing project", "", `{"session_id":"known"}`, "session-project"},
		{"empty project", "", `{"project":"","session_id":"known"}`, "session-project"},
		{"null project", "", `{"project":null,"session_id":"known"}`, "session-project"},
		{"numeric project", "", `{"project":0,"session_id":"known"}`, "0"},
		{"false project", "", `{"project":false,"session_id":"known"}`, "0"},
		{"true project", "", `{"project":true,"session_id":"known"}`, "1"},
		{"object project", "", `{"project":{"a":1},"session_id":"known"}`, `{"a":1}`},
		{"array project", "", `{"project":["a"],"session_id":"known"}`, `["a"]`},
		{"numeric session", "", `{"session_id":0}`, "zero-project"},
		{"unknown session", "", `{"session_id":"unknown"}`, ""},
		{"blank session project", "", `{"session_id":"blank"}`, ""},
		{"null session project", "", `{"session_id":"null"}`, ""},
		{"space session project", "", `{"session_id":"space"}`, " "},
		{"empty payload", "", "", ""},
		{"scalar payload", "", "123", ""},
		{"null payload value", "", "null", ""},
		{"kept project", "kept", `{"project":"other","session_id":"known"}`, "kept"},
		{"excluded malformed payload", "kept", "{", "kept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSyncProjectBackfillStore(t)
			if _, err := s.db.Exec("INSERT INTO sync_mutations VALUES (1, ?, ?, 'untouched')", tc.project, tc.payload); err != nil {
				t.Fatal(err)
			}
			affected := int64(0)
			original := s.hooks.exec
			s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
				result, err := original(db, query, args...)
				if err == nil {
					n, err := result.RowsAffected()
					if err != nil {
						t.Fatal(err)
					}
					affected += n
				}
				return result, err
			}
			if err := s.backfillLegacySyncMutationProjects(); err != nil {
				t.Fatal(err)
			}
			var project, payload, marker string
			if err := s.db.QueryRow("SELECT project, payload, marker FROM sync_mutations WHERE seq = 1").Scan(&project, &payload, &marker); err != nil {
				t.Fatal(err)
			}
			wantAffected := int64(0)
			if tc.want != tc.project {
				wantAffected = 1
			}
			if project != tc.want || payload != tc.payload || marker != "untouched" || affected != wantAffected {
				t.Fatalf("backfill = project %q, payload %q, marker %q, affected %d; want %q, original payload/marker, affected %d", project, payload, marker, affected, tc.want, wantAffected)
			}
			affected = 0
			if err := s.backfillLegacySyncMutationProjects(); err != nil || affected != 0 {
				t.Fatalf("repeat backfill: %v, affected %d", err, affected)
			}
		})
	}
}

func TestLegacySyncProjectBackfillMalformedJSON(t *testing.T) {
	s := newSyncProjectBackfillStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_mutations VALUES (1, '', '{"project":"valid"}', 'first'), (2, '', '{', 'invalid')`); err != nil {
		t.Fatal(err)
	}
	if err := s.backfillLegacySyncMutationProjects(); err == nil || !strings.Contains(strings.ToLower(err.Error()), "json") {
		t.Fatalf("eligible malformed JSON did not fail: %v", err)
	}
	var blank int
	if err := s.db.QueryRow("SELECT count(*) FROM sync_mutations WHERE project = ''").Scan(&blank); err != nil || blank != 2 {
		t.Fatalf("failed first statement did not roll back: blank=%d err=%v", blank, err)
	}
}

func TestLegacySyncProjectBackfillPreservesExcludedNulls(t *testing.T) {
	s := newSyncProjectBackfillStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_mutations VALUES (1, NULL, '{', 'null project'), (2, '', NULL, 'null payload')`); err != nil {
		t.Fatal(err)
	}
	if err := s.backfillLegacySyncMutationProjects(); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err := s.db.QueryRow("SELECT count(*) FROM sync_mutations WHERE (seq = 1 AND project IS NULL AND payload = '{') OR (seq = 2 AND project = '' AND payload IS NULL)").Scan(&kept); err != nil || kept != 2 {
		t.Fatalf("excluded NULL rows changed: %d, %v", kept, err)
	}
}

func TestLegacySyncProjectBackfillHealsLaterSession(t *testing.T) {
	s := newSyncProjectBackfillStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_mutations VALUES (1, '', '{"session_id":"later"}', 'untouched')`); err != nil {
		t.Fatal(err)
	}
	if err := s.backfillLegacySyncMutationProjects(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO sessions VALUES ('later', 'new-project')"); err != nil {
		t.Fatal(err)
	}
	if err := s.backfillLegacySyncMutationProjects(); err != nil {
		t.Fatal(err)
	}
	var project string
	if err := s.db.QueryRow("SELECT project FROM sync_mutations").Scan(&project); err != nil || project != "new-project" {
		t.Fatalf("later session not recovered: %q, %v", project, err)
	}
}

func TestLegacySyncProjectBackfillPreservesPhaseOrder(t *testing.T) {
	s := newSyncProjectBackfillStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_mutations VALUES
		(1, '', '{"project":"payload-one","session_id":"known"}', 'one'),
		(2, '', '{"session_id":"known"}', 'two'),
		(3, '', '{"project":"payload-three"}', 'three');
		CREATE TABLE project_events(seq INTEGER);
		CREATE TRIGGER project_audit AFTER UPDATE OF project ON sync_mutations WHEN OLD.project IS NOT NEW.project BEGIN INSERT INTO project_events VALUES (NEW.seq); END;`); err != nil {
		t.Fatal(err)
	}
	if err := s.backfillLegacySyncMutationProjects(); err != nil {
		t.Fatal(err)
	}
	var order string
	if err := s.db.QueryRow("SELECT group_concat(seq, ',') FROM (SELECT seq FROM project_events ORDER BY rowid)").Scan(&order); err != nil || order != "1,3,2" {
		t.Fatalf("value-changing trigger order = %q, %v; want payload phase then session", order, err)
	}
}

func TestLegacySyncProjectBackfillPhaseErrors(t *testing.T) {
	for _, phase := range []int{1, 2} {
		t.Run(strconv.Itoa(phase), func(t *testing.T) {
			s := newSyncProjectBackfillStore(t)
			if _, err := s.db.Exec(`INSERT INTO sync_mutations VALUES (1, '', '{"project":"payload-project"}', 'one'), (2, '', '{"session_id":"known"}', 'two')`); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("forced backfill failure")
			original := s.hooks.exec
			calls := 0
			s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
				calls++
				if calls == phase {
					return nil, failure
				}
				return original(db, query, args...)
			}
			if err := s.backfillLegacySyncMutationProjects(); !errors.Is(err, failure) || calls != phase {
				t.Fatalf("phase error = %v, calls %d", err, calls)
			}
			var project string
			want := ""
			if phase == 2 {
				want = "payload-project"
			}
			if err := s.db.QueryRow("SELECT project FROM sync_mutations WHERE seq=1").Scan(&project); err != nil || project != want {
				t.Fatalf("earlier phase commit changed: %q, %v, want %q", project, err, want)
			}
		})
	}
}
