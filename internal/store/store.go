// Package store implements the persistent memory engine for Engram.
//
// It uses SQLite with FTS5 full-text search to store and retrieve
// observations from AI coding sessions. This is the core of Engram —
// everything else (HTTP server, MCP server, CLI, plugins) talks to this.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ─── Types ───────────────────────────────────────────────────────────────────

type Session struct {
	ID        string  `json:"id"`
	Project   string  `json:"project"`
	Directory string  `json:"directory"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
	Summary   *string `json:"summary,omitempty"`
	Status    string  `json:"status"`
}

type Observation struct {
	ID            int64   `json:"id"`
	SessionID     string  `json:"session_id"`
	Type          string  `json:"type"`
	Title         string  `json:"title"`
	Content       string  `json:"content"`
	ToolName      *string `json:"tool_name,omitempty"`
	Project       *string `json:"project,omitempty"`
	CreatedAt     string  `json:"created_at"`
	SessionStatus string  `json:"session_status,omitempty"` // Populated by AllObservations for TUI
}

type SearchResult struct {
	Observation
	Rank float64 `json:"rank"`
}

type SessionSummary struct {
	ID               string  `json:"id"`
	Project          string  `json:"project"`
	StartedAt        string  `json:"started_at"`
	EndedAt          *string `json:"ended_at,omitempty"`
	Summary          *string `json:"summary,omitempty"`
	Status           string  `json:"status"`
	ObservationCount int     `json:"observation_count"`
}

type Stats struct {
	TotalSessions     int      `json:"total_sessions"`
	TotalObservations int      `json:"total_observations"`
	TotalPrompts      int      `json:"total_prompts"`
	Projects          []string `json:"projects"`
}

type TimelineEntry struct {
	ID        int64   `json:"id"`
	SessionID string  `json:"session_id"`
	Type      string  `json:"type"`
	Title     string  `json:"title"`
	Content   string  `json:"content"`
	ToolName  *string `json:"tool_name,omitempty"`
	Project   *string `json:"project,omitempty"`
	CreatedAt string  `json:"created_at"`
	IsFocus   bool    `json:"is_focus"` // true for the anchor observation
}

type TimelineResult struct {
	Focus        Observation     `json:"focus"`        // The anchor observation
	Before       []TimelineEntry `json:"before"`       // Observations before the focus (chronological)
	After        []TimelineEntry `json:"after"`        // Observations after the focus (chronological)
	SessionInfo  *Session        `json:"session_info"` // Session that contains the focus observation
	TotalInRange int             `json:"total_in_range"`
}

type SearchOptions struct {
	Type    string `json:"type,omitempty"`
	Project string `json:"project,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type AddObservationParams struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	ToolName  string `json:"tool_name,omitempty"`
	Project   string `json:"project,omitempty"`
}

type Prompt struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Project   string `json:"project,omitempty"`
	CreatedAt string `json:"created_at"`
}

type AddPromptParams struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Project   string `json:"project,omitempty"`
}

// ExportData is the full serializable dump of the engram database.
type ExportData struct {
	Version      string        `json:"version"`
	ExportedAt   string        `json:"exported_at"`
	Sessions     []Session     `json:"sessions"`
	Observations []Observation `json:"observations"`
	Prompts      []Prompt      `json:"prompts"`
}

// ─── Config ──────────────────────────────────────────────────────────────────

type Config struct {
	DataDir              string
	MaxObservationLength int
	MaxContextResults    int
	MaxSearchResults     int
}

func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		DataDir:              filepath.Join(home, ".engram"),
		MaxObservationLength: 2000,
		MaxContextResults:    20,
		MaxSearchResults:     20,
	}
}

// ─── Store ───────────────────────────────────────────────────────────────────

type Store struct {
	db  *sql.DB
	cfg Config
}

func New(cfg Config) (*Store, error) {
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("engram: create data dir: %w", err)
	}

	dbPath := filepath.Join(cfg.DataDir, "engram.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("engram: open database: %w", err)
	}

	// SQLite performance pragmas
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return nil, fmt.Errorf("engram: pragma %q: %w", p, err)
		}
	}

	s := &Store{db: db, cfg: cfg}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("engram: migration: %w", err)
	}

	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// ─── Migrations ──────────────────────────────────────────────────────────────

func (s *Store) migrate() error {
	schema := `
		CREATE TABLE IF NOT EXISTS sessions (
			id         TEXT PRIMARY KEY,
			project    TEXT NOT NULL,
			directory  TEXT NOT NULL,
			started_at TEXT NOT NULL DEFAULT (datetime('now')),
			ended_at   TEXT,
			summary    TEXT,
			status     TEXT NOT NULL DEFAULT 'active'
		);

		CREATE TABLE IF NOT EXISTS observations (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT    NOT NULL,
			type       TEXT    NOT NULL,
			title      TEXT    NOT NULL,
			content    TEXT    NOT NULL,
			tool_name  TEXT,
			project    TEXT,
			created_at TEXT    NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);

		CREATE INDEX IF NOT EXISTS idx_obs_session  ON observations(session_id);
		CREATE INDEX IF NOT EXISTS idx_obs_type     ON observations(type);
		CREATE INDEX IF NOT EXISTS idx_obs_project  ON observations(project);
		CREATE INDEX IF NOT EXISTS idx_obs_created  ON observations(created_at DESC);

		CREATE VIRTUAL TABLE IF NOT EXISTS observations_fts USING fts5(
			title,
			content,
			tool_name,
			type,
			project,
			content='observations',
			content_rowid='id'
		);

		CREATE TABLE IF NOT EXISTS user_prompts (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT    NOT NULL,
			content    TEXT    NOT NULL,
			project    TEXT,
			created_at TEXT    NOT NULL DEFAULT (datetime('now')),
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);

		CREATE INDEX IF NOT EXISTS idx_prompts_session ON user_prompts(session_id);
		CREATE INDEX IF NOT EXISTS idx_prompts_project ON user_prompts(project);
		CREATE INDEX IF NOT EXISTS idx_prompts_created ON user_prompts(created_at DESC);

		CREATE VIRTUAL TABLE IF NOT EXISTS prompts_fts USING fts5(
			content,
			project,
			content='user_prompts',
			content_rowid='id'
		);

		CREATE TABLE IF NOT EXISTS sync_chunks (
			chunk_id    TEXT PRIMARY KEY,
			imported_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Create triggers to keep FTS in sync (idempotent check)
	var name string
	err := s.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='trigger' AND name='obs_fts_insert'",
	).Scan(&name)

	if err == sql.ErrNoRows {
		triggers := `
			CREATE TRIGGER obs_fts_insert AFTER INSERT ON observations BEGIN
				INSERT INTO observations_fts(rowid, title, content, tool_name, type, project)
				VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project);
			END;

			CREATE TRIGGER obs_fts_delete AFTER DELETE ON observations BEGIN
				INSERT INTO observations_fts(observations_fts, rowid, title, content, tool_name, type, project)
				VALUES ('delete', old.id, old.title, old.content, old.tool_name, old.type, old.project);
			END;

			CREATE TRIGGER obs_fts_update AFTER UPDATE ON observations BEGIN
				INSERT INTO observations_fts(observations_fts, rowid, title, content, tool_name, type, project)
				VALUES ('delete', old.id, old.title, old.content, old.tool_name, old.type, old.project);
				INSERT INTO observations_fts(rowid, title, content, tool_name, type, project)
				VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project);
			END;
		`
		if _, err := s.db.Exec(triggers); err != nil {
			return err
		}
	}

	// Prompts FTS triggers (separate idempotent check)
	var promptTrigger string
	err = s.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='trigger' AND name='prompt_fts_insert'",
	).Scan(&promptTrigger)

	if err == sql.ErrNoRows {
		promptTriggers := `
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
		if _, err := s.db.Exec(promptTriggers); err != nil {
			return err
		}
	}

	return nil
}

// ─── Sessions ────────────────────────────────────────────────────────────────

func (s *Store) CreateSession(id, project, directory string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO sessions (id, project, directory) VALUES (?, ?, ?)`,
		id, project, directory,
	)
	return err
}

func (s *Store) EndSession(id string, summary string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET ended_at = datetime('now'), status = 'completed', summary = ? WHERE id = ?`,
		nullableString(summary), id,
	)
	return err
}

func (s *Store) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT id, project, directory, started_at, ended_at, summary, status FROM sessions WHERE id = ?`, id,
	)
	var sess Session
	if err := row.Scan(&sess.ID, &sess.Project, &sess.Directory, &sess.StartedAt, &sess.EndedAt, &sess.Summary, &sess.Status); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) RecentSessions(project string, limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 5
	}

	query := `
		SELECT s.id, s.project, s.started_at, s.ended_at, s.summary, s.status,
		       COUNT(o.id) as observation_count
		FROM sessions s
		LEFT JOIN observations o ON o.session_id = s.id
		WHERE s.status = 'completed'
	`
	args := []any{}

	if project != "" {
		query += " AND s.project = ?"
		args = append(args, project)
	}

	query += " GROUP BY s.id ORDER BY s.ended_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SessionSummary
	for rows.Next() {
		var ss SessionSummary
		if err := rows.Scan(&ss.ID, &ss.Project, &ss.StartedAt, &ss.EndedAt, &ss.Summary, &ss.Status, &ss.ObservationCount); err != nil {
			return nil, err
		}
		results = append(results, ss)
	}
	return results, rows.Err()
}

// AllSessions returns recent sessions regardless of status (for TUI browsing).
// Active sessions are sorted to the top.
func (s *Store) AllSessions(project string, limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT s.id, s.project, s.started_at, s.ended_at, s.summary, s.status,
		       COUNT(o.id) as observation_count
		FROM sessions s
		LEFT JOIN observations o ON o.session_id = s.id
		WHERE 1=1
	`
	args := []any{}

	if project != "" {
		query += " AND s.project = ?"
		args = append(args, project)
	}

	query += " GROUP BY s.id ORDER BY (s.status = 'active') DESC, s.started_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SessionSummary
	for rows.Next() {
		var ss SessionSummary
		if err := rows.Scan(&ss.ID, &ss.Project, &ss.StartedAt, &ss.EndedAt, &ss.Summary, &ss.Status, &ss.ObservationCount); err != nil {
			return nil, err
		}
		results = append(results, ss)
	}
	return results, rows.Err()
}

// AllObservations returns recent observations regardless of session status (for TUI browsing).
// Observations from active sessions are sorted to the top.
func (s *Store) AllObservations(project string, limit int) ([]Observation, error) {
	if limit <= 0 {
		limit = s.cfg.MaxContextResults
	}

	query := `
		SELECT o.id, o.session_id, o.type, o.title, o.content, o.tool_name, o.project, o.created_at,
		       s.status
		FROM observations o
		JOIN sessions s ON s.id = o.session_id
	`
	args := []any{}

	if project != "" {
		query += " WHERE o.project = ?"
		args = append(args, project)
	}

	query += " ORDER BY (s.status = 'active') DESC, o.created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Observation
	for rows.Next() {
		var o Observation
		var sessionStatus string
		if err := rows.Scan(&o.ID, &o.SessionID, &o.Type, &o.Title, &o.Content, &o.ToolName, &o.Project, &o.CreatedAt, &sessionStatus); err != nil {
			return nil, err
		}
		o.SessionStatus = sessionStatus
		results = append(results, o)
	}
	return results, rows.Err()
}

// SessionObservations returns all observations for a specific session.
func (s *Store) SessionObservations(sessionID string, limit int) ([]Observation, error) {
	if limit <= 0 {
		limit = 200
	}

	query := `
		SELECT id, session_id, type, title, content, tool_name, project, created_at
		FROM observations
		WHERE session_id = ?
		ORDER BY created_at ASC
		LIMIT ?
	`
	return s.queryObservations(query, sessionID, limit)
}

// ─── Observations ────────────────────────────────────────────────────────────

func (s *Store) AddObservation(p AddObservationParams) (int64, error) {
	// Strip <private>...</private> tags before persisting ANYTHING
	title := stripPrivateTags(p.Title)
	content := stripPrivateTags(p.Content)

	if len(content) > s.cfg.MaxObservationLength {
		content = content[:s.cfg.MaxObservationLength] + "... [truncated]"
	}

	res, err := s.db.Exec(
		`INSERT INTO observations (session_id, type, title, content, tool_name, project)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		p.SessionID, p.Type, title, content,
		nullableString(p.ToolName), nullableString(p.Project),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) RecentObservations(project string, limit int) ([]Observation, error) {
	if limit <= 0 {
		limit = s.cfg.MaxContextResults
	}

	query := `
		SELECT o.id, o.session_id, o.type, o.title, o.content, o.tool_name, o.project, o.created_at
		FROM observations o
		JOIN sessions s ON s.id = o.session_id
		WHERE s.status = 'completed'
	`
	args := []any{}

	if project != "" {
		query += " AND o.project = ?"
		args = append(args, project)
	}

	query += " ORDER BY o.created_at DESC LIMIT ?"
	args = append(args, limit)

	return s.queryObservations(query, args...)
}

// ─── User Prompts ────────────────────────────────────────────────────────────

func (s *Store) AddPrompt(p AddPromptParams) (int64, error) {
	content := stripPrivateTags(p.Content)
	if len(content) > s.cfg.MaxObservationLength {
		content = content[:s.cfg.MaxObservationLength] + "... [truncated]"
	}

	res, err := s.db.Exec(
		`INSERT INTO user_prompts (session_id, content, project) VALUES (?, ?, ?)`,
		p.SessionID, content, nullableString(p.Project),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) RecentPrompts(project string, limit int) ([]Prompt, error) {
	if limit <= 0 {
		limit = 20
	}

	query := `SELECT id, session_id, content, project, created_at FROM user_prompts`
	args := []any{}

	if project != "" {
		query += " WHERE project = ?"
		args = append(args, project)
	}

	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Prompt
	for rows.Next() {
		var p Prompt
		if err := rows.Scan(&p.ID, &p.SessionID, &p.Content, &p.Project, &p.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, p)
	}
	return results, rows.Err()
}

func (s *Store) SearchPrompts(query string, project string, limit int) ([]Prompt, error) {
	if limit <= 0 {
		limit = 10
	}

	ftsQuery := sanitizeFTS(query)

	sql := `
		SELECT p.id, p.session_id, p.content, p.project, p.created_at
		FROM prompts_fts fts
		JOIN user_prompts p ON p.id = fts.rowid
		WHERE prompts_fts MATCH ?
	`
	args := []any{ftsQuery}

	if project != "" {
		sql += " AND p.project = ?"
		args = append(args, project)
	}

	sql += " ORDER BY fts.rank LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search prompts: %w", err)
	}
	defer rows.Close()

	var results []Prompt
	for rows.Next() {
		var p Prompt
		if err := rows.Scan(&p.ID, &p.SessionID, &p.Content, &p.Project, &p.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, p)
	}
	return results, rows.Err()
}

// ─── Get Single Observation ──────────────────────────────────────────────────

func (s *Store) GetObservation(id int64) (*Observation, error) {
	row := s.db.QueryRow(
		`SELECT id, session_id, type, title, content, tool_name, project, created_at
		 FROM observations WHERE id = ?`, id,
	)
	var o Observation
	if err := row.Scan(&o.ID, &o.SessionID, &o.Type, &o.Title, &o.Content, &o.ToolName, &o.Project, &o.CreatedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

// ─── Timeline ────────────────────────────────────────────────────────────────
//
// Timeline provides chronological context around a specific observation.
// Given an observation ID, it returns N observations before and M after,
// all within the same session. This is the "progressive disclosure" pattern
// from claude-mem — agents first search, then use timeline to drill into
// the chronological neighborhood of a result.

func (s *Store) Timeline(observationID int64, before, after int) (*TimelineResult, error) {
	if before <= 0 {
		before = 5
	}
	if after <= 0 {
		after = 5
	}

	// 1. Get the focus observation
	focus, err := s.GetObservation(observationID)
	if err != nil {
		return nil, fmt.Errorf("timeline: observation #%d not found: %w", observationID, err)
	}

	// 2. Get session info
	session, err := s.GetSession(focus.SessionID)
	if err != nil {
		// Session might be missing for manual-save observations — non-fatal
		session = nil
	}

	// 3. Get observations BEFORE the focus (same session, older, chronological order)
	beforeRows, err := s.db.Query(`
		SELECT id, session_id, type, title, content, tool_name, project, created_at
		FROM observations
		WHERE session_id = ? AND id < ?
		ORDER BY id DESC
		LIMIT ?
	`, focus.SessionID, observationID, before)
	if err != nil {
		return nil, fmt.Errorf("timeline: before query: %w", err)
	}
	defer beforeRows.Close()

	var beforeEntries []TimelineEntry
	for beforeRows.Next() {
		var e TimelineEntry
		if err := beforeRows.Scan(&e.ID, &e.SessionID, &e.Type, &e.Title, &e.Content, &e.ToolName, &e.Project, &e.CreatedAt); err != nil {
			return nil, err
		}
		beforeEntries = append(beforeEntries, e)
	}
	if err := beforeRows.Err(); err != nil {
		return nil, err
	}
	// Reverse to get chronological order (oldest first)
	for i, j := 0, len(beforeEntries)-1; i < j; i, j = i+1, j-1 {
		beforeEntries[i], beforeEntries[j] = beforeEntries[j], beforeEntries[i]
	}

	// 4. Get observations AFTER the focus (same session, newer, chronological order)
	afterRows, err := s.db.Query(`
		SELECT id, session_id, type, title, content, tool_name, project, created_at
		FROM observations
		WHERE session_id = ? AND id > ?
		ORDER BY id ASC
		LIMIT ?
	`, focus.SessionID, observationID, after)
	if err != nil {
		return nil, fmt.Errorf("timeline: after query: %w", err)
	}
	defer afterRows.Close()

	var afterEntries []TimelineEntry
	for afterRows.Next() {
		var e TimelineEntry
		if err := afterRows.Scan(&e.ID, &e.SessionID, &e.Type, &e.Title, &e.Content, &e.ToolName, &e.Project, &e.CreatedAt); err != nil {
			return nil, err
		}
		afterEntries = append(afterEntries, e)
	}
	if err := afterRows.Err(); err != nil {
		return nil, err
	}

	// 5. Count total observations in the session for context
	var totalInRange int
	s.db.QueryRow(
		"SELECT COUNT(*) FROM observations WHERE session_id = ?", focus.SessionID,
	).Scan(&totalInRange)

	return &TimelineResult{
		Focus:        *focus,
		Before:       beforeEntries,
		After:        afterEntries,
		SessionInfo:  session,
		TotalInRange: totalInRange,
	}, nil
}

// ─── Search (FTS5) ───────────────────────────────────────────────────────────

func (s *Store) Search(query string, opts SearchOptions) ([]SearchResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > s.cfg.MaxSearchResults {
		limit = s.cfg.MaxSearchResults
	}

	// Sanitize query for FTS5 — wrap each term in quotes to avoid syntax errors
	ftsQuery := sanitizeFTS(query)

	sql := `
		SELECT o.id, o.session_id, o.type, o.title, o.content, o.tool_name, o.project, o.created_at,
		       fts.rank
		FROM observations_fts fts
		JOIN observations o ON o.id = fts.rowid
		WHERE observations_fts MATCH ?
	`
	args := []any{ftsQuery}

	if opts.Type != "" {
		sql += " AND o.type = ?"
		args = append(args, opts.Type)
	}

	if opts.Project != "" {
		sql += " AND o.project = ?"
		args = append(args, opts.Project)
	}

	sql += " ORDER BY fts.rank LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var sr SearchResult
		if err := rows.Scan(
			&sr.ID, &sr.SessionID, &sr.Type, &sr.Title, &sr.Content,
			&sr.ToolName, &sr.Project, &sr.CreatedAt, &sr.Rank,
		); err != nil {
			return nil, err
		}
		results = append(results, sr)
	}
	return results, rows.Err()
}

// ─── Stats ───────────────────────────────────────────────────────────────────

func (s *Store) Stats() (*Stats, error) {
	stats := &Stats{}

	s.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&stats.TotalSessions)
	s.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&stats.TotalObservations)
	s.db.QueryRow("SELECT COUNT(*) FROM user_prompts").Scan(&stats.TotalPrompts)

	rows, err := s.db.Query("SELECT DISTINCT project FROM observations WHERE project IS NOT NULL ORDER BY project")
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

// ─── Context Formatting ─────────────────────────────────────────────────────

func (s *Store) FormatContext(project string) (string, error) {
	sessions, err := s.RecentSessions(project, 5)
	if err != nil {
		return "", err
	}

	observations, err := s.RecentObservations(project, s.cfg.MaxContextResults)
	if err != nil {
		return "", err
	}

	prompts, err := s.RecentPrompts(project, 10)
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

// ─── Export / Import ─────────────────────────────────────────────────────────

func (s *Store) Export() (*ExportData, error) {
	data := &ExportData{
		Version:    "0.1.0",
		ExportedAt: Now(),
	}

	// Sessions
	rows, err := s.db.Query(
		"SELECT id, project, directory, started_at, ended_at, summary, status FROM sessions ORDER BY started_at",
	)
	if err != nil {
		return nil, fmt.Errorf("export sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.Project, &sess.Directory, &sess.StartedAt, &sess.EndedAt, &sess.Summary, &sess.Status); err != nil {
			return nil, err
		}
		data.Sessions = append(data.Sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Observations
	obsRows, err := s.db.Query(
		"SELECT id, session_id, type, title, content, tool_name, project, created_at FROM observations ORDER BY id",
	)
	if err != nil {
		return nil, fmt.Errorf("export observations: %w", err)
	}
	defer obsRows.Close()
	for obsRows.Next() {
		var o Observation
		if err := obsRows.Scan(&o.ID, &o.SessionID, &o.Type, &o.Title, &o.Content, &o.ToolName, &o.Project, &o.CreatedAt); err != nil {
			return nil, err
		}
		data.Observations = append(data.Observations, o)
	}
	if err := obsRows.Err(); err != nil {
		return nil, err
	}

	// Prompts
	promptRows, err := s.db.Query(
		"SELECT id, session_id, content, project, created_at FROM user_prompts ORDER BY id",
	)
	if err != nil {
		return nil, fmt.Errorf("export prompts: %w", err)
	}
	defer promptRows.Close()
	for promptRows.Next() {
		var p Prompt
		if err := promptRows.Scan(&p.ID, &p.SessionID, &p.Content, &p.Project, &p.CreatedAt); err != nil {
			return nil, err
		}
		data.Prompts = append(data.Prompts, p)
	}
	if err := promptRows.Err(); err != nil {
		return nil, err
	}

	return data, nil
}

func (s *Store) Import(data *ExportData) (*ImportResult, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("import: begin tx: %w", err)
	}
	defer tx.Rollback()

	result := &ImportResult{}

	// Import sessions (skip duplicates)
	for _, sess := range data.Sessions {
		res, err := tx.Exec(
			`INSERT OR IGNORE INTO sessions (id, project, directory, started_at, ended_at, summary, status)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sess.ID, sess.Project, sess.Directory, sess.StartedAt, sess.EndedAt, sess.Summary, sess.Status,
		)
		if err != nil {
			return nil, fmt.Errorf("import session %s: %w", sess.ID, err)
		}
		n, _ := res.RowsAffected()
		result.SessionsImported += int(n)
	}

	// Import observations (use new IDs — AUTOINCREMENT)
	for _, obs := range data.Observations {
		_, err := tx.Exec(
			`INSERT INTO observations (session_id, type, title, content, tool_name, project, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			obs.SessionID, obs.Type, obs.Title, obs.Content, obs.ToolName, obs.Project, obs.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("import observation %d: %w", obs.ID, err)
		}
		result.ObservationsImported++
	}

	// Import prompts
	for _, p := range data.Prompts {
		_, err := tx.Exec(
			`INSERT INTO user_prompts (session_id, content, project, created_at)
			 VALUES (?, ?, ?, ?)`,
			p.SessionID, p.Content, p.Project, p.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("import prompt %d: %w", p.ID, err)
		}
		result.PromptsImported++
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("import: commit: %w", err)
	}

	return result, nil
}

type ImportResult struct {
	SessionsImported     int `json:"sessions_imported"`
	ObservationsImported int `json:"observations_imported"`
	PromptsImported      int `json:"prompts_imported"`
}

// ─── Sync Chunk Tracking ─────────────────────────────────────────────────────

// GetSyncedChunks returns a set of chunk IDs that have been imported/exported.
func (s *Store) GetSyncedChunks() (map[string]bool, error) {
	rows, err := s.db.Query("SELECT chunk_id FROM sync_chunks")
	if err != nil {
		return nil, fmt.Errorf("get synced chunks: %w", err)
	}
	defer rows.Close()

	chunks := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		chunks[id] = true
	}
	return chunks, rows.Err()
}

// RecordSyncedChunk marks a chunk as imported/exported so it won't be processed again.
func (s *Store) RecordSyncedChunk(chunkID string) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO sync_chunks (chunk_id) VALUES (?)",
		chunkID,
	)
	return err
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (s *Store) queryObservations(query string, args ...any) ([]Observation, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Observation
	for rows.Next() {
		var o Observation
		if err := rows.Scan(&o.ID, &o.SessionID, &o.Type, &o.Title, &o.Content, &o.ToolName, &o.Project, &o.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, o)
	}
	return results, rows.Err()
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// privateTagRegex matches <private>...</private> tags and their contents.
// Supports multiline and nested content. Case-insensitive.
var privateTagRegex = regexp.MustCompile(`(?is)<private>.*?</private>`)

// stripPrivateTags removes all <private>...</private> content from a string.
// This ensures sensitive information (API keys, passwords, personal data)
// is never persisted to the memory database.
func stripPrivateTags(s string) string {
	result := privateTagRegex.ReplaceAllString(s, "[REDACTED]")
	// Clean up multiple consecutive [REDACTED] and excessive whitespace
	result = strings.TrimSpace(result)
	return result
}

// sanitizeFTS wraps each word in quotes so FTS5 doesn't choke on special chars.
// "fix auth bug" → `"fix" "auth" "bug"`
func sanitizeFTS(query string) string {
	words := strings.Fields(query)
	for i, w := range words {
		// Strip existing quotes to avoid double-quoting
		w = strings.Trim(w, `"`)
		words[i] = `"` + w + `"`
	}
	return strings.Join(words, " ")
}

// ClassifyTool returns the observation type for a given tool name.
func ClassifyTool(toolName string) string {
	switch toolName {
	case "write", "edit", "patch":
		return "file_change"
	case "bash":
		return "command"
	case "read", "view":
		return "file_read"
	case "grep", "glob", "ls":
		return "search"
	default:
		return "tool_use"
	}
}

// Now returns the current time formatted for SQLite.
func Now() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}
