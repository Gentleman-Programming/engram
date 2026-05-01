// Package store implements the persistent memory engine for Engram.
//
// It uses SQLite with FTS5 full-text search to store and retrieve
// observations from AI coding sessions. This is the core of Engram —
// everything else (HTTP server, MCP server, CLI, plugins) talks to this.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
)

var openDB = sql.Open

// sqliteConstraintForeignKey is the extended SQLite result code for a foreign-key
// constraint violation (SQLITE_CONSTRAINT_FOREIGNKEY = 787).
// See https://www.sqlite.org/rescode.html#constraint_foreignkey
const sqliteConstraintForeignKey = 787

const (
	sqlitePrimaryBusy   = 5
	sqlitePrimaryLocked = 6
)

var sqliteWriteRetryBackoffs = []time.Duration{
	10 * time.Millisecond,
	25 * time.Millisecond,
	50 * time.Millisecond,
}

// Sentinel errors returned by delete operations so callers can use errors.Is.
var (
	ErrSessionNotFound        = errors.New("session not found")
	ErrSessionHasObservations = errors.New("session still has observations")
	ErrSessionDeleteBlocked   = errors.New("session deletion is blocked while cloud sync enrollment is active")
	ErrObservationNotFound    = errors.New("observation not found")
	ErrPromptNotFound         = errors.New("prompt not found")
)

// Sentinel errors for relation sync apply path (Phase 2).
var (
	// ErrRelationFKMissing is returned by applyRelationUpsertTx when one or
	// both observations referenced by the relation payload do not exist locally
	// yet. The caller must write the mutation to sync_apply_deferred and ACK
	// the sequence so the cursor does not stall.
	ErrRelationFKMissing = errors.New("relation FK precondition not met: referenced observation missing")

	// ErrCrossProjectRelation is returned by JudgeRelation when the source and
	// target observations belong to different projects. The write is rejected
	// entirely; no memory_relations row is created and no sync mutation is
	// enqueued.
	ErrCrossProjectRelation = errors.New("relation rejected: source and target observations are in different projects")

	// ErrApplyDead is returned when a deferred relation payload cannot be
	// decoded or fails a hard validation. The row is written to
	// sync_apply_deferred with apply_status='dead' and is never retried
	// automatically; Phase 3 adds a republish CLI.
	ErrApplyDead = errors.New("relation apply permanently failed: payload invalid or undecodable")
)

// ─── Types ───────────────────────────────────────────────────────────────────

type Session struct {
	ID        string  `json:"id"`
	Project   string  `json:"project"`
	Directory string  `json:"directory"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
	Summary   *string `json:"summary,omitempty"`
}

type Observation struct {
	ID             int64   `json:"id"`
	SyncID         string  `json:"sync_id"`
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
	ObservationCount int     `json:"observation_count"`
}

type Stats struct {
	TotalSessions     int      `json:"total_sessions"`
	TotalObservations int      `json:"total_observations"`
	TotalPrompts      int      `json:"total_prompts"`
	Projects          []string `json:"projects"`
}

type TimelineEntry struct {
	ID             int64   `json:"id"`
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
	IsFocus        bool    `json:"is_focus"` // true for the anchor observation
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
	Scope   string `json:"scope,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type AddObservationParams struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	ToolName  string `json:"tool_name,omitempty"`
	Project   string `json:"project,omitempty"`
	Scope     string `json:"scope,omitempty"`
	TopicKey  string `json:"topic_key,omitempty"`
}

type UpdateObservationParams struct {
	Type     *string `json:"type,omitempty"`
	Title    *string `json:"title,omitempty"`
	Content  *string `json:"content,omitempty"`
	Project  *string `json:"project,omitempty"`
	Scope    *string `json:"scope,omitempty"`
	TopicKey *string `json:"topic_key,omitempty"`
}

type Prompt struct {
	ID        int64  `json:"id"`
	SyncID    string `json:"sync_id"`
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

const (
	DefaultSyncTargetKey = "cloud"
	LocalChunkTargetKey  = "local"

	SyncLifecycleIdle     = "idle"
	SyncLifecyclePending  = "pending"
	SyncLifecycleRunning  = "running"
	SyncLifecycleHealthy  = "healthy"
	SyncLifecycleDegraded = "degraded"

	SyncEntitySession     = "session"
	SyncEntityObservation = "observation"
	SyncEntityPrompt      = "prompt"
	SyncEntityRelation    = "relation"

	SyncOpUpsert = "upsert"
	SyncOpDelete = "delete"

	SyncSourceLocal  = "local"
	SyncSourceRemote = "remote"

	// Decay defaults — months added to now() to compute review_after on new inserts.
	// expires_at is NULL for all types in Phase 1.
	decayDecisionMonths   = 6
	decayPolicyMonths     = 12
	decayPreferenceMonths = 3
)

// decayReviewAfterMonths maps observation type → month offset for review_after.
// Types absent from this map get review_after = NULL (Phase 1 behavior).
var decayReviewAfterMonths = map[string]int{
	"decision":   decayDecisionMonths,
	"policy":     decayPolicyMonths,
	"preference": decayPreferenceMonths,
}

type SyncState struct {
	TargetKey           string  `json:"target_key"`
	Lifecycle           string  `json:"lifecycle"`
	LastEnqueuedSeq     int64   `json:"last_enqueued_seq"`
	LastAckedSeq        int64   `json:"last_acked_seq"`
	LastPulledSeq       int64   `json:"last_pulled_seq"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	BackoffUntil        *string `json:"backoff_until,omitempty"`
	LeaseOwner          *string `json:"lease_owner,omitempty"`
	LeaseUntil          *string `json:"lease_until,omitempty"`
	ReasonCode          *string `json:"reason_code,omitempty"`
	ReasonMessage       *string `json:"reason_message,omitempty"`
	LastError           *string `json:"last_error,omitempty"`
	UpdatedAt           string  `json:"updated_at"`
}

type SyncMutation struct {
	Seq        int64   `json:"seq"`
	TargetKey  string  `json:"target_key"`
	Entity     string  `json:"entity"`
	EntityKey  string  `json:"entity_key"`
	Op         string  `json:"op"`
	Payload    string  `json:"payload"`
	Source     string  `json:"source"`
	Project    string  `json:"project"`
	OccurredAt string  `json:"occurred_at"`
	AckedAt    *string `json:"acked_at,omitempty"`
}

type PendingSyncMutationProjectCount struct {
	Project string `json:"project"`
	Count   int64  `json:"count"`
}

const (
	UpgradeStagePlanned           = "planned"
	UpgradeStageDoctorReady       = "doctor_ready"
	UpgradeStageDoctorBlocked     = "doctor_blocked"
	UpgradeStageRepairApplied     = "repair_applied"
	UpgradeStageBootstrapEnrolled = "bootstrap_enrolled"
	UpgradeStageBootstrapPushed   = "bootstrap_pushed"
	UpgradeStageBootstrapVerified = "bootstrap_verified"
	UpgradeStageRolledBack        = "rolled_back"

	UpgradeRepairClassNone       = "none"
	UpgradeRepairClassReady      = "ready"
	UpgradeRepairClassRepairable = "repairable"
	UpgradeRepairClassBlocked    = "blocked"
	UpgradeRepairClassPolicy     = "policy"
)

type CloudUpgradeSnapshot struct {
	CloudConfigPresent bool   `json:"cloud_config_present"`
	CloudConfigJSON    string `json:"cloud_config_json,omitempty"`
	ProjectEnrolled    bool   `json:"project_enrolled"`
}

type CloudUpgradeState struct {
	Project          string               `json:"project"`
	Stage            string               `json:"stage"`
	RepairClass      string               `json:"repair_class"`
	Snapshot         CloudUpgradeSnapshot `json:"snapshot"`
	LastErrorCode    string               `json:"last_error_code,omitempty"`
	LastErrorMessage string               `json:"last_error_message,omitempty"`
	FindingsJSON     string               `json:"findings_json,omitempty"`
	AppliedActions   string               `json:"applied_actions,omitempty"`
	UpdatedAt        string               `json:"updated_at"`
}

type CloudUpgradeRepairReport struct {
	Class         string `json:"class"`
	ReasonCode    string `json:"reason_code"`
	Message       string `json:"message"`
	PlannedAction string `json:"planned_action,omitempty"`
	Applied       bool   `json:"applied"`
}

type CloudUpgradeLegacyMutationFinding struct {
	Seq        int64  `json:"seq"`
	Entity     string `json:"entity"`
	Op         string `json:"op"`
	ReasonCode string `json:"reason_code"`
	Message    string `json:"message"`
	Repairable bool   `json:"repairable"`
	RepairHint string `json:"repair_hint,omitempty"`
	EntityKey  string `json:"entity_key,omitempty"`
	TargetKey  string `json:"target_key,omitempty"`
	Project    string `json:"project,omitempty"`
}

type CloudUpgradeLegacyMutationReport struct {
	Project         string                              `json:"project"`
	RepairableCount int                                 `json:"repairable_count"`
	BlockedCount    int                                 `json:"blocked_count"`
	Findings        []CloudUpgradeLegacyMutationFinding `json:"findings,omitempty"`
}

const (
	UpgradeReasonRepairableLegacyMutationPayload = "upgrade_repairable_legacy_mutation_payload"
	UpgradeReasonBlockedLegacyMutationManual     = "upgrade_blocked_legacy_mutation_manual"
)

// EnrolledProject represents a project enrolled for cloud sync.
type EnrolledProject struct {
	Project    string `json:"project"`
	EnrolledAt string `json:"enrolled_at"`
}

type syncSessionPayload struct {
	ID         string  `json:"id"`
	Project    string  `json:"project"`
	Directory  string  `json:"directory,omitempty"`
	StartedAt  string  `json:"started_at,omitempty"`
	EndedAt    *string `json:"ended_at,omitempty"`
	Summary    *string `json:"summary,omitempty"`
	Deleted    bool    `json:"deleted,omitempty"`
	DeletedAt  *string `json:"deleted_at,omitempty"`
	HardDelete bool    `json:"hard_delete,omitempty"`
}

type syncObservationPayload struct {
	SyncID         string  `json:"sync_id"`
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
	CreatedAt      string  `json:"created_at,omitempty"`
	UpdatedAt      string  `json:"updated_at,omitempty"`
	Deleted        bool    `json:"deleted,omitempty"`
	DeletedAt      *string `json:"deleted_at,omitempty"`
	HardDelete     bool    `json:"hard_delete,omitempty"`
}

type syncPromptPayload struct {
	SyncID     string  `json:"sync_id"`
	SessionID  string  `json:"session_id"`
	Content    string  `json:"content"`
	Project    *string `json:"project,omitempty"`
	CreatedAt  string  `json:"created_at,omitempty"`
	Deleted    bool    `json:"deleted,omitempty"`
	DeletedAt  *string `json:"deleted_at,omitempty"`
	HardDelete bool    `json:"hard_delete,omitempty"`
}

// syncRelationPayload is the wire format for a memory_relations row sent over
// the sync_mutations / cloud_mutations rails (entity = 'relation', op = 'upsert').
//
// Phase 2 design §1: 13-field subset of the 17-column memory_relations row.
// Excluded: id (local autoincrement, not portable), superseded_at,
// superseded_by_relation_id (Phase 3 supersede chain).
// omitempty matches the style of syncSessionPayload / syncObservationPayload.
type syncRelationPayload struct {
	SyncID         string   `json:"sync_id"`
	SourceID       string   `json:"source_id"`
	TargetID       string   `json:"target_id"`
	Relation       string   `json:"relation"`
	Reason         *string  `json:"reason,omitempty"`
	Evidence       *string  `json:"evidence,omitempty"`
	Confidence     *float64 `json:"confidence,omitempty"`
	JudgmentStatus string   `json:"judgment_status"`
	MarkedByActor  *string  `json:"marked_by_actor,omitempty"`
	MarkedByKind   *string  `json:"marked_by_kind,omitempty"`
	MarkedByModel  *string  `json:"marked_by_model,omitempty"`
	SessionID      *string  `json:"session_id,omitempty"`
	Project        string   `json:"project"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
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
	DedupeWindow         time.Duration
}

func DefaultConfig() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("engram: determine home directory: %w", err)
	}
	return Config{
		DataDir:              filepath.Join(home, ".engram"),
		MaxObservationLength: 50000,
		MaxContextResults:    20,
		MaxSearchResults:     20,
		DedupeWindow:         15 * time.Minute,
	}, nil
}

// FallbackConfig returns a Config with the given DataDir and default values.
// Use this when DefaultConfig fails and you have resolved the home directory
// through alternative means.
func FallbackConfig(dataDir string) Config {
	return Config{
		DataDir:              dataDir,
		MaxObservationLength: 50000,
		MaxContextResults:    20,
		MaxSearchResults:     20,
		DedupeWindow:         15 * time.Minute,
	}
}

// MaxObservationLength returns the configured maximum content length for observations.
func (s *Store) MaxObservationLength() int {
	return s.cfg.MaxObservationLength
}

// ─── Store ───────────────────────────────────────────────────────────────────

type Store struct {
	db    *sql.DB
	cfg   Config
	hooks storeHooks
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

type sqlRowScanner struct {
	rows *sql.Rows
}

func (r sqlRowScanner) Next() bool {
	return r.rows.Next()
}

func (r sqlRowScanner) Scan(dest ...any) error {
	return r.rows.Scan(dest...)
}

func (r sqlRowScanner) Err() error {
	return r.rows.Err()
}

func (r sqlRowScanner) Close() error {
	return r.rows.Close()
}

func closeRowsWithError(rows rowScanner, err error) error {
	if closeErr := rows.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}

type storeHooks struct {
	exec    func(db execer, query string, args ...any) (sql.Result, error)
	query   func(db queryer, query string, args ...any) (*sql.Rows, error)
	queryIt func(db queryer, query string, args ...any) (rowScanner, error)
	beginTx func(db *sql.DB) (*sql.Tx, error)
	commit  func(tx *sql.Tx) error
}

func defaultStoreHooks() storeHooks {
	return storeHooks{
		exec: func(db execer, query string, args ...any) (sql.Result, error) {
			return db.Exec(query, args...)
		},
		query: func(db queryer, query string, args ...any) (*sql.Rows, error) {
			return db.Query(query, args...)
		},
		queryIt: func(db queryer, query string, args ...any) (rowScanner, error) {
			rows, err := db.Query(query, args...)
			if err != nil {
				return nil, err
			}
			return sqlRowScanner{rows: rows}, nil
		},
		beginTx: func(db *sql.DB) (*sql.Tx, error) {
			return db.Begin()
		},
		commit: func(tx *sql.Tx) error {
			return tx.Commit()
		},
	}
}

func (s *Store) execHook(db execer, query string, args ...any) (sql.Result, error) {
	if s.hooks.exec != nil {
		return s.hooks.exec(db, query, args...)
	}
	return db.Exec(query, args...)
}

func (s *Store) queryHook(db queryer, query string, args ...any) (*sql.Rows, error) {
	if s.hooks.query != nil {
		return s.hooks.query(db, query, args...)
	}
	return db.Query(query, args...)
}

func (s *Store) queryItHook(db queryer, query string, args ...any) (rowScanner, error) {
	if s.hooks.queryIt != nil {
		return s.hooks.queryIt(db, query, args...)
	}
	rows, err := s.queryHook(db, query, args...)
	if err != nil {
		return nil, err
	}
	return sqlRowScanner{rows: rows}, nil
}

func (s *Store) beginTxHook() (*sql.Tx, error) {
	if s.hooks.beginTx != nil {
		return s.hooks.beginTx(s.db)
	}
	return s.db.Begin()
}

func (s *Store) commitHook(tx *sql.Tx) error {
	if s.hooks.commit != nil {
		return s.hooks.commit(tx)
	}
	return tx.Commit()
}

func New(cfg Config) (*Store, error) {
	if !filepath.IsAbs(cfg.DataDir) {
		return nil, fmt.Errorf("engram: data directory must be an absolute path, got %q — set ENGRAM_DATA_DIR or ensure your home directory is resolvable", cfg.DataDir)
	}
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("engram: create data dir: %w", err)
	}

	dbPath := filepath.Join(cfg.DataDir, "engram.db")
	db, err := openDB("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("engram: open database: %w", err)
	}
	db.SetMaxOpenConns(1)

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

	s := &Store{db: db, cfg: cfg, hooks: defaultStoreHooks()}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("engram: migration: %w", err)
	}
	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		return nil, fmt.Errorf("engram: repair enrolled sync journal: %w", err)
	}

	return s, nil
}

// newWithoutRepair is the same as New but skips repairEnrolledProjectSyncMutations.
// It exists solely to support tests that need to seed data and call repair manually.
func newWithoutRepair(cfg Config) (*Store, error) {
	if !filepath.IsAbs(cfg.DataDir) {
		return nil, fmt.Errorf("engram: data directory must be an absolute path, got %q — set ENGRAM_DATA_DIR or ensure your home directory is resolvable", cfg.DataDir)
	}
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("engram: create data dir: %w", err)
	}

	dbPath := filepath.Join(cfg.DataDir, "engram.db")
	db, err := openDB("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("engram: open database: %w", err)
	}
	db.SetMaxOpenConns(1)

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

	s := &Store{db: db, cfg: cfg, hooks: defaultStoreHooks()}
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
			summary    TEXT
		);

			CREATE TABLE IF NOT EXISTS observations (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				sync_id    TEXT,
				session_id TEXT    NOT NULL,
			type       TEXT    NOT NULL,
			title      TEXT    NOT NULL,
			content    TEXT    NOT NULL,
			tool_name  TEXT,
			project    TEXT,
			scope      TEXT    NOT NULL DEFAULT 'project',
			topic_key  TEXT,
			normalized_hash TEXT,
			revision_count INTEGER NOT NULL DEFAULT 1,
			duplicate_count INTEGER NOT NULL DEFAULT 1,
			last_seen_at TEXT,
			created_at TEXT    NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT    NOT NULL DEFAULT (datetime('now')),
			deleted_at TEXT,
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
				occurred_at TEXT NOT NULL DEFAULT (datetime('now')),
				acked_at    TEXT,
				FOREIGN KEY (target_key) REFERENCES sync_state(target_key)
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
		`
	if _, err := s.execHook(s.db, schema); err != nil {
		return err
	}

	observationColumns := []struct {
		name       string
		definition string
	}{
		{name: "sync_id", definition: "TEXT"},
		{name: "scope", definition: "TEXT NOT NULL DEFAULT 'project'"},
		{name: "topic_key", definition: "TEXT"},
		{name: "normalized_hash", definition: "TEXT"},
		{name: "revision_count", definition: "INTEGER NOT NULL DEFAULT 1"},
		{name: "duplicate_count", definition: "INTEGER NOT NULL DEFAULT 1"},
		{name: "last_seen_at", definition: "TEXT"},
		{name: "updated_at", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "deleted_at", definition: "TEXT"},
	}
	for _, c := range observationColumns {
		if err := s.addColumnIfNotExists("observations", c.name, c.definition); err != nil {
			return err
		}
	}

	if err := s.migrateLegacyObservationsTable(); err != nil {
		return err
	}

	if err := s.addColumnIfNotExists("user_prompts", "sync_id", "TEXT"); err != nil {
		return err
	}

	if _, err := s.execHook(s.db, `
		CREATE INDEX IF NOT EXISTS idx_obs_scope ON observations(scope);
		CREATE INDEX IF NOT EXISTS idx_obs_sync_id ON observations(sync_id);
		CREATE INDEX IF NOT EXISTS idx_obs_topic ON observations(topic_key, project, scope, updated_at DESC);
		CREATE INDEX IF NOT EXISTS idx_obs_deleted ON observations(deleted_at);
		CREATE INDEX IF NOT EXISTS idx_obs_dedupe ON observations(normalized_hash, project, scope, type, title, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_prompts_sync_id ON user_prompts(sync_id);
		CREATE INDEX IF NOT EXISTS idx_prompt_tombstones_project ON prompt_tombstones(project, deleted_at DESC);
		CREATE INDEX IF NOT EXISTS idx_sync_mutations_target_seq ON sync_mutations(target_key, seq);
		CREATE INDEX IF NOT EXISTS idx_sync_mutations_pending ON sync_mutations(target_key, acked_at, seq);
	`); err != nil {
		return err
	}

	// Project-scoped sync: add project column to sync_mutations and enrollment table.
	if err := s.addColumnIfNotExists("sync_mutations", "project", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfNotExists("sync_state", "reason_code", "TEXT"); err != nil {
		return err
	}
	if err := s.addColumnIfNotExists("sync_state", "reason_message", "TEXT"); err != nil {
		return err
	}
	if err := s.migrateSyncChunksTable(); err != nil {
		return err
	}

	// ── Phase: memory-conflict-surfacing — B.1 ──────────────────────────────
	// Additive nullable columns on observations for conflict surfacing, decay,
	// and embedding reservation.  All applied via addColumnIfNotExists so that
	// running migrate() on a fresh DB (where CREATE TABLE already added these
	// columns) is a no-op.
	memConflictObsCols := []struct {
		name       string
		definition string
	}{
		{name: "review_after", definition: "TEXT"},
		{name: "expires_at", definition: "TEXT"},
		{name: "embedding", definition: "BLOB"},
		{name: "embedding_model", definition: "TEXT"},
		{name: "embedding_created_at", definition: "TEXT"},
	}
	for _, c := range memConflictObsCols {
		if err := s.addColumnIfNotExists("observations", c.name, c.definition); err != nil {
			return err
		}
	}

	// ── Phase: memory-conflict-surfacing — B.2 ──────────────────────────────
	// Create the memory_relations table (idempotent via IF NOT EXISTS).
	// source_id / target_id are TEXT sync_id keys (cross-machine portable).
	// NO UNIQUE on (source_id, target_id) — multi-actor disagreement allowed.
	if _, err := s.execHook(s.db, `
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
	`); err != nil {
		return err
	}

	// ── Phase: memory-conflict-surfacing — B.3 ──────────────────────────────
	// Indexes for memory_relations (all idempotent via IF NOT EXISTS).
	if _, err := s.execHook(s.db, `
		CREATE INDEX IF NOT EXISTS idx_memrel_source    ON memory_relations(source_id, judgment_status);
		CREATE INDEX IF NOT EXISTS idx_memrel_target    ON memory_relations(target_id, judgment_status);
		CREATE INDEX IF NOT EXISTS idx_memrel_supersede ON memory_relations(superseded_by_relation_id);
	`); err != nil {
		return err
	}

	if _, err := s.execHook(s.db, `
		CREATE TABLE IF NOT EXISTS sync_enrolled_projects (
			project     TEXT PRIMARY KEY,
			enrolled_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_sync_mutations_project ON sync_mutations(project);
	`); err != nil {
		return err
	}
	// Backfill: extract project from JSON payload for existing rows with empty project.
	if _, err := s.execHook(s.db, `
		UPDATE sync_mutations
		SET project = COALESCE(json_extract(payload, '$.project'), '')
		WHERE project = '' AND payload != ''
	`); err != nil {
		return err
	}
	if _, err := s.execHook(s.db, `
		UPDATE sync_mutations
		SET project = COALESCE((
			SELECT sessions.project
			FROM sessions
			WHERE sessions.id = json_extract(sync_mutations.payload, '$.session_id')
		), '')
		WHERE project = ''
		  AND payload != ''
		  AND ifnull(json_extract(payload, '$.session_id'), '') != ''
	`); err != nil {
		return err
	}

	if _, err := s.execHook(s.db, `UPDATE observations SET scope = 'project' WHERE scope IS NULL OR scope = ''`); err != nil {
		return err
	}
	if _, err := s.execHook(s.db, `UPDATE observations SET topic_key = NULL WHERE topic_key = ''`); err != nil {
		return err
	}
	if _, err := s.execHook(s.db, `UPDATE observations SET revision_count = 1 WHERE revision_count IS NULL OR revision_count < 1`); err != nil {
		return err
	}
	if _, err := s.execHook(s.db, `UPDATE observations SET duplicate_count = 1 WHERE duplicate_count IS NULL OR duplicate_count < 1`); err != nil {
		return err
	}
	if _, err := s.execHook(s.db, `UPDATE observations SET updated_at = created_at WHERE updated_at IS NULL OR updated_at = ''`); err != nil {
		return err
	}
	if _, err := s.execHook(s.db, `UPDATE observations SET sync_id = 'obs-' || lower(hex(randomblob(16))) WHERE sync_id IS NULL OR sync_id = ''`); err != nil {
		return err
	}

	if _, err := s.execHook(s.db, `UPDATE user_prompts SET project = '' WHERE project IS NULL`); err != nil {
		return err
	}
	if _, err := s.execHook(s.db, `UPDATE prompt_tombstones SET project = '' WHERE project IS NULL`); err != nil {
		return err
	}
	if _, err := s.execHook(s.db, `UPDATE user_prompts SET sync_id = 'prompt-' || lower(hex(randomblob(16))) WHERE sync_id IS NULL OR sync_id = ''`); err != nil {
		return err
	}
	if _, err := s.execHook(s.db, `INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at) VALUES ('cloud', 'idle', datetime('now'))`); err != nil {
		return err
	}
	if _, err := s.execHook(s.db, `
		CREATE INDEX IF NOT EXISTS idx_cloud_upgrade_state_stage ON cloud_upgrade_state(stage);
		CREATE INDEX IF NOT EXISTS idx_sync_mutations_lookup ON sync_mutations(target_key, entity, entity_key, source);
	`); err != nil {
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
		`
		if _, err := s.execHook(s.db, triggers); err != nil {
			return err
		}
	}

	if err := s.migrateFTSTopicKey(); err != nil {
		return err
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
		if _, err := s.execHook(s.db, promptTriggers); err != nil {
			return err
		}
	}

	// Phase 3: add republish CLI, surface dead rows via mem_status.
	if _, err := s.execHook(s.db, `
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
		CREATE INDEX IF NOT EXISTS idx_sad_status_seen
			ON sync_apply_deferred(apply_status, first_seen_at);
	`); err != nil {
		return err
	}

	// Phase 3b: composite index for conflict-audit list/count queries.
	if _, err := s.execHook(s.db, `
		CREATE INDEX IF NOT EXISTS idx_memrel_status_created
			ON memory_relations(judgment_status, created_at DESC);
	`); err != nil {
		return err
	}

	return nil
}

func (s *Store) SaveCloudUpgradeState(state CloudUpgradeState) error {
	project, _ := NormalizeProject(state.Project)
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("cloud upgrade project must not be empty")
	}
	state.Project = project
	state.Stage = normalizeUpgradeStage(state.Stage)
	state.RepairClass = normalizeUpgradeRepairClass(state.RepairClass)

	snapshotJSON, err := json.Marshal(state.Snapshot)
	if err != nil {
		return fmt.Errorf("marshal cloud upgrade snapshot: %w", err)
	}

	_, err = s.execHook(s.db, `
		INSERT INTO cloud_upgrade_state (
			project, stage, repair_class, snapshot_json, last_error_code, last_error_message, findings_json, applied_actions, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(project) DO UPDATE SET
			stage = excluded.stage,
			repair_class = excluded.repair_class,
			snapshot_json = excluded.snapshot_json,
			last_error_code = excluded.last_error_code,
			last_error_message = excluded.last_error_message,
			findings_json = excluded.findings_json,
			applied_actions = excluded.applied_actions,
			updated_at = datetime('now')
	`, state.Project, state.Stage, state.RepairClass, string(snapshotJSON), nullableString(state.LastErrorCode), nullableString(state.LastErrorMessage), nullableString(state.FindingsJSON), nullableString(state.AppliedActions))
	return err
}

func (s *Store) GetCloudUpgradeState(project string) (*CloudUpgradeState, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, nil
	}

	row := s.db.QueryRow(`
		SELECT project, stage, repair_class, snapshot_json, ifnull(last_error_code, ''), ifnull(last_error_message, ''), ifnull(findings_json, ''), ifnull(applied_actions, ''), updated_at
		FROM cloud_upgrade_state
		WHERE project = ?
	`, project)

	var state CloudUpgradeState
	var snapshotJSON string
	if err := row.Scan(&state.Project, &state.Stage, &state.RepairClass, &snapshotJSON, &state.LastErrorCode, &state.LastErrorMessage, &state.FindingsJSON, &state.AppliedActions, &state.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(snapshotJSON) != "" {
		if err := json.Unmarshal([]byte(snapshotJSON), &state.Snapshot); err != nil {
			return nil, fmt.Errorf("parse cloud upgrade snapshot: %w", err)
		}
	}
	state.Stage = normalizeUpgradeStage(state.Stage)
	state.RepairClass = normalizeUpgradeRepairClass(state.RepairClass)
	return &state, nil
}

func (s *Store) ClearCloudUpgradeState(project string) error {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return nil
	}
	_, err := s.execHook(s.db, `DELETE FROM cloud_upgrade_state WHERE project = ?`, project)
	return err
}

func (s *Store) CanRollbackCloudUpgrade(project string) (bool, error) {
	state, err := s.GetCloudUpgradeState(project)
	if err != nil {
		return false, err
	}
	if state == nil {
		return false, nil
	}
	return state.Stage != UpgradeStageBootstrapVerified, nil
}

func (s *Store) RollbackCloudUpgrade(project string) (CloudUpgradeState, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return CloudUpgradeState{}, fmt.Errorf("cloud upgrade rollback requires project")
	}

	state, err := s.GetCloudUpgradeState(project)
	if err != nil {
		return CloudUpgradeState{}, fmt.Errorf("read cloud upgrade rollback state: %w", err)
	}
	if state == nil {
		return CloudUpgradeState{}, fmt.Errorf("rollback requires existing upgrade checkpoint state")
	}
	if state.Stage == UpgradeStageBootstrapVerified {
		return CloudUpgradeState{}, fmt.Errorf("rollback is unavailable post-bootstrap; use explicit disconnect/unenroll flows")
	}

	if state.Snapshot.ProjectEnrolled {
		if err := s.EnrollProject(project); err != nil {
			return CloudUpgradeState{}, fmt.Errorf("restore project enrollment from rollback snapshot: %w", err)
		}
	} else {
		if err := s.UnenrollProject(project); err != nil {
			return CloudUpgradeState{}, fmt.Errorf("restore project unenrollment from rollback snapshot: %w", err)
		}
	}

	state.Stage = UpgradeStageRolledBack
	state.LastErrorCode = ""
	state.LastErrorMessage = ""
	state.FindingsJSON = ""
	state.AppliedActions = ""
	if err := s.SaveCloudUpgradeState(*state); err != nil {
		return CloudUpgradeState{}, fmt.Errorf("persist rolled back upgrade state: %w", err)
	}

	rolledBack, err := s.GetCloudUpgradeState(project)
	if err != nil {
		return CloudUpgradeState{}, fmt.Errorf("load rolled back cloud upgrade state: %w", err)
	}
	if rolledBack == nil {
		return CloudUpgradeState{}, fmt.Errorf("rolled back cloud upgrade state not found")
	}
	return *rolledBack, nil
}

func (s *Store) RepairCloudUpgrade(project string, apply bool) (CloudUpgradeRepairReport, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return CloudUpgradeRepairReport{
			Class:      UpgradeRepairClassBlocked,
			ReasonCode: "upgrade_blocked_project_required",
			Message:    "project is required for cloud upgrade repair",
		}, nil
	}

	if blocked, report, err := s.cloudUpgradeManualActionReport(project); err != nil {
		return CloudUpgradeRepairReport{}, err
	} else if blocked {
		return report, nil
	}

	enrolled, err := s.IsProjectEnrolled(project)
	if err != nil {
		return CloudUpgradeRepairReport{}, fmt.Errorf("check project enrollment: %w", err)
	}
	if !enrolled {
		return CloudUpgradeRepairReport{
			Class:      UpgradeRepairClassBlocked,
			ReasonCode: "upgrade_blocked_manual",
			Message:    fmt.Sprintf("project %q is not enrolled; run doctor/bootstrap guidance first", project),
		}, nil
	}

	legacyReport, err := s.DiagnoseCloudUpgradeLegacyMutations(project)
	if err != nil {
		return CloudUpgradeRepairReport{}, fmt.Errorf("diagnose legacy cloud upgrade mutations: %w", err)
	}
	if legacyReport.BlockedCount > 0 {
		first := legacyReport.Findings[0]
		return CloudUpgradeRepairReport{
			Class:      UpgradeRepairClassBlocked,
			ReasonCode: UpgradeReasonBlockedLegacyMutationManual,
			Message:    fmt.Sprintf("manual-action-required: %s (seq=%d entity=%s op=%s)", first.Message, first.Seq, first.Entity, first.Op),
			Applied:    false,
		}, nil
	}
	if legacyReport.RepairableCount > 0 {
		report := CloudUpgradeRepairReport{
			Class:         UpgradeRepairClassRepairable,
			ReasonCode:    UpgradeReasonRepairableLegacyMutationPayload,
			Message:       fmt.Sprintf("project %q has %d repairable legacy mutation payload issue(s)", project, legacyReport.RepairableCount),
			PlannedAction: "repair_legacy_mutation_payloads",
			Applied:       false,
		}
		if !apply {
			return report, nil
		}
		if err := s.applyCloudUpgradeLegacyMutationRepairs(project); err != nil {
			return CloudUpgradeRepairReport{}, fmt.Errorf("apply cloud upgrade legacy mutation repairs: %w", err)
		}
		report.Applied = true
		report.Message = fmt.Sprintf("applied deterministic legacy mutation payload repairs for project %q", project)
		_ = s.SaveCloudUpgradeState(CloudUpgradeState{
			Project:     project,
			Stage:       UpgradeStageRepairApplied,
			RepairClass: UpgradeRepairClassRepairable,
		})
		return report, nil
	}

	requiresBackfill, err := s.projectSyncBackfillRequired(project)
	if err != nil {
		return CloudUpgradeRepairReport{}, err
	}
	if !requiresBackfill {
		return CloudUpgradeRepairReport{
			Class:      UpgradeRepairClassReady,
			ReasonCode: "upgrade_repair_noop",
			Message:    fmt.Sprintf("project %q has no deterministic local repairs to apply", project),
		}, nil
	}

	report := CloudUpgradeRepairReport{
		Class:         UpgradeRepairClassRepairable,
		ReasonCode:    "upgrade_repair_backfill_sync_journal",
		Message:       fmt.Sprintf("project %q has deterministic local sync metadata gaps", project),
		PlannedAction: "backfill_sync_journal",
		Applied:       false,
	}
	if !apply {
		return report, nil
	}

	if err := s.withTx(func(tx *sql.Tx) error {
		return s.backfillProjectSyncMutationsTx(tx, project)
	}); err != nil {
		return CloudUpgradeRepairReport{}, fmt.Errorf("apply cloud upgrade repair: %w", err)
	}
	report.Applied = true
	_ = s.SaveCloudUpgradeState(CloudUpgradeState{
		Project:     project,
		Stage:       UpgradeStageRepairApplied,
		RepairClass: UpgradeRepairClassRepairable,
	})
	return report, nil
}

type cloudUpgradeLegacyMutationEvaluation struct {
	finding         CloudUpgradeLegacyMutationFinding
	hasIssue        bool
	repairedPayload string
	canRepair       bool
}

func (s *Store) DiagnoseCloudUpgradeLegacyMutations(project string) (CloudUpgradeLegacyMutationReport, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return CloudUpgradeLegacyMutationReport{Project: project}, nil
	}

	evaluations, err := s.evaluateCloudUpgradeLegacyMutations(project)
	if err != nil {
		return CloudUpgradeLegacyMutationReport{}, err
	}
	report := CloudUpgradeLegacyMutationReport{Project: project}
	for _, eval := range evaluations {
		if !eval.hasIssue {
			continue
		}
		report.Findings = append(report.Findings, eval.finding)
		if eval.canRepair {
			report.RepairableCount++
		} else {
			report.BlockedCount++
		}
	}
	return report, nil
}

func (s *Store) applyCloudUpgradeLegacyMutationRepairs(project string) error {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return nil
	}
	return s.withTx(func(tx *sql.Tx) error {
		mutations, err := s.listPendingProjectMutationsTx(tx, project)
		if err != nil {
			return err
		}
		for _, mutation := range mutations {
			eval, err := s.evaluateCloudUpgradeLegacyMutationTx(tx, mutation)
			if err != nil {
				return err
			}
			if !eval.hasIssue || !eval.canRepair || strings.TrimSpace(eval.repairedPayload) == "" {
				continue
			}
			if _, err := s.execHook(tx,
				`UPDATE sync_mutations SET payload = ? WHERE target_key = ? AND project = ? AND seq = ? AND acked_at IS NULL`,
				eval.repairedPayload,
				DefaultSyncTargetKey,
				project,
				mutation.Seq,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) evaluateCloudUpgradeLegacyMutations(project string) ([]cloudUpgradeLegacyMutationEvaluation, error) {
	return s.withReadTx(func(tx *sql.Tx) ([]cloudUpgradeLegacyMutationEvaluation, error) {
		mutations, err := s.listPendingProjectMutationsTx(tx, project)
		if err != nil {
			return nil, err
		}
		evaluations := make([]cloudUpgradeLegacyMutationEvaluation, 0, len(mutations))
		for _, mutation := range mutations {
			eval, err := s.evaluateCloudUpgradeLegacyMutationTx(tx, mutation)
			if err != nil {
				return nil, err
			}
			evaluations = append(evaluations, eval)
		}
		return evaluations, nil
	})
}

func (s *Store) withReadTx(fn func(tx *sql.Tx) ([]cloudUpgradeLegacyMutationEvaluation, error)) ([]cloudUpgradeLegacyMutationEvaluation, error) {
	tx, err := s.beginTxHook()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return fn(tx)
}

func (s *Store) listPendingProjectMutationsTx(tx *sql.Tx, project string) ([]SyncMutation, error) {
	rows, err := s.queryItHook(tx, `
		SELECT seq, target_key, entity, entity_key, op, payload, source, project, occurred_at, acked_at
		FROM sync_mutations
		WHERE target_key = ? AND project = ? AND acked_at IS NULL
		ORDER BY seq ASC
	`, DefaultSyncTargetKey, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	mutations := make([]SyncMutation, 0)
	for rows.Next() {
		var m SyncMutation
		if err := rows.Scan(&m.Seq, &m.TargetKey, &m.Entity, &m.EntityKey, &m.Op, &m.Payload, &m.Source, &m.Project, &m.OccurredAt, &m.AckedAt); err != nil {
			return nil, err
		}
		mutations = append(mutations, m)
	}
	return mutations, rows.Err()
}

func (s *Store) evaluateCloudUpgradeLegacyMutationTx(tx *sql.Tx, mutation SyncMutation) (cloudUpgradeLegacyMutationEvaluation, error) {
	entity := strings.TrimSpace(mutation.Entity)
	op := strings.TrimSpace(mutation.Op)
	payload := strings.TrimSpace(mutation.Payload)
	base := CloudUpgradeLegacyMutationFinding{
		Seq:       mutation.Seq,
		Entity:    entity,
		Op:        op,
		EntityKey: strings.TrimSpace(mutation.EntityKey),
		TargetKey: strings.TrimSpace(mutation.TargetKey),
		Project:   strings.TrimSpace(mutation.Project),
	}

	repairable := func(msg, hint string, repairedPayload string) cloudUpgradeLegacyMutationEvaluation {
		finding := base
		finding.Repairable = true
		finding.ReasonCode = UpgradeReasonRepairableLegacyMutationPayload
		finding.Message = msg
		finding.RepairHint = hint
		return cloudUpgradeLegacyMutationEvaluation{finding: finding, hasIssue: true, canRepair: true, repairedPayload: repairedPayload}
	}
	blocked := func(code, msg string) cloudUpgradeLegacyMutationEvaluation {
		finding := base
		finding.Repairable = false
		finding.ReasonCode = code
		finding.Message = msg
		return cloudUpgradeLegacyMutationEvaluation{finding: finding, hasIssue: true, canRepair: false}
	}

	if payload == "" {
		return blocked(UpgradeReasonBlockedLegacyMutationManual, "legacy mutation payload is empty"), nil
	}

	supported := (entity == SyncEntitySession && (op == SyncOpUpsert || op == SyncOpDelete)) ||
		((entity == SyncEntityObservation || entity == SyncEntityPrompt) && (op == SyncOpUpsert || op == SyncOpDelete))
	if !supported {
		return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("unsupported legacy mutation %q/%q", entity, op)), nil
	}

	switch entity {
	case SyncEntitySession:
		var body syncSessionPayload
		if err := decodeSyncPayload([]byte(payload), &body); err != nil {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("decode session payload: %v", err)), nil
		}
		body.ID = strings.TrimSpace(body.ID)
		body.Directory = strings.TrimSpace(body.Directory)
		changed := false
		if body.ID == "" && strings.TrimSpace(mutation.EntityKey) != "" {
			body.ID = strings.TrimSpace(mutation.EntityKey)
			changed = true
		}
		if body.ID == "" {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, "session payload id is required"), nil
		}
		if strings.TrimSpace(mutation.EntityKey) != "" && strings.TrimSpace(mutation.EntityKey) != body.ID {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("session entity_key %q does not match payload id %q", mutation.EntityKey, body.ID)), nil
		}
		if op == SyncOpUpsert && body.Directory == "" {
			var directory string
			err := tx.QueryRow(`SELECT ifnull(directory, '') FROM sessions WHERE id = ?`, body.ID).Scan(&directory)
			if errors.Is(err, sql.ErrNoRows) || strings.TrimSpace(directory) == "" {
				return blocked(UpgradeReasonBlockedLegacyMutationManual, "session payload directory is required and cannot be inferred from local state"), nil
			}
			if err != nil {
				return cloudUpgradeLegacyMutationEvaluation{}, err
			}
			body.Directory = strings.TrimSpace(directory)
			changed = true
		}
		if !changed {
			return cloudUpgradeLegacyMutationEvaluation{}, nil
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return cloudUpgradeLegacyMutationEvaluation{}, err
		}
		return repairable("session payload is missing required fields", "repair fills session id/directory from local sessions table", string(encoded)), nil

	case SyncEntityObservation:
		var body syncObservationPayload
		if err := decodeSyncPayload([]byte(payload), &body); err != nil {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("decode observation payload: %v", err)), nil
		}
		body.SyncID = strings.TrimSpace(body.SyncID)
		body.SessionID = strings.TrimSpace(body.SessionID)
		body.Type = strings.TrimSpace(body.Type)
		body.Title = strings.TrimSpace(body.Title)
		body.Content = strings.TrimSpace(body.Content)
		body.Scope = strings.TrimSpace(body.Scope)
		changed := false
		if body.SyncID == "" && strings.TrimSpace(mutation.EntityKey) != "" {
			body.SyncID = strings.TrimSpace(mutation.EntityKey)
			changed = true
		}
		if body.SyncID == "" {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, "observation payload sync_id is required"), nil
		}
		if strings.TrimSpace(mutation.EntityKey) != "" && strings.TrimSpace(mutation.EntityKey) != body.SyncID {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("observation entity_key %q does not match payload sync_id %q", mutation.EntityKey, body.SyncID)), nil
		}
		if op == SyncOpUpsert {
			obs, err := s.getObservationBySyncIDTx(tx, body.SyncID, true)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return cloudUpgradeLegacyMutationEvaluation{}, err
			}
			if strings.TrimSpace(body.SessionID) == "" && obs != nil && strings.TrimSpace(obs.SessionID) != "" {
				body.SessionID = strings.TrimSpace(obs.SessionID)
				changed = true
			}
			if strings.TrimSpace(body.Type) == "" && obs != nil && strings.TrimSpace(obs.Type) != "" {
				body.Type = strings.TrimSpace(obs.Type)
				changed = true
			}
			if strings.TrimSpace(body.Title) == "" && obs != nil && strings.TrimSpace(obs.Title) != "" {
				body.Title = strings.TrimSpace(obs.Title)
				changed = true
			}
			if strings.TrimSpace(body.Content) == "" && obs != nil && strings.TrimSpace(obs.Content) != "" {
				body.Content = strings.TrimSpace(obs.Content)
				changed = true
			}
			if strings.TrimSpace(body.Scope) == "" && obs != nil && strings.TrimSpace(obs.Scope) != "" {
				body.Scope = strings.TrimSpace(obs.Scope)
				changed = true
			}
			missing := []string{}
			if strings.TrimSpace(body.SessionID) == "" {
				missing = append(missing, "session_id")
			}
			if strings.TrimSpace(body.Type) == "" {
				missing = append(missing, "type")
			}
			if strings.TrimSpace(body.Title) == "" {
				missing = append(missing, "title")
			}
			if strings.TrimSpace(body.Content) == "" {
				missing = append(missing, "content")
			}
			if strings.TrimSpace(body.Scope) == "" {
				missing = append(missing, "scope")
			}
			if len(missing) > 0 {
				return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("observation payload missing required upsert fields: %s", strings.Join(missing, ", "))), nil
			}
		}
		if !changed {
			return cloudUpgradeLegacyMutationEvaluation{}, nil
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return cloudUpgradeLegacyMutationEvaluation{}, err
		}
		return repairable("observation payload is missing required fields for canonical bootstrap", "repair fills missing observation fields from local observations table", string(encoded)), nil

	case SyncEntityPrompt:
		var body syncPromptPayload
		if err := decodeSyncPayload([]byte(payload), &body); err != nil {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("decode prompt payload: %v", err)), nil
		}
		body.SyncID = strings.TrimSpace(body.SyncID)
		body.SessionID = strings.TrimSpace(body.SessionID)
		body.Content = strings.TrimSpace(body.Content)
		changed := false
		if body.SyncID == "" && strings.TrimSpace(mutation.EntityKey) != "" {
			body.SyncID = strings.TrimSpace(mutation.EntityKey)
			changed = true
		}
		if body.SyncID == "" {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, "prompt payload sync_id is required"), nil
		}
		if strings.TrimSpace(mutation.EntityKey) != "" && strings.TrimSpace(mutation.EntityKey) != body.SyncID {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("prompt entity_key %q does not match payload sync_id %q", mutation.EntityKey, body.SyncID)), nil
		}
		if op == SyncOpUpsert {
			var local syncPromptPayload
			err := tx.QueryRow(
				`SELECT sync_id, session_id, content, project, created_at FROM user_prompts WHERE sync_id = ? ORDER BY id DESC LIMIT 1`,
				body.SyncID,
			).Scan(&local.SyncID, &local.SessionID, &local.Content, &local.Project, &local.CreatedAt)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return cloudUpgradeLegacyMutationEvaluation{}, err
			}
			if strings.TrimSpace(body.SessionID) == "" && err == nil && strings.TrimSpace(local.SessionID) != "" {
				body.SessionID = strings.TrimSpace(local.SessionID)
				changed = true
			}
			if strings.TrimSpace(body.Content) == "" && err == nil && strings.TrimSpace(local.Content) != "" {
				body.Content = strings.TrimSpace(local.Content)
				changed = true
			}
			missing := []string{}
			if strings.TrimSpace(body.SessionID) == "" {
				missing = append(missing, "session_id")
			}
			if strings.TrimSpace(body.Content) == "" {
				missing = append(missing, "content")
			}
			if len(missing) > 0 {
				return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("prompt payload missing required upsert fields: %s", strings.Join(missing, ", "))), nil
			}
		}
		if !changed {
			return cloudUpgradeLegacyMutationEvaluation{}, nil
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return cloudUpgradeLegacyMutationEvaluation{}, err
		}
		return repairable("prompt payload is missing required fields for canonical bootstrap", "repair fills missing prompt fields from local prompts table", string(encoded)), nil
	}

	return cloudUpgradeLegacyMutationEvaluation{}, nil
}

func (s *Store) cloudUpgradeManualActionReport(project string) (bool, CloudUpgradeRepairReport, error) {
	targetKey := DefaultSyncTargetKey
	if project != "" {
		targetKey = fmt.Sprintf("%s:%s", DefaultSyncTargetKey, project)
	}
	state, err := s.GetSyncState(targetKey)
	if err != nil {
		return false, CloudUpgradeRepairReport{}, fmt.Errorf("read sync state for cloud upgrade repair: %w", err)
	}
	if state == nil {
		return false, CloudUpgradeRepairReport{}, nil
	}
	reasonCode := strings.TrimSpace(derefString(state.ReasonCode))
	if reasonCode == "" {
		return false, CloudUpgradeRepairReport{}, nil
	}

	reasonMap := map[string]string{
		"auth_required":      "upgrade_policy_auth_required",
		"policy_forbidden":   "upgrade_policy_forbidden",
		"cloud_config_error": "upgrade_policy_cloud_config_error",
	}
	repairReasonCode, requiresManualAction := reasonMap[reasonCode]
	if !requiresManualAction {
		return false, CloudUpgradeRepairReport{}, nil
	}
	reasonMessage := strings.TrimSpace(derefString(state.ReasonMessage))
	if reasonMessage == "" {
		reasonMessage = "cloud policy/auth precondition must be resolved before repair"
	}
	return true, CloudUpgradeRepairReport{
		Class:      UpgradeRepairClassPolicy,
		ReasonCode: repairReasonCode,
		Message:    fmt.Sprintf("manual-action-required: %s", reasonMessage),
		Applied:    false,
	}, nil
}

func (s *Store) projectSyncBackfillRequired(project string) (bool, error) {
	var missing int
	err := s.db.QueryRow(`
		SELECT EXISTS(
			SELECT 1
			FROM sessions sess
			WHERE sess.project = ?
			  AND NOT EXISTS (
				SELECT 1 FROM sync_mutations sm
				WHERE sm.target_key = ?
				  AND sm.entity = ?
				  AND sm.entity_key = sess.id
				  AND sm.source = ?
			  )
			UNION ALL
			SELECT 1
			FROM observations obs
			LEFT JOIN sessions sess ON sess.id = obs.session_id
			WHERE (
				ifnull(obs.project, '') = ?
				OR (ifnull(obs.project, '') = '' AND ifnull(sess.project, '') = ?)
			)
			  AND obs.deleted_at IS NULL
			  AND NOT EXISTS (
				SELECT 1 FROM sync_mutations sm
				WHERE sm.target_key = ?
				  AND sm.entity = ?
				  AND sm.entity_key = obs.sync_id
				  AND sm.source = ?
			  )
		)
	`, project, DefaultSyncTargetKey, SyncEntitySession, SyncSourceLocal, project, project, DefaultSyncTargetKey, SyncEntityObservation, SyncSourceLocal).Scan(&missing)
	if err != nil {
		return false, fmt.Errorf("detect project sync metadata gaps: %w", err)
	}
	return missing == 1, nil
}

func normalizeUpgradeStage(stage string) string {
	stage = strings.TrimSpace(strings.ToLower(stage))
	switch stage {
	case UpgradeStagePlanned,
		UpgradeStageDoctorReady,
		UpgradeStageDoctorBlocked,
		UpgradeStageRepairApplied,
		UpgradeStageBootstrapEnrolled,
		UpgradeStageBootstrapPushed,
		UpgradeStageBootstrapVerified,
		UpgradeStageRolledBack:
		return stage
	default:
		return UpgradeStagePlanned
	}
}

func normalizeUpgradeRepairClass(class string) string {
	class = strings.TrimSpace(strings.ToLower(class))
	switch class {
	case UpgradeRepairClassNone,
		UpgradeRepairClassReady,
		UpgradeRepairClassRepairable,
		UpgradeRepairClassBlocked,
		UpgradeRepairClassPolicy:
		return class
	default:
		return UpgradeRepairClassNone
	}
}

func (s *Store) migrateFTSTopicKey() error {
	var colCount int
	err := s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_xinfo('observations_fts') WHERE name = 'topic_key'").Scan(&colCount)
	if err != nil || colCount > 0 {
		return nil
	}

	if _, err := s.execHook(s.db, `
		DROP TRIGGER IF EXISTS obs_fts_insert;
		DROP TRIGGER IF EXISTS obs_fts_update;
		DROP TRIGGER IF EXISTS obs_fts_delete;
		DROP TABLE IF EXISTS observations_fts;
		CREATE VIRTUAL TABLE observations_fts USING fts5(
			title,
			content,
			tool_name,
			type,
			project,
			topic_key,
			content='observations',
			content_rowid='id'
		);
		INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
		SELECT id, title, content, tool_name, type, project, topic_key
		FROM observations
		WHERE deleted_at IS NULL;

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
	`); err != nil {
		return fmt.Errorf("migrate fts topic_key: %w", err)
	}
	return nil
}

// ─── Sessions ────────────────────────────────────────────────────────────────

func (s *Store) CreateSession(id, project, directory string) error {
	// Normalize project name before storing
	project, _ = NormalizeProject(project)

	return s.withTx(func(tx *sql.Tx) error {
		if err := s.createSessionTx(tx, id, project, directory); err != nil {
			return err
		}
		var startedAt string
		if err := tx.QueryRow(`SELECT started_at FROM sessions WHERE id = ?`, id).Scan(&startedAt); err != nil {
			return err
		}
		return s.enqueueSyncMutationTx(tx, SyncEntitySession, id, SyncOpUpsert, syncSessionPayload{
			ID:        id,
			Project:   project,
			Directory: directory,
			StartedAt: startedAt,
		})
	})
}

func (s *Store) EndSession(id string, summary string) error {
	return s.withTx(func(tx *sql.Tx) error {
		res, err := s.execHook(tx,
			`UPDATE sessions SET ended_at = datetime('now'), summary = ? WHERE id = ?`,
			nullableString(summary), id,
		)
		if err != nil {
			return err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return nil
		}

		var startedAt, endedAt string
		var project, directory string
		var storedSummary *string
		if err := tx.QueryRow(
			`SELECT project, directory, started_at, ended_at, summary FROM sessions WHERE id = ?`,
			id,
		).Scan(&project, &directory, &startedAt, &endedAt, &storedSummary); err != nil {
			return err
		}

		return s.enqueueSyncMutationTx(tx, SyncEntitySession, id, SyncOpUpsert, syncSessionPayload{
			ID:        id,
			Project:   project,
			Directory: directory,
			StartedAt: startedAt,
			EndedAt:   &endedAt,
			Summary:   storedSummary,
		})
	})
}

func (s *Store) GetSession(id string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT id, project, directory, started_at, ended_at, summary FROM sessions WHERE id = ?`, id,
	)
	var sess Session
	if err := row.Scan(&sess.ID, &sess.Project, &sess.Directory, &sess.StartedAt, &sess.EndedAt, &sess.Summary); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) RecentSessions(project string, limit int) ([]SessionSummary, error) {
	// Normalize project filter for case-insensitive matching
	project, _ = NormalizeProject(project)

	if limit <= 0 {
		limit = 5
	}

	query := `
		SELECT s.id, s.project, s.started_at, s.ended_at, s.summary,
		       COUNT(o.id) as observation_count
		FROM sessions s
		LEFT JOIN observations o ON o.session_id = s.id AND o.deleted_at IS NULL
		WHERE 1=1
	`
	args := []any{}

	if project != "" {
		query += " AND s.project = ?"
		args = append(args, project)
	}

	query += " GROUP BY s.id ORDER BY MAX(datetime(COALESCE(o.created_at, s.started_at))) DESC, s.id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.queryItHook(s.db, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SessionSummary
	for rows.Next() {
		var ss SessionSummary
		if err := rows.Scan(&ss.ID, &ss.Project, &ss.StartedAt, &ss.EndedAt, &ss.Summary, &ss.ObservationCount); err != nil {
			return nil, err
		}
		results = append(results, ss)
	}
	return results, rows.Err()
}

// AllSessions returns recent sessions ordered by most recent first (for TUI browsing).
func (s *Store) AllSessions(project string, limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT s.id, s.project, s.started_at, s.ended_at, s.summary,
		       COUNT(o.id) as observation_count
		FROM sessions s
		LEFT JOIN observations o ON o.session_id = s.id AND o.deleted_at IS NULL
		WHERE 1=1
	`
	args := []any{}

	if project != "" {
		query += " AND s.project = ?"
		args = append(args, project)
	}

	query += " GROUP BY s.id ORDER BY MAX(datetime(COALESCE(o.created_at, s.started_at))) DESC, s.id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.queryItHook(s.db, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SessionSummary
	for rows.Next() {
		var ss SessionSummary
		if err := rows.Scan(&ss.ID, &ss.Project, &ss.StartedAt, &ss.EndedAt, &ss.Summary, &ss.ObservationCount); err != nil {
			return nil, err
		}
		results = append(results, ss)
	}
	return results, rows.Err()
}

// AllObservations returns recent observations ordered by most recent first (for TUI browsing).
func (s *Store) AllObservations(project, scope string, limit int) ([]Observation, error) {
	if limit <= 0 {
		limit = s.cfg.MaxContextResults
	}

	query := `
		SELECT o.id, ifnull(o.sync_id, '') as sync_id, o.session_id, o.type, o.title, o.content, o.tool_name, o.project,
		       o.scope, o.topic_key, o.revision_count, o.duplicate_count, o.last_seen_at, o.created_at, o.updated_at, o.deleted_at
		FROM observations o
		WHERE o.deleted_at IS NULL
	`
	args := []any{}

	if project != "" {
		query += " AND o.project = ?"
		args = append(args, project)
	}
	if scope != "" {
		query += " AND o.scope = ?"
		args = append(args, normalizeScope(scope))
	}

	query += " ORDER BY datetime(o.created_at) DESC, o.id DESC LIMIT ?"
	args = append(args, limit)

	return s.queryObservations(query, args...)
}

// SessionObservations returns all observations for a specific session.
func (s *Store) SessionObservations(sessionID string, limit int) ([]Observation, error) {
	if limit <= 0 {
		limit = 200
	}

	query := `
		SELECT id, ifnull(sync_id, '') as sync_id, session_id, type, title, content, tool_name, project,
		       scope, topic_key, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at
		FROM observations
		WHERE session_id = ? AND deleted_at IS NULL
		ORDER BY created_at ASC
		LIMIT ?
	`
	return s.queryObservations(query, sessionID, limit)
}

// ─── Observations ────────────────────────────────────────────────────────────

func (s *Store) AddObservation(p AddObservationParams) (int64, error) {
	// Normalize project name (lowercase + trim) before any persistence
	p.Project, _ = NormalizeProject(p.Project)

	// Strip <private>...</private> tags before persisting ANYTHING
	title := stripPrivateTags(p.Title)
	content := stripPrivateTags(p.Content)

	if len(content) > s.cfg.MaxObservationLength {
		content = content[:s.cfg.MaxObservationLength] + "... [truncated]"
	}
	scope := normalizeScope(p.Scope)
	normHash := hashNormalized(content)
	topicKey := normalizeTopicKey(p.TopicKey)

	var observationID int64
	err := s.withTx(func(tx *sql.Tx) error {
		var obs *Observation
		if topicKey != "" {
			var existingID int64
			err := tx.QueryRow(
				`SELECT id FROM observations
				 WHERE topic_key = ?
				   AND ifnull(project, '') = ifnull(?, '')
				   AND scope = ?
				   AND deleted_at IS NULL
				 ORDER BY datetime(updated_at) DESC, datetime(created_at) DESC
				 LIMIT 1`,
				topicKey, nullableString(p.Project), scope,
			).Scan(&existingID)
			if err == nil {
				if _, err := s.execHook(tx,
					`UPDATE observations
					 SET type = ?,
					     title = ?,
					     content = ?,
					     tool_name = ?,
					     topic_key = ?,
					     normalized_hash = ?,
					     revision_count = revision_count + 1,
					     last_seen_at = datetime('now'),
					     updated_at = datetime('now')
					 WHERE id = ?`,
					p.Type,
					title,
					content,
					nullableString(p.ToolName),
					nullableString(topicKey),
					normHash,
					existingID,
				); err != nil {
					return err
				}
				obs, err = s.getObservationTx(tx, existingID)
				if err != nil {
					return err
				}
				observationID = existingID
				return s.enqueueSyncMutationTx(tx, SyncEntityObservation, obs.SyncID, SyncOpUpsert, observationPayloadFromObservation(obs))
			}
			if err != sql.ErrNoRows {
				return err
			}
		}

		window := dedupeWindowExpression(s.cfg.DedupeWindow)
		var existingID int64
		err := tx.QueryRow(
			`SELECT id FROM observations
			 WHERE normalized_hash = ?
			   AND ifnull(project, '') = ifnull(?, '')
			   AND scope = ?
			   AND type = ?
			   AND title = ?
			   AND deleted_at IS NULL
			   AND datetime(created_at) >= datetime('now', ?)
			 ORDER BY created_at DESC
			 LIMIT 1`,
			normHash, nullableString(p.Project), scope, p.Type, title, window,
		).Scan(&existingID)
		if err == nil {
			if _, err := s.execHook(tx,
				`UPDATE observations
				 SET duplicate_count = duplicate_count + 1,
				     last_seen_at = datetime('now'),
				     updated_at = datetime('now')
				 WHERE id = ?`,
				existingID,
			); err != nil {
				return err
			}
			obs, err = s.getObservationTx(tx, existingID)
			if err != nil {
				return err
			}
			observationID = existingID
			return s.enqueueSyncMutationTx(tx, SyncEntityObservation, obs.SyncID, SyncOpUpsert, observationPayloadFromObservation(obs))
		}
		if err != sql.ErrNoRows {
			return err
		}

		syncID := newSyncID("obs")
		res, err := s.execHook(tx,
			`INSERT INTO observations (sync_id, session_id, type, title, content, tool_name, project, scope, topic_key, normalized_hash, revision_count, duplicate_count, last_seen_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, datetime('now'), datetime('now'))`,
			syncID, p.SessionID, p.Type, title, content,
			nullableString(p.ToolName), nullableString(p.Project), scope, nullableString(topicKey), normHash,
		)
		if err != nil {
			return err
		}
		observationID, err = res.LastInsertId()
		if err != nil {
			return err
		}

		// Populate review_after for types that have a configured decay offset.
		// expires_at is intentionally NULL for all types in Phase 1.
		// This UPDATE runs only for NEW inserts (not topic_key revisions or deduplication).
		if months, ok := decayReviewAfterMonths[p.Type]; ok {
			reviewAfter := time.Now().UTC().AddDate(0, months, 0).Format("2006-01-02 15:04:05")
			if _, err := s.execHook(tx,
				`UPDATE observations SET review_after = ? WHERE id = ?`,
				reviewAfter, observationID,
			); err != nil {
				return fmt.Errorf("set review_after: %w", err)
			}
		}

		obs, err = s.getObservationTx(tx, observationID)
		if err != nil {
			return err
		}
		return s.enqueueSyncMutationTx(tx, SyncEntityObservation, obs.SyncID, SyncOpUpsert, observationPayloadFromObservation(obs))
	})
	if err != nil {
		return 0, err
	}
	return observationID, nil
}

func (s *Store) RecentObservations(project, scope string, limit int) ([]Observation, error) {
	// Normalize project filter for case-insensitive matching
	project, _ = NormalizeProject(project)

	if limit <= 0 {
		limit = s.cfg.MaxContextResults
	}

	query := `
		SELECT o.id, ifnull(o.sync_id, '') as sync_id, o.session_id, o.type, o.title, o.content, o.tool_name, o.project,
		       o.scope, o.topic_key, o.revision_count, o.duplicate_count, o.last_seen_at, o.created_at, o.updated_at, o.deleted_at
		FROM observations o
		WHERE o.deleted_at IS NULL
	`
	args := []any{}

	if project != "" {
		query += " AND o.project = ?"
		args = append(args, project)
	}
	if scope != "" {
		query += " AND o.scope = ?"
		args = append(args, normalizeScope(scope))
	}

	query += " ORDER BY datetime(o.created_at) DESC, o.id DESC LIMIT ?"
	args = append(args, limit)

	return s.queryObservations(query, args...)
}

// ─── User Prompts ────────────────────────────────────────────────────────────

func (s *Store) AddPrompt(p AddPromptParams) (int64, error) {
	// Normalize project name before storing
	p.Project, _ = NormalizeProject(p.Project)

	content := stripPrivateTags(p.Content)
	if len(content) > s.cfg.MaxObservationLength {
		content = content[:s.cfg.MaxObservationLength] + "... [truncated]"
	}

	var promptID int64
	err := s.withTx(func(tx *sql.Tx) error {
		syncID := newSyncID("prompt")
		res, err := s.execHook(tx,
			`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`,
			syncID, p.SessionID, content, nullableString(p.Project),
		)
		if err != nil {
			return err
		}
		promptID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		var createdAt string
		if err := tx.QueryRow(`SELECT created_at FROM user_prompts WHERE id = ?`, promptID).Scan(&createdAt); err != nil {
			return err
		}
		if _, err := s.execHook(tx, `DELETE FROM prompt_tombstones WHERE sync_id = ?`, syncID); err != nil {
			return err
		}
		return s.enqueueSyncMutationTx(tx, SyncEntityPrompt, syncID, SyncOpUpsert, syncPromptPayload{
			SyncID:    syncID,
			SessionID: p.SessionID,
			Content:   content,
			Project:   nullableString(p.Project),
			CreatedAt: createdAt,
		})
	})
	if err != nil {
		return 0, err
	}
	return promptID, nil
}

func (s *Store) RecentPrompts(project string, limit int) ([]Prompt, error) {
	// Normalize project filter for case-insensitive matching
	project, _ = NormalizeProject(project)

	if limit <= 0 {
		limit = 20
	}

	query := `SELECT id, ifnull(sync_id, '') as sync_id, session_id, content, ifnull(project, '') as project, created_at FROM user_prompts`
	args := []any{}

	if project != "" {
		query += " WHERE project = ?"
		args = append(args, project)
	}

	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.queryItHook(s.db, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Prompt
	for rows.Next() {
		var p Prompt
		if err := rows.Scan(&p.ID, &p.SyncID, &p.SessionID, &p.Content, &p.Project, &p.CreatedAt); err != nil {
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
		SELECT p.id, ifnull(p.sync_id, '') as sync_id, p.session_id, p.content, ifnull(p.project, '') as project, p.created_at
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

	rows, err := s.queryItHook(s.db, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search prompts: %w", err)
	}
	defer rows.Close()

	var results []Prompt
	for rows.Next() {
		var p Prompt
		if err := rows.Scan(&p.ID, &p.SyncID, &p.SessionID, &p.Content, &p.Project, &p.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, p)
	}
	return results, rows.Err()
}

// ─── Delete Session ──────────────────────────────────────────────────────────

// DeleteSession hard-deletes a session and its prompts.
// It returns ErrSessionHasObservations if the session has any observations
// (including soft-deleted ones) to prevent orphaned rows.
// It returns ErrSessionNotFound if no session with that ID exists.
//
// When the session belongs to an enrolled project, this operation also enqueues
// a session/delete mutation so cloud replicas can remove the session.
func (s *Store) DeleteSession(id string) error {
	return s.withTx(func(tx *sql.Tx) error {
		var project string
		if err := tx.QueryRow(`SELECT project FROM sessions WHERE id = ?`, id).Scan(&project); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %q", ErrSessionNotFound, id)
			}
			return fmt.Errorf("delete session: load session: %w", err)
		}

		var enrolled int
		if err := tx.QueryRow(`SELECT 1 FROM sync_enrolled_projects WHERE project = ? LIMIT 1`, project).Scan(&enrolled); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("delete session: check enrollment: %w", err)
			}
		}
		// Count ALL observations for the session, including soft-deleted ones,
		// because the FK constraint on observations.session_id has no ON DELETE CASCADE.
		var count int
		rows, err := s.queryItHook(tx, `SELECT COUNT(*) FROM observations WHERE session_id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete session: count observations: %w", err)
		}
		if rows.Next() {
			if err := rows.Scan(&count); err != nil {
				_ = rows.Close()
				return fmt.Errorf("delete session: count observations: %w", err)
			}
		}
		_ = rows.Close()
		if count > 0 {
			return fmt.Errorf("%w: session %q has %d observation(s)", ErrSessionHasObservations, id, count)
		}

		if _, err := s.execHook(tx, `DELETE FROM user_prompts WHERE session_id = ?`, id); err != nil {
			return fmt.Errorf("delete session: remove prompts: %w", err)
		}

		res, err := s.execHook(tx, `DELETE FROM sessions WHERE id = ?`, id)
		if err != nil {
			var sqliteErr *sqlite.Error
			if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqliteConstraintForeignKey {
				return fmt.Errorf("%w: session %q has observation(s)", ErrSessionHasObservations, id)
			}
			return fmt.Errorf("delete session: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete session: rows affected: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %q", ErrSessionNotFound, id)
		}

		if enrolled == 1 {
			now := Now()
			if err := s.enqueueSyncMutationTx(tx, SyncEntitySession, id, SyncOpDelete, syncSessionPayload{
				ID:        id,
				Project:   project,
				DeletedAt: &now,
			}); err != nil {
				return fmt.Errorf("delete session: enqueue mutation: %w", err)
			}
		}

		return nil
	})
}

// ─── Delete Prompt ───────────────────────────────────────────────────────────

// DeletePrompt hard-deletes a single prompt by ID and records a sync tombstone.
// It returns ErrPromptNotFound if no prompt with that ID exists.
func (s *Store) DeletePrompt(id int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		var payload syncPromptPayload
		var project string
		if err := tx.QueryRow(`SELECT sync_id, session_id, ifnull(project, '') FROM user_prompts WHERE id = ?`, id).Scan(&payload.SyncID, &payload.SessionID, &project); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: prompt #%d", ErrPromptNotFound, id)
			}
			return fmt.Errorf("delete prompt: load row: %w", err)
		}
		payload.Project = nullableString(project)
		now := Now()
		payload.Deleted = true
		payload.HardDelete = true
		payload.DeletedAt = &now

		res, err := s.execHook(tx, `DELETE FROM user_prompts WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete prompt: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete prompt: rows affected: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: prompt #%d", ErrPromptNotFound, id)
		}
		if _, err := s.execHook(tx,
			`INSERT INTO prompt_tombstones (sync_id, session_id, project, deleted_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(sync_id) DO UPDATE SET session_id = excluded.session_id, project = excluded.project, deleted_at = excluded.deleted_at`,
			payload.SyncID, payload.SessionID, payload.Project, now,
		); err != nil {
			return fmt.Errorf("delete prompt: upsert tombstone: %w", err)
		}
		if err := s.enqueueSyncMutationTx(tx, SyncEntityPrompt, payload.SyncID, SyncOpDelete, payload); err != nil {
			return fmt.Errorf("delete prompt: enqueue mutation: %w", err)
		}
		return nil
	})
}

// ─── Get Single Observation ──────────────────────────────────────────────────

func (s *Store) GetObservation(id int64) (*Observation, error) {
	row := s.db.QueryRow(
		`SELECT id, ifnull(sync_id, '') as sync_id, session_id, type, title, content, tool_name, project,
		        scope, topic_key, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at
		 FROM observations WHERE id = ? AND deleted_at IS NULL`, id,
	)
	var o Observation
	if err := row.Scan(
		&o.ID, &o.SyncID, &o.SessionID, &o.Type, &o.Title, &o.Content,
		&o.ToolName, &o.Project, &o.Scope, &o.TopicKey, &o.RevisionCount, &o.DuplicateCount, &o.LastSeenAt,
		&o.CreatedAt, &o.UpdatedAt, &o.DeletedAt,
	); err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Store) UpdateObservation(id int64, p UpdateObservationParams) (*Observation, error) {
	var updated *Observation
	err := s.withTx(func(tx *sql.Tx) error {
		obs, err := s.getObservationTx(tx, id)
		if err != nil {
			return err
		}

		typ := obs.Type
		title := obs.Title
		content := obs.Content
		project := derefString(obs.Project)
		scope := obs.Scope
		topicKey := derefString(obs.TopicKey)

		if p.Type != nil {
			typ = *p.Type
		}
		if p.Title != nil {
			title = stripPrivateTags(*p.Title)
		}
		if p.Content != nil {
			content = stripPrivateTags(*p.Content)
			if len(content) > s.cfg.MaxObservationLength {
				content = content[:s.cfg.MaxObservationLength] + "... [truncated]"
			}
		}
		if p.Project != nil {
			project, _ = NormalizeProject(*p.Project)
		}
		if p.Scope != nil {
			scope = normalizeScope(*p.Scope)
		}
		if p.TopicKey != nil {
			topicKey = normalizeTopicKey(*p.TopicKey)
		}

		if _, err := s.execHook(tx,
			`UPDATE observations
			 SET type = ?,
			     title = ?,
			     content = ?,
			     project = ?,
			     scope = ?,
			     topic_key = ?,
			     normalized_hash = ?,
			     revision_count = revision_count + 1,
			     updated_at = datetime('now')
			 WHERE id = ? AND deleted_at IS NULL`,
			typ,
			title,
			content,
			nullableString(project),
			scope,
			nullableString(topicKey),
			hashNormalized(content),
			id,
		); err != nil {
			return err
		}

		updated, err = s.getObservationTx(tx, id)
		if err != nil {
			return err
		}
		return s.enqueueSyncMutationTx(tx, SyncEntityObservation, updated.SyncID, SyncOpUpsert, observationPayloadFromObservation(updated))
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) DeleteObservation(id int64, hardDelete bool) error {
	return s.withTx(func(tx *sql.Tx) error {
		obs, err := s.getObservationTx(tx, id)
		if err == sql.ErrNoRows {
			return ErrObservationNotFound
		}
		if err != nil {
			return err
		}

		deletedAt := Now()
		if hardDelete {
			if _, err := s.execHook(tx, `DELETE FROM observations WHERE id = ?`, id); err != nil {
				return err
			}
			// ── Phase: memory-conflict-surfacing — C.11 ──────────────────────
			// Orphan any memory_relations rows that reference this observation's
			// sync_id (as source or target). Relations are never cascade-deleted;
			// they become audit history with judgment_status='orphaned'.
			if obs.SyncID != "" {
				if _, err := s.execHook(tx, `
					UPDATE memory_relations
					SET judgment_status = 'orphaned',
					    updated_at      = datetime('now')
					WHERE source_id = ? OR target_id = ?
				`, obs.SyncID, obs.SyncID); err != nil {
					return fmt.Errorf("orphan memory_relations after hard-delete: %w", err)
				}
			}
		} else {
			if _, err := s.execHook(tx,
				`UPDATE observations
				 SET deleted_at = datetime('now'),
				     updated_at = datetime('now')
				 WHERE id = ? AND deleted_at IS NULL`,
				id,
			); err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT deleted_at FROM observations WHERE id = ?`, id).Scan(&deletedAt); err != nil {
				return err
			}
		}

		return s.enqueueSyncMutationTx(tx, SyncEntityObservation, obs.SyncID, SyncOpDelete, syncObservationPayload{
			SyncID:     obs.SyncID,
			SessionID:  obs.SessionID,
			Project:    obs.Project,
			Deleted:    true,
			DeletedAt:  &deletedAt,
			HardDelete: hardDelete,
		})
	})
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
	beforeRows, err := s.queryItHook(s.db, `
		SELECT id, session_id, type, title, content, tool_name, project,
		       scope, topic_key, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at
		FROM observations
		WHERE session_id = ? AND id < ? AND deleted_at IS NULL
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
		if err := beforeRows.Scan(
			&e.ID, &e.SessionID, &e.Type, &e.Title, &e.Content,
			&e.ToolName, &e.Project, &e.Scope, &e.TopicKey, &e.RevisionCount, &e.DuplicateCount, &e.LastSeenAt,
			&e.CreatedAt, &e.UpdatedAt, &e.DeletedAt,
		); err != nil {
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
	afterRows, err := s.queryItHook(s.db, `
		SELECT id, session_id, type, title, content, tool_name, project,
		       scope, topic_key, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at
		FROM observations
		WHERE session_id = ? AND id > ? AND deleted_at IS NULL
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
		if err := afterRows.Scan(
			&e.ID, &e.SessionID, &e.Type, &e.Title, &e.Content,
			&e.ToolName, &e.Project, &e.Scope, &e.TopicKey, &e.RevisionCount, &e.DuplicateCount, &e.LastSeenAt,
			&e.CreatedAt, &e.UpdatedAt, &e.DeletedAt,
		); err != nil {
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
		"SELECT COUNT(*) FROM observations WHERE session_id = ? AND deleted_at IS NULL", focus.SessionID,
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
	// Normalize project filter so "Engram" finds records stored as "engram"
	opts.Project, _ = NormalizeProject(opts.Project)

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > s.cfg.MaxSearchResults {
		limit = s.cfg.MaxSearchResults
	}

	var directResults []SearchResult
	if strings.Contains(query, "/") {
		tkSQL := `
			SELECT id, ifnull(sync_id, '') as sync_id, session_id, type, title, content, tool_name, project,
			       scope, topic_key, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at
			FROM observations
			WHERE topic_key = ? AND deleted_at IS NULL
		`
		tkArgs := []any{query}

		if opts.Type != "" {
			tkSQL += " AND type = ?"
			tkArgs = append(tkArgs, opts.Type)
		}
		if opts.Project != "" {
			tkSQL += " AND project = ?"
			tkArgs = append(tkArgs, opts.Project)
		}
		if opts.Scope != "" {
			tkSQL += " AND scope = ?"
			tkArgs = append(tkArgs, normalizeScope(opts.Scope))
		}

		tkSQL += " ORDER BY updated_at DESC LIMIT ?"
		tkArgs = append(tkArgs, limit)

		tkRows, err := s.queryItHook(s.db, tkSQL, tkArgs...)
		if err == nil {
			defer tkRows.Close()
			for tkRows.Next() {
				var sr SearchResult
				if err := tkRows.Scan(
					&sr.ID, &sr.SyncID, &sr.SessionID, &sr.Type, &sr.Title, &sr.Content,
					&sr.ToolName, &sr.Project, &sr.Scope, &sr.TopicKey, &sr.RevisionCount, &sr.DuplicateCount,
					&sr.LastSeenAt, &sr.CreatedAt, &sr.UpdatedAt, &sr.DeletedAt,
				); err != nil {
					break
				}
				sr.Rank = -1000
				directResults = append(directResults, sr)
			}
		}
	}

	// Sanitize query for FTS5 — wrap each term in quotes to avoid syntax errors
	ftsQuery := sanitizeFTS(query)

	sqlQ := `
		SELECT o.id, ifnull(o.sync_id, '') as sync_id, o.session_id, o.type, o.title, o.content, o.tool_name, o.project,
		       o.scope, o.topic_key, o.revision_count, o.duplicate_count, o.last_seen_at, o.created_at, o.updated_at, o.deleted_at,
		       fts.rank
		FROM observations_fts fts
		JOIN observations o ON o.id = fts.rowid
		WHERE observations_fts MATCH ? AND o.deleted_at IS NULL
	`
	args := []any{ftsQuery}

	if opts.Type != "" {
		sqlQ += " AND o.type = ?"
		args = append(args, opts.Type)
	}

	if opts.Project != "" {
		sqlQ += " AND o.project = ?"
		args = append(args, opts.Project)
	}

	if opts.Scope != "" {
		sqlQ += " AND o.scope = ?"
		args = append(args, normalizeScope(opts.Scope))
	}

	sqlQ += " ORDER BY fts.rank LIMIT ?"
	args = append(args, limit)

	rows, err := s.queryItHook(s.db, sqlQ, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	seen := make(map[int64]bool)
	for _, dr := range directResults {
		seen[dr.ID] = true
	}

	var results []SearchResult
	results = append(results, directResults...)
	for rows.Next() {
		var sr SearchResult
		if err := rows.Scan(
			&sr.ID, &sr.SyncID, &sr.SessionID, &sr.Type, &sr.Title, &sr.Content,
			&sr.ToolName, &sr.Project, &sr.Scope, &sr.TopicKey, &sr.RevisionCount, &sr.DuplicateCount,
			&sr.LastSeenAt, &sr.CreatedAt, &sr.UpdatedAt, &sr.DeletedAt,
			&sr.Rank,
		); err != nil {
			return nil, err
		}
		if !seen[sr.ID] {
			results = append(results, sr)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// ─── Stats ───────────────────────────────────────────────────────────────────

func (s *Store) Stats() (*Stats, error) {
	stats := &Stats{}

	s.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&stats.TotalSessions)
	s.db.QueryRow("SELECT COUNT(*) FROM observations WHERE deleted_at IS NULL").Scan(&stats.TotalObservations)
	s.db.QueryRow("SELECT COUNT(*) FROM user_prompts").Scan(&stats.TotalPrompts)

	rows, err := s.queryItHook(s.db, "SELECT project FROM observations WHERE project IS NOT NULL AND deleted_at IS NULL GROUP BY project ORDER BY MAX(created_at) DESC")
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

// ─── Project Existence ───────────────────────────────────────────────────────

// ProjectExists returns true if the named project has at least one record in
// any of observations, sessions, prompts, or enrollment tables.
// Uses a single UNION ALL LIMIT 1 query for efficiency (REQ-315).
// The sync_enrolled_projects branch ensures a project enrolled via EnrollProject()
// without any other data is still recognized (JC1).
func (s *Store) ProjectExists(name string) (bool, error) {
	const query = `
SELECT 1 FROM (
  SELECT project FROM observations WHERE project = ? AND deleted_at IS NULL
  UNION ALL
  SELECT project FROM sessions WHERE project = ?
  UNION ALL
  SELECT project FROM user_prompts WHERE project = ?
  UNION ALL
  SELECT project FROM sync_enrolled_projects WHERE project = ?
) LIMIT 1`
	var dummy int
	err := s.db.QueryRow(query, name, name, name, name).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ─── Context Formatting ─────────────────────────────────────────────────────

func (s *Store) FormatContext(project, scope string) (string, error) {
	sessions, err := s.RecentSessions(project, 5)
	if err != nil {
		return "", err
	}

	observations, err := s.RecentObservations(project, scope, s.cfg.MaxContextResults)
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
	return s.exportWithProjectScope("")
}

// ExportProject returns an export restricted to records relevant to a single
// normalized project. This avoids full-database exports when only one project
// needs to sync.
func (s *Store) ExportProject(project string) (*ExportData, error) {
	normalizedProject, _ := NormalizeProject(project)
	normalizedProject = strings.TrimSpace(normalizedProject)
	if normalizedProject == "" {
		return nil, fmt.Errorf("project is required")
	}
	return s.exportWithProjectScope(normalizedProject)
}

func (s *Store) exportWithProjectScope(project string) (*ExportData, error) {
	data := &ExportData{
		Version:    "0.1.0",
		ExportedAt: Now(),
	}

	sessionQuery := "SELECT id, project, directory, started_at, ended_at, summary FROM sessions"
	sessionArgs := []any{}
	if project != "" {
		sessionQuery += `
			WHERE project = ?
			   OR id IN (
				SELECT session_id FROM observations
				 WHERE ifnull(project, '') = ?
				    OR (ifnull(project, '') = '' AND session_id IN (SELECT id FROM sessions WHERE project = ?))
				UNION
				SELECT session_id FROM user_prompts
				 WHERE ifnull(project, '') = ?
				    OR (ifnull(project, '') = '' AND session_id IN (SELECT id FROM sessions WHERE project = ?))
			)`
		sessionArgs = append(sessionArgs, project, project, project, project, project)
	}
	sessionQuery += " ORDER BY started_at"

	// Sessions
	rows, err := s.queryItHook(s.db,
		sessionQuery,
		sessionArgs...,
	)
	if err != nil {
		return nil, fmt.Errorf("export sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.Project, &sess.Directory, &sess.StartedAt, &sess.EndedAt, &sess.Summary); err != nil {
			return nil, err
		}
		data.Sessions = append(data.Sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Observations
	obsQuery := `SELECT id, ifnull(sync_id, '') as sync_id, session_id, type, title, content, tool_name, project,
	        scope, topic_key, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at
	 FROM observations`
	obsArgs := []any{}
	if project != "" {
		obsQuery += `
			WHERE ifnull(project, '') = ?
			   OR (ifnull(project, '') = '' AND session_id IN (SELECT id FROM sessions WHERE project = ?))`
		obsArgs = append(obsArgs, project, project)
	}
	obsQuery += " ORDER BY id"
	obsRows, err := s.queryItHook(s.db, obsQuery, obsArgs...)
	if err != nil {
		return nil, fmt.Errorf("export observations: %w", err)
	}
	defer obsRows.Close()
	for obsRows.Next() {
		var o Observation
		if err := obsRows.Scan(
			&o.ID, &o.SyncID, &o.SessionID, &o.Type, &o.Title, &o.Content,
			&o.ToolName, &o.Project, &o.Scope, &o.TopicKey, &o.RevisionCount, &o.DuplicateCount, &o.LastSeenAt,
			&o.CreatedAt, &o.UpdatedAt, &o.DeletedAt,
		); err != nil {
			return nil, err
		}
		data.Observations = append(data.Observations, o)
	}
	if err := obsRows.Err(); err != nil {
		return nil, err
	}

	// Prompts
	promptQuery := "SELECT id, ifnull(sync_id, '') as sync_id, session_id, content, ifnull(project, '') as project, created_at FROM user_prompts"
	promptArgs := []any{}
	if project != "" {
		promptQuery += `
			WHERE ifnull(project, '') = ?
			   OR (ifnull(project, '') = '' AND session_id IN (SELECT id FROM sessions WHERE project = ?))`
		promptArgs = append(promptArgs, project, project)
	}
	promptQuery += " ORDER BY id"
	promptRows, err := s.queryItHook(s.db, promptQuery, promptArgs...)
	if err != nil {
		return nil, fmt.Errorf("export prompts: %w", err)
	}
	defer promptRows.Close()
	for promptRows.Next() {
		var p Prompt
		if err := promptRows.Scan(&p.ID, &p.SyncID, &p.SessionID, &p.Content, &p.Project, &p.CreatedAt); err != nil {
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
	tx, err := s.beginTxHook()
	if err != nil {
		return nil, fmt.Errorf("import: begin tx: %w", err)
	}
	defer tx.Rollback()

	result := &ImportResult{}

	// Import sessions (skip duplicates)
	for _, sess := range data.Sessions {
		res, err := s.execHook(tx,
			`INSERT OR IGNORE INTO sessions (id, project, directory, started_at, ended_at, summary)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			sess.ID, sess.Project, sess.Directory, sess.StartedAt, sess.EndedAt, sess.Summary,
		)
		if err != nil {
			return nil, fmt.Errorf("import session %s: %w", sess.ID, err)
		}
		n, _ := res.RowsAffected()
		result.SessionsImported += int(n)
	}

	// Import observations (use new IDs — AUTOINCREMENT)
	for _, obs := range data.Observations {
		_, err := s.execHook(tx,
			`INSERT INTO observations (sync_id, session_id, type, title, content, tool_name, project, scope, topic_key, normalized_hash, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			normalizeExistingSyncID(obs.SyncID, "obs"),
			obs.SessionID,
			obs.Type,
			obs.Title,
			obs.Content,
			obs.ToolName,
			obs.Project,
			normalizeScope(obs.Scope),
			nullableString(normalizeTopicKey(derefString(obs.TopicKey))),
			hashNormalized(obs.Content),
			maxInt(obs.RevisionCount, 1),
			maxInt(obs.DuplicateCount, 1),
			obs.LastSeenAt,
			obs.CreatedAt,
			obs.UpdatedAt,
			obs.DeletedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("import observation %d: %w", obs.ID, err)
		}
		result.ObservationsImported++
	}

	// Import prompts
	for _, p := range data.Prompts {
		_, err := s.execHook(tx,
			`INSERT INTO user_prompts (sync_id, session_id, content, project, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			normalizeExistingSyncID(p.SyncID, "prompt"), p.SessionID, p.Content, p.Project, p.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("import prompt %d: %w", p.ID, err)
		}
		result.PromptsImported++
	}

	if err := s.commitHook(tx); err != nil {
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

// GetSyncedChunks returns local-target chunk IDs for backwards compatibility.
func (s *Store) GetSyncedChunks() (map[string]bool, error) {
	return s.GetSyncedChunksForTarget(LocalChunkTargetKey)
}

// GetSyncedChunksForTarget returns chunk IDs tracked for a specific sync target.
func (s *Store) GetSyncedChunksForTarget(targetKey string) (map[string]bool, error) {
	targetKey = normalizeChunkTargetKey(targetKey)
	rows, err := s.queryItHook(s.db, "SELECT chunk_id FROM sync_chunks WHERE target_key = ?", targetKey)
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

// RecordSyncedChunk marks a local-target chunk as imported/exported.
func (s *Store) RecordSyncedChunk(chunkID string) error {
	return s.RecordSyncedChunkForTarget(LocalChunkTargetKey, chunkID)
}

// RecordSyncedChunkForTarget marks a chunk as imported/exported for a target.
func (s *Store) RecordSyncedChunkForTarget(targetKey, chunkID string) error {
	targetKey = normalizeChunkTargetKey(targetKey)
	_, err := s.execHook(s.db,
		"INSERT OR IGNORE INTO sync_chunks (target_key, chunk_id) VALUES (?, ?)",
		targetKey, chunkID,
	)
	return err
}

// ─── Local Sync State & Mutation Journal ─────────────────────────────────────

func (s *Store) GetSyncState(targetKey string) (*SyncState, error) {
	targetKey = normalizeSyncTargetKey(targetKey)
	if err := s.ensureSyncState(targetKey); err != nil {
		return nil, err
	}
	return s.getSyncState(targetKey)
}

func (s *Store) ListPendingSyncMutations(targetKey string, limit int) ([]SyncMutation, error) {
	targetKey = normalizeSyncTargetKey(targetKey)
	if limit <= 0 {
		limit = 100
	}
	// Only return mutations for enrolled projects or empty-project (global) mutations.
	// Empty-project mutations always sync regardless of enrollment.
	rows, err := s.queryItHook(s.db, `
		SELECT sm.seq, sm.target_key, sm.entity, sm.entity_key, sm.op, sm.payload, sm.source, sm.project, sm.occurred_at, sm.acked_at
		FROM sync_mutations sm
		LEFT JOIN sync_enrolled_projects sep ON sm.project = sep.project
		WHERE sm.target_key = ? AND sm.acked_at IS NULL
		  AND (sm.project = '' OR sep.project IS NOT NULL)
		ORDER BY sm.seq ASC
		LIMIT ?`, targetKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mutations []SyncMutation
	for rows.Next() {
		var mutation SyncMutation
		if err := rows.Scan(&mutation.Seq, &mutation.TargetKey, &mutation.Entity, &mutation.EntityKey, &mutation.Op, &mutation.Payload, &mutation.Source, &mutation.Project, &mutation.OccurredAt, &mutation.AckedAt); err != nil {
			return nil, err
		}
		mutations = append(mutations, mutation)
	}
	return mutations, rows.Err()
}

func (s *Store) ListPendingSyncMutationsAfterSeq(targetKey string, afterSeq int64, limit int) ([]SyncMutation, error) {
	targetKey = normalizeSyncTargetKey(targetKey)
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.queryItHook(s.db, `
		SELECT sm.seq, sm.target_key, sm.entity, sm.entity_key, sm.op, sm.payload, sm.source, sm.project, sm.occurred_at, sm.acked_at
		FROM sync_mutations sm
		LEFT JOIN sync_enrolled_projects sep ON sm.project = sep.project
		WHERE sm.target_key = ? AND sm.acked_at IS NULL
		  AND sm.seq > ?
		  AND (sm.project = '' OR sep.project IS NOT NULL)
		ORDER BY sm.seq ASC
		LIMIT ?`, targetKey, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mutations := make([]SyncMutation, 0, limit)
	for rows.Next() {
		var mutation SyncMutation
		if err := rows.Scan(&mutation.Seq, &mutation.TargetKey, &mutation.Entity, &mutation.EntityKey, &mutation.Op, &mutation.Payload, &mutation.Source, &mutation.Project, &mutation.OccurredAt, &mutation.AckedAt); err != nil {
			return nil, err
		}
		mutations = append(mutations, mutation)
	}
	return mutations, rows.Err()
}

func (s *Store) CountPendingNonEnrolledSyncMutations(targetKey string) ([]PendingSyncMutationProjectCount, error) {
	targetKey = normalizeSyncTargetKey(targetKey)
	rows, err := s.queryItHook(s.db, `
		SELECT sm.project, COUNT(*)
		FROM sync_mutations sm
		LEFT JOIN sync_enrolled_projects sep ON sm.project = sep.project
		WHERE sm.target_key = ?
		  AND sm.acked_at IS NULL
		  AND sm.project != ''
		  AND sep.project IS NULL
		GROUP BY sm.project
		ORDER BY sm.project ASC`, targetKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := []PendingSyncMutationProjectCount{}
	for rows.Next() {
		var count PendingSyncMutationProjectCount
		if err := rows.Scan(&count.Project, &count.Count); err != nil {
			return nil, err
		}
		counts = append(counts, count)
	}
	return counts, rows.Err()
}

// SkipAckNonEnrolledMutations acks (marks as skipped) all pending mutations
// that belong to non-enrolled projects, preventing journal bloat. Empty-project
// mutations are never skipped — they always sync regardless of enrollment.
func (s *Store) SkipAckNonEnrolledMutations(targetKey string) (int64, error) {
	targetKey = normalizeSyncTargetKey(targetKey)
	res, err := s.execHook(s.db, `
		UPDATE sync_mutations
		SET acked_at = datetime('now')
		WHERE target_key = ?
		  AND acked_at IS NULL
		  AND project != ''
		  AND project NOT IN (SELECT project FROM sync_enrolled_projects)`,
		targetKey,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) AckSyncMutations(targetKey string, lastAckedSeq int64) error {
	if lastAckedSeq <= 0 {
		return nil
	}
	targetKey = normalizeSyncTargetKey(targetKey)
	return s.withTx(func(tx *sql.Tx) error {
		affectedProjects := map[string]struct{}{}
		if targetKey == DefaultSyncTargetKey {
			rows, err := s.queryItHook(tx,
				`SELECT DISTINCT ifnull(project, '') FROM sync_mutations
				 WHERE target_key = ? AND seq <= ? AND acked_at IS NULL`,
				targetKey, lastAckedSeq,
			)
			if err != nil {
				return err
			}
			for rows.Next() {
				var project string
				if err := rows.Scan(&project); err != nil {
					_ = rows.Close()
					return err
				}
				project, _ = NormalizeProject(project)
				project = strings.TrimSpace(project)
				if project != "" {
					affectedProjects[project] = struct{}{}
				}
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			_ = rows.Close()
		}

		state, err := s.getSyncStateTx(tx, targetKey)
		if err != nil {
			return err
		}
		if _, err := s.execHook(tx,
			`UPDATE sync_mutations SET acked_at = datetime('now') WHERE target_key = ? AND seq <= ? AND acked_at IS NULL`,
			targetKey, lastAckedSeq,
		); err != nil {
			return err
		}
		acked := state.LastAckedSeq
		if lastAckedSeq > acked {
			acked = lastAckedSeq
		}
		lifecycle := SyncLifecyclePending
		if acked >= state.LastEnqueuedSeq {
			lifecycle = SyncLifecycleHealthy
		}
		if isActivelyDegradedState(state, time.Now().UTC()) {
			lifecycle = SyncLifecycleDegraded
		}
		if lifecycle == SyncLifecycleDegraded {
			_, err = s.execHook(tx,
				`UPDATE sync_state
				 SET last_acked_seq = ?, lifecycle = ?, updated_at = datetime('now')
				 WHERE target_key = ?`,
				acked, lifecycle, targetKey,
			)
		} else {
			_, err = s.execHook(tx,
				`UPDATE sync_state
				 SET last_acked_seq = ?, lifecycle = ?, consecutive_failures = 0, backoff_until = NULL, reason_code = NULL, reason_message = NULL, last_error = NULL, updated_at = datetime('now')
				 WHERE target_key = ?`,
				acked, lifecycle, targetKey,
			)
		}
		if err != nil {
			return err
		}
		if targetKey != DefaultSyncTargetKey {
			return nil
		}
		for project := range affectedProjects {
			if err := s.refreshProjectSyncStateTx(tx, project); err != nil {
				return err
			}
		}
		return nil
	})
}

// AckSyncMutationSeqs acknowledges specific mutation sequence numbers without
// requiring them to be contiguous.
func (s *Store) AckSyncMutationSeqs(targetKey string, seqs []int64) error {
	if len(seqs) == 0 {
		return nil
	}
	targetKey = normalizeSyncTargetKey(targetKey)
	return s.withTx(func(tx *sql.Tx) error {
		affectedProjects := map[string]struct{}{}
		if targetKey == DefaultSyncTargetKey {
			for _, seq := range seqs {
				if seq <= 0 {
					continue
				}
				var project string
				err := tx.QueryRow(
					`SELECT ifnull(project, '') FROM sync_mutations WHERE target_key = ? AND seq = ?`,
					targetKey, seq,
				).Scan(&project)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				project, _ = NormalizeProject(project)
				project = strings.TrimSpace(project)
				if project != "" {
					affectedProjects[project] = struct{}{}
				}
			}
		}

		state, err := s.getSyncStateTx(tx, targetKey)
		if err != nil {
			return err
		}
		maxSeq := state.LastAckedSeq
		for _, seq := range seqs {
			if seq <= 0 {
				continue
			}
			if _, err := s.execHook(tx,
				`UPDATE sync_mutations SET acked_at = datetime('now') WHERE target_key = ? AND seq = ? AND acked_at IS NULL`,
				targetKey, seq,
			); err != nil {
				return err
			}
			if seq > maxSeq {
				maxSeq = seq
			}
		}
		var remaining int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE target_key = ? AND acked_at IS NULL`, targetKey).Scan(&remaining); err != nil {
			return err
		}
		lifecycle := SyncLifecyclePending
		if remaining == 0 {
			lifecycle = SyncLifecycleHealthy
		}
		if isActivelyDegradedState(state, time.Now().UTC()) {
			lifecycle = SyncLifecycleDegraded
		}
		if lifecycle == SyncLifecycleDegraded {
			_, err = s.execHook(tx,
				`UPDATE sync_state SET last_acked_seq = ?, lifecycle = ?, updated_at = datetime('now') WHERE target_key = ?`,
				maxSeq, lifecycle, targetKey,
			)
		} else {
			_, err = s.execHook(tx,
				`UPDATE sync_state SET last_acked_seq = ?, lifecycle = ?, consecutive_failures = 0, backoff_until = NULL, reason_code = NULL, reason_message = NULL, last_error = NULL, updated_at = datetime('now') WHERE target_key = ?`,
				maxSeq, lifecycle, targetKey,
			)
		}
		if err != nil {
			return err
		}
		if targetKey != DefaultSyncTargetKey {
			return nil
		}
		for project := range affectedProjects {
			if err := s.refreshProjectSyncStateTx(tx, project); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) HasPendingSyncMutationsForProject(project string) (bool, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return false, nil
	}

	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE target_key = ? AND project = ? AND acked_at IS NULL`,
		DefaultSyncTargetKey,
		project,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Store) refreshProjectSyncStateTx(tx *sql.Tx, project string) error {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return nil
	}
	projectTargetKey := syncTargetKeyForProject(project)
	state, err := s.getSyncStateTx(tx, projectTargetKey)
	if err != nil {
		return err
	}

	var maxAckedSeq int64
	if err := tx.QueryRow(
		`SELECT ifnull(MAX(seq), 0)
		 FROM sync_mutations
		 WHERE target_key = ? AND project = ? AND acked_at IS NOT NULL`,
		DefaultSyncTargetKey,
		project,
	).Scan(&maxAckedSeq); err != nil {
		return err
	}
	if maxAckedSeq < state.LastAckedSeq {
		maxAckedSeq = state.LastAckedSeq
	}

	var maxEnqueuedSeq int64
	if err := tx.QueryRow(
		`SELECT ifnull(MAX(seq), 0)
		 FROM sync_mutations
		 WHERE target_key = ? AND project = ?`,
		DefaultSyncTargetKey,
		project,
	).Scan(&maxEnqueuedSeq); err != nil {
		return err
	}
	if maxEnqueuedSeq < state.LastEnqueuedSeq {
		maxEnqueuedSeq = state.LastEnqueuedSeq
	}

	var pendingCount int
	if err := tx.QueryRow(
		`SELECT COUNT(*)
		 FROM sync_mutations
		 WHERE target_key = ? AND project = ? AND acked_at IS NULL`,
		DefaultSyncTargetKey,
		project,
	).Scan(&pendingCount); err != nil {
		return err
	}

	lifecycle := SyncLifecycleHealthy
	if pendingCount > 0 {
		lifecycle = SyncLifecyclePending
	}
	if isActivelyDegradedState(state, time.Now().UTC()) {
		lifecycle = SyncLifecycleDegraded
	}

	if lifecycle == SyncLifecycleDegraded {
		_, err = s.execHook(tx,
			`UPDATE sync_state
			 SET last_enqueued_seq = ?, last_acked_seq = ?, lifecycle = ?, updated_at = datetime('now')
			 WHERE target_key = ?`,
			maxEnqueuedSeq, maxAckedSeq, lifecycle, projectTargetKey,
		)
	} else {
		_, err = s.execHook(tx,
			`UPDATE sync_state
			 SET last_enqueued_seq = ?, last_acked_seq = ?, lifecycle = ?, consecutive_failures = 0, backoff_until = NULL, reason_code = NULL, reason_message = NULL, last_error = NULL, updated_at = datetime('now')
			 WHERE target_key = ?`,
			maxEnqueuedSeq, maxAckedSeq, lifecycle, projectTargetKey,
		)
	}
	return err
}

func isActivelyDegradedState(state *SyncState, now time.Time) bool {
	if state == nil || state.Lifecycle != SyncLifecycleDegraded {
		return false
	}
	reasonCode := strings.TrimSpace(derefString(state.ReasonCode))
	switch reasonCode {
	case "blocked_unenrolled", "paused", "auth_required", "policy_forbidden", "cloud_config_error":
		return true
	}
	if state.BackoffUntil != nil {
		if backoffUntil, err := time.Parse(time.RFC3339, strings.TrimSpace(*state.BackoffUntil)); err == nil && backoffUntil.After(now) {
			return true
		}
	}
	return false
}

func (s *Store) AcquireSyncLease(targetKey, owner string, ttl time.Duration, now time.Time) (bool, error) {
	targetKey = normalizeSyncTargetKey(targetKey)
	if ttl <= 0 {
		ttl = time.Minute
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var acquired bool
	err := s.withTx(func(tx *sql.Tx) error {
		state, err := s.getSyncStateTx(tx, targetKey)
		if err != nil {
			return err
		}
		if state.LeaseUntil != nil {
			leaseUntil, err := time.Parse(time.RFC3339, *state.LeaseUntil)
			if err == nil && leaseUntil.After(now) && derefString(state.LeaseOwner) != "" && derefString(state.LeaseOwner) != owner {
				acquired = false
				return nil
			}
		}
		leaseUntil := now.Add(ttl).UTC().Format(time.RFC3339)
		_, err = s.execHook(tx,
			`UPDATE sync_state
			 SET lease_owner = ?, lease_until = ?, updated_at = datetime('now')
			 WHERE target_key = ?`,
			owner, leaseUntil, targetKey,
		)
		if err == nil {
			acquired = true
		}
		return err
	})
	return acquired, err
}

func (s *Store) ReleaseSyncLease(targetKey, owner string) error {
	targetKey = normalizeSyncTargetKey(targetKey)
	_, err := s.execHook(s.db,
		`UPDATE sync_state
		 SET lease_owner = NULL, lease_until = NULL, updated_at = datetime('now')
		 WHERE target_key = ? AND (lease_owner = ? OR lease_owner IS NULL OR lease_owner = '')`,
		targetKey, owner,
	)
	return err
}

func (s *Store) MarkSyncBlocked(targetKey, reasonCode, message string) error {
	targetKey = normalizeSyncTargetKey(targetKey)
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := s.getSyncStateTx(tx, targetKey); err != nil {
			return err
		}
		_, err := s.execHook(tx,
			`UPDATE sync_state
			 SET lifecycle = ?, consecutive_failures = 0, backoff_until = NULL, reason_code = ?, reason_message = ?, last_error = ?, updated_at = datetime('now')
			 WHERE target_key = ?`,
			SyncLifecycleDegraded, reasonCode, message, message, targetKey,
		)
		return err
	})
}

func (s *Store) MarkSyncPaused(targetKey, message string) error {
	return s.MarkSyncBlocked(targetKey, "paused", message)
}

func (s *Store) MarkSyncAuthRequired(targetKey, message string) error {
	return s.MarkSyncBlocked(targetKey, "auth_required", message)
}

func (s *Store) MarkSyncFailure(targetKey, message string, backoffUntil time.Time) error {
	targetKey = normalizeSyncTargetKey(targetKey)
	backoff := backoffUntil.UTC().Format(time.RFC3339)
	return s.withTx(func(tx *sql.Tx) error {
		state, err := s.getSyncStateTx(tx, targetKey)
		if err != nil {
			return err
		}
		_, err = s.execHook(tx,
			`UPDATE sync_state
			 SET lifecycle = ?, consecutive_failures = ?, backoff_until = ?, reason_code = ?, reason_message = ?, last_error = ?, updated_at = datetime('now')
			 WHERE target_key = ?`,
			SyncLifecycleDegraded, state.ConsecutiveFailures+1, backoff, "transport_failed", message, message, targetKey,
		)
		return err
	})
}

func (s *Store) MarkSyncHealthy(targetKey string) error {
	targetKey = normalizeSyncTargetKey(targetKey)
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := s.getSyncStateTx(tx, targetKey); err != nil {
			return err
		}
		_, err := s.execHook(tx,
			`UPDATE sync_state
			 SET lifecycle = ?, consecutive_failures = 0, backoff_until = NULL, reason_code = NULL, reason_message = NULL, last_error = NULL, updated_at = datetime('now')
			 WHERE target_key = ?`,
			SyncLifecycleHealthy, targetKey,
		)
		return err
	})
}

func (s *Store) MarkSyncPending(targetKey string) error {
	targetKey = normalizeSyncTargetKey(targetKey)
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := s.getSyncStateTx(tx, targetKey); err != nil {
			return err
		}
		_, err := s.execHook(tx,
			`UPDATE sync_state
			 SET lifecycle = ?, consecutive_failures = 0, backoff_until = NULL, reason_code = NULL, reason_message = NULL, last_error = NULL, updated_at = datetime('now')
			 WHERE target_key = ?`,
			SyncLifecyclePending, targetKey,
		)
		return err
	})
}

func (s *Store) ApplyPulledMutation(targetKey string, mutation SyncMutation) error {
	targetKey = normalizeSyncTargetKey(targetKey)
	return s.withTx(func(tx *sql.Tx) error {
		state, err := s.getSyncStateTx(tx, targetKey)
		if err != nil {
			return err
		}
		if mutation.Seq <= state.LastPulledSeq {
			return nil
		}

		applyErr := s.applyPulledMutationTx(tx, mutation)
		if applyErr != nil {
			// Phase E: per-entity skip+log policy (design §9).
			// For relation FK misses, write to sync_apply_deferred and ACK the seq
			// so the cursor can advance. All other errors propagate and halt the pull.
			if mutation.Entity == SyncEntityRelation && errors.Is(applyErr, ErrRelationFKMissing) {
				log.Printf("[store] ApplyPulledMutation: relation FK miss seq=%d entity_key=%s — deferring",
					mutation.Seq, mutation.EntityKey)
				if _, deferErr := s.execHook(tx, `
					INSERT INTO sync_apply_deferred
						(sync_id, entity, payload, apply_status, retry_count, first_seen_at)
					VALUES (?, ?, ?, 'deferred', 0, datetime('now'))
					ON CONFLICT(sync_id) DO UPDATE SET
						payload            = excluded.payload,
						last_attempted_at  = datetime('now')
				`, mutation.EntityKey, mutation.Entity, mutation.Payload); deferErr != nil {
					return fmt.Errorf("ApplyPulledMutation: write deferred row: %w", deferErr)
				}
				// Fall through to advance the cursor (ACK the seq).
			} else if mutation.Entity == SyncEntityRelation && errors.Is(applyErr, ErrApplyDead) {
				// Payload is permanently undecodable — write directly as dead and ACK.
				// There is no point retrying; a malformed payload will never become valid.
				log.Printf("[store] ApplyPulledMutation: relation payload dead seq=%d entity_key=%s err=%v — marking dead",
					mutation.Seq, mutation.EntityKey, applyErr)
				if _, deferErr := s.execHook(tx, `
					INSERT INTO sync_apply_deferred
						(sync_id, entity, payload, apply_status, retry_count, first_seen_at)
					VALUES (?, ?, ?, 'dead', 0, datetime('now'))
					ON CONFLICT(sync_id) DO UPDATE SET
						payload           = excluded.payload,
						apply_status      = 'dead',
						last_attempted_at = datetime('now')
				`, mutation.EntityKey, mutation.Entity, mutation.Payload); deferErr != nil {
					return fmt.Errorf("ApplyPulledMutation: write dead row: %w", deferErr)
				}
				// Fall through to advance the cursor (ACK the seq).
			} else {
				return applyErr
			}
		}

		_, err = s.execHook(tx,
			`UPDATE sync_state
			 SET last_pulled_seq = ?, lifecycle = ?, consecutive_failures = 0, backoff_until = NULL, reason_code = NULL, reason_message = NULL, last_error = NULL, updated_at = datetime('now')
			 WHERE target_key = ?`,
			mutation.Seq, SyncLifecycleHealthy, targetKey,
		)
		return err
	})
}

// ApplyPulledChunk atomically applies all mutations contained in a pulled chunk
// and records the chunk as synced in the same transaction. This guarantees
// retry safety: a failed chunk import leaves no partial semantic mutations.
func (s *Store) ApplyPulledChunk(targetKey, chunkID string, mutations []SyncMutation) error {
	targetKey = normalizeSyncTargetKey(targetKey)
	chunkTargetKey := normalizeChunkTargetKey(targetKey)
	chunkID = strings.TrimSpace(chunkID)
	if chunkID == "" {
		return fmt.Errorf("chunk id is required")
	}

	return s.withTx(func(tx *sql.Tx) error {
		if _, err := s.getSyncStateTx(tx, targetKey); err != nil {
			return err
		}

		var alreadyImported int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sync_chunks WHERE target_key = ? AND chunk_id = ?`, chunkTargetKey, chunkID).Scan(&alreadyImported); err != nil {
			return err
		}
		if alreadyImported > 0 {
			return nil
		}

		state, err := s.getSyncStateTx(tx, targetKey)
		if err != nil {
			return err
		}

		seq := state.LastPulledSeq
		for i, mutation := range mutations {
			seq++
			mutation.Seq = seq
			mutation.TargetKey = targetKey
			mutation.Source = SyncSourceRemote
			if err := s.applyPulledMutationTx(tx, mutation); err != nil {
				return fmt.Errorf("apply chunk mutation %d: %w", i, err)
			}
		}

		if _, err := s.execHook(tx,
			`INSERT OR IGNORE INTO sync_chunks (target_key, chunk_id) VALUES (?, ?)`,
			chunkTargetKey, chunkID,
		); err != nil {
			return err
		}

		_, err = s.execHook(tx,
			`UPDATE sync_state
			 SET last_pulled_seq = ?, lifecycle = ?, consecutive_failures = 0, backoff_until = NULL, reason_code = NULL, reason_message = NULL, last_error = NULL, updated_at = datetime('now')
			 WHERE target_key = ?`,
			seq, SyncLifecycleHealthy, targetKey,
		)
		return err
	})
}

func (s *Store) GetObservationBySyncID(syncID string) (*Observation, error) {
	row := s.db.QueryRow(
		`SELECT id, ifnull(sync_id, '') as sync_id, session_id, type, title, content, tool_name, project,
		        scope, topic_key, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at
		 FROM observations WHERE sync_id = ? AND deleted_at IS NULL ORDER BY id DESC LIMIT 1`,
		syncID,
	)
	var o Observation
	if err := row.Scan(&o.ID, &o.SyncID, &o.SessionID, &o.Type, &o.Title, &o.Content, &o.ToolName, &o.Project, &o.Scope, &o.TopicKey, &o.RevisionCount, &o.DuplicateCount, &o.LastSeenAt, &o.CreatedAt, &o.UpdatedAt, &o.DeletedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

// ─── Project Enrollment for Cloud Sync ───────────────────────────────────────

// EnrollProject registers a project for cloud sync. Idempotent — re-enrolling
// an already-enrolled project is a no-op.
func (s *Store) EnrollProject(project string) error {
	project, _ = NormalizeProject(project)
	if project == "" {
		return fmt.Errorf("project name must not be empty")
	}
	return s.withTx(func(tx *sql.Tx) error {
		res, err := s.execHook(tx,
			`INSERT OR IGNORE INTO sync_enrolled_projects (project) VALUES (?)`,
			project,
		)
		if err != nil {
			return err
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return nil
		}
		return s.backfillProjectSyncMutationsTx(tx, project)
	})
}

// UnenrollProject removes a project from cloud sync enrollment. Idempotent —
// unenrolling a non-enrolled project is a no-op.
func (s *Store) UnenrollProject(project string) error {
	project, _ = NormalizeProject(project)
	if project == "" {
		return fmt.Errorf("project name must not be empty")
	}
	_, err := s.execHook(s.db,
		`DELETE FROM sync_enrolled_projects WHERE project = ?`,
		project,
	)
	return err
}

// ListEnrolledProjects returns all projects currently enrolled for cloud sync,
// ordered alphabetically by project name.
func (s *Store) ListEnrolledProjects() ([]EnrolledProject, error) {
	rows, err := s.queryItHook(s.db,
		`SELECT project, enrolled_at FROM sync_enrolled_projects ORDER BY project ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []EnrolledProject
	for rows.Next() {
		var ep EnrolledProject
		if err := rows.Scan(&ep.Project, &ep.EnrolledAt); err != nil {
			return nil, err
		}
		projects = append(projects, ep)
	}
	return projects, rows.Err()
}

// IsProjectEnrolled returns true if the given project is enrolled for cloud sync.
func (s *Store) IsProjectEnrolled(project string) (bool, error) {
	project, _ = NormalizeProject(project)
	if project == "" {
		return false, nil
	}
	var exists int
	err := s.db.QueryRow(
		`SELECT 1 FROM sync_enrolled_projects WHERE project = ? LIMIT 1`,
		project,
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ─── Project Migration ───────────────────────────────────────────────────────

type MigrateResult struct {
	Migrated            bool  `json:"migrated"`
	ObservationsUpdated int64 `json:"observations_updated"`
	SessionsUpdated     int64 `json:"sessions_updated"`
	PromptsUpdated      int64 `json:"prompts_updated"`
}

func (s *Store) MigrateProject(oldName, newName string) (*MigrateResult, error) {
	if oldName == "" || newName == "" || oldName == newName {
		return &MigrateResult{}, nil
	}

	// Check if old project has any records (short-circuit on first match)
	var exists bool
	err := s.db.QueryRow(
		`SELECT EXISTS(
			SELECT 1 FROM observations WHERE project = ?
			UNION ALL
			SELECT 1 FROM sessions WHERE project = ?
			UNION ALL
			SELECT 1 FROM user_prompts WHERE project = ?
		)`, oldName, oldName, oldName,
	).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("check old project: %w", err)
	}
	if !exists {
		return &MigrateResult{}, nil
	}

	result := &MigrateResult{Migrated: true}

	err = s.withTx(func(tx *sql.Tx) error {
		// FTS triggers handle index updates automatically on UPDATE
		res, err := s.execHook(tx, `UPDATE observations SET project = ? WHERE project = ?`, newName, oldName)
		if err != nil {
			return fmt.Errorf("migrate observations: %w", err)
		}
		result.ObservationsUpdated, _ = res.RowsAffected()

		res, err = s.execHook(tx, `UPDATE sessions SET project = ? WHERE project = ?`, newName, oldName)
		if err != nil {
			return fmt.Errorf("migrate sessions: %w", err)
		}
		result.SessionsUpdated, _ = res.RowsAffected()

		res, err = s.execHook(tx, `UPDATE user_prompts SET project = ? WHERE project = ?`, newName, oldName)
		if err != nil {
			return fmt.Errorf("migrate prompts: %w", err)
		}
		result.PromptsUpdated, _ = res.RowsAffected()

		// Enqueue sync mutations so cloud sync picks up the migrated records.
		// Same pattern used by EnrollProject and MergeProjects.
		return s.backfillProjectSyncMutationsTx(tx, newName)
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// ─── Project Queries ──────────────────────────────────────────────────────────

// ProjectNameCount holds a project name and how many observations it has.
type ProjectNameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ListProjectNames returns all distinct project names from observations,
// ordered alphabetically. Used for fuzzy matching and consolidation.
func (s *Store) ListProjectNames() ([]string, error) {
	rows, err := s.queryItHook(s.db,
		`SELECT DISTINCT project FROM observations
		 WHERE project IS NOT NULL AND project != '' AND deleted_at IS NULL
		 ORDER BY project`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		results = append(results, name)
	}
	return results, rows.Err()
}

// ProjectStats holds aggregate statistics for a single project.
type ProjectStats struct {
	Name             string   `json:"name"`
	ObservationCount int      `json:"observation_count"`
	SessionCount     int      `json:"session_count"`
	PromptCount      int      `json:"prompt_count"`
	Directories      []string `json:"directories"` // unique directories from sessions
}

// ListProjectsWithStats returns all projects with aggregated counts.
// Ordered by observation count descending.
func (s *Store) ListProjectsWithStats() ([]ProjectStats, error) {
	// Observation counts per project
	obsRows, err := s.queryItHook(s.db,
		`SELECT project, COUNT(*) as cnt
		 FROM observations
		 WHERE project IS NOT NULL AND project != '' AND deleted_at IS NULL
		 GROUP BY project`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects obs: %w", err)
	}
	defer obsRows.Close()

	statsMap := make(map[string]*ProjectStats)
	for obsRows.Next() {
		var name string
		var cnt int
		if err := obsRows.Scan(&name, &cnt); err != nil {
			return nil, err
		}
		statsMap[name] = &ProjectStats{Name: name, ObservationCount: cnt}
	}
	if err := obsRows.Err(); err != nil {
		return nil, err
	}

	// Session counts + directories per project
	sessRows, err := s.queryItHook(s.db,
		`SELECT project, COUNT(*) as cnt, directory
		 FROM sessions
		 WHERE project IS NOT NULL AND project != ''
		 GROUP BY project, directory`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects sessions: %w", err)
	}
	defer sessRows.Close()

	type projDir struct {
		count int
		dirs  map[string]bool
	}
	sessData := make(map[string]*projDir)
	for sessRows.Next() {
		var name, dir string
		var cnt int
		if err := sessRows.Scan(&name, &cnt, &dir); err != nil {
			return nil, err
		}
		if sessData[name] == nil {
			sessData[name] = &projDir{dirs: make(map[string]bool)}
		}
		sessData[name].count += cnt
		if dir != "" {
			sessData[name].dirs[dir] = true
		}
	}
	if err := sessRows.Err(); err != nil {
		return nil, err
	}

	for name, sd := range sessData {
		if statsMap[name] == nil {
			statsMap[name] = &ProjectStats{Name: name}
		}
		statsMap[name].SessionCount = sd.count
		for d := range sd.dirs {
			statsMap[name].Directories = append(statsMap[name].Directories, d)
		}
	}

	// Prompt counts per project
	promptRows, err := s.queryItHook(s.db,
		`SELECT project, COUNT(*) as cnt
		 FROM user_prompts
		 WHERE project IS NOT NULL AND project != ''
		 GROUP BY project`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects prompts: %w", err)
	}
	defer promptRows.Close()

	for promptRows.Next() {
		var name string
		var cnt int
		if err := promptRows.Scan(&name, &cnt); err != nil {
			return nil, err
		}
		if statsMap[name] == nil {
			statsMap[name] = &ProjectStats{Name: name}
		}
		statsMap[name].PromptCount = cnt
	}
	if err := promptRows.Err(); err != nil {
		return nil, err
	}

	// Convert to slice, sorted by observation count descending
	results := make([]ProjectStats, 0, len(statsMap))
	for _, ps := range statsMap {
		results = append(results, *ps)
	}
	// Simple insertion sort — project lists are small
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].ObservationCount > results[j-1].ObservationCount; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}

	return results, nil
}

// CountObservationsForProject returns the number of non-deleted observations
// for the given project name. Used by handleSave for the similar-project
// warning instead of the heavier ListProjectsWithStats.
func (s *Store) CountObservationsForProject(name string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM observations WHERE project = ? AND deleted_at IS NULL`,
		name,
	).Scan(&count)
	return count, err
}

// MergeResult summarizes the result of merging multiple project name variants
// into a single canonical project name.
type MergeResult struct {
	Canonical           string   `json:"canonical"`
	SourcesMerged       []string `json:"sources_merged"`
	ObservationsUpdated int64    `json:"observations_updated"`
	SessionsUpdated     int64    `json:"sessions_updated"`
	PromptsUpdated      int64    `json:"prompts_updated"`
}

// MergeProjects migrates all records from each source project name into the
// canonical name. Sources that equal the canonical (after normalization) or
// have no records are silently skipped — the operation is idempotent.
// All updates are performed inside a single transaction for atomicity.
func (s *Store) MergeProjects(sources []string, canonical string) (*MergeResult, error) {
	canonical, _ = NormalizeProject(canonical)
	if canonical == "" {
		return nil, fmt.Errorf("canonical project name must not be empty")
	}

	result := &MergeResult{Canonical: canonical}

	err := s.withTx(func(tx *sql.Tx) error {
		seenSources := make(map[string]struct{})
		for _, srcInput := range sources {
			srcNormalized, _ := NormalizeProject(srcInput)
			if srcNormalized == "" || srcNormalized == canonical {
				continue
			}
			if _, seen := seenSources[srcNormalized]; seen {
				continue
			}
			seenSources[srcNormalized] = struct{}{}

			sourceVariants := projectMergeSourceVariants(srcInput, srcNormalized, canonical)
			if len(sourceVariants) == 0 {
				continue
			}

			placeholders := sqlPlaceholders(len(sourceVariants))
			args := make([]any, 0, len(sourceVariants)+1)
			args = append(args, canonical)
			for _, variant := range sourceVariants {
				args = append(args, variant)
			}

			res, err := s.execHook(tx, `UPDATE observations SET project = ? WHERE project IN (`+placeholders+`)`, args...)
			if err != nil {
				return fmt.Errorf("merge observations %q → %q: %w", srcNormalized, canonical, err)
			}
			n, _ := res.RowsAffected()
			result.ObservationsUpdated += n

			res, err = s.execHook(tx, `UPDATE sessions SET project = ? WHERE project IN (`+placeholders+`)`, args...)
			if err != nil {
				return fmt.Errorf("merge sessions %q → %q: %w", srcNormalized, canonical, err)
			}
			n, _ = res.RowsAffected()
			result.SessionsUpdated += n

			res, err = s.execHook(tx, `UPDATE user_prompts SET project = ? WHERE project IN (`+placeholders+`)`, args...)
			if err != nil {
				return fmt.Errorf("merge prompts %q → %q: %w", srcNormalized, canonical, err)
			}
			n, _ = res.RowsAffected()
			result.PromptsUpdated += n

			result.SourcesMerged = append(result.SourcesMerged, srcNormalized)
		}
		// Enqueue sync mutations so cloud sync picks up the merged records.
		// Same pattern used by EnrollProject.
		return s.backfillProjectSyncMutationsTx(tx, canonical)
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// sqlPlaceholders returns a comma-separated list of parameter markers only.
// Values are still passed separately through query arguments; no user data is
// interpolated into SQL here.
func sqlPlaceholders(count int) string {
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func projectMergeSourceVariants(rawSource, normalizedSource, canonical string) []string {
	seen := make(map[string]struct{})
	variants := make([]string, 0, 5)
	// Match both the historical raw project name and its normalized form so
	// legacy rows are migrated without reintroducing canonical-source churn.
	candidates := []string{strings.TrimSpace(rawSource), normalizedSource}
	parts := strings.FieldsFunc(normalizedSource, func(r rune) bool {
		return r == ' ' || r == '-' || r == '_'
	})
	if len(parts) > 1 {
		for _, sep := range []string{" ", "-", "_"} {
			candidates = append(candidates, strings.Join(parts, sep))
		}
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || candidate == canonical {
			continue
		}
		candidateNormalized, _ := NormalizeProject(candidate)
		if candidateNormalized == canonical {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		variants = append(variants, candidate)
	}
	return variants
}

// ─── Project Pruning ─────────────────────────────────────────────────────────

// PruneResult holds the outcome of pruning a single project.
type PruneResult struct {
	Project         string `json:"project"`
	SessionsDeleted int64  `json:"sessions_deleted"`
	PromptsDeleted  int64  `json:"prompts_deleted"`
}

// PruneProject removes all sessions and prompts for a project that has zero
// (non-deleted) observations. Returns an error if the project still has
// observations — the caller must verify first.
func (s *Store) PruneProject(project string) (*PruneResult, error) {
	if project == "" {
		return nil, fmt.Errorf("project name must not be empty")
	}

	// Safety check: refuse to prune if observations exist.
	count, err := s.CountObservationsForProject(project)
	if err != nil {
		return nil, fmt.Errorf("count observations: %w", err)
	}
	if count > 0 {
		return nil, fmt.Errorf("project %q still has %d observations — cannot prune", project, count)
	}

	result := &PruneResult{Project: project}

	err = s.withTx(func(tx *sql.Tx) error {
		res, err := s.execHook(tx, `DELETE FROM user_prompts WHERE project = ?`, project)
		if err != nil {
			return fmt.Errorf("prune prompts: %w", err)
		}
		result.PromptsDeleted, _ = res.RowsAffected()

		res, err = s.execHook(tx, `DELETE FROM sessions WHERE project = ?`, project)
		if err != nil {
			return fmt.Errorf("prune sessions: %w", err)
		}
		result.SessionsDeleted, _ = res.RowsAffected()

		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (s *Store) withTx(fn func(tx *sql.Tx) error) error {
	return withSQLiteWriteRetry(func() error {
		tx, err := s.beginTxHook()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := fn(tx); err != nil {
			return err
		}
		return s.commitHook(tx)
	})
}

func withSQLiteWriteRetry(fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= len(sqliteWriteRetryBackoffs); attempt++ {
		if err := fn(); err != nil {
			lastErr = err
			if !isRetryableSQLiteLockError(err) || attempt == len(sqliteWriteRetryBackoffs) {
				return err
			}
			time.Sleep(sqliteWriteRetryBackoffs[attempt])
			continue
		}
		return nil
	}
	return lastErr
}

func isRetryableSQLiteLockError(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		primaryCode := sqliteErr.Code() & 0xff
		return primaryCode == sqlitePrimaryBusy || primaryCode == sqlitePrimaryLocked
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "database is busy") || strings.Contains(msg, "sqlite_busy") || strings.Contains(msg, "sqlite_locked")
}

func (s *Store) createSessionTx(tx *sql.Tx, id, project, directory string) error {
	_, err := s.execHook(tx,
		`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   project   = CASE WHEN sessions.project = '' THEN excluded.project ELSE sessions.project END,
		   directory = CASE WHEN sessions.directory = '' THEN excluded.directory ELSE sessions.directory END`,
		id, project, directory,
	)
	return err
}

func (s *Store) ensureSyncState(targetKey string) error {
	_, err := s.execHook(s.db,
		`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, ?, datetime('now'))`,
		targetKey, SyncLifecycleIdle,
	)
	return err
}

func (s *Store) getSyncState(targetKey string) (*SyncState, error) {
	row := s.db.QueryRow(`
		SELECT target_key, lifecycle, last_enqueued_seq, last_acked_seq, last_pulled_seq,
		       consecutive_failures, backoff_until, lease_owner, lease_until, reason_code, reason_message, last_error, updated_at
		FROM sync_state WHERE target_key = ?`, targetKey)
	var state SyncState
	if err := row.Scan(&state.TargetKey, &state.Lifecycle, &state.LastEnqueuedSeq, &state.LastAckedSeq, &state.LastPulledSeq, &state.ConsecutiveFailures, &state.BackoffUntil, &state.LeaseOwner, &state.LeaseUntil, &state.ReasonCode, &state.ReasonMessage, &state.LastError, &state.UpdatedAt); err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *Store) getSyncStateTx(tx *sql.Tx, targetKey string) (*SyncState, error) {
	if _, err := s.execHook(tx,
		`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, ?, datetime('now'))`,
		targetKey, SyncLifecycleIdle,
	); err != nil {
		return nil, err
	}
	row := tx.QueryRow(`
		SELECT target_key, lifecycle, last_enqueued_seq, last_acked_seq, last_pulled_seq,
		       consecutive_failures, backoff_until, lease_owner, lease_until, reason_code, reason_message, last_error, updated_at
		FROM sync_state WHERE target_key = ?`, targetKey)
	var state SyncState
	if err := row.Scan(&state.TargetKey, &state.Lifecycle, &state.LastEnqueuedSeq, &state.LastAckedSeq, &state.LastPulledSeq, &state.ConsecutiveFailures, &state.BackoffUntil, &state.LeaseOwner, &state.LeaseUntil, &state.ReasonCode, &state.ReasonMessage, &state.LastError, &state.UpdatedAt); err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *Store) backfillProjectSyncMutationsTx(tx *sql.Tx, project string) error {
	if err := s.backfillSessionSyncMutationsTx(tx, project); err != nil {
		return err
	}
	if err := s.backfillObservationSyncMutationsTx(tx, project); err != nil {
		return err
	}
	return s.backfillPromptSyncMutationsTx(tx, project)
}

// projectNeedsBackfill returns true when a project has any sessions, live observations,
// or prompts that are missing a corresponding sync_mutation row.
// It runs three lightweight COUNT queries — no cursor is held open.
func (s *Store) projectNeedsBackfill(project string) (bool, error) {
	type countQuery struct {
		q    string
		args []any
	}
	queries := []countQuery{
		{
			q: `SELECT COUNT(*) FROM sessions
			    WHERE project = ?
			      AND NOT EXISTS (
			        SELECT 1 FROM sync_mutations sm
			        WHERE sm.target_key = ? AND sm.entity = ? AND sm.entity_key = sessions.id AND sm.source = ?
			      )`,
			args: []any{project, DefaultSyncTargetKey, SyncEntitySession, SyncSourceLocal},
		},
		{
			q: `SELECT COUNT(*) FROM observations o
			    LEFT JOIN sessions s ON s.id = o.session_id
			    WHERE (ifnull(o.project,'') = ? OR (ifnull(o.project,'') = '' AND ifnull(s.project,'') = ?))
			      AND o.deleted_at IS NULL
			      AND NOT EXISTS (
			        SELECT 1 FROM sync_mutations sm
			        WHERE sm.target_key = ? AND sm.entity = ? AND sm.entity_key = o.sync_id AND sm.source = ?
			      )`,
			args: []any{project, project, DefaultSyncTargetKey, SyncEntityObservation, SyncSourceLocal},
		},
		{
			q: `SELECT COUNT(*) FROM user_prompts p
			    LEFT JOIN sessions s ON s.id = p.session_id
			    WHERE (ifnull(p.project,'') = ? OR (ifnull(p.project,'') = '' AND ifnull(s.project,'') = ?))
			      AND NOT EXISTS (
			        SELECT 1 FROM sync_mutations sm
			        WHERE sm.target_key = ? AND sm.entity = ? AND sm.entity_key = p.sync_id AND sm.source = ?
			      )`,
			args: []any{project, project, DefaultSyncTargetKey, SyncEntityPrompt, SyncSourceLocal},
		},
	}
	for _, cq := range queries {
		var n int
		if err := s.db.QueryRow(cq.q, cq.args...).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) repairEnrolledProjectSyncMutations() error {
	// Collect enrolled projects outside a transaction so we avoid holding a read
	// cursor open while we later write inside backfillProjectSyncMutationsTx.
	rows, err := s.db.Query(`SELECT project FROM sync_enrolled_projects ORDER BY project ASC`)
	if err != nil {
		return err
	}
	var projects []string
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			return closeRowsWithError(rows, err)
		}
		projects = append(projects, project)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, project := range projects {
		// Fast path: if the project is already fully backfilled, skip the write tx entirely.
		needs, err := s.projectNeedsBackfill(project)
		if err != nil {
			return err
		}
		if !needs {
			continue
		}
		if err := s.withTx(func(tx *sql.Tx) error {
			return s.backfillProjectSyncMutationsTx(tx, project)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) backfillSessionSyncMutationsTx(tx *sql.Tx, project string) error {
	rows, err := s.queryItHook(tx, `
		SELECT id, project, directory, started_at, ended_at, summary
		FROM sessions
		WHERE project = ?
		  AND NOT EXISTS (
			SELECT 1
			FROM sync_mutations sm
			WHERE sm.target_key = ?
			  AND sm.entity = ?
			  AND sm.entity_key = sessions.id
			  AND sm.source = ?
		  )
		ORDER BY started_at ASC, id ASC`,
		project, DefaultSyncTargetKey, SyncEntitySession, SyncSourceLocal,
	)
	if err != nil {
		return err
	}

	// Phase 1: collect all missing sessions into memory before any INSERT.
	// Keeping the cursor open while inserting into sync_mutations causes SQLite
	// to re-evaluate the NOT EXISTS subquery against the in-progress write set,
	// which can produce an O(N*M) busy loop on large stores.
	var pending []syncSessionPayload
	for rows.Next() {
		var payload syncSessionPayload
		if err := rows.Scan(&payload.ID, &payload.Project, &payload.Directory, &payload.StartedAt, &payload.EndedAt, &payload.Summary); err != nil {
			return closeRowsWithError(rows, err)
		}
		pending = append(pending, payload)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Phase 2: insert now that the read cursor is closed.
	for _, payload := range pending {
		if err := s.enqueueSyncMutationTx(tx, SyncEntitySession, payload.ID, SyncOpUpsert, payload); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) backfillObservationSyncMutationsTx(tx *sql.Tx, project string) error {
	// ── Live observations ─────────────────────────────────────────────────────
	rows, err := s.queryItHook(tx, `
		SELECT o.sync_id, o.session_id, o.type, o.title, o.content, o.tool_name, o.project, o.scope, o.topic_key,
		       o.revision_count, o.duplicate_count, o.last_seen_at, o.created_at, o.updated_at
		FROM observations o
		LEFT JOIN sessions s ON s.id = o.session_id
		WHERE (
			ifnull(o.project, '') = ?
			OR (ifnull(o.project, '') = '' AND ifnull(s.project, '') = ?)
		)
		  AND deleted_at IS NULL
		  AND NOT EXISTS (
			SELECT 1
			FROM sync_mutations sm
			WHERE sm.target_key = ?
			  AND sm.entity = ?
			  AND sm.entity_key = o.sync_id
			  AND sm.source = ?
		  )
		ORDER BY o.id ASC`,
		project, project, DefaultSyncTargetKey, SyncEntityObservation, SyncSourceLocal,
	)
	if err != nil {
		return err
	}

	// Phase 1: collect live observations before any INSERT.
	var pending []syncObservationPayload
	for rows.Next() {
		var payload syncObservationPayload
		if err := rows.Scan(
			&payload.SyncID,
			&payload.SessionID,
			&payload.Type,
			&payload.Title,
			&payload.Content,
			&payload.ToolName,
			&payload.Project,
			&payload.Scope,
			&payload.TopicKey,
			&payload.RevisionCount,
			&payload.DuplicateCount,
			&payload.LastSeenAt,
			&payload.CreatedAt,
			&payload.UpdatedAt,
		); err != nil {
			return closeRowsWithError(rows, err)
		}
		pending = append(pending, payload)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Phase 2: insert live observation mutations.
	for _, payload := range pending {
		if err := s.enqueueSyncMutationTx(tx, SyncEntityObservation, payload.SyncID, SyncOpUpsert, payload); err != nil {
			return err
		}
	}

	// ── Deleted observations ──────────────────────────────────────────────────
	deletedRows, err := s.queryItHook(tx, `
		SELECT o.sync_id, o.session_id, o.project, o.deleted_at
		FROM observations o
		LEFT JOIN sessions s ON s.id = o.session_id
		WHERE (
			ifnull(o.project, '') = ?
			OR (ifnull(o.project, '') = '' AND ifnull(s.project, '') = ?)
		)
		  AND o.deleted_at IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1
			FROM sync_mutations sm
			WHERE sm.target_key = ?
			  AND sm.entity = ?
			  AND sm.entity_key = o.sync_id
			  AND sm.op = ?
			  AND sm.source = ?
		  )
		ORDER BY o.id ASC`,
		project, project, DefaultSyncTargetKey, SyncEntityObservation, SyncOpDelete, SyncSourceLocal,
	)
	if err != nil {
		return err
	}

	// Phase 1: collect deleted observations before any INSERT.
	var deletedPending []syncObservationPayload
	for deletedRows.Next() {
		var payload syncObservationPayload
		if err := deletedRows.Scan(&payload.SyncID, &payload.SessionID, &payload.Project, &payload.DeletedAt); err != nil {
			return closeRowsWithError(deletedRows, err)
		}
		payload.Deleted = true
		payload.HardDelete = false
		deletedPending = append(deletedPending, payload)
	}
	if err := deletedRows.Close(); err != nil {
		return err
	}
	if err := deletedRows.Err(); err != nil {
		return err
	}

	// Phase 2: insert deleted observation mutations.
	for _, payload := range deletedPending {
		if err := s.enqueueSyncMutationTx(tx, SyncEntityObservation, payload.SyncID, SyncOpDelete, payload); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) backfillPromptSyncMutationsTx(tx *sql.Tx, project string) error {
	// ── Live prompts ──────────────────────────────────────────────────────────
	rows, err := s.queryItHook(tx, `
		SELECT p.sync_id, p.session_id, p.content, p.project, p.created_at
		FROM user_prompts p
		LEFT JOIN sessions s ON s.id = p.session_id
		WHERE (
			ifnull(p.project, '') = ?
			OR (ifnull(p.project, '') = '' AND ifnull(s.project, '') = ?)
		)
		  AND NOT EXISTS (
			SELECT 1
			FROM sync_mutations sm
			WHERE sm.target_key = ?
			  AND sm.entity = ?
			  AND sm.entity_key = p.sync_id
			  AND sm.source = ?
		  )
		ORDER BY p.id ASC`,
		project, project, DefaultSyncTargetKey, SyncEntityPrompt, SyncSourceLocal,
	)
	if err != nil {
		return err
	}

	// Phase 1: collect live prompts before any INSERT.
	var pending []syncPromptPayload
	for rows.Next() {
		var payload syncPromptPayload
		if err := rows.Scan(&payload.SyncID, &payload.SessionID, &payload.Content, &payload.Project, &payload.CreatedAt); err != nil {
			return closeRowsWithError(rows, err)
		}
		pending = append(pending, payload)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Phase 2: insert live prompt mutations.
	for _, payload := range pending {
		if err := s.enqueueSyncMutationTx(tx, SyncEntityPrompt, payload.SyncID, SyncOpUpsert, payload); err != nil {
			return err
		}
	}

	// ── Tombstoned prompts ────────────────────────────────────────────────────
	tombstoneRows, err := s.queryItHook(tx, `
		SELECT prompt_tombstones.sync_id, prompt_tombstones.session_id, prompt_tombstones.project, prompt_tombstones.deleted_at
		FROM prompt_tombstones
		LEFT JOIN sessions s ON s.id = prompt_tombstones.session_id
		WHERE (
			ifnull(prompt_tombstones.project, '') = ?
			OR (ifnull(prompt_tombstones.project, '') = '' AND ifnull(s.project, '') = ?)
		)
		  AND NOT EXISTS (
			SELECT 1
			FROM sync_mutations sm
			WHERE sm.target_key = ?
			  AND sm.entity = ?
			  AND sm.entity_key = prompt_tombstones.sync_id
			  AND sm.source = ?
			  AND sm.op = ?
		  )
		ORDER BY deleted_at ASC`,
		project, project, DefaultSyncTargetKey, SyncEntityPrompt, SyncSourceLocal, SyncOpDelete,
	)
	if err != nil {
		return err
	}

	// Phase 1: collect tombstones before any INSERT.
	var tombstonePending []syncPromptPayload
	for tombstoneRows.Next() {
		var payload syncPromptPayload
		if err := tombstoneRows.Scan(&payload.SyncID, &payload.SessionID, &payload.Project, &payload.DeletedAt); err != nil {
			return closeRowsWithError(tombstoneRows, err)
		}
		payload.Deleted = true
		payload.HardDelete = true
		tombstonePending = append(tombstonePending, payload)
	}
	if err := tombstoneRows.Close(); err != nil {
		return err
	}
	if err := tombstoneRows.Err(); err != nil {
		return err
	}

	// Phase 2: insert tombstone mutations.
	for _, payload := range tombstonePending {
		if err := s.enqueueSyncMutationTx(tx, SyncEntityPrompt, payload.SyncID, SyncOpDelete, payload); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) enqueueSyncMutationTx(tx *sql.Tx, entity, entityKey, op string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	project := extractProjectFromPayload(payload)
	project, _ = NormalizeProject(strings.TrimSpace(project))
	if project == "" {
		sessionID := extractSessionIDFromPayload(payload)
		if sessionID != "" {
			if derived, err := s.resolveSessionProjectTx(tx, sessionID); err != nil {
				return err
			} else {
				project = derived
			}
		}
	}
	if _, err := s.execHook(tx,
		`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, ?, datetime('now'))`,
		DefaultSyncTargetKey, SyncLifecycleIdle,
	); err != nil {
		return err
	}
	res, err := s.execHook(tx,
		`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		DefaultSyncTargetKey, entity, entityKey, op, string(encoded), SyncSourceLocal, project,
	)
	if err != nil {
		return err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = s.execHook(tx,
		`UPDATE sync_state
		 SET lifecycle = ?, last_enqueued_seq = ?, updated_at = datetime('now')
		 WHERE target_key = ?`,
		SyncLifecyclePending, seq, DefaultSyncTargetKey,
	)
	if err != nil {
		return err
	}
	if project == "" {
		return nil
	}
	projectTargetKey := syncTargetKeyForProject(project)
	if _, err := s.execHook(tx,
		`INSERT OR IGNORE INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, ?, datetime('now'))`,
		projectTargetKey, SyncLifecycleIdle,
	); err != nil {
		return err
	}
	_, err = s.execHook(tx,
		`UPDATE sync_state
		 SET lifecycle = ?, last_enqueued_seq = ?, updated_at = datetime('now')
		 WHERE target_key = ?`,
		SyncLifecyclePending, seq, projectTargetKey,
	)
	return err
}

func syncTargetKeyForProject(project string) string {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return DefaultSyncTargetKey
	}
	return fmt.Sprintf("%s:%s", DefaultSyncTargetKey, project)
}

func extractSessionIDFromPayload(payload any) string {
	switch p := payload.(type) {
	case syncObservationPayload:
		return strings.TrimSpace(p.SessionID)
	case syncPromptPayload:
		return strings.TrimSpace(p.SessionID)
	default:
		data, err := json.Marshal(payload)
		if err != nil {
			return ""
		}
		var generic struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(data, &generic); err != nil {
			return ""
		}
		return strings.TrimSpace(generic.SessionID)
	}
}

func (s *Store) resolveSessionProjectTx(tx *sql.Tx, sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", nil
	}
	var project string
	err := tx.QueryRow(`SELECT ifnull(project, '') FROM sessions WHERE id = ?`, sessionID).Scan(&project)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	project, _ = NormalizeProject(strings.TrimSpace(project))
	return project, nil
}

func (s *Store) applyPulledMutationTx(tx *sql.Tx, mutation SyncMutation) error {
	switch mutation.Entity {
	case SyncEntityRelation:
		return s.applyRelationUpsertTx(tx, mutation)
	case SyncEntitySession:
		var payload syncSessionPayload
		if err := decodeSyncPayload([]byte(mutation.Payload), &payload); err != nil {
			return err
		}
		if strings.TrimSpace(payload.ID) == "" {
			payload.ID = strings.TrimSpace(mutation.EntityKey)
		}
		if mutation.Op == SyncOpDelete || isSessionDeletePayload(payload) {
			return s.applySessionDeleteTx(tx, payload)
		}
		return s.applySessionPayloadTx(tx, payload)
	case SyncEntityObservation:
		var payload syncObservationPayload
		if err := decodeSyncPayload([]byte(mutation.Payload), &payload); err != nil {
			return err
		}
		if mutation.Op == SyncOpDelete {
			return s.applyObservationDeleteTx(tx, payload)
		}
		return s.applyObservationUpsertTx(tx, payload)
	case SyncEntityPrompt:
		var payload syncPromptPayload
		if err := decodeSyncPayload([]byte(mutation.Payload), &payload); err != nil {
			return err
		}
		if mutation.Op == SyncOpDelete || payload.Deleted || payload.HardDelete {
			return s.applyPromptDeleteTx(tx, payload)
		}
		return s.applyPromptUpsertTx(tx, payload)
	default:
		return fmt.Errorf("unknown sync entity %q", mutation.Entity)
	}
}

// applyRelationUpsertTx handles a pulled mutation with entity='relation' and
// op='upsert'. It implements the pull-side behavior for Phase 2:
//
//  1. JSON-decode the payload into syncRelationPayload. Decode errors return
//     ErrApplyDead (non-retryable).
//  2. Verify both source and target observations exist locally by sync_id.
//     If either is missing, return ErrRelationFKMissing. The caller must write
//     the raw mutation to sync_apply_deferred and ACK the seq.
//  3. INSERT INTO memory_relations with ON CONFLICT(sync_id) DO UPDATE
//     (last-write-wins, preserving the original created_at).
//  4. On successful apply, DELETE any pre-existing deferred row for this sync_id
//     so it is not retried unnecessarily.
func (s *Store) applyRelationUpsertTx(tx *sql.Tx, mutation SyncMutation) error {
	// Step 1: decode payload.
	var p syncRelationPayload
	if err := decodeSyncPayload([]byte(mutation.Payload), &p); err != nil {
		return fmt.Errorf("%w: decode relation payload: %v", ErrApplyDead, err)
	}

	// Step 1b: required field validation — missing source_id or target_id is not
	// a retryable FK miss; it is a permanent payload defect (ErrApplyDead).
	if strings.TrimSpace(p.SourceID) == "" || strings.TrimSpace(p.TargetID) == "" {
		return fmt.Errorf("%w: relation payload missing required source_id or target_id", ErrApplyDead)
	}

	// Step 2: FK precondition — both observations must exist locally (by sync_id).
	var obsCount int
	if err := tx.QueryRow(
		`SELECT count(*) FROM observations WHERE sync_id IN (?, ?)`,
		p.SourceID, p.TargetID,
	).Scan(&obsCount); err != nil {
		return fmt.Errorf("applyRelationUpsertTx: check observations: %w", err)
	}
	if obsCount < 2 {
		return ErrRelationFKMissing
	}

	// Step 3: upsert into memory_relations keyed on sync_id (idempotent re-apply).
	if _, err := s.execHook(tx, `
		INSERT INTO memory_relations
			(sync_id, source_id, target_id, relation, reason, evidence, confidence,
			 judgment_status, marked_by_actor, marked_by_kind, marked_by_model,
			 session_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(sync_id) DO UPDATE SET
			source_id       = excluded.source_id,
			target_id       = excluded.target_id,
			relation        = excluded.relation,
			reason          = excluded.reason,
			evidence        = excluded.evidence,
			confidence      = excluded.confidence,
			judgment_status = excluded.judgment_status,
			marked_by_actor = excluded.marked_by_actor,
			marked_by_kind  = excluded.marked_by_kind,
			marked_by_model = excluded.marked_by_model,
			session_id      = excluded.session_id,
			updated_at      = excluded.updated_at
	`,
		p.SyncID, p.SourceID, p.TargetID, p.Relation,
		p.Reason, p.Evidence, p.Confidence,
		p.JudgmentStatus, p.MarkedByActor, p.MarkedByKind, p.MarkedByModel,
		p.SessionID, p.CreatedAt, p.UpdatedAt,
	); err != nil {
		return fmt.Errorf("applyRelationUpsertTx: upsert: %w", err)
	}

	// Step 4: clean up deferred row if one exists (resolves any prior FK-miss deferral).
	if _, err := s.execHook(tx,
		`DELETE FROM sync_apply_deferred WHERE sync_id = ?`, p.SyncID,
	); err != nil {
		return fmt.Errorf("applyRelationUpsertTx: clear deferred: %w", err)
	}

	return nil
}

// extractProjectFromPayload returns the project string from a sync payload struct.
// It handles both string and *string Project fields across all entity payload types.
// Returns empty string if the payload has no project or project is nil.
func extractProjectFromPayload(payload any) string {
	switch p := payload.(type) {
	case syncSessionPayload:
		return p.Project
	case syncObservationPayload:
		if p.Project != nil {
			return *p.Project
		}
		return ""
	case syncPromptPayload:
		if p.Project != nil {
			return *p.Project
		}
		return ""
	case syncRelationPayload:
		return p.Project
	default:
		// Fallback: marshal to JSON and extract $.project via json.Unmarshal.
		data, err := json.Marshal(payload)
		if err != nil {
			return ""
		}
		var generic struct {
			Project *string `json:"project"`
		}
		if err := json.Unmarshal(data, &generic); err != nil || generic.Project == nil {
			return ""
		}
		return *generic.Project
	}
}

func decodeSyncPayload(payload []byte, dest any) error {
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

func (s *Store) getObservationTx(tx *sql.Tx, id int64) (*Observation, error) {
	row := tx.QueryRow(
		`SELECT id, ifnull(sync_id, '') as sync_id, session_id, type, title, content, tool_name, project,
		        scope, topic_key, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at
		 FROM observations WHERE id = ? AND deleted_at IS NULL`, id,
	)
	var o Observation
	if err := row.Scan(&o.ID, &o.SyncID, &o.SessionID, &o.Type, &o.Title, &o.Content, &o.ToolName, &o.Project, &o.Scope, &o.TopicKey, &o.RevisionCount, &o.DuplicateCount, &o.LastSeenAt, &o.CreatedAt, &o.UpdatedAt, &o.DeletedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Store) getObservationBySyncIDTx(tx *sql.Tx, syncID string, includeDeleted bool) (*Observation, error) {
	query := `SELECT id, ifnull(sync_id, '') as sync_id, session_id, type, title, content, tool_name, project,
		        scope, topic_key, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at
		 FROM observations WHERE sync_id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	query += ` ORDER BY id DESC LIMIT 1`
	row := tx.QueryRow(query, syncID)
	var o Observation
	if err := row.Scan(&o.ID, &o.SyncID, &o.SessionID, &o.Type, &o.Title, &o.Content, &o.ToolName, &o.Project, &o.Scope, &o.TopicKey, &o.RevisionCount, &o.DuplicateCount, &o.LastSeenAt, &o.CreatedAt, &o.UpdatedAt, &o.DeletedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

func observationPayloadFromObservation(obs *Observation) syncObservationPayload {
	return syncObservationPayload{
		SyncID:         obs.SyncID,
		SessionID:      obs.SessionID,
		Type:           obs.Type,
		Title:          obs.Title,
		Content:        obs.Content,
		ToolName:       obs.ToolName,
		Project:        obs.Project,
		Scope:          obs.Scope,
		TopicKey:       obs.TopicKey,
		RevisionCount:  obs.RevisionCount,
		DuplicateCount: obs.DuplicateCount,
		LastSeenAt:     obs.LastSeenAt,
		CreatedAt:      obs.CreatedAt,
		UpdatedAt:      obs.UpdatedAt,
	}
}

func (s *Store) applySessionPayloadTx(tx *sql.Tx, payload syncSessionPayload) error {
	if isSessionDeletePayload(payload) {
		return s.applySessionDeleteTx(tx, payload)
	}
	_, err := s.execHook(tx,
		`INSERT INTO sessions (id, project, directory, started_at, ended_at, summary)
		 VALUES (?, ?, ?, COALESCE(NULLIF(?, ''), datetime('now')), ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   project = excluded.project,
		   directory = excluded.directory,
		   started_at = COALESCE(NULLIF(excluded.started_at, ''), sessions.started_at),
		   ended_at = COALESCE(excluded.ended_at, sessions.ended_at),
		   summary = COALESCE(excluded.summary, sessions.summary)`,
		payload.ID, payload.Project, payload.Directory, strings.TrimSpace(payload.StartedAt), payload.EndedAt, payload.Summary,
	)
	return err
}

func isSessionDeletePayload(payload syncSessionPayload) bool {
	if payload.Deleted || payload.HardDelete {
		return true
	}
	if payload.DeletedAt == nil {
		return false
	}
	return strings.TrimSpace(*payload.DeletedAt) != ""
}

func (s *Store) applySessionDeleteTx(tx *sql.Tx, payload syncSessionPayload) error {
	sessionID := strings.TrimSpace(payload.ID)
	if sessionID == "" {
		return nil
	}
	if _, err := s.execHook(tx, `DELETE FROM user_prompts WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	_, err := s.execHook(tx, `DELETE FROM sessions WHERE id = ?`, sessionID)
	return err
}

func (s *Store) applyObservationUpsertTx(tx *sql.Tx, payload syncObservationPayload) error {
	revisionCount := maxInt(payload.RevisionCount, 1)
	duplicateCount := maxInt(payload.DuplicateCount, 1)
	createdAt := strings.TrimSpace(payload.CreatedAt)
	updatedAt := strings.TrimSpace(payload.UpdatedAt)
	if createdAt == "" {
		createdAt = Now()
	}
	if updatedAt == "" {
		updatedAt = createdAt
	}

	existing, err := s.getObservationBySyncIDTx(tx, payload.SyncID, true)
	if err == sql.ErrNoRows {
		_, err = s.execHook(tx,
			`INSERT INTO observations (sync_id, session_id, type, title, content, tool_name, project, scope, topic_key, normalized_hash, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
			payload.SyncID,
			payload.SessionID,
			payload.Type,
			payload.Title,
			payload.Content,
			payload.ToolName,
			payload.Project,
			normalizeScope(payload.Scope),
			payload.TopicKey,
			hashNormalized(payload.Content),
			revisionCount,
			duplicateCount,
			payload.LastSeenAt,
			createdAt,
			updatedAt,
		)
		return err
	}
	if err != nil {
		return err
	}

	if payload.RevisionCount <= 0 {
		revisionCount = maxInt(existing.RevisionCount, 1)
	}
	if payload.DuplicateCount <= 0 {
		duplicateCount = maxInt(existing.DuplicateCount, 1)
	}
	if payload.LastSeenAt == nil {
		payload.LastSeenAt = existing.LastSeenAt
	}
	if strings.TrimSpace(payload.CreatedAt) == "" {
		createdAt = existing.CreatedAt
	}
	if strings.TrimSpace(payload.UpdatedAt) == "" {
		updatedAt = existing.UpdatedAt
	}

	_, err = s.execHook(tx,
		`UPDATE observations
		 SET session_id = ?, type = ?, title = ?, content = ?, tool_name = ?, project = ?, scope = ?, topic_key = ?, normalized_hash = ?, revision_count = ?, duplicate_count = ?, last_seen_at = ?, created_at = ?, updated_at = ?, deleted_at = NULL
		 WHERE id = ?`,
		payload.SessionID,
		payload.Type,
		payload.Title,
		payload.Content,
		payload.ToolName,
		payload.Project,
		normalizeScope(payload.Scope),
		payload.TopicKey,
		hashNormalized(payload.Content),
		revisionCount,
		duplicateCount,
		payload.LastSeenAt,
		createdAt,
		updatedAt,
		existing.ID,
	)
	return err
}

func (s *Store) applyObservationDeleteTx(tx *sql.Tx, payload syncObservationPayload) error {
	existing, err := s.getObservationBySyncIDTx(tx, payload.SyncID, true)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if payload.HardDelete {
		_, err = s.execHook(tx, `DELETE FROM observations WHERE id = ?`, existing.ID)
		return err
	}
	deletedAt := payload.DeletedAt
	if deletedAt == nil {
		now := Now()
		deletedAt = &now
	}
	_, err = s.execHook(tx,
		`UPDATE observations SET deleted_at = ?, updated_at = datetime('now') WHERE id = ?`,
		deletedAt, existing.ID,
	)
	return err
}

func (s *Store) applyPromptUpsertTx(tx *sql.Tx, payload syncPromptPayload) error {
	var tombstoneDeletedAt string
	err := tx.QueryRow(`SELECT deleted_at FROM prompt_tombstones WHERE sync_id = ?`, payload.SyncID).Scan(&tombstoneDeletedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if isStalePromptUpsert(payload, tombstoneDeletedAt) {
			return nil
		}
		if _, err := s.execHook(tx, `DELETE FROM prompt_tombstones WHERE sync_id = ?`, payload.SyncID); err != nil {
			return err
		}
	}

	var existingID int64
	err = tx.QueryRow(`SELECT id FROM user_prompts WHERE sync_id = ? ORDER BY id DESC LIMIT 1`, payload.SyncID).Scan(&existingID)
	if err == sql.ErrNoRows {
		if strings.TrimSpace(payload.CreatedAt) == "" {
			_, err = s.execHook(tx,
				`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, ?)`,
				payload.SyncID, payload.SessionID, payload.Content, payload.Project,
			)
		} else {
			_, err = s.execHook(tx,
				`INSERT INTO user_prompts (sync_id, session_id, content, project, created_at) VALUES (?, ?, ?, ?, ?)`,
				payload.SyncID, payload.SessionID, payload.Content, payload.Project, payload.CreatedAt,
			)
		}
		return err
	}
	if err != nil {
		return err
	}
	_, err = s.execHook(tx,
		`UPDATE user_prompts
		 SET session_id = ?,
		     content = ?,
		     project = ?,
		     created_at = CASE WHEN ? = '' THEN created_at ELSE ? END
		 WHERE id = ?`,
		payload.SessionID, payload.Content, payload.Project, strings.TrimSpace(payload.CreatedAt), payload.CreatedAt, existingID,
	)
	return err
}

func (s *Store) applyPromptDeleteTx(tx *sql.Tx, payload syncPromptPayload) error {
	if strings.TrimSpace(payload.SyncID) == "" {
		return nil
	}
	if _, err := s.execHook(tx, `DELETE FROM user_prompts WHERE sync_id = ?`, payload.SyncID); err != nil {
		return err
	}
	deletedAt := payload.DeletedAt
	if deletedAt == nil || strings.TrimSpace(*deletedAt) == "" {
		now := Now()
		deletedAt = &now
	}
	_, err := s.execHook(tx,
		`INSERT INTO prompt_tombstones (sync_id, session_id, project, deleted_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(sync_id) DO UPDATE SET session_id = excluded.session_id, project = excluded.project, deleted_at = excluded.deleted_at`,
		payload.SyncID, payload.SessionID, payload.Project, *deletedAt,
	)
	return err
}

func isStalePromptUpsert(payload syncPromptPayload, tombstoneDeletedAt string) bool {
	upsertTime := normalizeComparableTimestamp(payload.CreatedAt)
	if strings.TrimSpace(upsertTime) == "" {
		return true
	}
	return upsertTime <= normalizeComparableTimestamp(tombstoneDeletedAt)
}

func normalizeComparableTimestamp(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed.UTC().Format("2006-01-02 15:04:05")
	}
	return trimmed
}

func (s *Store) queryObservations(query string, args ...any) ([]Observation, error) {
	rows, err := s.queryItHook(s.db, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Observation
	for rows.Next() {
		var o Observation
		if err := rows.Scan(
			&o.ID, &o.SyncID, &o.SessionID, &o.Type, &o.Title, &o.Content,
			&o.ToolName, &o.Project, &o.Scope, &o.TopicKey, &o.RevisionCount, &o.DuplicateCount, &o.LastSeenAt,
			&o.CreatedAt, &o.UpdatedAt, &o.DeletedAt,
		); err != nil {
			return nil, err
		}
		results = append(results, o)
	}
	return results, rows.Err()
}

func (s *Store) addColumnIfNotExists(tableName, columnName, definition string) error {
	rows, err := s.queryItHook(s.db, fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return err
	}

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return closeRowsWithError(rows, err)
		}
		if name == columnName {
			rows.Close()
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, columnName, definition))
	return err
}

func (s *Store) migrateSyncChunksTable() error {
	rows, err := s.queryItHook(s.db, "PRAGMA table_info(sync_chunks)")
	if err != nil {
		return err
	}

	hasTargetKey := false
	targetKeyPK := 0
	chunkIDPK := 0
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return closeRowsWithError(rows, err)
		}
		switch name {
		case "target_key":
			hasTargetKey = true
			targetKeyPK = pk
		case "chunk_id":
			chunkIDPK = pk
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	// Already migrated: composite PK (target_key, chunk_id).
	if hasTargetKey && targetKeyPK == 1 && chunkIDPK == 2 {
		return nil
	}

	tx, err := s.beginTxHook()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := s.execHook(tx, `
		CREATE TABLE IF NOT EXISTS sync_chunks_new (
			target_key  TEXT NOT NULL DEFAULT 'local',
			chunk_id    TEXT NOT NULL,
			imported_at TEXT NOT NULL DEFAULT (datetime('now')),
			PRIMARY KEY (target_key, chunk_id)
		)
	`); err != nil {
		return err
	}

	if hasTargetKey {
		if _, err := s.execHook(tx, `
			INSERT OR IGNORE INTO sync_chunks_new (target_key, chunk_id, imported_at)
			SELECT CASE
				WHEN trim(ifnull(target_key, '')) = '' THEN ?
				ELSE trim(target_key)
			END,
			chunk_id,
			imported_at
			FROM sync_chunks
		`, LocalChunkTargetKey); err != nil {
			return err
		}
	} else {
		if _, err := s.execHook(tx, `
			INSERT OR IGNORE INTO sync_chunks_new (target_key, chunk_id, imported_at)
			SELECT ?, chunk_id, imported_at
			FROM sync_chunks
		`, LocalChunkTargetKey); err != nil {
			return err
		}
	}

	if _, err := s.execHook(tx, `DROP TABLE sync_chunks`); err != nil {
		return err
	}
	if _, err := s.execHook(tx, `ALTER TABLE sync_chunks_new RENAME TO sync_chunks`); err != nil {
		return err
	}

	return s.commitHook(tx)
}

func (s *Store) migrateLegacyObservationsTable() error {
	rows, err := s.queryItHook(s.db, "PRAGMA table_info(observations)")
	if err != nil {
		return err
	}

	var hasID bool
	var idIsPrimaryKey bool
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return closeRowsWithError(rows, err)
		}
		if name == "id" {
			hasID = true
			idIsPrimaryKey = pk == 1
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if !hasID || idIsPrimaryKey {
		return nil
	}

	tx, err := s.beginTxHook()
	if err != nil {
		return fmt.Errorf("migrate legacy observations: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := s.execHook(tx, `
		CREATE TABLE observations_migrated (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			sync_id    TEXT,
			session_id TEXT    NOT NULL,
			type       TEXT    NOT NULL,
			title      TEXT    NOT NULL,
			content    TEXT    NOT NULL,
			tool_name  TEXT,
			project    TEXT,
			scope      TEXT    NOT NULL DEFAULT 'project',
			topic_key  TEXT,
			normalized_hash TEXT,
			revision_count INTEGER NOT NULL DEFAULT 1,
			duplicate_count INTEGER NOT NULL DEFAULT 1,
			last_seen_at TEXT,
			created_at TEXT    NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT    NOT NULL DEFAULT (datetime('now')),
			deleted_at TEXT,
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);
	`); err != nil {
		return fmt.Errorf("migrate legacy observations: create table: %w", err)
	}

	if _, err := s.execHook(tx, `
		INSERT INTO observations_migrated (
			id, sync_id, session_id, type, title, content, tool_name, project,
			scope, topic_key, normalized_hash, revision_count, duplicate_count,
			last_seen_at, created_at, updated_at, deleted_at
		)
		SELECT
			CASE
				WHEN id IS NULL THEN NULL
				WHEN ROW_NUMBER() OVER (PARTITION BY id ORDER BY rowid) = 1 THEN CAST(id AS INTEGER)
				ELSE NULL
			END,
			'obs-' || lower(hex(randomblob(16))),
			session_id,
			COALESCE(NULLIF(type, ''), 'manual'),
			COALESCE(NULLIF(title, ''), 'Untitled observation'),
			COALESCE(content, ''),
			tool_name,
			project,
			CASE WHEN scope IS NULL OR scope = '' THEN 'project' ELSE scope END,
			NULLIF(topic_key, ''),
			normalized_hash,
			CASE WHEN revision_count IS NULL OR revision_count < 1 THEN 1 ELSE revision_count END,
			CASE WHEN duplicate_count IS NULL OR duplicate_count < 1 THEN 1 ELSE duplicate_count END,
			last_seen_at,
			COALESCE(NULLIF(created_at, ''), datetime('now')),
			COALESCE(NULLIF(updated_at, ''), NULLIF(created_at, ''), datetime('now')),
			deleted_at
		FROM observations
		ORDER BY rowid;
	`); err != nil {
		return fmt.Errorf("migrate legacy observations: copy rows: %w", err)
	}

	if _, err := s.execHook(tx, "DROP TABLE observations"); err != nil {
		return fmt.Errorf("migrate legacy observations: drop old table: %w", err)
	}

	if _, err := s.execHook(tx, "ALTER TABLE observations_migrated RENAME TO observations"); err != nil {
		return fmt.Errorf("migrate legacy observations: rename table: %w", err)
	}

	if _, err := s.execHook(tx, `
		DROP TRIGGER IF EXISTS obs_fts_insert;
		DROP TRIGGER IF EXISTS obs_fts_update;
		DROP TRIGGER IF EXISTS obs_fts_delete;
		DROP TABLE IF EXISTS observations_fts;
		CREATE VIRTUAL TABLE observations_fts USING fts5(
			title,
			content,
			tool_name,
			type,
			project,
			topic_key,
			content='observations',
			content_rowid='id'
		);
		INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
		SELECT id, title, content, tool_name, type, project, topic_key
		FROM observations
		WHERE deleted_at IS NULL;
	`); err != nil {
		return fmt.Errorf("migrate legacy observations: rebuild fts: %w", err)
	}

	if err := s.commitHook(tx); err != nil {
		return fmt.Errorf("migrate legacy observations: commit: %w", err)
	}

	return nil
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

func normalizeScope(scope string) string {
	v := strings.TrimSpace(strings.ToLower(scope))
	if v == "personal" {
		return "personal"
	}
	return "project"
}

// NormalizeProject applies canonical project name normalization:
// lowercase + trim whitespace + collapse consecutive hyphens/underscores.
// Returns the normalized name and a warning message if the name was changed
// (empty string if no change was needed).
// Exported so MCP and CLI handlers can surface the warning to users.
func NormalizeProject(project string) (normalized string, warning string) {
	if project == "" {
		return "", ""
	}
	n := strings.TrimSpace(strings.ToLower(project))
	// Collapse multiple consecutive hyphens
	for strings.Contains(n, "--") {
		n = strings.ReplaceAll(n, "--", "-")
	}
	// Collapse multiple consecutive underscores
	for strings.Contains(n, "__") {
		n = strings.ReplaceAll(n, "__", "_")
	}
	if n == project {
		return n, ""
	}
	return n, fmt.Sprintf("⚠️ Project name normalized: %q → %q", project, n)
}

// SuggestTopicKey generates a stable topic key suggestion from type/title/content.
// It infers a topic family (e.g. architecture/*, bug/*) and then appends
// a normalized segment from title/content for stable cross-session keys.
func SuggestTopicKey(typ, title, content string) string {
	family := inferTopicFamily(typ, title, content)
	cleanTitle := stripPrivateTags(title)
	segment := normalizeTopicSegment(cleanTitle)

	if segment == "" {
		cleanContent := stripPrivateTags(content)
		words := strings.Fields(strings.ToLower(cleanContent))
		if len(words) > 8 {
			words = words[:8]
		}
		segment = normalizeTopicSegment(strings.Join(words, " "))
	}

	if segment == "" {
		segment = "general"
	}

	if strings.HasPrefix(segment, family+"-") {
		segment = strings.TrimPrefix(segment, family+"-")
	}
	if segment == "" || segment == family {
		segment = "general"
	}

	return family + "/" + segment
}

func inferTopicFamily(typ, title, content string) string {
	t := strings.TrimSpace(strings.ToLower(typ))
	switch t {
	case "architecture", "design", "adr", "refactor":
		return "architecture"
	case "bug", "bugfix", "fix", "incident", "hotfix":
		return "bug"
	case "decision":
		return "decision"
	case "pattern", "convention", "guideline":
		return "pattern"
	case "config", "setup", "infra", "infrastructure", "ci":
		return "config"
	case "discovery", "investigation", "root_cause", "root-cause":
		return "discovery"
	case "learning", "learn":
		return "learning"
	case "session_summary":
		return "session"
	}

	text := strings.ToLower(title + " " + content)
	if hasAny(text, "bug", "fix", "panic", "error", "crash", "regression", "incident", "hotfix") {
		return "bug"
	}
	if hasAny(text, "architecture", "design", "adr", "boundary", "hexagonal", "refactor") {
		return "architecture"
	}
	if hasAny(text, "decision", "tradeoff", "chose", "choose", "decide") {
		return "decision"
	}
	if hasAny(text, "pattern", "convention", "naming", "guideline") {
		return "pattern"
	}
	if hasAny(text, "config", "setup", "environment", "env", "docker", "pipeline") {
		return "config"
	}
	if hasAny(text, "discovery", "investigate", "investigation", "found", "root cause") {
		return "discovery"
	}
	if hasAny(text, "learned", "learning") {
		return "learning"
	}

	if t != "" && t != "manual" {
		return normalizeTopicSegment(t)
	}

	return "topic"
}

func hasAny(text string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

func normalizeTopicSegment(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "" {
		return ""
	}
	re := regexp.MustCompile(`[^a-z0-9]+`)
	v = re.ReplaceAllString(v, " ")
	v = strings.Join(strings.Fields(v), "-")
	if len(v) > 100 {
		v = v[:100]
	}
	return v
}

func normalizeTopicKey(topic string) string {
	v := strings.TrimSpace(strings.ToLower(topic))
	if v == "" {
		return ""
	}
	v = strings.Join(strings.Fields(v), "-")
	if len(v) > 120 {
		v = v[:120]
	}
	return v
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func hashNormalized(content string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(content), " "))
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:])
}

func dedupeWindowExpression(window time.Duration) string {
	if window <= 0 {
		window = 15 * time.Minute
	}
	minutes := int(window.Minutes())
	if minutes < 1 {
		minutes = 1
	}
	return "-" + strconv.Itoa(minutes) + " minutes"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func normalizeSyncTargetKey(targetKey string) string {
	if strings.TrimSpace(targetKey) == "" {
		return DefaultSyncTargetKey
	}
	return strings.TrimSpace(strings.ToLower(targetKey))
}

func normalizeChunkTargetKey(targetKey string) string {
	if strings.TrimSpace(targetKey) == "" {
		return LocalChunkTargetKey
	}
	return strings.TrimSpace(strings.ToLower(targetKey))
}

func newSyncID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b)
}

func normalizeExistingSyncID(existing, prefix string) string {
	if strings.TrimSpace(existing) != "" {
		return existing
	}
	return newSyncID(prefix)
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

// ─── Passive Capture ─────────────────────────────────────────────────────────

// PassiveCaptureParams holds the input for passive memory capture.
type PassiveCaptureParams struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	Project   string `json:"project,omitempty"`
	Source    string `json:"source,omitempty"` // e.g. "subagent-stop", "session-end"
}

// PassiveCaptureResult holds the output of passive memory capture.
type PassiveCaptureResult struct {
	Extracted  int `json:"extracted"`  // Total learnings found in text
	Saved      int `json:"saved"`      // New observations created
	Duplicates int `json:"duplicates"` // Skipped because already existed
}

// learningHeaderPattern matches section headers for learnings in both English and Spanish.
var learningHeaderPattern = regexp.MustCompile(
	`(?im)^#{2,3}\s+(?:Aprendizajes(?:\s+Clave)?|Key\s+Learnings?|Learnings?):?\s*$`,
)

const (
	minLearningLength = 20
	minLearningWords  = 4
)

// ExtractLearnings parses structured learning items from text.
// It looks for sections like "## Key Learnings:" or "## Aprendizajes Clave:"
// and extracts numbered (1. text) or bullet (- text) items.
// Returns learnings from the LAST matching section (most recent output).
func ExtractLearnings(text string) []string {
	matches := learningHeaderPattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return nil
	}

	// Process sections in reverse — use first valid one (most recent)
	for i := len(matches) - 1; i >= 0; i-- {
		sectionStart := matches[i][1]
		sectionText := text[sectionStart:]

		// Cut off at next major section header
		if nextHeader := regexp.MustCompile(`\n#{1,3} `).FindStringIndex(sectionText); nextHeader != nil {
			sectionText = sectionText[:nextHeader[0]]
		}

		var learnings []string

		// Try numbered items: "1. text" or "1) text"
		numbered := regexp.MustCompile(`(?m)^\s*\d+[.)]\s+(.+)`).FindAllStringSubmatch(sectionText, -1)
		if len(numbered) > 0 {
			for _, m := range numbered {
				cleaned := cleanMarkdown(m[1])
				if len(cleaned) >= minLearningLength && len(strings.Fields(cleaned)) >= minLearningWords {
					learnings = append(learnings, cleaned)
				}
			}
		}

		// Fall back to bullet items: "- text" or "* text"
		if len(learnings) == 0 {
			bullets := regexp.MustCompile(`(?m)^\s*[-*]\s+(.+)`).FindAllStringSubmatch(sectionText, -1)
			for _, m := range bullets {
				cleaned := cleanMarkdown(m[1])
				if len(cleaned) >= minLearningLength && len(strings.Fields(cleaned)) >= minLearningWords {
					learnings = append(learnings, cleaned)
				}
			}
		}

		if len(learnings) > 0 {
			return learnings
		}
	}

	return nil
}

// cleanMarkdown strips basic markdown formatting and collapses whitespace.
func cleanMarkdown(text string) string {
	text = regexp.MustCompile(`\*\*([^*]+)\*\*`).ReplaceAllString(text, "$1") // bold
	text = regexp.MustCompile("`([^`]+)`").ReplaceAllString(text, "$1")       // inline code
	text = regexp.MustCompile(`\*([^*]+)\*`).ReplaceAllString(text, "$1")     // italic
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

// PassiveCapture extracts learnings from text and saves them as observations.
// It deduplicates against existing observations using content hash matching.
func (s *Store) PassiveCapture(p PassiveCaptureParams) (*PassiveCaptureResult, error) {
	// Normalize project name before storing
	p.Project, _ = NormalizeProject(p.Project)

	result := &PassiveCaptureResult{}

	learnings := ExtractLearnings(p.Content)
	result.Extracted = len(learnings)

	if len(learnings) == 0 {
		return result, nil
	}

	for _, learning := range learnings {
		// Check if this learning already exists (by content hash) within this project
		normHash := hashNormalized(learning)
		var existingID int64
		err := s.db.QueryRow(
			`SELECT id FROM observations
			 WHERE normalized_hash = ?
			   AND ifnull(project, '') = ifnull(?, '')
			   AND deleted_at IS NULL
			 LIMIT 1`,
			normHash, nullableString(p.Project),
		).Scan(&existingID)

		if err == nil {
			// Already exists — skip
			result.Duplicates++
			continue
		}

		// Truncate for title: first 60 chars
		title := learning
		if len(title) > 60 {
			title = title[:60] + "..."
		}

		_, err = s.AddObservation(AddObservationParams{
			SessionID: p.SessionID,
			Type:      "passive",
			Title:     title,
			Content:   learning,
			Project:   p.Project,
			Scope:     "project",
			ToolName:  p.Source,
		})
		if err != nil {
			return result, fmt.Errorf("passive capture save: %w", err)
		}
		result.Saved++
	}

	return result, nil
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

// ─── Test-accessor helpers (REQ-009 / Phase G integration tests) ──────────────

// CountRelationSyncMutations returns the number of sync_mutations rows whose
// entity is NOT 'session', 'observation', or 'prompt' — i.e., any entity that
// would indicate memory_relations data leaking into sync. Used by integration
// tests to assert that relation operations never enqueue sync mutations (REQ-009).
//
// In Phase 1, only 'session', 'observation', and 'prompt' are valid entities.
// Any other entity (e.g. 'relation', 'memory_relation') would be a regression.
func (s *Store) CountRelationSyncMutations() (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT count(*)
		FROM sync_mutations
		WHERE entity NOT IN ('session', 'observation', 'prompt')
	`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("CountRelationSyncMutations: %w", err)
	}
	return count, nil
}

// ─── Phase E: sync_apply_deferred helpers ────────────────────────────────────

// ReplayDeferredResult holds counts returned by ReplayDeferred.
type ReplayDeferredResult struct {
	Retried   int
	Succeeded int
	Failed    int
	Dead      int
}

// ReplayDeferred retries all rows in sync_apply_deferred with apply_status='deferred'
// (up to 50 per call, ordered by first_seen_at). For each row:
//   - Calls applyPulledMutationTx inside a transaction.
//   - On success: the apply itself deletes the deferred row (applyRelationUpsertTx
//     already includes DELETE FROM sync_apply_deferred on success path).
//   - On ErrRelationFKMissing: increments retry_count; if retry_count reaches 5,
//     marks apply_status='dead'. Otherwise updates last_error + last_attempted_at.
//   - On ErrApplyDead or other decode errors: marks apply_status='dead'.
//
// Dead rows are never retried. Idempotent: calling twice in one cycle does not
// double-retry because successful rows are deleted and failed rows update retry_count
// in place.
//
// Returns counts (retried, succeeded, failed, dead) for caller logging.
func (s *Store) ReplayDeferred() (result ReplayDeferredResult, err error) {
	const limit = 50
	const deadThreshold = 5

	rows, err := s.db.Query(`
		SELECT sync_id, entity, payload, retry_count
		FROM sync_apply_deferred
		WHERE apply_status = 'deferred'
		ORDER BY first_seen_at
		LIMIT ?
	`, limit)
	if err != nil {
		return result, fmt.Errorf("ReplayDeferred: list deferred: %w", err)
	}

	type deferredRow struct {
		syncID     string
		entity     string
		payload    string
		retryCount int
	}

	var pending []deferredRow
	for rows.Next() {
		var r deferredRow
		if err := rows.Scan(&r.syncID, &r.entity, &r.payload, &r.retryCount); err != nil {
			rows.Close()
			return result, fmt.Errorf("ReplayDeferred: scan: %w", err)
		}
		pending = append(pending, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("ReplayDeferred: rows error: %w", err)
	}

	for _, row := range pending {
		result.Retried++
		mut := SyncMutation{
			Entity:  row.entity,
			Op:      SyncOpUpsert,
			Payload: row.payload,
			Source:  SyncSourceRemote,
		}

		applyErr := s.withTx(func(tx *sql.Tx) error {
			return s.applyPulledMutationTx(tx, mut)
		})

		if applyErr == nil {
			// Success: applyRelationUpsertTx already deleted the deferred row.
			result.Succeeded++
			log.Printf("[store] replayDeferred: applied sync_id=%s", row.syncID)
			continue
		}

		// Classify the error and update the deferred row.
		newRetry := row.retryCount + 1
		var newStatus string
		if errors.Is(applyErr, ErrRelationFKMissing) && newRetry < deadThreshold {
			// Still retryable.
			newStatus = "deferred"
			result.Failed++
		} else {
			// Dead: either retry cap reached or non-retryable error.
			newStatus = "dead"
			result.Dead++
			log.Printf("[store] replayDeferred: marking dead sync_id=%s retry_count=%d err=%v",
				row.syncID, newRetry, applyErr)
		}

		if _, uErr := s.db.Exec(`
			UPDATE sync_apply_deferred
			SET retry_count = ?, apply_status = ?, last_error = ?, last_attempted_at = datetime('now')
			WHERE sync_id = ?
		`, newRetry, newStatus, applyErr.Error(), row.syncID); uErr != nil {
			log.Printf("[store] replayDeferred: update row sync_id=%s: %v", row.syncID, uErr)
		}
	}

	return result, nil
}

// CountDeferredAndDead returns the count of rows in sync_apply_deferred grouped
// by apply_status. Only 'deferred' and 'dead' statuses are counted; 'applied'
// rows (if any) are not included.
func (s *Store) CountDeferredAndDead() (deferred, dead int, err error) {
	rows, err := s.db.Query(`
		SELECT apply_status, count(*)
		FROM sync_apply_deferred
		WHERE apply_status IN ('deferred', 'dead')
		GROUP BY apply_status
	`)
	if err != nil {
		return 0, 0, fmt.Errorf("CountDeferredAndDead: query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return 0, 0, fmt.Errorf("CountDeferredAndDead: scan: %w", err)
		}
		switch status {
		case "deferred":
			deferred = n
		case "dead":
			dead = n
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("CountDeferredAndDead: rows error: %w", err)
	}
	return deferred, dead, nil
}

// ─── Phase 3: ListDeferred / GetDeferred ─────────────────────────────────────

// ListDeferred returns rows from sync_apply_deferred with optional status filter
// and pagination. The payload field is decoded to map[string]any; on malformed
// JSON, PayloadValid is false and PayloadRaw is preserved.
func (s *Store) ListDeferred(opts ListDeferredOptions) ([]DeferredRow, error) {
	query := `
		SELECT sync_id, entity, payload, apply_status, retry_count,
		       last_error, last_attempted_at, first_seen_at
		FROM sync_apply_deferred
		WHERE 1=1`
	var args []any

	if opts.Status != "" {
		query += ` AND apply_status = ?`
		args = append(args, opts.Status)
	}
	query += ` ORDER BY first_seen_at`
	if opts.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, opts.Limit)
	}
	if opts.Offset > 0 {
		query += ` OFFSET ?`
		args = append(args, opts.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("ListDeferred: query: %w", err)
	}
	defer rows.Close()

	var result []DeferredRow
	for rows.Next() {
		row, err := scanDeferredRow(rows)
		if err != nil {
			return nil, fmt.Errorf("ListDeferred: scan: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListDeferred: rows error: %w", err)
	}
	if result == nil {
		result = []DeferredRow{}
	}
	return result, nil
}

// GetDeferred returns a single row from sync_apply_deferred by sync_id.
// Returns an error wrapping "not found" when no row exists (matches FindCandidates style).
func (s *Store) GetDeferred(syncID string) (DeferredRow, error) {
	row := s.db.QueryRow(`
		SELECT sync_id, entity, payload, apply_status, retry_count,
		       last_error, last_attempted_at, first_seen_at
		FROM sync_apply_deferred
		WHERE sync_id = ?
	`, syncID)
	result, err := scanDeferredRow(row)
	if err == sql.ErrNoRows {
		return DeferredRow{}, fmt.Errorf("GetDeferred: deferred row %q not found", syncID)
	}
	if err != nil {
		return DeferredRow{}, fmt.Errorf("GetDeferred: %w", err)
	}
	return result, nil
}

// scannable is a common interface for *sql.Row and *sql.Rows.Scan.
type scannable interface {
	Scan(dest ...any) error
}

// scanDeferredRow scans a single sync_apply_deferred row into a DeferredRow.
// The payload is decoded to map[string]any; malformed JSON sets PayloadValid=false.
func scanDeferredRow(row scannable) (DeferredRow, error) {
	var r DeferredRow
	var rawPayload string
	if err := row.Scan(
		&r.SyncID, &r.Entity, &rawPayload, &r.ApplyStatus, &r.RetryCount,
		&r.LastError, &r.LastAttemptedAt, &r.FirstSeenAt,
	); err != nil {
		return r, err
	}
	r.PayloadRaw = rawPayload
	var decoded map[string]any
	if err := json.Unmarshal([]byte(rawPayload), &decoded); err == nil {
		r.Payload = decoded
		r.PayloadValid = true
	} else {
		r.PayloadValid = false
	}
	return r, nil
}

// ListObservationSyncPayloads returns the decoded payloads of all sync_mutations
// rows whose entity = 'observation'. Used by integration tests to assert that
// new observation columns (review_after, expires_at, embedding*) are NOT present
// in the sync wire format in Phase 1 (REQ-009).
func (s *Store) ListObservationSyncPayloads() ([]any, error) {
	rows, err := s.db.Query(`
		SELECT payload
		FROM sync_mutations
		WHERE entity = 'observation'
		ORDER BY seq ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("ListObservationSyncPayloads: query: %w", err)
	}
	defer rows.Close()

	var payloads []any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("ListObservationSyncPayloads: scan: %w", err)
		}
		var p syncObservationPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, fmt.Errorf("ListObservationSyncPayloads: unmarshal: %w", err)
		}
		payloads = append(payloads, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListObservationSyncPayloads: rows error: %w", err)
	}
	return payloads, nil
}
