package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ─── Sentinel errors (Phase 4 / C.6) ─────────────────────────────────────────

// ErrSemanticRunnerRequired is returned by ScanProject when ScanOptions.Semantic
// is true but ScanOptions.Runner is nil.
var ErrSemanticRunnerRequired = errors.New("semantic scan requires a non-nil Runner")

// ErrSemanticPromptBuilderRequired is returned by ScanProject when
// ScanOptions.Semantic is true but ScanOptions.BuildPrompt is nil.
var ErrSemanticPromptBuilderRequired = errors.New("semantic scan requires a non-nil BuildPrompt function")

// ─── Relation vocabulary (locked) ─────────────────────────────────────────────

// Valid relation type values. Type compatibility is NOT enforced in Phase 1;
// the agent does that judgment.
const (
	RelationPending      = "pending"
	RelationRelated      = "related"
	RelationCompatible   = "compatible"
	RelationScoped       = "scoped"
	RelationConflictsWith = "conflicts_with"
	RelationSupersedes   = "supersedes"
	RelationNotConflict  = "not_conflict"
)

// Valid judgment_status values.
const (
	JudgmentStatusPending  = "pending"
	JudgmentStatusJudged   = "judged"
	JudgmentStatusOrphaned = "orphaned"
	JudgmentStatusIgnored  = "ignored"
)

// validRelationVerbs is the locked set of relation verbs that mem_judge accepts.
// "pending" is NOT in this set — it is the default, not a verdict.
var validRelationVerbs = map[string]bool{
	RelationRelated:       true,
	RelationCompatible:    true,
	RelationScoped:        true,
	RelationConflictsWith: true,
	RelationSupersedes:    true,
	RelationNotConflict:   true,
}

// isValidRelationVerb returns true if v is an accepted mem_judge relation verb.
func isValidRelationVerb(v string) bool {
	return validRelationVerbs[v]
}

// ─── Types ────────────────────────────────────────────────────────────────────

// CandidateOptions controls the FindCandidates query.
type CandidateOptions struct {
	// Project filters candidates to the same project as the saved observation.
	Project string
	// Scope filters candidates to the same scope as the saved observation.
	Scope string
	// Type is reserved for Phase 2 type-compatibility filtering; NOT enforced Phase 1.
	Type string
	// Limit caps the number of candidates returned. Default 3 when nil or <=0.
	Limit int
	// BM25Floor is the minimum BM25 score (negative; closer to 0 = better match).
	// Candidates below the floor are excluded. Default -2.0 when nil.
	//
	// Use a pointer so that an explicit 0.0 (very strict — nothing passes) is
	// distinguishable from the zero value (which previously collided with the
	// default sentinel). nil means "use the default (-2.0)".
	BM25Floor *float64
	// SkipInsert controls whether FindCandidates inserts pending relation rows.
	// When true, candidates are returned but NO rows are written to memory_relations.
	// Default false preserves the existing behavior (rows are inserted).
	SkipInsert bool
}

// ─── Phase 3 types ────────────────────────────────────────────────────────────

// ListRelationsOptions controls ListRelations and CountRelations queries.
type ListRelationsOptions struct {
	// Project filters by the project of the source OR target observation (via JOIN).
	// Empty means no project filter (return all).
	Project string
	// Status filters by judgment_status. Empty means no status filter.
	Status string
	// SinceTime filters to rows created_at >= SinceTime. Zero value means no filter.
	SinceTime time.Time
	// Limit caps the number of rows returned. 0 or negative means no limit.
	Limit int
	// Offset is the pagination offset.
	Offset int
}

// RelationListItem represents a single row in a ListRelations result,
// enriched with observation titles via JOIN (no full Relation struct).
type RelationListItem struct {
	ID             int64  `json:"id"`
	SyncID         string `json:"sync_id"`
	Relation       string `json:"relation"`
	JudgmentStatus string `json:"judgment_status"`
	SourceID       string `json:"source_id"`
	SourceTitle    string `json:"source_title"`
	TargetID       string `json:"target_id"`
	TargetTitle    string `json:"target_title"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// RelationStats holds aggregate counts of relations for a project.
type RelationStats struct {
	Project         string         `json:"project"`
	ByRelation      map[string]int `json:"by_relation"`
	ByJudgmentStatus map[string]int `json:"by_judgment_status"`
	DeferredCount   int            `json:"deferred"`
	DeadCount       int            `json:"dead"`
}

// DeferredRow represents a row in sync_apply_deferred with the payload decoded.
type DeferredRow struct {
	SyncID          string         `json:"sync_id"`
	Entity          string         `json:"entity"`
	Payload         map[string]any `json:"payload,omitempty"`
	PayloadRaw      string         `json:"payload_raw"`
	PayloadValid    bool           `json:"payload_valid"`
	ApplyStatus     string         `json:"apply_status"`
	RetryCount      int            `json:"retry_count"`
	LastError       *string        `json:"last_error,omitempty"`
	LastAttemptedAt *string        `json:"last_attempted_at,omitempty"`
	FirstSeenAt     string         `json:"first_seen_at"`
}

// ScanResult holds the output of a ScanProject call.
type ScanResult struct {
	Project            string `json:"project"`
	Inspected          int    `json:"inspected"`
	CandidatesFound    int    `json:"candidates_found"`
	AlreadyRelated     int    `json:"already_related"`
	RelationsInserted  int    `json:"inserted"`
	Capped             bool   `json:"capped"`
	DryRun             bool   `json:"dry_run"`

	// Semantic counters — populated only when ScanOptions.Semantic is true.
	// Zero-value is safe for existing JSON consumers.
	SemanticJudged  int `json:"semantic_judged"`
	SemanticSkipped int `json:"semantic_skipped"`
	SemanticErrors  int `json:"semantic_errors"`
}

// ObservationSnippet carries the fields needed by BuildPrompt to construct an
// LLM comparison prompt without importing internal/llm from this package.
type ObservationSnippet struct {
	ID      int64
	SyncID  string
	Title   string
	Type    string
	Content string
}

// ScanOptions controls a ScanProject call.
type ScanOptions struct {
	// Project is required — scopes the observation walk.
	Project string
	// Since filters observations to created_at >= Since. Zero value means no filter.
	Since time.Time
	// Apply controls whether new relation rows are inserted.
	// When false (dry-run, default), candidates are reported but not written.
	Apply bool
	// MaxInsert caps the number of new relation rows inserted in a single Apply run.
	// Default 100 when 0 or negative.
	MaxInsert int

	// Semantic controls whether the worker pool LLM-judge step runs.
	// When false (default), ScanProject behaves exactly as Phase 3.
	Semantic bool
	// Concurrency is the worker pool size for semantic calls. Default 5 if 0.
	Concurrency int
	// TimeoutPerCall is the per-pair context timeout for runner.Compare.
	// Default 60s if zero.
	TimeoutPerCall time.Duration
	// MaxSemantic caps the number of LLM calls in a single semantic scan. Default 100 if 0.
	MaxSemantic int
	// Runner is the SemanticRunner used for LLM comparison. Required when Semantic=true.
	Runner SemanticRunner
	// BuildPrompt constructs the LLM prompt for a given (a, b) pair.
	// Required when Semantic=true.
	BuildPrompt func(a, b ObservationSnippet) string
}

// JudgeBySemanticParams holds the inputs for JudgeBySemantic.
type JudgeBySemanticParams struct {
	// SourceID is the TEXT sync_id of the source observation (required).
	SourceID string
	// TargetID is the TEXT sync_id of the target observation (required).
	TargetID string
	// Relation is the verdict verb (required); must be in validRelationVerbs.
	// Passing "not_conflict" is a no-op: no row is inserted and no error is returned.
	Relation string
	// Confidence is the LLM's self-reported confidence score [0.0, 1.0].
	Confidence float64
	// Reasoning is the LLM's short explanation.
	Reasoning string
	// Model is the LLM model identifier. Stored as marked_by_model.
	Model string
}

// ListDeferredOptions controls ListDeferred queries.
type ListDeferredOptions struct {
	// Status filters by apply_status. Empty means no status filter.
	Status string
	// Limit caps the number of rows returned. 0 or negative means no limit.
	Limit int
	// Offset is the pagination offset.
	Offset int
}

// Candidate represents a potential conflict candidate surfaced by FindCandidates.
type Candidate struct {
	// ID is the integer primary key of the candidate observation.
	ID int64
	// SyncID is the TEXT sync_id of the candidate observation.
	SyncID string
	// Title is the candidate's title.
	Title string
	// Type is the candidate's observation type.
	Type string
	// TopicKey is the candidate's topic_key (may be nil).
	TopicKey *string
	// Score is the FTS5 BM25 rank (negative; closer to 0 = better match).
	Score float64
	// JudgmentID is the sync_id of the pending memory_relations row created
	// for this (source, candidate) pair.
	JudgmentID string
}

// Relation represents a row in memory_relations.
type Relation struct {
	ID                    int64    `json:"id"`
	SyncID                string   `json:"sync_id"`
	SourceID              string   `json:"source_id"`
	TargetID              string   `json:"target_id"`
	Relation              string   `json:"relation"`
	Reason                *string  `json:"reason,omitempty"`
	Evidence              *string  `json:"evidence,omitempty"`
	Confidence            *float64 `json:"confidence,omitempty"`
	JudgmentStatus        string   `json:"judgment_status"`
	MarkedByActor         *string  `json:"marked_by_actor,omitempty"`
	MarkedByKind          *string  `json:"marked_by_kind,omitempty"`
	MarkedByModel         *string  `json:"marked_by_model,omitempty"`
	SessionID             *string  `json:"session_id,omitempty"`
	CreatedAt             string   `json:"created_at"`
	UpdatedAt             string   `json:"updated_at"`

	// Annotation fields — populated by GetRelationsForObservations via LEFT JOIN.
	// Excluded from JSON output (used only for in-process annotation building).
	// REQ-005, REQ-012 | Design §7, §8.
	SourceIntID     int64  `json:"-"` // integer primary key of source observation
	SourceTitle     string `json:"-"` // title of source observation; empty if missing/deleted
	SourceMissing   bool   `json:"-"` // true if source is soft-deleted or not found
	TargetIntID     int64  `json:"-"` // integer primary key of target observation
	TargetTitle     string `json:"-"` // title of target observation; empty if missing/deleted
	TargetMissing   bool   `json:"-"` // true if target is soft-deleted or not found
}

// ObservationRelations groups relations for a single observation, split by role.
type ObservationRelations struct {
	// AsSource holds relations where this observation is source_id.
	AsSource []Relation
	// AsTarget holds relations where this observation is target_id.
	AsTarget []Relation
}

// SaveRelationParams holds the inputs for SaveRelation.
type SaveRelationParams struct {
	// SyncID is the unique identifier for this relation row (format: rel-<16hex>).
	SyncID   string
	// SourceID is the TEXT sync_id of the source observation.
	SourceID string
	// TargetID is the TEXT sync_id of the target observation.
	TargetID string
}

// JudgeRelationParams holds the inputs for JudgeRelation.
type JudgeRelationParams struct {
	// JudgmentID is the sync_id of the relation row to update (required).
	JudgmentID    string
	// Relation is the verdict verb (required); must be one of validRelationVerbs.
	Relation      string
	// Reason is an optional free-text explanation.
	Reason        *string
	// Evidence is optional free-form JSON or text evidence.
	Evidence      *string
	// Confidence is optional 0..1 confidence score.
	Confidence    *float64
	// MarkedByActor is the actor identifier (e.g. "agent:claude-sonnet-4-6" or "user").
	MarkedByActor string
	// MarkedByKind is the actor kind ("agent", "human", "system").
	MarkedByKind  string
	// MarkedByModel is the model ID (may be empty for human actors).
	MarkedByModel string
	// SessionID is the session in which the judgment was made (optional).
	SessionID     string
}

// ─── FindCandidates ───────────────────────────────────────────────────────────

// FindCandidates runs a post-transaction FTS5 candidate query for the given
// savedID and returns at most opts.Limit candidates above the BM25 floor.
//
// For each candidate, a pending memory_relations row is inserted and the row's
// sync_id is exposed as Candidate.JudgmentID.
//
// Errors from this method are expected to be logged and swallowed by callers —
// detection failure must never fail the originating save.
func (s *Store) FindCandidates(savedID int64, opts CandidateOptions) ([]Candidate, error) {
	// Apply defaults.
	limit := opts.Limit
	if limit <= 0 {
		limit = 3
	}
	// BM25Floor uses pointer semantics: nil means "use the default (-2.0)".
	// An explicit pointer value (including 0.0) is used as-is.
	floor := -2.0
	if opts.BM25Floor != nil {
		floor = *opts.BM25Floor
	}

	// Get the saved observation to build the FTS query and for project/scope filtering.
	var title, project, scope string
	err := s.db.QueryRow(
		`SELECT title, ifnull(project,''), scope FROM observations WHERE id = ?`, savedID,
	).Scan(&title, &project, &scope)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("FindCandidates: observation %d not found", savedID)
	}
	if err != nil {
		return nil, fmt.Errorf("FindCandidates: get saved observation: %w", err)
	}

	// Use caller-supplied project/scope if provided (override from observation columns).
	if opts.Project != "" {
		project = opts.Project
	}
	if opts.Scope != "" {
		scope = opts.Scope
	}

	ftsQuery := sanitizeFTSCandidates(title)
	if ftsQuery == "" {
		return nil, nil
	}

	// FTS5 query: same project, same scope, exclude just-saved row, exclude soft-deleted.
	// BM25 floor filtering is done in Go after scanning.
	rows, err := s.db.Query(`
		SELECT o.id, ifnull(o.sync_id,'') as sync_id, o.title, o.type, o.topic_key,
		       fts.rank
		FROM observations_fts fts
		JOIN observations o ON o.id = fts.rowid
		WHERE observations_fts MATCH ?
		  AND o.id != ?
		  AND o.deleted_at IS NULL
		  AND ifnull(o.project,'') = ifnull(?,'')
		  AND o.scope = ?
		ORDER BY fts.rank
		LIMIT ?
	`, ftsQuery, savedID, project, scope, limit*3) // fetch extra rows to allow floor filtering
	if err != nil {
		return nil, fmt.Errorf("FindCandidates: FTS5 query: %w", err)
	}
	type rawCandidate struct {
		id       int64
		syncID   string
		title    string
		obsType  string
		topicKey *string
		score    float64
	}

	var raw []rawCandidate
	for rows.Next() {
		var rc rawCandidate
		if err := rows.Scan(&rc.id, &rc.syncID, &rc.title, &rc.obsType, &rc.topicKey, &rc.score); err != nil {
			if closeErr := rows.Close(); closeErr != nil {
				return nil, fmt.Errorf("FindCandidates: scan: %w; close rows: %v", err, closeErr)
			}
			return nil, fmt.Errorf("FindCandidates: scan: %w", err)
		}
		// Apply BM25 floor filter. BM25 scores are negative; closer to 0 = better.
		// We only include rows whose score >= floor (e.g., -1.5 >= -2.0).
		if rc.score < floor {
			continue
		}
		raw = append(raw, rc)
		if len(raw) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		if closeErr := rows.Close(); closeErr != nil {
			return nil, fmt.Errorf("FindCandidates: rows error: %w; close rows: %v", err, closeErr)
		}
		return nil, fmt.Errorf("FindCandidates: rows error: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("FindCandidates: close rows: %w", err)
	}

	if len(raw) == 0 {
		return nil, nil
	}

	// When SkipInsert=true, return candidates without writing any rows.
	if opts.SkipInsert {
		candidates := make([]Candidate, 0, len(raw))
		for _, rc := range raw {
			candidates = append(candidates, Candidate{
				ID:       rc.id,
				SyncID:   rc.syncID,
				Title:    rc.title,
				Type:     rc.obsType,
				TopicKey: rc.topicKey,
				Score:    rc.score,
				// JudgmentID is empty — no row was inserted.
			})
		}
		return candidates, nil
	}

	// Get the source observation's sync_id for the relation source_id.
	var sourceSyncID string
	if err := s.db.QueryRow(
		`SELECT ifnull(sync_id,'') FROM observations WHERE id = ?`, savedID,
	).Scan(&sourceSyncID); err != nil {
		return nil, fmt.Errorf("FindCandidates: get source sync_id: %w", err)
	}

	// Insert a pending relation row for each candidate.
	candidates := make([]Candidate, 0, len(raw))
	for _, rc := range raw {
		judgmentID := newSyncID("rel")
		_, err := s.db.Exec(`
			INSERT INTO memory_relations
				(sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', 'pending', datetime('now'), datetime('now'))
		`, judgmentID, sourceSyncID, rc.syncID)
		if err != nil {
			// Log and skip — don't fail the whole detection.
			continue
		}
		candidates = append(candidates, Candidate{
			ID:         rc.id,
			SyncID:     rc.syncID,
			Title:      rc.title,
			Type:       rc.obsType,
			TopicKey:   rc.topicKey,
			Score:      rc.score,
			JudgmentID: judgmentID,
		})
	}

	return candidates, nil
}

// ─── SaveRelation ─────────────────────────────────────────────────────────────

// SaveRelation inserts a new pending relation row. The SyncID field must be
// unique (enforced by the UNIQUE constraint on memory_relations.sync_id).
func (s *Store) SaveRelation(p SaveRelationParams) (*Relation, error) {
	_, err := s.db.Exec(`
		INSERT INTO memory_relations
			(sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', 'pending', datetime('now'), datetime('now'))
	`, p.SyncID, p.SourceID, p.TargetID)
	if err != nil {
		return nil, fmt.Errorf("SaveRelation: insert: %w", err)
	}
	return s.GetRelation(p.SyncID)
}

// ─── GetRelation ──────────────────────────────────────────────────────────────

// GetRelation retrieves a single relation row by its sync_id.
func (s *Store) GetRelation(syncID string) (*Relation, error) {
	row := s.db.QueryRow(`
		SELECT id, sync_id,
		       ifnull(source_id,''), ifnull(target_id,''),
		       relation, reason, evidence, confidence, judgment_status,
		       marked_by_actor, marked_by_kind, marked_by_model,
		       session_id, created_at, updated_at
		FROM memory_relations
		WHERE sync_id = ?
	`, syncID)

	var r Relation
	var sourceID, targetID string
	if err := row.Scan(
		&r.ID, &r.SyncID,
		&sourceID, &targetID,
		&r.Relation, &r.Reason, &r.Evidence, &r.Confidence, &r.JudgmentStatus,
		&r.MarkedByActor, &r.MarkedByKind, &r.MarkedByModel,
		&r.SessionID, &r.CreatedAt, &r.UpdatedAt,
	); err == sql.ErrNoRows {
		return nil, fmt.Errorf("GetRelation: relation %q not found", syncID)
	} else if err != nil {
		return nil, fmt.Errorf("GetRelation: %w", err)
	}
	r.SourceID = sourceID
	r.TargetID = targetID
	return &r, nil
}

// GetRelationByIntID retrieves a single relation enriched with source/target observation
// titles by its integer primary key. Returns a *RelationListItem (same shape as
// ListRelations rows) so HTTP handlers share one response type.
// Returns an error wrapping "not found" when the id does not exist.
func (s *Store) GetRelationByIntID(id int64) (*RelationListItem, error) {
	row := s.db.QueryRow(`
		SELECT r.id, r.sync_id, r.relation, r.judgment_status,
		       ifnull(r.source_id,''), ifnull(src.title,''),
		       ifnull(r.target_id,''), ifnull(tgt.title,''),
		       r.created_at, r.updated_at
		FROM memory_relations r
		LEFT JOIN observations src ON src.sync_id = r.source_id AND src.deleted_at IS NULL
		LEFT JOIN observations tgt ON tgt.sync_id = r.target_id AND tgt.deleted_at IS NULL
		WHERE r.id = ?
	`, id)

	var item RelationListItem
	if err := row.Scan(
		&item.ID, &item.SyncID, &item.Relation, &item.JudgmentStatus,
		&item.SourceID, &item.SourceTitle,
		&item.TargetID, &item.TargetTitle,
		&item.CreatedAt, &item.UpdatedAt,
	); err == sql.ErrNoRows {
		return nil, fmt.Errorf("GetRelationByIntID: relation id %d not found", id)
	} else if err != nil {
		return nil, fmt.Errorf("GetRelationByIntID: %w", err)
	}
	return &item, nil
}

// ─── JudgeRelation ────────────────────────────────────────────────────────────

// JudgeRelation records a verdict on an existing pending relation row.
//
// Re-judge policy: OVERWRITE the existing row (design decision). The updated row
// is returned on success.
//
// Phase 2: wraps the UPDATE in a transaction to atomically enqueue a sync
// mutation when the source observation's project is enrolled for cloud sync.
// Returns ErrCrossProjectRelation if source and target belong to different projects.
//
// Returns an error if the judgment_id is unknown or the relation verb is invalid.
func (s *Store) JudgeRelation(p JudgeRelationParams) (*Relation, error) {
	if !isValidRelationVerb(p.Relation) {
		return nil, fmt.Errorf("JudgeRelation: invalid relation verb %q — must be one of: related, compatible, scoped, conflicts_with, supersedes, not_conflict", p.Relation)
	}

	// Verify the relation exists and fetch source/target IDs for project check.
	var sourceID, targetID string
	if err := s.db.QueryRow(
		`SELECT ifnull(source_id,''), ifnull(target_id,'') FROM memory_relations WHERE sync_id = ?`,
		p.JudgmentID,
	).Scan(&sourceID, &targetID); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("JudgeRelation: relation %q not found", p.JudgmentID)
		}
		return nil, fmt.Errorf("JudgeRelation: check existence: %w", err)
	}

	// Build nullable model string.
	var markedByModel *string
	if p.MarkedByModel != "" {
		markedByModel = &p.MarkedByModel
	}
	var sessionID *string
	if p.SessionID != "" {
		sessionID = &p.SessionID
	}

	if err := s.withTx(func(tx *sql.Tx) error {
		// ── Cross-project guard (Phase 2, REQ-003) ─────────────────────────
		// Derive source and target project for enrollment checks and the guard.
		// Missing observation → empty string (REQ-011 edge).
		var srcProject, tgtProject string
		_ = tx.QueryRow(
			`SELECT ifnull(project,'') FROM observations WHERE sync_id = ?`, sourceID,
		).Scan(&srcProject)
		_ = tx.QueryRow(
			`SELECT ifnull(project,'') FROM observations WHERE sync_id = ?`, targetID,
		).Scan(&tgtProject)

		// Delegate to shared helper; reject cross-project pairs.
		if err := validateCrossProjectGuard(tx, sourceID, targetID); err != nil {
			return err
		}

		// ── UPDATE memory_relations ────────────────────────────────────────
		if _, err := s.execHook(tx, `
			UPDATE memory_relations
			SET relation        = ?,
			    reason          = ?,
			    evidence        = ?,
			    confidence      = ?,
			    judgment_status = 'judged',
			    marked_by_actor = ?,
			    marked_by_kind  = ?,
			    marked_by_model = ?,
			    session_id      = ?,
			    updated_at      = datetime('now')
			WHERE sync_id = ?
		`,
			p.Relation,
			p.Reason,
			p.Evidence,
			p.Confidence,
			p.MarkedByActor,
			p.MarkedByKind,
			markedByModel,
			sessionID,
			p.JudgmentID,
		); err != nil {
			return fmt.Errorf("JudgeRelation: update: %w", err)
		}

		// ── Enqueue sync mutation when project is enrolled (REQ-001) ───────
		// Derive project from source observation; empty string if source missing.
		// (REQ-011: loud failure is the server's job; we enqueue project='' and log.)
		//
		// Enrollment check: prefer srcProject; fall back to tgtProject when source
		// is missing locally (race condition). This ensures enqueue happens with
		// project='' when source is absent but target's project IS enrolled.
		enrollCheckProject := srcProject
		if enrollCheckProject == "" {
			enrollCheckProject = tgtProject
		}
		var enrolled int
		if err := tx.QueryRow(
			`SELECT 1 FROM sync_enrolled_projects WHERE project = ? LIMIT 1`, enrollCheckProject,
		).Scan(&enrolled); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("JudgeRelation: check enrollment: %w", err)
		}
		if enrolled == 0 {
			return nil // not enrolled — no mutation enqueued
		}

		// REQ-011: log at WARNING level when source observation is missing locally
		// (project='' race condition). The server will reject with 400; this log
		// is the local breadcrumb so the gap is not silently swallowed.
		if srcProject == "" {
			log.Printf("[store] WARNING: JudgeRelation enqueueing relation %s with project='' (source observation missing locally); server will reject", p.JudgmentID)
		}

		// Read the full updated row to build the payload.
		rel, err := s.getRelationTx(tx, p.JudgmentID)
		if err != nil {
			return fmt.Errorf("JudgeRelation: read updated relation: %w", err)
		}

		payload := syncRelationPayload{
			SyncID:         rel.SyncID,
			SourceID:       rel.SourceID,
			TargetID:       rel.TargetID,
			Relation:       rel.Relation,
			Reason:         rel.Reason,
			Evidence:       rel.Evidence,
			Confidence:     rel.Confidence,
			JudgmentStatus: rel.JudgmentStatus,
			MarkedByActor:  rel.MarkedByActor,
			MarkedByKind:   rel.MarkedByKind,
			MarkedByModel:  rel.MarkedByModel,
			SessionID:      rel.SessionID,
			Project:        srcProject,
			CreatedAt:      rel.CreatedAt,
			UpdatedAt:      rel.UpdatedAt,
		}
		return s.enqueueSyncMutationTx(tx, SyncEntityRelation, rel.SyncID, SyncOpUpsert, payload)
	}); err != nil {
		return nil, err
	}

	return s.GetRelation(p.JudgmentID)
}

// ─── Cross-project guard helper ───────────────────────────────────────────────

// validateCrossProjectGuard checks whether sourceID and targetID belong to the
// same project. It returns ErrCrossProjectRelation when they are in different
// projects. Both empty is allowed (observation may be missing locally — REQ-011).
// This function is shared by JudgeRelation and JudgeBySemantic.
func validateCrossProjectGuard(tx *sql.Tx, sourceID, targetID string) error {
	var srcProject, tgtProject string
	_ = tx.QueryRow(
		`SELECT ifnull(project,'') FROM observations WHERE sync_id = ?`, sourceID,
	).Scan(&srcProject)
	_ = tx.QueryRow(
		`SELECT ifnull(project,'') FROM observations WHERE sync_id = ?`, targetID,
	).Scan(&tgtProject)

	if srcProject != "" && tgtProject != "" && srcProject != tgtProject {
		return ErrCrossProjectRelation
	}
	return nil
}

// ─── JudgeBySemantic ──────────────────────────────────────────────────────────

// JudgeBySemantic persists a semantic verdict produced by an AgentRunner into
// the memory_relations table with system provenance (marked_by_kind="system",
// marked_by_actor="engram", marked_by_model=params.Model).
//
// When params.Relation is "not_conflict" the call is a no-op: no row is inserted
// and an empty sync_id is returned without error.
//
// Idempotency: if a row already exists for (source_id, target_id) in either
// direction, the existing row is updated (UPSERT). The returned sync_id is
// always the canonical row's sync_id.
//
// Returns ErrCrossProjectRelation when source and target belong to different
// projects. Returns a validation error when required fields are missing or
// Confidence is out of [0.0, 1.0].
func (s *Store) JudgeBySemantic(p JudgeBySemanticParams) (string, error) {
	// Validation.
	if p.SourceID == "" {
		return "", fmt.Errorf("JudgeBySemantic: SourceID is required")
	}
	if p.TargetID == "" {
		return "", fmt.Errorf("JudgeBySemantic: TargetID is required")
	}
	if !isValidRelationVerb(p.Relation) {
		return "", fmt.Errorf("JudgeBySemantic: invalid relation verb %q — must be one of: related, compatible, scoped, conflicts_with, supersedes, not_conflict", p.Relation)
	}
	if p.Confidence < 0.0 || p.Confidence > 1.0 {
		return "", fmt.Errorf("JudgeBySemantic: confidence %v is out of range [0.0, 1.0]", p.Confidence)
	}

	// not_conflict is a no-op.
	if p.Relation == RelationNotConflict {
		return "", nil
	}

	var resultSyncID string

	if err := s.withTx(func(tx *sql.Tx) error {
		// Cross-project guard.
		if err := validateCrossProjectGuard(tx, p.SourceID, p.TargetID); err != nil {
			return err
		}

		// Check whether a row already exists for this (source_id, target_id) pair
		// in either direction.
		var existingSyncID string
		err := tx.QueryRow(`
			SELECT sync_id FROM memory_relations
			WHERE (source_id = ? AND target_id = ?)
			   OR (source_id = ? AND target_id = ?)
			LIMIT 1
		`, p.SourceID, p.TargetID, p.TargetID, p.SourceID).Scan(&existingSyncID)

		confidence := p.Confidence
		var modelPtr *string
		if p.Model != "" {
			modelPtr = &p.Model
		}
		actor := "engram"
		kind := "system"

		if err == sql.ErrNoRows {
			// Insert new row.
			existingSyncID = newSyncID("rel")
			if _, execErr := tx.Exec(`
				INSERT INTO memory_relations
					(sync_id, source_id, target_id, relation, judgment_status,
					 confidence, reason,
					 marked_by_actor, marked_by_kind, marked_by_model,
					 created_at, updated_at)
				VALUES (?, ?, ?, ?, 'judged', ?, ?, ?, ?, ?, datetime('now'), datetime('now'))
			`, existingSyncID, p.SourceID, p.TargetID, p.Relation,
				confidence, p.Reasoning,
				actor, kind, modelPtr,
			); execErr != nil {
				return fmt.Errorf("JudgeBySemantic: insert: %w", execErr)
			}
		} else if err != nil {
			return fmt.Errorf("JudgeBySemantic: check existing: %w", err)
		} else {
			// Update existing row.
			if _, execErr := tx.Exec(`
				UPDATE memory_relations
				SET relation        = ?,
				    judgment_status = 'judged',
				    confidence      = ?,
				    reason          = ?,
				    marked_by_actor = ?,
				    marked_by_kind  = ?,
				    marked_by_model = ?,
				    updated_at      = datetime('now')
				WHERE sync_id = ?
			`, p.Relation, confidence, p.Reasoning,
				actor, kind, modelPtr,
				existingSyncID,
			); execErr != nil {
				return fmt.Errorf("JudgeBySemantic: update: %w", execErr)
			}
		}

		resultSyncID = existingSyncID
		return nil
	}); err != nil {
		return "", err
	}

	return resultSyncID, nil
}

// getRelationTx is the transactional variant of GetRelation used within
// JudgeRelation to read the freshly-updated row before commit.
func (s *Store) getRelationTx(tx *sql.Tx, syncID string) (*Relation, error) {
	row := tx.QueryRow(`
		SELECT id, sync_id,
		       ifnull(source_id,''), ifnull(target_id,''),
		       relation, reason, evidence, confidence, judgment_status,
		       marked_by_actor, marked_by_kind, marked_by_model,
		       session_id, created_at, updated_at
		FROM memory_relations
		WHERE sync_id = ?
	`, syncID)

	var r Relation
	var sourceID, targetID string
	if err := row.Scan(
		&r.ID, &r.SyncID,
		&sourceID, &targetID,
		&r.Relation, &r.Reason, &r.Evidence, &r.Confidence, &r.JudgmentStatus,
		&r.MarkedByActor, &r.MarkedByKind, &r.MarkedByModel,
		&r.SessionID, &r.CreatedAt, &r.UpdatedAt,
	); err == sql.ErrNoRows {
		return nil, fmt.Errorf("getRelationTx: relation %q not found", syncID)
	} else if err != nil {
		return nil, fmt.Errorf("getRelationTx: %w", err)
	}
	r.SourceID = sourceID
	r.TargetID = targetID
	return &r, nil
}

// ─── GetRelationsForObservations ──────────────────────────────────────────────

// GetRelationsForObservations returns a map of observation sync_id →
// ObservationRelations for all observations in syncIDs. Relations with
// judgment_status='orphaned' are excluded.
//
// A single SQL query with IN/OR and LEFT JOINs avoids N+1 queries.
// The returned Relation values are enriched with source/target integer IDs and
// titles via LEFT JOIN, used by the MCP annotation builder (REQ-005, REQ-012).
// Missing or soft-deleted observations set the corresponding *Missing flag to true.
func (s *Store) GetRelationsForObservations(syncIDs []string) (map[string]ObservationRelations, error) {
	if len(syncIDs) == 0 {
		return map[string]ObservationRelations{}, nil
	}

	// Build IN clause.
	placeholders := make([]string, len(syncIDs))
	args := make([]any, 0, len(syncIDs)*2)
	for i, id := range syncIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	for _, id := range syncIDs {
		args = append(args, id)
	}

	inClause := joinStrings(placeholders, ",")
	// LEFT JOIN to observations for title enrichment (REQ-005, Design §8).
	// source_missing / target_missing: observation is absent (not found) or soft-deleted.
	query := fmt.Sprintf(`
		SELECT r.id, r.sync_id,
		       ifnull(r.source_id,''), ifnull(r.target_id,''),
		       r.relation, r.reason, r.evidence, r.confidence, r.judgment_status,
		       r.marked_by_actor, r.marked_by_kind, r.marked_by_model,
		       r.session_id, r.created_at, r.updated_at,
		       ifnull(src.id,0)              AS source_int_id,
		       ifnull(src.title,'')          AS source_title,
		       (src.id IS NULL OR src.deleted_at IS NOT NULL) AS source_missing,
		       ifnull(tgt.id,0)              AS target_int_id,
		       ifnull(tgt.title,'')          AS target_title,
		       (tgt.id IS NULL OR tgt.deleted_at IS NOT NULL) AS target_missing
		FROM memory_relations r
		LEFT JOIN observations src ON src.sync_id = r.source_id
		LEFT JOIN observations tgt ON tgt.sync_id = r.target_id
		WHERE (r.source_id IN (%s) OR r.target_id IN (%s))
		  AND r.judgment_status != 'orphaned'
	`, inClause, inClause)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("GetRelationsForObservations: query: %w", err)
	}
	defer rows.Close()

	result := make(map[string]ObservationRelations)

	for rows.Next() {
		var r Relation
		var sourceID, targetID string
		// SQLite BOOLEAN → int; use int for missing flags.
		var sourceMissingInt, targetMissingInt int
		if err := rows.Scan(
			&r.ID, &r.SyncID,
			&sourceID, &targetID,
			&r.Relation, &r.Reason, &r.Evidence, &r.Confidence, &r.JudgmentStatus,
			&r.MarkedByActor, &r.MarkedByKind, &r.MarkedByModel,
			&r.SessionID, &r.CreatedAt, &r.UpdatedAt,
			&r.SourceIntID, &r.SourceTitle, &sourceMissingInt,
			&r.TargetIntID, &r.TargetTitle, &targetMissingInt,
		); err != nil {
			return nil, fmt.Errorf("GetRelationsForObservations: scan: %w", err)
		}
		r.SourceID = sourceID
		r.TargetID = targetID
		r.SourceMissing = sourceMissingInt != 0
		r.TargetMissing = targetMissingInt != 0

		// Index by source_id.
		for _, id := range syncIDs {
			if r.SourceID == id {
				entry := result[id]
				entry.AsSource = append(entry.AsSource, r)
				result[id] = entry
			}
			if r.TargetID == id {
				entry := result[id]
				entry.AsTarget = append(entry.AsTarget, r)
				result[id] = entry
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetRelationsForObservations: rows error: %w", err)
	}

	return result, nil
}

// sanitizeFTSCandidates builds an OR-based FTS5 query from a title so that
// FindCandidates returns documents with ANY term overlap (not all terms).
// Using implicit AND (sanitizeFTS) is too strict for candidate detection:
// the full saved title would require every word to appear in candidates.
// OR semantics give broader recall; BM25 score still captures relevance.
func sanitizeFTSCandidates(title string) string {
	words := strings.Fields(title)
	if len(words) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(words))
	for _, w := range words {
		w = strings.Trim(w, `"`)
		if w != "" {
			quoted = append(quoted, `"`+w+`"`)
		}
	}
	return strings.Join(quoted, " OR ")
}

// joinStrings joins a slice of strings with the given separator.
func joinStrings(items []string, sep string) string {
	if len(items) == 0 {
		return ""
	}
	result := items[0]
	for _, item := range items[1:] {
		result += sep + item
	}
	return result
}

// ─── Phase 3: ListRelations / CountRelations ──────────────────────────────────

// ListRelations returns a paginated list of relation rows filtered by the given
// options. Project filtering is done via LEFT JOIN to observations (no schema
// change). Uses idx_memrel_status_created for efficient status+date ordering.
func (s *Store) ListRelations(opts ListRelationsOptions) ([]RelationListItem, error) {
	query, args := buildRelationsQuery(opts, false)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("ListRelations: query: %w", err)
	}
	defer rows.Close()

	var items []RelationListItem
	for rows.Next() {
		var item RelationListItem
		if err := rows.Scan(
			&item.ID, &item.SyncID, &item.Relation, &item.JudgmentStatus,
			&item.SourceID, &item.SourceTitle, &item.TargetID, &item.TargetTitle,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("ListRelations: scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListRelations: rows error: %w", err)
	}
	if items == nil {
		items = []RelationListItem{}
	}
	return items, nil
}

// CountRelations returns the total number of relation rows matching opts.
// Uses the same WHERE conditions as ListRelations.
func (s *Store) CountRelations(opts ListRelationsOptions) (int, error) {
	query, args := buildRelationsQuery(opts, true)
	var total int
	if err := s.db.QueryRow(query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("CountRelations: %w", err)
	}
	return total, nil
}

// buildRelationsQuery constructs the SQL for ListRelations and CountRelations.
// When countOnly=true, generates SELECT count(*) instead of the full column list.
func buildRelationsQuery(opts ListRelationsOptions, countOnly bool) (string, []any) {
	var args []any

	var selectClause string
	if countOnly {
		selectClause = "SELECT count(*)"
	} else {
		selectClause = `SELECT r.id, r.sync_id, r.relation, r.judgment_status,
			ifnull(r.source_id,''), ifnull(src.title,''),
			ifnull(r.target_id,''), ifnull(tgt.title,''),
			r.created_at, r.updated_at`
	}

	query := selectClause + `
		FROM memory_relations r
		LEFT JOIN observations src ON src.sync_id = r.source_id AND src.deleted_at IS NULL
		LEFT JOIN observations tgt ON tgt.sync_id = r.target_id AND tgt.deleted_at IS NULL
		WHERE 1=1`

	if opts.Project != "" {
		query += ` AND (ifnull(src.project,'') = ? OR ifnull(tgt.project,'') = ?)`
		args = append(args, opts.Project, opts.Project)
	}
	if opts.Status != "" {
		query += ` AND r.judgment_status = ?`
		args = append(args, opts.Status)
	}
	if !opts.SinceTime.IsZero() {
		query += ` AND r.created_at >= ?`
		args = append(args, opts.SinceTime.UTC().Format("2006-01-02T15:04:05Z"))
	}

	if !countOnly {
		query += ` ORDER BY r.created_at DESC`
		if opts.Limit > 0 {
			query += ` LIMIT ?`
			args = append(args, opts.Limit)
		}
		if opts.Offset > 0 {
			query += ` OFFSET ?`
			args = append(args, opts.Offset)
		}
	}

	return query, args
}

// ─── Phase 3: GetRelationStats ─────────────────────────────────────────────────

// GetRelationStats returns aggregate counts for a project's relations plus
// the deferred and dead queue totals. Two queries are executed: one GROUP BY
// and one delegated to CountDeferredAndDead.
func (s *Store) GetRelationStats(project string) (RelationStats, error) {
	stats := RelationStats{
		Project:          project,
		ByRelation:       map[string]int{},
		ByJudgmentStatus: map[string]int{},
	}

	// Build query: when project is non-empty, filter via JOIN to observations.
	var q string
	var args []any
	if project != "" {
		q = `
			SELECT r.relation, r.judgment_status, count(*) AS cnt
			FROM memory_relations r
			LEFT JOIN observations src ON src.sync_id = r.source_id AND src.deleted_at IS NULL
			LEFT JOIN observations tgt ON tgt.sync_id = r.target_id AND tgt.deleted_at IS NULL
			WHERE ifnull(src.project,'') = ? OR ifnull(tgt.project,'') = ?
			GROUP BY r.relation, r.judgment_status
		`
		args = []any{project, project}
	} else {
		q = `
			SELECT relation, judgment_status, count(*) AS cnt
			FROM memory_relations
			GROUP BY relation, judgment_status
		`
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return stats, fmt.Errorf("GetRelationStats: query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var rel, status string
		var cnt int
		if err := rows.Scan(&rel, &status, &cnt); err != nil {
			return stats, fmt.Errorf("GetRelationStats: scan: %w", err)
		}
		stats.ByRelation[rel] += cnt
		stats.ByJudgmentStatus[status] += cnt
	}
	if err := rows.Err(); err != nil {
		return stats, fmt.Errorf("GetRelationStats: rows error: %w", err)
	}

	deferred, dead, err := s.CountDeferredAndDead()
	if err != nil {
		return stats, fmt.Errorf("GetRelationStats: count deferred/dead: %w", err)
	}
	stats.DeferredCount = deferred
	stats.DeadCount = dead

	return stats, nil
}

// ─── Phase 3: ScanProject ─────────────────────────────────────────────────────

// ScanProject walks all observations in the given project (filtered by Since)
// and for each observation calls FindCandidates with SkipInsert=true. If Apply
// is true and below MaxInsert cap, each new candidate pair is inserted as a
// pending relation (after a pre-check to skip already-related pairs).
//
// Phase 4 extension: when ScanOptions.Semantic is true, after the FTS5 candidate
// collection a bounded worker pool calls Runner.Compare on each pair and persists
// non-"not_conflict" verdicts via JudgeBySemantic. Semantic=false (zero value)
// preserves Phase 3 behaviour exactly.
//
// Returns a ScanResult with counts of inspected observations, candidates found,
// already-related pairs skipped, relations inserted, and whether the cap was hit.
func (s *Store) ScanProject(opts ScanOptions) (ScanResult, error) {
	// ── Semantic flag validation (Phase 4) ────────────────────────────────────
	if opts.Semantic {
		if opts.Runner == nil {
			return ScanResult{}, ErrSemanticRunnerRequired
		}
		if opts.BuildPrompt == nil {
			return ScanResult{}, ErrSemanticPromptBuilderRequired
		}
	}

	maxInsert := opts.MaxInsert
	if maxInsert <= 0 {
		maxInsert = 100
	}
	maxSemantic := opts.MaxSemantic
	if maxSemantic <= 0 {
		maxSemantic = 100
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 5
	}
	timeoutPerCall := opts.TimeoutPerCall
	if timeoutPerCall <= 0 {
		timeoutPerCall = 60 * time.Second
	}

	result := ScanResult{
		Project: opts.Project,
		DryRun:  !opts.Apply,
	}

	// Walk observations in the project.
	obsQuery := `
		SELECT id, ifnull(sync_id,''), scope
		FROM observations
		WHERE ifnull(project,'') = ?
		  AND deleted_at IS NULL
	`
	var obsArgs []any
	obsArgs = append(obsArgs, opts.Project)
	if !opts.Since.IsZero() {
		obsQuery += ` AND created_at >= ?`
		obsArgs = append(obsArgs, opts.Since.UTC().Format("2006-01-02T15:04:05Z"))
	}

	obsRows, err := s.db.Query(obsQuery, obsArgs...)
	if err != nil {
		return result, fmt.Errorf("ScanProject: list observations: %w", err)
	}

	type obsRow struct {
		id     int64
		syncID string
		scope  string
	}
	var observations []obsRow
	for obsRows.Next() {
		var o obsRow
		if err := obsRows.Scan(&o.id, &o.syncID, &o.scope); err != nil {
			obsRows.Close()
			return result, fmt.Errorf("ScanProject: scan obs row: %w", err)
		}
		observations = append(observations, o)
	}
	obsRows.Close()
	if err := obsRows.Err(); err != nil {
		return result, fmt.Errorf("ScanProject: obs rows error: %w", err)
	}

	// ── Phase 4: collect all (source, candidate) pairs for semantic scan ──────
	// candidatePair represents a source+candidate pair to be semantically judged.
	type candidatePair struct {
		sourceSnippet    ObservationSnippet
		candidateSnippet ObservationSnippet
	}
	var semanticPairs []candidatePair

	for _, obs := range observations {
		result.Inspected++

		// Find candidates without inserting (SkipInsert=true per design §5).
		candidates, err := s.FindCandidates(obs.id, CandidateOptions{
			Project:    opts.Project,
			Scope:      obs.scope,
			Limit:      10,
			SkipInsert: true,
		})
		if err != nil {
			log.Printf("[store] ScanProject: FindCandidates obs=%s: %v", obs.syncID, err)
			continue
		}
		result.CandidatesFound += len(candidates)

		if opts.Semantic {
			// In semantic mode, accumulate pairs for the worker pool.
			// We need the full content for prompt building — fetch from DB.
			var srcTitle, srcType, srcContent string
			_ = s.db.QueryRow(
				`SELECT title, type, ifnull(content,'') FROM observations WHERE sync_id = ?`, obs.syncID,
			).Scan(&srcTitle, &srcType, &srcContent)
			srcSnippet := ObservationSnippet{
				ID:      obs.id,
				SyncID:  obs.syncID,
				Title:   srcTitle,
				Type:    srcType,
				Content: srcContent,
			}

			for _, c := range candidates {
				if len(semanticPairs) >= maxSemantic {
					// Cap reached — stop adding pairs.
					result.Capped = true
					break
				}
				var candTitle, candType, candContent string
				_ = s.db.QueryRow(
					`SELECT title, type, ifnull(content,'') FROM observations WHERE sync_id = ?`, c.SyncID,
				).Scan(&candTitle, &candType, &candContent)
				semanticPairs = append(semanticPairs, candidatePair{
					sourceSnippet: srcSnippet,
					candidateSnippet: ObservationSnippet{
						ID:      c.ID,
						SyncID:  c.SyncID,
						Title:   candTitle,
						Type:    candType,
						Content: candContent,
					},
				})
			}
			if result.Capped {
				log.Printf("[store] ScanProject: MaxSemantic cap (%d) reached; some pairs skipped", maxSemantic)
				break
			}
			continue
		}

		// Phase 3 path: Apply inserts pending rows.
		if !opts.Apply {
			continue
		}

		for _, c := range candidates {
			if result.RelationsInserted >= maxInsert {
				result.Capped = true
				return result, nil
			}

			// Pre-check: skip pairs that already have any relation row in either direction.
			var exists int
			if err := s.db.QueryRow(
				`SELECT 1 FROM memory_relations
				 WHERE (source_id = ? AND target_id = ?)
				    OR (source_id = ? AND target_id = ?)
				 LIMIT 1`,
				obs.syncID, c.SyncID, c.SyncID, obs.syncID,
			).Scan(&exists); err == nil {
				result.AlreadyRelated++
				continue
			} else if err != sql.ErrNoRows {
				log.Printf("[store] ScanProject: pre-check obs=%s cand=%s: %v", obs.syncID, c.SyncID, err)
				continue
			}

			judgmentID := newSyncID("rel")
			if _, err := s.db.Exec(`
				INSERT INTO memory_relations
					(sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at)
				VALUES (?, ?, ?, 'pending', 'pending', datetime('now'), datetime('now'))
			`, judgmentID, obs.syncID, c.SyncID); err != nil {
				log.Printf("[store] ScanProject: insert relation obs=%s cand=%s: %v", obs.syncID, c.SyncID, err)
				continue
			}
			result.RelationsInserted++
		}

		if result.RelationsInserted >= maxInsert {
			result.Capped = true
			return result, nil
		}
	}

	// ── Phase 4: semantic worker pool ─────────────────────────────────────────
	if !opts.Semantic || len(semanticPairs) == 0 {
		return result, nil
	}

	type pairResult struct {
		judged  int
		skipped int
		errors  int
	}

	pairCh := make(chan candidatePair, len(semanticPairs))
	for _, p := range semanticPairs {
		pairCh <- p
	}
	close(pairCh)

	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for pair := range pairCh {
				func() {
					// Recover from panics in runner.Compare.
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[store] ScanProject: runner.Compare panic pair=(%s,%s): %v",
								pair.sourceSnippet.SyncID, pair.candidateSnippet.SyncID, r)
							mu.Lock()
							result.SemanticErrors++
							mu.Unlock()
						}
					}()

					callCtx, cancel := context.WithTimeout(context.Background(), timeoutPerCall)
					defer cancel()

					prompt := opts.BuildPrompt(pair.sourceSnippet, pair.candidateSnippet)
					verdict, err := opts.Runner.Compare(callCtx, prompt)
					if err != nil {
						log.Printf("[store] ScanProject: runner.Compare pair=(%s,%s) error: %v",
							pair.sourceSnippet.SyncID, pair.candidateSnippet.SyncID, err)
						mu.Lock()
						result.SemanticErrors++
						mu.Unlock()
						return
					}

					if verdict.Relation == RelationNotConflict {
						mu.Lock()
						result.SemanticSkipped++
						mu.Unlock()
						return
					}

					// Persist non-not_conflict verdict.
					_, judgeErr := s.JudgeBySemantic(JudgeBySemanticParams{
						SourceID:   pair.sourceSnippet.SyncID,
						TargetID:   pair.candidateSnippet.SyncID,
						Relation:   verdict.Relation,
						Confidence: verdict.Confidence,
						Reasoning:  verdict.Reasoning,
						Model:      verdict.Model,
					})
					if judgeErr != nil {
						log.Printf("[store] ScanProject: JudgeBySemantic pair=(%s,%s) error: %v",
							pair.sourceSnippet.SyncID, pair.candidateSnippet.SyncID, judgeErr)
						mu.Lock()
						result.SemanticErrors++
						mu.Unlock()
						return
					}

					mu.Lock()
					result.SemanticJudged++
					mu.Unlock()
				}()
			}
		}()
	}

	wg.Wait()

	return result, nil
}
