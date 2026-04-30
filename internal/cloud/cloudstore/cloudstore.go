package cloudstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	"github.com/Gentleman-Programming/engram/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/internal/store"
	engramsync "github.com/Gentleman-Programming/engram/internal/sync"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type CloudStore struct {
	db                     *sql.DB
	dashboardAllowedScopes map[string]struct{}
	dashboardReadModelMu   sync.RWMutex
	dashboardReadModel     dashboardReadModel
	dashboardReadModelOK   bool
	dashboardReadModelLoad func() (dashboardReadModel, error)
}

var ErrChunkNotFound = errors.New("cloudstore: chunk not found")
var ErrChunkConflict = errors.New("cloudstore: chunk id conflict")

func New(cfg cloud.Config) (*CloudStore, error) {
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		return nil, fmt.Errorf("cloudstore: database dsn is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: open postgres: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cloudstore: ping postgres: %w", err)
	}
	store := &CloudStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (cs *CloudStore) Close() error {
	if cs == nil || cs.db == nil {
		return nil
	}
	return cs.db.Close()
}

func (cs *CloudStore) SetDashboardAllowedProjects(projects []string) {
	if cs == nil {
		return
	}
	cs.dashboardAllowedScopes = make(map[string]struct{})
	for _, project := range projects {
		project = strings.TrimSpace(project)
		if project == "" {
			continue
		}
		cs.dashboardAllowedScopes[project] = struct{}{}
	}
	cs.invalidateDashboardReadModel()
}

type User struct {
	ID           string
	Username     string
	Email        string
	PasswordHash string
}

func (cs *CloudStore) CreateUser(username, email, _ string) (*User, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		INSERT INTO cloud_users (username, email, password_hash)
		VALUES ($1, $2, '')
		ON CONFLICT (username) DO UPDATE SET email = EXCLUDED.email
		RETURNING id::text, username, email, password_hash`
	var u User
	if err := cs.db.QueryRowContext(context.Background(), q, strings.TrimSpace(username), strings.TrimSpace(email)).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash); err != nil {
		return nil, fmt.Errorf("cloudstore: create user: %w", err)
	}
	return &u, nil
}

func (cs *CloudStore) GetUserByUsername(username string) (*User, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT id::text, username, email, password_hash FROM cloud_users WHERE username = $1`
	var u User
	err := cs.db.QueryRowContext(context.Background(), q, strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: lookup user by username: %w", err)
	}
	return &u, nil
}

func (cs *CloudStore) GetUserByEmail(email string) (*User, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT id::text, username, email, password_hash FROM cloud_users WHERE email = $1`
	var u User
	err := cs.db.QueryRowContext(context.Background(), q, strings.TrimSpace(email)).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: lookup user by email: %w", err)
	}
	return &u, nil
}

func (cs *CloudStore) ReadManifest(ctx context.Context, project string) (*engramsync.Manifest, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("cloudstore: project is required")
	}
	rows, err := cs.db.QueryContext(ctx, `
		SELECT chunk_id, created_by, COALESCE(client_created_at, created_at) AS manifest_created_at, sessions_count, observations_count, prompts_count, created_at
		FROM cloud_chunks
		WHERE project_name = $1
		ORDER BY created_at ASC, chunk_id ASC`, project)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: query manifest: %w", err)
	}
	defer rows.Close()

	manifestRows := make([]manifestRow, 0)
	for rows.Next() {
		var row manifestRow
		if err := rows.Scan(&row.chunkID, &row.createdBy, &row.manifestTime, &row.sessions, &row.observations, &row.prompts, &row.serverCreated); err != nil {
			return nil, fmt.Errorf("cloudstore: scan manifest: %w", err)
		}
		manifestRows = append(manifestRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate manifest: %w", err)
	}
	return &engramsync.Manifest{Version: 1, Chunks: toManifestEntries(manifestRows)}, nil
}

type manifestRow struct {
	chunkID       string
	createdBy     string
	manifestTime  time.Time
	sessions      int
	observations  int
	prompts       int
	serverCreated time.Time
}

func toManifestEntries(rows []manifestRow) []engramsync.ChunkEntry {
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if !left.serverCreated.Equal(right.serverCreated) {
			return left.serverCreated.Before(right.serverCreated)
		}
		return left.chunkID < right.chunkID
	})
	entries := make([]engramsync.ChunkEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, engramsync.ChunkEntry{
			ID:        row.chunkID,
			CreatedBy: row.createdBy,
			CreatedAt: row.manifestTime.UTC().Format(time.RFC3339),
			Sessions:  row.sessions,
			Memories:  row.observations,
			Prompts:   row.prompts,
		})
	}
	return entries
}

func (cs *CloudStore) WriteChunk(ctx context.Context, project, chunkID, createdBy, clientCreatedAt string, payload []byte) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("cloudstore: project is required")
	}
	if strings.TrimSpace(chunkID) == "" {
		return fmt.Errorf("cloudstore: chunk id is required")
	}
	expectedChunkID := chunkIDFromPayload(payload)
	if chunkID != expectedChunkID {
		return fmt.Errorf("cloudstore: chunk id does not match payload hash (expected %s)", expectedChunkID)
	}
	originCreatedAt, err := parseClientCreatedAt(clientCreatedAt)
	if err != nil {
		return err
	}

	var existingPayload []byte
	err = cs.db.QueryRowContext(ctx, `SELECT payload::text FROM cloud_chunks WHERE project_name = $1 AND chunk_id = $2`, project, chunkID).Scan(&existingPayload)
	if err == nil {
		normalizedIncoming := normalizeJSON(payload)
		normalizedExisting := normalizeJSON(existingPayload)
		if string(normalizedIncoming) != string(normalizedExisting) {
			return fmt.Errorf("%w: existing chunk %q has different payload", ErrChunkConflict, chunkID)
		}
		_ = cs.indexChunkSessions(ctx, project, payload)
		cs.invalidateDashboardReadModel()
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("cloudstore: read existing chunk: %w", err)
	}

	chunk, err := parseChunkData(payload)
	if err != nil {
		return fmt.Errorf("cloudstore: parse chunk for materialization: %w", err)
	}
	mutations, err := materializedChunkMutations(project, chunk)
	if err != nil {
		return err
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: begin write chunk tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	counts := summarizeChunk(payload)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO cloud_chunks (project_name, chunk_id, created_by, client_created_at, payload, sessions_count, observations_count, prompts_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		project, strings.TrimSpace(chunkID), strings.TrimSpace(createdBy), originCreatedAt, payload, counts.sessions, counts.observations, counts.prompts)
	if err != nil {
		if isUniqueViolation(err) {
			conflictErr := cs.resolveChunkConflict(ctx, project, chunkID, payload)
			if conflictErr != nil {
				return conflictErr
			}
			cs.invalidateDashboardReadModel()
			return nil
		}
		return fmt.Errorf("cloudstore: write chunk: %w", err)
	}
	if err := cs.indexChunkSessionsWith(ctx, tx, project, payload); err != nil {
		return err
	}
	if err := insertMaterializedMutations(ctx, tx, mutations); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: commit write chunk: %w", err)
	}
	tx = nil
	cs.invalidateDashboardReadModel()
	return nil
}

func (cs *CloudStore) invalidateDashboardReadModel() {
	if cs == nil {
		return
	}
	cs.dashboardReadModelMu.Lock()
	defer cs.dashboardReadModelMu.Unlock()
	cs.dashboardReadModel = dashboardReadModel{}
	cs.dashboardReadModelOK = false
}

func (cs *CloudStore) KnownSessionIDs(ctx context.Context, project string) (map[string]struct{}, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("cloudstore: project is required")
	}
	rows, err := cs.db.QueryContext(ctx, `SELECT session_id FROM cloud_project_sessions WHERE project_name = $1`, project)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: query session index: %w", err)
	}
	defer rows.Close()

	known := make(map[string]struct{})
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, fmt.Errorf("cloudstore: scan session index: %w", err)
		}
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		known[sessionID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate session index: %w", err)
	}
	return known, nil
}

func (cs *CloudStore) indexChunkSessions(ctx context.Context, project string, payload []byte) error {
	return cs.indexChunkSessionsWith(ctx, cs.db, project, payload)
}

type chunkSessionIndexer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (cs *CloudStore) indexChunkSessionsWith(ctx context.Context, execer chunkSessionIndexer, project string, payload []byte) error {
	sessionIDs := collectSessionIDsFromPayload(payload)
	if len(sessionIDs) == 0 {
		return nil
	}
	for sessionID := range sessionIDs {
		if _, err := execer.ExecContext(ctx,
			`INSERT INTO cloud_project_sessions (project_name, session_id) VALUES ($1, $2) ON CONFLICT (project_name, session_id) DO NOTHING`,
			project, sessionID,
		); err != nil {
			return fmt.Errorf("cloudstore: index session %q: %w", sessionID, err)
		}
	}
	return nil
}

func materializedChunkMutations(project string, chunk engramsync.ChunkData) ([]MutationEntry, error) {
	project = strings.TrimSpace(project)
	entries := make([]MutationEntry, 0, len(chunk.Sessions)+len(chunk.Observations)+len(chunk.Prompts))

	for i, session := range chunk.Sessions {
		entityKey := strings.TrimSpace(session.ID)
		if entityKey == "" {
			return nil, fmt.Errorf("cloudstore: materialize chunk: sessions[%d].id is required", i)
		}
		payload, err := json.Marshal(session)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: materialize chunk session %q: %w", entityKey, err)
		}
		entries = append(entries, MutationEntry{Project: project, Entity: store.SyncEntitySession, EntityKey: entityKey, Op: store.SyncOpUpsert, Payload: payload})
	}

	for i, observation := range chunk.Observations {
		entityKey := strings.TrimSpace(observation.SyncID)
		if entityKey == "" {
			return nil, fmt.Errorf("cloudstore: materialize chunk: observations[%d].sync_id is required", i)
		}
		payload, err := json.Marshal(observation)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: materialize chunk observation %q: %w", entityKey, err)
		}
		entries = append(entries, MutationEntry{Project: project, Entity: store.SyncEntityObservation, EntityKey: entityKey, Op: store.SyncOpUpsert, Payload: payload})
	}

	for i, prompt := range chunk.Prompts {
		entityKey := strings.TrimSpace(prompt.SyncID)
		if entityKey == "" {
			return nil, fmt.Errorf("cloudstore: materialize chunk: prompts[%d].sync_id is required", i)
		}
		payload, err := json.Marshal(prompt)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: materialize chunk prompt %q: %w", entityKey, err)
		}
		entries = append(entries, MutationEntry{Project: project, Entity: store.SyncEntityPrompt, EntityKey: entityKey, Op: store.SyncOpUpsert, Payload: payload})
	}

	return entries, nil
}

func insertMaterializedMutations(ctx context.Context, tx *sql.Tx, entries []MutationEntry) error {
	for _, entry := range entries {
		payload := entry.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cloud_mutations (project, entity, entity_key, op, payload)
			VALUES ($1, $2, $3, $4, $5)`,
			strings.TrimSpace(entry.Project), strings.TrimSpace(entry.Entity), strings.TrimSpace(entry.EntityKey), strings.TrimSpace(entry.Op), payload,
		)
		if err != nil {
			return fmt.Errorf("cloudstore: insert materialized chunk mutation %s/%s/%s: %w", entry.Project, entry.Entity, entry.EntityKey, err)
		}
	}
	return nil
}

func (cs *CloudStore) backfillProjectSessionsFromChunks(ctx context.Context) error {
	rows, err := cs.db.QueryContext(ctx, `SELECT project_name, payload FROM cloud_chunks`)
	if err != nil {
		return fmt.Errorf("cloudstore: backfill session index: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var project string
		var payload []byte
		if err := rows.Scan(&project, &payload); err != nil {
			return fmt.Errorf("cloudstore: backfill session index scan: %w", err)
		}
		if err := cs.indexChunkSessions(ctx, project, payload); err != nil {
			return fmt.Errorf("cloudstore: backfill session index row: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cloudstore: backfill session index iterate: %w", err)
	}
	return nil
}

func collectSessionIDsFromPayload(payload []byte) map[string]struct{} {
	chunk, err := parseChunkData(payload)
	if err != nil {
		return map[string]struct{}{}
	}
	return collectSessionIDs(chunk)
}

func parseChunkData(payload []byte) (engramsync.ChunkData, error) {
	var chunk engramsync.ChunkData
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return engramsync.ChunkData{}, err
	}
	return chunk, nil
}

func collectSessionIDs(chunk engramsync.ChunkData) map[string]struct{} {
	sessionIDs := make(map[string]struct{})
	for _, session := range chunk.Sessions {
		sessionID := strings.TrimSpace(session.ID)
		if sessionID != "" {
			sessionIDs[sessionID] = struct{}{}
		}
	}
	for _, mutation := range chunk.Mutations {
		if mutation.Entity != "session" || mutation.Op == "delete" {
			continue
		}
		mutationPayload := strings.TrimSpace(mutation.Payload)
		if mutationPayload == "" {
			continue
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := chunkcodec.DecodeSyncMutationPayload(mutationPayload, &body); err != nil {
			continue
		}
		sessionID := strings.TrimSpace(body.ID)
		if sessionID != "" {
			sessionIDs[sessionID] = struct{}{}
		}
	}
	return sessionIDs
}

func (cs *CloudStore) resolveChunkConflict(ctx context.Context, project, chunkID string, payload []byte) error {
	var existingPayload []byte
	err := cs.db.QueryRowContext(ctx, `SELECT payload::text FROM cloud_chunks WHERE project_name = $1 AND chunk_id = $2`, project, chunkID).Scan(&existingPayload)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: existing chunk %q was concurrently inserted", ErrChunkConflict, chunkID)
	}
	if err != nil {
		return fmt.Errorf("cloudstore: resolve chunk conflict: %w", err)
	}
	normalizedIncoming := normalizeJSON(payload)
	normalizedExisting := normalizeJSON(existingPayload)
	if string(normalizedIncoming) == string(normalizedExisting) {
		return nil
	}
	return fmt.Errorf("%w: existing chunk %q has different payload", ErrChunkConflict, chunkID)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505"
}

func (cs *CloudStore) ReadChunk(ctx context.Context, project, chunkID string) ([]byte, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("cloudstore: project is required")
	}
	var payload []byte
	err := cs.db.QueryRowContext(ctx, `SELECT payload FROM cloud_chunks WHERE project_name = $1 AND chunk_id = $2`, project, strings.TrimSpace(chunkID)).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q", ErrChunkNotFound, chunkID)
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: read chunk: %w", err)
	}
	return payload, nil
}

func (cs *CloudStore) migrate(ctx context.Context) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS cloud_users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT UNIQUE NOT NULL,
			email TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS cloud_chunks (
			project_name TEXT NOT NULL DEFAULT 'default',
			chunk_id TEXT NOT NULL,
			created_by TEXT NOT NULL,
			client_created_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			payload JSONB NOT NULL,
			sessions_count INTEGER NOT NULL DEFAULT 0,
			observations_count INTEGER NOT NULL DEFAULT 0,
			prompts_count INTEGER NOT NULL DEFAULT 0
		)`,
		`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS project_name TEXT`,
		`ALTER TABLE cloud_chunks ADD COLUMN IF NOT EXISTS client_created_at TIMESTAMPTZ`,
		`UPDATE cloud_chunks SET project_name = 'default' WHERE project_name IS NULL OR btrim(project_name) = ''`,
		`ALTER TABLE cloud_chunks ALTER COLUMN project_name SET NOT NULL`,
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conname = 'cloud_chunks_pkey' AND conrelid = 'cloud_chunks'::regclass
			) THEN
				ALTER TABLE cloud_chunks DROP CONSTRAINT cloud_chunks_pkey;
			END IF;
		END $$`,
		`CREATE UNIQUE INDEX IF NOT EXISTS cloud_chunks_project_chunk_uidx ON cloud_chunks (project_name, chunk_id)`,
		`CREATE TABLE IF NOT EXISTS cloud_project_sessions (
			project_name TEXT NOT NULL,
			session_id TEXT NOT NULL,
			indexed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (project_name, session_id)
		)`,
		`INSERT INTO cloud_project_sessions (project_name, session_id)
		 SELECT c.project_name, btrim(elem->>'id')
		 FROM cloud_chunks c,
		      jsonb_array_elements(COALESCE(c.payload->'sessions', '[]'::jsonb)) AS elem
		 WHERE btrim(COALESCE(elem->>'id', '')) <> ''
		 ON CONFLICT (project_name, session_id) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS cloud_project_controls (
		    project       TEXT PRIMARY KEY,
		    sync_enabled  BOOLEAN NOT NULL DEFAULT TRUE,
		    paused_reason TEXT,
		    updated_by    TEXT,
		    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cloud_project_controls_enabled ON cloud_project_controls(sync_enabled)`,
		// cloud_mutations: journal for fine-grained mutation sync (REQ-200, REQ-201).
		`CREATE TABLE IF NOT EXISTS cloud_mutations (
			seq        BIGSERIAL PRIMARY KEY,
			project    TEXT NOT NULL,
			entity     TEXT NOT NULL,
			entity_key TEXT NOT NULL,
			op         TEXT NOT NULL,
			payload    JSONB NOT NULL DEFAULT '{}',
			occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cloud_mutations_project ON cloud_mutations(project)`,
		`CREATE INDEX IF NOT EXISTS idx_cloud_mutations_seq ON cloud_mutations(seq)`,
		// cloud_sync_audit_log: persistent audit trail for push-rejection events (REQ-400).
		`CREATE TABLE IF NOT EXISTS cloud_sync_audit_log (
			id           SERIAL PRIMARY KEY,
			occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			contributor  TEXT NOT NULL,
			project      TEXT NOT NULL,
			action       TEXT NOT NULL,
			outcome      TEXT NOT NULL,
			entry_count  INT NOT NULL DEFAULT 0,
			reason_code  TEXT,
			metadata     JSONB
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_log_occurred_at ON cloud_sync_audit_log (occurred_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_log_contributor_project ON cloud_sync_audit_log (contributor, project)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_log_outcome ON cloud_sync_audit_log (outcome)`,
	}
	for _, q := range queries {
		if _, err := cs.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("cloudstore: migrate: %w", err)
		}
	}
	if err := cs.backfillProjectSessionsFromChunks(ctx); err != nil {
		return err
	}
	return nil
}

// ─── Mutation Journal Queries ─────────────────────────────────────────────────

// MutationEntry mirrors cloudserver.MutationEntry to avoid a circular import.
type MutationEntry struct {
	Project   string          `json:"project"`
	Entity    string          `json:"entity"`
	EntityKey string          `json:"entity_key"`
	Op        string          `json:"op"`
	Payload   json.RawMessage `json:"payload"`
}

// StoredMutation mirrors cloudserver.StoredMutation to avoid a circular import.
type StoredMutation struct {
	Seq        int64           `json:"seq"`
	Project    string          `json:"project"`
	Entity     string          `json:"entity"`
	EntityKey  string          `json:"entity_key"`
	Op         string          `json:"op"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt string          `json:"occurred_at"`
}

// InsertMutationBatch inserts a batch of mutations into the cloud_mutations journal.
// Returns the sequence numbers assigned to each entry.
// BW3: The entire batch is wrapped in a transaction — partial failures roll back
// all prior entries so the client can retry the full batch without creating duplicates.
func (cs *CloudStore) InsertMutationBatch(ctx context.Context, batch []MutationEntry) ([]int64, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	if len(batch) == 0 {
		return []int64{}, nil
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: begin mutation batch tx: %w", err)
	}
	// Ensure rollback on any error path.
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	seqs := make([]int64, 0, len(batch))
	for _, entry := range batch {
		project := strings.TrimSpace(entry.Project)
		entity := strings.TrimSpace(entry.Entity)
		entityKey := strings.TrimSpace(entry.EntityKey)
		op := strings.TrimSpace(entry.Op)
		payload := entry.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		var seq int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO cloud_mutations (project, entity, entity_key, op, payload)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING seq`,
			project, entity, entityKey, op, payload,
		).Scan(&seq)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: insert mutation: %w", err)
		}
		seqs = append(seqs, seq)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("cloudstore: commit mutation batch: %w", err)
	}
	tx = nil // mark committed so deferred Rollback is a no-op
	return seqs, nil
}

// ListMutationsSince returns mutations with seq > sinceSeq, filtered to allowedProjects.
// If allowedProjects is nil, no project filter is applied (returns all).
// If allowedProjects is non-nil (even empty), only those projects are returned.
// Returns (mutations, hasMore, latestSeq, error).
func (cs *CloudStore) ListMutationsSince(ctx context.Context, sinceSeq int64, limit int, allowedProjects []string) ([]StoredMutation, bool, int64, error) {
	if cs == nil || cs.db == nil {
		return nil, false, 0, fmt.Errorf("cloudstore: not initialized")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}

	// If allowedProjects is non-nil but empty, return empty result immediately.
	if allowedProjects != nil && len(allowedProjects) == 0 {
		return []StoredMutation{}, false, 0, nil
	}

	// Fetch limit+1 to detect hasMore.
	fetchLimit := limit + 1

	var rows *sql.Rows
	var err error

	if allowedProjects == nil {
		// No enrollment filter.
		rows, err = cs.db.QueryContext(ctx, `
			SELECT seq, project, entity, entity_key, op, payload::text, occurred_at
			FROM cloud_mutations
			WHERE seq > $1
			ORDER BY seq ASC
			LIMIT $2`,
			sinceSeq, fetchLimit,
		)
	} else {
		// Filter by allowed projects.
		rows, err = cs.db.QueryContext(ctx, `
			SELECT seq, project, entity, entity_key, op, payload::text, occurred_at
			FROM cloud_mutations
			WHERE seq > $1 AND project = ANY($2)
			ORDER BY seq ASC
			LIMIT $3`,
			sinceSeq, allowedProjects, fetchLimit,
		)
	}
	if err != nil {
		return nil, false, 0, fmt.Errorf("cloudstore: list mutations since %d: %w", sinceSeq, err)
	}
	defer rows.Close()

	var all []StoredMutation
	for rows.Next() {
		var m StoredMutation
		var payloadStr string
		var occurredAt time.Time
		if err := rows.Scan(&m.Seq, &m.Project, &m.Entity, &m.EntityKey, &m.Op, &payloadStr, &occurredAt); err != nil {
			return nil, false, 0, fmt.Errorf("cloudstore: scan mutation: %w", err)
		}
		m.Payload = json.RawMessage(payloadStr)
		m.OccurredAt = occurredAt.UTC().Format(time.RFC3339)
		all = append(all, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, fmt.Errorf("cloudstore: iterate mutations: %w", err)
	}

	hasMore := len(all) > limit
	if hasMore {
		all = all[:limit]
	}

	latestSeq := int64(0)
	if len(all) > 0 {
		latestSeq = all[len(all)-1].Seq
	}

	return all, hasMore, latestSeq, nil
}

func parseClientCreatedAt(value string) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: invalid client_created_at: %w", err)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func chunkIDFromPayload(payload []byte) string {
	return chunkcodec.ChunkID(payload)
}

func normalizeJSON(payload []byte) []byte {
	var body any
	if err := json.Unmarshal(payload, &body); err != nil {
		return payload
	}
	normalized, err := json.Marshal(body)
	if err != nil {
		return payload
	}
	return normalized
}

type chunkSummary struct {
	sessions     int
	observations int
	prompts      int
}

func summarizeChunk(payload []byte) chunkSummary {
	var body struct {
		Sessions     []json.RawMessage `json:"sessions"`
		Observations []json.RawMessage `json:"observations"`
		Prompts      []json.RawMessage `json:"prompts"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return chunkSummary{}
	}
	return chunkSummary{
		sessions:     len(body.Sessions),
		observations: len(body.Observations),
		prompts:      len(body.Prompts),
	}
}
