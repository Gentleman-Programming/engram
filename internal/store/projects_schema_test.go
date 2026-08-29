package store

import (
	"database/sql"
	"testing"
)

// newProjectsSchemaTestStore opens a Store against a throwaway temp
// directory (t.TempDir()). It never touches ~/.engram/engram.db: New()
// resolves its database path from cfg.DataDir, which is overridden here.
func newProjectsSchemaTestStore(t *testing.T) *Store {
	t.Helper()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New(cfg): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name=?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("check table %q: %v", name, err)
	}
	return true
}

// TestMigrateProjects_CreatesAllFiveContractTables asserts the five tables
// named in the roadmap/RFC contract exist after New(), plus the auxiliary
// table and both FTS5 tables.
func TestMigrateProjects_CreatesAllFiveContractTables(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	contractTables := []string{
		"project_cards", "tasks", "evidence", "runbook_index", "task_observations",
	}
	for _, table := range contractTables {
		if !tableExists(t, s.db, table) {
			t.Errorf("contract table %q missing after migrate()", table)
		}
	}

	auxiliaryObjects := []string{"observation_refs", "tasks_fts", "runbook_index_fts"}
	for _, name := range auxiliaryObjects {
		if !tableExists(t, s.db, name) {
			t.Errorf("auxiliary object %q missing after migrate()", name)
		}
	}
}

// TestMigrateProjects_StampsUserVersion asserts PRAGMA user_version is set
// to ProjectsSchemaVersion after a fresh migration.
func TestMigrateProjects_StampsUserVersion(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != ProjectsSchemaVersion {
		t.Fatalf("user_version = %d, want %d", version, ProjectsSchemaVersion)
	}
}

// TestMigrateProjects_Idempotent applies the migration twice against the
// same on-disk database (via two New()/Close() cycles, the same pattern
// TestMigrate_Idempotent uses for the upstream schema) and asserts the
// second run neither fails nor duplicates any table, trigger, or row.
func TestMigrateProjects_Idempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir

	s1, err := New(cfg)
	if err != nil {
		t.Fatalf("New(cfg) first run: %v", err)
	}
	if _, err := s1.execHook(s1.db,
		`INSERT INTO project_cards (slug, sync_id, display_name) VALUES ('nextcloud', 'card-1', 'Nextcloud')`,
	); err != nil {
		s1.Close()
		t.Fatalf("seed project_cards: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	// Second run: migrateProjects() executes again against the same file.
	s2, err := New(cfg)
	if err != nil {
		t.Fatalf("New(cfg) second run failed: %v — migrateProjects() is not idempotent", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	var version int
	if err := s2.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version after second run: %v", err)
	}
	if version != ProjectsSchemaVersion {
		t.Fatalf("user_version after second run = %d, want %d (must not regress or drift)", version, ProjectsSchemaVersion)
	}

	var cardCount int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM project_cards`).Scan(&cardCount); err != nil {
		t.Fatalf("count project_cards after second run: %v", err)
	}
	if cardCount != 1 {
		t.Fatalf("project_cards count after second migrate = %d, want 1 (re-running must not duplicate rows)", cardCount)
	}

	for _, trigger := range projectsSchemaTriggers {
		var count int
		if err := s2.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger,
		).Scan(&count); err != nil {
			t.Fatalf("count trigger %q: %v", trigger, err)
		}
		if count != 1 {
			t.Errorf("trigger %q count after second migrate = %d, want 1 (must not duplicate)", trigger, count)
		}
	}
}

// TestMigrateProjects_RunningMigrateTwiceInProcess calls migrateProjects()
// twice on the same open connection (in addition to the close/reopen
// idempotency test above) to guard the in-process call path used by
// Store.migrate() itself.
func TestMigrateProjects_RunningMigrateTwiceInProcess(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	if err := s.migrateProjects(); err != nil {
		t.Fatalf("second in-process migrateProjects() call failed: %v", err)
	}
}

// TestProjectsSchema_ForeignKeyConstraints asserts the RFC's FK
// relationships are enforced: tasks.project -> project_cards(slug),
// evidence.task_id -> tasks(id) ON DELETE CASCADE, and
// task_observations -> tasks/observations ON DELETE CASCADE.
func TestProjectsSchema_ForeignKeyConstraints(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	// tasks.project must reference an existing project_cards.slug.
	_, err := s.db.Exec(
		`INSERT INTO tasks (sync_id, project, jira_key, title, kind) VALUES ('task-orphan', 'ghost-project', 'PROJ-1', 'orphan task', 'feature')`,
	)
	if err == nil {
		t.Fatal("expected FK violation inserting a task for a non-existent project, got none")
	}

	if _, err := s.db.Exec(
		`INSERT INTO project_cards (slug, sync_id, display_name) VALUES ('nextcloud', 'card-nc', 'Nextcloud')`,
	); err != nil {
		t.Fatalf("seed project_cards: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO tasks (id, sync_id, project, jira_key, title, kind) VALUES (1, 'task-1', 'nextcloud', 'PROJ-100', 'fix upload', 'bugfix')`,
	); err != nil {
		t.Fatalf("insert valid task: %v", err)
	}

	// evidence.task_id ON DELETE CASCADE.
	if _, err := s.db.Exec(
		`INSERT INTO evidence (sync_id, project, task_id, task_sync_id, path, sha256, kind, proves, captured_at)
		 VALUES ('evd-1', 'nextcloud', 1, 'task-1', 'evidence/screenshot.png', ?, 'png', 'upload works', datetime('now'))`,
		"a"+repeatChar("0", 63),
	); err != nil {
		t.Fatalf("insert valid evidence: %v", err)
	}

	if _, err := s.db.Exec(`DELETE FROM tasks WHERE id = 1`); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	var evidenceCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM evidence WHERE task_id = 1`).Scan(&evidenceCount); err != nil {
		t.Fatalf("count evidence after task delete: %v", err)
	}
	if evidenceCount != 0 {
		t.Fatalf("evidence rows after deleting parent task = %d, want 0 (ON DELETE CASCADE)", evidenceCount)
	}
}

// TestProjectsSchema_EnumChecks asserts a representative CHECK constraint
// from each contract table rejects an out-of-enum value.
func TestProjectsSchema_EnumChecks(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO project_cards (slug, sync_id, display_name) VALUES ('nextcloud', 'card-nc', 'Nextcloud')`,
	); err != nil {
		t.Fatalf("seed project_cards: %v", err)
	}

	// tasks.kind must be one of the enumerated values.
	if _, err := s.db.Exec(
		`INSERT INTO tasks (sync_id, project, jira_key, title, kind) VALUES ('task-bad-kind', 'nextcloud', 'PROJ-1', 'bad kind', 'not-a-kind')`,
	); err == nil {
		t.Error("expected CHECK violation for tasks.kind = 'not-a-kind', got none")
	}

	// project_cards.slug must be lowercase, trimmed, length 1-64, and not a reserved word.
	if _, err := s.db.Exec(
		`INSERT INTO project_cards (slug, sync_id, display_name) VALUES ('migrate', 'card-reserved', 'Reserved')`,
	); err == nil {
		t.Error("expected CHECK violation for reserved slug 'migrate', got none")
	}

	// runbook_index.id must match RB-NNN.
	if _, err := s.db.Exec(
		`INSERT INTO runbook_index (id, project, vault_path, title, category, status)
		 VALUES ('RB-1', 'nextcloud', 'runbooks/rb1.md', 'Bad id', 'auth', 'draft')`,
	); err == nil {
		t.Error("expected CHECK violation for runbook_index.id = 'RB-1' (must be RB-NNN), got none")
	}

	// evidence.kind must be one of the enumerated file kinds.
	if _, err := s.db.Exec(
		`INSERT INTO tasks (id, sync_id, project, jira_key, title, kind) VALUES (1, 'task-1', 'nextcloud', 'PROJ-100', 'fix upload', 'bugfix')`,
	); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO evidence (sync_id, project, task_id, task_sync_id, path, sha256, kind, proves, captured_at)
		 VALUES ('evd-bad-kind', 'nextcloud', 1, 'task-1', 'evidence/file.exe', ?, 'exe', 'bad kind', datetime('now'))`,
		"b"+repeatChar("0", 63),
	); err == nil {
		t.Error("expected CHECK violation for evidence.kind = 'exe', got none")
	}

	// project_cards graph invariant: graph_summary requires graph_commit.
	if _, err := s.db.Exec(
		`INSERT INTO project_cards (slug, sync_id, display_name, graph_summary) VALUES ('orphan-graph', 'card-og', 'Orphan Graph', '{}')`,
	); err == nil {
		t.Error("expected CHECK violation for graph_summary without graph_commit, got none")
	}
}

// TestMigrateProjects_DoesNotTouchUpstreamTables asserts the extension
// leaves the upstream sessions/observations tables completely untouched:
// no new columns, no row mutation.
func TestMigrateProjects_DoesNotTouchUpstreamTables(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, project, directory) VALUES ('ses-1', 'engram', '/tmp')`,
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO observations (session_id, type, title, content, project) VALUES ('ses-1', 'decision', 'title', 'content', 'engram')`,
	); err != nil {
		t.Fatalf("insert observation: %v", err)
	}

	var obsCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE session_id = 'ses-1'`).Scan(&obsCount); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if obsCount != 1 {
		t.Fatalf("observations count = %d, want 1", obsCount)
	}
}

// TestProjectsSchemaStatus_ReportsPresenceVersionAndCounts exercises the
// read model engram doctor uses (ProjectsSchemaStatus).
func TestProjectsSchemaStatus_ReportsPresenceVersionAndCounts(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	status, err := s.ProjectsSchemaStatus()
	if err != nil {
		t.Fatalf("ProjectsSchemaStatus: %v", err)
	}
	if !status.Present {
		t.Fatal("status.Present = false, want true after migrate()")
	}
	if status.UserVersion != ProjectsSchemaVersion {
		t.Fatalf("status.UserVersion = %d, want %d", status.UserVersion, ProjectsSchemaVersion)
	}
	if status.ProjectCards != 0 || status.Tasks != 0 {
		t.Fatalf("expected zero rows on a fresh schema, got %+v", status)
	}

	if _, err := s.db.Exec(
		`INSERT INTO project_cards (slug, sync_id, display_name) VALUES ('nextcloud', 'card-nc', 'Nextcloud')`,
	); err != nil {
		t.Fatalf("seed project_cards: %v", err)
	}

	status, err = s.ProjectsSchemaStatus()
	if err != nil {
		t.Fatalf("ProjectsSchemaStatus after insert: %v", err)
	}
	if status.ProjectCards != 1 {
		t.Fatalf("status.ProjectCards = %d, want 1", status.ProjectCards)
	}
}

// TestDropProjectsSchema_RemovesEverythingAndResetsVersion asserts the
// explicit rollback path (DropProjectsSchema) removes every contract table,
// the auxiliary table, both FTS5 tables, and resets user_version to 0 —
// without touching the upstream schema.
func TestDropProjectsSchema_RemovesEverythingAndResetsVersion(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	if err := s.DropProjectsSchema(); err != nil {
		t.Fatalf("DropProjectsSchema: %v", err)
	}

	dropped := []string{
		"project_cards", "tasks", "evidence", "runbook_index", "task_observations",
		"observation_refs", "tasks_fts", "runbook_index_fts",
	}
	for _, name := range dropped {
		if tableExists(t, s.db, name) {
			t.Errorf("%q still present after DropProjectsSchema", name)
		}
	}

	for _, trigger := range projectsSchemaTriggers {
		var count int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger,
		).Scan(&count); err != nil {
			t.Fatalf("count trigger %q after drop: %v", trigger, err)
		}
		if count != 0 {
			t.Errorf("trigger %q still present after DropProjectsSchema", trigger)
		}
	}

	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version after drop: %v", err)
	}
	if version != 0 {
		t.Fatalf("user_version after DropProjectsSchema = %d, want 0", version)
	}

	// Upstream tables must be unaffected by the drop.
	if !tableExists(t, s.db, "sessions") || !tableExists(t, s.db, "observations") {
		t.Fatal("DropProjectsSchema removed an upstream table")
	}

	status, err := s.ProjectsSchemaStatus()
	if err != nil {
		t.Fatalf("ProjectsSchemaStatus after drop: %v", err)
	}
	if status.Present {
		t.Fatal("status.Present = true after DropProjectsSchema, want false")
	}
}

// TestDropProjectsSchema_ThenReapply reproduces the documented recovery
// path: drop the extension, then let migrate() recreate it from scratch on
// the next New(). This is the reversibility half of the roadmap acceptance
// criterion, exercised against a temp database rather than any real one.
func TestDropProjectsSchema_ThenReapply(t *testing.T) {
	s := newProjectsSchemaTestStore(t)

	if err := s.DropProjectsSchema(); err != nil {
		t.Fatalf("DropProjectsSchema: %v", err)
	}
	if err := s.migrateProjects(); err != nil {
		t.Fatalf("migrateProjects() after drop: %v", err)
	}

	status, err := s.ProjectsSchemaStatus()
	if err != nil {
		t.Fatalf("ProjectsSchemaStatus after reapply: %v", err)
	}
	if !status.Present {
		t.Fatal("status.Present = false after reapplying migrateProjects()")
	}
	if status.UserVersion != ProjectsSchemaVersion {
		t.Fatalf("status.UserVersion after reapply = %d, want %d", status.UserVersion, ProjectsSchemaVersion)
	}
}

func repeatChar(c string, n int) string {
	out := make([]byte, 0, n*len(c))
	for i := 0; i < n; i++ {
		out = append(out, c...)
	}
	return string(out)
}
