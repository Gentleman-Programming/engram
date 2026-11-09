package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestSearchFoldsDiacritics(t *testing.T) {
	for _, text := range []string{"resumen del vídeo configuración café", "resumen del video configuracion cafe"} {
		t.Run(text, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("accent", "alpha", "/alpha"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddObservation(AddObservationParams{SessionID: "accent", Project: "alpha", Type: "note", Title: text, Content: text}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddPrompt(AddPromptParams{SessionID: "accent", Project: "alpha", Content: text}); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{"video", "vídeo", "configuracion", "configuración", "cafe", "café", "figuraci"} {
				t.Run(query, func(t *testing.T) {
					obs, err := s.Search(query, SearchOptions{Project: "alpha", Limit: 10})
					if err != nil || len(obs) != 1 {
						t.Errorf("observations query=%q count=%d error=%v", query, len(obs), err)
					}
					prompts, err := s.SearchPrompts(query, "alpha", 10)
					if err != nil || len(prompts) != 1 {
						t.Errorf("prompts query=%q count=%d error=%v", query, len(prompts), err)
					}
				})
			}
		})
	}
}

// Recreate the precise pre-upgrade tables while preserving source rows and
// the current safe trigger contract, isolating tokenizer migration from repairs.
func installPlainTrigramForTest(t *testing.T, s *Store) {
	t.Helper()
	for _, name := range []string{"obs_fts_insert", "obs_fts_update_insert", "obs_fts_update", "obs_fts_delete", "prompt_fts_insert", "prompt_fts_update_insert", "prompt_fts_update", "prompt_fts_delete"} {
		if _, err := s.db.Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
			t.Fatal(err)
		}
	}
	ddl := "DROP TABLE observations_fts; DROP TABLE prompts_fts;" + observationsFTSDefinition + promptsFTSDefinition + observationsFTSTriggers + promptsFTSTriggers + `
 INSERT INTO observations_fts(rowid,title,content,tool_name,type,project,topic_key) SELECT id,title,content,tool_name,type,project,topic_key FROM observations WHERE deleted_at IS NULL;
 INSERT INTO prompts_fts(rowid,content,project) SELECT id,content,project FROM user_prompts;`
	ddl = strings.ReplaceAll(ddl, "trigram remove_diacritics 1", "trigram")
	if _, err := s.db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
}

func TestAccentFTSUpgradePreservesDataAndWrites(t *testing.T) {
	s := newTestStore(t)
	cfg := s.cfg
	if err := s.CreateSession("old", "alpha", "/alpha"); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "old", Project: "alpha", Type: "note", Title: "vídeo original", Content: "configuración original"})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := s.AddPrompt(AddPromptParams{SessionID: "old", Project: "alpha", Content: "vídeo original"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO observations(session_id,type,title,content,project,deleted_at) VALUES('old','note','vídeo eliminado','vídeo eliminado','alpha',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	installPlainTrigramForTest(t, s)
	assertSearchCount(t, s, "video", SearchOptions{Project: "alpha"}, 0)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	assertSearchCount(t, s, "video", SearchOptions{Project: "alpha"}, 1)
	assertSearchCount(t, s, "video", SearchOptions{Project: "other"}, 0)
	got, err := s.Search("configuracion", SearchOptions{Project: "alpha"})
	if err != nil || len(got) != 1 || got[0].ID != id || got[0].Content != "configuración original" {
		t.Fatalf("source data changed: %+v %v", got, err)
	}
	ps, err := s.SearchPrompts("video", "alpha", 10)
	if err != nil || len(ps) != 1 || ps[0].ID != prompt || ps[0].Content != "vídeo original" {
		t.Fatalf("prompt source changed: %+v %v", ps, err)
	}
	var before, after int
	if err := s.db.QueryRow("PRAGMA schema_version").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA schema_version").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("second open changed schema: %d -> %d", before, after)
	}
	if _, err := s.db.Exec(`UPDATE observations SET content='résumé nouveau' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	assertSearchCount(t, s, "configuracion", SearchOptions{Project: "alpha"}, 0)
	assertSearchCount(t, s, "resume", SearchOptions{Project: "alpha"}, 1)
	if _, err := s.db.Exec(`UPDATE user_prompts SET content='résumé nouveau' WHERE id=?`, prompt); err != nil {
		t.Fatal(err)
	}
	ps, err = s.SearchPrompts("resume", "alpha", 10)
	if err != nil || len(ps) != 1 {
		t.Fatalf("updated prompt=%+v %v", ps, err)
	}
	if err := s.DeletePrompt(prompt); err != nil {
		t.Fatal(err)
	}
	ps, err = s.SearchPrompts("resume", "alpha", 10)
	if err != nil || len(ps) != 0 {
		t.Fatalf("deleted prompt=%+v %v", ps, err)
	}
	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatal(err)
	}
	assertSearchCount(t, s, "resume", SearchOptions{Project: "alpha"}, 0)
}

func TestAccentFTSUpgradeRollsBackBothIndexes(t *testing.T) {
	s := newTestStore(t)
	installPlainTrigramForTest(t, s)
	original := s.hooks.exec
	s.hooks.exec = func(db execer, q string, args ...any) (sql.Result, error) {
		if strings.Contains(q, "DROP TABLE IF EXISTS prompts_fts") {
			return nil, errors.New("injected prompt rebuild failure")
		}
		return db.Exec(q, args...)
	}
	err := s.migrateFTS()
	s.hooks.exec = original
	if err == nil || !strings.Contains(err.Error(), "injected prompt rebuild failure") {
		t.Fatalf("migration error=%v", err)
	}
	for _, table := range []string{"observations_fts", "prompts_fts"} {
		var ddl string
		if err := s.db.QueryRow("SELECT sql FROM sqlite_master WHERE name=?", table).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(ddl, "remove_diacritics") {
			t.Fatalf("partial upgrade survived rollback: %s", ddl)
		}
	}
	if err := s.migrateFTS(); err != nil {
		t.Fatal(err)
	}
}
