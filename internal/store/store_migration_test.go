package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// legacyObsRow holds the columns that exist in the pre-conflict-surfacing schema.
// Only the columns that existed in the legacy DDL are captured here.
type legacyObsRow struct {
	syncID    string
	sessionID string
	obsType   string
	title     string
	content   string
	project   string
	scope     string
}

// newTestStoreWithLegacySchema creates a temporary SQLite database using the
// pre-memory-conflict-surfacing DDL (v_N), inserts the given fixture rows via
// raw SQL, then calls New(cfg) so that migrate() runs against the legacy
// database.  Returns a *Store ready for assertion.
//
// The raw SQLite DB is closed before New() is called so that the WAL file
// is fully flushed and the connection can be re-opened by New().
func newTestStoreWithLegacySchema(t *testing.T, fixtureRows []legacyObsRow) *Store {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")

	// 1. Open raw DB and apply legacy DDL.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("newTestStoreWithLegacySchema: open raw db: %v", err)
	}

	if _, err := raw.Exec("PRAGMA journal_mode = WAL"); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchema: WAL pragma: %v", err)
	}
	if _, err := raw.Exec("PRAGMA foreign_keys = ON"); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchema: foreign_keys pragma: %v", err)
	}

	if _, err := raw.Exec(legacyDDLPreMemoryConflictSurfacing); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchema: apply legacy DDL: %v", err)
	}

	// 2. Insert fixture rows.  We need a session row first because of the FK.
	if len(fixtureRows) > 0 {
		if _, err := raw.Exec(
			`INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('ses-migration-test', 'engram', '/tmp')`,
		); err != nil {
			raw.Close()
			t.Fatalf("newTestStoreWithLegacySchema: insert session: %v", err)
		}
		for _, row := range fixtureRows {
			if _, err := raw.Exec(
				`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope)
				 VALUES (?, 'ses-migration-test', ?, ?, ?, ?, ?)`,
				row.syncID, row.obsType, row.title, row.content, row.project, row.scope,
			); err != nil {
				raw.Close()
				t.Fatalf("newTestStoreWithLegacySchema: insert fixture row %+v: %v", row, err)
			}
		}
	}

	// 3. Close raw DB so the file is released before New() re-opens it.
	if err := raw.Close(); err != nil {
		t.Fatalf("newTestStoreWithLegacySchema: close raw db: %v", err)
	}

	// 4. Open via New() — this calls migrate() against the legacy DB.
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("newTestStoreWithLegacySchema: New(cfg): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

// fixtureRows returns a stable set of 5 legacy observation rows used across
// the migration tests.
func migrationFixtureRows() []legacyObsRow {
	return []legacyObsRow{
		{syncID: "obs-legacy-001", obsType: "decision", title: "Use sessions for auth", content: "We chose session-based auth", project: "engram", scope: "project"},
		{syncID: "obs-legacy-002", obsType: "bugfix", title: "Fixed tokenizer", content: "Normalized tokenizer panic on edge case", project: "engram", scope: "project"},
		{syncID: "obs-legacy-003", obsType: "architecture", title: "Hexagonal arch agreed", content: "We follow hexagonal architecture", project: "engram", scope: "project"},
		{syncID: "obs-legacy-004", obsType: "pattern", title: "Container-presentational pattern", content: "Use container components for state", project: "engram", scope: "project"},
		{syncID: "obs-legacy-005", obsType: "policy", title: "No secrets in commits", content: "Never commit .env or credentials", project: "engram", scope: "project"},
	}
}

// legacyRelationRow holds the columns needed to seed memory_relations in the
// post-Phase-1 schema. Used by Phase 2 migration tests.
type legacyRelationRow struct {
	syncID         string
	sourceID       string
	targetID       string
	relation       string
	judgmentStatus string
	project        string
}

// newTestStoreWithLegacySchemaPostP1 creates a temporary SQLite database using
// the post-memory-conflict-surfacing (Phase 1) DDL, inserts observation and
// relation fixture rows via raw SQL, then calls New(cfg) so that migrate()
// runs against that database.
//
// This is the Phase 2 equivalent of newTestStoreWithLegacySchema: it seeds
// the v_(N+1) baseline so that Phase 2 migration tests can assert that
// sync_apply_deferred is added by migrate().
func newTestStoreWithLegacySchemaPostP1(t *testing.T, obsRows []legacyObsRow, relRows []legacyRelationRow) *Store {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")

	// 1. Open raw DB and apply post-Phase-1 DDL.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("newTestStoreWithLegacySchemaPostP1: open raw db: %v", err)
	}

	if _, err := raw.Exec("PRAGMA journal_mode = WAL"); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchemaPostP1: WAL pragma: %v", err)
	}
	if _, err := raw.Exec("PRAGMA foreign_keys = ON"); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchemaPostP1: foreign_keys pragma: %v", err)
	}

	if _, err := raw.Exec(legacyDDLPostMemoryConflictSurfacing); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchemaPostP1: apply legacy DDL: %v", err)
	}

	// 2. Insert observation fixture rows. Requires a session due to the FK.
	if len(obsRows) > 0 {
		if _, err := raw.Exec(
			`INSERT OR IGNORE INTO sessions (id, project, directory) VALUES ('ses-p1-migration-test', 'engram', '/tmp')`,
		); err != nil {
			raw.Close()
			t.Fatalf("newTestStoreWithLegacySchemaPostP1: insert session: %v", err)
		}
		for _, row := range obsRows {
			if _, err := raw.Exec(
				`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope)
				 VALUES (?, 'ses-p1-migration-test', ?, ?, ?, ?, ?)`,
				row.syncID, row.obsType, row.title, row.content, row.project, row.scope,
			); err != nil {
				raw.Close()
				t.Fatalf("newTestStoreWithLegacySchemaPostP1: insert obs fixture %+v: %v", row, err)
			}
		}
	}

	// 3. Insert relation fixture rows.
	for _, row := range relRows {
		if _, err := raw.Exec(
			`INSERT INTO memory_relations (sync_id, source_id, target_id, relation, judgment_status)
			 VALUES (?, ?, ?, ?, ?)`,
			row.syncID, row.sourceID, row.targetID, row.relation, row.judgmentStatus,
		); err != nil {
			raw.Close()
			t.Fatalf("newTestStoreWithLegacySchemaPostP1: insert relation fixture %+v: %v", row, err)
		}
	}

	// 4. Close raw DB so the file is released before New() re-opens it.
	if err := raw.Close(); err != nil {
		t.Fatalf("newTestStoreWithLegacySchemaPostP1: close raw db: %v", err)
	}

	// 5. Open via New() — this calls migrate() against the post-Phase-1 DB.
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("newTestStoreWithLegacySchemaPostP1: New(cfg): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

// migrationFixtureRowsPostP1 returns observation rows for Phase 2 migration tests.
// Source and target IDs correspond to the obs sync_ids inserted in these rows.
func migrationFixtureRowsPostP1() ([]legacyObsRow, []legacyRelationRow) {
	obs := []legacyObsRow{
		{syncID: "obs-p1-001", obsType: "decision", title: "Use Redis for caching", content: "Decided to use Redis", project: "engram", scope: "project"},
		{syncID: "obs-p1-002", obsType: "decision", title: "Use Memcached for caching", content: "Alternative caching approach", project: "engram", scope: "project"},
		{syncID: "obs-p1-003", obsType: "architecture", title: "Hexagonal architecture", content: "Ports and adapters pattern", project: "engram", scope: "project"},
	}
	rels := []legacyRelationRow{
		{syncID: "rel-p1-001", sourceID: "obs-p1-001", targetID: "obs-p1-002", relation: "conflicts_with", judgmentStatus: "judged", project: "engram"},
		{syncID: "rel-p1-002", sourceID: "obs-p1-003", targetID: "obs-p1-001", relation: "related", judgmentStatus: "pending", project: "engram"},
	}
	return obs, rels
}

// ─── A.3: TestMigrate_PreMemoryConflictSurfacing_PreservesData ─────────────────
//
// RED test: verifies that after migrate() runs on a legacy DB:
//   - all 5 fixture rows are intact (id, sync_id, content unchanged)
//   - new columns (review_after, expires_at) are NULL for pre-existing rows
//   - memory_relations table exists
//
// This test FAILS (red) until Phase B adds the new columns and table to migrate().

func TestMigrate_PreMemoryConflictSurfacing_PreservesData(t *testing.T) {
	fixtures := migrationFixtureRows()
	s := newTestStoreWithLegacySchema(t, fixtures)

	// Assert all 5 rows are still present with their original sync_ids and content.
	rows, err := s.db.Query(
		`SELECT sync_id, content FROM observations WHERE session_id = 'ses-migration-test' ORDER BY id`,
	)
	if err != nil {
		t.Fatalf("query fixture rows: %v", err)
	}
	defer rows.Close()

	var got []struct{ syncID, content string }
	for rows.Next() {
		var r struct{ syncID, content string }
		if err := rows.Scan(&r.syncID, &r.content); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	if len(got) != len(fixtures) {
		t.Fatalf("expected %d rows after migration, got %d", len(fixtures), len(got))
	}
	for i, fix := range fixtures {
		if got[i].syncID != fix.syncID {
			t.Errorf("row %d: sync_id = %q, want %q", i, got[i].syncID, fix.syncID)
		}
		if got[i].content != fix.content {
			t.Errorf("row %d: content = %q, want %q", i, got[i].content, fix.content)
		}
	}

	// Assert new columns exist and are NULL for pre-existing rows.
	// This will fail (red) until Phase B adds the columns to migrate().
	nullRows, err := s.db.Query(
		`SELECT sync_id, review_after, expires_at FROM observations WHERE session_id = 'ses-migration-test'`,
	)
	if err != nil {
		t.Fatalf("query new columns: %v — columns not yet added by migrate() (expected red)", err)
	}
	defer nullRows.Close()

	for nullRows.Next() {
		var syncID string
		var reviewAfter, expiresAt *string
		if err := nullRows.Scan(&syncID, &reviewAfter, &expiresAt); err != nil {
			t.Fatalf("scan new columns: %v", err)
		}
		if reviewAfter != nil {
			t.Errorf("row %s: review_after = %q, want NULL for pre-existing row", syncID, *reviewAfter)
		}
		if expiresAt != nil {
			t.Errorf("row %s: expires_at = %q, want NULL for pre-existing row", syncID, *expiresAt)
		}
	}
	if err := nullRows.Err(); err != nil {
		t.Fatalf("nullRows.Err: %v", err)
	}

	// Assert memory_relations table exists.
	// This will fail (red) until Phase B adds the table to migrate().
	var tableName string
	err = s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='memory_relations'`,
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("memory_relations table not found: %v — table not yet created by migrate() (expected red)", err)
	}
	if tableName != "memory_relations" {
		t.Errorf("memory_relations table name = %q, want 'memory_relations'", tableName)
	}
}

// ─── A.4: TestMigrate_Idempotent ────────────────────────────────────────────
//
// RED test: calls migrate() twice on the same DB (indirectly via New()) and
// asserts the schema is identical after both runs.  Also asserts that the
// new columns and memory_relations table exist after both runs.
//
// This test FAILS (red) until Phase B adds the new DDL to migrate().

func TestMigrate_Idempotent(t *testing.T) {
	// First run is done inside newTestStoreWithLegacySchema via New().
	fixtures := migrationFixtureRows()
	s := newTestStoreWithLegacySchema(t, fixtures)
	dir := s.cfg.DataDir

	// Close the store so we can re-open it.
	if err := s.Close(); err != nil {
		t.Fatalf("close store before second migration: %v", err)
	}

	// Second run: open the same DB again — migrate() will run a second time.
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir
	s2, err := New(cfg)
	if err != nil {
		t.Fatalf("New() second run failed: %v — migrate() is not idempotent", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	// Assert memory_relations still exists after the second run.
	var tableName string
	err = s2.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='memory_relations'`,
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("memory_relations not found after second migrate: %v (expected red)", err)
	}

	// Assert all 5 fixture rows are still intact after the second migration.
	var count int
	if err := s2.db.QueryRow(
		`SELECT COUNT(*) FROM observations WHERE session_id = 'ses-migration-test'`,
	).Scan(&count); err != nil {
		t.Fatalf("count fixture rows after second migrate: %v", err)
	}
	if count != len(fixtures) {
		t.Errorf("after second migrate: expected %d rows, got %d", len(fixtures), count)
	}

	// Assert new columns still present and queryable.
	_, err = s2.db.Query(
		`SELECT review_after, expires_at FROM observations LIMIT 1`,
	)
	if err != nil {
		t.Fatalf("new columns missing after second migrate: %v (expected red)", err)
	}
}

// ─── A.5: TestMigrate_DoesNotTouchFTS5OrSyncMutations ───────────────────────
//
// RED test: asserts that after migrate() runs on a legacy DB:
//   - obs_fts (observations_fts) virtual table still exists and is queryable
//   - sync_mutations table still exists and any pre-existing rows are intact
//   - memory_relations table is present (new, expected)
//
// The test is primarily a regression guard: migrate() must not accidentally
// drop or corrupt the FTS5 virtual table or the sync_mutations table.
//
// This test FAILS (red) on the memory_relations assertion until Phase B.

func TestMigrate_DoesNotTouchFTS5OrSyncMutations(t *testing.T) {
	fixtures := migrationFixtureRows()
	s := newTestStoreWithLegacySchema(t, fixtures)

	// 1. observations_fts virtual table must still exist and return results.
	ftsRows, err := s.db.Query(
		`SELECT rowid FROM observations_fts WHERE observations_fts MATCH 'sessions'`,
	)
	if err != nil {
		t.Fatalf("observations_fts query failed after migrate: %v", err)
	}
	ftsRows.Close()

	// 2. observations_fts must be searchable with a fixture title term.
	var ftsCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM observations_fts WHERE observations_fts MATCH '"tokenizer"'`,
	).Scan(&ftsCount); err != nil {
		t.Fatalf("FTS5 match query: %v", err)
	}
	if ftsCount < 1 {
		t.Errorf("observations_fts: expected at least 1 match for 'tokenizer', got %d", ftsCount)
	}

	// 3. sync_mutations table must still exist (schema check via sqlite_master).
	var syncMutName string
	if err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='sync_mutations'`,
	).Scan(&syncMutName); err != nil {
		t.Fatalf("sync_mutations table missing after migrate: %v", err)
	}
	if syncMutName != "sync_mutations" {
		t.Errorf("sync_mutations table name = %q, want 'sync_mutations'", syncMutName)
	}

	// 4. sync_mutations schema must have the expected columns.
	smRows, err := s.db.Query(`PRAGMA table_info(sync_mutations)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(sync_mutations): %v", err)
	}
	defer smRows.Close()

	var smCols []string
	for smRows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultVal any
		var pk int
		if err := smRows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan sync_mutations column: %v", err)
		}
		smCols = append(smCols, name)
	}
	if err := smRows.Err(); err != nil {
		t.Fatalf("smRows.Err: %v", err)
	}

	requiredSMCols := []string{"seq", "target_key", "entity", "entity_key", "op", "payload", "source", "project", "occurred_at", "acked_at"}
	colSet := make(map[string]bool, len(smCols))
	for _, c := range smCols {
		colSet[c] = true
	}
	for _, req := range requiredSMCols {
		if !colSet[req] {
			t.Errorf("sync_mutations missing expected column %q after migrate", req)
		}
	}

	// 5. memory_relations table MUST exist after migrate() (red until Phase B).
	var mrName string
	if err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='memory_relations'`,
	).Scan(&mrName); err != nil {
		t.Fatalf("memory_relations not found after migrate: %v (expected red until Phase B)", err)
	}

	// 6. memory_relations must NOT be in sync_mutations (REQ-009).
	// This is a forward-looking assertion; passes trivially until Phase C adds SaveRelation.
	var relInSync int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE entity = 'memory_relation'`,
	).Scan(&relInSync); err != nil {
		t.Fatalf("query sync_mutations for memory_relation entity: %v", err)
	}
	if relInSync != 0 {
		t.Errorf("sync_mutations contains %d memory_relation rows, want 0 (REQ-009)", relInSync)
	}

	// 7. memory_relations columns must match the design DDL.
	// This assertion is also red until Phase B adds the table.
	mrColRows, err := s.db.Query(`PRAGMA table_info(memory_relations)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(memory_relations): %v", err)
	}
	defer mrColRows.Close()

	var mrCols []string
	for mrColRows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultVal any
		var pk int
		if err := mrColRows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan memory_relations column: %v", err)
		}
		mrCols = append(mrCols, name)
	}
	if err := mrColRows.Err(); err != nil {
		t.Fatalf("mrColRows.Err: %v", err)
	}

	requiredMRCols := []string{
		"id", "sync_id", "source_id", "target_id", "relation",
		"reason", "evidence", "confidence", "judgment_status",
		"marked_by_actor", "marked_by_kind", "marked_by_model",
		"session_id", "superseded_at", "superseded_by_relation_id",
		"created_at", "updated_at",
	}
	mrColSet := make(map[string]bool, len(mrCols))
	for _, c := range mrCols {
		mrColSet[c] = true
	}
	var missing []string
	for _, req := range requiredMRCols {
		if !mrColSet[req] {
			missing = append(missing, req)
		}
	}
	if len(missing) > 0 {
		t.Errorf("memory_relations missing columns: %s (expected red until Phase B)", strings.Join(missing, ", "))
	}
}

// ─── Phase 2 / A.3: RED tests for sync_apply_deferred migration ───────────────
//
// These tests seed a post-Phase-1 database (observations + memory_relations,
// no sync_apply_deferred) and assert that after migrate() the table exists
// with the correct schema. They MUST FAIL until Phase B adds sync_apply_deferred
// to migrate().

// TestMigrate_PostPhase1_AddsSyncApplyDeferred asserts that running migrate()
// on a v_(N+1) database (post-Phase-1, pre-Phase-2) creates the
// sync_apply_deferred table with all required columns and both indexes.
//
// RED: fails because migrate() does not yet create sync_apply_deferred.
func TestMigrate_PostPhase1_AddsSyncApplyDeferred(t *testing.T) {
	obsRows, relRows := migrationFixtureRowsPostP1()
	s := newTestStoreWithLegacySchemaPostP1(t, obsRows, relRows)

	// 1. Assert sync_apply_deferred table exists.
	var tableName string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='sync_apply_deferred'`,
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("sync_apply_deferred table not found after migrate(): %v — table not yet added by migrate() (expected RED until Phase B)", err)
	}
	if tableName != "sync_apply_deferred" {
		t.Errorf("sync_apply_deferred table name = %q, want 'sync_apply_deferred'", tableName)
	}

	// 2. Assert all required columns exist with correct types.
	colRows, err := s.db.Query(`PRAGMA table_info(sync_apply_deferred)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(sync_apply_deferred): %v", err)
	}
	defer colRows.Close()

	type colInfo struct {
		name    string
		notNull int
	}
	gotCols := make(map[string]colInfo)
	for colRows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultVal any
		var pk int
		if err := colRows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan sync_apply_deferred column: %v", err)
		}
		gotCols[name] = colInfo{name: name, notNull: notNull}
	}
	if err := colRows.Err(); err != nil {
		t.Fatalf("colRows.Err: %v", err)
	}

	// Required columns per design §2.
	requiredCols := []string{
		"sync_id", "entity", "payload", "apply_status",
		"retry_count", "last_error", "last_attempted_at", "first_seen_at",
	}
	var missingCols []string
	for _, req := range requiredCols {
		if _, ok := gotCols[req]; !ok {
			missingCols = append(missingCols, req)
		}
	}
	if len(missingCols) > 0 {
		t.Errorf("sync_apply_deferred missing columns: %s (expected RED until Phase B)", strings.Join(missingCols, ", "))
	}

	// 3. Assert both indexes exist.
	idxRows, err := s.db.Query(
		`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='sync_apply_deferred'`,
	)
	if err != nil {
		t.Fatalf("query indexes for sync_apply_deferred: %v", err)
	}
	defer idxRows.Close()

	gotIndexes := make(map[string]bool)
	for idxRows.Next() {
		var idxName string
		if err := idxRows.Scan(&idxName); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		gotIndexes[idxName] = true
	}
	if err := idxRows.Err(); err != nil {
		t.Fatalf("idxRows.Err: %v", err)
	}

	// The design specifies idx_sad_status_seen on (apply_status, first_seen_at).
	if !gotIndexes["idx_sad_status_seen"] {
		t.Errorf("index idx_sad_status_seen not found on sync_apply_deferred (expected RED until Phase B); got: %v", gotIndexes)
	}
}

// TestMigrate_PostPhase1_PreservesExistingRows asserts that running migrate()
// on a post-Phase-1 database preserves all pre-existing observations and
// memory_relations rows with their original field values.
//
// RED: fails if sync_apply_deferred is absent (table assertion will fail first),
// but the row-preservation checks are independently valuable.
func TestMigrate_PostPhase1_PreservesExistingRows(t *testing.T) {
	obsRows, relRows := migrationFixtureRowsPostP1()
	s := newTestStoreWithLegacySchemaPostP1(t, obsRows, relRows)

	// 1. Assert all observation fixture rows survive with original sync_id and content.
	rows, err := s.db.Query(
		`SELECT sync_id, content FROM observations WHERE session_id = 'ses-p1-migration-test' ORDER BY id`,
	)
	if err != nil {
		t.Fatalf("query observation fixtures: %v", err)
	}
	defer rows.Close()

	var gotObs []struct{ syncID, content string }
	for rows.Next() {
		var r struct{ syncID, content string }
		if err := rows.Scan(&r.syncID, &r.content); err != nil {
			t.Fatalf("scan observation row: %v", err)
		}
		gotObs = append(gotObs, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	if len(gotObs) != len(obsRows) {
		t.Fatalf("expected %d observation rows after migrate(), got %d", len(obsRows), len(gotObs))
	}
	for i, fix := range obsRows {
		if gotObs[i].syncID != fix.syncID {
			t.Errorf("obs row %d: sync_id = %q, want %q", i, gotObs[i].syncID, fix.syncID)
		}
		if gotObs[i].content != fix.content {
			t.Errorf("obs row %d: content = %q, want %q", i, gotObs[i].content, fix.content)
		}
	}

	// 2. Assert all memory_relations fixture rows survive with original sync_id,
	//    source_id, target_id, relation, and judgment_status.
	relQueryRows, err := s.db.Query(
		`SELECT sync_id, source_id, target_id, relation, judgment_status FROM memory_relations ORDER BY id`,
	)
	if err != nil {
		t.Fatalf("query memory_relations fixtures: %v", err)
	}
	defer relQueryRows.Close()

	var gotRels []struct{ syncID, sourceID, targetID, relation, judgmentStatus string }
	for relQueryRows.Next() {
		var r struct{ syncID, sourceID, targetID, relation, judgmentStatus string }
		if err := relQueryRows.Scan(&r.syncID, &r.sourceID, &r.targetID, &r.relation, &r.judgmentStatus); err != nil {
			t.Fatalf("scan memory_relations row: %v", err)
		}
		gotRels = append(gotRels, r)
	}
	if err := relQueryRows.Err(); err != nil {
		t.Fatalf("relQueryRows.Err: %v", err)
	}

	if len(gotRels) != len(relRows) {
		t.Fatalf("expected %d memory_relations rows after migrate(), got %d", len(relRows), len(gotRels))
	}
	for i, fix := range relRows {
		if gotRels[i].syncID != fix.syncID {
			t.Errorf("rel row %d: sync_id = %q, want %q", i, gotRels[i].syncID, fix.syncID)
		}
		if gotRels[i].sourceID != fix.sourceID {
			t.Errorf("rel row %d: source_id = %q, want %q", i, gotRels[i].sourceID, fix.sourceID)
		}
		if gotRels[i].targetID != fix.targetID {
			t.Errorf("rel row %d: target_id = %q, want %q", i, gotRels[i].targetID, fix.targetID)
		}
		if gotRels[i].relation != fix.relation {
			t.Errorf("rel row %d: relation = %q, want %q", i, gotRels[i].relation, fix.relation)
		}
		if gotRels[i].judgmentStatus != fix.judgmentStatus {
			t.Errorf("rel row %d: judgment_status = %q, want %q", i, gotRels[i].judgmentStatus, fix.judgmentStatus)
		}
	}

	// 3. Assert sync_apply_deferred table exists (will be RED until Phase B).
	var tableName string
	err = s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='sync_apply_deferred'`,
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("sync_apply_deferred not found after migrate(): %v (expected RED until Phase B)", err)
	}
	if tableName != "sync_apply_deferred" {
		t.Errorf("sync_apply_deferred table name = %q, want 'sync_apply_deferred'", tableName)
	}

	// 4. Assert no spurious rows in sync_apply_deferred — migration must not
	//    touch deferred rows for pre-existing data.
	var deferredCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_apply_deferred`,
	).Scan(&deferredCount); err != nil {
		t.Fatalf("count sync_apply_deferred rows: %v (table may not exist — expected RED until Phase B)", err)
	}
	if deferredCount != 0 {
		t.Errorf("sync_apply_deferred has %d rows after migration, want 0 (migration must not pre-populate)", deferredCount)
	}
}

// ─── Phase 3 / A: Migration test infra for idx_memrel_status_created ──────────

// newTestStoreWithLegacySchemaPostP2 creates a temporary SQLite database using
// the post-Phase-2 DDL (legacyDDLPostMemoryConflictAudit), optionally seeds
// relation fixture rows, then calls New(cfg) so that migrate() runs against
// that database.
//
// This is the Phase 3 equivalent of newTestStoreWithLegacySchemaPostP1: it
// seeds the v_(N+2) baseline so that Phase 3 migration tests can assert that
// idx_memrel_status_created is added by migrate().
func newTestStoreWithLegacySchemaPostP2(t *testing.T, relRows []legacyRelationRow) *Store {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")

	// 1. Open raw DB and apply post-Phase-2 DDL.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("newTestStoreWithLegacySchemaPostP2: open raw db: %v", err)
	}

	if _, err := raw.Exec("PRAGMA journal_mode = WAL"); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchemaPostP2: WAL pragma: %v", err)
	}
	if _, err := raw.Exec("PRAGMA foreign_keys = ON"); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchemaPostP2: foreign_keys pragma: %v", err)
	}

	if _, err := raw.Exec(legacyDDLPostMemoryConflictAudit); err != nil {
		raw.Close()
		t.Fatalf("newTestStoreWithLegacySchemaPostP2: apply legacy DDL: %v", err)
	}

	// 2. Insert optional relation fixture rows.
	for _, row := range relRows {
		if _, err := raw.Exec(
			`INSERT INTO memory_relations (sync_id, source_id, target_id, relation, judgment_status)
			 VALUES (?, ?, ?, ?, ?)`,
			row.syncID, row.sourceID, row.targetID, row.relation, row.judgmentStatus,
		); err != nil {
			raw.Close()
			t.Fatalf("newTestStoreWithLegacySchemaPostP2: insert relation fixture %+v: %v", row, err)
		}
	}

	// 3. Close raw DB so the file is released before New() re-opens it.
	if err := raw.Close(); err != nil {
		t.Fatalf("newTestStoreWithLegacySchemaPostP2: close raw db: %v", err)
	}

	// 4. Open via New() — this calls migrate() against the post-Phase-2 DB.
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("newTestStoreWithLegacySchemaPostP2: New(cfg): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

// TestMigrate_AddsIdxMemrelStatusCreated asserts that running migrate() on a
// v_(N+2) database (post-Phase-2, pre-Phase-3) creates the composite index
// idx_memrel_status_created on memory_relations(judgment_status, created_at DESC).
//
// RED: fails until Phase B appends the CREATE INDEX statement to migrate().
func TestMigrate_AddsIdxMemrelStatusCreated(t *testing.T) {
	// Seed a handful of memory_relations rows with mixed judgment_status values
	// so the index is exercised by a real query in the triangulation assertion.
	relRows := []legacyRelationRow{
		{syncID: "rel-p3-001", sourceID: "obs-p3-src-1", targetID: "obs-p3-tgt-1", relation: "conflicts_with", judgmentStatus: "pending"},
		{syncID: "rel-p3-002", sourceID: "obs-p3-src-2", targetID: "obs-p3-tgt-2", relation: "related", judgmentStatus: "judged"},
		{syncID: "rel-p3-003", sourceID: "obs-p3-src-3", targetID: "obs-p3-tgt-3", relation: "conflicts_with", judgmentStatus: "pending"},
	}
	s := newTestStoreWithLegacySchemaPostP2(t, relRows)

	// 1. Assert idx_memrel_status_created exists in sqlite_master.
	//    This will FAIL (RED) until Phase B adds it to migrate().
	var idxName string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master
		 WHERE type='index' AND name='idx_memrel_status_created'`,
	).Scan(&idxName)
	if err != nil {
		t.Fatalf("idx_memrel_status_created not found after migrate(): %v — index not yet added by migrate() (expected RED until Phase B)", err)
	}
	if idxName != "idx_memrel_status_created" {
		t.Errorf("index name = %q, want 'idx_memrel_status_created'", idxName)
	}

	// 2. Triangulation: confirm the index covers the correct columns by querying
	//    using the indexed columns and asserting the result count matches seeded data.
	//    A query that filters on judgment_status should work correctly via the index.
	var pendingCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM memory_relations WHERE judgment_status = 'pending'`,
	).Scan(&pendingCount); err != nil {
		t.Fatalf("count pending relations: %v", err)
	}
	if pendingCount != 2 {
		t.Errorf("pending relations count = %d, want 2 (seeded rows with judgment_status='pending')", pendingCount)
	}
}
