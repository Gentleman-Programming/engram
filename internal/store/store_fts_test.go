package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestTrigramSearchFindsCJKObservationsAndPrompts(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-cjk", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s-cjk", Type: "bugfix", Title: "CJK search", Content: "サンドボックス修正テスト", Project: "engram", Scope: "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s-cjk", Content: "サンドボックス修正プロンプト", Project: "engram"}); err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	observations, err := s.Search("サンドボックス", SearchOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("search observations: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("CJK observation search returned %d results, want 1", len(observations))
	}
	prompts, err := s.SearchPrompts("サンドボックス", "engram", 10)
	if err != nil {
		t.Fatalf("search prompts: %v", err)
	}
	if len(prompts) != 1 {
		t.Fatalf("CJK prompt search returned %d results, want 1", len(prompts))
	}
}

func TestMigrateFTSUpgradesLegacyContractsAndIsIdempotent(t *testing.T) {
	s := newStoreAfterFTSChange(t, func(t *testing.T, db *sql.DB) {
		t.Helper()
		if _, err := db.Exec(`
			DROP TRIGGER IF EXISTS obs_fts_insert;
			DROP TRIGGER IF EXISTS obs_fts_update_insert;
			DROP TRIGGER IF EXISTS obs_fts_update;
			DROP TRIGGER IF EXISTS obs_fts_delete;
			DROP TRIGGER IF EXISTS prompt_fts_insert;
			DROP TRIGGER IF EXISTS prompt_fts_update_insert;
			DROP TRIGGER IF EXISTS prompt_fts_update;
			DROP TRIGGER IF EXISTS prompt_fts_delete;
			DROP TABLE observations_fts;
			DROP TABLE prompts_fts;
			CREATE VIRTUAL TABLE observations_fts USING fts5(title, content, tool_name, type, project, topic_key, content='observations', content_rowid='id');
			CREATE VIRTUAL TABLE prompts_fts USING fts5(content, project, content='user_prompts', content_rowid='id');
			INSERT INTO sessions (id, project, directory) VALUES ('s-legacy-fts', 'engram', '/tmp/engram');
			INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, updated_at)
			VALUES ('obs-legacy-fts', 's-legacy-fts', 'bugfix', 'legacy', '日本語検索テスト', 'engram', 'project', 'legacy', datetime('now'));
			INSERT INTO user_prompts (sync_id, session_id, content, project)
			VALUES ('prompt-legacy-fts', 's-legacy-fts', '日本語プロンプト検索', 'engram');
		`); err != nil {
			t.Fatalf("create legacy FTS schema: %v", err)
		}
	})

	assertFTSContract(t, s, "observations_fts", []string{"title", "content", "tool_name", "type", "project", "topic_key"}, "observations")
	assertFTSContract(t, s, "prompts_fts", []string{"content", "project"}, "user_prompts")
	assertSearchCount(t, s, "日本語検索", SearchOptions{Project: "engram", Limit: 10}, 1)
	prompts, err := s.SearchPrompts("日本語プロンプト", "engram", 10)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("legacy prompt search = %d results, %v; want 1, nil", len(prompts), err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	assertFTSContract(t, s, "observations_fts", []string{"title", "content", "tool_name", "type", "project", "topic_key"}, "observations")
	assertSearchCount(t, s, "日本語検索", SearchOptions{Project: "engram", Limit: 10}, 1)
}

func TestMigrateFTSRepairsOneColumnPromptWorkaroundAndSynchronizesWrites(t *testing.T) {
	s := newStoreAfterFTSChange(t, func(t *testing.T, db *sql.DB) {
		t.Helper()
		if _, err := db.Exec(`
			DROP TRIGGER IF EXISTS prompt_fts_insert;
			DROP TRIGGER IF EXISTS prompt_fts_update_insert;
			DROP TRIGGER IF EXISTS prompt_fts_update;
			DROP TRIGGER IF EXISTS prompt_fts_delete;
			DROP TABLE prompts_fts;
			CREATE VIRTUAL TABLE prompts_fts USING fts5(content, tokenize='trigram');
			INSERT INTO sessions (id, project, directory) VALUES ('s-manual-prompt-fts', 'engram', '/tmp/engram');
			INSERT INTO user_prompts (sync_id, session_id, content, project)
			VALUES ('prompt-manual-fts', 's-manual-prompt-fts', '手動修正プロンプト', 'engram');
		`); err != nil {
			t.Fatalf("create one-column prompt workaround: %v", err)
		}
	})

	assertFTSContract(t, s, "prompts_fts", []string{"content", "project"}, "user_prompts")
	prompts, err := s.SearchPrompts("手動修正", "engram", 10)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("migrated manual prompt search = %d results, %v; want 1, nil", len(prompts), err)
	}

	id, err := s.AddPrompt(AddPromptParams{SessionID: "s-manual-prompt-fts", Content: "追加修正プロンプト", Project: "engram"})
	if err != nil {
		t.Fatalf("add prompt after migration: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE user_prompts SET content = '更新済み修正プロンプト' WHERE id = ?`, id); err != nil {
		t.Fatalf("update prompt after migration: %v", err)
	}
	prompts, err = s.SearchPrompts("更新済み", "engram", 10)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("updated prompt search = %d results, %v; want 1, nil", len(prompts), err)
	}
	if err := s.DeletePrompt(id); err != nil {
		t.Fatalf("delete prompt after migration: %v", err)
	}
	prompts, err = s.SearchPrompts("更新済み", "engram", 10)
	if err != nil || len(prompts) != 0 {
		t.Fatalf("deleted prompt search = %d results, %v; want 0, nil", len(prompts), err)
	}
}

func TestTrigramTriggersSynchronizeObservationLifecycle(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-fts-lifecycle", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s-fts-lifecycle", Type: "bugfix", Title: "lifecycle", Content: "初期状態テスト", Project: "engram", Scope: "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	assertSearchCount(t, s, "初期状態", SearchOptions{Project: "engram", Limit: 10}, 1)
	if _, err := s.db.Exec(`UPDATE observations SET content = '更新後状態テスト' WHERE id = ?`, id); err != nil {
		t.Fatalf("update observation: %v", err)
	}
	assertSearchCount(t, s, "初期状態", SearchOptions{Project: "engram", Limit: 10}, 0)
	assertSearchCount(t, s, "更新後状態", SearchOptions{Project: "engram", Limit: 10}, 1)

	if err := s.DeleteObservation(id, false); err != nil {
		t.Fatalf("soft-delete observation: %v", err)
	}
	assertSearchCount(t, s, "更新後状態", SearchOptions{Project: "engram", Limit: 10}, 0)
	if _, err := s.db.Exec(`UPDATE observations SET deleted_at = NULL WHERE id = ?`, id); err != nil {
		t.Fatalf("restore soft-deleted observation: %v", err)
	}
	assertSearchCount(t, s, "更新後状態", SearchOptions{Project: "engram", Limit: 10}, 1)
	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatalf("hard-delete observation: %v", err)
	}
	assertSearchCount(t, s, "更新後状態", SearchOptions{Project: "engram", Limit: 10}, 0)
}

func TestShortTermFallbackEscapesAndPreservesFiltersAndMatchModes(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-short-alpha", "alpha", "/tmp/alpha"); err != nil {
		t.Fatalf("create alpha session: %v", err)
	}
	if err := s.CreateSession("s-short-beta", "beta", "/tmp/beta"); err != nil {
		t.Fatalf("create beta session: %v", err)
	}
	first, err := s.AddObservation(AddObservationParams{SessionID: "s-short-alpha", Type: "bugfix", Title: `literal % _ \`, Content: "go release", Project: "alpha", Scope: "project", TopicKey: "go"})
	if err != nil {
		t.Fatalf("add first observation: %v", err)
	}
	second, err := s.AddObservation(AddObservationParams{SessionID: "s-short-alpha", Type: "decision", Title: "newer 修正", Content: "go", Project: "alpha", Scope: "project"})
	if err != nil {
		t.Fatalf("add second observation: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "s-short-beta", Type: "bugfix", Title: "beta", Content: "go release", Project: "beta", Scope: "project"}); err != nil {
		t.Fatalf("add beta observation: %v", err)
	}

	for _, query := range []string{"%", "_", `\`} {
		t.Run("escaped "+query, func(t *testing.T) {
			assertSearchCount(t, s, query, SearchOptions{Project: "alpha", Type: "bugfix", Limit: 10}, 1)
		})
	}
	results, err := s.Search("修", SearchOptions{Project: "alpha", Limit: 1})
	if err != nil {
		t.Fatalf("one-character fallback: %v", err)
	}
	if len(results) != 1 || results[0].ID != second {
		t.Fatalf("one-character fallback result = %+v, want latest observation %d", results, second)
	}
	assertSearchCount(t, s, "修正", SearchOptions{Project: "alpha", Limit: 10}, 1)
	assertSearchCount(t, s, "go release", SearchOptions{Project: "alpha", Limit: 10}, 1)
	assertSearchCount(t, s, "go release", SearchOptions{Project: "alpha", Limit: 10, MatchMode: "any"}, 2)

	if first == second {
		t.Fatal("expected distinct fixture observations")
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s-short-alpha", Content: "literal % prompt go", Project: "alpha"}); err != nil {
		t.Fatalf("add short prompt: %v", err)
	}
	prompts, err := s.SearchPrompts("%", "alpha", 10)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("escaped short prompt search = %d results, %v; want 1, nil", len(prompts), err)
	}
}

func newStoreAfterFTSChange(t *testing.T, change func(t *testing.T, db *sql.DB)) *Store {
	t.Helper()
	s := newTestStore(t)
	dir := s.cfg.DataDir
	if err := s.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "engram.db"))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	change(t, db)
	if err := db.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir
	migrated, err := New(cfg)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	return migrated
}

func assertFTSContract(t *testing.T, s *Store, table string, columns []string, contentTable string) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatalf("begin contract inspection: %v", err)
	}
	defer tx.Rollback()
	matches, err := ftsTableMatches(tx, table, columns, contentTable)
	if err != nil {
		t.Fatalf("inspect %s: %v", table, err)
	}
	if !matches {
		t.Fatalf("%s does not satisfy the trigram external-content contract", table)
	}
}

func assertSearchCount(t *testing.T, s *Store, query string, opts SearchOptions, want int) {
	t.Helper()
	results, err := s.Search(query, opts)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	if len(results) != want {
		t.Fatalf("search %q returned %d results, want %d", query, len(results), want)
	}
}
