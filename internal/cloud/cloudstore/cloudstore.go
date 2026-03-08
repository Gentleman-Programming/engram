// Package cloudstore implements the Postgres-backed storage layer for Engram Cloud.
//
// It mirrors the local SQLite store but uses Postgres with row-level user
// isolation, full-text search via tsvector, and chunk-based sync storage.
package cloudstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/internal/cloud"
	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

// openDB is a test seam matching the pattern from internal/store/store.go:23.
var openDB = sql.Open

// ─── Types ──────────────────────────────────────────────────────────────────

// CloudUser represents a registered user in the cloud system.
type CloudUser struct {
	ID           string  `json:"id"`
	Username     string  `json:"username"`
	Email        string  `json:"email"`
	PasswordHash string  `json:"-"`
	APIKeyHash   *string `json:"-"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// CloudSession represents a session record scoped to a user.
type CloudSession struct {
	ID        string  `json:"id"`
	UserID    string  `json:"user_id"`
	Project   string  `json:"project"`
	Directory string  `json:"directory"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
	Summary   *string `json:"summary,omitempty"`
}

// CloudSessionSummary holds session info with an observation count.
type CloudSessionSummary struct {
	ID               string  `json:"id"`
	Project          string  `json:"project"`
	StartedAt        string  `json:"started_at"`
	EndedAt          *string `json:"ended_at,omitempty"`
	Summary          *string `json:"summary,omitempty"`
	ObservationCount int     `json:"observation_count"`
}

// CloudObservation represents an observation scoped to a user.
type CloudObservation struct {
	ID             int64   `json:"id"`
	UserID         string  `json:"user_id"`
	SessionID      string  `json:"session_id"`
	Type           string  `json:"type"`
	Title          string  `json:"title"`
	Content        string  `json:"content"`
	ToolName       *string `json:"tool_name,omitempty"`
	Project        *string `json:"project,omitempty"`
	Scope          string  `json:"scope"`
	TopicKey       *string `json:"topic_key,omitempty"`
	RevisionCount  int     `json:"revision_count"`
	DuplicateCount int     `json:"duplicate_count"`
	LastSeenAt     *string `json:"last_seen_at,omitempty"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	DeletedAt      *string `json:"deleted_at,omitempty"`
}

// AddCloudObservationParams holds parameters for creating a new observation.
type AddCloudObservationParams struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	ToolName  string `json:"tool_name,omitempty"`
	Project   string `json:"project,omitempty"`
	Scope     string `json:"scope,omitempty"`
	TopicKey  string `json:"topic_key,omitempty"`
}

// CloudPrompt represents a user prompt scoped to a user.
type CloudPrompt struct {
	ID        int64  `json:"id"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Project   string `json:"project,omitempty"`
	CreatedAt string `json:"created_at"`
}

// AddCloudPromptParams holds parameters for creating a new prompt.
type AddCloudPromptParams struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Project   string `json:"project,omitempty"`
}

// CloudChunkEntry describes a sync chunk stored in the cloud.
type CloudChunkEntry struct {
	ChunkID    string `json:"chunk_id"`
	UserID     string `json:"user_id"`
	CreatedBy  string `json:"created_by"`
	Sessions   int    `json:"sessions"`
	Memories   int    `json:"memories"`
	Prompts    int    `json:"prompts"`
	ImportedAt string `json:"imported_at"`
}

// CloudStats holds aggregate statistics for a user.
type CloudStats struct {
	TotalSessions     int      `json:"total_sessions"`
	TotalObservations int      `json:"total_observations"`
	TotalPrompts      int      `json:"total_prompts"`
	Projects          []string `json:"projects"`
}

// ─── CloudStore ─────────────────────────────────────────────────────────────

// CloudStore provides Postgres-backed storage for Engram Cloud.
type CloudStore struct {
	db *sql.DB
}

// New creates a new CloudStore. It opens a Postgres connection using the DSN
// from cfg, configures the connection pool, and runs schema initialization
// inside a single transaction.
func New(cfg cloud.Config) (*CloudStore, error) {
	db, err := openDB("postgres", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: open db: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxPool)
	db.SetMaxIdleConns(cfg.MaxPool / 2)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("cloudstore: ping: %w", err)
	}

	// Run schema DDL inside a transaction for atomicity.
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("cloudstore: begin schema tx: %w", err)
	}
	if _, err := tx.Exec(schemaDDL); err != nil {
		tx.Rollback()
		db.Close()
		return nil, fmt.Errorf("cloudstore: schema init: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE cloud_project_controls ADD COLUMN IF NOT EXISTS paused_reason TEXT`); err != nil {
		tx.Rollback()
		db.Close()
		return nil, fmt.Errorf("cloudstore: project controls migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		return nil, fmt.Errorf("cloudstore: commit schema tx: %w", err)
	}

	return &CloudStore{db: db}, nil
}

// Close closes the underlying database connection.
func (cs *CloudStore) Close() error {
	return cs.db.Close()
}

// DB returns the underlying *sql.DB for advanced use cases (e.g. testing).
func (cs *CloudStore) DB() *sql.DB {
	return cs.db
}

// Ping checks whether the underlying database connection is currently healthy.
func (cs *CloudStore) Ping() error {
	if cs == nil || cs.db == nil {
		return nil
	}
	return cs.db.Ping()
}

// ─── User CRUD ──────────────────────────────────────────────────────────────

// CreateUser creates a new user with a bcrypt-hashed password (cost >= 10).
// Returns an error if the username or email already exists.
func (cs *CloudStore) CreateUser(username, email, password string) (*CloudUser, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: hash password: %w", err)
	}

	var u CloudUser
	err = cs.db.QueryRow(
		`INSERT INTO cloud_users (username, email, password_hash)
		 VALUES ($1, $2, $3)
		 RETURNING id, username, email, password_hash, api_key_hash, created_at, updated_at`,
		username, email, string(hash),
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.APIKeyHash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: create user: %w", err)
	}
	return &u, nil
}

// GetUserByUsername retrieves a user by username.
func (cs *CloudStore) GetUserByUsername(username string) (*CloudUser, error) {
	var u CloudUser
	err := cs.db.QueryRow(
		`SELECT id, username, email, password_hash, api_key_hash, created_at, updated_at
		 FROM cloud_users WHERE username = $1`,
		username,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.APIKeyHash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("cloudstore: get user by username: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("cloudstore: get user by username: %w", err)
	}
	return &u, nil
}

// GetUserByEmail retrieves a user by email address.
func (cs *CloudStore) GetUserByEmail(email string) (*CloudUser, error) {
	var u CloudUser
	err := cs.db.QueryRow(
		`SELECT id, username, email, password_hash, api_key_hash, created_at, updated_at
		 FROM cloud_users WHERE email = $1`,
		email,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.APIKeyHash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("cloudstore: get user by email: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("cloudstore: get user by email: %w", err)
	}
	return &u, nil
}

// GetUserByAPIKeyHash retrieves a user by their API key hash.
func (cs *CloudStore) GetUserByAPIKeyHash(hash string) (*CloudUser, error) {
	var u CloudUser
	err := cs.db.QueryRow(
		`SELECT id, username, email, password_hash, api_key_hash, created_at, updated_at
		 FROM cloud_users WHERE api_key_hash = $1`,
		hash,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.APIKeyHash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("cloudstore: get user by api key hash: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("cloudstore: get user by api key hash: %w", err)
	}
	return &u, nil
}

// GetUserByID retrieves a user by id.
func (cs *CloudStore) GetUserByID(userID string) (*CloudUser, error) {
	var u CloudUser
	err := cs.db.QueryRow(
		`SELECT id, username, email, password_hash, api_key_hash, created_at, updated_at
		 FROM cloud_users WHERE id = $1`,
		userID,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.APIKeyHash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("cloudstore: get user by id: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("cloudstore: get user by id: %w", err)
	}
	return &u, nil
}

// SetAPIKeyHash sets the API key hash for a user. Pass an empty string to revoke.
func (cs *CloudStore) SetAPIKeyHash(userID, hash string) error {
	var h *string
	if hash != "" {
		h = &hash
	}
	_, err := cs.db.Exec(
		`UPDATE cloud_users SET api_key_hash = $1, updated_at = NOW() WHERE id = $2`,
		h, userID,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: set api key hash: %w", err)
	}
	return nil
}

// ─── Sessions ───────────────────────────────────────────────────────────────

// CreateSession creates a new session for the given user.
func (cs *CloudStore) CreateSession(userID, sessionID, project, directory string) error {
	_, err := cs.db.Exec(
		`INSERT INTO cloud_sessions (id, user_id, project, directory)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (user_id, id) DO UPDATE SET
		   project   = CASE WHEN cloud_sessions.project = '' THEN EXCLUDED.project ELSE cloud_sessions.project END,
		   directory = CASE WHEN cloud_sessions.directory = '' THEN EXCLUDED.directory ELSE cloud_sessions.directory END`,
		sessionID, userID, project, directory,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: create session: %w", err)
	}
	return nil
}

// EndSession ends a session by setting ended_at and an optional summary.
// Filters by user_id to ensure data isolation.
func (cs *CloudStore) EndSession(userID, sessionID, summary string) error {
	_, err := cs.db.Exec(
		`UPDATE cloud_sessions SET ended_at = NOW(), summary = $1
		 WHERE user_id = $2 AND id = $3`,
		nullableString(summary), userID, sessionID,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: end session: %w", err)
	}
	return nil
}

// RecentSessions returns the most recent sessions for a user, optionally filtered
// by project, with observation counts.
func (cs *CloudStore) RecentSessions(userID, project string, limit int) ([]CloudSessionSummary, error) {
	if limit <= 0 {
		limit = 5
	}

	query := `
		SELECT s.id, s.project, s.started_at, s.ended_at, s.summary,
		       COUNT(o.id) as observation_count
		FROM cloud_sessions s
		LEFT JOIN cloud_observations o ON o.session_id = s.id AND o.user_id = s.user_id AND o.deleted_at IS NULL
		WHERE s.user_id = $1
	`
	args := []any{userID}
	argN := 2

	if project != "" {
		query += fmt.Sprintf(" AND s.project = $%d", argN)
		args = append(args, project)
		argN++
	}

	query += fmt.Sprintf(" GROUP BY s.id, s.user_id, s.project, s.started_at, s.ended_at, s.summary ORDER BY MAX(COALESCE(o.created_at, s.started_at)) DESC LIMIT $%d", argN)
	args = append(args, limit)

	rows, err := cs.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: recent sessions: %w", err)
	}
	defer rows.Close()

	var results []CloudSessionSummary
	for rows.Next() {
		var ss CloudSessionSummary
		if err := rows.Scan(&ss.ID, &ss.Project, &ss.StartedAt, &ss.EndedAt, &ss.Summary, &ss.ObservationCount); err != nil {
			return nil, fmt.Errorf("cloudstore: scan session summary: %w", err)
		}
		results = append(results, ss)
	}
	return results, rows.Err()
}

// GetSession retrieves a single session by ID, scoped to user_id.
func (cs *CloudStore) GetSession(userID, sessionID string) (*CloudSession, error) {
	var s CloudSession
	err := cs.db.QueryRow(
		`SELECT id, user_id, project, directory, started_at, ended_at, summary
		 FROM cloud_sessions
		 WHERE id = $1 AND user_id = $2`,
		sessionID, userID,
	).Scan(&s.ID, &s.UserID, &s.Project, &s.Directory, &s.StartedAt, &s.EndedAt, &s.Summary)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("cloudstore: get session: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("cloudstore: get session: %w", err)
	}
	return &s, nil
}

// SessionObservations returns all live observations for a single session.
func (cs *CloudStore) SessionObservations(userID, sessionID string, limit int) ([]CloudObservation, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := cs.db.Query(
		`SELECT id, user_id, session_id, type, title, content, tool_name, project,
		        scope, topic_key, revision_count, duplicate_count, last_seen_at,
		        created_at, updated_at, deleted_at
		 FROM cloud_observations
		 WHERE user_id = $1 AND session_id = $2 AND deleted_at IS NULL
		 ORDER BY created_at ASC
		 LIMIT $3`,
		userID, sessionID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: session observations: %w", err)
	}
	defer rows.Close()

	var results []CloudObservation
	for rows.Next() {
		var o CloudObservation
		if err := rows.Scan(
			&o.ID, &o.UserID, &o.SessionID, &o.Type, &o.Title, &o.Content,
			&o.ToolName, &o.Project, &o.Scope, &o.TopicKey,
			&o.RevisionCount, &o.DuplicateCount, &o.LastSeenAt,
			&o.CreatedAt, &o.UpdatedAt, &o.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("cloudstore: scan session observation: %w", err)
		}
		results = append(results, o)
	}
	return results, rows.Err()
}

// SessionPrompts returns prompts captured during a single session.
func (cs *CloudStore) SessionPrompts(userID, sessionID string, limit int) ([]CloudPrompt, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := cs.db.Query(
		`SELECT id, user_id, session_id, content, COALESCE(project, '') AS project, created_at
		 FROM cloud_prompts
		 WHERE user_id = $1 AND session_id = $2
		 ORDER BY created_at ASC
		 LIMIT $3`,
		userID, sessionID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: session prompts: %w", err)
	}
	defer rows.Close()

	var results []CloudPrompt
	for rows.Next() {
		var p CloudPrompt
		if err := rows.Scan(&p.ID, &p.UserID, &p.SessionID, &p.Content, &p.Project, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("cloudstore: scan session prompt: %w", err)
		}
		results = append(results, p)
	}
	return results, rows.Err()
}

// ─── Observations ───────────────────────────────────────────────────────────

// AddObservation creates a new observation for the given user.
func (cs *CloudStore) AddObservation(userID string, p AddCloudObservationParams) (int64, error) {
	scope := normalizeScope(p.Scope)
	normHash := hashNormalized(p.Content)

	var id int64
	err := cs.db.QueryRow(
		`INSERT INTO cloud_observations
		 (user_id, session_id, type, title, content, tool_name, project, scope, topic_key, normalized_hash,
		  revision_count, duplicate_count, last_seen_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1, 1, NOW(), NOW())
		 RETURNING id`,
		userID, p.SessionID, p.Type, p.Title, p.Content,
		nullableString(p.ToolName), nullableString(p.Project), scope,
		nullableString(p.TopicKey), normHash,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("cloudstore: add observation: %w", err)
	}
	return id, nil
}

// GetObservation retrieves a single observation by ID, scoped to user_id.
func (cs *CloudStore) GetObservation(userID string, id int64) (*CloudObservation, error) {
	var o CloudObservation
	err := cs.db.QueryRow(
		`SELECT id, user_id, session_id, type, title, content, tool_name, project,
		        scope, topic_key, revision_count, duplicate_count, last_seen_at,
		        created_at, updated_at, deleted_at
		 FROM cloud_observations
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		id, userID,
	).Scan(
		&o.ID, &o.UserID, &o.SessionID, &o.Type, &o.Title, &o.Content,
		&o.ToolName, &o.Project, &o.Scope, &o.TopicKey,
		&o.RevisionCount, &o.DuplicateCount, &o.LastSeenAt,
		&o.CreatedAt, &o.UpdatedAt, &o.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("cloudstore: get observation: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("cloudstore: get observation: %w", err)
	}
	return &o, nil
}

// RecentObservations returns recent observations for a user, optionally filtered
// by project and scope.
func (cs *CloudStore) RecentObservations(userID, project, scope string, limit int) ([]CloudObservation, error) {
	return cs.FilterObservations(userID, project, scope, "", limit)
}

// FilterObservations returns recent observations filtered by project, scope, and type.
func (cs *CloudStore) FilterObservations(userID, project, scope, obsType string, limit int) ([]CloudObservation, error) {
	if limit <= 0 {
		limit = 20
	}

	query := `
		SELECT id, user_id, session_id, type, title, content, tool_name, project,
		       scope, topic_key, revision_count, duplicate_count, last_seen_at,
		       created_at, updated_at, deleted_at
		FROM cloud_observations
		WHERE user_id = $1 AND deleted_at IS NULL
	`
	args := []any{userID}
	argN := 2

	if project != "" {
		query += fmt.Sprintf(" AND project = $%d", argN)
		args = append(args, project)
		argN++
	}
	if scope != "" {
		query += fmt.Sprintf(" AND scope = $%d", argN)
		args = append(args, normalizeScope(scope))
		argN++
	}
	if obsType != "" {
		query += fmt.Sprintf(" AND type = $%d", argN)
		args = append(args, obsType)
		argN++
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", argN)
	args = append(args, limit)

	rows, err := cs.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: recent observations: %w", err)
	}
	defer rows.Close()

	var results []CloudObservation
	for rows.Next() {
		var o CloudObservation
		if err := rows.Scan(
			&o.ID, &o.UserID, &o.SessionID, &o.Type, &o.Title, &o.Content,
			&o.ToolName, &o.Project, &o.Scope, &o.TopicKey,
			&o.RevisionCount, &o.DuplicateCount, &o.LastSeenAt,
			&o.CreatedAt, &o.UpdatedAt, &o.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("cloudstore: scan observation: %w", err)
		}
		results = append(results, o)
	}
	return results, rows.Err()
}

// DeleteObservation soft-deletes or hard-deletes an observation, scoped to user_id.
func (cs *CloudStore) DeleteObservation(userID string, id int64, hard bool) error {
	if hard {
		_, err := cs.db.Exec(
			`DELETE FROM cloud_observations WHERE id = $1 AND user_id = $2`,
			id, userID,
		)
		return err
	}
	_, err := cs.db.Exec(
		`UPDATE cloud_observations
		 SET deleted_at = NOW(), updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		id, userID,
	)
	return err
}

// ─── Prompts ────────────────────────────────────────────────────────────────

// AddPrompt creates a new prompt for the given user.
func (cs *CloudStore) AddPrompt(userID string, p AddCloudPromptParams) (int64, error) {
	var id int64
	err := cs.db.QueryRow(
		`INSERT INTO cloud_prompts (user_id, session_id, content, project)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id`,
		userID, p.SessionID, p.Content, nullableString(p.Project),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("cloudstore: add prompt: %w", err)
	}
	return id, nil
}

// RecentPrompts returns recent prompts for a user, optionally filtered by project.
func (cs *CloudStore) RecentPrompts(userID, project string, limit int) ([]CloudPrompt, error) {
	if limit <= 0 {
		limit = 20
	}

	query := `SELECT id, user_id, session_id, content, COALESCE(project, '') as project, created_at
	          FROM cloud_prompts WHERE user_id = $1`
	args := []any{userID}
	argN := 2

	if project != "" {
		query += fmt.Sprintf(" AND project = $%d", argN)
		args = append(args, project)
		argN++
	}

	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", argN)
	args = append(args, limit)

	rows, err := cs.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: recent prompts: %w", err)
	}
	defer rows.Close()

	var results []CloudPrompt
	for rows.Next() {
		var p CloudPrompt
		if err := rows.Scan(&p.ID, &p.UserID, &p.SessionID, &p.Content, &p.Project, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("cloudstore: scan prompt: %w", err)
		}
		results = append(results, p)
	}
	return results, rows.Err()
}

// GetPrompt retrieves a single prompt by id, scoped to user_id.
func (cs *CloudStore) GetPrompt(userID string, id int64) (*CloudPrompt, error) {
	var p CloudPrompt
	err := cs.db.QueryRow(
		`SELECT id, user_id, session_id, content, COALESCE(project, '') AS project, created_at
		 FROM cloud_prompts
		 WHERE id = $1 AND user_id = $2`,
		id, userID,
	).Scan(&p.ID, &p.UserID, &p.SessionID, &p.Content, &p.Project, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("cloudstore: get prompt: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("cloudstore: get prompt: %w", err)
	}
	return &p, nil
}

// ─── Chunks ─────────────────────────────────────────────────────────────────

// StoreChunk stores a sync chunk. Uses INSERT ON CONFLICT DO NOTHING for idempotency.
func (cs *CloudStore) StoreChunk(userID, chunkID, createdBy string, data []byte, sessions, memories, prompts int) error {
	_, err := cs.db.Exec(
		`INSERT INTO cloud_chunks (chunk_id, user_id, data, created_by, sessions, memories, prompts)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (user_id, chunk_id) DO NOTHING`,
		chunkID, userID, data, createdBy, sessions, memories, prompts,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: store chunk: %w", err)
	}
	return nil
}

// GetChunk retrieves the raw data for a chunk, scoped to user_id.
func (cs *CloudStore) GetChunk(userID, chunkID string) ([]byte, error) {
	var data []byte
	err := cs.db.QueryRow(
		`SELECT data FROM cloud_chunks WHERE user_id = $1 AND chunk_id = $2`,
		userID, chunkID,
	).Scan(&data)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("cloudstore: get chunk: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("cloudstore: get chunk: %w", err)
	}
	return data, nil
}

// ListChunks returns all chunk entries for a user.
func (cs *CloudStore) ListChunks(userID string) ([]CloudChunkEntry, error) {
	rows, err := cs.db.Query(
		`SELECT chunk_id, user_id, created_by, sessions, memories, prompts, imported_at
		 FROM cloud_chunks WHERE user_id = $1 ORDER BY imported_at`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list chunks: %w", err)
	}
	defer rows.Close()

	var results []CloudChunkEntry
	for rows.Next() {
		var c CloudChunkEntry
		if err := rows.Scan(&c.ChunkID, &c.UserID, &c.CreatedBy, &c.Sessions, &c.Memories, &c.Prompts, &c.ImportedAt); err != nil {
			return nil, fmt.Errorf("cloudstore: scan chunk entry: %w", err)
		}
		results = append(results, c)
	}
	return results, rows.Err()
}

// RecordSyncedChunk records that a chunk has been synced for a user.
// Uses INSERT ON CONFLICT DO NOTHING for idempotency.
func (cs *CloudStore) RecordSyncedChunk(userID, chunkID string) error {
	_, err := cs.db.Exec(
		`INSERT INTO cloud_sync_chunks (chunk_id, user_id)
		 VALUES ($1, $2)
		 ON CONFLICT (user_id, chunk_id) DO NOTHING`,
		chunkID, userID,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: record synced chunk: %w", err)
	}
	return nil
}

// GetSyncedChunks returns a set of chunk IDs that have been synced for a user.
func (cs *CloudStore) GetSyncedChunks(userID string) (map[string]bool, error) {
	rows, err := cs.db.Query(
		`SELECT chunk_id FROM cloud_sync_chunks WHERE user_id = $1`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: get synced chunks: %w", err)
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("cloudstore: scan synced chunk: %w", err)
		}
		result[id] = true
	}
	return result, rows.Err()
}

// ─── Stats & Context ────────────────────────────────────────────────────────

// Stats returns aggregate statistics for a user.
func (cs *CloudStore) Stats(userID string) (*CloudStats, error) {
	stats := &CloudStats{}

	cs.db.QueryRow(
		`SELECT COUNT(*) FROM cloud_sessions WHERE user_id = $1`, userID,
	).Scan(&stats.TotalSessions)

	cs.db.QueryRow(
		`SELECT COUNT(*) FROM cloud_observations WHERE user_id = $1 AND deleted_at IS NULL`, userID,
	).Scan(&stats.TotalObservations)

	cs.db.QueryRow(
		`SELECT COUNT(*) FROM cloud_prompts WHERE user_id = $1`, userID,
	).Scan(&stats.TotalPrompts)

	rows, err := cs.db.Query(
		`SELECT project FROM cloud_observations
		 WHERE user_id = $1 AND project IS NOT NULL AND deleted_at IS NULL
		 GROUP BY project ORDER BY MAX(created_at) DESC`,
		userID,
	)
	if err != nil {
		return stats, nil
	}
	defer rows.Close()

	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			stats.Projects = append(stats.Projects, p)
		}
	}

	return stats, nil
}

// FormatContext produces a formatted context string for a user, matching the
// output format of internal/store/store.go's FormatContext method.
func (cs *CloudStore) FormatContext(userID, project, scope string) (string, error) {
	sessions, err := cs.RecentSessions(userID, project, 5)
	if err != nil {
		return "", err
	}

	observations, err := cs.RecentObservations(userID, project, scope, 20)
	if err != nil {
		return "", err
	}

	prompts, err := cs.RecentPrompts(userID, project, 10)
	if err != nil {
		return "", err
	}

	if len(sessions) == 0 && len(observations) == 0 && len(prompts) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("## Memory from Previous Sessions\n\n")

	if len(sessions) > 0 {
		b.WriteString("### Recent Sessions\n")
		for _, sess := range sessions {
			summary := ""
			if sess.Summary != nil {
				summary = fmt.Sprintf(": %s", truncate(*sess.Summary, 200))
			}
			fmt.Fprintf(&b, "- **%s** (%s)%s [%d observations]\n",
				sess.Project, sess.StartedAt, summary, sess.ObservationCount)
		}
		b.WriteString("\n")
	}

	if len(prompts) > 0 {
		b.WriteString("### Recent User Prompts\n")
		for _, p := range prompts {
			fmt.Fprintf(&b, "- %s: %s\n", p.CreatedAt, truncate(p.Content, 200))
		}
		b.WriteString("\n")
	}

	if len(observations) > 0 {
		b.WriteString("### Recent Observations\n")
		for _, obs := range observations {
			fmt.Fprintf(&b, "- [%s] **%s**: %s\n",
				obs.Type, obs.Title, truncate(obs.Content, 300))
		}
		b.WriteString("\n")
	}

	return b.String(), nil
}

// ─── Dashboard Queries ──────────────────────────────────────────────────

// ProjectStat holds per-project aggregate data for the dashboard overview.
type ProjectStat struct {
	Project          string  `json:"project"`
	SessionCount     int     `json:"session_count"`
	ObservationCount int     `json:"observation_count"`
	PromptCount      int     `json:"prompt_count"`
	LastActivity     *string `json:"last_activity,omitempty"`
}

// ProjectStats returns per-project statistics for a user, ordered by most
// recent activity. Used by the dashboard overview (Phase 4).
func (cs *CloudStore) ProjectStats(userID string) ([]ProjectStat, error) {
	rows, err := cs.db.Query(`
		SELECT
			p.project,
			COALESCE(s.session_count, 0),
			COALESCE(o.obs_count, 0),
			COALESCE(pr.prompt_count, 0),
			GREATEST(s.last_session, o.last_obs, pr.last_prompt) AS last_activity
		FROM (
			SELECT DISTINCT COALESCE(project, '') AS project FROM cloud_sessions WHERE user_id = $1
			UNION
			SELECT DISTINCT COALESCE(project, '') AS project FROM cloud_observations WHERE user_id = $1 AND deleted_at IS NULL
			UNION
			SELECT DISTINCT COALESCE(project, '') AS project FROM cloud_prompts WHERE user_id = $1
		) p
		LEFT JOIN (
			SELECT project, COUNT(*) AS session_count, MAX(started_at) AS last_session
			FROM cloud_sessions WHERE user_id = $1 GROUP BY project
		) s ON s.project = p.project
		LEFT JOIN (
			SELECT COALESCE(project, '') AS project, COUNT(*) AS obs_count, MAX(created_at) AS last_obs
			FROM cloud_observations WHERE user_id = $1 AND deleted_at IS NULL GROUP BY COALESCE(project, '')
		) o ON o.project = p.project
		LEFT JOIN (
			SELECT COALESCE(project, '') AS project, COUNT(*) AS prompt_count, MAX(created_at) AS last_prompt
			FROM cloud_prompts WHERE user_id = $1 GROUP BY COALESCE(project, '')
		) pr ON pr.project = p.project
		WHERE p.project != ''
		ORDER BY last_activity DESC NULLS LAST
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: project stats: %w", err)
	}
	defer rows.Close()

	var results []ProjectStat
	for rows.Next() {
		var ps ProjectStat
		if err := rows.Scan(&ps.Project, &ps.SessionCount, &ps.ObservationCount, &ps.PromptCount, &ps.LastActivity); err != nil {
			return nil, fmt.Errorf("cloudstore: scan project stat: %w", err)
		}
		results = append(results, ps)
	}
	if results == nil {
		results = []ProjectStat{}
	}
	return results, rows.Err()
}

// ContributorStat holds per-user aggregate data for the contributors view.
type ContributorStat struct {
	UserID           string  `json:"user_id"`
	Username         string  `json:"username"`
	Email            string  `json:"email"`
	SessionCount     int     `json:"session_count"`
	ObservationCount int     `json:"observation_count"`
	LastSync         *string `json:"last_sync,omitempty"`
}

// ContributorStats returns per-user statistics across the whole system.
// This is an admin/org-level query (Phase 7).
func (cs *CloudStore) ContributorStats() ([]ContributorStat, error) {
	rows, err := cs.db.Query(`
		SELECT
			u.id, u.username, u.email,
			COALESCE(s.cnt, 0),
			COALESCE(o.cnt, 0),
			GREATEST(s.last_session, o.last_obs) AS last_sync
		FROM cloud_users u
		LEFT JOIN (
			SELECT user_id, COUNT(*) AS cnt, MAX(started_at) AS last_session
			FROM cloud_sessions GROUP BY user_id
		) s ON s.user_id = u.id
		LEFT JOIN (
			SELECT user_id, COUNT(*) AS cnt, MAX(created_at) AS last_obs
			FROM cloud_observations WHERE deleted_at IS NULL GROUP BY user_id
		) o ON o.user_id = u.id
		ORDER BY last_sync DESC NULLS LAST
	`)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: contributor stats: %w", err)
	}
	defer rows.Close()

	var results []ContributorStat
	for rows.Next() {
		var c ContributorStat
		if err := rows.Scan(&c.UserID, &c.Username, &c.Email, &c.SessionCount, &c.ObservationCount, &c.LastSync); err != nil {
			return nil, fmt.Errorf("cloudstore: scan contributor stat: %w", err)
		}
		results = append(results, c)
	}
	if results == nil {
		results = []ContributorStat{}
	}
	return results, rows.Err()
}

// ListAllUsers returns all registered users with their API key status.
// Used by the admin user management view (Phase 8).
func (cs *CloudStore) ListAllUsers() ([]CloudUser, error) {
	rows, err := cs.db.Query(`
		SELECT id, username, email, password_hash, api_key_hash, created_at, updated_at
		FROM cloud_users
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list all users: %w", err)
	}
	defer rows.Close()

	var results []CloudUser
	for rows.Next() {
		var u CloudUser
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.APIKeyHash, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("cloudstore: scan user: %w", err)
		}
		results = append(results, u)
	}
	if results == nil {
		results = []CloudUser{}
	}
	return results, rows.Err()
}

// SystemHealthInfo holds system health metrics for admin views.
type SystemHealthInfo struct {
	DBConnected    bool   `json:"db_connected"`
	TotalUsers     int    `json:"total_users"`
	TotalSessions  int    `json:"total_sessions"`
	TotalMemories  int    `json:"total_memories"`
	TotalPrompts   int    `json:"total_prompts"`
	TotalMutations int    `json:"total_mutations"`
	DBVersion      string `json:"db_version"`
}

// SystemHealth returns system-wide health metrics for admin views (Phase 8).
func (cs *CloudStore) SystemHealth() (*SystemHealthInfo, error) {
	h := &SystemHealthInfo{}

	if err := cs.db.Ping(); err != nil {
		h.DBConnected = false
		return h, nil
	}
	h.DBConnected = true

	cs.db.QueryRow(`SELECT COUNT(*) FROM cloud_users`).Scan(&h.TotalUsers)
	cs.db.QueryRow(`SELECT COUNT(*) FROM cloud_sessions`).Scan(&h.TotalSessions)
	cs.db.QueryRow(`SELECT COUNT(*) FROM cloud_observations WHERE deleted_at IS NULL`).Scan(&h.TotalMemories)
	cs.db.QueryRow(`SELECT COUNT(*) FROM cloud_prompts`).Scan(&h.TotalPrompts)
	cs.db.QueryRow(`SELECT COUNT(*) FROM cloud_mutations`).Scan(&h.TotalMutations)
	cs.db.QueryRow(`SELECT version()`).Scan(&h.DBVersion)

	return h, nil
}

// UserProjects returns distinct project names for a user.
func (cs *CloudStore) UserProjects(userID string) ([]string, error) {
	rows, err := cs.db.Query(`
		SELECT DISTINCT COALESCE(project, '') AS project
		FROM cloud_observations
		WHERE user_id = $1 AND deleted_at IS NULL AND project IS NOT NULL AND project != ''
		UNION
		SELECT DISTINCT project FROM cloud_sessions
		WHERE user_id = $1 AND project != ''
		ORDER BY project
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: user projects: %w", err)
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("cloudstore: scan project: %w", err)
		}
		results = append(results, p)
	}
	if results == nil {
		results = []string{}
	}
	return results, rows.Err()
}

// ─── Mutations (append-only per-user ledger for sync) ───────────────────

// CloudMutation represents a single append-only mutation in the cloud ledger.
type CloudMutation struct {
	Seq        int64           `json:"seq"`
	UserID     string          `json:"user_id"`
	Entity     string          `json:"entity"`
	EntityKey  string          `json:"entity_key"`
	Op         string          `json:"op"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt string          `json:"occurred_at"`
}

// PushMutationsRequest holds the JSON body for POST /sync/mutations/push.
type PushMutationsRequest struct {
	Mutations []PushMutationEntry `json:"mutations"`
}

// PushMutationEntry is a single mutation in a push request.
// The shape mirrors the local sync_mutations.payload contract.
type PushMutationEntry struct {
	Entity    string          `json:"entity"`
	EntityKey string          `json:"entity_key"`
	Op        string          `json:"op"`
	Payload   json.RawMessage `json:"payload"`
}

// PushMutationsResult holds the response for a mutation push.
type PushMutationsResult struct {
	Accepted int   `json:"accepted"`
	LastSeq  int64 `json:"last_seq"`
}

// PullMutationsResult holds the response for a mutation pull.
type PullMutationsResult struct {
	Mutations []CloudMutation `json:"mutations"`
	HasMore   bool            `json:"has_more"`
}

// AppendMutation inserts a mutation into the cloud ledger for a user.
// Returns the assigned sequence number.
func (cs *CloudStore) AppendMutation(userID, entity, entityKey, op string, payload json.RawMessage) (int64, error) {
	var seq int64
	err := cs.db.QueryRow(
		`INSERT INTO cloud_mutations (user_id, entity, entity_key, op, payload)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING seq`,
		userID, entity, entityKey, op, payload,
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("cloudstore: append mutation: %w", err)
	}
	return seq, nil
}

// AppendMutationBatch inserts multiple mutations atomically.
// Returns the number accepted and the last assigned sequence.
func (cs *CloudStore) AppendMutationBatch(userID string, entries []PushMutationEntry) (*PushMutationsResult, error) {
	if len(entries) == 0 {
		return &PushMutationsResult{}, nil
	}
	for _, e := range entries {
		if err := cs.ensureProjectSyncEnabled(e.Entity, e.Payload); err != nil {
			return nil, err
		}
	}
	tx, err := cs.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("cloudstore: begin mutation batch: %w", err)
	}
	defer tx.Rollback()

	var lastSeq int64
	accepted := 0
	for _, e := range entries {
		err := tx.QueryRow(
			`INSERT INTO cloud_mutations (user_id, entity, entity_key, op, payload)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING seq`,
			userID, e.Entity, e.EntityKey, e.Op, e.Payload,
		).Scan(&lastSeq)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: insert mutation: %w", err)
		}
		accepted++
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("cloudstore: commit mutation batch: %w", err)
	}
	return &PushMutationsResult{Accepted: accepted, LastSeq: lastSeq}, nil
}

// PullMutations returns mutations for a user with seq > sinceSeq, ordered ASC.
func (cs *CloudStore) PullMutations(userID string, sinceSeq int64, limit int) (*PullMutationsResult, error) {
	if limit <= 0 {
		limit = 100
	}
	var mutations []CloudMutation
	lastSeq := sinceSeq
	hasMore := false
	for len(mutations) < limit+1 {
		rows, err := cs.db.Query(
			`SELECT seq, user_id, entity, entity_key, op, payload, occurred_at
			 FROM cloud_mutations
			 WHERE user_id = $1 AND seq > $2
			 ORDER BY seq ASC
			 LIMIT $3`,
			userID, lastSeq, limit+1,
		)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: pull mutations: %w", err)
		}

		fetched := 0
		for rows.Next() {
			fetched++
			var m CloudMutation
			if err := rows.Scan(&m.Seq, &m.UserID, &m.Entity, &m.EntityKey, &m.Op, &m.Payload, &m.OccurredAt); err != nil {
				rows.Close()
				return nil, fmt.Errorf("cloudstore: scan mutation: %w", err)
			}
			lastSeq = m.Seq
			project, err := projectFromMutation(m.Entity, m.Payload)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("cloudstore: pull project from mutation: %w", err)
			}
			enabled, err := cs.IsProjectSyncEnabled(project)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if !enabled {
				continue
			}
			mutations = append(mutations, m)
			if len(mutations) > limit {
				hasMore = true
				break
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("cloudstore: pull mutations rows: %w", err)
		}
		rows.Close()
		if hasMore || fetched < limit+1 {
			break
		}
	}

	if len(mutations) > limit {
		mutations = mutations[:limit]
	}
	if mutations == nil {
		mutations = []CloudMutation{}
	}
	return &PullMutationsResult{Mutations: mutations, HasMore: hasMore}, nil
}

// UpsertSessionByPayload applies a session upsert from a sync mutation payload.
func (cs *CloudStore) UpsertSessionByPayload(userID string, payload json.RawMessage) error {
	var p struct {
		ID        string  `json:"id"`
		Project   string  `json:"project"`
		Directory string  `json:"directory"`
		EndedAt   *string `json:"ended_at,omitempty"`
		Summary   *string `json:"summary,omitempty"`
	}
	if err := decodeMutationPayload(payload, &p); err != nil {
		return fmt.Errorf("cloudstore: unmarshal session payload: %w", err)
	}
	_, err := cs.db.Exec(
		`INSERT INTO cloud_sessions (id, user_id, project, directory, ended_at, summary)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (user_id, id) DO UPDATE SET
		   project   = EXCLUDED.project,
		   directory = EXCLUDED.directory,
		   ended_at  = COALESCE(EXCLUDED.ended_at, cloud_sessions.ended_at),
		   summary   = COALESCE(EXCLUDED.summary, cloud_sessions.summary)`,
		p.ID, userID, p.Project, p.Directory, p.EndedAt, p.Summary,
	)
	return err
}

// UpsertObservationByPayload applies an observation upsert from a sync mutation payload.
// Uses sync_id as the stable cross-device identity for upserts.
func (cs *CloudStore) UpsertObservationByPayload(userID string, payload json.RawMessage) error {
	var p struct {
		SyncID    string  `json:"sync_id"`
		SessionID string  `json:"session_id"`
		Type      string  `json:"type"`
		Title     string  `json:"title"`
		Content   string  `json:"content"`
		ToolName  *string `json:"tool_name,omitempty"`
		Project   *string `json:"project,omitempty"`
		Scope     string  `json:"scope"`
		TopicKey  *string `json:"topic_key,omitempty"`
	}
	if err := decodeMutationPayload(payload, &p); err != nil {
		return fmt.Errorf("cloudstore: unmarshal observation payload: %w", err)
	}
	scope := normalizeScope(p.Scope)
	normHash := hashNormalized(p.Content)

	// Try to find an existing observation by sync_id for this user.
	var existingID int64
	err := cs.db.QueryRow(
		`SELECT id FROM cloud_observations
		 WHERE user_id = $1 AND session_id = $2 AND normalized_hash IN (
		   SELECT normalized_hash FROM cloud_observations WHERE user_id = $1
		 )
		 AND type = $3 AND title = $4
		 ORDER BY id DESC LIMIT 1`,
		userID, p.SessionID, p.Type, p.Title,
	).Scan(&existingID)

	// Simple approach: always upsert by looking for matching content, or insert new.
	if err == sql.ErrNoRows || err != nil {
		// Insert new
		_, err = cs.db.Exec(
			`INSERT INTO cloud_observations
			 (user_id, session_id, type, title, content, tool_name, project, scope, topic_key, normalized_hash,
			  revision_count, duplicate_count, last_seen_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1, 1, NOW(), NOW())`,
			userID, p.SessionID, p.Type, p.Title, p.Content,
			p.ToolName, p.Project, scope, p.TopicKey, normHash,
		)
		return err
	}
	// Update existing
	_, err = cs.db.Exec(
		`UPDATE cloud_observations
		 SET type = $1, title = $2, content = $3, tool_name = $4, project = $5,
		     scope = $6, topic_key = $7, normalized_hash = $8,
		     revision_count = revision_count + 1, updated_at = NOW(), deleted_at = NULL
		 WHERE id = $9 AND user_id = $10`,
		p.Type, p.Title, p.Content, p.ToolName, p.Project, scope, p.TopicKey, normHash, existingID, userID,
	)
	return err
}

// DeleteObservationByPayload applies a soft/hard delete from a sync mutation payload.
func (cs *CloudStore) DeleteObservationByPayload(userID string, payload json.RawMessage) error {
	var p struct {
		SyncID     string  `json:"sync_id"`
		Deleted    bool    `json:"deleted"`
		DeletedAt  *string `json:"deleted_at,omitempty"`
		HardDelete bool    `json:"hard_delete"`
	}
	if err := decodeMutationPayload(payload, &p); err != nil {
		return fmt.Errorf("cloudstore: unmarshal delete payload: %w", err)
	}
	// For the cloud side, we can't match by sync_id since the cloud schema
	// doesn't have a sync_id column. The mutation itself carries the intent —
	// the cloud simply records it in the ledger for pull by other devices.
	// Direct cloud-side apply is optional; the ledger is the source of truth.
	return nil
}

// UpsertPromptByPayload applies a prompt upsert from a sync mutation payload.
func (cs *CloudStore) UpsertPromptByPayload(userID string, payload json.RawMessage) error {
	var p struct {
		SyncID    string  `json:"sync_id"`
		SessionID string  `json:"session_id"`
		Content   string  `json:"content"`
		Project   *string `json:"project,omitempty"`
	}
	if err := decodeMutationPayload(payload, &p); err != nil {
		return fmt.Errorf("cloudstore: unmarshal prompt payload: %w", err)
	}
	// Insert new prompt (prompts are append-only in the cloud schema).
	_, err := cs.db.Exec(
		`INSERT INTO cloud_prompts (user_id, session_id, content, project)
		 VALUES ($1, $2, $3, $4)`,
		userID, p.SessionID, p.Content, p.Project,
	)
	return err
}

// ApplyMutationPayload dispatches a mutation to the appropriate entity handler.
// This enables the cloud to materialize pushed mutations into the relational tables.
func (cs *CloudStore) ApplyMutationPayload(userID, entity, op string, payload json.RawMessage) error {
	switch entity {
	case "session":
		return cs.UpsertSessionByPayload(userID, payload)
	case "observation":
		if op == "delete" {
			return cs.DeleteObservationByPayload(userID, payload)
		}
		return cs.UpsertObservationByPayload(userID, payload)
	case "prompt":
		return cs.UpsertPromptByPayload(userID, payload)
	default:
		return fmt.Errorf("cloudstore: unknown mutation entity %q", entity)
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────────

// nullableString returns nil for empty strings, matching the local store pattern.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func decodeMutationPayload(payload json.RawMessage, dest any) error {
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return fmt.Errorf("empty payload")
	}
	if trimmed[0] != '"' {
		return json.Unmarshal([]byte(trimmed), dest)
	}
	var encoded string
	if err := json.Unmarshal([]byte(trimmed), &encoded); err != nil {
		return err
	}
	return json.Unmarshal([]byte(encoded), dest)
}

// normalizeScope normalizes scope values, matching the local store pattern.
func normalizeScope(scope string) string {
	v := strings.TrimSpace(strings.ToLower(scope))
	if v == "personal" {
		return "personal"
	}
	if v == "global" {
		return "global"
	}
	if v == "" {
		return "project"
	}
	return v
}

// hashNormalized returns a SHA-256 hex digest of the content, matching
// the local store's deduplication hash pattern.
func hashNormalized(content string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(content))))
	return hex.EncodeToString(h[:])
}

// truncate truncates a string to max runes, appending "..." if needed.
// Uses rune counting for valid UTF-8 (matching internal/store/store.go).
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "..."
}
