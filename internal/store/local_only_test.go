package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// countPendingObsMutations returns the total number of unacked sync_mutations rows
// for entity='observation' without enrollment filtering — used by local_only tests
// to assert at the raw-DB level.
func countPendingObsMutations(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sync_mutations WHERE entity = ? AND acked_at IS NULL`,
		SyncEntityObservation,
	).Scan(&n); err != nil {
		t.Fatalf("countPendingObsMutations: %v", err)
	}
	return n
}

// countPendingObsMutationsByOp returns unacked sync_mutations rows for
// entity='observation' filtered by op.
func countPendingObsMutationsByOp(t *testing.T, s *Store, op string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sync_mutations WHERE entity = ? AND op = ? AND acked_at IS NULL`,
		SyncEntityObservation, op,
	).Scan(&n); err != nil {
		t.Fatalf("countPendingObsMutationsByOp(%s): %v", op, err)
	}
	return n
}

// setupLocalOnlyStore returns a fresh store with a session.
func setupLocalOnlyStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := s.CreateSession("ses-local", "testproj", "/tmp/local"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return s
}

// ─── (a) local_only save creates NO pending sync mutation ─────────────────────

func TestAddObservation_LocalOnly_NoSyncMutation(t *testing.T) {
	s := setupLocalOnlyStore(t)

	before := countPendingObsMutations(t, s)

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-local",
		Type:      "decision",
		Title:     "Private decision",
		Content:   "This must never reach the cloud",
		Project:   "testproj",
		Scope:     "project",
		LocalOnly: true,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	after := countPendingObsMutations(t, s)
	if after != before {
		t.Errorf("expected no new sync mutations for local_only save, got %d new mutation(s)", after-before)
	}
}

// LocalOnly observation must be persisted locally.
func TestAddObservation_LocalOnly_PersistedLocally(t *testing.T) {
	s := setupLocalOnlyStore(t)

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-local",
		Type:      "decision",
		Title:     "Private decision",
		Content:   "This must never reach the cloud",
		Project:   "testproj",
		Scope:     "project",
		LocalOnly: true,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if !obs.LocalOnly {
		t.Error("expected LocalOnly=true on persisted observation")
	}
	if obs.Title != "Private decision" {
		t.Errorf("unexpected title: %q", obs.Title)
	}
}

// ─── (b) default save still enqueues upsert ───────────────────────────────────

func TestAddObservation_Default_EnqueuesUpsert(t *testing.T) {
	s := setupLocalOnlyStore(t)

	before := countPendingObsMutationsByOp(t, s, SyncOpUpsert)

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-local",
		Type:      "decision",
		Title:     "Synced decision",
		Content:   "This reaches the cloud",
		Project:   "testproj",
		Scope:     "project",
		LocalOnly: false, // explicit false — same as omitting
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	after := countPendingObsMutationsByOp(t, s, SyncOpUpsert)
	if after <= before {
		t.Errorf("expected at least one new upsert mutation for default save, before=%d after=%d", before, after)
	}
}

// ─── (c) flip false→true on a synced memory enqueues a delete tombstone ───────

func TestAddObservation_FlipFalseToTrue_EnqueuesDeleteTombstone(t *testing.T) {
	s := setupLocalOnlyStore(t)

	// 1. Save normally (local_only=false) so the observation gets an upsert mutation.
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-local",
		Type:      "decision",
		Title:     "Flip me to local",
		Content:   "First save — synced",
		Project:   "testproj",
		Scope:     "project",
		TopicKey:  "test/flip-to-local",
		LocalOnly: false,
	})
	if err != nil {
		t.Fatalf("first AddObservation: %v", err)
	}

	beforeUpserts := countPendingObsMutationsByOp(t, s, SyncOpUpsert)
	beforeDeletes := countPendingObsMutationsByOp(t, s, SyncOpDelete)

	// 2. Save again with local_only=true — same topic_key triggers the update path.
	_, err = s.AddObservation(AddObservationParams{
		SessionID: "ses-local",
		Type:      "decision",
		Title:     "Flip me to local",
		Content:   "Second save — now local only",
		Project:   "testproj",
		Scope:     "project",
		TopicKey:  "test/flip-to-local",
		LocalOnly: true,
	})
	if err != nil {
		t.Fatalf("second AddObservation (flip): %v", err)
	}

	afterUpserts := countPendingObsMutationsByOp(t, s, SyncOpUpsert)
	afterDeletes := countPendingObsMutationsByOp(t, s, SyncOpDelete)

	// No new upsert mutations.
	if afterUpserts != beforeUpserts {
		t.Errorf("expected no new upsert mutations on flip, before=%d after=%d", beforeUpserts, afterUpserts)
	}
	// Exactly one new delete mutation (tombstone).
	if afterDeletes != beforeDeletes+1 {
		t.Errorf("expected exactly one new delete mutation on flip, before=%d after=%d", beforeDeletes, afterDeletes)
	}

	// 3. The delete mutation's entity_key must be the observation's sync_id.
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation after flip: %v", err)
	}
	if !obs.LocalOnly {
		t.Error("expected LocalOnly=true after flip")
	}

	var deleteSyncID string
	if err := s.db.QueryRow(
		`SELECT entity_key FROM sync_mutations WHERE entity = ? AND op = ? AND acked_at IS NULL ORDER BY seq DESC LIMIT 1`,
		SyncEntityObservation, SyncOpDelete,
	).Scan(&deleteSyncID); err != nil {
		t.Fatalf("fetch delete mutation entity_key: %v", err)
	}
	if deleteSyncID != obs.SyncID {
		t.Errorf("delete tombstone entity_key=%q, want obs sync_id=%q", deleteSyncID, obs.SyncID)
	}
}

// Flip test using the dedupe path (no topic_key, same content within window).
func TestAddObservation_FlipFalseToTrue_DedupePathEnqueuesTombstone(t *testing.T) {
	s := setupLocalOnlyStore(t)

	params := AddObservationParams{
		SessionID: "ses-local",
		Type:      "bugfix",
		Title:     "Dedupe flip test",
		Content:   "Exact content that deduplicates",
		Project:   "testproj",
		Scope:     "project",
		LocalOnly: false,
	}

	id, err := s.AddObservation(params)
	if err != nil {
		t.Fatalf("first AddObservation: %v", err)
	}

	beforeDeletes := countPendingObsMutationsByOp(t, s, SyncOpDelete)
	beforeUpserts := countPendingObsMutationsByOp(t, s, SyncOpUpsert)

	// Same content → dedupe path. Flip to local_only.
	params.LocalOnly = true
	if _, err := s.AddObservation(params); err != nil {
		t.Fatalf("second AddObservation (flip via dedupe): %v", err)
	}

	afterDeletes := countPendingObsMutationsByOp(t, s, SyncOpDelete)
	afterUpserts := countPendingObsMutationsByOp(t, s, SyncOpUpsert)

	if afterDeletes != beforeDeletes+1 {
		t.Errorf("expected one delete tombstone via dedupe flip, before=%d after=%d", beforeDeletes, afterDeletes)
	}
	if afterUpserts != beforeUpserts {
		t.Errorf("expected no new upsert on dedupe flip, before=%d after=%d", beforeUpserts, afterUpserts)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if !obs.LocalOnly {
		t.Error("expected LocalOnly=true after dedupe flip")
	}
}

// ─── (d) delete tombstone payload is valid JSON with Deleted=true ─────────────

func TestAddObservation_FlipFalseToTrue_TombstonePayloadValid(t *testing.T) {
	s := setupLocalOnlyStore(t)

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-local",
		Type:      "decision",
		Title:     "Tombstone payload test",
		Content:   "Check payload",
		Project:   "testproj",
		Scope:     "project",
		TopicKey:  "test/tombstone-payload",
		LocalOnly: false,
	})
	if err != nil {
		t.Fatalf("first AddObservation: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-local",
		Type:      "decision",
		Title:     "Tombstone payload test",
		Content:   "Check payload - updated",
		Project:   "testproj",
		Scope:     "project",
		TopicKey:  "test/tombstone-payload",
		LocalOnly: true,
	}); err != nil {
		t.Fatalf("flip AddObservation: %v", err)
	}

	var payload string
	if err := s.db.QueryRow(
		`SELECT payload FROM sync_mutations WHERE entity = ? AND op = ? AND acked_at IS NULL ORDER BY seq DESC LIMIT 1`,
		SyncEntityObservation, SyncOpDelete,
	).Scan(&payload); err != nil {
		t.Fatalf("fetch tombstone payload: %v", err)
	}

	var p syncObservationPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("unmarshal tombstone payload: %v", err)
	}
	if !p.Deleted {
		t.Error("expected tombstone payload.Deleted=true")
	}
	if p.SyncID == "" {
		t.Error("expected tombstone payload.SyncID to be set")
	}
}

// ─── (e) Migration: legacy DB gets local_only column with default 0 ───────────

func TestMigration_LocalOnlyColumnAddedToLegacyDB(t *testing.T) {
	// Build a legacy store (pre-local_only schema), then open via New() to run migrate().
	fixtureRows := migrationFixtureRows()
	s := newTestStoreWithLegacySchema(t, fixtureRows)

	// All migrated observations must have local_only=false (default).
	rows, err := s.db.Query(
		`SELECT id, ifnull(local_only, 0) FROM observations WHERE deleted_at IS NULL`,
	)
	if err != nil {
		t.Fatalf("query local_only: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id int64
		var localOnly bool
		if err := rows.Scan(&id, &localOnly); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if localOnly {
			t.Errorf("observation id=%d has local_only=true after migration (expected false)", id)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if count != len(fixtureRows) {
		t.Errorf("expected %d migrated observations, got %d", len(fixtureRows), count)
	}
}

// After migration, observations read via GetObservation have LocalOnly=false.
func TestMigration_LocalOnly_ObservationsReadableViaStore(t *testing.T) {
	s := newTestStoreWithLegacySchema(t, migrationFixtureRows())

	// Use AllObservations to get observations (no deletion filter needed here).
	obs, err := s.AllObservations("engram", "project", 100)
	if err != nil {
		t.Fatalf("AllObservations: %v", err)
	}
	if len(obs) == 0 {
		t.Fatal("expected at least one observation after migration")
	}
	for _, o := range obs {
		if o.LocalOnly {
			t.Errorf("observation %q has LocalOnly=true after migration", o.SyncID)
		}
	}
}

// ─── (f) Migration on DB that already has local_only column is a no-op ────────

func TestMigration_LocalOnly_IdempotentOnFreshDB(t *testing.T) {
	// A fresh store already has the local_only column via CREATE TABLE.
	// migrate() calling addColumnIfNotExists must be a no-op (not an error).
	s := newTestStore(t)

	if err := s.CreateSession("ses-idem", "idem-proj", "/tmp/idem"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "ses-idem",
		Type:      "decision",
		Title:     "Idempotent migration test",
		Content:   "Should work fine",
		Project:   "idem-proj",
		Scope:     "project",
		LocalOnly: true,
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if !obs.LocalOnly {
		t.Error("expected LocalOnly=true after fresh-DB insert")
	}
}

// ─── Legacy DDL without local_only column (used for migration tests) ──────────

// legacyDDLWithoutLocalOnly is a snapshot of the observations table DDL
// before the local_only column was added. It is the base for testing that
// addColumnIfNotExists runs cleanly on upgrade.
//
// This is NOT the full DB DDL — just the minimal schema needed to seed
// observations for the migration test. New() / migrate() adds the rest.
const legacyDDLWithoutLocalOnly = `
	CREATE TABLE IF NOT EXISTS sessions (
		id         TEXT PRIMARY KEY,
		project    TEXT NOT NULL DEFAULT '',
		directory  TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL DEFAULT (datetime('now')),
		ended_at   TEXT,
		summary    TEXT
	);

	CREATE TABLE IF NOT EXISTS observations (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		sync_id         TEXT,
		session_id      TEXT    NOT NULL,
		type            TEXT    NOT NULL,
		title           TEXT    NOT NULL,
		content         TEXT    NOT NULL,
		tool_name       TEXT,
		project         TEXT,
		scope           TEXT    NOT NULL DEFAULT 'project',
		topic_key       TEXT,
		normalized_hash TEXT,
		revision_count  INTEGER NOT NULL DEFAULT 1,
		duplicate_count INTEGER NOT NULL DEFAULT 1,
		last_seen_at    TEXT,
		pinned          BOOLEAN NOT NULL DEFAULT 0,
		created_at      TEXT    NOT NULL DEFAULT (datetime('now')),
		updated_at      TEXT    NOT NULL DEFAULT (datetime('now')),
		deleted_at      TEXT,
		FOREIGN KEY (session_id) REFERENCES sessions(id)
	);
`

// TestMigration_LocalOnly_ColumnAddedFromPreLocalOnlySchema opens a DB without
// the local_only column, inserts rows, then calls New() to trigger migrate() and
// confirms the column is present and all existing rows default to 0.
func TestMigration_LocalOnly_ColumnAddedFromPreLocalOnlySchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")

	// 1. Open raw DB with the legacy schema (no local_only column).
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec("PRAGMA journal_mode = WAL"); err != nil {
		raw.Close()
		t.Fatalf("WAL pragma: %v", err)
	}
	if _, err := raw.Exec("PRAGMA foreign_keys = ON"); err != nil {
		raw.Close()
		t.Fatalf("foreign_keys pragma: %v", err)
	}
	if _, err := raw.Exec(legacyDDLWithoutLocalOnly); err != nil {
		raw.Close()
		t.Fatalf("apply legacy DDL: %v", err)
	}

	// 2. Insert one row that should receive local_only=0 after migration.
	if _, err := raw.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES ('ses-pre-local', 'pre-local', '/tmp')`,
	); err != nil {
		raw.Close()
		t.Fatalf("insert session: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO observations (sync_id, session_id, type, title, content, scope)
		 VALUES ('obs-pre-local-001', 'ses-pre-local', 'decision', 'Pre-local title', 'Pre-local content', 'project')`,
	); err != nil {
		raw.Close()
		t.Fatalf("insert observation: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	// 3. Open via New() — this runs migrate().
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New(cfg) after legacy schema: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// 4. Confirm local_only column is present and the row defaults to 0.
	var localOnly int
	if err := s.db.QueryRow(
		`SELECT ifnull(local_only, 0) FROM observations WHERE sync_id = 'obs-pre-local-001'`,
	).Scan(&localOnly); err != nil {
		t.Fatalf("select local_only: %v", err)
	}
	if localOnly != 0 {
		t.Errorf("expected local_only=0 after migration, got %d", localOnly)
	}
}
