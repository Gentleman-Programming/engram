package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestMigrateFTSReadsTriggerCatalogOnce(t *testing.T) {
	s := newTestStore(t)
	reads, rebuilds := 0, 0
	originalQuery, originalExec := s.hooks.queryIt, s.hooks.exec
	s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
		if strings.Contains(query, "FROM sqlite_master") {
			reads++
		}
		return originalQuery(db, query, args...)
	}
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DROP TABLE IF EXISTS") {
			rebuilds++
		}
		return originalExec(db, query, args...)
	}
	if err := s.migrateFTS(); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || rebuilds != 0 {
		t.Fatalf("healthy FTS inspection: %d snapshot reads, %d rebuilds; want 1, 0", reads, rebuilds)
	}
}

func TestMigrateFTSAcceptsUppercaseTriggerTableReferences(t *testing.T) {
	s := newTestStore(t)
	for _, name := range []string{"obs_fts_insert", "obs_fts_delete", "obs_fts_update", "obs_fts_update_insert", "prompt_fts_insert", "prompt_fts_delete", "prompt_fts_update", "prompt_fts_update_insert"} {
		if _, err := s.db.Exec("DROP TRIGGER " + name); err != nil {
			t.Fatal(err)
		}
	}
	definitions := strings.ReplaceAll(observationsFTSTriggers, "ON observations", "ON OBSERVATIONS") +
		strings.ReplaceAll(promptsFTSTriggers, "ON user_prompts", "ON USER_PROMPTS")
	if _, err := s.db.Exec(definitions); err != nil {
		t.Fatal(err)
	}
	original := s.hooks.exec
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DROP TABLE IF EXISTS") {
			t.Fatal("valid uppercase table references caused an FTS rebuild")
		}
		return original(db, query, args...)
	}
	if err := s.migrateFTS(); err != nil {
		t.Fatal(err)
	}
}

func TestFTSTriggersMatchDefinitions(t *testing.T) {
	expected := map[string][]string{"obs_fts_insert": {"afterinsertonobservations", "whennew.deleted_atisnull"}}
	valid := "CREATE TRIGGER obs_fts_insert AFTER INSERT ON observations WHEN new.deleted_at IS NULL BEGIN SELECT 1; END"
	for _, tc := range []struct {
		name        string
		definitions map[string]string
		want        bool
	}{
		{"valid", map[string]string{"obs_fts_insert": valid}, true},
		{"case and whitespace", map[string]string{"obs_fts_insert": strings.ToLower(strings.ReplaceAll(valid, " ", "\n\t"))}, true},
		{"missing", map[string]string{}, false},
		{"unreadable", nil, false},
		{"wrong name", map[string]string{"unrelated_trigger": valid}, false},
		{"wrong table", map[string]string{"obs_fts_insert": strings.ReplaceAll(valid, "ON observations", "ON sessions")}, false},
		{"missing condition", map[string]string{"obs_fts_insert": strings.ReplaceAll(valid, " WHEN new.deleted_at IS NULL", "")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ftsTriggersMatch(tc.definitions, expected); got != tc.want {
				t.Fatalf("trigger contract match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMigrateFTSRepairsTriggerGroupsIndependently(t *testing.T) {
	for _, tc := range []struct {
		name, change, table string
	}{
		{"missing observation trigger", "DROP TRIGGER obs_fts_insert", "observations_fts"},
		{"wrong observation trigger", "DROP TRIGGER obs_fts_update; CREATE TRIGGER obs_fts_update AFTER UPDATE ON observations BEGIN SELECT 1; END", "observations_fts"},
		{"missing prompt trigger", "DROP TRIGGER prompt_fts_delete", "prompts_fts"},
		{"wrong prompt trigger", "DROP TRIGGER prompt_fts_insert; CREATE TRIGGER prompt_fts_insert AFTER INSERT ON observations BEGIN SELECT 1; END", "prompts_fts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("fts-snapshot", "engram", "/tmp/engram"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddObservation(AddObservationParams{SessionID: "fts-snapshot", Type: "discovery", Title: "snapshot", Content: "snapshotobservationmarker", Project: "engram"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddPrompt(AddPromptParams{SessionID: "fts-snapshot", Content: "snapshotpromptmarker", Project: "engram"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(tc.change); err != nil {
				t.Fatal(err)
			}
			rebuilt := []string{}
			original := s.hooks.exec
			s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
				for _, table := range []string{"observations_fts", "prompts_fts"} {
					if strings.Contains(query, "DROP TABLE IF EXISTS "+table) {
						rebuilt = append(rebuilt, table)
					}
				}
				return original(db, query, args...)
			}
			if err := s.migrateFTS(); err != nil {
				t.Fatal(err)
			}
			if len(rebuilt) != 1 || rebuilt[0] != tc.table {
				t.Fatalf("rebuilt %v, want only %s", rebuilt, tc.table)
			}
			assertSearchCount(t, s, "snapshotobservationmarker", SearchOptions{Project: "engram", Limit: 10}, 1)
			if prompts, err := s.SearchPrompts("snapshotpromptmarker", "engram", 10); err != nil || len(prompts) != 1 {
				t.Fatalf("prompt search after repair = %v, %v", prompts, err)
			}
			rebuilt = nil
			if err := s.migrateFTS(); err != nil {
				t.Fatal(err)
			}
			if len(rebuilt) != 0 {
				t.Fatalf("repaired contracts rebuilt again: %v", rebuilt)
			}
		})
	}
}

func TestFTSTriggerSnapshotRejectsPartialReads(t *testing.T) {
	for _, kind := range []string{"query", "scan", "rows", "close"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestStore(t)
			failure := errors.New("trigger snapshot " + kind)
			rows := &fakeRows{next: []bool{true, false}}
			switch kind {
			case "scan":
				rows.scanErr = failure
			case "rows":
				rows.err = failure
			case "close":
				rows.closeErr = failure
			}
			original := s.hooks.queryIt
			s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
				if kind == "query" {
					return nil, failure
				}
				return rows, nil
			}
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			definitions := s.readFTSTriggerDefinitions(tx)
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if definitions != nil || (kind != "query" && !rows.closed) {
				t.Fatalf("failed snapshot = %v, rows closed = %v; want nil and closed cursor", definitions, rows.closed)
			}
			s.hooks.queryIt = original
		})
	}
}

func TestMigrateFTSRetainsRepairOnUnreadableTriggers(t *testing.T) {
	s := newTestStore(t)
	s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
		return nil, errors.New("unreadable trigger catalog")
	}
	rebuilds := 0
	original := s.hooks.exec
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DROP TABLE IF EXISTS") {
			rebuilds++
		}
		return original(db, query, args...)
	}
	if err := s.migrateFTS(); err != nil {
		t.Fatal(err)
	}
	if rebuilds != 2 {
		t.Fatalf("unreadable trigger snapshot caused %d rebuilds, want both groups repaired", rebuilds)
	}
}
