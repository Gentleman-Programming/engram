package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ProjectsSchemaVersion is the current version of the engram-projects
// extension schema. It is stamped into PRAGMA user_version by
// migrateProjects. The upstream store does not use user_version for
// anything else, so this pragma is reserved exclusively for this
// extension and is safe to read from outside the store package (see
// ProjectsSchemaStatus).
const ProjectsSchemaVersion = 1

// projectsSchemaTriggers lists every FTS5 sync trigger created by
// projectsSchemaDDL. migrateProjects verifies each one exists after
// applying the DDL, as a defensive check against a SQLite build that
// silently failed to register a trigger.
var projectsSchemaTriggers = []string{
	"tasks_fts_insert", "tasks_fts_delete", "tasks_fts_update",
	"runbook_fts_insert", "runbook_fts_delete", "runbook_fts_update",
}

// projectsSchemaDDL creates the engram-projects v1 (EP-001) extension
// schema: the five contract tables (project_cards, tasks, evidence,
// runbook_index, task_observations), the observation_refs auxiliary table,
// and the tasks_fts / runbook_index_fts external-content FTS5 tables with
// their sync triggers. Every statement is idempotent (IF NOT EXISTS,
// including on triggers), so re-running it against an already-migrated
// database creates nothing and touches no existing row. No upstream table
// is modified.
const projectsSchemaDDL = `
PRAGMA foreign_keys = ON;

-- 1. Project card: 1:1 with the existing project value (same slug).
CREATE TABLE IF NOT EXISTS project_cards (
    slug               TEXT    PRIMARY KEY
                       CHECK (slug = lower(trim(slug)) AND length(slug) BETWEEN 1 AND 64
                              AND slug NOT IN ('migrate', 'current')),
    sync_id            TEXT    NOT NULL UNIQUE,
    display_name       TEXT    NOT NULL CHECK (length(trim(display_name)) > 0),
    repo_url           TEXT,
    default_branch     TEXT    NOT NULL DEFAULT 'master',
    jira_project       TEXT    NOT NULL DEFAULT 'PROJ',
    jira_component     TEXT,
    knowledge_hub_path TEXT,
    graph_path         TEXT    NOT NULL DEFAULT 'graphify-out/graph.json',
    graph_commit       TEXT    CHECK (graph_commit IS NULL OR length(graph_commit) = 40),
    graph_built_at     TEXT,
    graph_summary      TEXT    CHECK (graph_summary IS NULL OR json_valid(graph_summary)),
    owner              TEXT,
    created_at         TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at         TEXT    NOT NULL DEFAULT (datetime('now')),
    deleted_at         TEXT,
    -- A graph fact never exists without the commit it was built from.
    CHECK ((graph_commit IS NULL) = (graph_built_at IS NULL)),
    CHECK (graph_summary IS NULL OR graph_commit IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_project_cards_updated ON project_cards(updated_at DESC);

-- 2. Tasks: one row per Jira ticket or SDD change inside a project.
CREATE TABLE IF NOT EXISTS tasks (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    sync_id              TEXT    NOT NULL UNIQUE,
    project              TEXT    NOT NULL REFERENCES project_cards(slug)
                                 ON DELETE RESTRICT ON UPDATE CASCADE,
    jira_key             TEXT    UNIQUE
                         CHECK (jira_key IS NULL OR jira_key GLOB '[A-Z]*-[0-9]*'),
    sdd_change           TEXT    CHECK (sdd_change IS NULL OR sdd_change = lower(sdd_change)),
    title                TEXT    NOT NULL CHECK (length(trim(title)) > 0),
    kind                 TEXT    NOT NULL
                         CHECK (kind IN ('feature','bugfix','refactor','incident','migration','spike')),
    state                TEXT    NOT NULL DEFAULT 'open'
                         CHECK (state IN ('open','analysis','in_progress','review','verified',
                                          'done','blocked','cancelled')),
    jira_status          TEXT,
    jira_status_category TEXT    CHECK (jira_status_category IS NULL
                                        OR jira_status_category IN ('new','indeterminate','done')),
    state_synced_at      TEXT,
    branch               TEXT,
    pr_url               TEXT,
    knowledge_ref        TEXT,
    assignee             TEXT,
    created_at           TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at           TEXT    NOT NULL DEFAULT (datetime('now')),
    closed_at            TEXT,
    deleted_at           TEXT,
    CHECK (closed_at IS NULL OR state IN ('done','cancelled')),
    CHECK (jira_key IS NOT NULL OR sdd_change IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_tasks_project_state ON tasks(project, state, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_tasks_sdd_change    ON tasks(project, sdd_change);
CREATE INDEX IF NOT EXISTS idx_tasks_deleted       ON tasks(deleted_at);

-- 3. Evidence: one row per captured file (metadata only, never the bytes).
CREATE TABLE IF NOT EXISTS evidence (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    sync_id                 TEXT    NOT NULL UNIQUE,
    project                 TEXT    NOT NULL REFERENCES project_cards(slug)
                                    ON DELETE RESTRICT ON UPDATE CASCADE,
    task_id                 INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    task_sync_id            TEXT    NOT NULL REFERENCES tasks(sync_id) ON DELETE CASCADE,
    path                    TEXT    NOT NULL
                            CHECK (length(trim(path)) > 0 AND path NOT LIKE '/%' AND path NOT LIKE '~%'),
    sha256                  TEXT    NOT NULL
                            CHECK (length(sha256) = 64 AND sha256 = lower(sha256)),
    kind                    TEXT    NOT NULL CHECK (kind IN ('png','gif','mp4','json','log','txt')),
    proves                  TEXT    NOT NULL CHECK (length(trim(proves)) > 0),
    config_stamp            TEXT,
    captured_at             TEXT    NOT NULL,
    attached_jira           INTEGER NOT NULL DEFAULT 0 CHECK (attached_jira IN (0, 1)),
    attached_confluence_url TEXT,
    size_bytes              INTEGER CHECK (size_bytes IS NULL OR size_bytes >= 0),
    manifest_path           TEXT,
    created_at              TEXT    NOT NULL DEFAULT (datetime('now')),
    deleted_at              TEXT,
    UNIQUE (task_sync_id, sha256)
);
CREATE INDEX IF NOT EXISTS idx_evidence_task    ON evidence(task_id, captured_at DESC);
CREATE INDEX IF NOT EXISTS idx_evidence_project ON evidence(project, captured_at DESC);

-- 4. Runbook index: derived from the vault, rebuilt per machine, never replicated.
--    seq is the stable INTEGER rowid required by the external-content FTS5 table;
--    id (RB-NNN) stays the business key (UNIQUE).
CREATE TABLE IF NOT EXISTS runbook_index (
    seq              INTEGER PRIMARY KEY AUTOINCREMENT,
    id               TEXT    NOT NULL UNIQUE CHECK (id GLOB 'RB-[0-9][0-9][0-9]'),
    project          TEXT    NOT NULL REFERENCES project_cards(slug)
                             ON DELETE RESTRICT ON UPDATE CASCADE,
    vault_path       TEXT    NOT NULL UNIQUE CHECK (vault_path NOT LIKE '/%'),
    title            TEXT    NOT NULL,
    category         TEXT    NOT NULL
                     CHECK (category IN ('auth','database','queue','network','performance',
                                         'data-integrity','registration')),
    pattern          TEXT    CHECK (pattern IS NULL OR pattern IN ('missing-files','auth-access',
                                    'file-save-failure','sync-upload','registration-subscription','other')),
    severity         TEXT    CHECK (severity IS NULL OR severity IN ('P1','P2','P3','P4')),
    status           TEXT    NOT NULL CHECK (status IN ('draft','verified','outdated')),
    symptoms         TEXT    NOT NULL DEFAULT '',
    owner            TEXT,
    automation_level TEXT    CHECK (automation_level IS NULL
                                    OR automation_level IN ('manual','assisted','autonomous-with-gate')),
    last_updated     TEXT,
    last_verified    TEXT,
    stale            INTEGER NOT NULL DEFAULT 0 CHECK (stale IN (0, 1)),
    age_days         INTEGER CHECK (age_days IS NULL OR age_days >= 0),
    exec_count       INTEGER NOT NULL DEFAULT 0 CHECK (exec_count >= 0),
    last_exec_at     TEXT,
    synced_at        TEXT    NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_runbook_project_status   ON runbook_index(project, status, stale);
CREATE INDEX IF NOT EXISTS idx_runbook_category_pattern ON runbook_index(category, pattern);

-- 5. Task <-> observation link (N:M). Local integer keys for integrity,
--    sync_id pairs for cross-machine replication.
CREATE TABLE IF NOT EXISTS task_observations (
    task_id             INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    observation_id      INTEGER NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
    task_sync_id        TEXT    NOT NULL,
    observation_sync_id TEXT    NOT NULL,
    role                TEXT    NOT NULL DEFAULT 'context'
                        CHECK (role IN ('context','decision','root_cause','evidence','summary')),
    linked_at           TEXT    NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (task_id, observation_id),
    UNIQUE (task_sync_id, observation_sync_id)
);
CREATE INDEX IF NOT EXISTS idx_task_obs_observation ON task_observations(observation_id);

-- 6. Auxiliary: external references of an observation (knowledge_ref for observations,
--    graph facts stamped with graph_commit, runbook and Jira pointers). Keyed by
--    observation sync_id without FK, like memory_relations.source_id/target_id.
CREATE TABLE IF NOT EXISTS observation_refs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    observation_sync_id TEXT    NOT NULL,
    ref_kind            TEXT    NOT NULL CHECK (ref_kind IN ('knowledge','graph','runbook','jira')),
    ref                 TEXT    NOT NULL CHECK (length(trim(ref)) > 0),
    graph_commit        TEXT    CHECK (graph_commit IS NULL OR length(graph_commit) = 40),
    created_at          TEXT    NOT NULL DEFAULT (datetime('now')),
    UNIQUE (observation_sync_id, ref_kind, ref),
    CHECK (ref_kind <> 'graph' OR graph_commit IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_obs_refs_kind_ref ON observation_refs(ref_kind, ref);
CREATE INDEX IF NOT EXISTS idx_obs_refs_obs      ON observation_refs(observation_sync_id);

-- 7. FTS5 over tasks (external content, same pattern as observations_fts).
CREATE VIRTUAL TABLE IF NOT EXISTS tasks_fts USING fts5(
    title, jira_key, sdd_change, branch, project,
    content='tasks', content_rowid='id'
);
CREATE TRIGGER IF NOT EXISTS tasks_fts_insert AFTER INSERT ON tasks BEGIN
    INSERT INTO tasks_fts(rowid, title, jira_key, sdd_change, branch, project)
    VALUES (new.id, new.title, new.jira_key, new.sdd_change, new.branch, new.project);
END;
CREATE TRIGGER IF NOT EXISTS tasks_fts_delete AFTER DELETE ON tasks BEGIN
    INSERT INTO tasks_fts(tasks_fts, rowid, title, jira_key, sdd_change, branch, project)
    VALUES ('delete', old.id, old.title, old.jira_key, old.sdd_change, old.branch, old.project);
END;
CREATE TRIGGER IF NOT EXISTS tasks_fts_update AFTER UPDATE ON tasks BEGIN
    INSERT INTO tasks_fts(tasks_fts, rowid, title, jira_key, sdd_change, branch, project)
    VALUES ('delete', old.id, old.title, old.jira_key, old.sdd_change, old.branch, old.project);
    INSERT INTO tasks_fts(rowid, title, jira_key, sdd_change, branch, project)
    VALUES (new.id, new.title, new.jira_key, new.sdd_change, new.branch, new.project);
END;

-- 8. FTS5 over the runbook index (symptoms are the main retrieval signal).
CREATE VIRTUAL TABLE IF NOT EXISTS runbook_index_fts USING fts5(
    title, symptoms, category, pattern, project, id,
    content='runbook_index', content_rowid='seq'
);
CREATE TRIGGER IF NOT EXISTS runbook_fts_insert AFTER INSERT ON runbook_index BEGIN
    INSERT INTO runbook_index_fts(rowid, title, symptoms, category, pattern, project, id)
    VALUES (new.seq, new.title, new.symptoms, new.category, new.pattern, new.project, new.id);
END;
CREATE TRIGGER IF NOT EXISTS runbook_fts_delete AFTER DELETE ON runbook_index BEGIN
    INSERT INTO runbook_index_fts(runbook_index_fts, rowid, title, symptoms, category, pattern, project, id)
    VALUES ('delete', old.seq, old.title, old.symptoms, old.category, old.pattern, old.project, old.id);
END;
CREATE TRIGGER IF NOT EXISTS runbook_fts_update AFTER UPDATE ON runbook_index BEGIN
    INSERT INTO runbook_index_fts(runbook_index_fts, rowid, title, symptoms, category, pattern, project, id)
    VALUES ('delete', old.seq, old.title, old.symptoms, old.category, old.pattern, old.project, old.id);
    INSERT INTO runbook_index_fts(rowid, title, symptoms, category, pattern, project, id)
    VALUES (new.seq, new.title, new.symptoms, new.category, new.pattern, new.project, new.id);
END;
`

// migrateProjects applies the engram-projects v1 schema (EP-001). It is
// invoked once from Store.migrate() and is safe to run on every startup:
// every statement in projectsSchemaDDL is idempotent, so re-running it
// against an already-migrated database creates nothing and touches no
// existing row.
//
// After applying the DDL it verifies that every FTS5 sync trigger was
// actually registered, then stamps PRAGMA user_version forward-only: an
// already-migrated database (user_version >= ProjectsSchemaVersion) is left
// untouched. user_version is a diagnostic label here, not a migration
// precondition — the DDL's own idempotency is what makes re-runs safe.
func (s *Store) migrateProjects() error {
	if _, err := s.execHook(s.db, projectsSchemaDDL); err != nil {
		return fmt.Errorf("engram-projects: apply schema: %w", err)
	}

	for _, trigger := range projectsSchemaTriggers {
		var name string
		err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='trigger' AND name=?`, trigger,
		).Scan(&name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("engram-projects: trigger %q missing after migration", trigger)
			}
			return fmt.Errorf("engram-projects: verify trigger %q: %w", trigger, err)
		}
	}

	var current int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("engram-projects: read user_version: %w", err)
	}
	if current < ProjectsSchemaVersion {
		if _, err := s.execHook(s.db, fmt.Sprintf("PRAGMA user_version = %d", ProjectsSchemaVersion)); err != nil {
			return fmt.Errorf("engram-projects: stamp user_version: %w", err)
		}
	}
	return nil
}

// projectsSchemaDropDDL removes every engram-projects object in dependency
// order: FTS5 sync triggers first, then the FTS5 virtual tables, then the
// six contract/auxiliary tables (children before parents so foreign keys
// never block the drop). No upstream table is touched.
const projectsSchemaDropDDL = `
DROP TRIGGER IF EXISTS runbook_fts_update;
DROP TRIGGER IF EXISTS runbook_fts_delete;
DROP TRIGGER IF EXISTS runbook_fts_insert;
DROP TRIGGER IF EXISTS tasks_fts_update;
DROP TRIGGER IF EXISTS tasks_fts_delete;
DROP TRIGGER IF EXISTS tasks_fts_insert;
DROP TABLE IF EXISTS runbook_index_fts;
DROP TABLE IF EXISTS tasks_fts;
DROP TABLE IF EXISTS observation_refs;
DROP TABLE IF EXISTS task_observations;
DROP TABLE IF EXISTS evidence;
DROP TABLE IF EXISTS runbook_index;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS project_cards;
`

// DropProjectsSchema is the explicit rollback path for the engram-projects
// extension: it drops every trigger, FTS5 table, and contract table it
// created, then resets PRAGMA user_version to 0. Callers (e.g. the
// `engram projects schema drop --yes` CLI command) are responsible for
// requiring an export/confirmation before invoking this — it performs no
// backup itself. It never touches any upstream table (sessions,
// observations, memory_relations, sync_*, ...).
func (s *Store) DropProjectsSchema() error {
	if _, err := s.execHook(s.db, projectsSchemaDropDDL); err != nil {
		return fmt.Errorf("engram-projects: drop schema: %w", err)
	}
	if _, err := s.execHook(s.db, "PRAGMA user_version = 0"); err != nil {
		return fmt.Errorf("engram-projects: reset user_version: %w", err)
	}
	return nil
}

// ProjectsSchemaStatus reports whether the engram-projects schema is
// present, its PRAGMA user_version stamp, and row counts for the five
// contract tables. It is the read model `engram doctor` uses to confirm the
// schema is up to date (see ProjectsSchemaCheck in internal/diagnostic)
// without that package running raw SQL against the store.
type ProjectsSchemaStatus struct {
	Present          bool `json:"present"`
	UserVersion      int  `json:"user_version"`
	ProjectCards     int  `json:"project_cards"`
	Tasks            int  `json:"tasks"`
	Evidence         int  `json:"evidence"`
	RunbookIndex     int  `json:"runbook_index"`
	TaskObservations int  `json:"task_observations"`
}

// ProjectsSchemaStatus reads the current state of the engram-projects
// schema. When the schema has not been created yet (Present == false), the
// row counts are left at zero rather than erroring.
func (s *Store) ProjectsSchemaStatus() (ProjectsSchemaStatus, error) {
	var status ProjectsSchemaStatus

	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return status, fmt.Errorf("engram-projects: read user_version: %w", err)
	}
	status.UserVersion = version

	var name string
	err := s.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='project_cards'`,
	).Scan(&name)
	switch {
	case err == nil:
		status.Present = true
	case errors.Is(err, sql.ErrNoRows):
		return status, nil
	default:
		return status, fmt.Errorf("engram-projects: check schema presence: %w", err)
	}

	counts := []struct {
		table string
		dest  *int
	}{
		{"project_cards", &status.ProjectCards},
		{"tasks", &status.Tasks},
		{"evidence", &status.Evidence},
		{"runbook_index", &status.RunbookIndex},
		{"task_observations", &status.TaskObservations},
	}
	for _, c := range counts {
		if err := s.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", c.table)).Scan(c.dest); err != nil {
			return status, fmt.Errorf("engram-projects: count %s: %w", c.table, err)
		}
	}
	return status, nil
}
