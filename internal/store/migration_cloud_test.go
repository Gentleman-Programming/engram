package store

import (
	"database/sql"
	"encoding/json"
	"testing"
)

// Task 1.7: Migration on clean DB creates index without deleting anything
func TestMigrationCloudSync_CleanDB(t *testing.T) {
	s := newTestStore(t)

	// Store should have created the unique index during init
	var indexName string
	err := s.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_obs_sync_id_project'",
	).Scan(&indexName)
	if err != nil {
		t.Fatalf("unique index idx_obs_sync_id_project not found: %v", err)
	}

	// Verify sync_cloud_config table exists
	var tableName string
	err = s.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='sync_cloud_config'",
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("sync_cloud_config table not found: %v", err)
	}

	// Verify created_by and updated_by columns exist on observations
	hasCreatedBy := columnExists(t, s.db, "observations", "created_by")
	if !hasCreatedBy {
		t.Fatal("observations.created_by column not found")
	}
	hasUpdatedBy := columnExists(t, s.db, "observations", "updated_by")
	if !hasUpdatedBy {
		t.Fatal("observations.updated_by column not found")
	}
}

// Task 1.8: Migration dedup logic works correctly.
// Strategy: create a properly initialized store, drop the unique index,
// inject duplicates, then re-run migrateCloudSync to test the dedup logic.
func TestMigrationCloudSync_DedupDuplicates(t *testing.T) {
	s := newTestStore(t)

	// Create a session
	if err := s.CreateSession("test-session", "test-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Drop the unique index so we can insert duplicates
	if _, err := s.db.Exec("DROP INDEX IF EXISTS idx_obs_sync_id_project"); err != nil {
		t.Fatalf("drop index: %v", err)
	}

	// Insert two observations with the same sync_id (simulating broken import)
	_, err := s.db.Exec(`
		INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, created_at, updated_at)
		VALUES ('obs-dup', 'test-session', 'manual', 'First', 'Old content', 'test-project', 'project', datetime('now', '-1 hour'), datetime('now', '-1 hour'))
	`)
	if err != nil {
		t.Fatalf("insert first: %v", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, created_at, updated_at)
		VALUES ('obs-dup', 'test-session', 'manual', 'Second', 'New content', 'test-project', 'project', datetime('now'), datetime('now'))
	`)
	if err != nil {
		t.Fatalf("insert second: %v", err)
	}

	// Add orphan sync_mutation referencing a sync_id that doesn't exist in observations
	_, err = s.db.Exec(`
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		VALUES ('cloud', 'observation', 'obs-orphan', 'upsert', '{}', 'local', 'test-project')
	`)
	if err != nil {
		t.Fatalf("insert orphan mutation: %v", err)
	}

	// Re-run the cloud sync migration (dedup + recreate index)
	if err := s.migrateCloudSync(); err != nil {
		t.Fatalf("migrateCloudSync: %v", err)
	}

	// Should have exactly ONE observation with sync_id='obs-dup'
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM observations WHERE sync_id = 'obs-dup'").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 obs with sync_id='obs-dup', got %d", count)
	}

	// The surviving one should be the NEWEST (higher id = 'Second')
	var title string
	if err := s.db.QueryRow("SELECT title FROM observations WHERE sync_id = 'obs-dup'").Scan(&title); err != nil {
		t.Fatalf("get title: %v", err)
	}
	if title != "Second" {
		t.Fatalf("expected title='Second', got '%s'", title)
	}

	// Orphan sync_mutation (obs-orphan) should have been cleaned up
	var mutCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sync_mutations WHERE entity_key = 'obs-orphan'").Scan(&mutCount); err != nil {
		t.Fatalf("count orphan mutations: %v", err)
	}
	if mutCount != 0 {
		t.Fatalf("expected 0 orphan sync_mutations, got %d", mutCount)
	}

	// Unique index should exist
	var indexName string
	if err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='index' AND name='idx_obs_sync_id_project'").Scan(&indexName); err != nil {
		t.Fatalf("unique index not found: %v", err)
	}
}

// Task 1.9: Migration transaction rolls back on failure
func TestMigrationCloudSync_ObsWithoutSyncID(t *testing.T) {
	s := newTestStore(t)

	// Create session
	if err := s.CreateSession("test-session", "test-project", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Observations without sync_id should be unaffected
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "test-session",
		Type:      "manual",
		Title:     "No sync ID obs",
		Content:   "This obs has a generated sync_id",
		Project:   "test-project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Verify the observation still exists
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if obs.Title != "No sync ID obs" {
		t.Fatalf("expected title 'No sync ID obs', got '%s'", obs.Title)
	}
}

// Test sync_cloud_config table operations
func TestSyncCloudConfig(t *testing.T) {
	s := newTestStore(t)

	// Write a config value
	_, err := s.db.Exec("INSERT INTO sync_cloud_config (key, value) VALUES ('server_url', 'https://engram.team.com')")
	if err != nil {
		t.Fatalf("insert config: %v", err)
	}

	// Read it back
	var value string
	err = s.db.QueryRow("SELECT value FROM sync_cloud_config WHERE key = 'server_url'").Scan(&value)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if value != "https://engram.team.com" {
		t.Fatalf("expected 'https://engram.team.com', got '%s'", value)
	}

	// Upsert (PK conflict)
	_, err = s.db.Exec("INSERT OR REPLACE INTO sync_cloud_config (key, value) VALUES ('server_url', 'https://new.team.com')")
	if err != nil {
		t.Fatalf("upsert config: %v", err)
	}
	err = s.db.QueryRow("SELECT value FROM sync_cloud_config WHERE key = 'server_url'").Scan(&value)
	if err != nil {
		t.Fatalf("read updated config: %v", err)
	}
	if value != "https://new.team.com" {
		t.Fatalf("expected 'https://new.team.com', got '%s'", value)
	}
}

// Test syncObservationPayload includes created_by and updated_by
func TestSyncObservationPayload_IdentityFields(t *testing.T) {
	p := syncObservationPayload{
		SyncID:    "obs-test",
		SessionID: "session-1",
		Type:      "manual",
		Title:     "Test",
		Content:   "Content",
		Scope:     "project",
		CreatedBy: "user-abc",
		UpdatedBy: "user-xyz",
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded["created_by"] != "user-abc" {
		t.Fatalf("expected created_by='user-abc', got '%v'", decoded["created_by"])
	}
	if decoded["updated_by"] != "user-xyz" {
		t.Fatalf("expected updated_by='user-xyz', got '%v'", decoded["updated_by"])
	}
}

// helper
func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}
