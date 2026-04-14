package cloudstore

const schemaSQL = `
-- Users
CREATE TABLE IF NOT EXISTS users (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    email      TEXT UNIQUE,
    api_key    TEXT UNIQUE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Projects
CREATE TABLE IF NOT EXISTS projects (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Project membership
CREATE TABLE IF NOT EXISTS project_members (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT 'member',
    joined_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);

-- Observations (server copy)
CREATE TABLE IF NOT EXISTS observations (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sync_id        TEXT NOT NULL,
    session_id     TEXT,
    type           TEXT NOT NULL,
    title          TEXT NOT NULL,
    content        TEXT NOT NULL,
    tool_name      TEXT,
    project        TEXT NOT NULL,
    scope          TEXT NOT NULL DEFAULT 'project',
    topic_key      TEXT,
    revision_count INTEGER NOT NULL DEFAULT 0,
    created_by     UUID NOT NULL REFERENCES users(id),
    updated_by     UUID NOT NULL REFERENCES users(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    server_seq     BIGINT,
    deleted_at     TIMESTAMPTZ,
    UNIQUE (sync_id, project)
);

-- Full-text search (simple config = language-agnostic, configurable per-server)
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'observations' AND column_name = 'search_vector'
    ) THEN
        ALTER TABLE observations ADD COLUMN search_vector tsvector
            GENERATED ALWAYS AS (to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(content,''))) STORED;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_obs_search ON observations USING GIN(search_vector);
CREATE INDEX IF NOT EXISTS idx_obs_topic_key ON observations(topic_key, project, scope) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_obs_project_scope ON observations(project, scope) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_obs_server_seq ON observations(server_seq);

-- Revision history
CREATE TABLE IF NOT EXISTS observation_revisions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    observation_id  UUID NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
    project         TEXT NOT NULL,
    title           TEXT NOT NULL,
    content         TEXT NOT NULL,
    type            TEXT NOT NULL,
    topic_key       TEXT,
    updated_by      UUID NOT NULL REFERENCES users(id),
    superseded_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    revision_number INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_revisions_obs ON observation_revisions(observation_id, revision_number);
CREATE INDEX IF NOT EXISTS idx_revisions_topic ON observation_revisions(topic_key, project) WHERE topic_key IS NOT NULL;

-- Sessions (server copy)
CREATE TABLE IF NOT EXISTS sessions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sync_id    TEXT NOT NULL,
    project    TEXT NOT NULL,
    directory  TEXT,
    user_id    UUID NOT NULL REFERENCES users(id),
    started_at TIMESTAMPTZ NOT NULL,
    ended_at   TIMESTAMPTZ,
    summary    TEXT,
    server_seq BIGINT,
    UNIQUE (sync_id, project)
);

CREATE INDEX IF NOT EXISTS idx_sessions_server_seq ON sessions(server_seq);

-- Prompts (server copy, always private per user)
CREATE TABLE IF NOT EXISTS prompts (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sync_id    TEXT NOT NULL,
    session_id TEXT,
    content    TEXT NOT NULL,
    project    TEXT NOT NULL,
    user_id    UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    server_seq BIGINT,
    UNIQUE (sync_id, project)
);

CREATE INDEX IF NOT EXISTS idx_prompts_server_seq ON prompts(server_seq);

-- Server sequence counter (per-project, monotonic, no gaps)
-- Migration: convert single-row counter to per-project if old schema exists
DO $$ BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'server_seq_counter' AND column_name = 'id'
    ) THEN
        -- Old single-row table exists — drop and recreate as per-project
        DROP TABLE server_seq_counter;
        CREATE TABLE server_seq_counter (
            project TEXT PRIMARY KEY,
            value   BIGINT NOT NULL DEFAULT 0
        );
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS server_seq_counter (
    project TEXT PRIMARY KEY,
    value   BIGINT NOT NULL DEFAULT 0
);

-- Sync cursors
CREATE TABLE IF NOT EXISTS sync_cursors (
    user_id     UUID NOT NULL REFERENCES users(id),
    device_id   TEXT NOT NULL,
    project     TEXT NOT NULL,
    last_seq    BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, device_id, project)
);

-- Idempotency keys (24h TTL, cleaned by maintenance job)
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key        TEXT PRIMARY KEY,
    response   JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_idempotency_ttl ON idempotency_keys(created_at);

-- Rate limiting
CREATE TABLE IF NOT EXISTS rate_limits (
    user_id       UUID NOT NULL REFERENCES users(id),
    endpoint      TEXT NOT NULL,
    window_start  TIMESTAMPTZ NOT NULL,
    request_count INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (user_id, endpoint, window_start)
);

CREATE INDEX IF NOT EXISTS idx_rate_limits_ttl ON rate_limits(window_start);

-- Phase 3: Add numeric_id for RemoteStore ID mapping (int64 proxy for UUID PKs)
DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'observations' AND column_name = 'numeric_id'
    ) THEN
        CREATE SEQUENCE observations_numeric_id_seq;
        ALTER TABLE observations ADD COLUMN numeric_id BIGINT DEFAULT nextval('observations_numeric_id_seq') NOT NULL;
        ALTER SEQUENCE observations_numeric_id_seq OWNED BY observations.numeric_id;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'sessions' AND column_name = 'numeric_id'
    ) THEN
        CREATE SEQUENCE sessions_numeric_id_seq;
        ALTER TABLE sessions ADD COLUMN numeric_id BIGINT DEFAULT nextval('sessions_numeric_id_seq') NOT NULL;
        ALTER SEQUENCE sessions_numeric_id_seq OWNED BY sessions.numeric_id;
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'prompts' AND column_name = 'numeric_id'
    ) THEN
        CREATE SEQUENCE prompts_numeric_id_seq;
        ALTER TABLE prompts ADD COLUMN numeric_id BIGINT DEFAULT nextval('prompts_numeric_id_seq') NOT NULL;
        ALTER SEQUENCE prompts_numeric_id_seq OWNED BY prompts.numeric_id;
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_obs_numeric_id ON observations(numeric_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_numeric_id ON sessions(numeric_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_prompts_numeric_id ON prompts(numeric_id);
`
