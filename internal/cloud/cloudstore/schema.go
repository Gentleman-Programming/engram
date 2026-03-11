package cloudstore

// schemaDDL contains all CREATE TABLE IF NOT EXISTS statements for the
// Engram Cloud Postgres schema. It is executed inside a single transaction
// during CloudStore initialization to guarantee atomicity and idempotency.
const schemaDDL = `
-- ── Users ───────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS cloud_users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username        TEXT NOT NULL UNIQUE,
    email           TEXT NOT NULL UNIQUE,
    password_hash   TEXT NOT NULL,
    api_key_hash    TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cloud_users_username    ON cloud_users(username);
CREATE INDEX IF NOT EXISTS idx_cloud_users_email       ON cloud_users(email);
CREATE INDEX IF NOT EXISTS idx_cloud_users_api_key     ON cloud_users(api_key_hash) WHERE api_key_hash IS NOT NULL;

-- ── Sessions ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS cloud_sessions (
    id          TEXT NOT NULL,
    user_id     UUID NOT NULL REFERENCES cloud_users(id) ON DELETE CASCADE,
    project     TEXT NOT NULL,
    directory   TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at    TIMESTAMPTZ,
    summary     TEXT,
    PRIMARY KEY (user_id, id)
);
CREATE INDEX IF NOT EXISTS idx_cloud_sessions_project ON cloud_sessions(user_id, project);

-- ── Observations ────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS cloud_observations (
    id              BIGSERIAL PRIMARY KEY,
    user_id         UUID NOT NULL REFERENCES cloud_users(id) ON DELETE CASCADE,
    session_id      TEXT NOT NULL,
    type            TEXT NOT NULL,
    title           TEXT NOT NULL,
    content         TEXT NOT NULL,
    tool_name       TEXT,
    project         TEXT,
    scope           TEXT NOT NULL DEFAULT 'project',
    topic_key       TEXT,
    normalized_hash TEXT,
    revision_count  INTEGER NOT NULL DEFAULT 1,
    duplicate_count INTEGER NOT NULL DEFAULT 1,
    last_seen_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ,

    -- Full-text search vector: auto-maintained via GENERATED STORED
    tsv tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(content, '')), 'B') ||
        setweight(to_tsvector('english', coalesce(type, '') || ' ' || coalesce(project, '')), 'C')
    ) STORED
);
CREATE INDEX IF NOT EXISTS idx_cloud_obs_user_session  ON cloud_observations(user_id, session_id);
CREATE INDEX IF NOT EXISTS idx_cloud_obs_user_project  ON cloud_observations(user_id, project);
CREATE INDEX IF NOT EXISTS idx_cloud_obs_user_type     ON cloud_observations(user_id, type);
CREATE INDEX IF NOT EXISTS idx_cloud_obs_user_created  ON cloud_observations(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_cloud_obs_user_topic    ON cloud_observations(user_id, topic_key, project, scope, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_cloud_obs_user_dedupe   ON cloud_observations(user_id, normalized_hash, project, scope, type, title);
CREATE INDEX IF NOT EXISTS idx_cloud_obs_user_deleted  ON cloud_observations(user_id, deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_cloud_obs_tsv           ON cloud_observations USING GIN(tsv);

-- ── Prompts ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS cloud_prompts (
    id          BIGSERIAL PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES cloud_users(id) ON DELETE CASCADE,
    session_id  TEXT NOT NULL,
    content     TEXT NOT NULL,
    project     TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Full-text search vector for prompt content
    tsv tsvector GENERATED ALWAYS AS (
        to_tsvector('english', coalesce(content, ''))
    ) STORED
);
CREATE INDEX IF NOT EXISTS idx_cloud_prompts_user_session ON cloud_prompts(user_id, session_id);
CREATE INDEX IF NOT EXISTS idx_cloud_prompts_user_project ON cloud_prompts(user_id, project);
CREATE INDEX IF NOT EXISTS idx_cloud_prompts_tsv          ON cloud_prompts USING GIN(tsv);

-- ── Project Controls (cloud-managed sync policy) ─────────────────────────
CREATE TABLE IF NOT EXISTS cloud_project_controls (
    project      TEXT PRIMARY KEY,
    sync_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    paused_reason TEXT,
    updated_by   UUID REFERENCES cloud_users(id) ON DELETE SET NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cloud_project_controls_enabled ON cloud_project_controls(sync_enabled);

-- ── Chunks (raw chunk storage for sync) ─────────────────────────────────
CREATE TABLE IF NOT EXISTS cloud_chunks (
    chunk_id    TEXT NOT NULL,
    user_id     UUID NOT NULL REFERENCES cloud_users(id) ON DELETE CASCADE,
    data        BYTEA,
    created_by  TEXT NOT NULL DEFAULT '',
    sessions    INTEGER NOT NULL DEFAULT 0,
    memories    INTEGER NOT NULL DEFAULT 0,
    prompts     INTEGER NOT NULL DEFAULT 0,
    imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, chunk_id)
);

-- ── Sync Chunks (tracking which chunks have been synced) ────────────────
CREATE TABLE IF NOT EXISTS cloud_sync_chunks (
    chunk_id    TEXT NOT NULL,
    user_id     UUID NOT NULL REFERENCES cloud_users(id) ON DELETE CASCADE,
    synced_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, chunk_id)
);

-- ── Mutation Ledger (append-only per-user mutation journal for sync) ────
CREATE TABLE IF NOT EXISTS cloud_mutations (
    seq         BIGSERIAL PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES cloud_users(id) ON DELETE CASCADE,
    entity      TEXT NOT NULL,
    entity_key  TEXT NOT NULL,
    op          TEXT NOT NULL,
    payload     JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cloud_mutations_user_seq ON cloud_mutations(user_id, seq);

-- ── Project Enrollments (tracks which users are enrolled in which projects) ──
CREATE TABLE IF NOT EXISTS cloud_project_enrollments (
    user_id     UUID NOT NULL REFERENCES cloud_users(id) ON DELETE CASCADE,
    project     TEXT NOT NULL,
    enrolled_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, project)
);
CREATE INDEX IF NOT EXISTS idx_cloud_enrollments_project ON cloud_project_enrollments(project);
CREATE INDEX IF NOT EXISTS idx_cloud_enrollments_user    ON cloud_project_enrollments(user_id);
`
