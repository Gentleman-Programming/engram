// Package store implements the persistent memory engine for Engram.
//
// It uses SQLite with FTS5 full-text search to store and retrieve
// observations from AI coding sessions. This is the core of Engram —
// everything else (HTTP server, MCP server, CLI, plugins) talks to this.
package store

import (
	"context"
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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	projectpkg "github.com/Gentleman-Programming/engram/v2/internal/project"
	"github.com/Gentleman-Programming/engram/v2/internal/timeutil"
	sqlite "modernc.org/sqlite"
)

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

// Sentinel errors returned by Store operations so callers can use errors.Is.
var (
	ErrSessionNotFound             = errors.New("session not found")
	ErrSessionIDRequired           = errors.New("session id is required")
	ErrSessionHasObservations      = errors.New("session still has observations")
	ErrSessionDeleteBlocked        = errors.New("session deletion is blocked while cloud sync enrollment is active")
	ErrObservationNotFound         = errors.New("observation not found")
	ErrPromptNotFound              = errors.New("prompt not found")
	ErrProjectNotFound             = errors.New("project not found")
	ErrProjectRequired             = errors.New("project identity is required")
	ErrProjectRescueInvalidRequest = errors.New("project rescue request is invalid")
	// ErrProjectOwnershipAmbiguous is returned when an unowned session cannot
	// adopt a write's project because it already parents records owned by a
	// different one. Guessing there would split a record from its session.
	ErrProjectOwnershipAmbiguous   = errors.New("session project ownership is ambiguous")
	ErrObservationProjectImmutable = errors.New("observation project cannot be reassigned")
	ErrObservationTitleRequired    = errors.New("observation title is required")
	ErrObservationContentRequired  = errors.New("observation content is required")
	ErrPromptContentRequired       = errors.New("prompt content is required")
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

	// ErrPulledSessionIdentityInvalid identifies an invalid identity after successful decoding and legacy fallback.
	ErrPulledSessionIdentityInvalid = errors.New("pulled session identity is invalid")
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
	ReviewAfter    *string `json:"review_after,omitempty"`
	Pinned         bool    `json:"-"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	DeletedAt      *string `json:"deleted_at,omitempty"`
}

const (
	ObservationStateActive      = "active"
	ObservationStateNeedsReview = "needs_review"
)

// State returns the virtual lifecycle state derived from review_after.
func (o Observation) State() string {
	if o.ReviewAfter == nil || strings.TrimSpace(*o.ReviewAfter) == "" {
		return ObservationStateActive
	}
	reviewAfter, err := parseObservationTime(*o.ReviewAfter)
	if err != nil {
		return ObservationStateActive
	}
	if !reviewAfter.After(time.Now().UTC()) {
		return ObservationStateNeedsReview
	}
	return ObservationStateActive
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
	Type      string `json:"type,omitempty"`
	Project   string `json:"project,omitempty"`
	Scope     string `json:"scope,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	MatchMode string `json:"match_mode,omitempty"` // "all" (default) | "any"
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

// TruncationMetadata describes storage content processing after private-tag redaction.
type TruncationMetadata struct {
	OriginalBytes int  `json:"original_bytes"`
	LimitBytes    int  `json:"limit_bytes"`
	Truncated     bool `json:"truncated"`
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

	SyncSessionIdentityInvalidReasonCode = "sync_session_identity_invalid"

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

const observationSelectColumns = `id, ifnull(sync_id, '') as sync_id, session_id, type, title, content, tool_name, project,
	       scope, topic_key, revision_count, duplicate_count, last_seen_at, review_after, pinned, created_at, updated_at, deleted_at`

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
	LastSuccessAt       *string `json:"last_success_at,omitempty"`
	UpdatedAt           string  `json:"updated_at"`
}

// CloudSyncSummary is the aggregate local cloud state shown when no project is selected.
type CloudSyncSummary struct {
	LastSuccessAt    string
	PendingMutations int64
	LastError        string
}

type SyncMutation struct {
	Seq                 int64   `json:"seq"`
	TargetKey           string  `json:"target_key"`
	Entity              string  `json:"entity"`
	EntityKey           string  `json:"entity_key"`
	Op                  string  `json:"op"`
	Payload             string  `json:"payload"`
	Source              string  `json:"source"`
	Project             string  `json:"project"`
	OccurredAt          string  `json:"occurred_at"`
	AckedAt             *string `json:"acked_at,omitempty"`
	Disposition         string  `json:"disposition"`
	DispositionReason   string  `json:"disposition_reason,omitempty"`
	DispositionEvidence string  `json:"disposition_evidence,omitempty"`
	DispositionAt       *string `json:"disposition_at,omitempty"`
}

const (
	SyncMutationDispositionPending     = "pending"
	SyncMutationDispositionQuarantined = "quarantined"
)

// SyncMutationQuarantineAction records one deterministic local quarantine.
type SyncMutationQuarantineAction struct {
	Seq        int64  `json:"seq"`
	Project    string `json:"project"`
	Entity     string `json:"entity"`
	EntityKey  string `json:"entity_key"`
	Op         string `json:"op"`
	ReasonCode string `json:"reason_code"`
	Message    string `json:"message"`
	Evidence   string `json:"evidence"`
}

// SyncMutationQuarantineReport is the explicit local recovery result.
type SyncMutationQuarantineReport struct {
	Project string                         `json:"project,omitempty"`
	Applied bool                           `json:"applied"`
	Actions []SyncMutationQuarantineAction `json:"actions"`
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
	Captured        bool `json:"captured"`
	ProjectEnrolled bool `json:"project_enrolled"`
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

// DataDir returns the directory containing the store database and cloud.json.
func (s *Store) DataDir() string {
	return s.cfg.DataDir
}

// ─── Store ───────────────────────────────────────────────────────────────────

type Store struct {
	db    *sql.DB
	cfg   Config
	hooks storeHooks

	repairMu        sync.Mutex
	repairDone      bool
	repairInFlight  *enrolledProjectRepair
	repairOperation func() error // test seam; production uses repairEnrolledProjectSyncMutations.
}

type enrolledProjectRepair struct {
	done chan struct{}
	err  error
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
	exec           func(db execer, query string, args ...any) (sql.Result, error)
	query          func(db queryer, query string, args ...any) (*sql.Rows, error)
	queryIt        func(db queryer, query string, args ...any) (rowScanner, error)
	queryItContext func(ctx context.Context, db *sql.DB, query string, args ...any) (rowScanner, error)
	beginTx        func(db *sql.DB) (*sql.Tx, error)
	commit         func(tx *sql.Tx) error
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
		queryItContext: func(ctx context.Context, db *sql.DB, query string, args ...any) (rowScanner, error) {
			rows, err := db.QueryContext(ctx, query, args...)
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

// DB returns the underlying *sql.DB. Intended for test helpers and integration
// tests that need to inject raw rows (e.g. legacy data with non-normalized
// project names) without going through the Store's public API.
func (s *Store) DB() *sql.DB { return s.db }

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

func (s *Store) queryItContextHook(ctx context.Context, query string, args ...any) (rowScanner, error) {
	if s.hooks.queryItContext != nil {
		return s.hooks.queryItContext(ctx, s.db, query, args...)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
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
	if err := ensureDatabaseFile(dbPath); err != nil {
		return nil, fmt.Errorf("engram: create database file: %w", err)
	}
	generation, err := newDatabaseGeneration(dbPath)
	if err != nil {
		return nil, fmt.Errorf("engram: capture database generation: %w", err)
	}
	db, err := openDB(dbPath, generation)
	if err != nil {
		return nil, fmt.Errorf("engram: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	succeeded := false
	defer func() {
		if !succeeded {
			_ = db.Close()
		}
	}()

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

	succeeded = true
	return s, nil
}

// newWithoutRepair is retained as a test helper alias for New. Enrolled-project
// repair is deferred until the first synchronization operation in both cases.
func newWithoutRepair(cfg Config) (*Store, error) {
	if !filepath.IsAbs(cfg.DataDir) {
		return nil, fmt.Errorf("engram: data directory must be an absolute path, got %q — set ENGRAM_DATA_DIR or ensure your home directory is resolvable", cfg.DataDir)
	}
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("engram: create data dir: %w", err)
	}

	dbPath := filepath.Join(cfg.DataDir, "engram.db")
	if err := ensureDatabaseFile(dbPath); err != nil {
		return nil, fmt.Errorf("engram: create database file: %w", err)
	}
	generation, err := newDatabaseGeneration(dbPath)
	if err != nil {
		return nil, fmt.Errorf("engram: capture database generation: %w", err)
	}
	db, err := openDB(dbPath, generation)
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
			_ = db.Close()
			return nil, fmt.Errorf("engram: pragma %q: %w", p, err)
		}
	}

	s := &Store{db: db, cfg: cfg, hooks: defaultStoreHooks()}
	if err := s.migrate(); err != nil {
		_ = db.Close()
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
			pinned     BOOLEAN NOT NULL DEFAULT 0,
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
			tokenize='trigram',
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
			tokenize='trigram',
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
				last_success_at      TEXT,
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
				disposition TEXT NOT NULL DEFAULT 'pending',
				disposition_reason TEXT,
				disposition_evidence TEXT,
				disposition_at TEXT,
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
	if err := s.redactCloudUpgradeSnapshots(); err != nil {
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
		{name: "pinned", definition: "BOOLEAN NOT NULL DEFAULT 0"},
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
	if err := s.migrateFTS(); err != nil {
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
	for _, c := range []struct{ name, definition string }{
		{"disposition", "TEXT NOT NULL DEFAULT 'pending'"},
		{"disposition_reason", "TEXT"},
		{"disposition_evidence", "TEXT"},
		{"disposition_at", "TEXT"},
	} {
		if err := s.addColumnIfNotExists("sync_mutations", c.name, c.definition); err != nil {
			return err
		}
	}
	if _, err := s.execHook(s.db, `UPDATE sync_mutations SET disposition = 'pending' WHERE disposition IS NULL OR disposition = ''`); err != nil {
		return err
	}
	if err := s.addColumnIfNotExists("sync_state", "reason_code", "TEXT"); err != nil {
		return err
	}
	if err := s.addColumnIfNotExists("sync_state", "reason_message", "TEXT"); err != nil {
		return err
	}
	if err := s.addColumnIfNotExists("sync_state", "last_success_at", "TEXT"); err != nil {
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
			CREATE INDEX IF NOT EXISTS idx_sync_mutations_transport ON sync_mutations(target_key, disposition, acked_at, seq);
	`); err != nil {
		return err
	}

	if err := s.withTx(func(tx *sql.Tx) error {
		_, err := s.execHook(tx, `UPDATE observations SET project = CAST(project AS TEXT) WHERE typeof(project) = 'blob'`)
		return err
	}); err != nil {
		return err
	}

	// Phase 3: add republish CLI, surface dead rows via mem_status.
	if _, err := s.execHook(s.db, `
		CREATE TABLE IF NOT EXISTS sync_apply_deferred (
			sync_id           TEXT    PRIMARY KEY,
			entity            TEXT    NOT NULL,
			payload           TEXT    NOT NULL,
			target_key        TEXT    NOT NULL DEFAULT '',
			remote_seq        INTEGER NOT NULL DEFAULT 0,
			entity_key        TEXT    NOT NULL DEFAULT '',
			op                TEXT    NOT NULL DEFAULT '',
			reason_code       TEXT    NOT NULL DEFAULT '',
			project           TEXT    NOT NULL DEFAULT '',
			scope_class       TEXT    NOT NULL DEFAULT 'legacy_unscoped',
			apply_status      TEXT    NOT NULL DEFAULT 'deferred',
			retry_count       INTEGER NOT NULL DEFAULT 0,
			payload_sync_id   TEXT    NOT NULL DEFAULT '',
			last_error        TEXT,
			last_attempted_at TEXT,
			first_seen_at     TEXT    NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_sad_status_seen
			ON sync_apply_deferred(apply_status, first_seen_at);
	`); err != nil {
		return err
	}
	deferredColumns := []struct {
		name       string
		definition string
	}{
		{name: "target_key", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "remote_seq", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "entity_key", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "op", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "reason_code", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "project", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "scope_class", definition: "TEXT NOT NULL DEFAULT 'legacy_unscoped'"},
		// payload_sync_id records the entity identity the row's own payload
		// claims, which is not always the key the row is stored under. The
		// success-path cleanup needs that identity, and reading it from a stored
		// column keeps a JSON extract out of the apply write transaction. It stays
		// blank for payloads that carry no sync_id of their own — pulled session
		// payloads are keyed on `id`, and an undecodable payload has no identity
		// at all, which is itself one of the reasons its row is dead.
		{name: "payload_sync_id", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, c := range deferredColumns {
		if err := s.addColumnIfNotExists("sync_apply_deferred", c.name, c.definition); err != nil {
			return err
		}
	}
	if _, err := s.execHook(s.db, `
		CREATE INDEX IF NOT EXISTS idx_sad_scope_status_seen
			ON sync_apply_deferred(target_key, project, apply_status, first_seen_at);
		CREATE INDEX IF NOT EXISTS idx_sad_payload_sync
			ON sync_apply_deferred(payload_sync_id, entity);
	`); err != nil {
		return err
	}
	// Rows written before payload_sync_id existed carry their relation identity
	// only inside the payload, so derive it here rather than parsing JSON on every
	// apply.
	//
	// This runs on every open rather than only in the open that added the column.
	// The ALTER above and this UPDATE are separate auto-committed statements, so a
	// transient SQLITE_BUSY, an I/O error, or the process dying between them leaves
	// a database whose column exists while every legacy row is still blank. An open
	// that derived only what it had just added would skip such a database forever,
	// and a schema-version marker advanced past this point would skip it too. It is
	// also what repairs rows an older binary writes into the same database once the
	// column exists. That matters beyond migration hygiene: a blank identity is
	// what lets relationApplyCleanupSQL delete a row by the key it is stored under,
	// so a blank that means "not yet derived" is a route to deleting evidence about
	// a different relation.
	//
	// Running it unconditionally is cheap because it converges. The two equality
	// terms are the exact leading prefix of idx_sad_payload_sync(payload_sync_id,
	// entity), so this is a point lookup rather than a scan of the backlog, and the
	// CASE excludes every row whose identity cannot be derived — after one
	// successful run no row matches at all. CASE is used instead of a
	// `json_valid(...) AND json_extract(...)` conjunction because only CASE
	// guarantees the extract is never evaluated for a payload that is not valid
	// JSON. The value is trimmed so a derived identity is byte-identical to the one
	// recordRelationApplyFailureTx stores for the same payload.
	if _, err := s.execHook(s.db, `
		UPDATE sync_apply_deferred
		SET payload_sync_id = trim(json_extract(payload, '$.sync_id'))
		WHERE payload_sync_id = ''
		  AND entity = 'relation'
		  AND CASE
			WHEN json_valid(payload) THEN trim(ifnull(json_extract(payload, '$.sync_id'), ''))
			ELSE ''
		  END <> ''
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

func (s *Store) redactCloudUpgradeSnapshots() error {
	_, err := s.execHook(s.db, `
		UPDATE cloud_upgrade_state
		SET snapshot_json = CASE json_valid(snapshot_json)
			WHEN 1 THEN CASE
				WHEN json_type(snapshot_json, '$.cloud_config_present') IS NOT NULL
					OR json_type(snapshot_json, '$.cloud_config_json') IS NOT NULL
					THEN CASE
						WHEN stage IN ('planned', 'doctor_ready', 'doctor_blocked', 'repair_applied', 'bootstrap_enrolled', 'bootstrap_pushed')
							AND (
								json_extract(snapshot_json, '$.cloud_config_present') = 1
								OR ifnull(json_extract(snapshot_json, '$.cloud_config_json'), '') != ''
								OR json_extract(snapshot_json, '$.project_enrolled') = 1
							)
							THEN '{"captured":true,"project_enrolled":' ||
								CASE json_extract(snapshot_json, '$.project_enrolled') WHEN 1 THEN 'true' ELSE 'false' END || '}'
						ELSE '{"captured":false,"project_enrolled":false}'
					END
				ELSE '{"captured":' ||
					CASE json_extract(snapshot_json, '$.captured') WHEN 1 THEN 'true' ELSE 'false' END ||
					',"project_enrolled":' ||
					CASE json_extract(snapshot_json, '$.project_enrolled') WHEN 1 THEN 'true' ELSE 'false' END || '}'
			END
			ELSE '{"captured":false,"project_enrolled":false}'
		END
	`)
	return err
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
			snapshot_json = CASE
				WHEN json_extract(excluded.snapshot_json, '$.captured') = 0
					AND json_extract(cloud_upgrade_state.snapshot_json, '$.captured') = 1
					THEN cloud_upgrade_state.snapshot_json
				ELSE excluded.snapshot_json
			END,
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
	return state.Snapshot.Captured && state.Stage != UpgradeStageBootstrapVerified, nil
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
	if !state.Snapshot.Captured {
		return CloudUpgradeState{}, fmt.Errorf("rollback requires a captured pre-bootstrap snapshot")
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
	// When both repairable and blocked mutations coexist we must not hold the
	// entire repair pass hostage to the non-repairable entries. Apply the
	// repairable subset first, then surface the residual blockers.
	appliedRepairs := false
	if apply && legacyReport.RepairableCount > 0 {
		if err := s.applyCloudUpgradeLegacyMutationRepairs(project); err != nil {
			return CloudUpgradeRepairReport{}, fmt.Errorf("apply cloud upgrade legacy mutation repairs: %w", err)
		}
		appliedRepairs = true
	}

	if legacyReport.BlockedCount > 0 {
		// Scan for the first non-repairable finding so the operator sees the
		// actual blocker. Findings[0] is ordered by seq and may be a repairable
		// entry, which previously produced a misleading error message (#446).
		var blocked CloudUpgradeLegacyMutationFinding
		for _, f := range legacyReport.Findings {
			if !f.Repairable {
				blocked = f
				break
			}
		}
		var msg string
		switch {
		case appliedRepairs:
			msg = fmt.Sprintf("applied %d repairable payload(s); %d remain blocked: manual-action-required: %s (seq=%d entity=%s entity_key=%q op=%s)",
				legacyReport.RepairableCount, legacyReport.BlockedCount, blocked.Message, blocked.Seq, blocked.Entity, blocked.EntityKey, blocked.Op)
		case legacyReport.RepairableCount > 0:
			msg = fmt.Sprintf("%d repairable payload(s) would apply; %d would remain blocked: manual-action-required: %s (seq=%d entity=%s entity_key=%q op=%s)",
				legacyReport.RepairableCount, legacyReport.BlockedCount, blocked.Message, blocked.Seq, blocked.Entity, blocked.EntityKey, blocked.Op)
		default:
			msg = fmt.Sprintf("manual-action-required: %s (seq=%d entity=%s entity_key=%q op=%s)",
				blocked.Message, blocked.Seq, blocked.Entity, blocked.EntityKey, blocked.Op)
		}
		return CloudUpgradeRepairReport{
			Class:      UpgradeRepairClassBlocked,
			ReasonCode: UpgradeReasonBlockedLegacyMutationManual,
			Message:    msg,
			Applied:    appliedRepairs,
		}, nil
	}

	if legacyReport.RepairableCount > 0 {
		if !appliedRepairs {
			return CloudUpgradeRepairReport{
				Class:         UpgradeRepairClassRepairable,
				ReasonCode:    UpgradeReasonRepairableLegacyMutationPayload,
				Message:       fmt.Sprintf("project %q has %d repairable legacy mutation payload issue(s)", project, legacyReport.RepairableCount),
				PlannedAction: "repair_legacy_mutation_payloads",
				Applied:       false,
			}, nil
		}
		_ = s.SaveCloudUpgradeState(CloudUpgradeState{
			Project:     project,
			Stage:       UpgradeStageRepairApplied,
			RepairClass: UpgradeRepairClassRepairable,
		})
		return CloudUpgradeRepairReport{
			Class:         UpgradeRepairClassRepairable,
			ReasonCode:    UpgradeReasonRepairableLegacyMutationPayload,
			Message:       fmt.Sprintf("applied deterministic legacy mutation payload repairs for project %q", project),
			PlannedAction: "repair_legacy_mutation_payloads",
			Applied:       true,
		}, nil
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

// listPendingProjectMutationsTx returns the transportable pending journal rows a
// cloud upgrade still has to account for. Quarantined rows are excluded: they are
// already dispositioned local evidence, so counting them would keep the upgrade
// blocked forever with no remaining action an operator could take.
func (s *Store) listPendingProjectMutationsTx(tx *sql.Tx, project string) ([]SyncMutation, error) {
	rows, err := s.queryItHook(tx, `
		SELECT seq, target_key, entity, entity_key, op, payload, source, project, occurred_at, acked_at
		FROM sync_mutations
		WHERE target_key = ? AND project = ? AND acked_at IS NULL AND disposition = ?
		ORDER BY seq ASC
	`, DefaultSyncTargetKey, project, SyncMutationDispositionPending)
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
	if base.Project == "" || base.Project != mutation.Project {
		return blocked(UpgradeReasonBlockedLegacyMutationManual, "mutation project must be non-empty and canonical for cloud transport and cannot be inferred from authoritative local state"), nil
	}

	if payload == "" {
		return blocked(UpgradeReasonBlockedLegacyMutationManual, "legacy mutation payload is empty"), nil
	}

	supported := (entity == SyncEntitySession && (op == SyncOpUpsert || op == SyncOpDelete)) ||
		((entity == SyncEntityObservation || entity == SyncEntityPrompt) && (op == SyncOpUpsert || op == SyncOpDelete)) ||
		(entity == SyncEntityRelation && op == SyncOpUpsert)
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

	case SyncEntityRelation:
		var body syncRelationPayload
		if err := decodeSyncPayload([]byte(payload), &body); err != nil {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("decode relation payload: %v", err)), nil
		}
		body.SyncID = strings.TrimSpace(body.SyncID)
		body.SourceID = strings.TrimSpace(body.SourceID)
		body.TargetID = strings.TrimSpace(body.TargetID)
		body.Relation = strings.TrimSpace(body.Relation)
		body.JudgmentStatus = strings.TrimSpace(body.JudgmentStatus)
		body.Project = strings.TrimSpace(body.Project)
		changed := false
		if body.SyncID == "" && strings.TrimSpace(mutation.EntityKey) != "" {
			body.SyncID = strings.TrimSpace(mutation.EntityKey)
			changed = true
		}
		if body.SyncID == "" {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, "relation payload sync_id is required"), nil
		}
		if strings.TrimSpace(mutation.EntityKey) != "" && strings.TrimSpace(mutation.EntityKey) != body.SyncID {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("relation entity_key %q does not match payload sync_id %q", mutation.EntityKey, body.SyncID)), nil
		}

		type localRelationPayload struct {
			SyncID         string
			SourceID       string
			TargetID       string
			Relation       string
			Reason         sql.NullString
			Evidence       sql.NullString
			Confidence     sql.NullFloat64
			JudgmentStatus string
			MarkedByActor  sql.NullString
			MarkedByKind   sql.NullString
			MarkedByModel  sql.NullString
			SessionID      sql.NullString
			CreatedAt      string
			UpdatedAt      string
		}
		var local localRelationPayload
		err := tx.QueryRow(`
			SELECT ifnull(sync_id, ''), ifnull(source_id, ''), ifnull(target_id, ''), ifnull(relation, ''),
			       reason, evidence, confidence, ifnull(judgment_status, ''), marked_by_actor,
			       marked_by_kind, marked_by_model, session_id, ifnull(created_at, ''), ifnull(updated_at, '')
			FROM memory_relations
			WHERE sync_id = ?
			ORDER BY id DESC LIMIT 1
		`, body.SyncID).Scan(
			&local.SyncID, &local.SourceID, &local.TargetID, &local.Relation,
			&local.Reason, &local.Evidence, &local.Confidence, &local.JudgmentStatus,
			&local.MarkedByActor, &local.MarkedByKind, &local.MarkedByModel, &local.SessionID,
			&local.CreatedAt, &local.UpdatedAt,
		)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return cloudUpgradeLegacyMutationEvaluation{}, err
		}
		if err == nil {
			if body.SourceID == "" && strings.TrimSpace(local.SourceID) != "" {
				body.SourceID = strings.TrimSpace(local.SourceID)
				changed = true
			}
			if body.TargetID == "" && strings.TrimSpace(local.TargetID) != "" {
				body.TargetID = strings.TrimSpace(local.TargetID)
				changed = true
			}
			if body.Relation == "" && strings.TrimSpace(local.Relation) != "" {
				body.Relation = strings.TrimSpace(local.Relation)
				changed = true
			}
			if body.Reason == nil && local.Reason.Valid {
				v := local.Reason.String
				body.Reason = &v
				changed = true
			}
			if body.Evidence == nil && local.Evidence.Valid {
				v := local.Evidence.String
				body.Evidence = &v
				changed = true
			}
			if body.Confidence == nil && local.Confidence.Valid {
				v := local.Confidence.Float64
				body.Confidence = &v
				changed = true
			}
			if body.JudgmentStatus == "" && strings.TrimSpace(local.JudgmentStatus) != "" {
				body.JudgmentStatus = strings.TrimSpace(local.JudgmentStatus)
				changed = true
			}
			if (body.MarkedByActor == nil || strings.TrimSpace(*body.MarkedByActor) == "") && local.MarkedByActor.Valid && strings.TrimSpace(local.MarkedByActor.String) != "" {
				v := strings.TrimSpace(local.MarkedByActor.String)
				body.MarkedByActor = &v
				changed = true
			}
			if (body.MarkedByKind == nil || strings.TrimSpace(*body.MarkedByKind) == "") && local.MarkedByKind.Valid && strings.TrimSpace(local.MarkedByKind.String) != "" {
				v := strings.TrimSpace(local.MarkedByKind.String)
				body.MarkedByKind = &v
				changed = true
			}
			if body.MarkedByModel == nil && local.MarkedByModel.Valid {
				v := local.MarkedByModel.String
				body.MarkedByModel = &v
				changed = true
			}
			if body.SessionID == nil && local.SessionID.Valid {
				v := local.SessionID.String
				body.SessionID = &v
				changed = true
			}
			if strings.TrimSpace(body.CreatedAt) == "" && strings.TrimSpace(local.CreatedAt) != "" {
				body.CreatedAt = strings.TrimSpace(local.CreatedAt)
				changed = true
			}
			if strings.TrimSpace(body.UpdatedAt) == "" && strings.TrimSpace(local.UpdatedAt) != "" {
				body.UpdatedAt = strings.TrimSpace(local.UpdatedAt)
				changed = true
			}
		}
		if body.Project == "" && strings.TrimSpace(mutation.Project) != "" {
			body.Project = strings.TrimSpace(mutation.Project)
			changed = true
		}
		missing := []string{}
		if body.SourceID == "" {
			missing = append(missing, "source_id")
		}
		if body.TargetID == "" {
			missing = append(missing, "target_id")
		}
		if body.Relation == "" {
			missing = append(missing, "relation")
		}
		if body.JudgmentStatus == "" {
			missing = append(missing, "judgment_status")
		}
		if body.MarkedByActor == nil || strings.TrimSpace(*body.MarkedByActor) == "" {
			missing = append(missing, "marked_by_actor")
		}
		if body.MarkedByKind == nil || strings.TrimSpace(*body.MarkedByKind) == "" {
			missing = append(missing, "marked_by_kind")
		}
		if body.Project == "" {
			missing = append(missing, "project")
		}
		if len(missing) > 0 {
			return blocked(UpgradeReasonBlockedLegacyMutationManual, fmt.Sprintf("relation payload missing required upsert fields: %s", strings.Join(missing, ", "))), nil
		}
		if !changed {
			return cloudUpgradeLegacyMutationEvaluation{}, nil
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return cloudUpgradeLegacyMutationEvaluation{}, err
		}
		return repairable("relation payload is missing required fields for canonical bootstrap", "repair fills missing relation fields from local memory_relations table and mutation project", string(encoded)), nil
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

const observationsFTSDefinition = `
	CREATE VIRTUAL TABLE observations_fts USING fts5(
		title,
		content,
		tool_name,
		type,
		project,
		topic_key,
		tokenize='trigram',
		content='observations',
		content_rowid='id'
	);
`

const promptsFTSDefinition = `
	CREATE VIRTUAL TABLE prompts_fts USING fts5(
		content,
		project,
		tokenize='trigram',
		content='user_prompts',
		content_rowid='id'
	);
`

const observationsFTSTriggers = `
	CREATE TRIGGER obs_fts_insert AFTER INSERT ON observations
	WHEN new.deleted_at IS NULL BEGIN
		INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
		VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project, new.topic_key);
	END;

	CREATE TRIGGER obs_fts_delete BEFORE DELETE ON observations
	WHEN old.deleted_at IS NULL BEGIN
		DELETE FROM observations_fts WHERE rowid = old.id;
	END;

	CREATE TRIGGER obs_fts_update BEFORE UPDATE ON observations
	WHEN old.deleted_at IS NULL BEGIN
		DELETE FROM observations_fts WHERE rowid = old.id;
	END;

	CREATE TRIGGER obs_fts_update_insert AFTER UPDATE ON observations
	WHEN new.deleted_at IS NULL BEGIN
		INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
		VALUES (new.id, new.title, new.content, new.tool_name, new.type, new.project, new.topic_key);
	END;
`

const promptsFTSTriggers = `
	CREATE TRIGGER prompt_fts_insert AFTER INSERT ON user_prompts BEGIN
		INSERT INTO prompts_fts(rowid, content, project)
		VALUES (new.id, new.content, new.project);
	END;

	CREATE TRIGGER prompt_fts_delete BEFORE DELETE ON user_prompts BEGIN
		DELETE FROM prompts_fts WHERE rowid = old.id;
	END;

	CREATE TRIGGER prompt_fts_update BEFORE UPDATE ON user_prompts BEGIN
		DELETE FROM prompts_fts WHERE rowid = old.id;
	END;

	CREATE TRIGGER prompt_fts_update_insert AFTER UPDATE ON user_prompts BEGIN
		INSERT INTO prompts_fts(rowid, content, project)
		VALUES (new.id, new.content, new.project);
	END;
`

// migrateFTS upgrades older FTS tables and trigger definitions together. The
// external-content rows must be removed before their source rows change; using
// ordinary DELETE statements in BEFORE triggers avoids the trigram tokenizer's
// unsafe special FTS5 'delete' command on recent SQLite versions.
func (s *Store) migrateFTS() error {
	return s.withTx(func(tx *sql.Tx) error {
		observationsOK, err := ftsTableMatches(tx, "observations_fts", []string{"title", "content", "tool_name", "type", "project", "topic_key"}, "observations")
		if err != nil {
			return fmt.Errorf("inspect observations fts: %w", err)
		}
		promptsOK, err := ftsTableMatches(tx, "prompts_fts", []string{"content", "project"}, "user_prompts")
		if err != nil {
			return fmt.Errorf("inspect prompts fts: %w", err)
		}

		observationsRebuild := !observationsOK || !ftsTriggersMatch(tx, map[string][]string{
			"obs_fts_insert":        {"afterinsertonobservations", "whennew.deleted_atisnull"},
			"obs_fts_delete":        {"beforedeleteonobservations", "whenold.deleted_atisnull", "deletefromobservations_ftswhererowid=old.id"},
			"obs_fts_update":        {"beforeupdateonobservations", "whenold.deleted_atisnull", "deletefromobservations_ftswhererowid=old.id"},
			"obs_fts_update_insert": {"afterupdateonobservations", "whennew.deleted_atisnull"},
		})
		promptsRebuild := !promptsOK || !ftsTriggersMatch(tx, map[string][]string{
			"prompt_fts_insert":        {"afterinsertonuser_prompts"},
			"prompt_fts_delete":        {"beforedeleteonuser_prompts", "deletefromprompts_ftswhererowid=old.id"},
			"prompt_fts_update":        {"beforeupdateonuser_prompts", "deletefromprompts_ftswhererowid=old.id"},
			"prompt_fts_update_insert": {"afterupdateonuser_prompts"},
		})

		if observationsRebuild {
			if _, err := s.execHook(tx, `
				DROP TRIGGER IF EXISTS obs_fts_insert;
				DROP TRIGGER IF EXISTS obs_fts_update_insert;
				DROP TRIGGER IF EXISTS obs_fts_update;
				DROP TRIGGER IF EXISTS obs_fts_delete;
				DROP TABLE IF EXISTS observations_fts;
			`+observationsFTSDefinition+observationsFTSTriggers+`
				INSERT INTO observations_fts(rowid, title, content, tool_name, type, project, topic_key)
				SELECT id, title, content, tool_name, type, project, topic_key
				FROM observations
				WHERE deleted_at IS NULL;
			`); err != nil {
				return fmt.Errorf("rebuild observations fts: %w", err)
			}
		}

		if promptsRebuild {
			if _, err := s.execHook(tx, `
				DROP TRIGGER IF EXISTS prompt_fts_insert;
				DROP TRIGGER IF EXISTS prompt_fts_update_insert;
				DROP TRIGGER IF EXISTS prompt_fts_update;
				DROP TRIGGER IF EXISTS prompt_fts_delete;
				DROP TABLE IF EXISTS prompts_fts;
			`+promptsFTSDefinition+promptsFTSTriggers+`
				INSERT INTO prompts_fts(rowid, content, project)
				SELECT id, content, project FROM user_prompts;
			`); err != nil {
				return fmt.Errorf("rebuild prompts fts: %w", err)
			}
		}
		return nil
	})
}

func ftsTableMatches(tx *sql.Tx, table string, expectedColumns []string, contentTable string) (bool, error) {
	var ddl string
	err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	rows, err := tx.Query(`SELECT name FROM pragma_table_xinfo(?) WHERE hidden = 0 ORDER BY cid`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if !slices.Equal(columns, expectedColumns) {
		return false, nil
	}

	normalized := normalizeFTSSQL(ddl)
	return strings.Contains(normalized, "createvirtualtable"+table+"usingfts5(") &&
		strings.Contains(normalized, "tokenize='trigram'") &&
		strings.Contains(normalized, "content='"+contentTable+"'") &&
		strings.Contains(normalized, "content_rowid='id'"), nil
}

func ftsTriggersMatch(tx *sql.Tx, expected map[string][]string) bool {
	for name, fragments := range expected {
		var ddl string
		if err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&ddl); err != nil {
			return false
		}
		normalized := normalizeFTSSQL(ddl)
		for _, fragment := range fragments {
			if !strings.Contains(normalized, fragment) {
				return false
			}
		}
	}
	return true
}

func normalizeFTSSQL(ddl string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, ddl)
}

// ─── Sessions ────────────────────────────────────────────────────────────────

func (s *Store) CreateSession(id, project, directory string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	// Normalize project name before storing
	project, _ = NormalizeProject(project)
	if strings.TrimSpace(project) == "" {
		return ErrProjectRequired
	}

	return s.withTx(func(tx *sql.Tx) error {
		if err := s.createSessionTx(tx, id, project, directory); err != nil {
			return err
		}
		var persisted Session
		// sessions.project is read through ifnull() because a database upgraded from
		// the schema where the column was nullable still carries rows that identify no
		// project, and no migration rewrites them.
		if err := tx.QueryRow(`SELECT id, ifnull(project, ''), directory, started_at, ended_at, summary FROM sessions WHERE id = ?`, id).Scan(
			&persisted.ID, &persisted.Project, &persisted.Directory, &persisted.StartedAt, &persisted.EndedAt, &persisted.Summary,
		); err != nil {
			return err
		}
		return s.enqueueSyncMutationTx(tx, SyncEntitySession, persisted.ID, SyncOpUpsert, syncSessionPayload{
			ID:        persisted.ID,
			Project:   persisted.Project,
			Directory: persisted.Directory,
			StartedAt: persisted.StartedAt,
			EndedAt:   persisted.EndedAt,
			Summary:   persisted.Summary,
		})
	})
}

func (s *Store) EndSession(id string, summary string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
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
		// sessions.project is read through ifnull() because a database upgraded from
		// the schema where the column was nullable still carries rows that identify no
		// project, and no migration rewrites them.
		if err := tx.QueryRow(
			`SELECT ifnull(project, ''), directory, started_at, ended_at, summary FROM sessions WHERE id = ?`,
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
	// A database upgraded from the schema where sessions.project was nullable
	// still carries NULL ownership, so the column must be read as nullable or
	// every caller that inspects a legacy session dies on an opaque scan error.
	var project sql.NullString
	if err := row.Scan(&sess.ID, &project, &sess.Directory, &sess.StartedAt, &sess.EndedAt, &sess.Summary); err != nil {
		return nil, err
	}
	sess.Project = project.String
	return &sess, nil
}

// MostRecentActiveSession resolves the active (un-ended) session for a project
// from the persisted sessions table. It returns the session ID and ok=true when
// such a session exists, or ok=false when none does.
//
// This is the cross-process resolution that fixes issue #386: the SessionStart
// hook registers a UUID session via the HTTP server (POST /sessions) in one
// process, while mem_save runs in the separate MCP (stdio) process. The two
// share only the SQLite store, so the active session must be read from disk —
// never from in-memory state.
//
// Candidate rules:
//   - Scope to the (normalized) project.
//   - Scope to the current runtime directory.
//   - Require ended_at IS NULL — ended sessions are never returned, so stale
//     sessions naturally fall out without any explicit clearing step.
//   - Exclude the manual-save fallback sessions (id LIKE 'manual-save%'); those
//     are created by the fallback path itself and must not be resolved as "the
//     active session", which would make resolution circular.
//
// ActiveRuntimeSessions returns active, non-manual sessions for a project and
// runtime directories. The directories narrow candidates; callers must not
// treat them as session identity.
func (s *Store) ActiveRuntimeSessions(project string, directories ...string) ([]string, error) {
	project, _ = NormalizeProject(project)
	if project == "" {
		return nil, nil
	}
	directorySet := make(map[string]struct{}, len(directories))
	for _, directory := range directories {
		if directory != "" {
			directorySet[directory] = struct{}{}
		}
	}
	if len(directorySet) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(directorySet)+1)
	args = append(args, project)
	placeholders := make([]string, 0, len(directorySet))
	for directory := range directorySet {
		placeholders = append(placeholders, "?")
		args = append(args, directory)
	}

	rows, err := s.queryHook(s.db, `
		SELECT DISTINCT id
		FROM sessions
		WHERE LOWER(project) = ?
		  AND directory IN (`+strings.Join(placeholders, ", ")+`)
		  AND ended_at IS NULL
		  AND id NOT LIKE 'manual-save%'
		ORDER BY id
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// A database upgraded from the schema where sessions.project was nullable still
// carries rows that identify no project, so the column is read through ifnull():
// an unscoped listing reads every session row and must not die on one of them.
func (s *Store) RecentSessions(project string, limit int) ([]SessionSummary, error) {
	// Normalize project filter for case-insensitive matching
	project, _ = NormalizeProject(project)

	if limit <= 0 {
		limit = 5
	}

	query := `
		SELECT s.id, ifnull(s.project, ''), s.started_at, s.ended_at, s.summary,
		       COUNT(o.id) as observation_count
		FROM sessions s
		LEFT JOIN observations o ON o.session_id = s.id AND o.deleted_at IS NULL
		WHERE 1=1
	`
	args := []any{}

	if project != "" {
		query += " AND LOWER(s.project) = ?"
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
// A database upgraded from the schema where sessions.project was nullable still
// carries rows that identify no project, so the column is read through ifnull():
// an unscoped listing reads every session row and must not die on one of them.
func (s *Store) AllSessions(project string, limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT s.id, ifnull(s.project, ''), s.started_at, s.ended_at, s.summary,
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
		SELECT ` + observationSelectColumns + `
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
		SELECT ` + observationSelectColumns + `
		FROM observations
		WHERE session_id = ? AND deleted_at IS NULL
		ORDER BY created_at ASC
		LIMIT ?
	`
	return s.queryObservations(query, sessionID, limit)
}

// ─── Observations ────────────────────────────────────────────────────────────

// ValidateObservationTitle is the one definition of "an observation has a
// usable title", shared by every write path so the CLI, MCP and HTTP entry
// points can reject a titleless write before they create a session or open a
// transaction. It mirrors what ValidateSyncMutationPayload requires of an
// observation upsert: cloud sync rejects a payload whose title is empty, and
// because the mutation queue is an ordered log, one rejected row blocks every
// later mutation for the same project.
func ValidateObservationTitle(title string) error {
	if strings.TrimSpace(title) == "" {
		return ErrObservationTitleRequired
	}
	return nil
}

func (s *Store) AddObservation(p AddObservationParams) (int64, error) {
	// Normalize project name (lowercase + trim) before any persistence
	p.Project, _ = NormalizeProject(p.Project)

	// Strip <private>...</private> tags before persisting ANYTHING.
	title := stripPrivateTags(p.Title)
	content, _ := s.prepareStoredContent(p.Content)

	// The title guard runs on the post-strip title so redaction cannot turn a
	// valid title into an empty one behind our back. Persisting a titleless
	// observation also enqueues a cloud upsert that the sync validators reject,
	// which blocks every later mutation for the project (#459).
	if err := ValidateObservationTitle(title); err != nil {
		return 0, err
	}
	if content == "" {
		return 0, ErrObservationContentRequired
	}

	scope := normalizeScope(p.Scope)
	normHash := hashNormalized(content)
	topicKey := normalizeTopicKey(p.TopicKey)

	var observationID int64
	err := s.withTx(func(tx *sql.Tx) error {
		{
			// Settle ownership first: an unowned legacy session adopts this
			// write's project rather than rejecting the write forever.
			resolved, err := s.resolveWriteProjectTx(tx, p.SessionID, p.Project)
			if err != nil {
				return err
			}
			p.Project = resolved
		}
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
					 SET session_id = ?,
					     type = ?,
					     title = ?,
					     content = ?,
					     tool_name = ?,
					     topic_key = ?,
					     normalized_hash = ?,
					     revision_count = revision_count + 1,
					     last_seen_at = datetime('now'),
					     updated_at = datetime('now')
					 WHERE id = ?`,
					p.SessionID,
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
			 VALUES (?, ?, ?, ?, ?, ?, CAST(? AS TEXT), ?, ?, ?, 1, 1, datetime('now'), datetime('now'))`,
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
		SELECT ` + observationSelectColumns + `
		FROM observations o
		WHERE o.deleted_at IS NULL
	`
	args := []any{}

	if project != "" {
		query += " AND LOWER(o.project) = ?"
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

func (s *Store) PinnedObservations(project, scope string) ([]Observation, error) {
	project, _ = NormalizeProject(project)

	query := `
		SELECT ` + observationSelectColumns + `
		FROM observations o
		WHERE o.deleted_at IS NULL AND o.pinned = 1
	`
	args := []any{}

	if project != "" {
		query += " AND LOWER(o.project) = ?"
		args = append(args, project)
	}
	if scope != "" {
		query += " AND o.scope = ?"
		args = append(args, normalizeScope(scope))
	}

	query += " ORDER BY datetime(o.created_at) DESC, o.id DESC"
	return s.queryObservations(query, args...)
}

func (s *Store) PinObservation(id int64) error {
	return s.setObservationPinned(id, true)
}

func (s *Store) UnpinObservation(id int64) error {
	return s.setObservationPinned(id, false)
}

func (s *Store) setObservationPinned(id int64, pinned bool) error {
	value := 0
	if pinned {
		value = 1
	}
	res, err := s.execHook(s.db, `UPDATE observations SET pinned = ? WHERE id = ? AND deleted_at IS NULL`, value, id)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrObservationNotFound
	}
	return nil
}

func (s *Store) recentUnpinnedObservations(project, scope string, limit int) ([]Observation, error) {
	project, _ = NormalizeProject(project)
	if limit <= 0 {
		limit = s.cfg.MaxContextResults
	}

	query := `
		SELECT ` + observationSelectColumns + `
		FROM observations o
		WHERE o.deleted_at IS NULL AND o.pinned = 0
	`
	args := []any{}
	if project != "" {
		query += " AND LOWER(o.project) = ?"
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

func (s *Store) compactionObservations(sessionID, project string, pinned bool, limit int) ([]Observation, error) {
	pinnedValue := 0
	if pinned {
		pinnedValue = 1
	}
	query := `
		SELECT ` + observationSelectColumns + `
		FROM observations o
		WHERE o.deleted_at IS NULL AND o.session_id = ? AND LOWER(o.project) = ? AND o.pinned = ?
		ORDER BY datetime(o.created_at) DESC, o.id DESC`
	args := []any{sessionID, project, pinnedValue}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	return s.queryObservations(query, args...)
}

// ObservationsNeedingReview returns non-deleted observations whose review_after has passed.
// An empty project searches all projects, matching existing browse/search conventions.
func (s *Store) ObservationsNeedingReview(project string, limit int) ([]Observation, error) {
	project, _ = NormalizeProject(project)
	if limit <= 0 {
		limit = s.cfg.MaxContextResults
	}
	query := `
		SELECT ` + observationSelectColumns + `
		FROM observations o
		WHERE o.deleted_at IS NULL
		  AND o.review_after IS NOT NULL
		  AND datetime(o.review_after) <= datetime('now')
	`
	args := []any{}
	if project != "" {
		query += " AND LOWER(o.project) = ?"
		args = append(args, project)
	}
	query += " ORDER BY datetime(o.review_after) ASC, o.id ASC LIMIT ?"
	args = append(args, limit)

	return s.queryObservations(query, args...)
}

// MarkReviewed resets an observation's review_after using its type's configured decay offset.
// Types without a decay offset return to a NULL review_after value.
// This lifecycle reset is intentionally local-only until the sync wire format includes review_after.
func (s *Store) MarkReviewed(id int64) error {
	return s.markReviewed(id, "")
}

// MarkReviewedForProject resets an observation's review lifecycle only when it
// remains owned by project at the mutation boundary.
func (s *Store) MarkReviewedForProject(id int64, project string) error {
	project, _ = NormalizeProject(project)
	return s.markReviewed(id, project)
}

func (s *Store) markReviewed(id int64, project string) error {
	return s.withTx(func(tx *sql.Tx) error {
		query := `SELECT type FROM observations WHERE id = ? AND deleted_at IS NULL`
		args := []any{id}
		if project != "" {
			query += ` AND LOWER(project) = ?`
			args = append(args, project)
		}
		var observationType string
		err := tx.QueryRow(query, args...).Scan(&observationType)
		if err == sql.ErrNoRows {
			return ErrObservationNotFound
		}
		if err != nil {
			return err
		}

		var reviewAfter any
		if months, ok := decayReviewAfterMonths[observationType]; ok {
			reviewAfter = time.Now().UTC().AddDate(0, months, 0).Format("2006-01-02 15:04:05")
		}
		update := `UPDATE observations SET review_after = ?, updated_at = datetime('now') WHERE id = ? AND deleted_at IS NULL`
		updateArgs := []any{reviewAfter, id}
		if project != "" {
			update += ` AND LOWER(project) = ?`
			updateArgs = append(updateArgs, project)
		}
		result, err := s.execHook(tx, update, updateArgs...)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrObservationNotFound
		}
		return nil
	})
}

// ─── User Prompts ────────────────────────────────────────────────────────────

func (s *Store) AddPrompt(p AddPromptParams) (int64, error) {
	// Normalize project name before storing
	p.Project, _ = NormalizeProject(p.Project)

	content, _ := s.prepareStoredContent(p.Content)
	if content == "" {
		return 0, ErrPromptContentRequired
	}

	var promptID int64
	err := s.withTx(func(tx *sql.Tx) error {
		{
			// Settle ownership first: an unowned legacy session adopts this
			// write's project rather than rejecting the write forever.
			resolved, err := s.resolveWriteProjectTx(tx, p.SessionID, p.Project)
			if err != nil {
				return err
			}
			p.Project = resolved
		}
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

func (s *Store) AddPromptIfMissing(p AddPromptParams) (int64, bool, error) {
	p.Project, _ = NormalizeProject(p.Project)
	content, _ := s.prepareStoredContent(p.Content)
	if content == "" {
		return 0, false, ErrPromptContentRequired
	}

	var promptID int64
	inserted := false
	err := s.withTx(func(tx *sql.Tx) error {
		{
			// Settle ownership first: an unowned legacy session adopts this
			// write's project rather than rejecting the write forever.
			resolved, err := s.resolveWriteProjectTx(tx, p.SessionID, p.Project)
			if err != nil {
				return err
			}
			p.Project = resolved
		}
		err := tx.QueryRow(
			`SELECT id FROM user_prompts WHERE session_id = ? AND ifnull(project, '') = ? AND content = ? ORDER BY id DESC LIMIT 1`,
			p.SessionID, p.Project, content,
		).Scan(&promptID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

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
		inserted = true
		return s.enqueueSyncMutationTx(tx, SyncEntityPrompt, syncID, SyncOpUpsert, syncPromptPayload{
			SyncID:    syncID,
			SessionID: p.SessionID,
			Content:   content,
			Project:   nullableString(p.Project),
			CreatedAt: createdAt,
		})
	})
	if err != nil {
		return 0, false, err
	}
	return promptID, inserted, nil
}

// ContentTruncation returns the byte-based truncation metadata used by storage writes.
func (s *Store) ContentTruncation(content string) TruncationMetadata {
	_, metadata := s.prepareStoredContent(content)
	return metadata
}

func (s *Store) prepareStoredContent(content string) (string, TruncationMetadata) {
	content = stripPrivateTags(content)
	metadata := TruncationMetadata{
		OriginalBytes: len(content),
		LimitBytes:    s.cfg.MaxObservationLength,
		Truncated:     len(content) > s.cfg.MaxObservationLength,
	}
	return truncateContent(content, s.cfg.MaxObservationLength), metadata
}

func truncateContent(content string, max int) string {
	if len(content) <= max {
		return content
	}

	end := max
	for end > 0 && !utf8.RuneStart(content[end]) {
		end--
	}
	return content[:end] + "... [truncated]"
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

func (s *Store) compactionPrompts(sessionID, project string, limit int) ([]Prompt, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := s.queryItHook(s.db, `
		SELECT id, ifnull(sync_id, '') as sync_id, session_id, content, ifnull(project, '') as project, created_at
		FROM user_prompts
		WHERE session_id = ? AND LOWER(ifnull(project, '')) = ?
		ORDER BY created_at DESC
		LIMIT ?`, sessionID, project, limit)
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

	var sql string
	var args []any
	if hasShortFTSTerm(query) {
		sql, args = buildPromptLIKEQuery(query, project, limit)
	} else {
		ftsQuery := sanitizeFTS(query)
		sql = `
			SELECT p.id, ifnull(p.sync_id, '') as sync_id, p.session_id, p.content, ifnull(p.project, '') as project, p.created_at
			FROM prompts_fts fts
			JOIN user_prompts p ON p.id = fts.rowid
			WHERE prompts_fts MATCH ?
		`
		args = []any{ftsQuery}

		if project != "" {
			sql += " AND p.project = ?"
			args = append(args, project)
		}

		sql += " ORDER BY fts.rank LIMIT ?"
		args = append(args, limit)
	}

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
	if err := validateSessionID(id); err != nil {
		return err
	}
	return s.withTx(func(tx *sql.Tx) error {
		var project string
		// sessions.project is read through ifnull() because a database upgraded from
		// the schema where the column was nullable still carries rows that identify no
		// project, and no migration rewrites them.
		if err := tx.QueryRow(`SELECT ifnull(project, '') FROM sessions WHERE id = ?`, id).Scan(&project); err != nil {
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
		`SELECT `+observationSelectColumns+`
		 FROM observations WHERE id = ? AND deleted_at IS NULL`, id,
	)
	var o Observation
	if err := scanObservationRow(row, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Store) UpdateObservation(id int64, p UpdateObservationParams) (*Observation, error) {
	// Admission runs before the transaction so a rejected update opens no
	// transaction, touches no row and enqueues no sync mutation. The title is
	// checked post-strip so redaction cannot smuggle an empty one through.
	if p.Title != nil {
		if err := ValidateObservationTitle(stripPrivateTags(*p.Title)); err != nil {
			return nil, err
		}
	}
	if p.Content != nil && stripPrivateTags(*p.Content) == "" {
		return nil, ErrObservationContentRequired
	}

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
		if strings.TrimSpace(project) == "" {
			return ErrProjectRequired
		}
		scope := obs.Scope
		topicKey := derefString(obs.TopicKey)

		if p.Type != nil {
			typ = *p.Type
		}
		if p.Title != nil {
			title = stripPrivateTags(*p.Title)
		}
		if p.Content != nil {
			content, _ = s.prepareStoredContent(*p.Content)
		}
		if p.Project != nil {
			requestedProject, _ := NormalizeProject(*p.Project)
			if requestedProject != project {
				return ErrObservationProjectImmutable
			}
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
			     project = CAST(? AS TEXT),
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

// Search preserves the original non-context API for callers that do not need
// cancellation.
func (s *Store) Search(query string, opts SearchOptions) ([]SearchResult, error) {
	return s.SearchContext(context.Background(), query, opts)
}

// SearchContext searches observations while honoring cancellation from the
// caller, including while materializing rows.
func (s *Store) SearchContext(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Validate match_mode early so invalid values always error regardless of query shape.
	switch opts.MatchMode {
	case "", "all", "any":
		// valid
	default:
		return nil, fmt.Errorf("invalid match_mode %q: must be \"all\" or \"any\"", opts.MatchMode)
	}

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
			SELECT ` + observationSelectColumns + `
			FROM observations
			WHERE topic_key = ? AND deleted_at IS NULL
		`
		tkArgs := []any{query}

		if opts.Type != "" {
			tkSQL += " AND type = ?"
			tkArgs = append(tkArgs, opts.Type)
		}
		if opts.Project != "" {
			tkSQL += " AND LOWER(project) = ?"
			tkArgs = append(tkArgs, opts.Project)
		}
		if opts.Scope != "" {
			tkSQL += " AND scope = ?"
			tkArgs = append(tkArgs, normalizeScope(opts.Scope))
		}

		tkSQL += " ORDER BY updated_at DESC LIMIT ?"
		tkArgs = append(tkArgs, limit)

		tkRows, err := s.db.QueryContext(ctx, tkSQL, tkArgs...)
		if err == nil {
			defer tkRows.Close()
			for tkRows.Next() {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				var sr SearchResult
				if err := tkRows.Scan(
					&sr.ID, &sr.SyncID, &sr.SessionID, &sr.Type, &sr.Title, &sr.Content,
					&sr.ToolName, &sr.Project, &sr.Scope, &sr.TopicKey, &sr.RevisionCount, &sr.DuplicateCount,
					&sr.LastSeenAt, &sr.ReviewAfter, &sr.Pinned, &sr.CreatedAt, &sr.UpdatedAt, &sr.DeletedAt,
				); err != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return nil, ctxErr
					}
					break
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				sr.Rank = -1000
				directResults = append(directResults, sr)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		} else if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
	}

	var sqlQ string
	var args []any
	if hasShortFTSTerm(query) {
		sqlQ, args = buildSearchLIKEQuery(query, opts, limit)
	} else {
		// Build FTS5 query: "all" (default) uses AND semantics; "any" uses OR for broader recall.
		var ftsQuery string
		if opts.MatchMode == "any" {
			ftsQuery = sanitizeFTSCandidates(query)
		} else {
			ftsQuery = sanitizeFTS(query)
		}
		sqlQ, args = buildSearchFTSQuery(ftsQuery, opts, limit)
	}
	rows, err := s.queryItContextHook(ctx, sqlQ, args...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var sr SearchResult
		if err := rows.Scan(
			&sr.ID, &sr.SyncID, &sr.SessionID, &sr.Type, &sr.Title, &sr.Content,
			&sr.ToolName, &sr.Project, &sr.Scope, &sr.TopicKey, &sr.RevisionCount, &sr.DuplicateCount,
			&sr.LastSeenAt, &sr.ReviewAfter, &sr.Pinned, &sr.CreatedAt, &sr.UpdatedAt, &sr.DeletedAt,
			&sr.Rank,
		); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !seen[sr.ID] {
			results = append(results, sr)
		}
	}
	if err := rows.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func buildSearchFTSQuery(ftsQuery string, opts SearchOptions, limit int) (string, []any) {
	sqlQ := `
		SELECT o.id, ifnull(o.sync_id, '') as sync_id, o.session_id, o.type, o.title, o.content, o.tool_name, o.project,
		       o.scope, o.topic_key, o.revision_count, o.duplicate_count, o.last_seen_at, o.review_after, o.pinned, o.created_at, o.updated_at, o.deleted_at,
		       bm25(observations_fts, 5.0, 1.0, 0.0, 0.0, 0.0, 3.0) as rank
		FROM observations_fts fts
		CROSS JOIN observations o ON o.id = fts.rowid
		WHERE observations_fts MATCH ? AND o.deleted_at IS NULL
	`
	args := []any{ftsQuery}

	if opts.Type != "" {
		sqlQ += " AND o.type = ?"
		args = append(args, opts.Type)
	}
	if opts.Project != "" {
		sqlQ += " AND LOWER(o.project) = ?"
		args = append(args, opts.Project)
	}
	if opts.Scope != "" {
		sqlQ += " AND o.scope = ?"
		args = append(args, normalizeScope(opts.Scope))
	}

	sqlQ += " ORDER BY rank LIMIT ?"
	return sqlQ, append(args, limit)
}

// hasShortFTSTerm identifies the only queries trigram FTS5 cannot satisfy.
// Those terms use a bounded, escaped base-table fallback instead of changing
// normal FTS ranking or treating every search as a LIKE scan.
func hasShortFTSTerm(query string) bool {
	for _, term := range searchTerms(query) {
		if utf8.RuneCountInString(term) < 3 {
			return true
		}
	}
	return false
}

func searchTerms(query string) []string {
	var terms []string
	for _, term := range strings.Fields(query) {
		term = strings.Trim(term, `"`)
		if term != "" {
			terms = append(terms, term)
		}
	}
	return terms
}

func escapeLIKE(term string) string {
	term = strings.ReplaceAll(term, `\`, `\\`)
	term = strings.ReplaceAll(term, `%`, `\%`)
	return strings.ReplaceAll(term, `_`, `\_`)
}

func buildSearchLIKEQuery(query string, opts SearchOptions, limit int) (string, []any) {
	sqlQ := `
		SELECT o.id, ifnull(o.sync_id, '') as sync_id, o.session_id, o.type, o.title, o.content, o.tool_name, o.project,
		       o.scope, o.topic_key, o.revision_count, o.duplicate_count, o.last_seen_at, o.review_after, o.pinned, o.created_at, o.updated_at, o.deleted_at,
		       0.0 AS rank
		FROM observations o
		WHERE o.deleted_at IS NULL
	`
	args := []any{}
	termGroups := make([]string, 0)
	for _, term := range searchTerms(query) {
		termGroups = append(termGroups, `(o.title LIKE ? ESCAPE '\' OR o.content LIKE ? ESCAPE '\' OR o.tool_name LIKE ? ESCAPE '\' OR o.type LIKE ? ESCAPE '\' OR o.project LIKE ? ESCAPE '\' OR o.topic_key LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeLIKE(term) + "%"
		for range 6 {
			args = append(args, pattern)
		}
	}
	joiner := " AND "
	if opts.MatchMode == "any" {
		joiner = " OR "
	}
	sqlQ += " AND (" + strings.Join(termGroups, joiner) + ")"
	if opts.Type != "" {
		sqlQ += " AND o.type = ?"
		args = append(args, opts.Type)
	}
	if opts.Project != "" {
		sqlQ += " AND LOWER(o.project) = ?"
		args = append(args, opts.Project)
	}
	if opts.Scope != "" {
		sqlQ += " AND o.scope = ?"
		args = append(args, normalizeScope(opts.Scope))
	}
	sqlQ += " ORDER BY datetime(o.updated_at) DESC, o.id DESC LIMIT ?"
	return sqlQ, append(args, limit)
}

func buildPromptLIKEQuery(query, project string, limit int) (string, []any) {
	sqlQ := `
		SELECT p.id, ifnull(p.sync_id, '') as sync_id, p.session_id, p.content, ifnull(p.project, '') as project, p.created_at
		FROM user_prompts p
		WHERE `
	args := []any{}
	termGroups := make([]string, 0)
	for _, term := range searchTerms(query) {
		termGroups = append(termGroups, `(p.content LIKE ? ESCAPE '\' OR p.project LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeLIKE(term) + "%"
		args = append(args, pattern, pattern)
	}
	sqlQ += "(" + strings.Join(termGroups, " AND ") + ")"
	if project != "" {
		sqlQ += " AND p.project = ?"
		args = append(args, project)
	}
	sqlQ += " ORDER BY p.created_at DESC, p.id DESC LIMIT ?"
	return sqlQ, append(args, limit)
}

// ─── Stats ───────────────────────────────────────────────────────────────────

func (s *Store) Stats() (*Stats, error) {
	return s.statsForProject("")
}

// StatsProject returns aggregate counts restricted to one project. Selection
// policy belongs to callers; this method only applies the supplied scope.
func (s *Store) StatsProject(project string) (*Stats, error) {
	project, _ = NormalizeProject(project)
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("project is required")
	}
	return s.statsForProject(project)
}

func (s *Store) statsForProject(project string) (*Stats, error) {
	stats := &Stats{}
	args := []any{}
	where := ""
	if project != "" {
		where = " WHERE LOWER(project) = ?"
		args = append(args, project)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sessions"+where, args...).Scan(&stats.TotalSessions); errors.Is(err, ErrDatabaseGenerationChanged) {
		return nil, err
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM observations WHERE deleted_at IS NULL"+func() string {
		if project == "" {
			return ""
		}
		return " AND LOWER(project) = ?"
	}(), args...).Scan(&stats.TotalObservations); errors.Is(err, ErrDatabaseGenerationChanged) {
		return nil, err
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM user_prompts"+where, args...).Scan(&stats.TotalPrompts); errors.Is(err, ErrDatabaseGenerationChanged) {
		return nil, err
	}

	projectsQuery := "SELECT project FROM observations WHERE project IS NOT NULL AND deleted_at IS NULL"
	if project != "" {
		projectsQuery += " AND LOWER(project) = ?"
	}
	projectsQuery += " GROUP BY project ORDER BY MAX(created_at) DESC"
	rows, err := s.queryItHook(s.db, projectsQuery, args...)
	if err != nil {
		if errors.Is(err, ErrDatabaseGenerationChanged) {
			return nil, err
		}
		return stats, nil
	}

	for rows.Next() {
		var projectName string
		if err := rows.Scan(&projectName); err != nil {
			_ = rows.Close()
			if errors.Is(err, ErrDatabaseGenerationChanged) {
				return nil, err
			}
			return stats, nil
		}
		stats.Projects = append(stats.Projects, projectName)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		if errors.Is(err, ErrDatabaseGenerationChanged) {
			return nil, err
		}
		return stats, nil
	}
	if err := rows.Close(); err != nil {
		if errors.Is(err, ErrDatabaseGenerationChanged) {
			return nil, err
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
	// Use LOWER(project) = ? so legacy data stored with mixed-case names
	// (created before project normalization was enforced on writes) is found
	// when queried with the current normalized (lowercase) name. The caller
	// is expected to pass an already-normalized name (NormalizeProject result).
	const query = `
SELECT 1 FROM (
  SELECT project FROM observations WHERE LOWER(project) = ? AND deleted_at IS NULL
  UNION ALL
  SELECT project FROM sessions WHERE LOWER(project) = ?
  UNION ALL
  SELECT project FROM user_prompts WHERE LOWER(project) = ?
  UNION ALL
  SELECT project FROM sync_enrolled_projects WHERE LOWER(project) = ?
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

	pinned, err := s.PinnedObservations(project, scope)
	if err != nil {
		return "", err
	}

	observations, err := s.recentUnpinnedObservations(project, scope, s.cfg.MaxContextResults)
	if err != nil {
		return "", err
	}

	prompts, err := s.RecentPrompts(project, 10)
	if err != nil {
		return "", err
	}

	if len(sessions) == 0 && len(pinned) == 0 && len(observations) == 0 && len(prompts) == 0 {
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
				sess.Project, timeutil.FormatLocal(sess.StartedAt), summary, sess.ObservationCount)
		}
		b.WriteString("\n")
	}

	if len(prompts) > 0 {
		b.WriteString("### Recent User Prompts\n")
		for _, p := range prompts {
			fmt.Fprintf(&b, "- %s: %s\n", timeutil.FormatLocal(p.CreatedAt), truncate(p.Content, 200))
		}
		b.WriteString("\n")
	}

	if len(pinned) > 0 {
		b.WriteString("### Pinned\n")
		for _, obs := range pinned {
			fmt.Fprintf(&b, "- [%s] **%s**: %s\n",
				obs.Type, obs.Title, truncate(obs.Content, 300))
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

// FormatCompactionContext returns runtime context that is strictly limited to
// one persisted session. The session's project is derived from the store and
// is never supplied by the caller.
func (s *Store) FormatCompactionContext(sessionID string) (string, error) {
	session, err := s.GetSession(sessionID)
	if err != nil {
		return "", err
	}

	project, _ := NormalizeProject(session.Project)
	var observationCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE session_id = ? AND LOWER(project) = ? AND deleted_at IS NULL`, session.ID, project).Scan(&observationCount); err != nil {
		return "", err
	}
	pinned, err := s.compactionObservations(session.ID, project, true, 0)
	if err != nil {
		return "", err
	}
	observations, err := s.compactionObservations(session.ID, project, false, s.cfg.MaxContextResults)
	if err != nil {
		return "", err
	}
	prompts, err := s.compactionPrompts(session.ID, project, 10)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("## Memory from This Session\n\n")
	b.WriteString("### Session\n")
	summary := ""
	if session.Summary != nil {
		summary = fmt.Sprintf(": %s", truncate(*session.Summary, 200))
	}
	fmt.Fprintf(&b, "- **%s** (%s)%s [%d observations]\n\n", session.ID, timeutil.FormatLocal(session.StartedAt), summary, observationCount)

	if len(prompts) > 0 {
		b.WriteString("### Recent User Prompts\n")
		for _, p := range prompts {
			fmt.Fprintf(&b, "- %s: %s\n", timeutil.FormatLocal(p.CreatedAt), truncate(p.Content, 200))
		}
		b.WriteString("\n")
	}
	if len(pinned) > 0 {
		b.WriteString("### Pinned\n")
		for _, obs := range pinned {
			fmt.Fprintf(&b, "- [%s] **%s**: %s\n", obs.Type, obs.Title, truncate(obs.Content, 300))
		}
		b.WriteString("\n")
	}
	if len(observations) > 0 {
		b.WriteString("### Recent Observations\n")
		for _, obs := range observations {
			fmt.Fprintf(&b, "- [%s] **%s**: %s\n", obs.Type, obs.Title, truncate(obs.Content, 300))
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

// ExportRelationMutations returns relation upsert mutations for non-orphaned
// relation rows whose source and target observations are available locally.
func (s *Store) ExportRelationMutations(project string) ([]SyncMutation, error) {
	normalizedProject, _ := NormalizeProject(project)
	normalizedProject = strings.TrimSpace(normalizedProject)

	query := `
		SELECT r.sync_id, r.source_id, r.target_id, r.relation, r.reason, r.evidence, r.confidence,
		       r.judgment_status, r.marked_by_actor, r.marked_by_kind, r.marked_by_model,
		       r.session_id, coalesce(nullif(src.project, ''), src_s.project, ''), r.created_at, r.updated_at
		FROM memory_relations r
		JOIN observations src ON src.sync_id = r.source_id AND src.deleted_at IS NULL
		JOIN observations tgt ON tgt.sync_id = r.target_id AND tgt.deleted_at IS NULL
		LEFT JOIN sessions src_s ON src_s.id = src.session_id
		LEFT JOIN sessions tgt_s ON tgt_s.id = tgt.session_id
		WHERE r.judgment_status != ?`
	args := []any{JudgmentStatusOrphaned}
	if normalizedProject != "" {
		query += ` AND coalesce(nullif(src.project, ''), src_s.project, '') = ?
			AND coalesce(nullif(tgt.project, ''), tgt_s.project, '') = ?`
		args = append(args, normalizedProject, normalizedProject)
	}
	query += ` ORDER BY r.created_at, r.sync_id`

	rows, err := s.queryItHook(s.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("export relation mutations: %w", err)
	}
	defer rows.Close()

	mutations := []SyncMutation{}
	for rows.Next() {
		var p syncRelationPayload
		if err := rows.Scan(
			&p.SyncID, &p.SourceID, &p.TargetID, &p.Relation, &p.Reason, &p.Evidence, &p.Confidence,
			&p.JudgmentStatus, &p.MarkedByActor, &p.MarkedByKind, &p.MarkedByModel,
			&p.SessionID, &p.Project, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("export relation mutations: scan: %w", err)
		}
		payload, err := json.Marshal(p)
		if err != nil {
			return nil, fmt.Errorf("export relation mutations: marshal %s: %w", p.SyncID, err)
		}
		mutations = append(mutations, SyncMutation{
			Entity:     SyncEntityRelation,
			EntityKey:  strings.TrimSpace(p.SyncID),
			Op:         SyncOpUpsert,
			Payload:    string(payload),
			Project:    strings.TrimSpace(p.Project),
			OccurredAt: p.UpdatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export relation mutations: rows: %w", err)
	}
	return mutations, nil
}

// exportWithProjectScope reads sessions.project through ifnull(): an export is how
// data leaves the store before a repair, so it must not be the one path that
// refuses to read the legacy unowned rows the operator is trying to rescue.
func (s *Store) exportWithProjectScope(project string) (*ExportData, error) {
	data := &ExportData{
		Version:    "0.1.0",
		ExportedAt: Now(),
	}

	sessionQuery := "SELECT id, ifnull(project, ''), directory, started_at, ended_at, summary FROM sessions"
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
	obsQuery := `SELECT ` + observationSelectColumns + `
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
		if err := scanObservationRow(obsRows, &o); err != nil {
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
	for _, sess := range data.Sessions {
		if err := validateSessionID(sess.ID); err != nil {
			return nil, fmt.Errorf("import session: %w", err)
		}
	}
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

	// Import observations (use new IDs — AUTOINCREMENT, skip duplicate sync IDs)
	for _, obs := range data.Observations {
		syncID := normalizeExistingSyncID(obs.SyncID, "obs")
		res, err := s.execHook(tx,
			`INSERT INTO observations (sync_id, session_id, type, title, content, tool_name, project, scope, topic_key, normalized_hash, revision_count, duplicate_count, last_seen_at, review_after, created_at, updated_at, deleted_at)
			 SELECT ?, ?, ?, ?, ?, ?, CAST(? AS TEXT), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			 WHERE NOT EXISTS (SELECT 1 FROM observations WHERE sync_id = ?)`,
			syncID,
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
			obs.ReviewAfter,
			obs.CreatedAt,
			obs.UpdatedAt,
			obs.DeletedAt,
			syncID,
		)
		if err != nil {
			return nil, fmt.Errorf("import observation %d: %w", obs.ID, err)
		}
		n, _ := res.RowsAffected()
		result.ObservationsImported += int(n)
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
		SELECT sm.seq, sm.target_key, sm.entity, sm.entity_key, sm.op, sm.payload, sm.source, sm.project, sm.occurred_at, sm.acked_at, sm.disposition, ifnull(sm.disposition_reason, ''), ifnull(sm.disposition_evidence, ''), sm.disposition_at
		FROM sync_mutations sm
		LEFT JOIN sync_enrolled_projects sep ON sm.project = sep.project
		WHERE sm.target_key = ? AND sm.acked_at IS NULL AND sm.disposition = 'pending'
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
		if err := rows.Scan(&mutation.Seq, &mutation.TargetKey, &mutation.Entity, &mutation.EntityKey, &mutation.Op, &mutation.Payload, &mutation.Source, &mutation.Project, &mutation.OccurredAt, &mutation.AckedAt, &mutation.Disposition, &mutation.DispositionReason, &mutation.DispositionEvidence, &mutation.DispositionAt); err != nil {
			return nil, err
		}
		mutations = append(mutations, mutation)
	}
	return mutations, rows.Err()
}

// CloudSyncSummary returns status across project-scoped cloud targets only.
// It deliberately excludes the legacy global cloud target because explicit cloud
// sync records state under cloud:<project>.
func (s *Store) CloudSyncSummary() (CloudSyncSummary, error) {
	const cloudProjectTarget = "cloud:%"
	var summary CloudSyncSummary
	var lastSuccess, lastError sql.NullString
	err := s.db.QueryRow(`
		SELECT MAX(last_success_at), (
			SELECT last_error FROM sync_state
			WHERE target_key LIKE ? AND last_error IS NOT NULL
			ORDER BY updated_at DESC LIMIT 1
		)
		FROM sync_state WHERE target_key LIKE ?`, cloudProjectTarget, cloudProjectTarget).Scan(&lastSuccess, &lastError)
	if err != nil {
		return CloudSyncSummary{}, err
	}
	if lastSuccess.Valid {
		summary.LastSuccessAt = lastSuccess.String
	}
	if lastError.Valid {
		summary.LastError = lastError.String
	}
	err = s.db.QueryRow(`
		SELECT COUNT(*)
		FROM sync_mutations sm
		JOIN sync_enrolled_projects sep ON sm.project = sep.project
		WHERE sm.target_key LIKE ? AND sm.acked_at IS NULL AND sm.disposition = 'pending'`, cloudProjectTarget).Scan(&summary.PendingMutations)
	if err != nil {
		return CloudSyncSummary{}, err
	}
	return summary, nil
}

func (s *Store) ListPendingSyncMutationsAfterSeq(targetKey string, afterSeq int64, limit int) ([]SyncMutation, error) {
	targetKey = normalizeSyncTargetKey(targetKey)
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.queryItHook(s.db, `
		SELECT sm.seq, sm.target_key, sm.entity, sm.entity_key, sm.op, sm.payload, sm.source, sm.project, sm.occurred_at, sm.acked_at, sm.disposition, ifnull(sm.disposition_reason, ''), ifnull(sm.disposition_evidence, ''), sm.disposition_at
		FROM sync_mutations sm
		LEFT JOIN sync_enrolled_projects sep ON sm.project = sep.project
		WHERE sm.target_key = ? AND sm.acked_at IS NULL AND sm.disposition = 'pending'
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
		if err := rows.Scan(&mutation.Seq, &mutation.TargetKey, &mutation.Entity, &mutation.EntityKey, &mutation.Op, &mutation.Payload, &mutation.Source, &mutation.Project, &mutation.OccurredAt, &mutation.AckedAt, &mutation.Disposition, &mutation.DispositionReason, &mutation.DispositionEvidence, &mutation.DispositionAt); err != nil {
			return nil, err
		}
		mutations = append(mutations, mutation)
	}
	return mutations, rows.Err()
}

// QuarantineIrreparableSyncMutations explicitly disposes of pending mutations
// the existing legacy validator proves cannot be repaired from local state.
// It never acknowledges, rewrites, or deletes a mutation.
func (s *Store) QuarantineIrreparableSyncMutations(targetKey, project string, apply bool) (SyncMutationQuarantineReport, error) {
	targetKey = normalizeSyncTargetKey(targetKey)
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	report := SyncMutationQuarantineReport{Project: project, Applied: apply, Actions: []SyncMutationQuarantineAction{}}
	err := s.withTx(func(tx *sql.Tx) error {
		affectedProjects := map[string]struct{}{}
		quarantinedAny := false
		query := `SELECT seq, target_key, entity, entity_key, op, payload, source, project, occurred_at, acked_at
			FROM sync_mutations WHERE target_key = ? AND acked_at IS NULL AND disposition = 'pending'`
		args := []any{targetKey}
		if project != "" {
			query += ` AND project = ?`
			args = append(args, project)
		}
		query += ` ORDER BY seq ASC`
		rows, err := s.queryItHook(tx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var mutation SyncMutation
			if err := rows.Scan(&mutation.Seq, &mutation.TargetKey, &mutation.Entity, &mutation.EntityKey, &mutation.Op, &mutation.Payload, &mutation.Source, &mutation.Project, &mutation.OccurredAt, &mutation.AckedAt); err != nil {
				return err
			}
			evaluation, err := s.evaluateCloudUpgradeLegacyMutationTx(tx, mutation)
			if err != nil {
				return err
			}
			if !evaluation.hasIssue || evaluation.canRepair {
				continue
			}
			evidence, err := json.Marshal(map[string]any{"check": "sync_mutation_required_fields", "finding": evaluation.finding})
			if err != nil {
				return err
			}
			action := SyncMutationQuarantineAction{Seq: mutation.Seq, Project: mutation.Project, Entity: mutation.Entity, EntityKey: mutation.EntityKey, Op: mutation.Op, ReasonCode: evaluation.finding.ReasonCode, Message: evaluation.finding.Message, Evidence: string(evidence)}
			report.Actions = append(report.Actions, action)
			if !apply {
				continue
			}
			result, err := s.execHook(tx, `UPDATE sync_mutations SET disposition = 'quarantined', disposition_reason = ?, disposition_evidence = ?, disposition_at = datetime('now') WHERE target_key = ? AND seq = ? AND acked_at IS NULL AND disposition = 'pending'`, action.ReasonCode, action.Evidence, targetKey, action.Seq)
			if err != nil {
				return err
			}
			updated, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if updated == 0 {
				continue
			}
			quarantinedAny = true
			mutation.Project, _ = NormalizeProject(mutation.Project)
			if mutation.Project = strings.TrimSpace(mutation.Project); mutation.Project != "" {
				affectedProjects[mutation.Project] = struct{}{}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if !apply || !quarantinedAny {
			return nil
		}
		if err := s.refreshSyncLifecycleTx(tx, targetKey); err != nil {
			return err
		}
		if targetKey == DefaultSyncTargetKey {
			for affectedProject := range affectedProjects {
				if err := s.refreshProjectSyncLifecycleTx(tx, affectedProject); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return SyncMutationQuarantineReport{}, fmt.Errorf("quarantine irreparable sync mutations: %w", err)
	}
	return report, nil
}

func (s *Store) CountPendingNonEnrolledSyncMutations(targetKey string) ([]PendingSyncMutationProjectCount, error) {
	targetKey = normalizeSyncTargetKey(targetKey)
	rows, err := s.queryItHook(s.db, `
		SELECT sm.project, COUNT(*)
		FROM sync_mutations sm
		LEFT JOIN sync_enrolled_projects sep ON sm.project = sep.project
		WHERE sm.target_key = ?
		  AND sm.acked_at IS NULL
		  AND sm.disposition = 'pending'
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
		  AND disposition = 'pending'
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
				 WHERE target_key = ? AND seq <= ? AND acked_at IS NULL AND disposition = 'pending'`,
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
			`UPDATE sync_mutations SET acked_at = datetime('now') WHERE target_key = ? AND seq <= ? AND acked_at IS NULL AND disposition = 'pending'`,
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
				`UPDATE sync_mutations SET acked_at = datetime('now') WHERE target_key = ? AND seq = ? AND acked_at IS NULL AND disposition = 'pending'`,
				targetKey, seq,
			); err != nil {
				return err
			}
			if seq > maxSeq {
				maxSeq = seq
			}
		}
		var remaining int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE target_key = ? AND acked_at IS NULL AND disposition = 'pending'`, targetKey).Scan(&remaining); err != nil {
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
		`SELECT COUNT(*) FROM sync_mutations WHERE target_key = ? AND project = ? AND acked_at IS NULL AND disposition = 'pending'`,
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
		 WHERE target_key = ? AND project = ? AND acked_at IS NULL AND disposition = 'pending'`,
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

// refreshSyncLifecycleTx derives a target lifecycle from its remaining transportable mutations.
func (s *Store) refreshSyncLifecycleTx(tx *sql.Tx, targetKey string) error {
	targetKey = normalizeSyncTargetKey(targetKey)
	var pendingCount int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE target_key = ? AND acked_at IS NULL AND disposition = ?`,
		targetKey, SyncMutationDispositionPending,
	).Scan(&pendingCount); err != nil {
		return err
	}
	return s.applySyncLifecycleTx(tx, targetKey, pendingCount)
}

// refreshProjectSyncLifecycleTx derives the `cloud:<project>` lifecycle from the
// journal rows the local writer actually produces. enqueueSyncMutationTx always
// stores mutations under the default `cloud` target key and keeps the project in
// its own column, so counting rows keyed by `cloud:<project>` would always return
// zero and mark the project healthy while real pending work remains.
func (s *Store) refreshProjectSyncLifecycleTx(tx *sql.Tx, project string) error {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	if project == "" {
		return nil
	}
	var pendingCount int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM sync_mutations WHERE target_key = ? AND project = ? AND acked_at IS NULL AND disposition = ?`,
		DefaultSyncTargetKey, project, SyncMutationDispositionPending,
	).Scan(&pendingCount); err != nil {
		return err
	}
	return s.applySyncLifecycleTx(tx, syncTargetKeyForProject(project), pendingCount)
}

func (s *Store) applySyncLifecycleTx(tx *sql.Tx, targetKey string, pendingCount int) error {
	state, err := s.getSyncStateTx(tx, targetKey)
	if err != nil {
		return err
	}
	lifecycle := SyncLifecycleHealthy
	if pendingCount > 0 {
		lifecycle = SyncLifecyclePending
	}
	if isActivelyDegradedState(state, time.Now().UTC()) {
		lifecycle = SyncLifecycleDegraded
	}
	if lifecycle == state.Lifecycle {
		return nil
	}
	_, err = s.execHook(tx, `UPDATE sync_state SET lifecycle = ?, updated_at = datetime('now') WHERE target_key = ?`, lifecycle, targetKey)
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
			 SET lifecycle = ?, consecutive_failures = 0, backoff_until = NULL, reason_code = NULL, reason_message = NULL, last_error = NULL, last_success_at = datetime('now'), updated_at = datetime('now')
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

// ApplyPulledMutation applies one remote mutation and advances the pull cursor.
//
// Session-identity semantics are skip-plus-evidence, not fail-closed. A blank
// or inconsistent session identity in a pulled mutation is quarantined through
// deadLetterPulledSessionIdentityTx and the cursor still advances past it.
// Failing closed here would be a permanent retry loop: servers that predate the
// identity rule hold historical chunks with blank session IDs, and no local
// action can ever make such a mutation valid, so halting would pin the cursor
// forever and block every later mutation behind it. Quarantining keeps the
// dropped data visible — `engram doctor --check invalid_session_identity`
// reports it and `engram conflicts deferred` lists the raw row.
//
// Every other apply failure keeps its existing fail-closed behavior.
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
			if handled, err := s.recordRelationApplyFailureTx(tx, targetKey, mutation, applyErr); err != nil {
				return err
			} else if !handled {
				if errors.Is(applyErr, ErrPulledSessionIdentityInvalid) {
					if err := s.deadLetterPulledSessionIdentityTx(tx, targetKey, mutation); err != nil {
						return err
					}
				} else {
					return applyErr
				}
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

// relationApplyFailureSyncID derives the sync_apply_deferred key for a failed
// relation mutation. The two apply statuses record different things and so carry
// different identities.
//
// A 'deferred' row is retry state for one relation. applyRelationUpsertTx
// validates the wire contract before it ever reports a missing endpoint, so such
// a mutation is proven to carry a non-blank entity_key equal to its payload's
// sync_id. That relation sync_id is the identity the whole retry contract is
// written in: ReplayDeferredForScope replays the row as the relation's mutation
// and the success path deletes it once the relation applies. Keying on it also
// collapses a redelivered retryable mutation onto the single pending row instead
// of queueing the same relation twice.
//
// A 'dead' row is evidence that one mutation was discarded, and nothing about it
// is proven. Its entity_key may be blank, or may disagree with its payload — both
// are among the reasons it is dead in the first place — so distinct discarded
// mutations can share it. Using it as the key made the second such mutation
// overwrite the first through ON CONFLICT(sync_id) DO UPDATE, destroying the only
// record that the first mutation's data was dropped. The identity is therefore
// derived from the mutation's own distinguishing material, as
// pulledSessionDeadLetterSyncID already does for discarded session mutations:
// distinct mutations stay apart by construction, while a genuine redelivery of
// the same mutation still lands on the same row. Each field is length-prefixed so
// no field's content can imitate a separator and forge a collision.
func relationApplyFailureSyncID(status, targetKey string, mutation SyncMutation) string {
	if status == "deferred" && strings.TrimSpace(mutation.EntityKey) != "" {
		return mutation.EntityKey
	}
	digest := sha256.New()
	for _, field := range []string{targetKey, mutation.Entity, mutation.EntityKey, mutation.Op, mutation.Payload} {
		digest.Write([]byte(strconv.Itoa(len(field))))
		digest.Write([]byte(":"))
		digest.Write([]byte(field))
	}
	return "relation-dead-" + hex.EncodeToString(digest.Sum(nil))
}

// recordRelationApplyFailureTx records relation failures that are safe to
// acknowledge while preserving the existing fail-fast behavior for other
// entities and errors.
func (s *Store) recordRelationApplyFailureTx(tx *sql.Tx, targetKey string, mutation SyncMutation, applyErr error) (bool, error) {
	if mutation.Entity != SyncEntityRelation {
		return false, nil
	}

	status := ""
	switch {
	case errors.Is(applyErr, ErrRelationFKMissing):
		status = "deferred"
	case errors.Is(applyErr, ErrApplyDead):
		status = "dead"
	default:
		return false, nil
	}

	// Read the payload the same way applyRelationUpsertTx reads it, so the
	// identity stored on the row is the identity the applier would recognise.
	var payload syncRelationPayload
	payloadDecoded := decodeSyncPayload([]byte(mutation.Payload), &payload) == nil

	project := strings.TrimSpace(mutation.Project)
	if project == "" && payloadDecoded {
		project = strings.TrimSpace(payload.Project)
	}
	project, _ = NormalizeProject(project)
	scopeClass := "target_scoped"
	if project != "" {
		scopeClass = "scoped"
	}

	// The relation this payload claims to be. It is not derivable from the row
	// key, because a dead row is keyed on the discarded mutation's own material,
	// and it is not derivable from entity_key, because a dead mutation's key may
	// be blank or may name some other relation entirely. Persisting it is what
	// lets the success-path cleanup find a relation's rows without parsing JSON
	// inside the apply write transaction.
	payloadSyncID := ""
	if payloadDecoded {
		payloadSyncID = strings.TrimSpace(payload.SyncID)
	}

	syncID := relationApplyFailureSyncID(status, targetKey, mutation)

	// Rows written before the identity above existed are keyed on the mutation's
	// entity_key and store no entity_key of their own. Retire the one this exact
	// mutation wrote, so redelivering it rekeys its evidence instead of leaving a
	// duplicate behind. The payload equality is what proves the legacy row belongs
	// to this mutation rather than to another one that collapsed onto the same key,
	// and the status guard keeps pending retry state out of reach.
	if syncID != mutation.EntityKey {
		if _, err := s.execHook(tx, `
			DELETE FROM sync_apply_deferred
			WHERE entity = ?
			  AND sync_id = ?
			  AND entity_key = ''
			  AND payload = ?
			  AND apply_status = 'dead'
		`, mutation.Entity, mutation.EntityKey, mutation.Payload); err != nil {
			return false, fmt.Errorf("retire legacy relation apply failure: %w", err)
		}
	}

	if _, err := s.execHook(tx, `
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, entity_key, op, payload_sync_id, project, scope_class, apply_status, retry_count, first_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, datetime('now'))
		ON CONFLICT(sync_id) DO UPDATE SET
			payload           = excluded.payload,
			target_key        = excluded.target_key,
			entity_key        = excluded.entity_key,
			op                = excluded.op,
			payload_sync_id   = excluded.payload_sync_id,
			project           = excluded.project,
			scope_class       = excluded.scope_class,
			apply_status      = CASE
				WHEN excluded.apply_status = 'dead' THEN 'dead'
				ELSE sync_apply_deferred.apply_status
			END,
			last_attempted_at = datetime('now')
	`, syncID, mutation.Entity, mutation.Payload, targetKey, mutation.EntityKey, mutation.Op, payloadSyncID, project, scopeClass, status); err != nil {
		return false, fmt.Errorf("write relation apply failure: %w", err)
	}

	log.Printf("[store] relation apply seq=%d entity_key=%s sync_id=%s err=%v - marking %s", mutation.Seq, mutation.EntityKey, syncID, applyErr, status)
	return true, nil
}

// ApplyPulledChunk atomically applies all mutations contained in a pulled chunk
// and records the chunk as synced in the same transaction. This guarantees
// retry safety: a failed chunk import leaves no partial semantic mutations.
//
// It shares ApplyPulledMutation's skip-plus-evidence rule for invalid session
// identities: such a mutation is quarantined and the rest of the chunk still
// applies, so one historical blank identity cannot block the chunk forever.
// A payload that does not even decode stays fail-closed and rolls back the
// whole chunk, because an undecodable payload is a transport-level fault rather
// than known-corrupt historical data.
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
			if applyErr := s.applyPulledMutationTx(tx, mutation); applyErr != nil {
				if handled, err := s.recordRelationApplyFailureTx(tx, targetKey, mutation, applyErr); err != nil {
					return fmt.Errorf("apply chunk mutation %d: %w", i, err)
				} else if !handled {
					if errors.Is(applyErr, ErrPulledSessionIdentityInvalid) {
						if err := s.deadLetterPulledSessionIdentityTx(tx, targetKey, mutation); err != nil {
							return fmt.Errorf("apply chunk mutation %d: %w", i, err)
						}
					} else {
						return fmt.Errorf("apply chunk mutation %d: %w", i, applyErr)
					}
				}
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
		`SELECT `+observationSelectColumns+`
		 FROM observations WHERE sync_id = ? AND deleted_at IS NULL ORDER BY id DESC LIMIT 1`,
		syncID,
	)
	var o Observation
	if err := scanObservationRow(row, &o); err != nil {
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

// ProjectRescueParams identifies historical rows whose missing project ownership
// was explicitly confirmed by an operator. Only rows with a NULL project qualify.
type ProjectRescueParams struct {
	TargetProject  string
	ObservationIDs []int64
	SessionIDs     []string
	PromptIDs      []int64
}

// Reasons a record or session was left behind by a rescue.
const (
	// RescueBlockedOwnedByOtherProject means the row itself already belongs to a
	// project other than the target.
	RescueBlockedOwnedByOtherProject = "owned_by_other_project"
	// RescueBlockedSessionOwnedByOtherProject means the row is unowned but its
	// parent session belongs to another project, so moving it would split it.
	RescueBlockedSessionOwnedByOtherProject = "session_owned_by_other_project"
	// RescueBlockedDependentRecordOwnedByOtherProject means an unowned session
	// was left in place because it parents a record owned by another project.
	RescueBlockedDependentRecordOwnedByOtherProject = "dependent_record_owned_by_other_project"
	// RescueBlockedMissing means the requested row does not exist.
	RescueBlockedMissing = "missing"
)

// ProjectRescueBlocked names one row the rescue deliberately did not move, and
// why. It is what lets an operator tell "everything moved" apart from "some
// things were left behind" without guessing from counters.
type ProjectRescueBlocked struct {
	// Kind is "session", "observation", or "prompt".
	Kind string `json:"kind"`
	// ID is the session id or the decimal record id.
	ID string `json:"id"`
	// Reason is one of the RescueBlocked* constants.
	Reason string `json:"reason"`
	// OwnedBy is the conflicting project, when one is known.
	OwnedBy string `json:"owned_by,omitempty"`
}

// ProjectRescueResult reports local ownership recovery. Journaled means a
// canonical pending local mutation exists after the call, whether newly
// inserted or already pending; it does not imply a cloud acknowledgement.
type ProjectRescueResult struct {
	RescuedObservations int64 `json:"rescued_observations"`
	RescuedSessions     int64 `json:"rescued_sessions"`
	RescuedPrompts      int64 `json:"rescued_prompts"`
	ConflictingRecords  int64 `json:"conflicting_records"`
	SkippedRecords      int64 `json:"skipped_records"`
	Journaled           bool  `json:"journaled"`
	// Complete is true only when every requested row now belongs to the target
	// project and nothing was left behind.
	Complete bool `json:"complete"`
	// Blocked lists exactly what was left behind, and why.
	Blocked []ProjectRescueBlocked `json:"blocked"`
}

func (r ProjectRescueResult) Rescued() int64 {
	return r.RescuedObservations + r.RescuedSessions + r.RescuedPrompts
}

func (r *ProjectRescueResult) block(kind, id, reason, ownedBy string) {
	r.Blocked = append(r.Blocked, ProjectRescueBlocked{Kind: kind, ID: id, Reason: reason, OwnedBy: ownedBy})
}

// RescueNullProjectOwnership assigns an explicit project only to selected legacy
// records with NULL ownership and enqueues their missing canonical mutations.
func (s *Store) RescueNullProjectOwnership(p ProjectRescueParams) (*ProjectRescueResult, error) {
	target, _ := NormalizeProject(p.TargetProject)
	if strings.TrimSpace(target) == "" {
		return nil, ErrProjectRequired
	}
	if len(p.ObservationIDs) == 0 && len(p.SessionIDs) == 0 && len(p.PromptIDs) == 0 {
		return nil, fmt.Errorf("%w: at least one record id is required", ErrProjectRescueInvalidRequest)
	}
	for _, id := range append(append([]int64{}, p.ObservationIDs...), p.PromptIDs...) {
		if id <= 0 {
			return nil, fmt.Errorf("%w: record ids must be positive", ErrProjectRescueInvalidRequest)
		}
	}
	for _, id := range p.SessionIDs {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("%w: session ids must not be blank", ErrProjectRescueInvalidRequest)
		}
	}

	result := &ProjectRescueResult{}
	err := s.withTx(func(tx *sql.Tx) error {
		// The whole plan — which sessions and which records will move — is
		// resolved before anything is written. Mutating the session pass first
		// would move a session out from under a record the record pass then
		// classifies as conflicting, splitting the record from its session in
		// the mirror direction of the case this rescue guards.
		observations, err := loadRescueRecordsTx(tx, rescueObservationQuery, p.ObservationIDs)
		if err != nil {
			return err
		}
		prompts, err := loadRescueRecordsTx(tx, rescuePromptQuery, p.PromptIDs)
		if err != nil {
			return err
		}

		scope := resolveRescueSessionScope(p.SessionIDs, observations, prompts)
		plan, err := planRescueSessionsTx(tx, scope, target, result)
		if err != nil {
			return err
		}

		observationMoves, err := planRescueRecordsTx(tx, rescueObservationQuery, "observation", observations, plan, target, result)
		if err != nil {
			return err
		}
		promptMoves, err := planRescueRecordsTx(tx, rescuePromptQuery, "prompt", prompts, plan, target, result)
		if err != nil {
			return err
		}

		// Plan settled — only now does anything change.
		for _, sessionID := range plan.claim {
			if _, err := s.execHook(tx, rescueSessionQuery.updateProject, target, sessionID); err != nil {
				return err
			}
			result.RescuedSessions++
		}
		for _, query := range []struct {
			query rescueRecordQuery
			moves []int64
			count *int64
		}{
			{rescueObservationQuery, observationMoves, &result.RescuedObservations},
			{rescuePromptQuery, promptMoves, &result.RescuedPrompts},
		} {
			for _, id := range query.moves {
				if _, err := s.execHook(tx, query.query.updateProject, target, id); err != nil {
					return err
				}
				*query.count++
			}
		}

		journaled, err := s.enqueueRescuedProjectMutationsTx(tx, target, scope.ordered, p)
		if err != nil {
			return err
		}
		result.Journaled = journaled
		result.Complete = len(result.Blocked) == 0
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// rescuePlan is the settled decision about session ownership, computed before
// any row is written.
type rescuePlan struct {
	// claim lists unowned sessions that will be moved to the target.
	claim []string
	// willOwn reports, for every session in scope, whether it belongs to the
	// target once the plan is applied.
	willOwn map[string]bool
}

// planRescueSessionsTx decides which sessions can move, without moving any. An
// unowned session that already parents a record owned by another project stays
// put: claiming it would split that record from its session.
func planRescueSessionsTx(tx *sql.Tx, scope rescueSessionScope, target string, result *ProjectRescueResult) (rescuePlan, error) {
	plan := rescuePlan{willOwn: make(map[string]bool, len(scope.ordered))}
	for _, sessionID := range scope.ordered {
		project, found, err := sessionOwnershipTx(tx, sessionID)
		if err != nil {
			return plan, err
		}
		switch {
		case !found:
			if scope.explicit[sessionID] {
				result.countOutcome(rescueMissing)
				result.block("session", sessionID, RescueBlockedMissing, "")
			}
		case project == target:
			plan.willOwn[sessionID] = true
			if scope.explicit[sessionID] {
				result.countOutcome(rescueAlreadyOwned)
			}
		case project != "":
			if scope.explicit[sessionID] {
				result.countOutcome(rescueConflict)
			}
			result.block("session", sessionID, RescueBlockedOwnedByOtherProject, project)
		default:
			_, owner, err := foreignRecordOwnerTx(tx, sessionID, target)
			if err != nil {
				return plan, err
			}
			if owner != "" {
				// Leaving the session unowned keeps it with its records; moving
				// it would strand the foreign-owned one.
				if scope.explicit[sessionID] {
					result.countOutcome(rescueConflict)
				}
				result.block("session", sessionID, RescueBlockedDependentRecordOwnedByOtherProject, owner)
				continue
			}
			plan.claim = append(plan.claim, sessionID)
			plan.willOwn[sessionID] = true
		}
	}
	return plan, nil
}

// planRescueRecordsTx decides which records can move, without moving any.
func planRescueRecordsTx(tx *sql.Tx, query rescueRecordQuery, kind string, records []rescueRecord, plan rescuePlan, target string, result *ProjectRescueResult) ([]int64, error) {
	var moves []int64
	for _, record := range records {
		id := strconv.FormatInt(record.id, 10)
		if !record.exists {
			result.countOutcome(rescueMissing)
			result.block(kind, id, RescueBlockedMissing, "")
			continue
		}
		var raw sql.NullString
		if err := tx.QueryRow(query.selectProject, record.id).Scan(&raw); err != nil {
			return nil, err
		}
		owned, _ := NormalizeProject(strings.TrimSpace(raw.String))
		if owned == target {
			result.countOutcome(rescueAlreadyOwned)
			continue
		}
		if owned != "" {
			result.countOutcome(rescueConflict)
			result.block(kind, id, RescueBlockedOwnedByOtherProject, owned)
			continue
		}
		if !plan.willOwn[record.sessionID] {
			result.countOutcome(rescueConflict)
			result.block(kind, id, RescueBlockedSessionOwnedByOtherProject, "")
			continue
		}
		moves = append(moves, record.id)
	}
	return moves, nil
}

// rescueOutcome classifies what happened to one record during a rescue.
type rescueOutcome int

const (
	rescueRescued rescueOutcome = iota
	rescueAlreadyOwned
	rescueConflict
	rescueMissing
)

func (r *ProjectRescueResult) countOutcome(outcome rescueOutcome) {
	switch outcome {
	case rescueAlreadyOwned, rescueMissing:
		r.SkippedRecords++
	case rescueConflict:
		r.ConflictingRecords++
	}
}

type rescueRecordQuery struct {
	// selectSessionID reads the parent session id of one record. It is empty for
	// sessions, which have no parent.
	selectSessionID string
	selectProject   string
	updateProject   string
}

var (
	rescueObservationQuery = rescueRecordQuery{
		selectSessionID: `SELECT session_id FROM observations WHERE id = ?`,
		selectProject:   `SELECT project FROM observations WHERE id = ?`,
		updateProject:   `UPDATE observations SET project = ? WHERE id = ? AND ifnull(trim(project), '') = ''`,
	}
	rescuePromptQuery = rescueRecordQuery{
		selectSessionID: `SELECT session_id FROM user_prompts WHERE id = ?`,
		selectProject:   `SELECT project FROM user_prompts WHERE id = ?`,
		updateProject:   `UPDATE user_prompts SET project = ? WHERE id = ? AND ifnull(trim(project), '') = ''`,
	}
	rescueSessionQuery = rescueRecordQuery{
		selectProject: `SELECT project FROM sessions WHERE id = ?`,
		updateProject: `UPDATE sessions SET project = ? WHERE id = ? AND ifnull(trim(project), '') = ''`,
	}
)

// rescueRecord is the pre-read ownership state of one requested record.
type rescueRecord struct {
	id        int64
	sessionID string
	exists    bool
}

func loadRescueRecordsTx(tx *sql.Tx, query rescueRecordQuery, ids []int64) ([]rescueRecord, error) {
	records := make([]rescueRecord, 0, len(ids))
	for _, id := range ids {
		record := rescueRecord{id: id}
		err := tx.QueryRow(query.selectSessionID, id).Scan(&record.sessionID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		record.exists = err == nil
		records = append(records, record)
	}
	return records, nil
}

// rescueSessionScope is the set of sessions whose ownership must move with the
// requested records: the explicitly requested ones plus every parent session.
type rescueSessionScope struct {
	ordered  []string
	explicit map[string]bool
}

func resolveRescueSessionScope(explicitIDs []string, recordGroups ...[]rescueRecord) rescueSessionScope {
	scope := rescueSessionScope{explicit: make(map[string]bool, len(explicitIDs))}
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		scope.ordered = append(scope.ordered, id)
	}
	for _, id := range explicitIDs {
		scope.explicit[id] = true
		add(id)
	}
	for _, group := range recordGroups {
		for _, record := range group {
			if record.exists {
				add(record.sessionID)
			}
		}
	}
	return scope
}

func (s *Store) MigrateProject(oldName, newName string) (*MigrateResult, error) {
	// The rename lands on the migrated (normalized) identity — every other
	// write path normalizes project names, so a rename must as well or the
	// renamed records would carry a spelling no other route can produce.
	newName, _ = NormalizeProject(newName)
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

		// Migrate the old name's sync identity — pending journal rows and
		// enrollment — so renaming a project (including one that was
		// consolidated) keeps a single deliverable sync identity. Only the
		// exact spelling whose records moved above is migrated: a distinct
		// project stored under the normalized spelling keeps its own journal
		// rows and enrollment.
		if err := s.migrateProjectSyncIdentityTx(tx, []string{oldName}, newName); err != nil {
			return fmt.Errorf("migrate sync identity: %w", err)
		}

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

// ListProjectsForCloudEnrollment returns every local identity that can be
// enrolled, normalized and ordered deterministically for the cloud TUI.
func (s *Store) ListProjectsForCloudEnrollment() ([]string, error) {
	rows, err := s.queryItHook(s.db, `SELECT project FROM (
		SELECT project FROM observations WHERE deleted_at IS NULL
		UNION
		SELECT project FROM sessions
		UNION
		SELECT project FROM user_prompts
		UNION
		SELECT project FROM sync_enrolled_projects
	) WHERE project IS NOT NULL AND TRIM(project) != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projects := make(map[string]struct{})
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			return nil, err
		}
		project, _ = NormalizeProject(project)
		if strings.TrimSpace(project) != "" {
			projects[project] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	results := make([]string, 0, len(projects))
	for project := range projects {
		results = append(results, project)
	}
	sort.Strings(results)
	return results, nil
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
// canonical name. Every source must normalize to the canonical name; sources
// that exactly equal the canonical name or have no records are skipped.
// All updates are performed inside a single transaction for atomicity.
func (s *Store) MergeProjects(sources []string, canonical string) (*MergeResult, error) {
	canonical, _ = NormalizeProject(canonical)
	if canonical == "" {
		return nil, fmt.Errorf("canonical project name must not be empty")
	}
	validatedSources := make([]string, len(sources))
	for i, source := range sources {
		normalizedSource, _ := NormalizeProject(source)
		if normalizedSource == "" {
			return nil, fmt.Errorf("source project name must not be empty")
		}
		if normalizedSource != canonical {
			return nil, fmt.Errorf("source project %q must normalize to canonical project %q", source, canonical)
		}
		validatedSources[i] = normalizedSource
	}

	result := &MergeResult{Canonical: canonical}

	err := s.withTx(func(tx *sql.Tx) error {
		seenSources := make(map[string]struct{})
		for i, srcInput := range sources {
			srcNormalized := validatedSources[i]
			if srcInput == canonical {
				continue
			}
			if _, seen := seenSources[srcInput]; seen {
				continue
			}
			seenSources[srcInput] = struct{}{}

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
			sourceUpdated := false

			res, err := s.execHook(tx, `UPDATE observations SET project = ? WHERE project IN (`+placeholders+`)`, args...)
			if err != nil {
				return fmt.Errorf("merge observations %q → %q: %w", srcNormalized, canonical, err)
			}
			n, _ := res.RowsAffected()
			result.ObservationsUpdated += n
			sourceUpdated = sourceUpdated || n > 0

			res, err = s.execHook(tx, `UPDATE sessions SET project = ? WHERE project IN (`+placeholders+`)`, args...)
			if err != nil {
				return fmt.Errorf("merge sessions %q → %q: %w", srcNormalized, canonical, err)
			}
			n, _ = res.RowsAffected()
			result.SessionsUpdated += n
			sourceUpdated = sourceUpdated || n > 0

			res, err = s.execHook(tx, `UPDATE user_prompts SET project = ? WHERE project IN (`+placeholders+`)`, args...)
			if err != nil {
				return fmt.Errorf("merge prompts %q → %q: %w", srcNormalized, canonical, err)
			}
			n, _ = res.RowsAffected()
			result.PromptsUpdated += n
			sourceUpdated = sourceUpdated || n > 0

			// Migrate the source's sync identity — pending journal rows and
			// enrollment — so no legacy mutation suppresses canonical backfill
			// or is later skip-acked as belonging to a non-enrolled project.
			if err := s.migrateProjectSyncIdentityTx(tx, sourceVariants, canonical); err != nil {
				return fmt.Errorf("merge sync identity %q → %q: %w", srcNormalized, canonical, err)
			}

			if sourceUpdated {
				result.SourcesMerged = append(result.SourcesMerged, sourceVariants[0])
			}
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
	rawSource = strings.TrimSpace(rawSource)
	if rawSource == "" || rawSource == canonical || normalizedSource != canonical {
		return nil
	}
	return []string{rawSource}
}

// migrateProjectSyncIdentityTx moves the pending sync journal rows and the
// cloud sync enrollment of the given source project spellings onto the
// canonical name, so a merged or renamed project keeps a single deliverable
// sync identity. Pending mutations are migrated in place — journal project
// column and payload project field — which preserves their entity coverage
// (so backfill correctly skips those entities) while making them deliverable
// under the canonical enrollment instead of being skip-acked as non-enrolled.
// Acked journal rows are immutable history and stay untouched.
func (s *Store) migrateProjectSyncIdentityTx(tx *sql.Tx, sources []string, canonical string) error {
	seen := make(map[string]struct{}, len(sources))
	variants := make([]string, 0, len(sources))
	for _, source := range sources {
		source = strings.TrimSpace(source)
		if source == "" || source == canonical {
			continue
		}
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		variants = append(variants, source)
	}
	if len(variants) == 0 {
		return nil
	}

	placeholders := sqlPlaceholders(len(variants))
	variantArgs := make([]any, 0, len(variants))
	for _, variant := range variants {
		variantArgs = append(variantArgs, variant)
	}

	// Migrate pending journal rows: both rows whose project column carries a
	// source spelling and rows whose payload still embeds one.
	args := make([]any, 0, 3*len(variants)+2)
	args = append(args, variantArgs...)
	args = append(args, canonical, canonical)
	args = append(args, variantArgs...)
	args = append(args, variantArgs...)
	if _, err := s.execHook(tx, `
		UPDATE sync_mutations
		SET payload = CASE
				WHEN json_valid(payload) AND json_extract(payload, '$.project') IN (`+placeholders+`)
				THEN json_set(payload, '$.project', ?)
				ELSE payload
			END,
			project = ?
		WHERE acked_at IS NULL
		  AND (project IN (`+placeholders+`)
		       OR (json_valid(payload) AND json_extract(payload, '$.project') IN (`+placeholders+`)))`,
		args...,
	); err != nil {
		return fmt.Errorf("migrate pending sync mutations: %w", err)
	}

	// Carry enrollment: if any source spelling was enrolled, the canonical
	// name becomes (or stays) enrolled and the source rows are superseded.
	insertArgs := make([]any, 0, len(variants)+1)
	insertArgs = append(insertArgs, canonical)
	insertArgs = append(insertArgs, variantArgs...)
	if _, err := s.execHook(tx, `
		INSERT OR IGNORE INTO sync_enrolled_projects (project)
		SELECT ? WHERE EXISTS (
			SELECT 1 FROM sync_enrolled_projects WHERE project IN (`+placeholders+`)
		)`,
		insertArgs...,
	); err != nil {
		return fmt.Errorf("carry sync enrollment: %w", err)
	}
	if _, err := s.execHook(tx,
		`DELETE FROM sync_enrolled_projects WHERE project IN (`+placeholders+`)`,
		variantArgs...,
	); err != nil {
		return fmt.Errorf("supersede source sync enrollment: %w", err)
	}
	return nil
}

// ─── Project Pruning ─────────────────────────────────────────────────────────

// PruneResult holds the outcome of pruning a single project.
type PruneResult struct {
	Project         string `json:"project"`
	SessionsDeleted int64  `json:"sessions_deleted"`
	PromptsDeleted  int64  `json:"prompts_deleted"`
}

// PruneProject removes prompts and sessions without observations for a project
// that has zero active observations. Soft-deleted observations and their
// sessions are retained.
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

		res, err = s.execHook(tx, `DELETE FROM sessions
			WHERE project = ?
			  AND NOT EXISTS (SELECT 1 FROM observations WHERE observations.session_id = sessions.id)`, project)
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

// ─── Delete Project ───────────────────────────────────────────────────────────

// DeleteProjectResult summarises a cascade project deletion.
type DeleteProjectResult struct {
	Project             string `json:"project"`
	ObservationsDeleted int64  `json:"observations_deleted"`
	PromptsDeleted      int64  `json:"prompts_deleted"`
	SessionsDeleted     int64  `json:"sessions_deleted"`
	HardDelete          bool   `json:"hard_delete"`
}

// DeleteProject removes all data associated with a project in a single
// transaction.
//
// When hardDelete is true: observation rows are permanently removed, prompts
// are hard-deleted, and sessions are hard-deleted. memory_relations that
// reference any removed observation are marked orphaned (audit history).
//
// When hardDelete is false: observations are soft-deleted (deleted_at set),
// and prompts are hard-deleted. Sessions are NOT removed in this path because
// observations.session_id is a NOT NULL FK to sessions — removing sessions
// while soft-deleted observation rows still reference them would violate the FK
// constraint. The session rows remain and can be cleaned up with
// engram delete session <id> once the observations are purged.
//
// Returns ErrProjectNotFound when no sessions or observations exist for the
// given project name.
func (s *Store) DeleteProject(project string, hardDelete bool) (*DeleteProjectResult, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, fmt.Errorf("project name must not be empty")
	}

	result := &DeleteProjectResult{Project: project, HardDelete: hardDelete}

	err := s.withTx(func(tx *sql.Tx) error {
		// Existence check: at least one session or observation must exist.
		var sessionCount int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sessions WHERE project = ?`, project).Scan(&sessionCount); err != nil {
			return fmt.Errorf("delete project: count sessions: %w", err)
		}
		var obsCount int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM observations WHERE project = ?`, project).Scan(&obsCount); err != nil {
			return fmt.Errorf("delete project: count observations: %w", err)
		}
		if sessionCount == 0 && obsCount == 0 {
			return fmt.Errorf("%w: %q", ErrProjectNotFound, project)
		}

		// 1. Delete/soft-delete observations.
		if hardDelete {
			// Orphan memory_relations rows that reference any observation in this project.
			if _, err := s.execHook(tx, `
				UPDATE memory_relations
				SET judgment_status = 'orphaned',
				    updated_at      = datetime('now')
				WHERE source_id IN (SELECT sync_id FROM observations WHERE project = ?)
				   OR target_id IN (SELECT sync_id FROM observations WHERE project = ?)
			`, project, project); err != nil {
				return fmt.Errorf("delete project: orphan relations: %w", err)
			}
			res, err := s.execHook(tx, `DELETE FROM observations WHERE project = ?`, project)
			if err != nil {
				return fmt.Errorf("delete project: hard-delete observations: %w", err)
			}
			result.ObservationsDeleted, _ = res.RowsAffected()
		} else {
			res, err := s.execHook(tx, `
				UPDATE observations
				SET deleted_at = datetime('now'),
				    updated_at = datetime('now')
				WHERE project = ? AND deleted_at IS NULL
			`, project)
			if err != nil {
				return fmt.Errorf("delete project: soft-delete observations: %w", err)
			}
			result.ObservationsDeleted, _ = res.RowsAffected()
		}

		// 2. Delete prompts for the project (no soft-delete mechanism exists).
		res, err := s.execHook(tx, `DELETE FROM user_prompts WHERE project = ?`, project)
		if err != nil {
			return fmt.Errorf("delete project: delete prompts: %w", err)
		}
		result.PromptsDeleted, _ = res.RowsAffected()

		// 3. Delete sessions — only when hard-deleting, because observation rows
		//    reference sessions via a NOT NULL FK and soft-deleted rows are still
		//    present in the table. Keep sessions referenced by either table, even
		//    when the remaining rows belong to another project.
		if hardDelete {
			res, err = s.execHook(tx, `DELETE FROM sessions
				WHERE project = ?
				  AND NOT EXISTS (SELECT 1 FROM observations WHERE observations.session_id = sessions.id)
				  AND NOT EXISTS (SELECT 1 FROM user_prompts WHERE user_prompts.session_id = sessions.id)`, project)
			if err != nil {
				return fmt.Errorf("delete project: delete sessions: %w", err)
			}
			result.SessionsDeleted, _ = res.RowsAffected()
		}

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
	if err := validateSessionID(id); err != nil {
		return err
	}
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
		       consecutive_failures, backoff_until, lease_owner, lease_until, reason_code, reason_message, last_error, last_success_at, updated_at
		FROM sync_state WHERE target_key = ?`, targetKey)
	var state SyncState
	if err := row.Scan(&state.TargetKey, &state.Lifecycle, &state.LastEnqueuedSeq, &state.LastAckedSeq, &state.LastPulledSeq, &state.ConsecutiveFailures, &state.BackoffUntil, &state.LeaseOwner, &state.LeaseUntil, &state.ReasonCode, &state.ReasonMessage, &state.LastError, &state.LastSuccessAt, &state.UpdatedAt); err != nil {
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
		       consecutive_failures, backoff_until, lease_owner, lease_until, reason_code, reason_message, last_error, last_success_at, updated_at
		FROM sync_state WHERE target_key = ?`, targetKey)
	var state SyncState
	if err := row.Scan(&state.TargetKey, &state.Lifecycle, &state.LastEnqueuedSeq, &state.LastAckedSeq, &state.LastPulledSeq, &state.ConsecutiveFailures, &state.BackoffUntil, &state.LeaseOwner, &state.LeaseUntil, &state.ReasonCode, &state.ReasonMessage, &state.LastError, &state.LastSuccessAt, &state.UpdatedAt); err != nil {
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
	if err := s.backfillPromptSyncMutationsTx(tx, project); err != nil {
		return err
	}
	return s.backfillRelationSyncMutationsTx(tx, project)
}

// enqueueRescuedProjectMutationsTx journals the rescued rows. sessionIDs covers
// the explicitly requested sessions plus every dependent parent session, so a
// rescued observation is never pushed ahead of the session that now owns it.
func (s *Store) enqueueRescuedProjectMutationsTx(tx *sql.Tx, target string, sessionIDs []string, p ProjectRescueParams) (bool, error) {
	journaled := false
	for _, id := range sessionIDs {
		var payload syncSessionPayload
		err := tx.QueryRow(`SELECT id, project, directory, started_at, ended_at, summary FROM sessions WHERE id = ? AND project = ?`, id, target).
			Scan(&payload.ID, &payload.Project, &payload.Directory, &payload.StartedAt, &payload.EndedAt, &payload.Summary)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		canonical, err := s.enqueueMissingLocalMutationTx(tx, SyncEntitySession, payload.ID, payload)
		if err != nil {
			return false, err
		}
		journaled = journaled || canonical
	}
	for _, id := range p.ObservationIDs {
		var payload syncObservationPayload
		err := tx.QueryRow(`SELECT sync_id, session_id, type, title, content, tool_name, project, scope, topic_key, revision_count, duplicate_count, last_seen_at, created_at, updated_at, deleted_at FROM observations WHERE id = ? AND project = ?`, id, target).
			Scan(&payload.SyncID, &payload.SessionID, &payload.Type, &payload.Title, &payload.Content, &payload.ToolName, &payload.Project, &payload.Scope, &payload.TopicKey, &payload.RevisionCount, &payload.DuplicateCount, &payload.LastSeenAt, &payload.CreatedAt, &payload.UpdatedAt, &payload.DeletedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		op := SyncOpUpsert
		if payload.DeletedAt != nil {
			op = SyncOpDelete
			payload.Deleted = true
		}
		canonical, err := s.enqueueMissingLocalMutationTx(tx, SyncEntityObservation, payload.SyncID, payload, op)
		if err != nil {
			return false, err
		}
		journaled = journaled || canonical
	}
	for _, id := range p.PromptIDs {
		var payload syncPromptPayload
		err := tx.QueryRow(`SELECT sync_id, session_id, content, project, created_at FROM user_prompts WHERE id = ? AND project = ?`, id, target).
			Scan(&payload.SyncID, &payload.SessionID, &payload.Content, &payload.Project, &payload.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		canonical, err := s.enqueueMissingLocalMutationTx(tx, SyncEntityPrompt, payload.SyncID, payload)
		if err != nil {
			return false, err
		}
		journaled = journaled || canonical
	}
	return journaled, nil
}

func (s *Store) enqueueMissingLocalMutationTx(tx *sql.Tx, entity, entityKey string, payload any, ops ...string) (bool, error) {
	project, _ := NormalizeProject(strings.TrimSpace(extractProjectFromPayload(payload)))
	// A blank-owned payload has no project identity to reconcile against, so it
	// must never reach the journal — pushing it would recreate the unowned rows
	// this rescue exists to repair.
	if project == "" {
		return false, ErrProjectRequired
	}
	op := SyncOpUpsert
	if len(ops) > 0 {
		op = ops[0]
	}
	var canonical bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sync_mutations WHERE target_key = ? AND entity = ? AND entity_key = ? AND op = ? AND source = ? AND project = ? AND acked_at IS NULL AND json_extract(payload, '$.project') = ?)`, DefaultSyncTargetKey, entity, entityKey, op, SyncSourceLocal, project, project).Scan(&canonical); err != nil {
		return false, err
	}
	if canonical {
		return true, nil
	}
	if err := s.enqueueSyncMutationTx(tx, entity, entityKey, op, payload); err != nil {
		return false, err
	}
	return true, nil
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
			// Blank source identities can never be enqueued (enqueueSyncMutationTx
			// rejects them), so counting them here would make every open of an
			// affected store run a backfill transaction that can only fail or
			// no-op. The predicate must stay identical to the one in
			// backfillSessionSyncMutationsTx and to isBlankSessionID.
			q: `SELECT COUNT(*) FROM sessions
			    WHERE project = ?
			      AND ` + sqlSessionIDNotBlank("id") + `
			      AND NOT EXISTS (
			        SELECT 1 FROM sync_mutations sm
			        WHERE sm.target_key = ? AND sm.entity = ? AND sm.entity_key = sessions.id AND sm.source = ?
			      )`,
			args: []any{project, sqlWhitespaceTrimSet, DefaultSyncTargetKey, SyncEntitySession, SyncSourceLocal},
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
		{
			// Count only fully-judged relations (not orphaned, not pending, with
			// marked_by_actor/kind populated) whose source and target observations
			// are locally available and that have no local upsert sync_mutations row.
			// Mirrors the SELECT in backfillRelationSyncMutationsTx exactly — any
			// divergence causes the fast-path skip to desync from the write path.
			// Pending/unmarked rows lack marked_by_* and would be rejected by cloud
			// validation (HTTP 400), so we exclude them from both the count and the
			// backfill to avoid polluting the sync journal with undeliverable mutations.
			q: `SELECT COUNT(*)
			    FROM memory_relations r
			    JOIN observations src ON src.sync_id = r.source_id AND src.deleted_at IS NULL
			    JOIN observations tgt ON tgt.sync_id = r.target_id AND tgt.deleted_at IS NULL
			    LEFT JOIN sessions src_s ON src_s.id = src.session_id
			    WHERE r.judgment_status NOT IN (?, ?)
			      AND ifnull(r.marked_by_actor, '') != ''
			      AND ifnull(r.marked_by_kind, '') != ''
			      AND coalesce(nullif(src.project, ''), src_s.project, '') = ?
			      AND NOT EXISTS (
			        SELECT 1 FROM sync_mutations sm
			        WHERE sm.target_key = ? AND sm.entity = ? AND sm.entity_key = r.sync_id AND sm.source = ?
			      )`,
			args: []any{JudgmentStatusOrphaned, JudgmentStatusPending, project, DefaultSyncTargetKey, SyncEntityRelation, SyncSourceLocal},
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

// EnsureEnrolledProjectSyncMutations repairs legacy enrolled-project journal
// entries before a sync operation reads them. A successful repair is memoized
// for this Store's lifetime; failures are returned to callers and retried by a
// later synchronization attempt.
func (s *Store) EnsureEnrolledProjectSyncMutations(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.repairMu.Lock()
	if s.repairDone {
		s.repairMu.Unlock()
		return nil
	}
	if inFlight := s.repairInFlight; inFlight != nil {
		s.repairMu.Unlock()
		select {
		case <-inFlight.done:
			return inFlight.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	inFlight := &enrolledProjectRepair{done: make(chan struct{})}
	s.repairInFlight = inFlight
	s.repairMu.Unlock()

	var err error
	if s.repairOperation != nil {
		err = s.repairOperation()
	} else {
		err = s.repairEnrolledProjectSyncMutations()
	}

	s.repairMu.Lock()
	inFlight.err = err
	if err == nil {
		s.repairDone = true
	}
	s.repairInFlight = nil
	close(inFlight.done)
	s.repairMu.Unlock()
	return err
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
	// Blank source identities are skipped, not enqueued: enqueueSyncMutationTx
	// rejects them and a single corrupt row would otherwise roll back the whole
	// backfill transaction. The predicate must stay identical to the COUNT in
	// projectNeedsBackfill and to isBlankSessionID.
	rows, err := s.queryItHook(tx, `
		SELECT id, project, directory, started_at, ended_at, summary
		FROM sessions
		WHERE project = ?
		  AND `+sqlSessionIDNotBlank("id")+`
		  AND NOT EXISTS (
			SELECT 1
			FROM sync_mutations sm
			WHERE sm.target_key = ?
			  AND sm.entity = ?
			  AND sm.entity_key = sessions.id
			  AND sm.source = ?
		  )
		ORDER BY started_at ASC, id ASC`,
		project, sqlWhitespaceTrimSet, DefaultSyncTargetKey, SyncEntitySession, SyncSourceLocal,
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

// backfillRelationSyncMutationsTx creates sync_mutations rows for non-orphaned
// relations that have no corresponding local sync_mutations row.
//
// This fills the cloud-journal gap described in issue #496: a relation can exist
// in memory_relations with no sync_mutations row and therefore never replicates.
//
// Design mirrors backfillObservationSyncMutationsTx exactly:
//   - Phase 1: collect all missing rows into a slice (close cursor first).
//   - Phase 2: insert, avoiding the SQLite cursor-open-during-write busy loop.
//
// The SELECT mirrors ExportRelationMutations' join/orphan-filter structure
// (join both observations, exclude orphaned status, exclude rows that already
// have a local upsert mutation), but scopes by source-observation project only.
// ExportRelationMutations additionally filters by tgt.project; the backfill
// intentionally omits that filter to avoid skipping cross-project edges where
// only the source belongs to this project.
func (s *Store) backfillRelationSyncMutationsTx(tx *sql.Tx, project string) error {
	// Only backfill fully-judged relations: exclude orphaned/pending and any row
	// that is missing marked_by_actor or marked_by_kind.  Cloud validation
	// (chunkcodec + server) hard-rejects mutations without those fields (HTTP 400),
	// so enqueueing them would block the entire sync batch.
	// This predicate must stay identical to the COUNT in projectNeedsBackfill.
	rows, err := s.queryItHook(tx, `
		SELECT r.sync_id, r.source_id, r.target_id, r.relation, r.reason, r.evidence, r.confidence,
		       r.judgment_status, r.marked_by_actor, r.marked_by_kind, r.marked_by_model,
		       r.session_id,
		       coalesce(nullif(src.project, ''), src_s.project, ''),
		       r.created_at, r.updated_at
		FROM memory_relations r
		JOIN observations src ON src.sync_id = r.source_id AND src.deleted_at IS NULL
		JOIN observations tgt ON tgt.sync_id = r.target_id AND tgt.deleted_at IS NULL
		LEFT JOIN sessions src_s ON src_s.id = src.session_id
		WHERE r.judgment_status NOT IN (?, ?)
		  AND ifnull(r.marked_by_actor, '') != ''
		  AND ifnull(r.marked_by_kind, '') != ''
		  AND coalesce(nullif(src.project, ''), src_s.project, '') = ?
		  AND NOT EXISTS (
		    SELECT 1 FROM sync_mutations sm
		    WHERE sm.target_key = ?
		      AND sm.entity = ?
		      AND sm.entity_key = r.sync_id
		      AND sm.source = ?
		  )
		ORDER BY r.created_at ASC, r.sync_id ASC`,
		JudgmentStatusOrphaned, JudgmentStatusPending,
		project,
		DefaultSyncTargetKey, SyncEntityRelation, SyncSourceLocal,
	)
	if err != nil {
		return err
	}

	// Phase 1: collect into memory before any INSERT to avoid cursor-open-during-write.
	var pending []syncRelationPayload
	for rows.Next() {
		var p syncRelationPayload
		if err := rows.Scan(
			&p.SyncID, &p.SourceID, &p.TargetID, &p.Relation, &p.Reason, &p.Evidence, &p.Confidence,
			&p.JudgmentStatus, &p.MarkedByActor, &p.MarkedByKind, &p.MarkedByModel,
			&p.SessionID, &p.Project, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return closeRowsWithError(rows, err)
		}
		pending = append(pending, p)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Phase 2: insert now that the read cursor is closed.
	for _, p := range pending {
		if err := s.enqueueSyncMutationTx(tx, SyncEntityRelation, p.SyncID, SyncOpUpsert, p); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) enqueueSyncMutationTx(tx *sql.Tx, entity, entityKey, op string, payload any) error {
	if entity == SyncEntitySession && strings.TrimSpace(entityKey) == "" {
		return ErrSessionIDRequired
	}
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

// RescueOwnershipCommand is the repair an operator can always run, whatever the
// server authorization configuration is: it reaches the local store directly and
// never goes through the HTTP endpoint. Every ownership error names it, so the
// failure carries its own remedy instead of pointing at a changelog.
const RescueOwnershipCommand = "engram projects rescue-ownership"

// rescueOwnershipHint renders the exact repair for one session.
func rescueOwnershipHint(sessionID string) string {
	return fmt.Sprintf("%s --project <name> --session %s", RescueOwnershipCommand, sessionID)
}

// sessionOwnershipTx reads one session's ownership. found reports whether the
// session row exists; project is empty when the row carries NULL or blank
// ownership, which are the two legacy shapes that identify no project.
func sessionOwnershipTx(tx *sql.Tx, sessionID string) (project string, found bool, err error) {
	var raw sql.NullString
	err = tx.QueryRow(`SELECT project FROM sessions WHERE id = ?`, sessionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	normalized, _ := NormalizeProject(strings.TrimSpace(raw.String))
	return normalized, true, nil
}

// foreignRecordOwnerTx returns the first project, other than exclude, that owns
// a record parented by this session. It is how a would-be adoption discovers it
// would split an existing record away from its session. kind reads as a noun
// phrase inside the error message, so it carries its own article.
func foreignRecordOwnerTx(tx *sql.Tx, sessionID, exclude string) (string, string, error) {
	for _, source := range []struct{ kind, query string }{
		{"an observation", `SELECT project FROM observations WHERE session_id = ? AND ifnull(trim(project), '') <> '' AND deleted_at IS NULL`},
		{"a prompt", `SELECT project FROM user_prompts WHERE session_id = ? AND ifnull(trim(project), '') <> ''`},
	} {
		rows, err := tx.Query(source.query, sessionID)
		if err != nil {
			return "", "", err
		}
		owner, findErr := func() (string, error) {
			defer rows.Close()
			for rows.Next() {
				var raw sql.NullString
				if err := rows.Scan(&raw); err != nil {
					return "", err
				}
				owned, _ := NormalizeProject(strings.TrimSpace(raw.String))
				if owned != "" && owned != exclude {
					return owned, nil
				}
			}
			return "", rows.Err()
		}()
		if findErr != nil {
			return "", "", findErr
		}
		if owner != "" {
			return source.kind, owner, nil
		}
	}
	return "", "", nil
}

// resolveWriteProjectTx settles the project one write lands in, and repairs the
// parent session's ownership on the way through.
//
// A database upgraded from the schema where sessions.project was nullable still
// carries sessions that identify no project. Demanding ownership those rows
// never had would make every later write to them fail permanently, so ownership
// is established forward instead: when the write already knows its project, the
// unowned session adopts it inside this same transaction and is journaled like
// any other ownership move. The record and its session therefore agree, which is
// the invariant the hard gate was reaching for, without the lockout.
//
// Adoption is refused in the one genuinely ambiguous case — an unowned session
// that already parents a record owned by a different project — because claiming
// it there would split that record from its session.
func (s *Store) resolveWriteProjectTx(tx *sql.Tx, sessionID, requested string) (string, error) {
	requested, _ = NormalizeProject(strings.TrimSpace(requested))
	sessionProject, found, err := sessionOwnershipTx(tx, sessionID)
	if err != nil {
		return "", err
	}

	if requested == "" {
		// Nothing on the request to fall back on: the session is the only
		// source of identity left.
		if !found {
			return "", fmt.Errorf("%w: session %q does not exist, so the write has no project to inherit", ErrProjectRequired, sessionID)
		}
		if sessionProject == "" {
			return "", fmt.Errorf(
				"%w: session %q carries no project ownership and the write supplied none; assign ownership with: %s",
				ErrProjectRequired, sessionID, rescueOwnershipHint(sessionID),
			)
		}
		return sessionProject, nil
	}

	// The session already agrees, or does not exist yet (callers create it
	// separately); nothing to repair.
	if !found || sessionProject == requested {
		return requested, nil
	}
	if sessionProject != "" {
		// An owned session keeps its ownership; the caller decides whether a
		// mismatch is an error. This preserves existing behavior.
		return requested, nil
	}

	kind, owner, err := foreignRecordOwnerTx(tx, sessionID, requested)
	if err != nil {
		return "", err
	}
	if owner != "" {
		return "", fmt.Errorf(
			"%w: session %q carries no project ownership but already parents %s owned by %q, so it cannot adopt %q; resolve it explicitly with: %s",
			ErrProjectOwnershipAmbiguous, sessionID, kind, owner, requested, rescueOwnershipHint(sessionID),
		)
	}

	if err := s.adoptSessionOwnershipTx(tx, sessionID, requested); err != nil {
		return "", err
	}
	return requested, nil
}

// adoptSessionOwnershipTx claims an unowned session for project and journals the
// move, so the cloud converges on the same ownership the local store now holds.
func (s *Store) adoptSessionOwnershipTx(tx *sql.Tx, sessionID, project string) error {
	res, err := s.execHook(tx,
		`UPDATE sessions SET project = ? WHERE id = ? AND ifnull(trim(project), '') = ''`,
		project, sessionID,
	)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err != nil || affected == 0 {
		return err
	}
	var payload syncSessionPayload
	err = tx.QueryRow(
		`SELECT id, project, directory, started_at, ended_at, summary FROM sessions WHERE id = ?`, sessionID,
	).Scan(&payload.ID, &payload.Project, &payload.Directory, &payload.StartedAt, &payload.EndedAt, &payload.Summary)
	if err != nil {
		return err
	}
	_, err = s.enqueueMissingLocalMutationTx(tx, SyncEntitySession, payload.ID, payload)
	return err
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
		entityKey := mutation.EntityKey
		if strings.TrimSpace(payload.ID) == "" && strings.TrimSpace(entityKey) != "" {
			payload.ID = entityKey
		}
		if err := validateSessionMutationIdentity(payload.ID, entityKey); err != nil {
			return fmt.Errorf("%w: %v", ErrPulledSessionIdentityInvalid, err)
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

// pulledSessionDeadLetterSyncID derives the identity of a quarantined pulled
// session mutation from the mutation itself, never from its position in the pull.
//
// The row it identifies is the only record that remote data was discarded, and
// the insert resolves conflicts with ON CONFLICT(sync_id) DO UPDATE, so the
// identity carries two obligations at once. Two different dropped mutations must
// land on different rows, or the second silently erases the first and
// skip-plus-evidence degrades into the silent drop it exists to prevent. One
// dropped mutation redelivered must land on the same row, or a single discarded
// session accumulates an evidence row per delivery and overstates the loss.
//
// The pull sequence satisfies neither obligation. ApplyPulledChunk overwrites it
// with the local cursor position, so it describes when a mutation was applied
// rather than what was applied: two unrelated mutations can carry the same value
// (including zero), and one mutation redelivered inside a differently hashed
// chunk carries a new one. The mutation's own entity, key, operation and payload
// are what actually distinguish dropped data, so they are what the identity
// hashes. Each field is length-prefixed so no field content can imitate a
// separator and forge a collision.
func pulledSessionDeadLetterSyncID(targetKey string, mutation SyncMutation) string {
	digest := sha256.New()
	for _, field := range []string{targetKey, mutation.Entity, mutation.EntityKey, mutation.Op, mutation.Payload} {
		digest.Write([]byte(strconv.Itoa(len(field))))
		digest.Write([]byte(":"))
		digest.Write([]byte(field))
	}
	return "pulled-session-" + hex.EncodeToString(digest.Sum(nil))
}

func (s *Store) deadLetterPulledSessionIdentityTx(tx *sql.Tx, targetKey string, mutation SyncMutation) error {
	syncID := pulledSessionDeadLetterSyncID(targetKey, mutation)
	project := strings.TrimSpace(mutation.Project)
	if project == "" {
		var payload syncSessionPayload
		if err := decodeSyncPayload([]byte(mutation.Payload), &payload); err == nil {
			project = strings.TrimSpace(payload.Project)
		}
	}
	project, _ = NormalizeProject(project)
	scopeClass := "target_scoped"
	if project != "" {
		scopeClass = "scoped"
	}
	_, err := s.execHook(tx, `
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, remote_seq, entity_key, op, reason_code, project, scope_class, apply_status, retry_count, first_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'dead', 0, datetime('now'))
		ON CONFLICT(sync_id) DO UPDATE SET
			entity = excluded.entity, payload = excluded.payload, target_key = excluded.target_key,
			remote_seq = excluded.remote_seq, entity_key = excluded.entity_key, op = excluded.op,
			reason_code = excluded.reason_code, project = excluded.project, scope_class = excluded.scope_class,
			apply_status = 'dead', last_attempted_at = datetime('now')`,
		syncID, mutation.Entity, mutation.Payload, targetKey, mutation.Seq, mutation.EntityKey, mutation.Op, SyncSessionIdentityInvalidReasonCode, project, scopeClass,
	)
	if err != nil {
		return fmt.Errorf("write invalid pulled session evidence: %w", err)
	}
	return nil
}

// relationApplyCleanupSQL removes the sync_apply_deferred rows that belong to a
// relation which has just applied successfully. It runs inside the apply write
// transaction on every successful relation upsert, so the test suite plans it
// directly to keep it off a full table scan.
//
// A row belongs to this relation when the relation it is about is this one. Two
// clauses say that, and both are index-satisfiable:
//
//   - The disjunction locates the row. It can be found under either identity it
//     may be stored with: as a primary-key point delete on sync_id, which is
//     where this relation's retry state lives — a 'deferred' row is proven to be
//     keyed on the relation's own sync_id, because applyRelationUpsertTx
//     validates the wire contract before it ever reports a missing endpoint, and
//     rows written by the old scheme are keyed the same way — or as an indexed
//     lookup on payload_sync_id, which is how evidence keyed on a discarded
//     mutation's own material is reached. Both terms are point lookups on the
//     relation identity, so the statement stays off a table scan no matter which
//     one the planner drives.
//   - The IN clause then keeps only the rows that really are about this
//     relation: ones whose payload names it, and ones that carry no identity of
//     their own and so are only known by the key they are stored under.
//
// The blank half of that IN clause is only sound because a blank payload_sync_id
// has exactly one meaning: the row's payload carries no relation identity at all,
// because it does not decode or names no sync_id. It can never mean "an identity
// exists in the payload but has not been derived yet", because the migration
// derives it unconditionally on every store open rather than only in the open
// that added the column — see the backfill in migrate(). Making that backfill
// conditional again, or gating it behind a schema-version marker, would restore
// the second meaning and with it the evidence loss this comment describes: a
// legacy row keyed on relation X whose payload really describes relation Y would
// satisfy both clauses and be deleted by a successful apply of X.
//
// A row's entity_key is deliberately never consulted, and a key that merely
// names this relation is not enough on its own. A dead row stores the discarded
// mutation's raw entity_key and may be stored under it, and that key may be
// blank or may name a completely different relation — the disagreement is one of
// the reasons the row is dead. Deleting on that match let a successful apply of
// relation X destroy the evidence that a mutation describing relation Y had been
// dropped, which is the evidence loss this table exists to prevent.
//
// Nothing is scoped by target_key or project, because a relation sync_id is
// global: rows for one relation collapse onto a single key regardless of which
// target delivered them, and the relation now exists locally for every scope.
//
// Arguments: entity, relation sync_id, relation sync_id, relation sync_id.
const relationApplyCleanupSQL = `
	DELETE FROM sync_apply_deferred
	WHERE entity = ?
	  AND (sync_id = ? OR payload_sync_id = ?)
	  AND payload_sync_id IN ('', ?)
`

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
	if mutation.Op != SyncOpUpsert {
		return fmt.Errorf("%w: unsupported relation operation %q", ErrApplyDead, mutation.Op)
	}

	// Step 1: decode payload.
	var p syncRelationPayload
	if err := decodeSyncPayload([]byte(mutation.Payload), &p); err != nil {
		return fmt.Errorf("%w: decode relation payload: %v", ErrApplyDead, err)
	}

	p.SyncID = strings.TrimSpace(p.SyncID)
	p.SourceID = strings.TrimSpace(p.SourceID)
	p.TargetID = strings.TrimSpace(p.TargetID)
	p.Relation = strings.TrimSpace(p.Relation)
	p.JudgmentStatus = strings.TrimSpace(p.JudgmentStatus)
	p.Project = strings.TrimSpace(p.Project)
	if p.MarkedByActor != nil {
		actor := strings.TrimSpace(*p.MarkedByActor)
		p.MarkedByActor = &actor
	}
	if p.MarkedByKind != nil {
		kind := strings.TrimSpace(*p.MarkedByKind)
		p.MarkedByKind = &kind
	}

	// Step 1b: validate the relation wire contract before classifying an FK miss.
	// A malformed relation is terminal evidence, never a retryable dependency.
	missing := make([]string, 0, 5)
	if p.SyncID == "" {
		missing = append(missing, "sync_id")
	}
	if p.SourceID == "" {
		missing = append(missing, "source_id")
	}
	if p.TargetID == "" {
		missing = append(missing, "target_id")
	}
	if p.Relation == "" {
		missing = append(missing, "relation")
	}
	if p.JudgmentStatus == "" {
		missing = append(missing, "judgment_status")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: relation payload missing required fields: %s", ErrApplyDead, strings.Join(missing, ", "))
	}
	if entityKey := strings.TrimSpace(mutation.EntityKey); entityKey == "" || entityKey != p.SyncID {
		return fmt.Errorf("%w: relation entity_key %q does not match payload sync_id %q", ErrApplyDead, mutation.EntityKey, p.SyncID)
	}

	// Step 2: FK precondition — both observations must exist locally (by sync_id).
	var obsCount int
	if err := tx.QueryRow(
		`SELECT count(*) FROM observations WHERE sync_id IN (?, ?)`,
		p.SourceID, p.TargetID,
	).Scan(&obsCount); err != nil {
		return fmt.Errorf("applyRelationUpsertTx: check observations: %w", err)
	}
	requiredObservations := 2
	if p.SourceID == p.TargetID {
		requiredObservations = 1
	}
	if obsCount < requiredObservations {
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

	// Step 4: clean up the rows this relation left behind (its pending retry
	// state and any evidence about this same relation).
	if _, err := s.execHook(tx, relationApplyCleanupSQL,
		SyncEntityRelation, p.SyncID, p.SyncID, p.SyncID); err != nil {
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
		`SELECT `+observationSelectColumns+`
		 FROM observations WHERE id = ? AND deleted_at IS NULL`, id,
	)
	var o Observation
	if err := scanObservationRow(row, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Store) getObservationBySyncIDTx(tx *sql.Tx, syncID string, includeDeleted bool) (*Observation, error) {
	query := `SELECT ` + observationSelectColumns + `
		 FROM observations WHERE sync_id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	query += ` ORDER BY id DESC LIMIT 1`
	row := tx.QueryRow(query, syncID)
	var o Observation
	if err := scanObservationRow(row, &o); err != nil {
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
	if err := validateSessionID(payload.ID); err != nil {
		return err
	}
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
	sessionID := payload.ID
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if _, err := s.execHook(tx, `DELETE FROM user_prompts WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	_, err := s.execHook(tx, `DELETE FROM sessions WHERE id = ?`, sessionID)
	return err
}

// isBlankSessionID is the single authority on what "blank session identity"
// means. Everything else — the Go guards and the SQL predicates built from
// sqlWhitespaceTrimSet — must agree with it byte for byte.
func isBlankSessionID(id string) bool {
	return strings.TrimSpace(id) == ""
}

func validateSessionID(id string) error {
	if isBlankSessionID(id) {
		return ErrSessionIDRequired
	}
	return nil
}

// sqlWhitespaceTrimSet holds every rune strings.TrimSpace strips, so a SQL
// predicate can express exactly the rule isBlankSessionID applies in Go.
//
// SQLite's one-argument trim(X) only strips U+0020. Relying on it split the
// blank rule in two: a tab-, newline-, or CR-only legacy identity looked
// non-blank to SQL and blank to Go, so doctor's source-row scan skipped the
// corrupt row while the backfill SELECT kept it and enqueueSyncMutationTx then
// aborted the whole transaction, rolling back every valid session batched with
// it. Pass this value as the second argument of SQLite's trim(X, Y) — see
// sqlSessionIDBlank / sqlSessionIDNotBlank — to keep both sides on one rule.
var sqlWhitespaceTrimSet = buildSQLWhitespaceTrimSet()

func buildSQLWhitespaceTrimSet() string {
	var b strings.Builder
	appendRange := func(lo, hi, stride rune) {
		for r := lo; r <= hi; r += stride {
			b.WriteRune(r)
		}
	}
	for _, r := range unicode.White_Space.R16 {
		appendRange(rune(r.Lo), rune(r.Hi), rune(r.Stride))
	}
	for _, r := range unicode.White_Space.R32 {
		appendRange(rune(r.Lo), rune(r.Hi), rune(r.Stride))
	}
	return b.String()
}

// sqlSessionIDBlank and sqlSessionIDNotBlank render the blank-identity
// predicate for a session-identity column. Both expect sqlWhitespaceTrimSet to
// be bound as the next positional argument of the enclosing statement.
func sqlSessionIDBlank(column string) string {
	return "trim(" + column + ", ?) = ''"
}

func sqlSessionIDNotBlank(column string) string {
	return "trim(" + column + ", ?) != ''"
}

func validateSessionMutationIdentity(payloadID, entityKey string) error {
	if err := validateSessionID(payloadID); err != nil {
		return fmt.Errorf("session mutation payload id: %w", err)
	}
	if err := validateSessionID(entityKey); err != nil {
		return fmt.Errorf("session mutation entity_key: %w", err)
	}
	if payloadID != entityKey {
		return fmt.Errorf("session mutation entity_key %q does not match payload id %q", entityKey, payloadID)
	}
	return nil
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
			 VALUES (?, ?, ?, ?, ?, ?, CAST(? AS TEXT), ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
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
		 SET session_id = ?, type = ?, title = ?, content = ?, tool_name = ?, project = CAST(? AS TEXT), scope = ?, topic_key = ?, normalized_hash = ?, revision_count = ?, duplicate_count = ?, last_seen_at = ?, created_at = ?, updated_at = ?, deleted_at = NULL
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

func parseObservationTime(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	formats := []string{"2006-01-02 15:04:05", time.RFC3339, time.RFC3339Nano, "2006-01-02"}
	for _, layout := range formats {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}

type observationScanner interface {
	Scan(dest ...any) error
}

func scanObservationRow(scanner observationScanner, o *Observation) error {
	return scanner.Scan(
		&o.ID, &o.SyncID, &o.SessionID, &o.Type, &o.Title, &o.Content,
		&o.ToolName, &o.Project, &o.Scope, &o.TopicKey, &o.RevisionCount, &o.DuplicateCount, &o.LastSeenAt, &o.ReviewAfter,
		&o.Pinned, &o.CreatedAt, &o.UpdatedAt, &o.DeletedAt,
	)
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
		if err := scanObservationRow(rows, &o); err != nil {
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
			pinned     BOOLEAN NOT NULL DEFAULT 0,
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
			last_seen_at, pinned, created_at, updated_at, deleted_at
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
			0,
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
	switch v {
	case "personal", "global":
		return v
	default:
		return "project"
	}
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
	n := projectpkg.CanonicalizeProjectName(project)
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
		// Double interior double-quotes: FTS5 escapes a literal " inside a
		// quoted phrase by doubling it (""). Without this, `hello"world`
		// becomes `"hello"world"` — an unterminated string literal that crashes
		// the query with "SQL logic error: unterminated string". See #574.
		w = strings.ReplaceAll(w, `"`, `""`)
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
// entity is NOT 'session', 'observation', or 'prompt'. Used by integration
// tests to verify the enrollment gate: an UNENROLLED project must never enqueue
// relation sync mutations (the enqueue in JudgeBySemantic/JudgeRelation is
// guarded by an enrollment check). The test that calls this uses an unenrolled
// store, so the count must remain zero.
//
// Note: relation sync mutations ARE valid for enrolled projects (#313/#379/#383
// enabled cloud relation sync; #496 extends it with backfill). This function
// is not a blanket "relations are local-only" check — it is an enrollment-gate
// regression guard scoped to the unenrolled test context that uses it.
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

// ReplayDeferred retries every deferred row, including legacy-unscoped rows. It is
// reserved for deliberate administrative operations such as conflict recovery.
func (s *Store) ReplayDeferred() (ReplayDeferredResult, error) {
	return s.ReplayDeferredForScope("", "")
}

func (s *Store) ListDeferredProjectsForTarget(targetKey string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT project
		FROM sync_apply_deferred
		WHERE target_key = ?
		  AND scope_class = 'scoped'
		  AND apply_status = 'deferred'
		  AND project != ''
		ORDER BY project
	`, normalizeSyncTargetKey(targetKey))
	if err != nil {
		return nil, fmt.Errorf("ListDeferredProjectsForTarget: query: %w", err)
	}
	defer rows.Close()

	projects := make([]string, 0)
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			return nil, fmt.Errorf("ListDeferredProjectsForTarget: scan: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListDeferredProjectsForTarget: rows: %w", err)
	}
	return projects, nil
}

// ReplayDeferredForScope retries deferred rows for one sync target and optional
// project. An empty project includes all safely target-scoped rows for that target;
// an empty target is reserved for ReplayDeferred's administrative global replay.
// Legacy-unscoped rows are excluded whenever a target or project scope is supplied.
//
// It retries rows with apply_status='deferred'
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
func (s *Store) ReplayDeferredForScope(targetKey, project string) (result ReplayDeferredResult, err error) {
	const limit = 50
	const deadThreshold = 5
	targetKey = strings.TrimSpace(targetKey)
	project, _ = NormalizeProject(strings.TrimSpace(project))

	query := `
		SELECT sync_id, entity, payload, entity_key, op, retry_count
		FROM sync_apply_deferred
		WHERE apply_status = 'deferred'`
	args := []any{}
	if targetKey != "" {
		query += ` AND target_key = ? AND scope_class != 'legacy_unscoped'`
		args = append(args, normalizeSyncTargetKey(targetKey))
	}
	if project != "" {
		query += ` AND project = ? AND scope_class = 'scoped'`
		args = append(args, project)
	}
	query += ` ORDER BY first_seen_at LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return result, fmt.Errorf("ReplayDeferred: list deferred: %w", err)
	}

	type deferredRow struct {
		syncID     string
		entity     string
		payload    string
		entityKey  string
		op         string
		retryCount int
	}

	var pending []deferredRow
	for rows.Next() {
		var r deferredRow
		if err := rows.Scan(&r.syncID, &r.entity, &r.payload, &r.entityKey, &r.op, &r.retryCount); err != nil {
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
		// Rows written before entity_key and op were stored carry the mutation's
		// entity key as their row key and no operation, so fall back to those.
		entityKey := row.entityKey
		if entityKey == "" {
			entityKey = row.syncID
		}
		op := row.op
		if op == "" {
			op = SyncOpUpsert
		}
		mut := SyncMutation{
			Entity:    row.entity,
			EntityKey: entityKey,
			Op:        op,
			Payload:   row.payload,
			Source:    SyncSourceRemote,
			TargetKey: targetKey,
			Project:   project,
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

// CountDeferredAndDead returns global administrative totals, including legacy
// unscoped rows that normal scoped imports intentionally leave untouched.
func (s *Store) CountDeferredAndDead() (deferred, dead int, err error) {
	return s.CountDeferredAndDeadForScope("", "")
}

// CountDeferredAndDeadForScope returns queue totals for an optional target and
// project. Scoped totals exclude legacy-unscoped rows; empty filters return the
// global administrative totals.
func (s *Store) CountDeferredAndDeadForScope(targetKey, project string) (deferred, dead int, err error) {
	targetKey = strings.TrimSpace(targetKey)
	project, _ = NormalizeProject(strings.TrimSpace(project))
	query := `
		SELECT apply_status, count(*)
		FROM sync_apply_deferred
		WHERE apply_status IN ('deferred', 'dead')`
	args := []any{}
	if targetKey != "" {
		query += ` AND target_key = ? AND scope_class != 'legacy_unscoped'`
		args = append(args, normalizeSyncTargetKey(targetKey))
	}
	if project != "" {
		query += ` AND project = ? AND scope_class = 'scoped'`
		args = append(args, project)
	}
	query += ` GROUP BY apply_status`
	rows, err := s.db.Query(query, args...)
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
		SELECT sync_id, entity, payload, target_key, project, scope_class, remote_seq, entity_key, op, reason_code, apply_status, retry_count,
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
		SELECT sync_id, entity, payload, target_key, project, scope_class, remote_seq, entity_key, op, reason_code, apply_status, retry_count,
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
		&r.SyncID, &r.Entity, &rawPayload, &r.TargetKey, &r.Project, &r.ScopeClass, &r.RemoteSeq, &r.EntityKey, &r.Op, &r.ReasonCode, &r.ApplyStatus, &r.RetryCount,
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
