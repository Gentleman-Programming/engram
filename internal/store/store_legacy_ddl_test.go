package store

// legacyDDLPostMemoryConflictSurfacing is a snapshot of the full database
// schema as it exists AFTER the memory-conflict-surfacing change (Phase 1)
// was applied and BEFORE the memory-conflict-surfacing-cloud-sync change
// (Phase 2) is applied.
//
// This is the v_(N+1) baseline. Phase 2 migration tests seed a DB with this
// DDL and then call migrate() to assert that sync_apply_deferred is added.
//
// Rules:
//   - NEVER modify this constant after it is committed.
//   - It captures the exact schema that Phase 1 left behind.
//   - sync_apply_deferred is intentionally absent — Phase B will add it.
const legacyDDLPostMemoryConflictSurfacing = `
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    project    TEXT NOT NULL,
    directory  TEXT NOT NULL,
    started_at TEXT NOT NULL DEFAULT (datetime('now')),
    ended_at   TEXT,
    summary    TEXT
);

CREATE TABLE IF NOT EXISTS observations (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    sync_id              TEXT,
    session_id           TEXT    NOT NULL,
    type                 TEXT    NOT NULL,
    title                TEXT    NOT NULL,
    content              TEXT    NOT NULL,
    tool_name            TEXT,
    project              TEXT,
    scope                TEXT    NOT NULL DEFAULT 'project',
    topic_key            TEXT,
    normalized_hash      TEXT,
    revision_count       INTEGER NOT NULL DEFAULT 1,
    duplicate_count      INTEGER NOT NULL DEFAULT 1,
    last_seen_at         TEXT,
    created_at           TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at           TEXT    NOT NULL DEFAULT (datetime('now')),
    deleted_at           TEXT,
    review_after         TEXT,
    expires_at           TEXT,
    embedding            BLOB,
    embedding_model      TEXT,
    embedding_created_at TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE INDEX IF NOT EXISTS idx_obs_session  ON observations(session_id);
CREATE INDEX IF NOT EXISTS idx_obs_type     ON observations(type);
CREATE INDEX IF NOT EXISTS idx_obs_project  ON observations(project);
CREATE INDEX IF NOT EXISTS idx_obs_created  ON observations(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_obs_scope    ON observations(scope);
CREATE INDEX IF NOT EXISTS idx_obs_sync_id  ON observations(sync_id);
CREATE INDEX IF NOT EXISTS idx_obs_topic    ON observations(topic_key, project, scope, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_obs_deleted  ON observations(deleted_at);
CREATE INDEX IF NOT EXISTS idx_obs_dedupe   ON observations(normalized_hash, project, scope, type, title, created_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS observations_fts USING fts5(
    title,
    content,
    tool_name,
    type,
    project,
    topic_key,
    content='observations',
    content_rowid='id'
);

CREATE TABLE IF NOT EXISTS user_prompts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    sync_id    TEXT,
    session_id TEXT    NOT NULL,
    content    TEXT    NOT NULL,
    project    TEXT,
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE TABLE IF NOT EXISTS prompt_tombstones (
    sync_id    TEXT PRIMARY KEY,
    session_id TEXT,
    project    TEXT,
    deleted_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_prompts_session    ON user_prompts(session_id);
CREATE INDEX IF NOT EXISTS idx_prompts_project    ON user_prompts(project);
CREATE INDEX IF NOT EXISTS idx_prompts_created    ON user_prompts(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_prompts_sync_id    ON user_prompts(sync_id);
CREATE INDEX IF NOT EXISTS idx_prompt_tombstones_project ON prompt_tombstones(project, deleted_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS prompts_fts USING fts5(
    content,
    project,
    content='user_prompts',
    content_rowid='id'
);

CREATE TABLE IF NOT EXISTS sync_chunks (
    target_key  TEXT NOT NULL DEFAULT 'local',
    chunk_id    TEXT NOT NULL,
    imported_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (target_key, chunk_id)
);

CREATE TABLE IF NOT EXISTS sync_state (
    target_key           TEXT PRIMARY KEY,
    lifecycle            TEXT NOT NULL DEFAULT 'idle',
    last_enqueued_seq    INTEGER NOT NULL DEFAULT 0,
    last_acked_seq       INTEGER NOT NULL DEFAULT 0,
    last_pulled_seq      INTEGER NOT NULL DEFAULT 0,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    backoff_until        TEXT,
    lease_owner          TEXT,
    lease_until          TEXT,
    last_error           TEXT,
    reason_code          TEXT,
    reason_message       TEXT,
    updated_at           TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS sync_mutations (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    target_key  TEXT NOT NULL,
    entity      TEXT NOT NULL,
    entity_key  TEXT NOT NULL,
    op          TEXT NOT NULL,
    payload     TEXT NOT NULL,
    source      TEXT NOT NULL DEFAULT 'local',
    project     TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
    acked_at    TEXT,
    FOREIGN KEY (target_key) REFERENCES sync_state(target_key)
);

CREATE INDEX IF NOT EXISTS idx_sync_mutations_target_seq ON sync_mutations(target_key, seq);
CREATE INDEX IF NOT EXISTS idx_sync_mutations_pending    ON sync_mutations(target_key, acked_at, seq);
CREATE INDEX IF NOT EXISTS idx_sync_mutations_project    ON sync_mutations(project);
CREATE INDEX IF NOT EXISTS idx_sync_mutations_lookup     ON sync_mutations(target_key, entity, entity_key, source);

CREATE TABLE IF NOT EXISTS sync_enrolled_projects (
    project     TEXT PRIMARY KEY,
    enrolled_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS cloud_upgrade_state (
    project            TEXT PRIMARY KEY,
    stage              TEXT NOT NULL DEFAULT 'planned',
    repair_class       TEXT NOT NULL DEFAULT 'none',
    snapshot_json      TEXT NOT NULL DEFAULT '{}',
    last_error_code    TEXT,
    last_error_message TEXT,
    findings_json      TEXT,
    applied_actions    TEXT,
    updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_cloud_upgrade_state_stage ON cloud_upgrade_state(stage);

CREATE TABLE IF NOT EXISTS memory_relations (
    id                        INTEGER PRIMARY KEY AUTOINCREMENT,
    sync_id                   TEXT    NOT NULL UNIQUE,
    source_id                 TEXT,
    target_id                 TEXT,
    relation                  TEXT    NOT NULL DEFAULT 'pending',
    reason                    TEXT,
    evidence                  TEXT,
    confidence                REAL,
    judgment_status           TEXT    NOT NULL DEFAULT 'pending',
    marked_by_actor           TEXT,
    marked_by_kind            TEXT,
    marked_by_model           TEXT,
    session_id                TEXT,
    superseded_at             TEXT,
    superseded_by_relation_id INTEGER REFERENCES memory_relations(id) ON DELETE SET NULL,
    created_at                TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at                TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_memrel_source    ON memory_relations(source_id, judgment_status);
CREATE INDEX IF NOT EXISTS idx_memrel_target    ON memory_relations(target_id, judgment_status);
CREATE INDEX IF NOT EXISTS idx_memrel_supersede ON memory_relations(superseded_by_relation_id);

INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at)
VALUES ('cloud', 'idle', datetime('now'));

CREATE TRIGGER obs_fts_insert AFTER INSERT ON observations BEGIN
    INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
    VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project, new.topic_key);
END;

CREATE TRIGGER obs_fts_delete AFTER DELETE ON observations BEGIN
    INSERT INTO observations_fts(observations_fts, rowid, title, content, tool_name, type, project, topic_key)
    VALUES ('delete', old.id, old.title, old.content, old.tool_name, old.type, old.project, old.topic_key);
END;

CREATE TRIGGER obs_fts_update AFTER UPDATE ON observations BEGIN
    INSERT INTO observations_fts(observations_fts, rowid, title, content, tool_name, type, project, topic_key)
    VALUES ('delete', old.id, old.title, old.content, old.tool_name, old.type, old.project, old.topic_key);
    INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
    VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project, new.topic_key);
END;

CREATE TRIGGER prompt_fts_insert AFTER INSERT ON user_prompts BEGIN
    INSERT INTO prompts_fts(rowid, content, project)
    VALUES (new.id, new.content, new.project);
END;

CREATE TRIGGER prompt_fts_delete AFTER DELETE ON user_prompts BEGIN
    INSERT INTO prompts_fts(prompts_fts, rowid, content, project)
    VALUES ('delete', old.id, old.content, old.project);
END;

CREATE TRIGGER prompt_fts_update AFTER UPDATE ON user_prompts BEGIN
    INSERT INTO prompts_fts(prompts_fts, rowid, content, project)
    VALUES ('delete', old.id, old.content, old.project);
    INSERT INTO prompts_fts(rowid, content, project)
    VALUES (new.id, new.content, new.project);
END;
`

// legacyDDLPostMemoryConflictAudit is a snapshot of the full database schema
// as it exists AFTER the memory-conflict-surfacing-cloud-sync change (Phase 2)
// was applied and BEFORE the memory-conflict-audit change (Phase 3) is applied.
//
// This is the v_(N+2) baseline. Phase 3 migration tests seed a DB with this
// DDL and then call migrate() to assert that idx_memrel_status_created is added.
//
// Delta from legacyDDLPostMemoryConflictSurfacing (Phase 1 baseline):
//   - sync_apply_deferred table (Phase 2 addition)
//   - idx_sad_status_seen index on sync_apply_deferred (Phase 2 addition)
//
// Rules:
//   - NEVER modify this constant after it is committed.
//   - idx_memrel_status_created is intentionally absent — Phase B will add it.
const legacyDDLPostMemoryConflictAudit = `
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    project    TEXT NOT NULL,
    directory  TEXT NOT NULL,
    started_at TEXT NOT NULL DEFAULT (datetime('now')),
    ended_at   TEXT,
    summary    TEXT
);

CREATE TABLE IF NOT EXISTS observations (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    sync_id              TEXT,
    session_id           TEXT    NOT NULL,
    type                 TEXT    NOT NULL,
    title                TEXT    NOT NULL,
    content              TEXT    NOT NULL,
    tool_name            TEXT,
    project              TEXT,
    scope                TEXT    NOT NULL DEFAULT 'project',
    topic_key            TEXT,
    normalized_hash      TEXT,
    revision_count       INTEGER NOT NULL DEFAULT 1,
    duplicate_count      INTEGER NOT NULL DEFAULT 1,
    last_seen_at         TEXT,
    created_at           TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at           TEXT    NOT NULL DEFAULT (datetime('now')),
    deleted_at           TEXT,
    review_after         TEXT,
    expires_at           TEXT,
    embedding            BLOB,
    embedding_model      TEXT,
    embedding_created_at TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE INDEX IF NOT EXISTS idx_obs_session  ON observations(session_id);
CREATE INDEX IF NOT EXISTS idx_obs_type     ON observations(type);
CREATE INDEX IF NOT EXISTS idx_obs_project  ON observations(project);
CREATE INDEX IF NOT EXISTS idx_obs_created  ON observations(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_obs_scope    ON observations(scope);
CREATE INDEX IF NOT EXISTS idx_obs_sync_id  ON observations(sync_id);
CREATE INDEX IF NOT EXISTS idx_obs_topic    ON observations(topic_key, project, scope, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_obs_deleted  ON observations(deleted_at);
CREATE INDEX IF NOT EXISTS idx_obs_dedupe   ON observations(normalized_hash, project, scope, type, title, created_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS observations_fts USING fts5(
    title,
    content,
    tool_name,
    type,
    project,
    topic_key,
    content='observations',
    content_rowid='id'
);

CREATE TABLE IF NOT EXISTS user_prompts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    sync_id    TEXT,
    session_id TEXT    NOT NULL,
    content    TEXT    NOT NULL,
    project    TEXT,
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE TABLE IF NOT EXISTS prompt_tombstones (
    sync_id    TEXT PRIMARY KEY,
    session_id TEXT,
    project    TEXT,
    deleted_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_prompts_session    ON user_prompts(session_id);
CREATE INDEX IF NOT EXISTS idx_prompts_project    ON user_prompts(project);
CREATE INDEX IF NOT EXISTS idx_prompts_created    ON user_prompts(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_prompts_sync_id    ON user_prompts(sync_id);
CREATE INDEX IF NOT EXISTS idx_prompt_tombstones_project ON prompt_tombstones(project, deleted_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS prompts_fts USING fts5(
    content,
    project,
    content='user_prompts',
    content_rowid='id'
);

CREATE TABLE IF NOT EXISTS sync_chunks (
    target_key  TEXT NOT NULL DEFAULT 'local',
    chunk_id    TEXT NOT NULL,
    imported_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (target_key, chunk_id)
);

CREATE TABLE IF NOT EXISTS sync_state (
    target_key           TEXT PRIMARY KEY,
    lifecycle            TEXT NOT NULL DEFAULT 'idle',
    last_enqueued_seq    INTEGER NOT NULL DEFAULT 0,
    last_acked_seq       INTEGER NOT NULL DEFAULT 0,
    last_pulled_seq      INTEGER NOT NULL DEFAULT 0,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    backoff_until        TEXT,
    lease_owner          TEXT,
    lease_until          TEXT,
    last_error           TEXT,
    reason_code          TEXT,
    reason_message       TEXT,
    updated_at           TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS sync_mutations (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    target_key  TEXT NOT NULL,
    entity      TEXT NOT NULL,
    entity_key  TEXT NOT NULL,
    op          TEXT NOT NULL,
    payload     TEXT NOT NULL,
    source      TEXT NOT NULL DEFAULT 'local',
    project     TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
    acked_at    TEXT,
    FOREIGN KEY (target_key) REFERENCES sync_state(target_key)
);

CREATE INDEX IF NOT EXISTS idx_sync_mutations_target_seq ON sync_mutations(target_key, seq);
CREATE INDEX IF NOT EXISTS idx_sync_mutations_pending    ON sync_mutations(target_key, acked_at, seq);
CREATE INDEX IF NOT EXISTS idx_sync_mutations_project    ON sync_mutations(project);
CREATE INDEX IF NOT EXISTS idx_sync_mutations_lookup     ON sync_mutations(target_key, entity, entity_key, source);

CREATE TABLE IF NOT EXISTS sync_enrolled_projects (
    project     TEXT PRIMARY KEY,
    enrolled_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS cloud_upgrade_state (
    project            TEXT PRIMARY KEY,
    stage              TEXT NOT NULL DEFAULT 'planned',
    repair_class       TEXT NOT NULL DEFAULT 'none',
    snapshot_json      TEXT NOT NULL DEFAULT '{}',
    last_error_code    TEXT,
    last_error_message TEXT,
    findings_json      TEXT,
    applied_actions    TEXT,
    updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_cloud_upgrade_state_stage ON cloud_upgrade_state(stage);

CREATE TABLE IF NOT EXISTS memory_relations (
    id                        INTEGER PRIMARY KEY AUTOINCREMENT,
    sync_id                   TEXT    NOT NULL UNIQUE,
    source_id                 TEXT,
    target_id                 TEXT,
    relation                  TEXT    NOT NULL DEFAULT 'pending',
    reason                    TEXT,
    evidence                  TEXT,
    confidence                REAL,
    judgment_status           TEXT    NOT NULL DEFAULT 'pending',
    marked_by_actor           TEXT,
    marked_by_kind            TEXT,
    marked_by_model           TEXT,
    session_id                TEXT,
    superseded_at             TEXT,
    superseded_by_relation_id INTEGER REFERENCES memory_relations(id) ON DELETE SET NULL,
    created_at                TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at                TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_memrel_source    ON memory_relations(source_id, judgment_status);
CREATE INDEX IF NOT EXISTS idx_memrel_target    ON memory_relations(target_id, judgment_status);
CREATE INDEX IF NOT EXISTS idx_memrel_supersede ON memory_relations(superseded_by_relation_id);

CREATE TABLE IF NOT EXISTS sync_apply_deferred (
    sync_id           TEXT    PRIMARY KEY,
    entity            TEXT    NOT NULL,
    payload           TEXT    NOT NULL,
    apply_status      TEXT    NOT NULL DEFAULT 'deferred',
    retry_count       INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    last_attempted_at TEXT,
    first_seen_at     TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_sad_status_seen ON sync_apply_deferred(apply_status, first_seen_at);

INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at)
VALUES ('cloud', 'idle', datetime('now'));

CREATE TRIGGER obs_fts_insert AFTER INSERT ON observations BEGIN
    INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
    VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project, new.topic_key);
END;

CREATE TRIGGER obs_fts_delete AFTER DELETE ON observations BEGIN
    INSERT INTO observations_fts(observations_fts, rowid, title, content, tool_name, type, project, topic_key)
    VALUES ('delete', old.id, old.title, old.content, old.tool_name, old.type, old.project, old.topic_key);
END;

CREATE TRIGGER obs_fts_update AFTER UPDATE ON observations BEGIN
    INSERT INTO observations_fts(observations_fts, rowid, title, content, tool_name, type, project, topic_key)
    VALUES ('delete', old.id, old.title, old.content, old.tool_name, old.type, old.project, old.topic_key);
    INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
    VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project, new.topic_key);
END;

CREATE TRIGGER prompt_fts_insert AFTER INSERT ON user_prompts BEGIN
    INSERT INTO prompts_fts(rowid, content, project)
    VALUES (new.id, new.content, new.project);
END;

CREATE TRIGGER prompt_fts_delete AFTER DELETE ON user_prompts BEGIN
    INSERT INTO prompts_fts(prompts_fts, rowid, content, project)
    VALUES ('delete', old.id, old.content, old.project);
END;

CREATE TRIGGER prompt_fts_update AFTER UPDATE ON user_prompts BEGIN
    INSERT INTO prompts_fts(prompts_fts, rowid, content, project)
    VALUES ('delete', old.id, old.content, old.project);
    INSERT INTO prompts_fts(rowid, content, project)
    VALUES (new.id, new.content, new.project);
END;
`

// legacyDDLPreMemoryConflictSurfacing is a snapshot of the observations table
// DDL (plus supporting tables, indexes, and FTS triggers) as it existed BEFORE
// the memory-conflict-surfacing change was applied.
//
// Rules:
//   - NEVER modify this constant after it is committed.
//   - When the next schema change ships, add a NEW constant with its own name.
//   - This constant exists so migration tests can spin up a v_N database and
//     verify that migrate() moves it cleanly to v_N+1.
const legacyDDLPreMemoryConflictSurfacing = `
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    project    TEXT NOT NULL,
    directory  TEXT NOT NULL,
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
    created_at      TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT    NOT NULL DEFAULT (datetime('now')),
    deleted_at      TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE INDEX IF NOT EXISTS idx_obs_session  ON observations(session_id);
CREATE INDEX IF NOT EXISTS idx_obs_type     ON observations(type);
CREATE INDEX IF NOT EXISTS idx_obs_project  ON observations(project);
CREATE INDEX IF NOT EXISTS idx_obs_created  ON observations(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_obs_scope    ON observations(scope);
CREATE INDEX IF NOT EXISTS idx_obs_sync_id  ON observations(sync_id);
CREATE INDEX IF NOT EXISTS idx_obs_topic    ON observations(topic_key, project, scope, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_obs_deleted  ON observations(deleted_at);
CREATE INDEX IF NOT EXISTS idx_obs_dedupe   ON observations(normalized_hash, project, scope, type, title, created_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS observations_fts USING fts5(
    title,
    content,
    tool_name,
    type,
    project,
    topic_key,
    content='observations',
    content_rowid='id'
);

CREATE TABLE IF NOT EXISTS user_prompts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    sync_id    TEXT,
    session_id TEXT    NOT NULL,
    content    TEXT    NOT NULL,
    project    TEXT,
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE TABLE IF NOT EXISTS prompt_tombstones (
    sync_id    TEXT PRIMARY KEY,
    session_id TEXT,
    project    TEXT,
    deleted_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_prompts_session    ON user_prompts(session_id);
CREATE INDEX IF NOT EXISTS idx_prompts_project    ON user_prompts(project);
CREATE INDEX IF NOT EXISTS idx_prompts_created    ON user_prompts(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_prompts_sync_id    ON user_prompts(sync_id);
CREATE INDEX IF NOT EXISTS idx_prompt_tombstones_project ON prompt_tombstones(project, deleted_at DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS prompts_fts USING fts5(
    content,
    project,
    content='user_prompts',
    content_rowid='id'
);

CREATE TABLE IF NOT EXISTS sync_chunks (
    target_key  TEXT NOT NULL DEFAULT 'local',
    chunk_id    TEXT NOT NULL,
    imported_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (target_key, chunk_id)
);

CREATE TABLE IF NOT EXISTS sync_state (
    target_key           TEXT PRIMARY KEY,
    lifecycle            TEXT NOT NULL DEFAULT 'idle',
    last_enqueued_seq    INTEGER NOT NULL DEFAULT 0,
    last_acked_seq       INTEGER NOT NULL DEFAULT 0,
    last_pulled_seq      INTEGER NOT NULL DEFAULT 0,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    backoff_until        TEXT,
    lease_owner          TEXT,
    lease_until          TEXT,
    last_error           TEXT,
    reason_code          TEXT,
    reason_message       TEXT,
    updated_at           TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS sync_mutations (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    target_key  TEXT NOT NULL,
    entity      TEXT NOT NULL,
    entity_key  TEXT NOT NULL,
    op          TEXT NOT NULL,
    payload     TEXT NOT NULL,
    source      TEXT NOT NULL DEFAULT 'local',
    project     TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
    acked_at    TEXT,
    FOREIGN KEY (target_key) REFERENCES sync_state(target_key)
);

CREATE INDEX IF NOT EXISTS idx_sync_mutations_target_seq ON sync_mutations(target_key, seq);
CREATE INDEX IF NOT EXISTS idx_sync_mutations_pending    ON sync_mutations(target_key, acked_at, seq);
CREATE INDEX IF NOT EXISTS idx_sync_mutations_project    ON sync_mutations(project);
CREATE INDEX IF NOT EXISTS idx_sync_mutations_lookup     ON sync_mutations(target_key, entity, entity_key, source);

CREATE TABLE IF NOT EXISTS sync_enrolled_projects (
    project     TEXT PRIMARY KEY,
    enrolled_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS cloud_upgrade_state (
    project            TEXT PRIMARY KEY,
    stage              TEXT NOT NULL DEFAULT 'planned',
    repair_class       TEXT NOT NULL DEFAULT 'none',
    snapshot_json      TEXT NOT NULL DEFAULT '{}',
    last_error_code    TEXT,
    last_error_message TEXT,
    findings_json      TEXT,
    applied_actions    TEXT,
    updated_at         TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_cloud_upgrade_state_stage ON cloud_upgrade_state(stage);

INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at)
VALUES ('cloud', 'idle', datetime('now'));

CREATE TRIGGER obs_fts_insert AFTER INSERT ON observations BEGIN
    INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
    VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project, new.topic_key);
END;

CREATE TRIGGER obs_fts_delete AFTER DELETE ON observations BEGIN
    INSERT INTO observations_fts(observations_fts, rowid, title, content, tool_name, type, project, topic_key)
    VALUES ('delete', old.id, old.title, old.content, old.tool_name, old.type, old.project, old.topic_key);
END;

CREATE TRIGGER obs_fts_update AFTER UPDATE ON observations BEGIN
    INSERT INTO observations_fts(observations_fts, rowid, title, content, tool_name, type, project, topic_key)
    VALUES ('delete', old.id, old.title, old.content, old.tool_name, old.type, old.project, old.topic_key);
    INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
    VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project, new.topic_key);
END;

CREATE TRIGGER prompt_fts_insert AFTER INSERT ON user_prompts BEGIN
    INSERT INTO prompts_fts(rowid, content, project)
    VALUES (new.id, new.content, new.project);
END;

CREATE TRIGGER prompt_fts_delete AFTER DELETE ON user_prompts BEGIN
    INSERT INTO prompts_fts(prompts_fts, rowid, content, project)
    VALUES ('delete', old.id, old.content, old.project);
END;

CREATE TRIGGER prompt_fts_update AFTER UPDATE ON user_prompts BEGIN
    INSERT INTO prompts_fts(prompts_fts, rowid, content, project)
    VALUES ('delete', old.id, old.content, old.project);
    INSERT INTO prompts_fts(rowid, content, project)
    VALUES (new.id, new.content, new.project);
END;
`
