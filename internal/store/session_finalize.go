package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// FinalizeSessionParams identifies an existing session and its resolved project.
// Summary is caller-authored content saved only as a session_summary observation.
// The existing sessions.summary free-text column is preserved.
// The session identity is exact (never trimmed or implicitly created).
type FinalizeSessionParams struct {
	SessionID string
	Project   string
	Summary   string
}

// FinalizeSessionResult retains the original observation identity on every retry,
// even when the caller supplies different summary text. The local journal is not
// replicated; an imported ended session without this journal rejects finalization.
type FinalizeSessionResult struct {
	SummaryID        int64  `json:"summary_id"`
	SummarySyncID    string `json:"summary_sync_id"`
	AlreadyFinalized bool   `json:"already_finalized"`
}

// FinalizeSession atomically saves a new summary observation, ends the session,
// queues their normal sync mutations, and records a durable local retry marker.
// It never deduplicates against arbitrary summaries or changes legacy EndSession.
func (s *Store) FinalizeSession(p FinalizeSessionParams) (FinalizeSessionResult, error) {
	if err := validateSessionID(p.SessionID); err != nil {
		return FinalizeSessionResult{}, err
	}
	project, _ := NormalizeProject(p.Project)
	if project == "" {
		return FinalizeSessionResult{}, ErrProjectRequired
	}
	content, _ := s.prepareStoredContent(p.Summary)
	if strings.TrimSpace(content) == "" {
		return FinalizeSessionResult{}, ErrObservationContentRequired
	}
	var result FinalizeSessionResult
	err := s.withTx(func(tx *sql.Tx) error {
		result = FinalizeSessionResult{}
		var owner string
		var ended, priorSummary sql.NullString
		if err := tx.QueryRow(`SELECT ifnull(project, ''), ended_at, summary FROM sessions WHERE id = ?`, p.SessionID).Scan(&owner, &ended, &priorSummary); err != nil {
			if err == sql.ErrNoRows {
				return ErrSessionNotFound
			}
			return err
		}
		owner, _ = NormalizeProject(owner)
		if owner != project {
			return ErrSessionOwnershipMismatch
		}
		err := tx.QueryRow(`SELECT summary_id, summary_sync_id FROM session_finalizations WHERE session_id = ?`, p.SessionID).Scan(&result.SummaryID, &result.SummarySyncID)
		if err == nil {
			result.AlreadyFinalized = true
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		if ended.Valid {
			return ErrSessionAlreadyEnded
		}
		observation := AddObservationParams{SessionID: p.SessionID, Project: project, Type: "session_summary", Title: stripPrivateTags("Session summary: " + project), Scope: "project"}
		id, err := s.insertObservationTx(tx, observation, observation.Title, content, "project", "", hashNormalized(content))
		if err != nil {
			return err
		}
		obs, err := s.getObservationTx(tx, id)
		if err != nil {
			return err
		}
		if err := s.recordObservationSaveOperationTx(tx, "", "", id, obs); err != nil {
			return err
		}
		// Structured content belongs only in the observation. Preserve the prior
		// free-text column exactly while using the established end/sync path.
		if err := s.endSessionSummaryOperation(p.SessionID, priorSummary)(tx); err != nil {
			return err
		}
		if _, err := s.execHook(tx, `INSERT INTO session_finalizations (session_id, summary_id, summary_sync_id) VALUES (?, ?, ?)`, p.SessionID, id, obs.SyncID); err != nil {
			return err
		}
		result.SummaryID, result.SummarySyncID = id, obs.SyncID
		return nil
	})
	if err != nil {
		return FinalizeSessionResult{}, err
	}
	return result, nil
}

// insertObservationTx is the shared new-row path. Callers own preparation,
// ownership validation, deduplication policy, and sync enqueueing.
func (s *Store) insertObservationTx(tx *sql.Tx, p AddObservationParams, title, content, scope, topicKey, normHash string) (int64, error) {
	res, err := s.execHook(tx,
		`INSERT INTO observations (sync_id, session_id, type, title, content, tool_name, project, scope, org, topic_key, normalized_hash, revision_count, duplicate_count, last_seen_at, updated_at)
 VALUES (?, ?, ?, ?, ?, ?, CAST(? AS TEXT), ?, ?, ?, ?, 1, 1, datetime('now'), datetime('now'))`,
		newSyncID("obs"), p.SessionID, p.Type, title, content,
		nullableString(p.ToolName), nullableString(p.Project), scope, nullableString(p.Org), nullableString(topicKey), normHash)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// Decay is applied only on insertion, preserving AddObservation behavior.
	if months, ok := decayReviewAfterMonths[p.Type]; ok {
		reviewAfter := time.Now().UTC().AddDate(0, months, 0).Format("2006-01-02 15:04:05")
		if _, err := s.execHook(tx, `UPDATE observations SET review_after = ? WHERE id = ?`, reviewAfter, id); err != nil {
			return 0, fmt.Errorf("set review_after: %w", err)
		}
	}
	return id, nil
}
