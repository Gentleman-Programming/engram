package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func mustDefaultConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	return cfg
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

type fakeRows struct {
	next     []bool
	scanErr  error
	err      error
	closeErr error
	closed   bool
}

func (f *fakeRows) Next() bool {
	if len(f.next) == 0 {
		return false
	}
	v := f.next[0]
	f.next = f.next[1:]
	return v
}

func (f *fakeRows) Scan(dest ...any) error {
	return f.scanErr
}

func (f *fakeRows) Err() error {
	return f.err
}

func (f *fakeRows) Close() error {
	f.closed = true
	return f.closeErr
}

func TestAddObservationDeduplicatesWithinWindow(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed tokenizer",
		Content:   "Normalized tokenizer panic on edge case",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add first observation: %v", err)
	}

	secondID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fixed tokenizer",
		Content:   "normalized   tokenizer panic on EDGE case",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add duplicate observation: %v", err)
	}

	if firstID != secondID {
		t.Fatalf("expected duplicate to reuse same id, got %d and %d", firstID, secondID)
	}

	obs, err := s.GetObservation(firstID)
	if err != nil {
		t.Fatalf("get deduped observation: %v", err)
	}
	if obs.DuplicateCount != 2 {
		t.Fatalf("expected duplicate_count=2, got %d", obs.DuplicateCount)
	}
}

func TestScopeFiltersSearchAndContext(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Project auth",
		Content:   "Keep auth middleware in project memory",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add project observation: %v", err)
	}

	_, err = s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Personal note",
		Content:   "Use this regex trick later",
		Project:   "engram",
		Scope:     "personal",
	})
	if err != nil {
		t.Fatalf("add personal observation: %v", err)
	}

	projectResults, err := s.Search("regex", SearchOptions{Project: "engram", Scope: "project", Limit: 10})
	if err != nil {
		t.Fatalf("search project scope: %v", err)
	}
	if len(projectResults) != 0 {
		t.Fatalf("expected no project-scope regex results, got %d", len(projectResults))
	}

	personalResults, err := s.Search("regex", SearchOptions{Project: "engram", Scope: "personal", Limit: 10})
	if err != nil {
		t.Fatalf("search personal scope: %v", err)
	}
	if len(personalResults) != 1 {
		t.Fatalf("expected 1 personal-scope result, got %d", len(personalResults))
	}

	ctx, err := s.FormatContext("engram", "personal")
	if err != nil {
		t.Fatalf("format context personal: %v", err)
	}
	if !strings.Contains(ctx, "Personal note") {
		t.Fatalf("expected personal context to include personal observation")
	}
	if strings.Contains(ctx, "Project auth") {
		t.Fatalf("expected personal context to exclude project observation")
	}
}

func TestUpdateAndSoftDeleteExcludedFromSearchAndTimeline(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "first",
		Content:   "first event",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add first: %v", err)
	}

	middleID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "middle",
		Content:   "to be deleted",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add middle: %v", err)
	}

	lastID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "last",
		Content:   "last event",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add last: %v", err)
	}

	newTitle := "last-updated"
	newContent := "updated content"
	newScope := "personal"
	updated, err := s.UpdateObservation(lastID, UpdateObservationParams{
		Title:   &newTitle,
		Content: &newContent,
		Scope:   &newScope,
	})
	if err != nil {
		t.Fatalf("update observation: %v", err)
	}
	if updated.Title != newTitle || updated.Scope != "personal" {
		t.Fatalf("update did not apply; got title=%q scope=%q", updated.Title, updated.Scope)
	}

	if err := s.DeleteObservation(middleID, false); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	if _, err := s.GetObservation(middleID); err == nil {
		t.Fatalf("expected deleted observation to be hidden from GetObservation")
	}

	searchResults, err := s.Search("deleted", SearchOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(searchResults) != 0 {
		t.Fatalf("expected deleted observation excluded from search")
	}

	timeline, err := s.Timeline(firstID, 5, 5)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if len(timeline.After) != 1 || timeline.After[0].ID != lastID {
		t.Fatalf("expected timeline to skip deleted observation")
	}

	if err := s.DeleteObservation(lastID, true); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	if _, err := s.GetObservation(lastID); err == nil {
		t.Fatalf("expected hard-deleted observation to be missing")
	}
}

func TestTopicKeyUpsertUpdatesSameTopicWithoutCreatingNewRow(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Use middleware for JWT validation.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture auth model",
	})
	if err != nil {
		t.Fatalf("add first architecture: %v", err)
	}

	secondID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Move auth to gateway + middleware chain.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "ARCHITECTURE   AUTH  MODEL",
	})
	if err != nil {
		t.Fatalf("upsert architecture: %v", err)
	}

	if firstID != secondID {
		t.Fatalf("expected topic upsert to reuse id, got %d and %d", firstID, secondID)
	}

	obs, err := s.GetObservation(firstID)
	if err != nil {
		t.Fatalf("get upserted observation: %v", err)
	}
	if obs.RevisionCount != 2 {
		t.Fatalf("expected revision_count=2, got %d", obs.RevisionCount)
	}
	if obs.TopicKey == nil || *obs.TopicKey != "architecture-auth-model" {
		t.Fatalf("expected normalized topic key, got %v", obs.TopicKey)
	}
	if !strings.Contains(obs.Content, "gateway") {
		t.Fatalf("expected latest content after upsert, got %q", obs.Content)
	}
}

func TestDifferentTopicsDoNotReplaceEachOther(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	archID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Architecture decision",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth",
	})
	if err != nil {
		t.Fatalf("add architecture observation: %v", err)
	}

	bugID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Fix auth nil panic",
		Content:   "Bugfix details",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "bug/auth-nil-panic",
	})
	if err != nil {
		t.Fatalf("add bug observation: %v", err)
	}

	if archID == bugID {
		t.Fatalf("expected different topic keys to create different observations")
	}

	observations, err := s.AllObservations("engram", "project", 10)
	if err != nil {
		t.Fatalf("all observations: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(observations))
	}
}

func TestNewMigratesLegacyObservationIDSchema(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "engram.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			project TEXT NOT NULL,
			directory TEXT NOT NULL,
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			ended_at TEXT,
			summary TEXT
		);
		CREATE TABLE observations (
			id INT,
			session_id TEXT,
			type TEXT,
			title TEXT,
			content TEXT,
			tool_name TEXT,
			project TEXT,
			created_at TEXT
		);
		INSERT INTO sessions (id, project, directory) VALUES ('s1', 'engram', '/tmp/engram');
		INSERT INTO observations (id, session_id, type, title, content, project, created_at)
		VALUES
			(NULL, 's1', 'bugfix', 'legacy null', 'legacy null content', 'engram', datetime('now')),
			(7, 's1', 'bugfix', 'legacy fixed', 'legacy fixed content', 'engram', datetime('now')),
			(7, 's1', 'bugfix', 'legacy duplicate', 'legacy duplicate content', 'engram', datetime('now'));
	`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = dataDir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store after legacy schema: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	obs, err := s.AllObservations("engram", "", 20)
	if err != nil {
		t.Fatalf("all observations after migration: %v", err)
	}
	if len(obs) != 3 {
		t.Fatalf("expected 3 migrated observations, got %d", len(obs))
	}

	seen := make(map[int64]bool)
	for _, o := range obs {
		if o.ID <= 0 {
			t.Fatalf("expected migrated observation id > 0, got %d", o.ID)
		}
		if seen[o.ID] {
			t.Fatalf("expected unique migrated ids, duplicate %d", o.ID)
		}
		seen[o.ID] = true
	}

	results, err := s.Search("legacy", SearchOptions{Project: "engram", Limit: 10})
	if err != nil {
		t.Fatalf("search after migration: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected search results after migration")
	}

	newID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "post migration",
		Content:   "new row should get id",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation after migration: %v", err)
	}
	if newID <= 0 {
		t.Fatalf("expected autoincrement id after migration, got %d", newID)
	}
}

func TestNewMigratesLegacyUserPromptsSyncIDSchema(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "engram.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			project TEXT NOT NULL,
			directory TEXT NOT NULL,
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			ended_at TEXT,
			summary TEXT
		);
		CREATE TABLE user_prompts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			content TEXT NOT NULL,
			project TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		INSERT INTO sessions (id, project, directory) VALUES ('s1', 'engram', '/tmp/engram');
		INSERT INTO user_prompts (session_id, content, project) VALUES ('s1', 'legacy prompt', 'engram');
	`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = dataDir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store after legacy prompt schema: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var syncID string
	if err := s.db.QueryRow("SELECT sync_id FROM user_prompts WHERE content = ?", "legacy prompt").Scan(&syncID); err != nil {
		t.Fatalf("query migrated prompt sync_id: %v", err)
	}
	if syncID == "" {
		t.Fatalf("expected migrated prompt sync_id to be backfilled")
	}

	var hasSyncIDColumn bool
	rows, err := s.db.Query("PRAGMA table_info(user_prompts)")
	if err != nil {
		t.Fatalf("query prompt columns: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan prompt column: %v", err)
		}
		if name == "sync_id" {
			hasSyncIDColumn = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate prompt columns: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close prompt columns: %v", err)
	}
	if !hasSyncIDColumn {
		t.Fatalf("expected user_prompts.sync_id column after migration")
	}

	var indexName string
	if err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_prompts_sync_id'").Scan(&indexName); err != nil {
		t.Fatalf("query prompt sync index: %v", err)
	}
	if indexName != "idx_prompts_sync_id" {
		t.Fatalf("expected idx_prompts_sync_id to exist, got %q", indexName)
	}
}

func TestSuggestTopicKeyNormalizesDeterministically(t *testing.T) {
	got := SuggestTopicKey("Architecture", "  Auth Model  ", "ignored")
	if got != "architecture/auth-model" {
		t.Fatalf("expected architecture/auth-model, got %q", got)
	}

	fallback := SuggestTopicKey("bugfix", "", "Fix nil panic in auth middleware on empty token")
	if fallback != "bug/fix-nil-panic-in-auth-middleware-on-empty" {
		t.Fatalf("unexpected fallback topic key: %q", fallback)
	}
}

func TestSuggestTopicKeyInfersFamilyFromTextWhenTypeIsGeneric(t *testing.T) {
	bug := SuggestTopicKey("manual", "", "Fix regression in auth login flow")
	if bug != "bug/fix-regression-in-auth-login-flow" {
		t.Fatalf("expected bug family inference, got %q", bug)
	}

	arch := SuggestTopicKey("", "ADR: Split API gateway boundary", "")
	if arch != "architecture/adr-split-api-gateway-boundary" {
		t.Fatalf("expected architecture family inference, got %q", arch)
	}
}

func TestTopicKeyUpsertIsScopedByProjectAndScope(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	baseID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth model",
		Content:   "Initial architecture",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add base observation: %v", err)
	}

	personalID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth model",
		Content:   "Personal take",
		Project:   "engram",
		Scope:     "personal",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add personal scoped observation: %v", err)
	}

	otherProjectID, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth model",
		Content:   "Other project",
		Project:   "another-project",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add other project observation: %v", err)
	}

	if baseID == personalID || baseID == otherProjectID || personalID == otherProjectID {
		t.Fatalf("expected topic upsert boundaries by project+scope, got ids base=%d personal=%d other=%d", baseID, personalID, otherProjectID)
	}
}

func TestPromptProjectNullScan(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Manually insert a prompt with NULL project to simulate legacy data or external changes
	_, err := s.db.Exec(
		"INSERT INTO user_prompts (session_id, content, project) VALUES (?, ?, NULL)",
		"s1", "prompt with null project",
	)
	if err != nil {
		t.Fatalf("manual insert: %v", err)
	}

	// 1. Test RecentPrompts
	prompts, err := s.RecentPrompts("", 10)
	if err != nil {
		t.Fatalf("RecentPrompts failed with null project: %v", err)
	}
	if len(prompts) != 1 || prompts[0].Project != "" {
		t.Errorf("expected empty string for null project, got %q", prompts[0].Project)
	}

	// 2. Test SearchPrompts
	searchResult, err := s.SearchPrompts("null", "", 10)
	if err != nil {
		t.Fatalf("SearchPrompts failed with null project: %v", err)
	}
	if len(searchResult) != 1 || searchResult[0].Project != "" {
		t.Errorf("expected empty string for null project in search, got %q", searchResult[0].Project)
	}

	// 3. Test Export
	data, err := s.Export()
	if err != nil {
		t.Fatalf("Export failed with null project: %v", err)
	}
	found := false
	for _, p := range data.Prompts {
		if p.Content == "prompt with null project" {
			found = true
			if p.Project != "" {
				t.Errorf("expected empty string for null project in export, got %q", p.Project)
			}
		}
	}
	if !found {
		t.Error("exported prompts missing the test prompt")
	}
}

func TestExportProjectScopesRowsWithoutGlobalDumpFiltering(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session proj-a: %v", err)
	}
	if err := s.CreateSession("sess-b", "proj-b", "/tmp/proj-b"); err != nil {
		t.Fatalf("create session proj-b: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "sess-a", Type: "note", Title: "a", Content: "a", Project: "proj-a", Scope: "project"}); err != nil {
		t.Fatalf("add obs proj-a: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "sess-b", Type: "note", Title: "b", Content: "b", Project: "proj-b", Scope: "project"}); err != nil {
		t.Fatalf("add obs proj-b: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "sess-a", Content: "prompt-a", Project: "proj-a"}); err != nil {
		t.Fatalf("add prompt proj-a: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "sess-b", Content: "prompt-b", Project: "proj-b"}); err != nil {
		t.Fatalf("add prompt proj-b: %v", err)
	}

	data, err := s.ExportProject("proj-a")
	if err != nil {
		t.Fatalf("ExportProject: %v", err)
	}
	if len(data.Sessions) != 1 || data.Sessions[0].Project != "proj-a" {
		t.Fatalf("expected only proj-a sessions, got %+v", data.Sessions)
	}
	if len(data.Observations) != 1 || data.Observations[0].SessionID != "sess-a" {
		t.Fatalf("expected only proj-a observations, got %+v", data.Observations)
	}
	if len(data.Prompts) != 1 || data.Prompts[0].SessionID != "sess-a" {
		t.Fatalf("expected only proj-a prompts, got %+v", data.Prompts)
	}
}

func TestExportProjectPreservesSessionReferentialClosure(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-owned-by-proj-b", "proj-b", "/tmp/proj-b"); err != nil {
		t.Fatalf("create session proj-b: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-owned-by-proj-b",
		Type:      "note",
		Title:     "cross-project obs",
		Content:   "observation references proj-b session",
		Project:   "proj-a",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add cross-project observation: %v", err)
	}

	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-owned-by-proj-b",
		Content:   "cross-project prompt",
		Project:   "proj-a",
	}); err != nil {
		t.Fatalf("add cross-project prompt: %v", err)
	}

	exported, err := s.ExportProject("proj-a")
	if err != nil {
		t.Fatalf("ExportProject: %v", err)
	}

	if len(exported.Observations) != 1 || len(exported.Prompts) != 1 {
		t.Fatalf("expected one cross-project observation and prompt, got obs=%d prompts=%d", len(exported.Observations), len(exported.Prompts))
	}

	foundReferencedSession := false
	for _, sess := range exported.Sessions {
		if sess.ID == "sess-owned-by-proj-b" {
			foundReferencedSession = true
			break
		}
	}
	if !foundReferencedSession {
		t.Fatalf("expected export to include referenced session sess-owned-by-proj-b for referential closure")
	}

	dstCfg := mustDefaultConfig(t)
	dstCfg.DataDir = t.TempDir()
	dst, err := New(dstCfg)
	if err != nil {
		t.Fatalf("new destination store: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	if _, err := dst.Import(exported); err != nil {
		t.Fatalf("import exported project data should succeed with referential closure: %v", err)
	}
}

func TestExportProjectDoesNotLeakRowsOwnedByOtherProjectsViaSessionMembership(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-proj-a", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session proj-a: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-proj-a",
		Type:      "note",
		Title:     "owned-by-proj-b",
		Content:   "should not leak in proj-a export",
		Project:   "proj-b",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add cross-owned observation: %v", err)
	}

	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-proj-a",
		Content:   "prompt owned by proj-b",
		Project:   "proj-b",
	}); err != nil {
		t.Fatalf("add cross-owned prompt: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-proj-a",
		Type:      "note",
		Title:     "projectless observation",
		Content:   "derive ownership from proj-a session",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add projectless observation: %v", err)
	}

	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-proj-a",
		Content:   "projectless prompt",
	}); err != nil {
		t.Fatalf("add projectless prompt: %v", err)
	}

	exported, err := s.ExportProject("proj-a")
	if err != nil {
		t.Fatalf("ExportProject: %v", err)
	}

	if len(exported.Observations) != 1 {
		t.Fatalf("expected only project-owned/projectless-derived observations, got %+v", exported.Observations)
	}
	if exported.Observations[0].Title != "projectless observation" {
		t.Fatalf("expected only projectless-derived observation, got %+v", exported.Observations[0])
	}

	if len(exported.Prompts) != 1 {
		t.Fatalf("expected only project-owned/projectless-derived prompts, got %+v", exported.Prompts)
	}
	if exported.Prompts[0].Content != "projectless prompt" {
		t.Fatalf("expected only projectless-derived prompt, got %+v", exported.Prompts[0])
	}
}

// ─── Passive Capture Tests ───────────────────────────────────────────────────

func TestExtractLearningsNumberedList(t *testing.T) {
	text := `Some preamble text here.

## Key Learnings:

1. bcrypt cost=12 is the right balance for our server performance
2. JWT refresh tokens need atomic rotation to prevent race conditions
3. Always validate the audience claim in JWT tokens before trusting them

## Next Steps
- something else
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 3 {
		t.Fatalf("expected 3 learnings, got %d: %v", len(learnings), learnings)
	}
	if !strings.Contains(learnings[0], "bcrypt") {
		t.Fatalf("expected first learning about bcrypt, got %q", learnings[0])
	}
}

func TestExtractLearningsSpanishHeader(t *testing.T) {
	text := `## Aprendizajes Clave:

1. El costo de bcrypt=12 es el balance correcto para nuestro servidor
2. Los refresh tokens de JWT necesitan rotacion atomica
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 2 {
		t.Fatalf("expected 2 learnings, got %d: %v", len(learnings), learnings)
	}
}

func TestExtractLearningsBulletList(t *testing.T) {
	text := `### Learnings:

- bcrypt cost=12 is the right balance for our server performance
- JWT refresh tokens need atomic rotation to prevent race conditions
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 2 {
		t.Fatalf("expected 2 learnings, got %d: %v", len(learnings), learnings)
	}
}

func TestExtractLearningsIgnoresShortItems(t *testing.T) {
	text := `## Key Learnings:

1. too short
2. bcrypt cost=12 is the right balance for our server performance
3. also short
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 1 {
		t.Fatalf("expected 1 learning (short ones filtered), got %d: %v", len(learnings), learnings)
	}
}

func TestExtractLearningsNoSection(t *testing.T) {
	text := `This is just regular text without any learning section headers.
It has multiple lines but no ## Key Learnings or similar.
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 0 {
		t.Fatalf("expected 0 learnings, got %d: %v", len(learnings), learnings)
	}
}

func TestExtractLearningsSectionPresentButNoValidItems(t *testing.T) {
	text := `## Key Learnings:

1. short
2. tiny
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 0 {
		t.Fatalf("expected 0 learnings when section has no valid items, got %d: %v", len(learnings), learnings)
	}
}

func TestExtractLearningsUsesLastSection(t *testing.T) {
	text := `## Key Learnings:

1. This is from the first section and should be ignored

Some other text here.

## Key Learnings:

1. This is from the last section and should be captured as the real one
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 1 {
		t.Fatalf("expected 1 learning from last section, got %d: %v", len(learnings), learnings)
	}
	if !strings.Contains(learnings[0], "last section") {
		t.Fatalf("expected learning from last section, got %q", learnings[0])
	}
}

func TestExtractLearningsFallsBackWhenLastSectionHasNoValidItems(t *testing.T) {
	text := `## Key Learnings:

1. This is long enough and should be captured from the previous section

## Key Learnings:

1. short
2. tiny
`
	learnings := ExtractLearnings(text)
	if len(learnings) != 1 {
		t.Fatalf("expected fallback to previous valid section, got %d: %v", len(learnings), learnings)
	}
	if !strings.Contains(learnings[0], "previous section") {
		t.Fatalf("expected learning from previous section, got %q", learnings[0])
	}
}

func TestExtractLearningsCleansMarkdown(t *testing.T) {
	text := "## Key Learnings:\n\n1. **Use** `context.Context` in *all* handlers to support cancellation correctly\n"
	learnings := ExtractLearnings(text)
	if len(learnings) != 1 {
		t.Fatalf("expected 1 learning, got %d: %v", len(learnings), learnings)
	}
	if strings.Contains(learnings[0], "**") || strings.Contains(learnings[0], "`") || strings.Contains(learnings[0], "*") {
		t.Fatalf("expected markdown to be stripped, got %q", learnings[0])
	}
}

func TestPassiveCaptureStoresLearnings(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	text := `## Key Learnings:

1. bcrypt cost=12 is the right balance for our server performance
2. JWT refresh tokens need atomic rotation to prevent race conditions
`
	result, err := s.PassiveCapture(PassiveCaptureParams{
		SessionID: "s1",
		Content:   text,
		Project:   "engram",
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("passive capture: %v", err)
	}
	if result.Extracted != 2 {
		t.Fatalf("expected 2 extracted, got %d", result.Extracted)
	}
	if result.Saved != 2 {
		t.Fatalf("expected 2 saved, got %d", result.Saved)
	}

	obs, err := s.AllObservations("engram", "", 10)
	if err != nil {
		t.Fatalf("all observations: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(obs))
	}
	for _, o := range obs {
		if o.Type != "passive" {
			t.Fatalf("expected type=passive, got %q", o.Type)
		}
	}
	if obs[0].ToolName == nil || *obs[0].ToolName != "test" {
		t.Fatalf("expected tool_name source to be stored as 'test', got %+v", obs[0].ToolName)
	}
}

func TestPassiveCaptureEmptyContent(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	result, err := s.PassiveCapture(PassiveCaptureParams{
		SessionID: "s1",
		Content:   "",
		Project:   "engram",
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("passive capture: %v", err)
	}
	if result.Extracted != 0 || result.Saved != 0 {
		t.Fatalf("expected 0 extracted and 0 saved, got %d/%d", result.Extracted, result.Saved)
	}
}

func TestPassiveCaptureDedupesAgainstExistingObservations(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// First: agent saves actively via mem_save
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "bcrypt cost",
		Content:   "bcrypt cost=12 is the right balance for our server performance",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add active observation: %v", err)
	}

	// Then: passive capture fires with overlapping content
	text := `## Key Learnings:

1. bcrypt cost=12 is the right balance for our server performance
2. JWT refresh tokens need atomic rotation to prevent race conditions
`
	result, err := s.PassiveCapture(PassiveCaptureParams{
		SessionID: "s1",
		Content:   text,
		Project:   "engram",
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("passive capture: %v", err)
	}
	if result.Extracted != 2 {
		t.Fatalf("expected 2 extracted, got %d", result.Extracted)
	}
	if result.Saved != 1 {
		t.Fatalf("expected 1 saved (1 deduped), got %d", result.Saved)
	}
	if result.Duplicates != 1 {
		t.Fatalf("expected 1 duplicate, got %d", result.Duplicates)
	}
}

func TestPassiveCaptureReturnsErrorWhenSessionDoesNotExist(t *testing.T) {
	s := newTestStore(t)

	text := `## Key Learnings:

1. This learning is long enough to attempt insert and fail without session
`
	_, err := s.PassiveCapture(PassiveCaptureParams{
		SessionID: "missing-session",
		Content:   text,
		Project:   "engram",
		Source:    "test",
	})
	if err == nil {
		t.Fatalf("expected error when session does not exist")
	}
}

func TestStatsProjectsOrderedByMostRecentObservation(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session s1: %v", err)
	}
	if err := s.CreateSession("s2", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session s2: %v", err)
	}

	_, err := s.db.Exec(
		`INSERT INTO observations (session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?),
		        (?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)`,
		"s1", "note", "older", "older alpha", "alpha", "project", hashNormalized("older alpha"), "2026-02-01 10:00:00", "2026-02-01 10:00:00",
		"s2", "note", "newer", "newer beta", "beta", "project", hashNormalized("newer beta"), "2026-02-02 10:00:00", "2026-02-02 10:00:00",
	)
	if err != nil {
		t.Fatalf("insert observations: %v", err)
	}

	stats, err := s.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats.Projects) < 2 {
		t.Fatalf("expected at least 2 projects, got %d", len(stats.Projects))
	}

	if stats.Projects[0] != "beta" || stats.Projects[1] != "alpha" {
		t.Fatalf("expected recency order [beta alpha], got %v", stats.Projects[:2])
	}
}

func TestSessionsOrderedByMostRecentActivity(t *testing.T) {
	s := newTestStore(t)

	_, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory, started_at) VALUES
		 (?, ?, ?, ?),
		 (?, ?, ?, ?)`,
		"s-older", "engram", "/tmp/engram", "2026-02-01 09:00:00",
		"s-newer", "engram", "/tmp/engram", "2026-02-02 09:00:00",
	)
	if err != nil {
		t.Fatalf("insert sessions: %v", err)
	}

	_, err = s.db.Exec(
		`INSERT INTO observations (session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)`,
		"s-older", "note", "latest", "session old got new activity", "engram", "project", hashNormalized("session old got new activity"), "2026-02-03 09:00:00", "2026-02-03 09:00:00",
	)
	if err != nil {
		t.Fatalf("insert latest observation: %v", err)
	}

	all, err := s.AllSessions("", 10)
	if err != nil {
		t.Fatalf("all sessions: %v", err)
	}
	if len(all) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(all))
	}
	if all[0].ID != "s-older" {
		t.Fatalf("expected s-older first in all sessions, got %s", all[0].ID)
	}

	recent, err := s.RecentSessions("", 10)
	if err != nil {
		t.Fatalf("recent sessions: %v", err)
	}
	if len(recent) < 2 {
		t.Fatalf("expected at least 2 recent sessions, got %d", len(recent))
	}
	if recent[0].ID != "s-older" {
		t.Fatalf("expected s-older first in recent sessions, got %s", recent[0].ID)
	}
}

func TestSessionObservationsAddPromptImportAndSyncChunks(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Auth",
		Content:   "Use middleware chain",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	longPrompt := strings.Repeat("x", s.cfg.MaxObservationLength+25)
	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "s1", Content: longPrompt, Project: "engram"})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	if promptID <= 0 {
		t.Fatalf("expected valid prompt id, got %d", promptID)
	}

	sessionObs, err := s.SessionObservations("s1", 0)
	if err != nil {
		t.Fatalf("session observations: %v", err)
	}
	if len(sessionObs) != 1 {
		t.Fatalf("expected 1 session observation, got %d", len(sessionObs))
	}

	exported, err := s.Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	dst, err := New(cfg)
	if err != nil {
		t.Fatalf("new destination store: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	imported, err := dst.Import(exported)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported.SessionsImported < 1 || imported.ObservationsImported < 1 || imported.PromptsImported < 1 {
		t.Fatalf("expected non-zero import counts, got %+v", imported)
	}

	if err := dst.RecordSyncedChunk("chunk-1"); err != nil {
		t.Fatalf("record synced chunk: %v", err)
	}
	chunks, err := dst.GetSyncedChunks()
	if err != nil {
		t.Fatalf("get synced chunks: %v", err)
	}
	if !chunks["chunk-1"] {
		t.Fatalf("expected chunk-1 to be marked as synced")
	}

	if err := dst.RecordSyncedChunkForTarget(DefaultSyncTargetKey, "chunk-1"); err != nil {
		t.Fatalf("record cloud-target synced chunk: %v", err)
	}
	localChunks, err := dst.GetSyncedChunksForTarget(LocalChunkTargetKey)
	if err != nil {
		t.Fatalf("get local synced chunks: %v", err)
	}
	cloudChunks, err := dst.GetSyncedChunksForTarget(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get cloud synced chunks: %v", err)
	}
	if !localChunks["chunk-1"] {
		t.Fatal("expected chunk-1 to exist in local chunk target")
	}
	if !cloudChunks["chunk-1"] {
		t.Fatal("expected chunk-1 to exist in cloud chunk target")
	}
}

func TestStoreLocalSyncFoundationEnqueuesCoreMutations(t *testing.T) {
	s := newTestStore(t)

	// Enroll "engram" so mutations are visible via ListPendingSyncMutations.
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	if err := s.CreateSession("sync-session", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "sync-session",
		Type:      "decision",
		Title:     "Initial title",
		Content:   "Initial content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	updatedTitle := "Updated title"
	updatedContent := "Updated content"
	if _, err := s.UpdateObservation(obsID, UpdateObservationParams{
		Title:   &updatedTitle,
		Content: &updatedContent,
	}); err != nil {
		t.Fatalf("update observation: %v", err)
	}

	if err := s.DeleteObservation(obsID, false); err != nil {
		t.Fatalf("soft delete observation: %v", err)
	}

	promptID, err := s.AddPrompt(AddPromptParams{
		SessionID: "sync-session",
		Content:   "How do we keep this local-first?",
		Project:   "engram",
	})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	if err := s.EndSession("sync-session", "done"); err != nil {
		t.Fatalf("end session: %v", err)
	}

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.TargetKey != DefaultSyncTargetKey {
		t.Fatalf("expected target %q, got %q", DefaultSyncTargetKey, state.TargetKey)
	}
	if state.Lifecycle != SyncLifecyclePending {
		t.Fatalf("expected pending lifecycle after local writes, got %q", state.Lifecycle)
	}
	if state.LastEnqueuedSeq != 6 {
		t.Fatalf("expected 6 enqueued mutations, got %d", state.LastEnqueuedSeq)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending sync mutations: %v", err)
	}
	if len(mutations) != 6 {
		t.Fatalf("expected 6 pending mutations, got %d", len(mutations))
	}

	var observationSyncID string
	if err := s.db.QueryRow("SELECT sync_id FROM observations WHERE id = ?", obsID).Scan(&observationSyncID); err != nil {
		t.Fatalf("lookup observation sync id: %v", err)
	}
	if observationSyncID == "" {
		t.Fatalf("expected observation sync id to be persisted")
	}

	var promptSyncID string
	if err := s.db.QueryRow("SELECT sync_id FROM user_prompts WHERE id = ?", promptID).Scan(&promptSyncID); err != nil {
		t.Fatalf("lookup prompt sync id: %v", err)
	}
	if promptSyncID == "" {
		t.Fatalf("expected prompt sync id to be persisted")
	}

	if mutations[0].Entity != SyncEntitySession || mutations[0].EntityKey != "sync-session" || mutations[0].Op != SyncOpUpsert {
		t.Fatalf("unexpected session mutation: %+v", mutations[0])
	}
	if mutations[1].Entity != SyncEntityObservation || mutations[1].EntityKey != observationSyncID || mutations[1].Op != SyncOpUpsert {
		t.Fatalf("unexpected observation insert mutation: %+v", mutations[1])
	}
	if mutations[2].Entity != SyncEntityObservation || mutations[2].EntityKey != observationSyncID || mutations[2].Op != SyncOpUpsert {
		t.Fatalf("unexpected observation update mutation: %+v", mutations[2])
	}
	if mutations[3].Entity != SyncEntityObservation || mutations[3].EntityKey != observationSyncID || mutations[3].Op != SyncOpDelete {
		t.Fatalf("unexpected observation delete mutation: %+v", mutations[3])
	}
	if mutations[4].Entity != SyncEntityPrompt || mutations[4].EntityKey != promptSyncID || mutations[4].Op != SyncOpUpsert {
		t.Fatalf("unexpected prompt mutation: %+v", mutations[4])
	}
	if mutations[5].Entity != SyncEntitySession || mutations[5].EntityKey != "sync-session" || mutations[5].Op != SyncOpUpsert {
		t.Fatalf("unexpected end session mutation: %+v", mutations[5])
	}

	var deletedPayload map[string]any
	if err := json.Unmarshal([]byte(mutations[3].Payload), &deletedPayload); err != nil {
		t.Fatalf("decode delete payload: %v", err)
	}
	if deletedPayload["sync_id"] != observationSyncID {
		t.Fatalf("expected delete payload sync id %q, got %#v", observationSyncID, deletedPayload["sync_id"])
	}
	if deletedPayload["deleted"] != true {
		t.Fatalf("expected delete payload to mark deleted=true, got %#v", deletedPayload["deleted"])
	}

	if err := s.AckSyncMutations(DefaultSyncTargetKey, mutations[3].Seq); err != nil {
		t.Fatalf("ack sync mutations: %v", err)
	}
	remaining, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list remaining sync mutations: %v", err)
	}
	if len(remaining) != 2 || remaining[0].Entity != SyncEntityPrompt || remaining[1].Entity != SyncEntitySession {
		t.Fatalf("expected prompt and end-session mutations to remain pending, got %+v", remaining)
	}
}

func TestStoreLocalSyncFoundationStateHelpers(t *testing.T) {
	s := newTestStore(t)

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get initial sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleIdle {
		t.Fatalf("expected idle lifecycle, got %q", state.Lifecycle)
	}

	acquired, err := s.AcquireSyncLease(DefaultSyncTargetKey, "worker-a", 2*time.Minute, time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	if !acquired {
		t.Fatalf("expected first lease acquisition to succeed")
	}

	acquired, err = s.AcquireSyncLease(DefaultSyncTargetKey, "worker-b", 2*time.Minute, time.Date(2026, 3, 7, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("acquire conflicting lease: %v", err)
	}
	if acquired {
		t.Fatalf("expected conflicting lease acquisition to fail")
	}

	if err := s.ReleaseSyncLease(DefaultSyncTargetKey, "worker-a"); err != nil {
		t.Fatalf("release lease: %v", err)
	}

	acquired, err = s.AcquireSyncLease(DefaultSyncTargetKey, "worker-b", 2*time.Minute, time.Date(2026, 3, 7, 12, 2, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("acquire released lease: %v", err)
	}
	if !acquired {
		t.Fatalf("expected lease acquisition after release to succeed")
	}

	if err := s.MarkSyncFailure(DefaultSyncTargetKey, "timeout talking to cloud", time.Date(2026, 3, 7, 12, 10, 0, 0, time.UTC)); err != nil {
		t.Fatalf("mark sync failure: %v", err)
	}

	state, err = s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get degraded sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleDegraded {
		t.Fatalf("expected degraded lifecycle, got %q", state.Lifecycle)
	}
	if state.ConsecutiveFailures != 1 {
		t.Fatalf("expected failure count 1, got %d", state.ConsecutiveFailures)
	}
	if state.LastError == nil || *state.LastError != "timeout talking to cloud" {
		t.Fatalf("expected last error to be stored, got %+v", state.LastError)
	}
	if state.BackoffUntil == nil || *state.BackoffUntil != "2026-03-07T12:10:00Z" {
		t.Fatalf("expected backoff timestamp to be stored, got %+v", state.BackoffUntil)
	}

	if err := s.MarkSyncHealthy(DefaultSyncTargetKey); err != nil {
		t.Fatalf("mark sync healthy: %v", err)
	}

	state, err = s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get healthy sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleHealthy {
		t.Fatalf("expected healthy lifecycle, got %q", state.Lifecycle)
	}
	if state.ConsecutiveFailures != 0 || state.LastError != nil || state.BackoffUntil != nil {
		t.Fatalf("expected healthy state to clear failure metadata, got %+v", state)
	}
}

func TestAckSyncMutationSeqsRefreshesProjectScopedState(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("sess-proj", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-proj",
		Type:      "note",
		Title:     "proj scoped",
		Content:   "pending",
		Project:   "proj-a",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 20)
	if err != nil {
		t.Fatalf("list pending mutations: %v", err)
	}
	if len(mutations) < 2 {
		t.Fatalf("expected at least session + observation mutations, got %+v", mutations)
	}

	projectTarget := syncTargetKeyForProject("proj-a")
	before, err := s.GetSyncState(projectTarget)
	if err != nil {
		t.Fatalf("get project sync state before ack: %v", err)
	}
	if before.Lifecycle != SyncLifecyclePending {
		t.Fatalf("expected project lifecycle pending before ack, got %q", before.Lifecycle)
	}

	seqs := make([]int64, 0, len(mutations))
	for _, mutation := range mutations {
		if mutation.Project == "proj-a" {
			seqs = append(seqs, mutation.Seq)
		}
	}
	if len(seqs) == 0 {
		t.Fatalf("expected project-scoped pending mutations, got %+v", mutations)
	}

	if err := s.AckSyncMutationSeqs(DefaultSyncTargetKey, seqs); err != nil {
		t.Fatalf("ack project mutation seqs: %v", err)
	}

	after, err := s.GetSyncState(projectTarget)
	if err != nil {
		t.Fatalf("get project sync state after ack: %v", err)
	}
	if after.Lifecycle != SyncLifecycleHealthy {
		t.Fatalf("expected project lifecycle healthy after ack, got %q", after.Lifecycle)
	}
	if after.LastAckedSeq < after.LastEnqueuedSeq {
		t.Fatalf("expected project ack counters reconciled, got enqueued=%d acked=%d", after.LastEnqueuedSeq, after.LastAckedSeq)
	}
	if after.ReasonCode != nil || after.ReasonMessage != nil || after.LastError != nil || after.BackoffUntil != nil {
		t.Fatalf("expected ack reconciliation to clear degraded metadata for healthy state, got %+v", after)
	}

	hasPending, err := s.HasPendingSyncMutationsForProject("proj-a")
	if err != nil {
		t.Fatalf("pending project mutations query: %v", err)
	}
	if hasPending {
		t.Fatalf("expected no pending project mutations after ack")
	}
}

func TestAckSyncMutationsRefreshesProjectStateAndClearsDegradedMetadata(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnrollProject("proj-a"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	if err := s.CreateSession("sess-proj", "proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-proj",
		Type:      "note",
		Title:     "proj scoped",
		Content:   "pending",
		Project:   "proj-a",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	projectTarget := syncTargetKeyForProject("proj-a")
	if err := s.MarkSyncFailure(projectTarget, "seed degraded before ack", time.Now().UTC().Add(-45*time.Second)); err != nil {
		t.Fatalf("seed degraded project state: %v", err)
	}

	allMutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 20)
	if err != nil {
		t.Fatalf("list pending mutations: %v", err)
	}
	var maxProjectSeq int64
	for _, mutation := range allMutations {
		if mutation.Project == "proj-a" && mutation.Seq > maxProjectSeq {
			maxProjectSeq = mutation.Seq
		}
	}
	if maxProjectSeq == 0 {
		t.Fatalf("expected pending project mutations, got %+v", allMutations)
	}

	if err := s.AckSyncMutations(DefaultSyncTargetKey, maxProjectSeq); err != nil {
		t.Fatalf("ack sync mutations: %v", err)
	}

	state, err := s.GetSyncState(projectTarget)
	if err != nil {
		t.Fatalf("get project sync state after ack: %v", err)
	}
	if state.Lifecycle != SyncLifecycleHealthy {
		t.Fatalf("expected project lifecycle healthy after full ack, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.ReasonMessage != nil || state.LastError != nil || state.BackoffUntil != nil {
		t.Fatalf("expected healthy project state to clear degraded metadata, got %+v", state)
	}
}

func TestAckSyncMutationsPreservesActivelyDegradedProjectState(t *testing.T) {
	s := newTestStore(t)
	targetKey := syncTargetKeyForProject("proj-a")
	if err := s.MarkSyncBlocked(targetKey, "blocked_unenrolled", "project is blocked by policy"); err != nil {
		t.Fatalf("seed actively degraded state: %v", err)
	}

	if err := s.AckSyncMutations(DefaultSyncTargetKey, 1); err != nil {
		t.Fatalf("ack sync mutations: %v", err)
	}

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleDegraded {
		t.Fatalf("expected actively degraded state to remain degraded, got %q", state.Lifecycle)
	}
	if state.ReasonCode == nil || *state.ReasonCode != "blocked_unenrolled" {
		t.Fatalf("expected blocked_unenrolled reason to be preserved, got %v", state.ReasonCode)
	}
}

func TestSyncStateDeterministicReasonCodes(t *testing.T) {
	s := newTestStore(t)

	tests := []struct {
		name      string
		mark      func() error
		reason    string
		msgSubstr string
	}{
		{
			name: "blocked unenrolled",
			mark: func() error {
				return s.MarkSyncBlocked(DefaultSyncTargetKey, "blocked_unenrolled", "project not enrolled for cloud replication")
			},
			reason:    "blocked_unenrolled",
			msgSubstr: "not enrolled",
		},
		{
			name: "paused",
			mark: func() error {
				return s.MarkSyncPaused(DefaultSyncTargetKey, "cloud sync paused by organization policy")
			},
			reason:    "paused",
			msgSubstr: "paused",
		},
		{
			name: "auth required",
			mark: func() error {
				return s.MarkSyncAuthRequired(DefaultSyncTargetKey, "cloud token is missing")
			},
			reason:    "auth_required",
			msgSubstr: "missing",
		},
		{
			name: "transport failed",
			mark: func() error {
				return s.MarkSyncFailure(DefaultSyncTargetKey, "dial tcp timeout", time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC))
			},
			reason:    "transport_failed",
			msgSubstr: "timeout",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.mark(); err != nil {
				t.Fatalf("mark sync state: %v", err)
			}

			state, err := s.GetSyncState(DefaultSyncTargetKey)
			if err != nil {
				t.Fatalf("get sync state: %v", err)
			}
			if state.ReasonCode == nil || *state.ReasonCode != tc.reason {
				t.Fatalf("expected reason_code=%q, got %+v", tc.reason, state.ReasonCode)
			}
			if state.ReasonMessage == nil || !strings.Contains(*state.ReasonMessage, tc.msgSubstr) {
				t.Fatalf("expected reason_message containing %q, got %+v", tc.msgSubstr, state.ReasonMessage)
			}
		})
	}

	if err := s.MarkSyncHealthy(DefaultSyncTargetKey); err != nil {
		t.Fatalf("mark healthy: %v", err)
	}
	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.ReasonCode != nil || state.ReasonMessage != nil {
		t.Fatalf("expected healthy state to clear reasons, got code=%v message=%v", state.ReasonCode, state.ReasonMessage)
	}
}

func TestUpgradeStateSnapshotLifecycle(t *testing.T) {
	s := newTestStore(t)
	project := "upgrade-proj"

	initial, err := s.GetCloudUpgradeState(project)
	if err != nil {
		t.Fatalf("get initial cloud upgrade state: %v", err)
	}
	if initial != nil {
		t.Fatalf("expected nil initial upgrade state, got %+v", initial)
	}

	snapshot := CloudUpgradeSnapshot{
		CloudConfigPresent: true,
		CloudConfigJSON:    `{"server_url":"https://cloud.example.test"}`,
		ProjectEnrolled:    false,
	}
	state := CloudUpgradeState{
		Project:          project,
		Stage:            UpgradeStageBootstrapEnrolled,
		RepairClass:      UpgradeRepairClassRepairable,
		Snapshot:         snapshot,
		LastErrorCode:    "upgrade_blocked_manual",
		LastErrorMessage: "manual fix required",
	}
	if err := s.SaveCloudUpgradeState(state); err != nil {
		t.Fatalf("save upgrade state: %v", err)
	}

	stored, err := s.GetCloudUpgradeState(project)
	if err != nil {
		t.Fatalf("get stored cloud upgrade state: %v", err)
	}
	if stored == nil {
		t.Fatal("expected stored upgrade state")
	}
	if stored.Stage != UpgradeStageBootstrapEnrolled {
		t.Fatalf("expected stage %q, got %q", UpgradeStageBootstrapEnrolled, stored.Stage)
	}
	if !stored.Snapshot.CloudConfigPresent || stored.Snapshot.CloudConfigJSON == "" {
		t.Fatalf("expected snapshot to roundtrip, got %+v", stored.Snapshot)
	}

	allowed, err := s.CanRollbackCloudUpgrade(project)
	if err != nil {
		t.Fatalf("can rollback before verification: %v", err)
	}
	if !allowed {
		t.Fatal("expected rollback allowed before bootstrap verification")
	}

	state.Stage = UpgradeStageBootstrapVerified
	if err := s.SaveCloudUpgradeState(state); err != nil {
		t.Fatalf("save verified stage: %v", err)
	}

	allowed, err = s.CanRollbackCloudUpgrade(project)
	if err != nil {
		t.Fatalf("can rollback after verification: %v", err)
	}
	if allowed {
		t.Fatal("expected rollback blocked after bootstrap verification")
	}

	if err := s.ClearCloudUpgradeState(project); err != nil {
		t.Fatalf("clear upgrade state: %v", err)
	}

	afterClear, err := s.GetCloudUpgradeState(project)
	if err != nil {
		t.Fatalf("get cleared upgrade state: %v", err)
	}
	if afterClear != nil {
		t.Fatalf("expected nil upgrade state after clear, got %+v", afterClear)
	}
}

func TestUpgradeRepairDryRunAndApply(t *testing.T) {
	t.Run("dry-run is deterministic and non-mutating", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("repair-s1", "repair-proj", "/tmp/repair"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "repair-s1", Type: "decision", Title: "t", Content: "c", Project: "repair-proj", Scope: "project"}); err != nil {
			t.Fatalf("add observation: %v", err)
		}
		if _, err := s.AddPrompt(AddPromptParams{SessionID: "repair-s1", Content: "p", Project: "repair-proj"}); err != nil {
			t.Fatalf("add prompt: %v", err)
		}
		if err := s.EnrollProject("repair-proj"); err != nil {
			t.Fatalf("enroll project: %v", err)
		}

		if _, err := s.execHook(s.db, `
			DELETE FROM sync_mutations
			WHERE seq IN (
				SELECT seq FROM sync_mutations WHERE project = ? AND entity = ? ORDER BY seq ASC LIMIT 1
			)
		`, "repair-proj", SyncEntityObservation); err != nil {
			t.Fatalf("delete mutation for repair setup: %v", err)
		}

		beforeCount := 0
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = ?`, "repair-proj").Scan(&beforeCount); err != nil {
			t.Fatalf("count before dry-run: %v", err)
		}

		report1, err := s.RepairCloudUpgrade("repair-proj", false)
		if err != nil {
			t.Fatalf("dry-run repair: %v", err)
		}
		report2, err := s.RepairCloudUpgrade("repair-proj", false)
		if err != nil {
			t.Fatalf("second dry-run repair: %v", err)
		}
		if report1 != report2 {
			t.Fatalf("expected deterministic dry-run report, got %+v and %+v", report1, report2)
		}
		if report1.Class != UpgradeRepairClassRepairable {
			t.Fatalf("expected repairable class, got %+v", report1)
		}

		afterCount := 0
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE project = ?`, "repair-proj").Scan(&afterCount); err != nil {
			t.Fatalf("count after dry-run: %v", err)
		}
		if beforeCount != afterCount {
			t.Fatalf("dry-run must not mutate local state, before=%d after=%d", beforeCount, afterCount)
		}
	})

	t.Run("apply backfills safe local fixes", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("repair-s2", "repair-apply", "/tmp/repair-apply"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "repair-s2", Type: "decision", Title: "t", Content: "c", Project: "repair-apply", Scope: "project"}); err != nil {
			t.Fatalf("add observation: %v", err)
		}
		if err := s.EnrollProject("repair-apply"); err != nil {
			t.Fatalf("enroll project: %v", err)
		}
		if _, err := s.execHook(s.db, `
			DELETE FROM sync_mutations
			WHERE seq IN (
				SELECT seq FROM sync_mutations WHERE project = ? AND entity = ? ORDER BY seq ASC LIMIT 1
			)
		`, "repair-apply", SyncEntityObservation); err != nil {
			t.Fatalf("delete mutation for apply setup: %v", err)
		}

		report, err := s.RepairCloudUpgrade("repair-apply", true)
		if err != nil {
			t.Fatalf("apply repair: %v", err)
		}
		if report.Class != UpgradeRepairClassRepairable || !report.Applied {
			t.Fatalf("expected applied repairable result, got %+v", report)
		}

		pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 20)
		if err != nil {
			t.Fatalf("list pending mutations: %v", err)
		}
		foundObservation := false
		for _, mutation := range pending {
			if mutation.Project == "repair-apply" && mutation.Entity == SyncEntityObservation {
				foundObservation = true
				break
			}
		}
		if !foundObservation {
			t.Fatal("expected observation mutation to be backfilled by apply")
		}
	})

	t.Run("blocked ambiguity is not auto-mutated", func(t *testing.T) {
		s := newTestStore(t)
		report, err := s.RepairCloudUpgrade("unregistered-proj", true)
		if err != nil {
			t.Fatalf("blocked repair report: %v", err)
		}
		if report.Class != UpgradeRepairClassBlocked || report.Applied {
			t.Fatalf("expected blocked non-applied report, got %+v", report)
		}
	})

	t.Run("auth and policy blockers are manual-action-required", func(t *testing.T) {
		tests := []struct {
			name       string
			reasonCode string
			message    string
			wantClass  string
		}{
			{name: "auth required", reasonCode: "auth_required", message: "token expired", wantClass: UpgradeRepairClassPolicy},
			{name: "policy forbidden", reasonCode: "policy_forbidden", message: "project denied by org policy", wantClass: UpgradeRepairClassPolicy},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				s := newTestStore(t)
				if err := s.MarkSyncBlocked("cloud:repair-policy", tc.reasonCode, tc.message); err != nil {
					t.Fatalf("seed sync blocked state: %v", err)
				}

				report, err := s.RepairCloudUpgrade("repair-policy", true)
				if err != nil {
					t.Fatalf("repair report: %v", err)
				}
				if report.Class != tc.wantClass || report.Applied {
					t.Fatalf("expected class=%s applied=false, got %+v", tc.wantClass, report)
				}
				if !strings.Contains(report.Message, "manual-action-required") {
					t.Fatalf("expected manual-action-required guidance, got %q", report.Message)
				}
			})
		}
	})

	t.Run("legacy mutation required fields are detected and repaired from authoritative local state", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("legacy-s1", "legacy-proj", "/tmp/legacy"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "legacy-s1", Type: "decision", Title: "Authoritative title", Content: "Authoritative content", Project: "legacy-proj", Scope: "project"}); err != nil {
			t.Fatalf("add observation: %v", err)
		}
		if err := s.EnrollProject("legacy-proj"); err != nil {
			t.Fatalf("enroll project: %v", err)
		}

		var syncID string
		if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE session_id = ? ORDER BY id DESC LIMIT 1`, "legacy-s1").Scan(&syncID); err != nil {
			t.Fatalf("lookup observation sync id: %v", err)
		}

		payload := `{"sync_id":"` + syncID + `","session_id":"legacy-s1","type":"decision","content":"legacy payload missing title","scope":"project"}`
		if _, err := s.execHook(s.db,
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey,
			SyncEntityObservation,
			syncID,
			SyncOpUpsert,
			payload,
			SyncSourceLocal,
			"legacy-proj",
		); err != nil {
			t.Fatalf("insert malformed legacy mutation: %v", err)
		}

		diagnosis, err := s.DiagnoseCloudUpgradeLegacyMutations("legacy-proj")
		if err != nil {
			t.Fatalf("diagnose legacy mutations: %v", err)
		}
		if diagnosis.RepairableCount == 0 || diagnosis.BlockedCount != 0 {
			t.Fatalf("expected repairable-only diagnosis, got %+v", diagnosis)
		}
		if len(diagnosis.Findings) == 0 || !diagnosis.Findings[0].Repairable {
			t.Fatalf("expected at least one repairable finding, got %+v", diagnosis.Findings)
		}

		report, err := s.RepairCloudUpgrade("legacy-proj", true)
		if err != nil {
			t.Fatalf("repair legacy payload gaps: %v", err)
		}
		if report.Class != UpgradeRepairClassRepairable || !report.Applied {
			t.Fatalf("expected applied repairable result, got %+v", report)
		}

		var repairedPayload string
		if err := s.db.QueryRow(`
			SELECT payload FROM sync_mutations
			WHERE target_key = ? AND project = ? AND entity = ? AND entity_key = ? AND op = ?
			ORDER BY seq DESC LIMIT 1
		`, DefaultSyncTargetKey, "legacy-proj", SyncEntityObservation, syncID, SyncOpUpsert).Scan(&repairedPayload); err != nil {
			t.Fatalf("load repaired payload: %v", err)
		}
		var repaired syncObservationPayload
		if err := decodeSyncPayload([]byte(repairedPayload), &repaired); err != nil {
			t.Fatalf("decode repaired payload: %v", err)
		}
		if strings.TrimSpace(repaired.Title) == "" {
			t.Fatalf("expected repaired payload title from authoritative local observation, got %+v", repaired)
		}

		after, err := s.DiagnoseCloudUpgradeLegacyMutations("legacy-proj")
		if err != nil {
			t.Fatalf("diagnose after repair: %v", err)
		}
		if after.RepairableCount != 0 || after.BlockedCount != 0 || len(after.Findings) != 0 {
			t.Fatalf("expected no remaining legacy findings after repair, got %+v", after)
		}
	})
}

func TestRollbackCloudUpgradeSafetyBoundary(t *testing.T) {
	t.Run("rollback before bootstrap verification restores snapshot enrollment", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("rb-s1", "rb-proj", "/tmp/rb"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if err := s.EnrollProject("rb-proj"); err != nil {
			t.Fatalf("seed enrolled project: %v", err)
		}
		if err := s.SaveCloudUpgradeState(CloudUpgradeState{
			Project:     "rb-proj",
			Stage:       UpgradeStageBootstrapPushed,
			RepairClass: UpgradeRepairClassRepairable,
			Snapshot: CloudUpgradeSnapshot{
				CloudConfigPresent: true,
				CloudConfigJSON:    `{"server_url":"https://cloud.example.test"}`,
				ProjectEnrolled:    false,
			},
		}); err != nil {
			t.Fatalf("seed upgrade state: %v", err)
		}

		rolledBack, err := s.RollbackCloudUpgrade("rb-proj")
		if err != nil {
			t.Fatalf("rollback before verification: %v", err)
		}
		if rolledBack.Stage != UpgradeStageRolledBack {
			t.Fatalf("expected rolled_back stage, got %q", rolledBack.Stage)
		}
		enrolled, err := s.IsProjectEnrolled("rb-proj")
		if err != nil {
			t.Fatalf("verify enrollment: %v", err)
		}
		if enrolled {
			t.Fatal("expected rollback to restore unenrolled snapshot state")
		}
	})

	t.Run("rollback after bootstrap verification fails loudly", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.SaveCloudUpgradeState(CloudUpgradeState{
			Project:     "rb-verified",
			Stage:       UpgradeStageBootstrapVerified,
			RepairClass: UpgradeRepairClassReady,
			Snapshot: CloudUpgradeSnapshot{
				CloudConfigPresent: true,
				ProjectEnrolled:    true,
			},
		}); err != nil {
			t.Fatalf("seed verified state: %v", err)
		}

		_, err := s.RollbackCloudUpgrade("rb-verified")
		if err == nil || !strings.Contains(err.Error(), "rollback is unavailable post-bootstrap") {
			t.Fatalf("expected loud post-boundary failure, got %v", err)
		}
	})
}

func TestMarkSyncBlockedResetsConsecutiveFailures(t *testing.T) {
	s := newTestStore(t)
	if err := s.MarkSyncFailure(DefaultSyncTargetKey, "transport timeout", time.Now().UTC().Add(30*time.Second)); err != nil {
		t.Fatalf("mark sync failure: %v", err)
	}
	if err := s.MarkSyncBlocked(DefaultSyncTargetKey, "blocked_unenrolled", "project not enrolled"); err != nil {
		t.Fatalf("mark sync blocked: %v", err)
	}

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.ConsecutiveFailures != 0 {
		t.Fatalf("expected blocked state to reset consecutive failures, got %d", state.ConsecutiveFailures)
	}
}

func TestMarkSyncHealthyCreatesSyncStateWhenMissing(t *testing.T) {
	s := newTestStore(t)
	targetKey := "cloud:proj-a"

	if err := s.MarkSyncHealthy(targetKey); err != nil {
		t.Fatalf("mark healthy on missing row: %v", err)
	}

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleHealthy {
		t.Fatalf("expected healthy lifecycle, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.ReasonMessage != nil || state.LastError != nil {
		t.Fatalf("expected healthy state without degraded reasons/errors, got %+v", state)
	}
}

func TestMarkSyncPendingClearsDegradedMetadata(t *testing.T) {
	s := newTestStore(t)
	targetKey := "cloud:proj-a"

	if err := s.MarkSyncFailure(targetKey, "dial tcp timeout", time.Now().UTC().Add(30*time.Second)); err != nil {
		t.Fatalf("seed degraded state: %v", err)
	}

	if err := s.MarkSyncPending(targetKey); err != nil {
		t.Fatalf("mark pending: %v", err)
	}

	state, err := s.GetSyncState(targetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecyclePending {
		t.Fatalf("expected pending lifecycle, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.ReasonMessage != nil || state.LastError != nil || state.BackoffUntil != nil {
		t.Fatalf("expected pending state to clear degraded metadata, got %+v", state)
	}
}

func TestApplyRemoteMutationIdempotent(t *testing.T) {
	s := newTestStore(t)

	create := SyncMutation{
		Seq:       41,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-session",
		Op:        SyncOpUpsert,
		Payload:   `{"id":"remote-session","project":"engram","directory":"/remote"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, create); err != nil {
		t.Fatalf("apply session mutation: %v", err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, create); err != nil {
		t.Fatalf("reapply session mutation: %v", err)
	}

	obsMutation := SyncMutation{
		Seq:       42,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntityObservation,
		EntityKey: "obs-remote-1",
		Op:        SyncOpUpsert,
		Payload:   `{"sync_id":"obs-remote-1","session_id":"remote-session","type":"decision","title":"Remote","content":"Pulled from cloud","project":"engram","scope":"project"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, obsMutation); err != nil {
		t.Fatalf("apply observation mutation: %v", err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, obsMutation); err != nil {
		t.Fatalf("reapply observation mutation: %v", err)
	}

	var rowCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM observations WHERE sync_id = ?", "obs-remote-1").Scan(&rowCount); err != nil {
		t.Fatalf("count remote observation rows: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("expected one remote observation row after idempotent upsert, got %d", rowCount)
	}

	deleteMutation := SyncMutation{
		Seq:       43,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntityObservation,
		EntityKey: "obs-remote-1",
		Op:        SyncOpDelete,
		Payload:   `{"sync_id":"obs-remote-1","deleted":true}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, deleteMutation); err != nil {
		t.Fatalf("apply delete mutation: %v", err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, deleteMutation); err != nil {
		t.Fatalf("reapply delete mutation: %v", err)
	}

	if _, err := s.GetObservationBySyncID("obs-remote-1"); err == nil {
		t.Fatalf("expected pulled delete to hide observation")
	}

	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending after pulled apply: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected pulled apply helpers to avoid local re-enqueue, got %+v", pending)
	}

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state after pulled apply: %v", err)
	}
	if state.LastPulledSeq != 43 {
		t.Fatalf("expected last pulled seq 43, got %d", state.LastPulledSeq)
	}
}

func TestApplyPulledMutationClearsDegradedReasonFields(t *testing.T) {
	s := newTestStore(t)
	if err := s.MarkSyncBlocked(DefaultSyncTargetKey, "blocked_unenrolled", "project not enrolled"); err != nil {
		t.Fatalf("seed degraded sync state: %v", err)
	}

	mutation := SyncMutation{
		Seq:       1,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-session",
		Op:        SyncOpUpsert,
		Payload:   `{"id":"remote-session","project":"engram","directory":"/remote"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled mutation: %v", err)
	}

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecycleHealthy {
		t.Fatalf("expected lifecycle healthy after pulled apply, got %q", state.Lifecycle)
	}
	if state.ReasonCode != nil || state.ReasonMessage != nil {
		t.Fatalf("expected pulled apply to clear degraded reasons, got reason_code=%v reason_message=%v", state.ReasonCode, state.ReasonMessage)
	}
}

func TestApplyPulledMutationAcceptsStringifiedSessionPayload(t *testing.T) {
	s := newTestStore(t)

	mutation := SyncMutation{
		Seq:       1,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-session",
		Op:        SyncOpUpsert,
		Payload:   `"{\"id\":\"remote-session\",\"project\":\"engram\",\"directory\":\"/remote\"}"`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply stringified session mutation: %v", err)
	}

	session, err := s.GetSession("remote-session")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Project != "engram" || session.Directory != "/remote" {
		t.Fatalf("unexpected session after pulled apply: %+v", session)
	}
}

func TestApplyPulledSessionDeleteRemovesSessionAndPrompts(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "remote-delete", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`, "prompt-remote-delete", "remote-delete", "prompt", "engram"); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}

	mutation := SyncMutation{
		Seq:       2,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-delete",
		Op:        SyncOpDelete,
		Payload:   `{"id":"remote-delete","project":"engram","deleted_at":"2026-04-26T10:00:00Z"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled session delete: %v", err)
	}

	if _, err := s.GetSession("remote-delete"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected session to be deleted by pulled mutation, got err=%v", err)
	}
	var promptCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE session_id = ?`, "remote-delete").Scan(&promptCount); err != nil {
		t.Fatalf("count prompts: %v", err)
	}
	if promptCount != 0 {
		t.Fatalf("expected pulled session delete to remove prompts, got %d", promptCount)
	}

	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending after pulled session delete: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected pulled session delete not to enqueue local mutations, got %+v", pending)
	}
}

func TestApplyPulledSessionUpsertTombstoneRemovesSessionAndPrompts(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "remote-tombstone", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`, "prompt-remote-tombstone", "remote-tombstone", "prompt", "engram"); err != nil {
		t.Fatalf("seed prompt: %v", err)
	}

	mutation := SyncMutation{
		Seq:       3,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-tombstone",
		Op:        SyncOpUpsert,
		Payload:   `{"id":"remote-tombstone","project":"engram","deleted_at":"2026-04-26T11:00:00Z"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled upsert tombstone: %v", err)
	}

	if _, err := s.GetSession("remote-tombstone"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected session removed by upsert tombstone, got err=%v", err)
	}
	var promptCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE session_id = ?`, "remote-tombstone").Scan(&promptCount); err != nil {
		t.Fatalf("count prompts: %v", err)
	}
	if promptCount != 0 {
		t.Fatalf("expected upsert tombstone to remove prompts, got %d", promptCount)
	}
}

func TestSessionSyncPayloadPreservesStartedAtOnApply(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("local-session", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var payloadRaw string
	if err := s.db.QueryRow(
		`SELECT payload FROM sync_mutations WHERE entity = ? AND entity_key = ? ORDER BY seq DESC LIMIT 1`,
		SyncEntitySession,
		"local-session",
	).Scan(&payloadRaw); err != nil {
		t.Fatalf("query session mutation payload: %v", err)
	}
	var enqueued syncSessionPayload
	if err := decodeSyncPayload([]byte(payloadRaw), &enqueued); err != nil {
		t.Fatalf("decode enqueued session payload: %v", err)
	}
	if strings.TrimSpace(enqueued.StartedAt) == "" {
		t.Fatal("expected session mutation payload to include started_at")
	}

	mutation := SyncMutation{
		Seq:       2,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntitySession,
		EntityKey: "remote-started-at",
		Op:        SyncOpUpsert,
		Payload:   `{"id":"remote-started-at","project":"engram","directory":"/remote","started_at":"2024-01-02 03:04:05"}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled session with started_at: %v", err)
	}

	imported, err := s.GetSession("remote-started-at")
	if err != nil {
		t.Fatalf("get imported session: %v", err)
	}
	if imported.StartedAt != "2024-01-02 03:04:05" {
		t.Fatalf("expected started_at to be preserved from pulled payload, got %q", imported.StartedAt)
	}
}

func TestApplyPulledObservationPreservesChronologyAndRevisionMetadata(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("remote-obs-session", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	mutation := SyncMutation{
		Seq:       10,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntityObservation,
		EntityKey: "obs-meta-1",
		Op:        SyncOpUpsert,
		Payload:   `{"sync_id":"obs-meta-1","session_id":"remote-obs-session","type":"decision","title":"meta","content":"preserve metadata","project":"engram","scope":"project","created_at":"2024-01-01 00:00:00","updated_at":"2024-01-05 12:30:00","last_seen_at":"2024-01-06 09:15:00","revision_count":7,"duplicate_count":3}`,
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled observation metadata: %v", err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("reapply pulled observation metadata: %v", err)
	}

	obs, err := s.GetObservationBySyncID("obs-meta-1")
	if err != nil {
		t.Fatalf("get pulled observation: %v", err)
	}
	if obs.CreatedAt != "2024-01-01 00:00:00" {
		t.Fatalf("expected created_at preserved, got %q", obs.CreatedAt)
	}
	if obs.UpdatedAt != "2024-01-05 12:30:00" {
		t.Fatalf("expected updated_at preserved, got %q", obs.UpdatedAt)
	}
	if obs.LastSeenAt == nil || *obs.LastSeenAt != "2024-01-06 09:15:00" {
		t.Fatalf("expected last_seen_at preserved, got %+v", obs.LastSeenAt)
	}
	if obs.RevisionCount != 7 {
		t.Fatalf("expected revision_count=7, got %d", obs.RevisionCount)
	}
	if obs.DuplicateCount != 3 {
		t.Fatalf("expected duplicate_count=3, got %d", obs.DuplicateCount)
	}
}

func TestApplyPulledChunkIsAtomicAndRetrySafe(t *testing.T) {
	s := newTestStore(t)

	badChunk := []SyncMutation{
		{
			Entity:    SyncEntitySession,
			EntityKey: "chunk-session",
			Op:        SyncOpUpsert,
			Payload:   `{"id":"chunk-session","project":"engram","directory":"/remote"}`,
		},
		{
			Entity:    SyncEntityObservation,
			EntityKey: "chunk-obs-bad",
			Op:        SyncOpUpsert,
			Payload:   `{"sync_id":"chunk-obs-bad","session_id":"missing-session","type":"note","title":"bad","content":"fails fk","project":"engram","scope":"project"}`,
		},
	}

	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-retry-safe", badChunk); err == nil {
		t.Fatal("expected chunk apply error for invalid observation payload")
	}
	if _, err := s.GetSession("chunk-session"); err == nil {
		t.Fatal("expected chunk session upsert to roll back after failed chunk apply")
	}
	chunks, err := s.GetSyncedChunksForTarget(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get synced chunks: %v", err)
	}
	if chunks["chunk-retry-safe"] {
		t.Fatal("failed chunk must not be marked as synced")
	}

	goodChunk := []SyncMutation{
		{
			Entity:    SyncEntitySession,
			EntityKey: "chunk-session",
			Op:        SyncOpUpsert,
			Payload:   `{"id":"chunk-session","project":"engram","directory":"/remote"}`,
		},
	}
	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-retry-safe", goodChunk); err != nil {
		t.Fatalf("apply valid chunk: %v", err)
	}
	if _, err := s.GetSession("chunk-session"); err != nil {
		t.Fatalf("expected session imported after valid chunk apply: %v", err)
	}
	chunks, err = s.GetSyncedChunksForTarget(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get synced chunks after success: %v", err)
	}
	if !chunks["chunk-retry-safe"] {
		t.Fatal("expected successful chunk to be marked synced")
	}

	if err := s.ApplyPulledChunk(DefaultSyncTargetKey, "chunk-retry-safe", goodChunk); err != nil {
		t.Fatalf("reapplying already synced chunk should be idempotent: %v", err)
	}
	var sessionCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, "chunk-session").Scan(&sessionCount); err != nil {
		t.Fatalf("count imported session: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("expected exactly one imported session row, got %d", sessionCount)
	}
}

func TestApplyPulledPromptDeleteCreatesTombstoneAndRemovesPrompt(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-prompt", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "s-prompt", Content: "to-delete", Project: "engram"})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	var syncID string
	if err := s.db.QueryRow(`SELECT sync_id FROM user_prompts WHERE id = ?`, promptID).Scan(&syncID); err != nil {
		t.Fatalf("lookup prompt sync id: %v", err)
	}

	mutation := SyncMutation{
		Seq:       44,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntityPrompt,
		EntityKey: syncID,
		Op:        SyncOpDelete,
		Payload:   fmt.Sprintf(`{"sync_id":"%s","session_id":"s-prompt","project":"engram","deleted":true,"hard_delete":true}`, syncID),
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled prompt delete: %v", err)
	}

	var remaining int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE sync_id = ?`, syncID).Scan(&remaining); err != nil {
		t.Fatalf("count prompts by sync id: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected prompt row removed by pulled delete, got %d", remaining)
	}

	var tombstones int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prompt_tombstones WHERE sync_id = ?`, syncID).Scan(&tombstones); err != nil {
		t.Fatalf("count prompt tombstones: %v", err)
	}
	if tombstones != 1 {
		t.Fatalf("expected prompt tombstone row, got %d", tombstones)
	}
}

func TestApplyPulledPromptUpsertUpdatesCreatedAtOnExistingPrompt(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-prompt-upsert", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "s-prompt-upsert", Content: "local", Project: "engram"})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	var syncID string
	if err := s.db.QueryRow(`SELECT sync_id FROM user_prompts WHERE id = ?`, promptID).Scan(&syncID); err != nil {
		t.Fatalf("lookup prompt sync id: %v", err)
	}

	mutation := SyncMutation{
		Seq:       45,
		TargetKey: DefaultSyncTargetKey,
		Entity:    SyncEntityPrompt,
		EntityKey: syncID,
		Op:        SyncOpUpsert,
		Payload:   fmt.Sprintf(`{"sync_id":"%s","session_id":"s-prompt-upsert","content":"remote overwrite","project":"engram","created_at":"2024-01-02 03:04:05"}`, syncID),
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, mutation); err != nil {
		t.Fatalf("apply pulled prompt upsert: %v", err)
	}

	var createdAt string
	var content string
	if err := s.db.QueryRow(`SELECT created_at, content FROM user_prompts WHERE sync_id = ?`, syncID).Scan(&createdAt, &content); err != nil {
		t.Fatalf("query updated prompt: %v", err)
	}
	if createdAt != "2024-01-02 03:04:05" {
		t.Fatalf("expected prompt created_at to be overwritten by incoming payload, got %q", createdAt)
	}
	if content != "remote overwrite" {
		t.Fatalf("expected prompt content updated, got %q", content)
	}
}

func TestDeletePromptEnqueuesDeleteMutationAndTombstone(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-del-prompt", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "s-del-prompt", Content: "bye", Project: "engram"})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	var syncID string
	if err := s.db.QueryRow(`SELECT sync_id FROM user_prompts WHERE id = ?`, promptID).Scan(&syncID); err != nil {
		t.Fatalf("query prompt sync id: %v", err)
	}

	if err := s.DeletePrompt(promptID); err != nil {
		t.Fatalf("delete prompt: %v", err)
	}

	var tombstones int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prompt_tombstones WHERE sync_id = ?`, syncID).Scan(&tombstones); err != nil {
		t.Fatalf("count prompt tombstones: %v", err)
	}
	if tombstones != 1 {
		t.Fatalf("expected one prompt tombstone after delete, got %d", tombstones)
	}

	var op string
	if err := s.db.QueryRow(`SELECT op FROM sync_mutations WHERE entity = ? AND entity_key = ? ORDER BY seq DESC LIMIT 1`, SyncEntityPrompt, syncID).Scan(&op); err != nil {
		t.Fatalf("query latest prompt mutation: %v", err)
	}
	if op != SyncOpDelete {
		t.Fatalf("expected latest prompt mutation op=%q, got %q", SyncOpDelete, op)
	}
}

func TestDeleteObservationHardDeleteEnqueuesProjectScopedMutationMetadata(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-del-obs", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-del-obs",
		Type:      "decision",
		Title:     "to-delete",
		Content:   "content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	if err := s.DeleteObservation(obsID, true); err != nil {
		t.Fatalf("hard delete observation: %v", err)
	}

	var project string
	var payloadRaw string
	if err := s.db.QueryRow(
		`SELECT project, payload FROM sync_mutations WHERE entity = ? AND op = ? ORDER BY seq DESC LIMIT 1`,
		SyncEntityObservation,
		SyncOpDelete,
	).Scan(&project, &payloadRaw); err != nil {
		t.Fatalf("query observation delete mutation: %v", err)
	}
	if project != "engram" {
		t.Fatalf("expected project-scoped delete mutation, got project=%q", project)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
		t.Fatalf("decode delete mutation payload: %v", err)
	}
	if payload["session_id"] != "s-del-obs" {
		t.Fatalf("expected delete payload session_id metadata, got %#v", payload["session_id"])
	}
	if payload["project"] != "engram" {
		t.Fatalf("expected delete payload project metadata, got %#v", payload["project"])
	}
}

func TestDeleteObservationHardDeleteDerivesProjectFromSessionWhenEntityProjectEmpty(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-del-obs-empty", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-del-obs-empty",
		Type:      "decision",
		Title:     "to-delete",
		Content:   "content",
		Project:   "",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	if err := s.DeleteObservation(obsID, true); err != nil {
		t.Fatalf("hard delete observation: %v", err)
	}

	var project string
	if err := s.db.QueryRow(
		`SELECT project FROM sync_mutations WHERE entity = ? AND op = ? ORDER BY seq DESC LIMIT 1`,
		SyncEntityObservation,
		SyncOpDelete,
	).Scan(&project); err != nil {
		t.Fatalf("query observation delete mutation: %v", err)
	}
	if project != "engram" {
		t.Fatalf("expected session-derived project on hard delete mutation, got %q", project)
	}
}

func TestDeleteObservationNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.DeleteObservation(999999, false)
	if !errors.Is(err, ErrObservationNotFound) {
		t.Fatalf("expected ErrObservationNotFound, got %v", err)
	}
}

func TestUtilityHelpersCoverage(t *testing.T) {
	if got := derefString(nil); got != "" {
		t.Fatalf("expected empty string for nil pointer, got %q", got)
	}
	v := "value"
	if got := derefString(&v); got != "value" {
		t.Fatalf("expected dereferenced value, got %q", got)
	}

	if got := maxInt(10, 5); got != 10 {
		t.Fatalf("expected maxInt(10,5)=10, got %d", got)
	}
	if got := maxInt(3, 7); got != 7 {
		t.Fatalf("expected maxInt(3,7)=7, got %d", got)
	}

	if got := dedupeWindowExpression(0); got != "-15 minutes" {
		t.Fatalf("expected default dedupe window, got %q", got)
	}
	if got := dedupeWindowExpression(20 * time.Second); got != "-1 minutes" {
		t.Fatalf("expected minimum 1 minute window, got %q", got)
	}

	cases := map[string]string{
		"write":   "file_change",
		"patch":   "file_change",
		"bash":    "command",
		"read":    "file_read",
		"glob":    "search",
		"unknown": "tool_use",
	}
	for in, want := range cases {
		if got := ClassifyTool(in); got != want {
			t.Fatalf("ClassifyTool(%q): expected %q, got %q", in, want, got)
		}
	}
}

func TestEndSessionAndTimelineDefaults(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-end", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-end",
		Type:      "note",
		Title:     "first",
		Content:   "first note",
		Project:   "engram",
	})
	if err != nil {
		t.Fatalf("add first observation: %v", err)
	}
	_, err = s.AddObservation(AddObservationParams{
		SessionID: "s-end",
		Type:      "note",
		Title:     "second",
		Content:   "second note",
		Project:   "engram",
	})
	if err != nil {
		t.Fatalf("add second observation: %v", err)
	}

	if err := s.EndSession("s-end", "finished session"); err != nil {
		t.Fatalf("end session: %v", err)
	}

	sess, err := s.GetSession("s-end")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.EndedAt == nil {
		t.Fatalf("expected ended_at to be set")
	}
	if sess.Summary == nil || *sess.Summary != "finished session" {
		t.Fatalf("expected summary to be stored, got %+v", sess.Summary)
	}

	timeline, err := s.Timeline(firstID, 0, -1)
	if err != nil {
		t.Fatalf("timeline with default before/after: %v", err)
	}
	if timeline.SessionInfo == nil {
		t.Fatalf("expected session info in timeline")
	}
	if timeline.TotalInRange != 2 {
		t.Fatalf("expected total_in_range=2, got %d", timeline.TotalInRange)
	}
}

func TestInferTopicFamilyCoverage(t *testing.T) {
	cases := []struct {
		name    string
		typ     string
		title   string
		content string
		want    string
	}{
		{name: "type architecture", typ: "architecture", want: "architecture"},
		{name: "type bugfix", typ: "bugfix", want: "bug"},
		{name: "type decision", typ: "decision", want: "decision"},
		{name: "type pattern", typ: "pattern", want: "pattern"},
		{name: "type config", typ: "config", want: "config"},
		{name: "type discovery", typ: "discovery", want: "discovery"},
		{name: "type learning", typ: "learning", want: "learning"},
		{name: "type session summary", typ: "session_summary", want: "session"},
		{name: "text bug", title: "", content: "this caused a crash regression", want: "bug"},
		{name: "text architecture", title: "", content: "new boundary design", want: "architecture"},
		{name: "text decision", title: "", content: "we chose this tradeoff", want: "decision"},
		{name: "text pattern", title: "", content: "naming convention for handlers", want: "pattern"},
		{name: "text config", title: "", content: "docker env setup", want: "config"},
		{name: "text discovery", title: "", content: "root cause found", want: "discovery"},
		{name: "text learning", title: "", content: "key learning from this issue", want: "learning"},
		{name: "fallback type", typ: "Custom Type", want: "custom-type"},
		{name: "default topic", typ: "manual", title: "", content: "", want: "topic"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inferTopicFamily(tc.typ, tc.title, tc.content)
			if got != tc.want {
				t.Fatalf("inferTopicFamily(%q,%q,%q): expected %q, got %q", tc.typ, tc.title, tc.content, tc.want, got)
			}
		})
	}
}

func TestStoreAdditionalQueryAndMutationBranches(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-q", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	longContent := strings.Repeat("x", s.cfg.MaxObservationLength+100)
	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "s-q",
		Type:      "note",
		Title:     "Private <private>secret</private> title",
		Content:   longContent + " <private>token</private>",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	obs, err := s.GetObservation(obsID)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if !strings.Contains(obs.Title, "[REDACTED]") {
		t.Fatalf("expected private tags redacted in title, got %q", obs.Title)
	}
	if !strings.Contains(obs.Content, "... [truncated]") {
		t.Fatalf("expected truncated content marker, got %q", obs.Content)
	}

	newProject := ""
	newTopic := ""
	updated, err := s.UpdateObservation(obsID, UpdateObservationParams{Project: &newProject, TopicKey: &newTopic})
	if err != nil {
		t.Fatalf("update observation: %v", err)
	}
	if updated.Project != nil {
		t.Fatalf("expected nil project after empty update")
	}
	if updated.TopicKey != nil {
		t.Fatalf("expected nil topic key after empty update")
	}

	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s-q", Content: "alpha prompt", Project: "alpha"}); err != nil {
		t.Fatalf("add alpha prompt: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "s-q", Content: "beta prompt", Project: "beta"}); err != nil {
		t.Fatalf("add beta prompt: %v", err)
	}

	recentPrompts, err := s.RecentPrompts("beta", 1)
	if err != nil {
		t.Fatalf("recent prompts with project filter: %v", err)
	}
	if len(recentPrompts) != 1 || recentPrompts[0].Project != "beta" {
		t.Fatalf("expected one beta prompt, got %+v", recentPrompts)
	}

	searchPrompts, err := s.SearchPrompts("prompt", "alpha", 0)
	if err != nil {
		t.Fatalf("search prompts with project filter/default limit: %v", err)
	}
	if len(searchPrompts) != 1 || searchPrompts[0].Project != "alpha" {
		t.Fatalf("expected one alpha prompt search result, got %+v", searchPrompts)
	}

	searchResults, err := s.Search("title", SearchOptions{Scope: "project", Limit: 9999})
	if err != nil {
		t.Fatalf("search with clamped limit: %v", err)
	}
	if len(searchResults) == 0 {
		t.Fatalf("expected search results")
	}

	ctx, err := s.FormatContext("", "project")
	if err != nil {
		t.Fatalf("format context: %v", err)
	}
	if !strings.Contains(ctx, "Recent User Prompts") {
		t.Fatalf("expected prompts section in context output")
	}
}

func TestStoreErrorBranchesWithClosedDatabase(t *testing.T) {
	s := newTestStore(t)

	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	if _, err := s.GetSession("missing"); err == nil {
		t.Fatalf("expected GetSession error when db is closed")
	}
	if _, err := s.AllSessions("", 1); err == nil {
		t.Fatalf("expected AllSessions error when db is closed")
	}
	if _, err := s.RecentSessions("", 1); err == nil {
		t.Fatalf("expected RecentSessions error when db is closed")
	}
	if _, err := s.SearchPrompts("x", "", 1); err == nil {
		t.Fatalf("expected SearchPrompts error when db is closed")
	}
	if _, err := s.Search("x", SearchOptions{}); err == nil {
		t.Fatalf("expected Search error when db is closed")
	}
	if _, err := s.Export(); err == nil {
		t.Fatalf("expected Export error when db is closed")
	}
	if _, err := s.Timeline(1, 1, 1); err == nil {
		t.Fatalf("expected Timeline error when db is closed")
	}
}

func TestEndSessionEdgeCases(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-edge", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := s.EndSession("missing", "ignored"); err != nil {
		t.Fatalf("end missing session should be no-op: %v", err)
	}

	if err := s.EndSession("s-edge", ""); err != nil {
		t.Fatalf("end session with empty summary: %v", err)
	}

	sess, err := s.GetSession("s-edge")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.EndedAt == nil {
		t.Fatalf("expected ended_at to be set")
	}
	if sess.Summary != nil {
		t.Fatalf("expected empty summary to persist as NULL, got %q", *sess.Summary)
	}
}

func TestTimelineHandlesMissingSessionRecord(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatalf("disable fk: %v", err)
	}
	defer func() {
		_, _ = s.db.Exec("PRAGMA foreign_keys = ON")
	}()

	res, err := s.db.Exec(
		`INSERT INTO observations (session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, last_seen_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, datetime('now'), datetime('now'))`,
		"manual-save", "manual", "orphan", "orphan content", "engram", "project", hashNormalized("orphan content"),
	)
	if err != nil {
		t.Fatalf("insert orphan observation: %v", err)
	}
	obsID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	timeline, err := s.Timeline(obsID, 1, 1)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if timeline.SessionInfo != nil {
		t.Fatalf("expected nil session info for missing session, got %+v", timeline.SessionInfo)
	}
	if timeline.TotalInRange != 1 {
		t.Fatalf("expected total in range=1, got %d", timeline.TotalInRange)
	}
}

func TestQueryObservationsScanError(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.queryObservations("SELECT 1"); err == nil {
		t.Fatalf("expected scan error for mismatched projection")
	}
}

func TestMigrationAndHelperEdgeBranches(t *testing.T) {
	t.Run("migrate is idempotent with existing triggers", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.migrate(); err != nil {
			t.Fatalf("second migrate should succeed: %v", err)
		}
	})

	t.Run("legacy migrate skips table without id column", func(t *testing.T) {
		s := newTestStore(t)

		if _, err := s.db.Exec(`
			DROP TRIGGER IF EXISTS obs_fts_insert;
			DROP TRIGGER IF EXISTS obs_fts_update;
			DROP TRIGGER IF EXISTS obs_fts_delete;
			DROP TABLE IF EXISTS observations_fts;
			DROP TABLE observations;
			CREATE TABLE observations (
				session_id TEXT,
				type TEXT,
				title TEXT,
				content TEXT
			);
		`); err != nil {
			t.Fatalf("recreate observations without id: %v", err)
		}

		if err := s.migrateLegacyObservationsTable(); err != nil {
			t.Fatalf("legacy migrate should skip tables without id: %v", err)
		}
	})

	t.Run("topic helpers normalize edge cases", func(t *testing.T) {
		if got := SuggestTopicKey("decision", "decision", ""); got != "decision/general" {
			t.Fatalf("expected decision/general, got %q", got)
		}
		if got := SuggestTopicKey("bugfix", "bug-auth-panic", ""); got != "bug/auth-panic" {
			t.Fatalf("expected bug/auth-panic, got %q", got)
		}
		if got := SuggestTopicKey("manual", "!!!", "..."); got != "topic/general" {
			t.Fatalf("expected topic/general fallback, got %q", got)
		}

		longSegment := normalizeTopicSegment(strings.Repeat("abc", 50))
		if len(longSegment) != 100 {
			t.Fatalf("expected topic segment truncation to 100, got %d", len(longSegment))
		}

		longKey := normalizeTopicKey(strings.Repeat("k", 200))
		if len(longKey) != 120 {
			t.Fatalf("expected topic key truncation to 120, got %d", len(longKey))
		}
	})

	t.Run("format context empty returns empty string", func(t *testing.T) {
		s := newTestStore(t)
		ctx, err := s.FormatContext("", "")
		if err != nil {
			t.Fatalf("format context: %v", err)
		}
		if ctx != "" {
			t.Fatalf("expected empty context when no data, got %q", ctx)
		}
	})
}

func TestExportImportEdgeBranches(t *testing.T) {
	t.Run("export fails when observations query fails", func(t *testing.T) {
		s := newTestStore(t)

		if _, err := s.db.Exec(`
			DROP TRIGGER IF EXISTS obs_fts_insert;
			DROP TRIGGER IF EXISTS obs_fts_update;
			DROP TRIGGER IF EXISTS obs_fts_delete;
			DROP TABLE IF EXISTS observations_fts;
			DROP TABLE observations;
		`); err != nil {
			t.Fatalf("drop observations: %v", err)
		}

		_, err := s.Export()
		if err == nil || !strings.Contains(err.Error(), "export observations") {
			t.Fatalf("expected observations export error, got %v", err)
		}
	})

	t.Run("export fails when prompts query fails", func(t *testing.T) {
		s := newTestStore(t)

		if _, err := s.db.Exec(`
			DROP TRIGGER IF EXISTS prompt_fts_insert;
			DROP TRIGGER IF EXISTS prompt_fts_update;
			DROP TRIGGER IF EXISTS prompt_fts_delete;
			DROP TABLE IF EXISTS prompts_fts;
			DROP TABLE user_prompts;
		`); err != nil {
			t.Fatalf("drop prompts: %v", err)
		}

		_, err := s.Export()
		if err == nil || !strings.Contains(err.Error(), "export prompts") {
			t.Fatalf("expected prompts export error, got %v", err)
		}
	})

	t.Run("import begin tx fails on closed db", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}

		_, err := s.Import(&ExportData{})
		if err == nil || !strings.Contains(err.Error(), "begin tx") {
			t.Fatalf("expected begin tx import error, got %v", err)
		}
	})

	t.Run("import fails on observation fk error", func(t *testing.T) {
		s := newTestStore(t)
		_, err := s.Import(&ExportData{
			Observations: []Observation{{
				ID:        1,
				SessionID: "missing-session",
				Type:      "bugfix",
				Title:     "x",
				Content:   "y",
				Scope:     "project",
				CreatedAt: Now(),
				UpdatedAt: Now(),
			}},
		})
		if err == nil || !strings.Contains(err.Error(), "import observation") {
			t.Fatalf("expected observation import error, got %v", err)
		}
	})

	t.Run("import fails on prompt fk error", func(t *testing.T) {
		s := newTestStore(t)
		_, err := s.Import(&ExportData{
			Prompts: []Prompt{{
				ID:        1,
				SessionID: "missing-session",
				Content:   "prompt",
				Project:   "engram",
				CreatedAt: Now(),
			}},
		})
		if err == nil || !strings.Contains(err.Error(), "import prompt") {
			t.Fatalf("expected prompt import error, got %v", err)
		}
	})
}

func TestNewErrorBranches(t *testing.T) {
	t.Run("fails when data dir is a file", func(t *testing.T) {
		base := t.TempDir()
		badPath := filepath.Join(base, "not-a-dir")
		if err := os.WriteFile(badPath, []byte("x"), 0600); err != nil {
			t.Fatalf("write file: %v", err)
		}

		cfg := mustDefaultConfig(t)
		cfg.DataDir = badPath

		_, err := New(cfg)
		if err == nil || !strings.Contains(err.Error(), "create data dir") {
			t.Fatalf("expected create data dir error, got %v", err)
		}
	})

	t.Run("fails when db path is a directory", func(t *testing.T) {
		dataDir := t.TempDir()
		dbAsDir := filepath.Join(dataDir, "engram.db")
		if err := os.Mkdir(dbAsDir, 0755); err != nil {
			t.Fatalf("mkdir db path: %v", err)
		}

		cfg := mustDefaultConfig(t)
		cfg.DataDir = dataDir

		_, err := New(cfg)
		if err == nil {
			t.Fatalf("expected New to fail when db path is a directory")
		}
	})

	t.Run("fails when migration encounters conflicting object", func(t *testing.T) {
		dataDir := t.TempDir()
		dbPath := filepath.Join(dataDir, "engram.db")

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		_, err = db.Exec(`
			CREATE TABLE sessions (
				id TEXT PRIMARY KEY,
				project TEXT NOT NULL,
				directory TEXT NOT NULL,
				started_at TEXT NOT NULL,
				ended_at TEXT,
				summary TEXT
			);
			CREATE TABLE user_prompts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id TEXT NOT NULL,
				content TEXT NOT NULL,
				created_at TEXT NOT NULL
			);
		`)
		if err != nil {
			_ = db.Close()
			t.Fatalf("create conflicting view: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close db: %v", err)
		}

		cfg := mustDefaultConfig(t)
		cfg.DataDir = dataDir

		_, err = New(cfg)
		if err == nil || !strings.Contains(err.Error(), "migration") {
			t.Fatalf("expected migration error, got %v", err)
		}
	})
}

func TestMigrationInternalErrorAndNoopBranches(t *testing.T) {
	t.Run("addColumnIfNotExists adds then noops", func(t *testing.T) {
		s := newTestStore(t)
		if _, err := s.db.Exec(`CREATE TABLE extra_table (id INTEGER)`); err != nil {
			t.Fatalf("create extra table: %v", err)
		}

		if err := s.addColumnIfNotExists("extra_table", "name", "TEXT"); err != nil {
			t.Fatalf("add column: %v", err)
		}
		if err := s.addColumnIfNotExists("extra_table", "name", "TEXT"); err != nil {
			t.Fatalf("add existing column should noop: %v", err)
		}

		if err := s.addColumnIfNotExists("missing_table", "x", "TEXT"); err == nil {
			t.Fatalf("expected missing table error")
		}
	})

	t.Run("legacy migrate noops when id is primary key", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.migrateLegacyObservationsTable(); err != nil {
			t.Fatalf("expected noop for modern schema: %v", err)
		}
	})

	t.Run("legacy migrate fails if temp table already exists", func(t *testing.T) {
		s := newTestStore(t)
		if _, err := s.db.Exec(`
			DROP TRIGGER IF EXISTS obs_fts_insert;
			DROP TRIGGER IF EXISTS obs_fts_update;
			DROP TRIGGER IF EXISTS obs_fts_delete;
			DROP TABLE IF EXISTS observations_fts;
			DROP TABLE observations;
			CREATE TABLE observations (
				id INT,
				session_id TEXT,
				type TEXT,
				title TEXT,
				content TEXT,
				created_at TEXT
			);
			CREATE TABLE observations_migrated (id INTEGER PRIMARY KEY);
		`); err != nil {
			t.Fatalf("prepare legacy schema: %v", err)
		}

		err := s.migrateLegacyObservationsTable()
		if err == nil || !strings.Contains(err.Error(), "create table") {
			t.Fatalf("expected create table error, got %v", err)
		}
	})

	t.Run("migrate returns deterministic exec hook errors", func(t *testing.T) {
		s := newTestStore(t)

		origExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "UPDATE observations SET scope = 'project'") {
				return nil, errors.New("forced migrate update failure")
			}
			return origExec(db, query, args...)
		}

		err := s.migrate()
		if err == nil || !strings.Contains(err.Error(), "forced migrate update failure") {
			t.Fatalf("expected forced migrate failure, got %v", err)
		}
	})

	t.Run("migrate fails when creating missing triggers", func(t *testing.T) {
		s := newTestStore(t)

		if _, err := s.db.Exec(`
			DROP TRIGGER IF EXISTS obs_fts_insert;
			DROP TRIGGER IF EXISTS obs_fts_update;
			DROP TRIGGER IF EXISTS obs_fts_delete;
		`); err != nil {
			t.Fatalf("drop obs triggers: %v", err)
		}

		origExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "CREATE TRIGGER obs_fts_insert") {
				return nil, errors.New("forced obs trigger failure")
			}
			return origExec(db, query, args...)
		}

		err := s.migrate()
		if err == nil || !strings.Contains(err.Error(), "forced obs trigger failure") {
			t.Fatalf("expected forced trigger failure, got %v", err)
		}
	})

	t.Run("legacy migrate surfaces begin and commit hook failures", func(t *testing.T) {
		prepareLegacyStore := func(t *testing.T) *Store {
			t.Helper()
			s := newTestStore(t)
			if _, err := s.db.Exec(`
				DROP TRIGGER IF EXISTS obs_fts_insert;
				DROP TRIGGER IF EXISTS obs_fts_update;
				DROP TRIGGER IF EXISTS obs_fts_delete;
				DROP TABLE IF EXISTS observations_fts;
				DROP TABLE observations;
				INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('s1', 'engram', '/tmp/engram');
				CREATE TABLE observations (
					id INT,
					session_id TEXT,
					type TEXT,
					title TEXT,
					content TEXT,
					tool_name TEXT,
					project TEXT,
					scope TEXT,
					topic_key TEXT,
					normalized_hash TEXT,
					revision_count INTEGER,
					duplicate_count INTEGER,
					last_seen_at TEXT,
					created_at TEXT,
					updated_at TEXT,
					deleted_at TEXT
				);
				INSERT INTO observations (id, session_id, type, title, content, project, created_at, updated_at)
				VALUES (1, 's1', 'bugfix', 'legacy', 'legacy row', 'engram', datetime('now'), datetime('now'));
			`); err != nil {
				t.Fatalf("prepare legacy table: %v", err)
			}
			return s
		}

		t.Run("begin tx", func(t *testing.T) {
			s := prepareLegacyStore(t)
			s.hooks.beginTx = func(_ *sql.DB) (*sql.Tx, error) {
				return nil, errors.New("forced begin failure")
			}

			err := s.migrateLegacyObservationsTable()
			if err == nil || !strings.Contains(err.Error(), "forced begin failure") {
				t.Fatalf("expected begin failure, got %v", err)
			}
		})

		t.Run("commit", func(t *testing.T) {
			s := prepareLegacyStore(t)
			s.hooks.commit = func(_ *sql.Tx) error {
				return errors.New("forced legacy commit failure")
			}

			err := s.migrateLegacyObservationsTable()
			if err == nil || !strings.Contains(err.Error(), "forced legacy commit failure") {
				t.Fatalf("expected commit failure, got %v", err)
			}
		})
	})
}

func TestImportExportSeamErrors(t *testing.T) {
	t.Run("export query hooks", func(t *testing.T) {
		s := newTestStore(t)

		origQueryIt := s.hooks.queryIt
		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "FROM sessions") {
				return nil, errors.New("forced sessions export query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Export(); err == nil || !strings.Contains(err.Error(), "export sessions") {
			t.Fatalf("expected sessions export error, got %v", err)
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "FROM observations") {
				return nil, errors.New("forced observations export query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Export(); err == nil || !strings.Contains(err.Error(), "export observations") {
			t.Fatalf("expected observations export error, got %v", err)
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "FROM user_prompts") {
				return nil, errors.New("forced prompts export query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Export(); err == nil || !strings.Contains(err.Error(), "export prompts") {
			t.Fatalf("expected prompts export error, got %v", err)
		}
	})

	t.Run("import tx and exec hooks", func(t *testing.T) {
		s := newTestStore(t)

		s.hooks.beginTx = func(_ *sql.DB) (*sql.Tx, error) {
			return nil, errors.New("forced import begin failure")
		}
		if _, err := s.Import(&ExportData{}); err == nil || !strings.Contains(err.Error(), "begin tx") {
			t.Fatalf("expected begin tx error, got %v", err)
		}

		s.hooks = defaultStoreHooks()
		origExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "INSERT OR IGNORE INTO sessions") {
				return nil, errors.New("forced import session insert failure")
			}
			return origExec(db, query, args...)
		}
		if _, err := s.Import(&ExportData{Sessions: []Session{{ID: "s-x", Project: "p", Directory: "/tmp", StartedAt: Now()}}}); err == nil || !strings.Contains(err.Error(), "import session") {
			t.Fatalf("expected session import error, got %v", err)
		}

		s.hooks = defaultStoreHooks()
		s.hooks.commit = func(_ *sql.Tx) error {
			return errors.New("forced import commit failure")
		}
		if _, err := s.Import(&ExportData{}); err == nil || !strings.Contains(err.Error(), "import: commit") {
			t.Fatalf("expected commit error, got %v", err)
		}
	})
}

func TestHookFallbacksAndAdditionalBranches(t *testing.T) {
	t.Run("hook fallbacks call default DB methods", func(t *testing.T) {
		s := newTestStore(t)
		s.hooks = storeHooks{}

		if _, err := s.execHook(s.db, "SELECT 1"); err != nil {
			t.Fatalf("exec hook fallback: %v", err)
		}
		rows, err := s.queryHook(s.db, "SELECT 1")
		if err != nil {
			t.Fatalf("query hook fallback: %v", err)
		}
		_ = rows.Close()

		iter, err := s.queryItHook(s.db, "SELECT 1")
		if err != nil {
			t.Fatalf("query iterator fallback: %v", err)
		}
		_ = iter.Close()

		tx, err := s.beginTxHook()
		if err != nil {
			t.Fatalf("begin tx hook fallback: %v", err)
		}
		if err := s.commitHook(tx); err != nil {
			t.Fatalf("commit hook fallback: %v", err)
		}

		s2 := newTestStore(t)
		rows2, err := s2.queryHook(s2.db, "SELECT 1")
		if err != nil {
			t.Fatalf("query hook default closure: %v", err)
		}
		_ = rows2.Close()

		s.hooks.query = func(db queryer, query string, args ...any) (*sql.Rows, error) {
			return nil, errors.New("forced query hook error")
		}
		s.hooks.queryIt = nil
		if _, err := s.queryItHook(s.db, "SELECT 1"); err == nil {
			t.Fatalf("expected queryItHook error through queryHook fallback")
		}
	})

	t.Run("sessions and observations filters with default limits", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-p", "proj-a", "/tmp/proj-a"); err != nil {
			t.Fatalf("create session proj-a: %v", err)
		}
		if err := s.CreateSession("s-q", "proj-b", "/tmp/proj-b"); err != nil {
			t.Fatalf("create session proj-b: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-p", Type: "note", Title: "a", Content: "a", Project: "proj-a", Scope: "project"}); err != nil {
			t.Fatalf("add observation proj-a: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-q", Type: "note", Title: "b", Content: "b", Project: "proj-b", Scope: "project"}); err != nil {
			t.Fatalf("add observation proj-b: %v", err)
		}

		recent, err := s.RecentSessions("proj-a", 0)
		if err != nil {
			t.Fatalf("recent sessions filtered: %v", err)
		}
		if len(recent) != 1 || recent[0].Project != "proj-a" {
			t.Fatalf("expected one proj-a recent session, got %+v", recent)
		}

		all, err := s.AllSessions("proj-b", -1)
		if err != nil {
			t.Fatalf("all sessions filtered: %v", err)
		}
		if len(all) != 1 || all[0].Project != "proj-b" {
			t.Fatalf("expected one proj-b session, got %+v", all)
		}

		obs, err := s.AllObservations("proj-a", "project", 0)
		if err != nil {
			t.Fatalf("all observations defaults: %v", err)
		}
		if len(obs) != 1 || obs[0].SessionID != "s-p" {
			t.Fatalf("expected one proj-a observation, got %+v", obs)
		}

		sessionObs, err := s.SessionObservations("s-p", 0)
		if err != nil {
			t.Fatalf("session observations default limit: %v", err)
		}
		if len(sessionObs) != 1 {
			t.Fatalf("expected one session observation, got %d", len(sessionObs))
		}

		recentObs, err := s.RecentObservations("proj-a", "project", 0)
		if err != nil {
			t.Fatalf("recent observations default limit: %v", err)
		}
		if len(recentObs) != 1 {
			t.Fatalf("expected one recent observation, got %d", len(recentObs))
		}

		recentPrompts, err := s.RecentPrompts("", 0)
		if err != nil {
			t.Fatalf("recent prompts default limit: %v", err)
		}
		if len(recentPrompts) != 0 {
			t.Fatalf("expected zero prompts, got %d", len(recentPrompts))
		}
	})

	t.Run("timeline includes before and after in chronological order", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-tl", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}

		firstID, err := s.AddObservation(AddObservationParams{SessionID: "s-tl", Type: "note", Title: "1", Content: "one", Project: "engram"})
		if err != nil {
			t.Fatalf("add first observation: %v", err)
		}
		middleID, err := s.AddObservation(AddObservationParams{SessionID: "s-tl", Type: "note", Title: "2", Content: "two", Project: "engram"})
		if err != nil {
			t.Fatalf("add middle observation: %v", err)
		}
		lastID, err := s.AddObservation(AddObservationParams{SessionID: "s-tl", Type: "note", Title: "3", Content: "three", Project: "engram"})
		if err != nil {
			t.Fatalf("add last observation: %v", err)
		}

		tl, err := s.Timeline(middleID, 5, 5)
		if err != nil {
			t.Fatalf("timeline middle: %v", err)
		}
		if len(tl.Before) != 1 || tl.Before[0].ID != firstID {
			t.Fatalf("expected first in before list, got %+v", tl.Before)
		}
		if len(tl.After) != 1 || tl.After[0].ID != lastID {
			t.Fatalf("expected last in after list, got %+v", tl.After)
		}
	})

	t.Run("format context returns specific query stage errors", func(t *testing.T) {
		t.Run("recent sessions error", func(t *testing.T) {
			s := newTestStore(t)
			_ = s.Close()
			if _, err := s.FormatContext("", ""); err == nil {
				t.Fatalf("expected format context to fail from recent sessions")
			}
		})

		t.Run("recent observations error", func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("s-ctx", "engram", "/tmp/engram"); err != nil {
				t.Fatalf("create session: %v", err)
			}
			if _, err := s.db.Exec("DROP TABLE observations"); err != nil {
				t.Fatalf("drop observations: %v", err)
			}
			if _, err := s.FormatContext("", ""); err == nil {
				t.Fatalf("expected format context to fail from recent observations")
			}
		})

		t.Run("recent prompts error", func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("s-ctx2", "engram", "/tmp/engram"); err != nil {
				t.Fatalf("create session: %v", err)
			}
			if _, err := s.db.Exec("DROP TABLE user_prompts"); err != nil {
				t.Fatalf("drop prompts: %v", err)
			}
			if _, err := s.FormatContext("", ""); err == nil {
				t.Fatalf("expected format context to fail from recent prompts")
			}
		})
	})
}

func TestSQLiteWriteRetryRetriesTransientLockErrors(t *testing.T) {
	oldBackoffs := sqliteWriteRetryBackoffs
	sqliteWriteRetryBackoffs = []time.Duration{0, 0, 0}
	t.Cleanup(func() { sqliteWriteRetryBackoffs = oldBackoffs })

	t.Run("begin lock is retried and succeeds", func(t *testing.T) {
		s := newTestStore(t)
		origBegin := s.hooks.beginTx
		attempts := 0
		s.hooks.beginTx = func(db *sql.DB) (*sql.Tx, error) {
			attempts++
			if attempts < 3 {
				return nil, errors.New("database is locked")
			}
			return origBegin(db)
		}

		if err := s.CreateSession("retry-session", "retry-project", "/tmp/retry-project"); err != nil {
			t.Fatalf("expected retry to succeed, got %v", err)
		}
		if attempts != 3 {
			t.Fatalf("expected 3 begin attempts, got %d", attempts)
		}
	})

	t.Run("non lock error is not retried", func(t *testing.T) {
		s := newTestStore(t)
		attempts := 0
		s.hooks.beginTx = func(_ *sql.DB) (*sql.Tx, error) {
			attempts++
			return nil, errors.New("permanent begin failure")
		}

		err := s.CreateSession("no-retry-session", "retry-project", "/tmp/retry-project")
		if err == nil || !strings.Contains(err.Error(), "permanent begin failure") {
			t.Fatalf("expected permanent error, got %v", err)
		}
		if attempts != 1 {
			t.Fatalf("expected one attempt for permanent error, got %d", attempts)
		}
	})

	t.Run("lock errors remain bounded", func(t *testing.T) {
		s := newTestStore(t)
		attempts := 0
		s.hooks.beginTx = func(_ *sql.DB) (*sql.Tx, error) {
			attempts++
			return nil, errors.New("SQLITE_BUSY: database is locked")
		}

		err := s.CreateSession("bounded-session", "retry-project", "/tmp/retry-project")
		if err == nil || !isRetryableSQLiteLockError(err) {
			t.Fatalf("expected retryable lock error after exhaustion, got %v", err)
		}
		if attempts != len(sqliteWriteRetryBackoffs)+1 {
			t.Fatalf("expected bounded attempts=%d, got %d", len(sqliteWriteRetryBackoffs)+1, attempts)
		}
	})
}

func TestStoreUncoveredBranchesPushToHundred(t *testing.T) {
	t.Run("new open database hook error", func(t *testing.T) {
		orig := openDB
		t.Cleanup(func() { openDB = orig })
		openDB = func(driverName, dataSourceName string) (*sql.DB, error) {
			return nil, errors.New("forced open error")
		}

		cfg := mustDefaultConfig(t)
		cfg.DataDir = t.TempDir()
		if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "open database") {
			t.Fatalf("expected open database error, got %v", err)
		}
	})

	t.Run("migrate forced failures for remaining exec branches", func(t *testing.T) {
		failCases := []string{
			"CREATE INDEX IF NOT EXISTS idx_obs_scope",
			"UPDATE observations SET topic_key = NULL",
			"UPDATE observations SET revision_count = 1",
			"UPDATE observations SET duplicate_count = 1",
			"UPDATE observations SET updated_at = created_at",
			"UPDATE user_prompts SET project = ''",
			"CREATE TRIGGER prompt_fts_insert",
		}
		for _, needle := range failCases {
			t.Run(needle, func(t *testing.T) {
				s := newTestStore(t)
				if strings.Contains(needle, "CREATE TRIGGER prompt_fts_insert") {
					if _, err := s.db.Exec(`
						DROP TRIGGER IF EXISTS prompt_fts_insert;
						DROP TRIGGER IF EXISTS prompt_fts_update;
						DROP TRIGGER IF EXISTS prompt_fts_delete;
					`); err != nil {
						t.Fatalf("drop prompt triggers: %v", err)
					}
				}
				origExec := s.hooks.exec
				s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
					if strings.Contains(query, needle) {
						return nil, errors.New("forced migrate failure")
					}
					return origExec(db, query, args...)
				}
				if err := s.migrate(); err == nil {
					t.Fatalf("expected migrate error for %q", needle)
				}
			})
		}
	})

	t.Run("migrate addColumn and legacy-call propagation", func(t *testing.T) {
		t.Run("propagates addColumn error", func(t *testing.T) {
			s := newTestStore(t)
			origQueryIt := s.hooks.queryIt
			called := 0
			s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
				if strings.Contains(query, "PRAGMA table_info(observations)") {
					called++
					if called == 1 {
						return nil, errors.New("forced addColumn failure")
					}
				}
				return origQueryIt(db, query, args...)
			}
			if err := s.migrate(); err == nil {
				t.Fatalf("expected migrate to propagate addColumn failure")
			}
		})

		t.Run("propagates legacy migrate error", func(t *testing.T) {
			s := newTestStore(t)
			origQueryIt := s.hooks.queryIt
			called := 0
			s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
				if strings.Contains(query, "PRAGMA table_info(observations)") {
					called++
					if called == 9 {
						return nil, errors.New("forced legacy call failure")
					}
				}
				return origQueryIt(db, query, args...)
			}
			if err := s.migrate(); err == nil {
				t.Fatalf("expected migrate to propagate legacy migrate failure")
			}
		})
	})

	t.Run("add observation, prompt, update forced errors", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-e", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}

		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "top", Content: "x", Project: "engram", TopicKey: "x"}); err != nil {
			t.Fatalf("seed topic observation: %v", err)
		}
		origExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "SET type = ?") {
				return nil, errors.New("forced topic update error")
			}
			return origExec(db, query, args...)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "top", Content: "x", Project: "engram", TopicKey: "x"}); err == nil {
			t.Fatalf("expected topic upsert exec error")
		}

		s.hooks = defaultStoreHooks()
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "dup", Content: "dup content", Project: "engram"}); err != nil {
			t.Fatalf("seed dedupe observation: %v", err)
		}
		origExec = s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "SET duplicate_count = duplicate_count + 1") {
				return nil, errors.New("forced dedupe update error")
			}
			return origExec(db, query, args...)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "dup", Content: "dup content", Project: "engram"}); err == nil {
			t.Fatalf("expected dedupe exec error")
		}

		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "x", Content: "y", Project: "engram", TopicKey: "t"}); err == nil {
			t.Fatalf("expected topic query error on closed db")
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-e", Type: "note", Title: "x", Content: "y", Project: "engram"}); err == nil {
			t.Fatalf("expected dedupe query error on closed db")
		}
		if _, err := s.AddPrompt(AddPromptParams{SessionID: "s-e", Content: "x"}); err == nil {
			t.Fatalf("expected add prompt error on closed db")
		}
	})

	t.Run("update observation remaining branches", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-u", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		id, err := s.AddObservation(AddObservationParams{SessionID: "s-u", Type: "old", Title: "t", Content: "c", Project: "engram", TopicKey: "topic/key"})
		if err != nil {
			t.Fatalf("seed observation: %v", err)
		}

		if _, err := s.UpdateObservation(999999, UpdateObservationParams{}); err == nil {
			t.Fatalf("expected update missing observation error")
		}

		newType := "new-type"
		longContent := strings.Repeat("z", s.cfg.MaxObservationLength+50)
		if _, err := s.UpdateObservation(id, UpdateObservationParams{Type: &newType, Content: &longContent}); err != nil {
			t.Fatalf("update with type+truncation: %v", err)
		}

		origExec := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "UPDATE observations") {
				return nil, errors.New("forced update exec error")
			}
			return origExec(db, query, args...)
		}
		if _, err := s.UpdateObservation(id, UpdateObservationParams{}); err == nil {
			t.Fatalf("expected update exec error")
		}
	})

	t.Run("query iterator scan and rows.Err branches", func(t *testing.T) {
		s := newTestStore(t)
		origQueryIt := s.hooks.queryIt

		setScanErr := func(match string) {
			s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
				if strings.Contains(query, match) {
					return &fakeRows{next: []bool{true, false}, scanErr: errors.New("forced scan error")}, nil
				}
				return origQueryIt(db, query, args...)
			}
		}

		setRowsErr := func(match string) {
			s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
				if strings.Contains(query, match) {
					return &fakeRows{next: []bool{false}, err: errors.New("forced rows err")}, nil
				}
				return origQueryIt(db, query, args...)
			}
		}

		if err := s.CreateSession("s-iter", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-iter", Type: "note", Title: "one", Content: "one", Project: "engram"}); err != nil {
			t.Fatalf("add observation: %v", err)
		}
		if _, err := s.AddPrompt(AddPromptParams{SessionID: "s-iter", Content: "prompt", Project: "engram"}); err != nil {
			t.Fatalf("add prompt: %v", err)
		}

		setScanErr("FROM sessions s")
		if _, err := s.RecentSessions("", 10); err == nil {
			t.Fatalf("expected recent sessions scan error")
		}

		setScanErr("FROM sessions s")
		if _, err := s.AllSessions("", 10); err == nil {
			t.Fatalf("expected all sessions scan error")
		}

		setScanErr("FROM user_prompts")
		if _, err := s.RecentPrompts("", 10); err == nil {
			t.Fatalf("expected recent prompts scan error")
		}

		setScanErr("FROM prompts_fts")
		if _, err := s.SearchPrompts("prompt", "", 10); err == nil {
			t.Fatalf("expected search prompts scan error")
		}

		setScanErr("FROM observations_fts")
		if _, err := s.Search("one", SearchOptions{}); err == nil {
			t.Fatalf("expected search scan error")
		}

		setRowsErr("FROM observations_fts")
		if _, err := s.Search("one", SearchOptions{}); err == nil {
			t.Fatalf("expected search rows err")
		}

		setScanErr("SELECT id, project, directory")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export sessions scan error")
		}

		setRowsErr("SELECT id, project, directory")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export sessions rows err")
		}

		setScanErr("FROM observations ORDER BY id")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export observations scan error")
		}

		setRowsErr("FROM observations ORDER BY id")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export observations rows err")
		}

		setScanErr("FROM user_prompts ORDER BY id")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export prompts scan error")
		}

		setRowsErr("FROM user_prompts ORDER BY id")
		if _, err := s.Export(); err == nil {
			t.Fatalf("expected export prompts rows err")
		}

		setScanErr("FROM sync_chunks")
		if _, err := s.GetSyncedChunks(); err == nil {
			t.Fatalf("expected synced chunks scan error")
		}

		setRowsErr("PRAGMA table_info(extra_table)")
		if _, err := s.db.Exec(`CREATE TABLE extra_table (id INTEGER)`); err != nil {
			t.Fatalf("create extra table: %v", err)
		}
		if err := s.addColumnIfNotExists("extra_table", "n", "TEXT"); err == nil {
			t.Fatalf("expected add column rows err")
		}

		setScanErr("PRAGMA table_info(extra_table)")
		if err := s.addColumnIfNotExists("extra_table", "n2", "TEXT"); err == nil {
			t.Fatalf("expected add column scan error")
		}

		setRowsErr("PRAGMA table_info(observations)")
		if err := s.migrateLegacyObservationsTable(); err == nil {
			t.Fatalf("expected legacy migrate pragma rows err")
		}

		setScanErr("PRAGMA table_info(observations)")
		if err := s.migrateLegacyObservationsTable(); err == nil {
			t.Fatalf("expected legacy migrate pragma scan error")
		}

		s.hooks.queryIt = origQueryIt
	})

	t.Run("migration helpers close rows on scan errors", func(t *testing.T) {
		s := newTestStore(t)
		if _, err := s.db.Exec(`CREATE TABLE extra_table (id INTEGER)`); err != nil {
			t.Fatalf("create extra table: %v", err)
		}

		cases := []struct {
			name        string
			queryNeedle string
			run         func() error
		}{
			{
				name:        "add column",
				queryNeedle: "PRAGMA table_info(extra_table)",
				run:         func() error { return s.addColumnIfNotExists("extra_table", "n3", "TEXT") },
			},
			{
				name:        "sync chunks migration",
				queryNeedle: "PRAGMA table_info(sync_chunks)",
				run:         s.migrateSyncChunksTable,
			},
			{
				name:        "legacy observations migration",
				queryNeedle: "PRAGMA table_info(observations)",
				run:         s.migrateLegacyObservationsTable,
			},
		}

		origQueryIt := s.hooks.queryIt
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				forcedRows := &fakeRows{next: []bool{true}, scanErr: errors.New("forced migration scan error")}
				s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
					if strings.Contains(query, tc.queryNeedle) {
						return forcedRows, nil
					}
					return origQueryIt(db, query, args...)
				}

				if err := tc.run(); err == nil {
					t.Fatalf("expected scan error")
				}
				if !forcedRows.closed {
					t.Fatalf("expected rows to be closed after scan error")
				}
			})
		}
		s.hooks.queryIt = origQueryIt
	})

	t.Run("timeline and search type filter branches", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-t2", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		first, _ := s.AddObservation(AddObservationParams{SessionID: "s-t2", Type: "decision", Title: "a", Content: "a", Project: "engram"})
		_, _ = s.AddObservation(AddObservationParams{SessionID: "s-t2", Type: "decision", Title: "aa", Content: "aa", Project: "engram"})
		focus, _ := s.AddObservation(AddObservationParams{SessionID: "s-t2", Type: "decision", Title: "b", Content: "b", Project: "engram"})
		_, _ = s.AddObservation(AddObservationParams{SessionID: "s-t2", Type: "decision", Title: "c", Content: "c", Project: "engram"})

		if _, err := s.Search("b", SearchOptions{Type: "decision", Project: "engram", Scope: "project", Limit: 5}); err != nil {
			t.Fatalf("search with type filter: %v", err)
		}

		origQueryIt := s.hooks.queryIt
		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id < ?") {
				return nil, errors.New("forced before query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline before query error")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id < ?") {
				return &fakeRows{next: []bool{true, false}, scanErr: errors.New("forced before scan error")}, nil
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline before scan error")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id < ?") {
				return &fakeRows{next: []bool{false}, err: errors.New("forced before rows err")}, nil
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline before rows err")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id > ?") {
				return nil, errors.New("forced after query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline after query error")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id > ?") {
				return &fakeRows{next: []bool{true, false}, scanErr: errors.New("forced after scan error")}, nil
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline after scan error")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "id > ?") {
				return &fakeRows{next: []bool{false}, err: errors.New("forced after rows err")}, nil
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Timeline(focus, 2, 2); err == nil {
			t.Fatalf("expected timeline after rows err")
		}

		s.hooks.queryIt = origQueryIt
		tl, err := s.Timeline(first, 5, 5)
		if err != nil {
			t.Fatalf("timeline reverse branch run: %v", err)
		}
		if len(tl.After) == 0 {
			t.Fatalf("expected timeline after entries")
		}
	})

	t.Run("format context and stats remaining branches", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("s-c", "engram", "/tmp/engram"); err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := s.AddObservation(AddObservationParams{SessionID: "s-c", Type: "note", Title: "n", Content: "n", Project: "engram"}); err != nil {
			t.Fatalf("add obs: %v", err)
		}

		origQueryIt := s.hooks.queryIt
		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "FROM observations o") && strings.Contains(query, "WHERE o.deleted_at IS NULL") {
				return nil, errors.New("forced recent observations error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.FormatContext("engram", "project"); err == nil {
			t.Fatalf("expected format context observations error")
		}

		s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
			if strings.Contains(query, "GROUP BY project") {
				return nil, errors.New("forced stats query error")
			}
			return origQueryIt(db, query, args...)
		}
		if _, err := s.Stats(); err != nil {
			t.Fatalf("stats should swallow project query errors: %v", err)
		}

		if err := s.EndSession("s-c", "has summary"); err != nil {
			t.Fatalf("end session: %v", err)
		}
		s.hooks.queryIt = origQueryIt
		ctx, err := s.FormatContext("engram", "project")
		if err != nil {
			t.Fatalf("format context with summary: %v", err)
		}
		if !strings.Contains(ctx, "has summary") {
			t.Fatalf("expected session summary included in context")
		}
	})

	t.Run("helper query errors and legacy migration late-stage failures", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		if _, err := s.GetSyncedChunks(); err == nil {
			t.Fatalf("expected synced chunks query error")
		}
		if _, err := s.queryObservations("SELECT id FROM observations"); err == nil {
			t.Fatalf("expected queryObservations query error")
		}
		if err := s.addColumnIfNotExists("observations", "x", "TEXT"); err == nil {
			t.Fatalf("expected addColumn query error")
		}
		if err := s.migrateLegacyObservationsTable(); err == nil {
			t.Fatalf("expected legacy migrate query error")
		}

		s2 := newTestStore(t)
		if _, err := s2.db.Exec(`
			DROP TRIGGER IF EXISTS obs_fts_insert;
			DROP TRIGGER IF EXISTS obs_fts_update;
			DROP TRIGGER IF EXISTS obs_fts_delete;
			DROP TABLE IF EXISTS observations_fts;
			DROP TABLE observations;
			INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('s1', 'engram', '/tmp/engram');
			CREATE TABLE observations (
				id INT,
				session_id TEXT,
				type TEXT,
				title TEXT,
				content TEXT,
				tool_name TEXT,
				project TEXT,
				scope TEXT,
				topic_key TEXT,
				normalized_hash TEXT,
				revision_count INTEGER,
				duplicate_count INTEGER,
				last_seen_at TEXT,
				created_at TEXT,
				updated_at TEXT,
				deleted_at TEXT
			);
			INSERT INTO observations (id, session_id, type, title, content, project, created_at, updated_at)
			VALUES (1, 's1', 'bugfix', 'legacy', 'legacy row', 'engram', datetime('now'), datetime('now'));
		`); err != nil {
			t.Fatalf("prepare legacy table: %v", err)
		}

		lateFail := []string{"INSERT INTO observations_migrated", "DROP TABLE observations", "RENAME TO observations", "CREATE VIRTUAL TABLE observations_fts"}
		for _, needle := range lateFail {
			t.Run(needle, func(t *testing.T) {
				s3 := newTestStore(t)
				if _, err := s3.db.Exec(`
					DROP TRIGGER IF EXISTS obs_fts_insert;
					DROP TRIGGER IF EXISTS obs_fts_update;
					DROP TRIGGER IF EXISTS obs_fts_delete;
					DROP TABLE IF EXISTS observations_fts;
					DROP TABLE observations;
					INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('s1', 'engram', '/tmp/engram');
					CREATE TABLE observations (
						id INT,
						session_id TEXT,
						type TEXT,
						title TEXT,
						content TEXT,
						tool_name TEXT,
						project TEXT,
						scope TEXT,
						topic_key TEXT,
						normalized_hash TEXT,
						revision_count INTEGER,
						duplicate_count INTEGER,
						last_seen_at TEXT,
						created_at TEXT,
						updated_at TEXT,
						deleted_at TEXT
					);
					INSERT INTO observations (id, session_id, type, title, content, project, created_at, updated_at)
					VALUES (1, 's1', 'bugfix', 'legacy', 'legacy row', 'engram', datetime('now'), datetime('now'));
				`); err != nil {
					t.Fatalf("prepare legacy schema: %v", err)
				}

				origExec := s3.hooks.exec
				s3.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
					if strings.Contains(query, needle) {
						return nil, errors.New("forced legacy late failure")
					}
					return origExec(db, query, args...)
				}
				if err := s3.migrateLegacyObservationsTable(); err == nil {
					t.Fatalf("expected legacy migrate error for %q", needle)
				}
			})
		}
	})
}

// ─── Issue #25: Session collision regression tests ──────────────────────────

func TestCreateSessionUpsertsEmptyProjectAndDirectory(t *testing.T) {
	s := newTestStore(t)

	// Create session with empty project/directory (simulates first MCP call without context)
	if err := s.CreateSession("sess-upsert", "", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Second call with real project/directory should fill in the blanks.
	// Project names are normalized to lowercase, so "projectA" becomes "projecta".
	if err := s.CreateSession("sess-upsert", "projectA", "/tmp/a"); err != nil {
		t.Fatalf("upsert session: %v", err)
	}

	sess, err := s.GetSession("sess-upsert")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Project != "projecta" {
		t.Fatalf("expected project=projecta after upsert (normalized), got %q", sess.Project)
	}
	if sess.Directory != "/tmp/a" {
		t.Fatalf("expected directory=/tmp/a after upsert, got %q", sess.Directory)
	}
}

func TestCreateSessionDoesNotOverwriteExistingProject(t *testing.T) {
	s := newTestStore(t)

	// Create session with project A (normalized to "projecta")
	if err := s.CreateSession("sess-preserve", "projectA", "/tmp/a"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Second call with project B should NOT overwrite
	if err := s.CreateSession("sess-preserve", "projectB", "/tmp/b"); err != nil {
		t.Fatalf("upsert session: %v", err)
	}

	sess, err := s.GetSession("sess-preserve")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	// Project names are normalized to lowercase, so "projectA" is stored as "projecta"
	if sess.Project != "projecta" {
		t.Fatalf("expected project=projecta (preserved, normalized), got %q", sess.Project)
	}
	if sess.Directory != "/tmp/a" {
		t.Fatalf("expected directory=/tmp/a (preserved), got %q", sess.Directory)
	}
}

func TestCreateSessionPartialUpsert(t *testing.T) {
	s := newTestStore(t)

	t.Run("fills directory when project already set", func(t *testing.T) {
		if err := s.CreateSession("sess-partial-1", "myproject", ""); err != nil {
			t.Fatalf("create: %v", err)
		}
		// Second call fills directory but project stays
		if err := s.CreateSession("sess-partial-1", "other", "/new/dir"); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		sess, err := s.GetSession("sess-partial-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if sess.Project != "myproject" {
			t.Fatalf("project should be preserved, got %q", sess.Project)
		}
		if sess.Directory != "/new/dir" {
			t.Fatalf("directory should be filled, got %q", sess.Directory)
		}
	})

	t.Run("fills project when directory already set", func(t *testing.T) {
		if err := s.CreateSession("sess-partial-2", "", "/existing/dir"); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := s.CreateSession("sess-partial-2", "newproject", ""); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		sess, err := s.GetSession("sess-partial-2")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if sess.Project != "newproject" {
			t.Fatalf("project should be filled, got %q", sess.Project)
		}
		if sess.Directory != "/existing/dir" {
			t.Fatalf("directory should be preserved, got %q", sess.Directory)
		}
	})

	t.Run("both empty stays empty", func(t *testing.T) {
		if err := s.CreateSession("sess-partial-3", "", ""); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := s.CreateSession("sess-partial-3", "", ""); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		sess, err := s.GetSession("sess-partial-3")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if sess.Project != "" {
			t.Fatalf("project should stay empty, got %q", sess.Project)
		}
		if sess.Directory != "" {
			t.Fatalf("directory should stay empty, got %q", sess.Directory)
		}
	})
}

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "short ascii", in: "abc", max: 10, want: "abc"},
		{name: "exact length", in: "hello", max: 5, want: "hello"},
		{name: "long ascii", in: "abcdef", max: 3, want: "abc..."},
		{name: "spanish accents", in: "Decisión de arquitectura", max: 8, want: "Decisión..."},
		{name: "emoji", in: "🐛🔧🚀✨🎉💡", max: 3, want: "🐛🔧🚀..."},
		{name: "mixed ascii and multibyte", in: "café☕latte", max: 5, want: "café☕..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.max)
			if got != tc.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

// ─── Project Enrollment CRUD Tests ───────────────────────────────────────────

func TestEnrollProjectBasic(t *testing.T) {
	s := newTestStore(t)

	// Enroll a project.
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Verify it shows up in the list.
	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list enrolled projects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 enrolled project, got %d", len(projects))
	}
	if projects[0].Project != "engram" {
		t.Fatalf("expected project 'engram', got %q", projects[0].Project)
	}
	if projects[0].EnrolledAt == "" {
		t.Fatal("expected enrolled_at to be set")
	}

	// Verify IsProjectEnrolled returns true.
	enrolled, err := s.IsProjectEnrolled("engram")
	if err != nil {
		t.Fatalf("is project enrolled: %v", err)
	}
	if !enrolled {
		t.Fatal("expected project to be enrolled")
	}
}

func TestEnrollProjectIdempotent(t *testing.T) {
	s := newTestStore(t)

	// Enroll twice — should not error.
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("second enroll (idempotent): %v", err)
	}

	// Should still be exactly one row.
	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 enrolled project after double-enroll, got %d", len(projects))
	}
}

func TestEnrollAndLookupProjectNormalization(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("  ENGRAM__CORE  "); err != nil {
		t.Fatalf("enroll normalized project: %v", err)
	}

	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list enrolled: %v", err)
	}
	if len(projects) != 1 || projects[0].Project != "engram_core" {
		t.Fatalf("expected canonical enrolled project engram_core, got %+v", projects)
	}

	enrolled, err := s.IsProjectEnrolled("engram__core")
	if err != nil {
		t.Fatalf("is enrolled: %v", err)
	}
	if !enrolled {
		t.Fatal("expected normalized enrollment lookup to succeed")
	}

	if err := s.UnenrollProject("ENGRAM__CORE"); err != nil {
		t.Fatalf("unenroll normalized project: %v", err)
	}
	enrolled, err = s.IsProjectEnrolled("engram_core")
	if err != nil {
		t.Fatalf("is enrolled after unenroll: %v", err)
	}
	if enrolled {
		t.Fatal("expected project to be unenrolled after normalized removal")
	}
}

func TestEnrollProjectBackfillsHistoricalMutations(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory, ended_at, summary) VALUES (?, ?, ?, datetime('now'), ?)`,
		"legacy-session", "legacy-proj", "/tmp/legacy", "done",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, last_seen_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, datetime('now'), datetime('now'))`,
		"obs-legacy", "legacy-session", "decision", "Legacy obs", "Historical content", "legacy-proj", "project", hashNormalized("Historical content"),
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`,
		"prompt-legacy", "legacy-session", "What happened before enterprise?", "legacy-proj",
	); err != nil {
		t.Fatalf("insert prompt: %v", err)
	}

	var before int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&before); err != nil {
		t.Fatalf("count mutations before enroll: %v", err)
	}
	if before != 0 {
		t.Fatalf("expected 0 sync mutations before enroll, got %d", before)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(mutations) != 3 {
		t.Fatalf("expected 3 backfilled mutations, got %d", len(mutations))
	}

	expected := map[string]string{
		SyncEntitySession:     "legacy-session",
		SyncEntityObservation: "obs-legacy",
		SyncEntityPrompt:      "prompt-legacy",
	}
	for _, mutation := range mutations {
		entityKey, ok := expected[mutation.Entity]
		if !ok {
			t.Fatalf("unexpected mutation entity %q", mutation.Entity)
		}
		if mutation.EntityKey != entityKey {
			t.Fatalf("expected entity_key %q for %s, got %q", entityKey, mutation.Entity, mutation.EntityKey)
		}
		if mutation.Project != "legacy-proj" {
			t.Fatalf("expected project legacy-proj, got %q", mutation.Project)
		}
	}
	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("get sync state: %v", err)
	}
	if state.LastEnqueuedSeq != 3 {
		t.Fatalf("expected last_enqueued_seq 3 after backfill, got %d", state.LastEnqueuedSeq)
	}
}

func TestEnrollProjectBackfillIsIdempotentAndSkipsExistingMutations(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		"legacy-session", "legacy-proj", "/tmp/legacy",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, last_seen_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, datetime('now'), datetime('now'))`,
		"obs-legacy", "legacy-session", "decision", "Legacy obs", "Historical content", "legacy-proj", "project", hashNormalized("Historical content"),
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`,
		"prompt-legacy", "legacy-session", "Historical prompt", "legacy-proj",
	); err != nil {
		t.Fatalf("insert prompt: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		DefaultSyncTargetKey, SyncEntityObservation, "obs-legacy", SyncOpUpsert, `{"sync_id":"obs-legacy","session_id":"legacy-session","project":"legacy-proj"}`, SyncSourceLocal, "legacy-proj",
	); err != nil {
		t.Fatalf("insert existing mutation: %v", err)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("first enroll: %v", err)
	}

	var afterFirst int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&afterFirst); err != nil {
		t.Fatalf("count after first enroll: %v", err)
	}
	if afterFirst != 3 {
		t.Fatalf("expected 3 total mutations after first enroll, got %d", afterFirst)
	}

	var observationMutations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ?`, SyncEntityObservation, "obs-legacy").Scan(&observationMutations); err != nil {
		t.Fatalf("count observation mutations: %v", err)
	}
	if observationMutations != 1 {
		t.Fatalf("expected existing observation mutation to remain single, got %d rows", observationMutations)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("second enroll: %v", err)
	}

	var afterSecond int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&afterSecond); err != nil {
		t.Fatalf("count after second enroll: %v", err)
	}
	if afterSecond != afterFirst {
		t.Fatalf("expected no duplicate backfill on re-enroll, got %d mutations after second enroll vs %d after first", afterSecond, afterFirst)
	}
}

func TestEnrollProjectBackfillsSessionOwnedEntitiesWithEmptyProject(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		"legacy-empty-project", "legacy-proj", "/tmp/legacy",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, NULL, ?, ?, 2, 4, ?, ?)`,
		"obs-empty-project", "legacy-empty-project", "decision", "Legacy empty project obs", "historical empty project observation", "project", hashNormalized("historical empty project observation"), "2024-01-01 10:00:00", "2024-01-02 11:00:00",
	); err != nil {
		t.Fatalf("insert observation with empty project: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO user_prompts (sync_id, session_id, content, project, created_at) VALUES (?, ?, ?, NULL, ?)`,
		"prompt-empty-project", "legacy-empty-project", "prompt for empty project entity", "2024-01-01 12:00:00",
	); err != nil {
		t.Fatalf("insert prompt with empty project: %v", err)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(mutations) != 3 {
		t.Fatalf("expected session + observation + prompt backfilled, got %d", len(mutations))
	}

	byEntity := map[string]string{}
	for _, mutation := range mutations {
		byEntity[mutation.Entity] = mutation.EntityKey
		if mutation.Project != "legacy-proj" {
			t.Fatalf("expected derived project legacy-proj for backfilled mutation, got %q", mutation.Project)
		}
	}
	if byEntity[SyncEntitySession] != "legacy-empty-project" {
		t.Fatalf("expected session backfill for legacy-empty-project, got %q", byEntity[SyncEntitySession])
	}
	if byEntity[SyncEntityObservation] != "obs-empty-project" {
		t.Fatalf("expected observation backfill for empty-project entity, got %q", byEntity[SyncEntityObservation])
	}
	if byEntity[SyncEntityPrompt] != "prompt-empty-project" {
		t.Fatalf("expected prompt backfill for empty-project entity, got %q", byEntity[SyncEntityPrompt])
	}

	var obsPayloadRaw string
	if err := s.db.QueryRow(
		`SELECT payload FROM sync_mutations WHERE entity = ? AND entity_key = ?`,
		SyncEntityObservation,
		"obs-empty-project",
	).Scan(&obsPayloadRaw); err != nil {
		t.Fatalf("query observation backfill payload: %v", err)
	}
	var obsPayload syncObservationPayload
	if err := decodeSyncPayload([]byte(obsPayloadRaw), &obsPayload); err != nil {
		t.Fatalf("decode observation backfill payload: %v", err)
	}
	if obsPayload.CreatedAt != "2024-01-01 10:00:00" || obsPayload.UpdatedAt != "2024-01-02 11:00:00" {
		t.Fatalf("expected backfill payload to preserve chronology metadata, got created_at=%q updated_at=%q", obsPayload.CreatedAt, obsPayload.UpdatedAt)
	}
	if obsPayload.RevisionCount != 2 || obsPayload.DuplicateCount != 4 {
		t.Fatalf("expected backfill payload to preserve revision metadata, got revision_count=%d duplicate_count=%d", obsPayload.RevisionCount, obsPayload.DuplicateCount)
	}
}

func TestEnrollProjectBackfillsSoftDeletedObservationDeleteMutations(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		"legacy-soft-delete-session", "legacy-proj", "/tmp/legacy",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?)`,
		"obs-soft-deleted", "legacy-soft-delete-session", "decision", "Legacy deleted obs", "historical deleted observation", "legacy-proj", "project", hashNormalized("historical deleted observation"),
		"2024-01-01 10:00:00", "2024-01-02 11:00:00", "2024-01-03 12:00:00",
	); err != nil {
		t.Fatalf("insert soft-deleted observation: %v", err)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	var foundDelete bool
	for _, mutation := range mutations {
		if mutation.Entity == SyncEntityObservation && mutation.EntityKey == "obs-soft-deleted" {
			if mutation.Op != SyncOpDelete {
				t.Fatalf("expected observation backfill op=delete for soft-deleted row, got %q", mutation.Op)
			}
			if mutation.Project != "legacy-proj" {
				t.Fatalf("expected derived project legacy-proj for soft-delete mutation, got %q", mutation.Project)
			}
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatalf("expected soft-deleted observation delete mutation to be backfilled")
	}
}

func TestEnrollProjectBackfillsPromptDeleteTombstonesWithDerivedProject(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		"legacy-prompt-session", "legacy-proj", "/tmp/legacy",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if _, err := s.db.Exec(
		`INSERT INTO prompt_tombstones (sync_id, session_id, project, deleted_at) VALUES (?, ?, '', ?)`,
		"prompt-soft-delete", "legacy-prompt-session", "2024-01-05 12:34:56",
	); err != nil {
		t.Fatalf("insert prompt tombstone: %v", err)
	}

	if err := s.EnrollProject("legacy-proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	var foundDelete bool
	for _, mutation := range mutations {
		if mutation.Entity == SyncEntityPrompt && mutation.EntityKey == "prompt-soft-delete" {
			if mutation.Op != SyncOpDelete {
				t.Fatalf("expected prompt tombstone backfill op=delete, got %q", mutation.Op)
			}
			if mutation.Project != "legacy-proj" {
				t.Fatalf("expected project derived from session to be legacy-proj, got %q", mutation.Project)
			}
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatalf("expected prompt tombstone delete mutation to be backfilled")
	}
}

func TestNewRepairsSoftDeletedObservationDeleteMutationsForEnrolledProjects(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "engram.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	obsHash := hashNormalized("Historical deleted content")
	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			project TEXT NOT NULL,
			directory TEXT NOT NULL,
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			ended_at TEXT,
			summary TEXT
		);
		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sync_id TEXT,
			session_id TEXT NOT NULL,
			type TEXT NOT NULL,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			tool_name TEXT,
			project TEXT,
			scope TEXT NOT NULL DEFAULT 'project',
			topic_key TEXT,
			normalized_hash TEXT,
			revision_count INTEGER NOT NULL DEFAULT 1,
			duplicate_count INTEGER NOT NULL DEFAULT 1,
			last_seen_at TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			deleted_at TEXT,
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE TABLE user_prompts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sync_id TEXT,
			session_id TEXT NOT NULL,
			content TEXT NOT NULL,
			project TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE TABLE sync_state (
			target_key TEXT PRIMARY KEY,
			lifecycle TEXT NOT NULL DEFAULT 'idle',
			last_enqueued_seq INTEGER NOT NULL DEFAULT 0,
			last_acked_seq INTEGER NOT NULL DEFAULT 0,
			last_pulled_seq INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			backoff_until TEXT,
			lease_owner TEXT,
			lease_until TEXT,
			last_error TEXT,
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE sync_mutations (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			target_key TEXT NOT NULL,
			entity TEXT NOT NULL,
			entity_key TEXT NOT NULL,
			op TEXT NOT NULL,
			payload TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'local',
			occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
			acked_at TEXT,
			project TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE sync_enrolled_projects (
			project TEXT PRIMARY KEY,
			enrolled_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		INSERT INTO sessions (id, project, directory) VALUES ('legacy-session', 'legacy-proj', '/tmp/legacy');
		INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, updated_at, deleted_at)
		VALUES ('obs-soft-deleted', 'legacy-session', 'decision', 'Legacy deleted', 'Historical deleted content', 'legacy-proj', 'project', ?, 1, 1, '2024-01-03 12:00:00', '2024-01-03 12:00:00');
		INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, 'idle', datetime('now'));
		INSERT INTO sync_enrolled_projects (project) VALUES ('legacy-proj');
	`, obsHash, DefaultSyncTargetKey)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = dataDir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var op string
	if err := s.db.QueryRow(
		`SELECT op FROM sync_mutations WHERE entity = ? AND entity_key = ?`,
		SyncEntityObservation,
		"obs-soft-deleted",
	).Scan(&op); err != nil {
		t.Fatalf("query repaired soft-delete mutation: %v", err)
	}
	if op != SyncOpDelete {
		t.Fatalf("expected repaired observation mutation op=delete, got %q", op)
	}
}

func TestNewRepairsAlreadyEnrolledProjectsMissingHistoricalSyncMutations(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "engram.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}

	obsHash := hashNormalized("Historical content")
	_, err = db.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			project TEXT NOT NULL,
			directory TEXT NOT NULL,
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			ended_at TEXT,
			summary TEXT
		);
		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sync_id TEXT,
			session_id TEXT NOT NULL,
			type TEXT NOT NULL,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			tool_name TEXT,
			project TEXT,
			scope TEXT NOT NULL DEFAULT 'project',
			topic_key TEXT,
			normalized_hash TEXT,
			revision_count INTEGER NOT NULL DEFAULT 1,
			duplicate_count INTEGER NOT NULL DEFAULT 1,
			last_seen_at TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			deleted_at TEXT,
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE TABLE user_prompts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sync_id TEXT,
			session_id TEXT NOT NULL,
			content TEXT NOT NULL,
			project TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
		CREATE TABLE sync_state (
			target_key TEXT PRIMARY KEY,
			lifecycle TEXT NOT NULL DEFAULT 'idle',
			last_enqueued_seq INTEGER NOT NULL DEFAULT 0,
			last_acked_seq INTEGER NOT NULL DEFAULT 0,
			last_pulled_seq INTEGER NOT NULL DEFAULT 0,
			consecutive_failures INTEGER NOT NULL DEFAULT 0,
			backoff_until TEXT,
			lease_owner TEXT,
			lease_until TEXT,
			last_error TEXT,
			updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE TABLE sync_mutations (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			target_key TEXT NOT NULL,
			entity TEXT NOT NULL,
			entity_key TEXT NOT NULL,
			op TEXT NOT NULL,
			payload TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'local',
			occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
			acked_at TEXT,
			project TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (target_key) REFERENCES sync_state(target_key)
		);
		CREATE TABLE sync_enrolled_projects (
			project TEXT PRIMARY KEY,
			enrolled_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		INSERT INTO sessions (id, project, directory, summary) VALUES ('legacy-session', 'legacy-proj', '/tmp/legacy', 'done');
		INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, last_seen_at, updated_at)
		VALUES ('obs-legacy', 'legacy-session', 'decision', 'Legacy obs', 'Historical content', 'legacy-proj', 'project', ?, 1, 1, datetime('now'), datetime('now'));
		INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES ('prompt-legacy', 'legacy-session', 'Historical prompt', 'legacy-proj');
		INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, 'idle', datetime('now'));
		INSERT INTO sync_enrolled_projects (project) VALUES ('legacy-proj');
	`, obsHash, DefaultSyncTargetKey)
	if err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = dataDir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store after enrolled legacy state: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		_ = s.Close()
		t.Fatalf("list pending after repair: %v", err)
	}
	if len(mutations) != 3 {
		_ = s.Close()
		t.Fatalf("expected 3 repaired mutations, got %d", len(mutations))
	}

	state, err := s.GetSyncState(DefaultSyncTargetKey)
	if err != nil {
		_ = s.Close()
		t.Fatalf("get sync state after repair: %v", err)
	}
	if state.LastEnqueuedSeq != 3 {
		_ = s.Close()
		t.Fatalf("expected last_enqueued_seq 3 after automatic repair, got %d", state.LastEnqueuedSeq)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close repaired store: %v", err)
	}

	s, err = New(cfg)
	if err != nil {
		t.Fatalf("reopen repaired store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&count); err != nil {
		t.Fatalf("count repaired mutations after reopen: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected repair to stay idempotent across reopen, got %d sync mutations", count)
	}
}

func TestEnrollProjectEmptyNameReturnsError(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject(""); err == nil {
		t.Fatal("expected error when enrolling empty project name")
	}
}

func TestUnenrollProjectBasic(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	// Unenroll.
	if err := s.UnenrollProject("engram"); err != nil {
		t.Fatalf("unenroll: %v", err)
	}

	// Should be gone.
	enrolled, err := s.IsProjectEnrolled("engram")
	if err != nil {
		t.Fatalf("is enrolled after unenroll: %v", err)
	}
	if enrolled {
		t.Fatal("expected project to be unenrolled")
	}

	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected 0 enrolled projects after unenroll, got %d", len(projects))
	}
}

func TestUnenrollProjectIdempotent(t *testing.T) {
	s := newTestStore(t)

	// Unenroll a project that was never enrolled — should not error.
	if err := s.UnenrollProject("nonexistent"); err != nil {
		t.Fatalf("unenroll non-enrolled project should be idempotent: %v", err)
	}
}

func TestUnenrollProjectEmptyNameReturnsError(t *testing.T) {
	s := newTestStore(t)

	if err := s.UnenrollProject(""); err == nil {
		t.Fatal("expected error when unenrolling empty project name")
	}
}

func TestIsProjectEnrolledReturnsFalseForUnknown(t *testing.T) {
	s := newTestStore(t)

	enrolled, err := s.IsProjectEnrolled("unknown-project")
	if err != nil {
		t.Fatalf("is enrolled: %v", err)
	}
	if enrolled {
		t.Fatal("expected false for unknown project")
	}
}

func TestListEnrolledProjectsEmpty(t *testing.T) {
	s := newTestStore(t)

	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list enrolled projects: %v", err)
	}
	if projects != nil {
		t.Fatalf("expected nil for empty list, got %v", projects)
	}
}

func TestListEnrolledProjectsAlphabeticalOrder(t *testing.T) {
	s := newTestStore(t)

	// Enroll in non-alphabetical order.
	for _, p := range []string{"zebra", "alpha", "mango"} {
		if err := s.EnrollProject(p); err != nil {
			t.Fatalf("enroll %q: %v", p, err)
		}
	}

	projects, err := s.ListEnrolledProjects()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(projects))
	}
	expected := []string{"alpha", "mango", "zebra"}
	for i, ep := range projects {
		if ep.Project != expected[i] {
			t.Fatalf("position %d: expected %q, got %q", i, expected[i], ep.Project)
		}
	}
}

func TestSyncMutationProjectColumnExists(t *testing.T) {
	s := newTestStore(t)

	// Verify the project column exists on sync_mutations by inserting a row.
	_, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		DefaultSyncTargetKey, "session", "test-key", SyncOpUpsert, `{"project":"myproj"}`, SyncSourceLocal, "myproj",
	)
	if err != nil {
		t.Fatalf("insert sync_mutation with project: %v", err)
	}

	// Read it back and verify project is populated.
	var project string
	if err := s.db.QueryRow(`SELECT project FROM sync_mutations WHERE entity_key = ?`, "test-key").Scan(&project); err != nil {
		t.Fatalf("scan project: %v", err)
	}
	if project != "myproj" {
		t.Fatalf("expected project 'myproj', got %q", project)
	}
}

func TestSyncMutationProjectBackfill(t *testing.T) {
	s := newTestStore(t)

	// Insert a mutation that simulates a pre-migration row (project is empty, but payload has it).
	// The backfill runs during schema init, so we test it by inserting directly then re-running.
	// Since the store already ran migrations, let's verify backfill logic by inserting a new row
	// with empty project and manually running the backfill.
	_, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		 VALUES (?, ?, ?, ?, ?, ?, '')`,
		DefaultSyncTargetKey, "observation", "backfill-key", SyncOpUpsert, `{"project":"backfilled"}`, SyncSourceLocal,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Run the backfill manually.
	_, err = s.db.Exec(`
		UPDATE sync_mutations
		SET project = COALESCE(json_extract(payload, '$.project'), '')
		WHERE project = '' AND payload != ''
	`)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var project string
	if err := s.db.QueryRow(`SELECT project FROM sync_mutations WHERE entity_key = ?`, "backfill-key").Scan(&project); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if project != "backfilled" {
		t.Fatalf("expected backfilled project 'backfilled', got %q", project)
	}
}

func TestSyncMutationProjectBackfillFromSessionID(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "sess-backfill", "derived-proj", "/tmp/derived"); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	_, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		 VALUES (?, ?, ?, ?, ?, ?, '')`,
		DefaultSyncTargetKey, SyncEntityObservation, "obs-backfill-session", SyncOpDelete, `{"sync_id":"obs-backfill-session","session_id":"sess-backfill","deleted":true,"hard_delete":true}`, SyncSourceLocal,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Same SQL used by migrate() for legacy rows that have empty project but include session metadata.
	_, err = s.db.Exec(`
		UPDATE sync_mutations
		SET project = COALESCE((
			SELECT sessions.project
			FROM sessions
			WHERE sessions.id = json_extract(sync_mutations.payload, '$.session_id')
		), '')
		WHERE project = ''
		  AND payload != ''
		  AND ifnull(json_extract(payload, '$.session_id'), '') != ''
	`)
	if err != nil {
		t.Fatalf("backfill from session_id: %v", err)
	}

	var project string
	if err := s.db.QueryRow(`SELECT project FROM sync_mutations WHERE entity_key = ?`, "obs-backfill-session").Scan(&project); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if project != "derived-proj" {
		t.Fatalf("expected session-derived project 'derived-proj', got %q", project)
	}
}

func TestListPendingSyncMutationsIncludesProject(t *testing.T) {
	s := newTestStore(t)

	// Enroll the project so mutations are visible in ListPendingSyncMutations.
	if err := s.EnrollProject("my-project"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	if err := s.CreateSession("proj-session", "my-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "proj-session",
		Type:      "decision",
		Title:     "Test obs",
		Content:   "Content",
		Project:   "my-project",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	// There should be mutations (session create + observation create at minimum).
	if len(mutations) == 0 {
		t.Fatal("expected at least one pending mutation")
	}

	// Phase 3: Verify the Project field is populated at enqueue time.
	foundProject := false
	for _, m := range mutations {
		if m.Project == "my-project" {
			foundProject = true
			break
		}
	}
	if !foundProject {
		t.Fatal("expected at least one mutation with project='my-project'")
	}
}

func TestCountPendingNonEnrolledSyncMutations(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("enrolled-project"); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	for i, project := range []string{"alpha", "alpha", "beta", "enrolled-project"} {
		if _, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey,
			SyncEntityObservation,
			fmt.Sprintf("key-%s-%d", project, i),
			SyncOpUpsert,
			`{}`,
			SyncSourceLocal,
			project,
		); err != nil {
			t.Fatalf("insert mutation for %s: %v", project, err)
		}
	}
	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, '')`,
		DefaultSyncTargetKey, SyncEntityObservation, "global-key", SyncOpUpsert, `{}`, SyncSourceLocal,
	); err != nil {
		t.Fatalf("insert global mutation: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project, acked_at) VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		DefaultSyncTargetKey, SyncEntityObservation, "acked-alpha", SyncOpUpsert, `{}`, SyncSourceLocal, "alpha",
	); err != nil {
		t.Fatalf("insert acked mutation: %v", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, 'idle', datetime('now'))`, "cloud:other"); err != nil {
		t.Fatalf("insert other sync state: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"cloud:other", SyncEntityObservation, "other-target-alpha", SyncOpUpsert, `{}`, SyncSourceLocal, "alpha",
	); err != nil {
		t.Fatalf("insert other target mutation: %v", err)
	}

	counts, err := s.CountPendingNonEnrolledSyncMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("count pending non-enrolled: %v", err)
	}
	want := []PendingSyncMutationProjectCount{{Project: "alpha", Count: 2}, {Project: "beta", Count: 1}}
	if len(counts) != len(want) {
		t.Fatalf("expected %d counts, got %#v", len(want), counts)
	}
	for i := range want {
		if counts[i] != want[i] {
			t.Fatalf("count[%d]: expected %#v, got %#v", i, want[i], counts[i])
		}
	}
}

// ─── Phase 3: extractProjectFromPayload ──────────────────────────────────────

func TestExtractProjectFromSessionPayload(t *testing.T) {
	p := syncSessionPayload{ID: "s1", Project: "acme"}
	got := extractProjectFromPayload(p)
	if got != "acme" {
		t.Fatalf("expected 'acme', got %q", got)
	}
}

func TestExtractProjectFromObservationPayload(t *testing.T) {
	proj := "obs-project"
	p := syncObservationPayload{SyncID: "obs-1", Project: &proj}
	got := extractProjectFromPayload(p)
	if got != "obs-project" {
		t.Fatalf("expected 'obs-project', got %q", got)
	}
}

func TestExtractProjectFromObservationPayloadNil(t *testing.T) {
	p := syncObservationPayload{SyncID: "obs-1", Project: nil}
	got := extractProjectFromPayload(p)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestExtractProjectFromPromptPayload(t *testing.T) {
	proj := "prompt-project"
	p := syncPromptPayload{SyncID: "p1", Project: &proj}
	got := extractProjectFromPayload(p)
	if got != "prompt-project" {
		t.Fatalf("expected 'prompt-project', got %q", got)
	}
}

func TestExtractProjectFromPromptPayloadNil(t *testing.T) {
	p := syncPromptPayload{SyncID: "p1", Project: nil}
	got := extractProjectFromPayload(p)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestExtractProjectFromUnknownPayloadFallback(t *testing.T) {
	// Unknown struct with a project field — uses JSON fallback.
	p := struct {
		Project string `json:"project"`
		Other   string `json:"other"`
	}{Project: "fallback-proj", Other: "x"}
	got := extractProjectFromPayload(p)
	if got != "fallback-proj" {
		t.Fatalf("expected 'fallback-proj', got %q", got)
	}
}

func TestExtractProjectFromPayloadWithoutProjectField(t *testing.T) {
	// Unknown struct without a project field — returns empty.
	p := struct {
		Name string `json:"name"`
	}{Name: "test"}
	got := extractProjectFromPayload(p)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

// ─── Phase 3: enqueueSyncMutationTx populates project column ────────────────

func TestEnqueueSyncMutationPopulatesProjectFromSessionPayload(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("enq-session", "enqueued-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// CreateSession enqueues a sync mutation internally. Check the project column.
	var project string
	err := s.db.QueryRow(
		`SELECT project FROM sync_mutations WHERE entity = ? AND entity_key = ?`,
		SyncEntitySession, "enq-session",
	).Scan(&project)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if project != "enqueued-project" {
		t.Fatalf("expected project='enqueued-project', got %q", project)
	}
}

func TestEnqueueSyncMutationPopulatesProjectFromObservationPayload(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("obs-enq", "obs-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "obs-enq",
		Type:      "decision",
		Title:     "Test",
		Content:   "Content",
		Project:   "obs-proj",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Check the observation mutation's project column.
	var project string
	err = s.db.QueryRow(
		`SELECT project FROM sync_mutations WHERE entity = ? ORDER BY seq DESC LIMIT 1`,
		SyncEntityObservation,
	).Scan(&project)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if project != "obs-proj" {
		t.Fatalf("expected project='obs-proj', got %q", project)
	}
}

func TestEnqueueSyncMutationPopulatesProjectFromPromptPayload(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("prompt-enq", "prompt-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddPrompt(AddPromptParams{
		SessionID: "prompt-enq",
		Content:   "What did we do?",
		Project:   "prompt-proj",
	})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	var project string
	err = s.db.QueryRow(
		`SELECT project FROM sync_mutations WHERE entity = ? ORDER BY seq DESC LIMIT 1`,
		SyncEntityPrompt,
	).Scan(&project)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if project != "prompt-proj" {
		t.Fatalf("expected project='prompt-proj', got %q", project)
	}
}

func TestEnqueueSyncMutationUsesProjectScopedTargetKey(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("target-session", "target-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	state, err := s.GetSyncState(syncTargetKeyForProject("target-proj"))
	if err != nil {
		t.Fatalf("get project-scoped sync state: %v", err)
	}
	if state.Lifecycle != SyncLifecyclePending {
		t.Fatalf("expected project-scoped lifecycle pending, got %q", state.Lifecycle)
	}
	if state.LastEnqueuedSeq == 0 {
		t.Fatal("expected project-scoped sync state to track last_enqueued_seq")
	}
}

// ─── Phase 4: ListPendingSyncMutations enrollment filtering ──────────────────

func TestListPendingFiltersNonEnrolledProjects(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-enrolled", "enrolled-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.CreateSession("s-not-enrolled", "other-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Enroll only "enrolled-proj".
	if err := s.EnrollProject("enrolled-proj"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	// Only enrolled-proj mutations should appear.
	for _, m := range mutations {
		if m.Project == "other-proj" {
			t.Fatalf("non-enrolled project 'other-proj' should not appear in pending mutations")
		}
	}

	foundEnrolled := false
	for _, m := range mutations {
		if m.Project == "enrolled-proj" {
			foundEnrolled = true
			break
		}
	}
	if !foundEnrolled {
		t.Fatal("expected enrolled-proj mutations to appear")
	}
}

func TestListPendingReturnsNoMutationsWhenNoneEnrolled(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-no-enroll", "some-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	// No projects enrolled → no mutations (all have project != '').
	if len(mutations) != 0 {
		t.Fatalf("expected 0 mutations when no projects enrolled, got %d", len(mutations))
	}
}

// ─── Phase 4: SkipAckNonEnrolledMutations ────────────────────────────────────

func TestSkipAckNonEnrolledMutationsBasic(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("skip-session", "skip-proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Do NOT enroll "skip-proj" → mutations should be skip-acked.
	skipped, err := s.SkipAckNonEnrolledMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("skip-ack: %v", err)
	}
	if skipped == 0 {
		t.Fatal("expected at least one mutation to be skip-acked")
	}

	// After skip-ack, there should be no pending mutations left.
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(mutations) != 0 {
		t.Fatalf("expected 0 pending mutations after skip-ack, got %d", len(mutations))
	}
}

func TestSkipAckPreservesEnrolledProjectMutations(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("enrolled"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	if err := s.CreateSession("s-enrolled", "enrolled", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.CreateSession("s-not-enrolled", "not-enrolled", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Count total pending before skip-ack.
	var totalBefore int
	s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE acked_at IS NULL`).Scan(&totalBefore)

	skipped, err := s.SkipAckNonEnrolledMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("skip-ack: %v", err)
	}
	if skipped == 0 {
		t.Fatal("expected at least one mutation to be skip-acked for 'not-enrolled'")
	}

	// Remaining pending should be only "enrolled" mutations.
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	for _, m := range mutations {
		if m.Project == "not-enrolled" {
			t.Fatal("skip-acked mutation still appears as pending")
		}
	}
	if len(mutations) == 0 {
		t.Fatal("expected enrolled-project mutations to remain")
	}
}

// ─── Phase 5: Empty/global project always syncs ──────────────────────────────

func TestEmptyProjectMutationsAlwaysSync(t *testing.T) {
	s := newTestStore(t)

	// Create a session with empty project (global).
	if err := s.CreateSession("global-session", "", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// No projects enrolled, but empty-project mutations should still appear.
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	if len(mutations) == 0 {
		t.Fatal("expected empty-project mutations to always sync regardless of enrollment")
	}

	// Verify they have project = ''.
	for _, m := range mutations {
		if m.Project != "" {
			t.Fatalf("expected empty project, got %q", m.Project)
		}
	}
}

func TestSkipAckDoesNotAffectEmptyProjectMutations(t *testing.T) {
	s := newTestStore(t)

	// Create a session with empty project (global).
	if err := s.CreateSession("global-session-2", "", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Count pending before skip-ack.
	beforeMutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	beforeCount := len(beforeMutations)

	// Skip-ack should not affect empty-project mutations.
	skipped, err := s.SkipAckNonEnrolledMutations(DefaultSyncTargetKey)
	if err != nil {
		t.Fatalf("skip-ack: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("expected 0 mutations to be skip-acked (all empty project), got %d", skipped)
	}

	// Verify count unchanged.
	afterMutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(afterMutations) != beforeCount {
		t.Fatalf("expected %d mutations after skip-ack, got %d", beforeCount, len(afterMutations))
	}
}

func TestMixedEnrolledAndEmptyProjectMutations(t *testing.T) {
	s := newTestStore(t)

	if err := s.EnrollProject("enrolled-mix"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	// Create sessions with different project states.
	if err := s.CreateSession("mix-enrolled", "enrolled-mix", "/tmp"); err != nil {
		t.Fatalf("create enrolled session: %v", err)
	}
	if err := s.CreateSession("mix-global", "", "/tmp"); err != nil {
		t.Fatalf("create global session: %v", err)
	}
	if err := s.CreateSession("mix-unenrolled", "unenrolled-mix", "/tmp"); err != nil {
		t.Fatalf("create unenrolled session: %v", err)
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}

	// Should have enrolled-mix and empty-project mutations, but NOT unenrolled-mix.
	var hasEnrolled, hasGlobal bool
	for _, m := range mutations {
		if m.Project == "unenrolled-mix" {
			t.Fatal("unenrolled project mutations should not appear")
		}
		if m.Project == "enrolled-mix" {
			hasEnrolled = true
		}
		if m.Project == "" {
			hasGlobal = true
		}
	}
	if !hasEnrolled {
		t.Fatal("expected enrolled-mix mutations to appear")
	}
	if !hasGlobal {
		t.Fatal("expected empty-project (global) mutations to appear")
	}
}

// ─── MigrateProject ─────────────────────────────────────────────────────────

func TestMigrateProject(t *testing.T) {
	s := newTestStore(t)
	old, new_ := "old-name", "new-name"

	// Seed data under old project name
	s.CreateSession("s1", old, "/tmp/old")
	s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision", Title: "test obs",
		Content: "some content", Project: old, Scope: "project",
	})
	s.AddPrompt(AddPromptParams{SessionID: "s1", Content: "test prompt", Project: old})

	// Run migration
	result, err := s.MigrateProject(old, new_)
	if err != nil {
		t.Fatalf("MigrateProject: %v", err)
	}
	if !result.Migrated {
		t.Fatal("expected migration to happen")
	}
	if result.ObservationsUpdated != 1 {
		t.Fatalf("expected 1 observation migrated, got %d", result.ObservationsUpdated)
	}
	if result.SessionsUpdated != 1 {
		t.Fatalf("expected 1 session migrated, got %d", result.SessionsUpdated)
	}
	if result.PromptsUpdated != 1 {
		t.Fatalf("expected 1 prompt migrated, got %d", result.PromptsUpdated)
	}

	// Verify old project has no records
	obs, _ := s.RecentObservations(old, "", 10)
	if len(obs) != 0 {
		t.Fatalf("expected 0 observations under old name, got %d", len(obs))
	}

	// Verify new project has the records
	obs, _ = s.RecentObservations(new_, "", 10)
	if len(obs) != 1 {
		t.Fatalf("expected 1 observation under new name, got %d", len(obs))
	}

	// Verify FTS search finds it under new project
	results, _ := s.Search("test obs", SearchOptions{Project: new_, Limit: 10})
	if len(results) != 1 {
		t.Fatalf("expected FTS to find 1 result under new project, got %d", len(results))
	}
}

func TestMigrateProjectNoOp(t *testing.T) {
	s := newTestStore(t)

	// No records under "nonexistent" — should be a no-op
	result, err := s.MigrateProject("nonexistent", "anything")
	if err != nil {
		t.Fatalf("MigrateProject: %v", err)
	}
	if result.Migrated {
		t.Fatal("expected no migration for nonexistent project")
	}
}

func TestMigrateProjectIdempotent(t *testing.T) {
	s := newTestStore(t)
	old, new_ := "old-proj", "new-proj"

	s.CreateSession("s1", old, "/tmp")
	s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision", Title: "test",
		Content: "content", Project: old, Scope: "project",
	})

	// First migration
	r1, err := s.MigrateProject(old, new_)
	if err != nil {
		t.Fatalf("first MigrateProject: %v", err)
	}
	if !r1.Migrated {
		t.Fatal("first migration should migrate")
	}

	// Second migration — no records under old name anymore
	r2, err := s.MigrateProject(old, new_)
	if err != nil {
		t.Fatalf("second MigrateProject: %v", err)
	}
	if r2.Migrated {
		t.Fatal("second migration should be a no-op")
	}
}

// ─── Phase 2: project-name-drift — NormalizeProject, ListProjectNames,
//              ListProjectsWithStats, MergeProjects tests ─────────────────────

func TestNormalizeProjectFunction(t *testing.T) {
	tests := []struct {
		input       string
		wantName    string
		wantWarning bool
	}{
		{"engram", "engram", false},
		{"Engram", "engram", true},
		{"ENGRAM", "engram", true},
		{"  engram  ", "engram", true},
		{"Engram-Memory", "engram-memory", true},
		{"engram--memory", "engram-memory", true},
		{"engram__memory", "engram_memory", true},
		{"", "", false},
		{"already-lower", "already-lower", false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, warning := NormalizeProject(tc.input)
			if got != tc.wantName {
				t.Errorf("NormalizeProject(%q) name = %q, want %q", tc.input, got, tc.wantName)
			}
			if tc.wantWarning && warning == "" {
				t.Errorf("NormalizeProject(%q) expected a warning, got empty string", tc.input)
			}
			if !tc.wantWarning && warning != "" {
				t.Errorf("NormalizeProject(%q) expected no warning, got %q", tc.input, warning)
			}
		})
	}
}

func TestAddObservationNormalizesProject(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Save with mixed-case project name
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Normalize test",
		Content:   "This should be stored under lowercase project",
		Project:   "Engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}

	// Stored project should be normalized to lowercase
	if obs.Project == nil || *obs.Project != "engram" {
		got := "<nil>"
		if obs.Project != nil {
			got = *obs.Project
		}
		t.Errorf("stored project = %q, want \"engram\"", got)
	}
}

func TestSearchNormalizesProjectFilter(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Store observation under already-lowercase project
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Search normalize test",
		Content:   "content for project filter normalization",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// Search with UPPERCASE project filter — should still find the record
	results, err := s.Search("normalize test", SearchOptions{
		Project: "Engram", // intentionally mixed-case
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) == 0 {
		t.Fatalf("expected ≥1 result when searching with normalized project filter, got 0")
	}
}

func TestRecentObservationsNormalizesProjectFilter(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Recent obs test",
		Content:   "some content",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// Query with uppercase project name
	obs, err := s.RecentObservations("ENGRAM", "", 10)
	if err != nil {
		t.Fatalf("RecentObservations: %v", err)
	}
	if len(obs) == 0 {
		t.Fatalf("expected ≥1 result with normalized project filter, got 0")
	}
}

func TestCreateSessionNormalizesProject(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s-norm", "MyProject", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sess, err := s.GetSession("s-norm")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Project != "myproject" {
		t.Errorf("expected project=myproject (normalized), got %q", sess.Project)
	}
}

func TestListProjectNames(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "alpha", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.CreateSession("s2", "beta", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	for _, proj := range []string{"alpha", "alpha", "beta", "gamma"} {
		_, err := s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "decision",
			Title:     "test " + proj,
			Content:   "content for " + proj,
			Project:   proj,
			Scope:     "project",
		})
		if err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
	}

	names, err := s.ListProjectNames()
	if err != nil {
		t.Fatalf("ListProjectNames: %v", err)
	}

	// Should return distinct names: alpha, beta, gamma
	want := map[string]bool{"alpha": true, "beta": true, "gamma": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected project name %q in results", n)
		}
		delete(want, n)
	}
	if len(want) > 0 {
		t.Errorf("missing project names: %v", want)
	}
}

func TestListProjectsWithStats(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "proj-a", "/work/a"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.CreateSession("s2", "proj-b", "/work/b"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add 3 observations to proj-a
	for i := 0; i < 3; i++ {
		_, err := s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "decision",
			Title:     "obs a",
			Content:   strings.Repeat("x", i+1), // unique content per obs
			Project:   "proj-a",
			Scope:     "project",
		})
		if err != nil {
			t.Fatalf("AddObservation proj-a: %v", err)
		}
	}

	// Add 1 observation to proj-b
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s2",
		Type:      "decision",
		Title:     "obs b",
		Content:   "content for proj-b",
		Project:   "proj-b",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation proj-b: %v", err)
	}

	stats, err := s.ListProjectsWithStats()
	if err != nil {
		t.Fatalf("ListProjectsWithStats: %v", err)
	}

	if len(stats) < 2 {
		t.Fatalf("expected ≥2 project stats, got %d", len(stats))
	}

	// Find proj-a and proj-b in results
	statsMap := make(map[string]ProjectStats)
	for _, ps := range stats {
		statsMap[ps.Name] = ps
	}

	if a, ok := statsMap["proj-a"]; !ok {
		t.Error("proj-a not in ListProjectsWithStats results")
	} else {
		if a.ObservationCount != 3 {
			t.Errorf("proj-a: expected 3 observations, got %d", a.ObservationCount)
		}
		if a.SessionCount != 1 {
			t.Errorf("proj-a: expected 1 session, got %d", a.SessionCount)
		}
	}

	if b, ok := statsMap["proj-b"]; !ok {
		t.Error("proj-b not in ListProjectsWithStats results")
	} else {
		if b.ObservationCount != 1 {
			t.Errorf("proj-b: expected 1 observation, got %d", b.ObservationCount)
		}
	}

	// Results should be sorted by observation count descending
	if stats[0].Name != "proj-a" {
		t.Errorf("expected proj-a first (most observations), got %q", stats[0].Name)
	}
}

func TestMergeProjects(t *testing.T) {
	s := newTestStore(t)

	// Set up three source projects
	sources := []string{"engram", "Engram", "engram-memory"}
	canonical := "engram"

	if err := s.CreateSession("s1", "engram", "/work"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Add observations to each source
	for _, src := range []string{"engram", "engram-memory"} {
		for i := 0; i < 2; i++ {
			_, err := s.AddObservation(AddObservationParams{
				SessionID: "s1",
				Type:      "decision",
				Title:     "obs from " + src,
				Content:   strings.Repeat(src, i+1),
				Project:   src,
				Scope:     "project",
			})
			if err != nil {
				t.Fatalf("AddObservation %s: %v", src, err)
			}
		}
	}

	result, err := s.MergeProjects(sources, canonical)
	if err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}

	if result.Canonical != "engram" {
		t.Errorf("canonical = %q, want \"engram\"", result.Canonical)
	}

	// "Engram" normalizes to "engram" (same as canonical) → skipped
	// "engram-memory" is different → merged
	// Only "engram-memory" should appear in SourcesMerged (and possibly "engram" if it had records,
	// but it equals canonical after normalization → skipped)
	for _, merged := range result.SourcesMerged {
		if merged == "engram" {
			t.Error("canonical 'engram' should not appear in SourcesMerged")
		}
	}

	// All records from engram-memory should now be under "engram"
	obs, err := s.RecentObservations("engram", "", 20)
	if err != nil {
		t.Fatalf("RecentObservations: %v", err)
	}
	if len(obs) < 4 {
		t.Errorf("expected ≥4 observations under 'engram' after merge, got %d", len(obs))
	}

	// engram-memory should have 0 observations
	obsMerged, err := s.RecentObservations("engram-memory", "", 10)
	if err != nil {
		t.Fatalf("RecentObservations engram-memory: %v", err)
	}
	if len(obsMerged) != 0 {
		t.Errorf("expected 0 observations under 'engram-memory' after merge, got %d", len(obsMerged))
	}
}

func TestMergeProjectsIdempotent(t *testing.T) {
	s := newTestStore(t)

	// Merge a nonexistent source — should not error
	result, err := s.MergeProjects([]string{"ghost-project"}, "engram")
	if err != nil {
		t.Fatalf("MergeProjects with nonexistent source: %v", err)
	}
	if result.ObservationsUpdated != 0 {
		t.Errorf("expected 0 observations updated for nonexistent source, got %d", result.ObservationsUpdated)
	}
}

func TestMergeProjectsCanonicalInSources(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/work"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Put some obs under "engram"
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "existing",
		Content:   "existing observation",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	// Sources include the canonical itself — should be silently skipped
	result, err := s.MergeProjects([]string{"engram", "Engram"}, "engram")
	if err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}

	// Nothing should have been changed (engram and Engram both normalize to "engram" = canonical)
	if result.ObservationsUpdated != 0 {
		t.Errorf("expected 0 observations updated when sources equal canonical, got %d", result.ObservationsUpdated)
	}
	if len(result.SourcesMerged) != 0 {
		t.Errorf("expected empty SourcesMerged when all sources equal canonical, got %v", result.SourcesMerged)
	}
}

func TestMergeProjectsNormalizesAliasSourcesWithoutLosingLegacyRows(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "legacy-session", "Engram Memory", "/work/engram"); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "legacy-obs", "legacy-session", "decision", "legacy", "legacy content", "Engram Memory", "project", "legacy-hash"); err != nil {
		t.Fatalf("seed legacy observation: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`, "legacy-prompt", "legacy-session", "legacy prompt", "Engram Memory"); err != nil {
		t.Fatalf("seed legacy prompt: %v", err)
	}

	result, err := s.MergeProjects([]string{"Engram Memory"}, "engram")
	if err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}
	if result.ObservationsUpdated != 1 || result.SessionsUpdated != 1 || result.PromptsUpdated != 1 {
		t.Fatalf("unexpected merge result: %+v", result)
	}
	if len(result.SourcesMerged) != 1 || result.SourcesMerged[0] != "engram memory" {
		t.Fatalf("SourcesMerged = %v, want [engram memory]", result.SourcesMerged)
	}

	for _, table := range []string{"sessions", "observations", "user_prompts"} {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE project = ?`, "Engram Memory").Scan(&count); err != nil {
			t.Fatalf("count legacy rows in %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s still has %d legacy project rows", table, count)
		}
	}
}

func TestMergeProjectsConsolidatesDeterministicAliasSpellings(t *testing.T) {
	s := newTestStore(t)

	for i, project := range []string{"Engram Memory", "engram memory", "engram-memory", "engram_memory"} {
		sessionID := fmt.Sprintf("alias-session-%d", i)
		if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, sessionID, project, "/work/engram"); err != nil {
			t.Fatalf("seed alias session %q: %v", project, err)
		}
		if _, err := s.db.Exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, fmt.Sprintf("alias-obs-%d", i), sessionID, "decision", "alias", "alias content", project, "project", fmt.Sprintf("alias-hash-%d", i)); err != nil {
			t.Fatalf("seed alias observation %q: %v", project, err)
		}
		if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`, fmt.Sprintf("alias-prompt-%d", i), sessionID, "alias prompt", project); err != nil {
			t.Fatalf("seed alias prompt %q: %v", project, err)
		}
	}

	result, err := s.MergeProjects([]string{"Engram Memory"}, "engram")
	if err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}
	if result.ObservationsUpdated != 4 || result.SessionsUpdated != 4 || result.PromptsUpdated != 4 {
		t.Fatalf("unexpected merge result: %+v", result)
	}
}

func TestMergeProjectsAliasVariantsDoNotRewriteCanonicalProject(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "canonical-session", "engram-memory", "/work/engram"); err != nil {
		t.Fatalf("seed canonical session: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "source-session", "Engram Memory", "/work/engram"); err != nil {
		t.Fatalf("seed source session: %v", err)
	}

	result, err := s.MergeProjects([]string{"Engram Memory"}, "engram-memory")
	if err != nil {
		t.Fatalf("MergeProjects: %v", err)
	}
	if result.SessionsUpdated != 1 {
		t.Fatalf("SessionsUpdated = %d, want 1", result.SessionsUpdated)
	}
	var canonicalRows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE project = ?`, "engram-memory").Scan(&canonicalRows); err != nil {
		t.Fatalf("count canonical rows: %v", err)
	}
	if canonicalRows != 2 {
		t.Fatalf("canonical rows = %d, want 2", canonicalRows)
	}
}

func TestNewLimitsSQLiteConnectionPoolToSingleOpenConnection(t *testing.T) {
	s := newTestStore(t)
	stats := s.db.Stats()
	if stats.MaxOpenConnections != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
}

func TestCountObservationsForProject(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "alpha", "/work/alpha"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// No observations yet — count should be 0
	count, err := s.CountObservationsForProject("alpha")
	if err != nil {
		t.Fatalf("CountObservationsForProject: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	// Add two observations
	for i := 0; i < 2; i++ {
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "decision",
			Title:     "obs " + string(rune('A'+i)),
			Content:   "unique content that is definitely unique " + string(rune('A'+i)),
			Project:   "alpha",
			Scope:     "project",
		}); err != nil {
			t.Fatalf("AddObservation: %v", err)
		}
	}

	count, err = s.CountObservationsForProject("alpha")
	if err != nil {
		t.Fatalf("CountObservationsForProject: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}

	// Different project should return 0
	count, err = s.CountObservationsForProject("beta")
	if err != nil {
		t.Fatalf("CountObservationsForProject for beta: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 for beta, got %d", count)
	}
}

// ─── DeleteSession tests ─────────────────────────────────────────────────────

func TestRecentObservationsOrderByCreatedAtBeforeID(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-recent-created", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	rows := []struct {
		id        int64
		title     string
		createdAt string
	}{
		{id: 100, title: "older-high-id", createdAt: "2025-01-01 00:00:00"},
		{id: 50, title: "newer-low-id", createdAt: "2025-01-02 00:00:00"},
	}
	for _, row := range rows {
		if _, err := s.db.Exec(`INSERT INTO observations (id, sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
			VALUES (?, ?, 's-recent-created', 'note', ?, ?, 'proj', 'project', ?, 1, 1, ?, ?)`, row.id, fmt.Sprintf("obs-%d", row.id), row.title, row.title, row.title, row.createdAt, row.createdAt); err != nil {
			t.Fatalf("insert observation %d: %v", row.id, err)
		}
	}

	obs, err := s.RecentObservations("proj", "project", 10)
	if err != nil {
		t.Fatalf("RecentObservations: %v", err)
	}
	if len(obs) < 2 || obs[0].Title != "newer-low-id" || obs[1].Title != "older-high-id" {
		t.Fatalf("expected created_at desc before id desc, got %+v", obs)
	}
}

func TestRecentObservationsSameTimestampTiesByIDDesc(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s-recent-tie", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, id := range []int64{10, 20} {
		if _, err := s.db.Exec(`INSERT INTO observations (id, sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
			VALUES (?, ?, 's-recent-tie', 'note', ?, ?, 'proj', 'project', ?, 1, 1, '2025-01-01 00:00:00', '2025-01-01 00:00:00')`, id, fmt.Sprintf("obs-tie-%d", id), fmt.Sprintf("tie-%d", id), fmt.Sprintf("tie-%d", id), fmt.Sprintf("hash-%d", id)); err != nil {
			t.Fatalf("insert observation %d: %v", id, err)
		}
	}

	obs, err := s.RecentObservations("proj", "project", 10)
	if err != nil {
		t.Fatalf("RecentObservations: %v", err)
	}
	if len(obs) < 2 || obs[0].ID != 20 || obs[1].ID != 10 {
		t.Fatalf("expected id desc tie-breaker, got %+v", obs)
	}
}

func TestRecentSessionsOrderByLatestCreatedAtDeterministically(t *testing.T) {
	s := newTestStore(t)
	for _, sess := range []struct {
		id        string
		startedAt string
	}{
		{id: "sess-a", startedAt: "2025-01-01 00:00:00"},
		{id: "sess-b", startedAt: "2025-01-01 00:00:00"},
		{id: "sess-c", startedAt: "2025-01-01 00:00:00"},
	} {
		if err := s.CreateSession(sess.id, "proj", "/tmp"); err != nil {
			t.Fatalf("create session %s: %v", sess.id, err)
		}
		if _, err := s.db.Exec(`UPDATE sessions SET started_at = ? WHERE id = ?`, sess.startedAt, sess.id); err != nil {
			t.Fatalf("update session %s: %v", sess.id, err)
		}
	}
	for _, row := range []struct {
		id        int64
		sessionID string
		createdAt string
	}{
		{id: 1, sessionID: "sess-a", createdAt: "2025-01-03 00:00:00"},
		{id: 2, sessionID: "sess-b", createdAt: "2025-01-02 00:00:00"},
		{id: 3, sessionID: "sess-c", createdAt: "2025-01-03 00:00:00"},
	} {
		if _, err := s.db.Exec(`INSERT INTO observations (id, sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
			VALUES (?, ?, ?, 'note', ?, ?, 'proj', 'project', ?, 1, 1, ?, ?)`, row.id, fmt.Sprintf("obs-session-%d", row.id), row.sessionID, row.sessionID, row.sessionID, fmt.Sprintf("hash-session-%d", row.id), row.createdAt, row.createdAt); err != nil {
			t.Fatalf("insert observation %d: %v", row.id, err)
		}
	}

	sessions, err := s.RecentSessions("proj", 10)
	if err != nil {
		t.Fatalf("RecentSessions: %v", err)
	}
	if len(sessions) < 3 || sessions[0].ID != "sess-c" || sessions[1].ID != "sess-a" || sessions[2].ID != "sess-b" {
		t.Fatalf("expected latest created_at desc with session id desc tie-breaker, got %+v", sessions)
	}
}

func TestDeleteSession_EmptySession(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-empty", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := s.DeleteSession("sess-empty"); err != nil {
		t.Fatalf("expected no error deleting empty session, got: %v", err)
	}

	// Session should be gone.
	sessions, err := s.RecentSessions("proj", 10)
	if err != nil {
		t.Fatalf("recent sessions: %v", err)
	}
	for _, ss := range sessions {
		if ss.ID == "sess-empty" {
			t.Fatal("expected session to be deleted but it still exists")
		}
	}
}

func TestDeleteSession_EnrolledProjectEnqueuesSyncDeleteMutation(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-enrolled", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-enrolled",
		Content:   "prompt should remain",
		Project:   "proj",
	}); err != nil {
		t.Fatalf("add prompt: %v", err)
	}
	if err := s.EnrollProject("proj"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	if err := s.DeleteSession("sess-enrolled"); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	sessions, err := s.RecentSessions("proj", 10)
	if err != nil {
		t.Fatalf("recent sessions: %v", err)
	}
	found := false
	for _, ss := range sessions {
		if ss.ID == "sess-enrolled" {
			found = true
			break
		}
	}
	if found {
		t.Fatal("expected enrolled session to be deleted")
	}

	prompts, err := s.RecentPrompts("proj", 10)
	if err != nil {
		t.Fatalf("recent prompts: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("expected prompt rows to be removed with enrolled delete, got %d", len(prompts))
	}

	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 10)
	if err != nil {
		t.Fatalf("list pending mutations: %v", err)
	}
	if len(mutations) == 0 {
		t.Fatal("expected session delete mutation to be enqueued")
	}
	last := mutations[len(mutations)-1]
	if last.Entity != SyncEntitySession || last.EntityKey != "sess-enrolled" || last.Op != SyncOpDelete {
		t.Fatalf("expected final mutation session/delete for sess-enrolled, got %+v", last)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(last.Payload), &payload); err != nil {
		t.Fatalf("decode session delete payload: %v", err)
	}
	if payload["id"] != "sess-enrolled" {
		t.Fatalf("expected delete payload id sess-enrolled, got %#v", payload["id"])
	}
	if payload["project"] != "proj" {
		t.Fatalf("expected delete payload project proj, got %#v", payload["project"])
	}
	if _, ok := payload["deleted_at"]; !ok {
		t.Fatalf("expected delete payload to include deleted_at, got %#v", payload)
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	s := newTestStore(t)

	err := s.DeleteSession("does-not-exist")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got: %v", err)
	}
}

func TestDeleteSession_HasActiveObservations(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-has-obs", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-has-obs",
		Type:      "decision",
		Title:     "some decision",
		Content:   "content",
		Project:   "proj",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	err := s.DeleteSession("sess-has-obs")
	if !errors.Is(err, ErrSessionHasObservations) {
		t.Fatalf("expected ErrSessionHasObservations, got: %v", err)
	}
}

func TestDeleteSession_HasSoftDeletedObservations(t *testing.T) {
	// Even soft-deleted observations must block the session delete
	// to avoid FK constraint violations.
	s := newTestStore(t)

	if err := s.CreateSession("sess-soft", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	obsID, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-soft",
		Type:      "decision",
		Title:     "soft deleted obs",
		Content:   "content",
		Project:   "proj",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if err := s.DeleteObservation(obsID, false); err != nil {
		t.Fatalf("soft delete observation: %v", err)
	}

	err = s.DeleteSession("sess-soft")
	if !errors.Is(err, ErrSessionHasObservations) {
		t.Fatalf("expected ErrSessionHasObservations for soft-deleted obs, got: %v", err)
	}
}

func TestDeleteSession_DeletesPromptsAlso(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-with-prompts", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-with-prompts",
		Content:   "a prompt",
		Project:   "proj",
	}); err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	if err := s.DeleteSession("sess-with-prompts"); err != nil {
		t.Fatalf("delete session: %v", err)
	}

	prompts, err := s.RecentPrompts("proj", 10)
	if err != nil {
		t.Fatalf("recent prompts: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("expected prompts to be deleted with session, got %d", len(prompts))
	}
}

func TestDeleteSession_FKConstraintFallback(t *testing.T) {
	// Verify that a SQLite FK constraint error on the DELETE FROM sessions
	// statement is translated into ErrSessionHasObservations.
	//
	// SQLite is a single-writer database, so it is not possible to inject an
	// observation from a concurrent connection while the transaction already
	// holds the write lock. Instead we simulate the race by:
	//   1. Pre-inserting an observation directly (bypassing store logic).
	//   2. Mocking the queryIt hook so the COUNT query returns 0 (as if the
	//      observation arrived after the count).
	//   3. Letting DeleteSession proceed; the DELETE FROM sessions then fails
	//      with a real SQLite FK constraint error (SQLITE_CONSTRAINT_FOREIGNKEY).
	s := newTestStore(t)

	if err := s.CreateSession("sess-race", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Insert the observation directly, bypassing the store COUNT guard.
	if _, err := s.db.Exec(`
		INSERT INTO observations
			(session_id, type, title, content, project, scope, created_at, updated_at, sync_id, duplicate_count, revision_count)
		VALUES
			('sess-race', 'decision', 'race obs', 'content', 'proj', 'project',
			 datetime('now'), datetime('now'), 'sync-race-1', 1, 1)`); err != nil {
		t.Fatalf("pre-insert observation: %v", err)
	}

	// Mock queryIt so the COUNT returns 0, simulating the race window where the
	// observation did not exist when the count ran.
	origQueryIt := s.hooks.queryIt
	faked := false
	s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
		if !faked && strings.Contains(query, "COUNT(*)") && strings.Contains(query, "observations WHERE session_id") {
			faked = true
			// Return a scanner that always yields count = 0.
			return &fakeCountScanner{}, nil
		}
		return origQueryIt(db, query, args...)
	}
	defer func() { s.hooks = defaultStoreHooks() }()

	err := s.DeleteSession("sess-race")
	if !errors.Is(err, ErrSessionHasObservations) {
		t.Fatalf("expected ErrSessionHasObservations from FK constraint, got: %v", err)
	}
}

// fakeCountScanner is a rowScanner that yields a single row with value 0,
// used to simulate a COUNT(*) result of zero.
type fakeCountScanner struct {
	done bool
}

func (f *fakeCountScanner) Next() bool {
	if f.done {
		return false
	}
	f.done = true
	return true
}
func (f *fakeCountScanner) Scan(dest ...any) error {
	if len(dest) > 0 {
		if p, ok := dest[0].(*int); ok {
			*p = 0
		}
	}
	return nil
}
func (f *fakeCountScanner) Err() error   { return nil }
func (f *fakeCountScanner) Close() error { return nil }

// ─── DeletePrompt tests ──────────────────────────────────────────────────────

func TestDeletePrompt_Success(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-p", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-p",
		Content:   "delete me",
		Project:   "proj",
	})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	if err := s.DeletePrompt(id); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	prompts, err := s.RecentPrompts("proj", 10)
	if err != nil {
		t.Fatalf("recent prompts: %v", err)
	}
	if len(prompts) != 0 {
		t.Fatalf("expected prompt to be deleted, got %d", len(prompts))
	}
}

func TestDeletePrompt_NotFound(t *testing.T) {
	s := newTestStore(t)

	err := s.DeletePrompt(999999)
	if !errors.Is(err, ErrPromptNotFound) {
		t.Fatalf("expected ErrPromptNotFound, got: %v", err)
	}
}

// ─── ProjectExists tests (Batch 2 — REQ-315) ─────────────────────────────────

func TestProjectExists_EmptyStore(t *testing.T) {
	s := newTestStore(t)

	exists, err := s.ProjectExists("any-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected false on empty store")
	}
}

func TestProjectExists_Known(t *testing.T) {
	s := newTestStore(t)

	// Insert an observation for the target project.
	if err := s.CreateSession("sess-1", "my-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-1",
		Type:      "manual",
		Title:     "test",
		Content:   "test content",
		Project:   "my-project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	exists, err := s.ProjectExists("my-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected true for known project with observation")
	}
}

func TestProjectExists_KnownViaSession(t *testing.T) {
	s := newTestStore(t)

	// Only a session, no observations.
	if err := s.CreateSession("sess-only", "session-only-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	exists, err := s.ProjectExists("session-only-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected true for project with a session only")
	}
}

func TestProjectExists_KnownViaPrompt(t *testing.T) {
	s := newTestStore(t)

	// Only a prompt, no session or observation.
	if err := s.CreateSession("sess-prompt", "prompt-only-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := s.AddPrompt(AddPromptParams{
		SessionID: "sess-prompt",
		Content:   "what is this?",
		Project:   "prompt-only-project",
	})
	if err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	exists, err := s.ProjectExists("prompt-only-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected true for project with a prompt only")
	}
}

func TestProjectExists_Unknown(t *testing.T) {
	s := newTestStore(t)

	// Populate with a different project.
	if err := s.CreateSession("sess-other", "other-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "sess-other",
		Type:      "manual",
		Title:     "other",
		Content:   "other content",
		Project:   "other-project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	exists, err := s.ProjectExists("does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected false for unknown project in populated store")
	}
}

// TestProjectExists_KnownViaEnrollmentOnly: a project enrolled via EnrollProject()
// with no observations/sessions/prompts must still be found by ProjectExists (JC1).
func TestProjectExists_KnownViaEnrollmentOnly(t *testing.T) {
	s := newTestStore(t)

	// Enroll a project — no observations, sessions, or prompts.
	if err := s.EnrollProject("enrolled-only-project"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	exists, err := s.ProjectExists("enrolled-only-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("enrolled-only-project must be found via sync_enrolled_projects UNION ALL branch")
	}
}

// ─── Doctor diagnostic helpers ───────────────────────────────────────────────

func TestListDiagnosticSessionsScopesByProject(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("manual-save-engram", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.CreateSession("manual-save-other", "other", "/work/other"); err != nil {
		t.Fatalf("CreateSession other: %v", err)
	}
	sessions, err := s.ListDiagnosticSessions("engram")
	if err != nil {
		t.Fatalf("ListDiagnosticSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "manual-save-engram" || sessions[0].Name != "manual-save-engram" {
		t.Fatalf("sessions=%+v", sessions)
	}
}

func TestListPendingProjectMutationsAndPayloadValidation(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`, DefaultSyncTargetKey, SyncEntityObservation, "obs-1", SyncOpUpsert, `{"sync_id":"obs-1"}`, SyncSourceLocal, "engram"); err != nil {
		t.Fatalf("insert pending mutation: %v", err)
	}
	mutations, err := s.ListPendingProjectMutations("engram")
	if err != nil {
		t.Fatalf("ListPendingProjectMutations: %v", err)
	}
	if len(mutations) != 1 {
		t.Fatalf("mutations=%+v", mutations)
	}
	validation := ValidateSyncMutationPayload(mutations[0].Entity, mutations[0].Op, mutations[0].Payload, mutations[0].EntityKey)
	if validation.ReasonCode != "sync_mutation_payload_missing_required_fields" {
		t.Fatalf("validation=%+v", validation)
	}
	if strings.Join(validation.MissingFields, ",") != "session_id,type,title,content,scope" {
		t.Fatalf("missing fields=%v", validation.MissingFields)
	}
}

func TestReadSQLiteLockSnapshotDoesNotMutateApplicationRows(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "engram", "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var before int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	snapshot, err := s.ReadSQLiteLockSnapshot(context.Background())
	if err != nil {
		t.Fatalf("ReadSQLiteLockSnapshot: %v", err)
	}
	var after int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if before != after {
		t.Fatalf("session count changed: before=%d after=%d", before, after)
	}
	if snapshot.JournalMode == "" || snapshot.BusyTimeoutMS <= 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

// newTestStoreRaw creates a store that runs migrations but skips the startup repair,
// allowing tests to seed data and call repairEnrolledProjectSyncMutations themselves.
func newTestStoreRaw(t *testing.T) *Store {
	t.Helper()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.DedupeWindow = time.Hour

	s, err := newWithoutRepair(cfg)
	if err != nil {
		t.Fatalf("new raw store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

// countSyncMutations returns the total number of rows in sync_mutations for a project.
func countSyncMutations(t *testing.T, s *Store, project string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE project = ? AND source = ?`,
		project, SyncSourceLocal,
	).Scan(&n); err != nil {
		t.Fatalf("count sync_mutations: %v", err)
	}
	return n
}

// TestRepairIsIdempotentOnAlreadyRepairedStore verifies that calling
// repairEnrolledProjectSyncMutations on a store where all sessions/observations/prompts
// already have corresponding sync_mutations is a no-op (no new mutations added).
func TestRepairIsIdempotentOnAlreadyRepairedStore(t *testing.T) {
	s := newTestStoreRaw(t)

	// Enroll so repair considers this project.
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_enrolled_projects (project) VALUES (?)`, "engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Create a session and observation via the normal API (which also enqueues mutations).
	if err := s.CreateSession("s-repair-1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s-repair-1",
		Type:      "decision",
		Title:     "Title",
		Content:   "Content",
		Project:   "engram",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{
		SessionID: "s-repair-1",
		Content:   "a prompt",
		Project:   "engram",
	}); err != nil {
		t.Fatalf("add prompt: %v", err)
	}

	// At this point mutations exist for session + observation + prompt.
	before := countSyncMutations(t, s, "engram")
	if before == 0 {
		t.Fatalf("expected mutations to exist before repair; got 0")
	}

	// Run repair — must be idempotent.
	start := time.Now()
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("repair: %v", err)
	}
	elapsed := time.Since(start)

	after := countSyncMutations(t, s, "engram")
	if after != before {
		t.Fatalf("repair added mutations on an already-repaired store: before=%d after=%d", before, after)
	}
	// Must complete quickly — no busy loop.
	if elapsed > 2*time.Second {
		t.Fatalf("repair took too long (%v); possible busy loop", elapsed)
	}
}

// TestRepairBackfillsMissingMutations verifies that sessions/observations/prompts
// that exist without corresponding sync_mutations are backfilled by repair.
func TestRepairBackfillsMissingMutations(t *testing.T) {
	s := newTestStoreRaw(t)

	// Enroll project.
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_enrolled_projects (project) VALUES (?)`, "engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Insert a session directly (bypasses mutation enqueue).
	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`,
		"s-missing", "engram", "/tmp/engram",
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Insert an observation directly (bypasses mutation enqueue).
	obsSync := "obs-sync-uuid-001"
	if _, err := s.db.Exec(`
		INSERT INTO observations (session_id, type, title, content, project, scope, normalized_hash,
		                          revision_count, duplicate_count, last_seen_at, updated_at, sync_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, datetime('now'), datetime('now'), ?)`,
		"s-missing", "decision", "T", "C", "engram", "project", hashNormalized("C"), obsSync,
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	// Insert a prompt directly (bypasses mutation enqueue).
	promptSync := "prompt-sync-uuid-001"
	if _, err := s.db.Exec(`
		INSERT INTO user_prompts (session_id, content, project, created_at, sync_id)
		VALUES (?, ?, ?, datetime('now'), ?)`,
		"s-missing", "hello", "engram", promptSync,
	); err != nil {
		t.Fatalf("insert prompt: %v", err)
	}

	// Confirm no mutations exist yet.
	before := countSyncMutations(t, s, "engram")
	if before != 0 {
		t.Fatalf("expected 0 mutations before repair, got %d", before)
	}

	// Run repair.
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("repair: %v", err)
	}

	// Mutations must now exist for session, observation, and prompt.
	after := countSyncMutations(t, s, "engram")
	if after == 0 {
		t.Fatalf("repair did not backfill any mutations; expected >=3, got 0")
	}

	// Session mutation must exist.
	var sessionMutCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND source = ?`,
		SyncEntitySession, "s-missing", SyncSourceLocal,
	).Scan(&sessionMutCount); err != nil {
		t.Fatalf("count session mutations: %v", err)
	}
	if sessionMutCount == 0 {
		t.Fatalf("expected session mutation to be backfilled, got 0")
	}

	// Observation mutation must exist.
	var obsMutCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND source = ?`,
		SyncEntityObservation, obsSync, SyncSourceLocal,
	).Scan(&obsMutCount); err != nil {
		t.Fatalf("count observation mutations: %v", err)
	}
	if obsMutCount == 0 {
		t.Fatalf("expected observation mutation to be backfilled, got 0")
	}

	// Prompt mutation must exist.
	var promptMutCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND source = ?`,
		SyncEntityPrompt, promptSync, SyncSourceLocal,
	).Scan(&promptMutCount); err != nil {
		t.Fatalf("count prompt mutations: %v", err)
	}
	if promptMutCount == 0 {
		t.Fatalf("expected prompt mutation to be backfilled, got 0")
	}
}

// TestRepairDoesNotDeadlockWithCursorAndInsert verifies that repair handles
// 100 sessions without mutations correctly — no deadlock, cursor-insert interference,
// or busy loop.
func TestRepairDoesNotDeadlockWithCursorAndInsert(t *testing.T) {
	s := newTestStoreRaw(t)

	// Enroll project.
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_enrolled_projects (project) VALUES (?)`, "engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}

	// Insert 100 sessions directly (no mutations).
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("session-%03d", i)
		if _, err := s.db.Exec(
			`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`,
			id, "engram", "/tmp/engram",
		); err != nil {
			t.Fatalf("insert session %s: %v", id, err)
		}
	}

	before := countSyncMutations(t, s, "engram")
	if before != 0 {
		t.Fatalf("expected 0 mutations before repair, got %d", before)
	}

	start := time.Now()
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("repair: %v", err)
	}
	elapsed := time.Since(start)

	after := countSyncMutations(t, s, "engram")
	if after != 100 {
		t.Fatalf("expected 100 session mutations after repair, got %d", after)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("repair took too long (%v) for 100 sessions; possible deadlock or busy loop", elapsed)
	}
}

// TestRepairHandlesMixedState verifies that repair only backfills what is missing:
// project A is fully repaired, project B is partially repaired, project C has nothing.
func TestRepairHandlesMixedState(t *testing.T) {
	s := newTestStoreRaw(t)

	// Enroll 3 projects.
	for _, p := range []string{"proj-a", "proj-b", "proj-c"} {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_enrolled_projects (project) VALUES (?)`, p); err != nil {
			t.Fatalf("enroll %s: %v", p, err)
		}
	}

	// proj-a: 2 sessions inserted directly, then manually enqueue mutations (fully repaired).
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("a-sess-%d", i)
		if _, err := s.db.Exec(
			`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`,
			id, "proj-a", "/tmp/a",
		); err != nil {
			t.Fatalf("insert proj-a session: %v", err)
		}
	}
	// For proj-a: simulate already-repaired by creating sync_state + mutations directly.
	if _, err := s.db.Exec(
		`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, ?, datetime('now'))`,
		DefaultSyncTargetKey, SyncLifecycleIdle,
	); err != nil {
		t.Fatalf("insert sync_state: %v", err)
	}
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("a-sess-%d", i)
		if _, err := s.db.Exec(
			`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			DefaultSyncTargetKey, SyncEntitySession, id, SyncOpUpsert, `{}`, SyncSourceLocal, "proj-a",
		); err != nil {
			t.Fatalf("insert proj-a mutation: %v", err)
		}
	}

	// proj-b: 3 sessions, only 1 has a mutation (partial).
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("b-sess-%d", i)
		if _, err := s.db.Exec(
			`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`,
			id, "proj-b", "/tmp/b",
		); err != nil {
			t.Fatalf("insert proj-b session: %v", err)
		}
	}
	// Only b-sess-0 has a mutation.
	if _, err := s.db.Exec(
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		DefaultSyncTargetKey, SyncEntitySession, "b-sess-0", SyncOpUpsert, `{}`, SyncSourceLocal, "proj-b",
	); err != nil {
		t.Fatalf("insert proj-b partial mutation: %v", err)
	}

	// proj-c: 2 sessions, no mutations.
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("c-sess-%d", i)
		if _, err := s.db.Exec(
			`INSERT INTO sessions (id, project, directory, started_at) VALUES (?, ?, ?, datetime('now'))`,
			id, "proj-c", "/tmp/c",
		); err != nil {
			t.Fatalf("insert proj-c session: %v", err)
		}
	}

	// Snapshot counts before repair.
	beforeA := countSyncMutations(t, s, "proj-a")
	beforeB := countSyncMutations(t, s, "proj-b")
	beforeC := countSyncMutations(t, s, "proj-c")

	if beforeA != 2 {
		t.Fatalf("proj-a: expected 2 mutations before repair, got %d", beforeA)
	}
	if beforeB != 1 {
		t.Fatalf("proj-b: expected 1 mutation before repair, got %d", beforeB)
	}
	if beforeC != 0 {
		t.Fatalf("proj-c: expected 0 mutations before repair, got %d", beforeC)
	}

	// Run repair.
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatalf("repair: %v", err)
	}

	afterA := countSyncMutations(t, s, "proj-a")
	afterB := countSyncMutations(t, s, "proj-b")
	afterC := countSyncMutations(t, s, "proj-c")

	// proj-a: fully repaired — count must not change.
	if afterA != beforeA {
		t.Fatalf("proj-a: repair must not add mutations to fully repaired project: before=%d after=%d", beforeA, afterA)
	}
	// proj-b: was missing 2 sessions — must now have 3 total.
	if afterB != 3 {
		t.Fatalf("proj-b: expected 3 mutations after repair (1 existing + 2 backfilled), got %d", afterB)
	}
	// proj-c: was missing all 2 sessions — must now have 2.
	if afterC != 2 {
		t.Fatalf("proj-c: expected 2 mutations after repair, got %d", afterC)
	}
}

// ---------------------------------------------------------------------------
// Phase F — Decay defaults wiring (REQ-006)
// ---------------------------------------------------------------------------

// queryReviewAfter returns the review_after and expires_at for a given observation ID.
// Returns ("", false) for review_after if NULL, ("", false) for expires_at if NULL.
func queryDecayFields(t *testing.T, s *Store, obsID int64) (reviewAfter string, reviewAfterNull bool, expiresAt string, expiresAtNull bool) {
	t.Helper()
	var ra, ea sql.NullString
	if err := s.db.QueryRow(
		`SELECT review_after, expires_at FROM observations WHERE id = ?`, obsID,
	).Scan(&ra, &ea); err != nil {
		t.Fatalf("queryDecayFields: %v", err)
	}
	return ra.String, !ra.Valid, ea.String, !ea.Valid
}

// withinDays asserts that parsed is within ±days of expected.
func withinDays(t *testing.T, label, value string, expected time.Time, days int) {
	t.Helper()
	parsed, err := time.Parse("2006-01-02 15:04:05", value)
	if err != nil {
		// try RFC3339 as fallback
		parsed, err = time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatalf("withinDays: %s: cannot parse %q: %v", label, value, err)
		}
	}
	delta := parsed.Sub(expected)
	if delta < 0 {
		delta = -delta
	}
	maxDelta := time.Duration(days) * 24 * time.Hour
	if delta > maxDelta {
		t.Fatalf("withinDays: %s: got %s, want ~%s (±%dd), delta=%s",
			label, parsed.Format(time.RFC3339), expected.Format(time.RFC3339), days, delta)
	}
}

// TestAddObservation_DecayDefaults verifies that AddObservation populates
// review_after for known types (decision, policy, preference) and leaves it
// NULL for unknown/unlisted types. expires_at is NULL for all types Phase 1.
func TestAddObservation_DecayDefaults(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("decay-sess", "decay-proj", "/tmp/decay"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	now := time.Now().UTC()

	cases := []struct {
		obsType          string
		wantReviewNull   bool
		wantMonthsOffset int
	}{
		{"decision", false, 6},
		{"policy", false, 12},
		{"preference", false, 3},
		{"observation", true, 0},
		{"manual", true, 0},
		{"bugfix", true, 0},
		{"architecture", true, 0},
		{"", true, 0},
	}

	for _, tc := range cases {
		tc := tc
		t.Run("type="+tc.obsType, func(t *testing.T) {
			obsType := tc.obsType
			if obsType == "" {
				obsType = "manual" // AddObservation requires non-empty type; test blank separately
			}
			title := "Decay test — " + tc.obsType + " — " + time.Now().Format(time.RFC3339Nano)
			id, err := s.AddObservation(AddObservationParams{
				SessionID: "decay-sess",
				Type:      obsType,
				Title:     title,
				Content:   "decay defaults test content",
				Project:   "decay-proj",
				Scope:     "project",
			})
			if err != nil {
				t.Fatalf("AddObservation: %v", err)
			}

			ra, raNull, _, eaNull := queryDecayFields(t, s, id)

			// expires_at MUST always be NULL (Phase 1)
			if !eaNull {
				t.Errorf("type=%s: expected expires_at=NULL, got non-NULL", tc.obsType)
			}

			if tc.wantReviewNull {
				if !raNull {
					t.Errorf("type=%s: expected review_after=NULL, got %q", tc.obsType, ra)
				}
				return
			}

			// For types with a decay offset, review_after must be ~N months from now.
			if raNull {
				t.Fatalf("type=%s: expected review_after to be set, got NULL", tc.obsType)
			}
			expected := now.AddDate(0, tc.wantMonthsOffset, 0)
			withinDays(t, "review_after type="+tc.obsType, ra, expected, 2)
		})
	}
}

// TestAddObservation_DecayNotAppliedToExistingRows verifies that topic_key
// revisions and deduplication do NOT overwrite review_after on existing rows.
func TestAddObservation_DecayNotAppliedToExistingRows(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("decay-rev-sess", "decay-rev-proj", "/tmp/decay-rev"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Insert a decision observation via topic_key so revision path is exercised.
	firstID, err := s.AddObservation(AddObservationParams{
		SessionID: "decay-rev-sess",
		Type:      "decision",
		Title:     "Architecture: use SQLite",
		Content:   "We chose SQLite as the primary store.",
		Project:   "decay-rev-proj",
		Scope:     "project",
		TopicKey:  "arch/db-choice",
	})
	if err != nil {
		t.Fatalf("AddObservation first: %v", err)
	}

	ra1, ra1Null, _, _ := queryDecayFields(t, s, firstID)
	if ra1Null {
		t.Fatalf("first insert: expected review_after to be populated for 'decision', got NULL")
	}

	// Revise via same topic_key — this hits the UPDATE path, NOT a new insert.
	secondID, err := s.AddObservation(AddObservationParams{
		SessionID: "decay-rev-sess",
		Type:      "decision",
		Title:     "Architecture: use SQLite (revised)",
		Content:   "Confirmed: SQLite is the right choice.",
		Project:   "decay-rev-proj",
		Scope:     "project",
		TopicKey:  "arch/db-choice",
	})
	if err != nil {
		t.Fatalf("AddObservation revision: %v", err)
	}

	// Topic_key revision should return same row ID.
	if firstID != secondID {
		t.Fatalf("expected topic_key revision to return same ID, got %d vs %d", firstID, secondID)
	}

	ra2, _, _, _ := queryDecayFields(t, s, firstID)

	// review_after MUST NOT have been updated by the revision (original value preserved).
	if ra1 != ra2 {
		t.Errorf("revision must not overwrite review_after: was %q, now %q", ra1, ra2)
	}
}

// ─── C.2 [RED] — ListDeferred / GetDeferred ──────────────────────────────────

// seedDeferredRow is a test helper that inserts a row into sync_apply_deferred.
// Uses a different name from the existing insertDeferredRow in sync_apply_test.go
// which has parameter order (syncID, entity, payload, retryCount, applyStatus).
func seedDeferredRow(t *testing.T, s *Store, syncID, entity, payload string, retryCount int, applyStatus string) {
	t.Helper()
	if _, err := s.db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, apply_status, retry_count, first_seen_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'))
	`, syncID, entity, payload, applyStatus, retryCount); err != nil {
		t.Fatalf("seedDeferredRow %q: %v", syncID, err)
	}
}

// TestListDeferred_HappyPath verifies pagination and status filter.
func TestListDeferred_HappyPath(t *testing.T) {
	s := newTestStore(t)

	validPayload := `{"relation_type":"conflicts_with","source_id":"obs-aaa","target_id":"obs-bbb"}`
	seedDeferredRow(t, s, "def-001", "relation", validPayload, 0, "deferred")
	seedDeferredRow(t, s, "def-002", "relation", validPayload, 1, "deferred")
	seedDeferredRow(t, s, "def-003", "relation", validPayload, 5, "dead")

	// List all.
	all, err := s.ListDeferred(ListDeferredOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListDeferred all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 rows; got %d", len(all))
	}

	// List only deferred status.
	deferred, err := s.ListDeferred(ListDeferredOptions{Status: "deferred", Limit: 50})
	if err != nil {
		t.Fatalf("ListDeferred deferred: %v", err)
	}
	if len(deferred) != 2 {
		t.Errorf("expected 2 deferred rows; got %d", len(deferred))
	}

	// Pagination: limit=1.
	page, err := s.ListDeferred(ListDeferredOptions{Limit: 1})
	if err != nil {
		t.Fatalf("ListDeferred limit=1: %v", err)
	}
	if len(page) != 1 {
		t.Errorf("expected 1 row with limit=1; got %d", len(page))
	}
}

// TestListDeferred_DecodedPayload verifies that DeferredRow.Payload is decoded
// and PayloadValid=true for well-formed JSON.
func TestListDeferred_DecodedPayload(t *testing.T) {
	s := newTestStore(t)

	validPayload := `{"relation_type":"conflicts_with","source_id":"obs-src","target_id":"obs-tgt","extra":42}`
	seedDeferredRow(t, s, "def-valid", "relation", validPayload, 0, "deferred")

	rows, err := s.ListDeferred(ListDeferredOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListDeferred: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row; got %d", len(rows))
	}
	row := rows[0]
	if !row.PayloadValid {
		t.Errorf("expected PayloadValid=true for well-formed JSON; got false. PayloadRaw=%q", row.PayloadRaw)
	}
	if row.Payload == nil {
		t.Fatal("expected decoded Payload map; got nil")
	}
	if row.Payload["relation_type"] != "conflicts_with" {
		t.Errorf("decoded Payload[relation_type]: want conflicts_with; got %v", row.Payload["relation_type"])
	}
}

// TestListDeferred_MalformedPayload verifies that a malformed JSON payload sets
// PayloadValid=false and preserves PayloadRaw.
func TestListDeferred_MalformedPayload(t *testing.T) {
	s := newTestStore(t)

	seedDeferredRow(t, s, "def-bad", "relation", "not valid json", 5, "dead")

	rows, err := s.ListDeferred(ListDeferredOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListDeferred malformed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row; got %d", len(rows))
	}
	row := rows[0]
	if row.PayloadValid {
		t.Errorf("expected PayloadValid=false for malformed JSON; got true")
	}
	if row.PayloadRaw != "not valid json" {
		t.Errorf("expected PayloadRaw preserved; got %q", row.PayloadRaw)
	}
}

// TestGetDeferred_HappyPath verifies GetDeferred returns the correct row.
func TestGetDeferred_HappyPath(t *testing.T) {
	s := newTestStore(t)

	validPayload := `{"relation_type":"related","source_id":"obs-xyz","target_id":"obs-abc"}`
	seedDeferredRow(t, s, "def-xyz", "relation", validPayload, 2, "deferred")

	row, err := s.GetDeferred("def-xyz")
	if err != nil {
		t.Fatalf("GetDeferred: %v", err)
	}
	if row.SyncID != "def-xyz" {
		t.Errorf("expected SyncID=def-xyz; got %q", row.SyncID)
	}
	if row.ApplyStatus != "deferred" {
		t.Errorf("expected ApplyStatus=deferred; got %q", row.ApplyStatus)
	}
	if row.RetryCount != 2 {
		t.Errorf("expected RetryCount=2; got %d", row.RetryCount)
	}
	if !row.PayloadValid {
		t.Errorf("expected PayloadValid=true for valid JSON; got false")
	}
	if row.Payload["relation_type"] != "related" {
		t.Errorf("decoded Payload[relation_type]: want related; got %v", row.Payload["relation_type"])
	}
}

// TestGetDeferred_NotFound verifies GetDeferred returns an error wrapping "not found".
func TestGetDeferred_NotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetDeferred("def-missing")
	if err == nil {
		t.Fatal("expected error for missing sync_id; got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to contain 'not found'; got %q", err.Error())
	}
}
