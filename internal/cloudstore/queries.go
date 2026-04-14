package cloudstore

import (
	"context"
	"fmt"
)

// ─── Sessions (T09) ─────────────────────────────────────────────────────────

// ListRecentSessions returns the most recent sessions for a project.
func (s *Store) ListRecentSessions(ctx context.Context, project string, limit int) ([]map[string]any, error) {
	return s.listSessions(ctx, project, limit)
}

// ListAllSessions returns all sessions for a project, paginated.
func (s *Store) ListAllSessions(ctx context.Context, project string, limit int) ([]map[string]any, error) {
	return s.listSessions(ctx, project, limit)
}

func (s *Store) listSessions(ctx context.Context, project string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}

	rows, err := s.pool.Query(ctx, `
		SELECT s.numeric_id, s.sync_id, s.project, s.directory,
			s.started_at::text, COALESCE(s.ended_at::text, '') AS ended_at,
			COALESCE(s.summary, '') AS summary,
			(SELECT COUNT(*) FROM observations o
			 WHERE o.project = s.project AND o.session_id = s.sync_id AND o.deleted_at IS NULL
			) AS observation_count
		FROM sessions s
		WHERE s.project = $1
		ORDER BY s.started_at DESC
		LIMIT $2
	`, project, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var result []map[string]any
	for rows.Next() {
		var (
			numericID                        int64
			syncID, project2, directory      string
			startedAt, endedAt, summary      string
			obsCount                         int
		)
		if err := rows.Scan(&numericID, &syncID, &project2, &directory,
			&startedAt, &endedAt, &summary, &obsCount); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		result = append(result, map[string]any{
			"numeric_id":        numericID,
			"sync_id":           syncID,
			"project":           project2,
			"directory":         directory,
			"started_at":        startedAt,
			"ended_at":          endedAt,
			"summary":           summary,
			"observation_count": obsCount,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result == nil {
		result = []map[string]any{}
	}
	return result, nil
}

// GetSessionObservations returns all observations for a given session.
func (s *Store) GetSessionObservations(ctx context.Context, sessionSyncID, project, userID string) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT numeric_id, sync_id, type, title, content, tool_name,
			scope, topic_key, revision_count, created_at::text, updated_at::text
		FROM observations
		WHERE session_id = $1 AND project = $2 AND deleted_at IS NULL
		  AND (scope = 'project' OR (scope = 'personal' AND created_by = $3))
		ORDER BY created_at ASC
	`, sessionSyncID, project, userID)
	if err != nil {
		return nil, fmt.Errorf("session observations: %w", err)
	}
	defer rows.Close()

	return scanObservationRows(rows)
}

// ─── Observations (T10) ─────────────────────────────────────────────────────

// ListRecentObservations returns recent observations for a project.
func (s *Store) ListRecentObservations(ctx context.Context, project, scope, userID string, limit int) ([]map[string]any, error) {
	return s.listObservations(ctx, project, scope, userID, limit)
}

// ListAllObservations returns all observations for a project, paginated.
func (s *Store) ListAllObservations(ctx context.Context, project, scope, userID string, limit int) ([]map[string]any, error) {
	return s.listObservations(ctx, project, scope, userID, limit)
}

func (s *Store) listObservations(ctx context.Context, project, scope, userID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}

	// Build scope filter
	query := `
		SELECT numeric_id, sync_id, session_id, type, title, content, tool_name,
			scope, topic_key, revision_count, created_at::text, updated_at::text
		FROM observations
		WHERE project = $1 AND deleted_at IS NULL
		  AND (scope = 'project' OR (scope = 'personal' AND created_by = $2))
	`
	args := []any{project, userID}

	if scope != "" {
		query += ` AND scope = $3`
		args = append(args, scope)
	}

	query += ` ORDER BY updated_at DESC LIMIT ` + fmt.Sprintf("%d", limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list observations: %w", err)
	}
	defer rows.Close()

	return scanObservationRows(rows)
}

// GetTimeline returns chronological context around an observation.
func (s *Store) GetTimeline(ctx context.Context, obsID, project, userID string, before, after int) (map[string]any, error) {
	if before <= 0 {
		before = 5
	}
	if after <= 0 {
		after = 5
	}

	// Get target observation.
	var (
		numericID                                            int64
		syncID, sessionID, typ, title, content               string
		toolName                                             *string
		scope                                                string
		topicKey                                             *string
		revisionCount                                        int
		createdAt, updatedAt                                 string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT numeric_id, sync_id, session_id, type, title, content, tool_name,
			scope, topic_key, revision_count, created_at::text, updated_at::text
		FROM observations
		WHERE id = $1 AND project = $2 AND deleted_at IS NULL
		  AND (scope = 'project' OR (scope = 'personal' AND created_by = $3))
	`, obsID, project, userID).Scan(
		&numericID, &syncID, &sessionID, &typ, &title, &content,
		&toolName, &scope, &topicKey, &revisionCount, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("target observation not found: %w", err)
	}

	target := map[string]any{
		"numeric_id":     numericID,
		"sync_id":        syncID,
		"session_id":     sessionID,
		"type":           typ,
		"title":          title,
		"content":        content,
		"tool_name":      toolName,
		"scope":          scope,
		"topic_key":      topicKey,
		"revision_count": revisionCount,
		"created_at":     createdAt,
		"updated_at":     updatedAt,
	}

	// Observations before target.
	beforeRows, err := s.pool.Query(ctx, `
		SELECT numeric_id, sync_id, session_id, type, title, content, tool_name,
			scope, topic_key, revision_count, created_at::text, updated_at::text
		FROM observations
		WHERE project = $1 AND deleted_at IS NULL AND created_at < $2::timestamptz
		  AND (scope = 'project' OR (scope = 'personal' AND created_by = $3))
		ORDER BY created_at DESC
		LIMIT $4
	`, project, createdAt, userID, before)
	if err != nil {
		return nil, fmt.Errorf("timeline before: %w", err)
	}
	defer beforeRows.Close()

	beforeObs, err := scanObservationRows(beforeRows)
	if err != nil {
		return nil, err
	}

	// Observations after
	afterRows, err := s.pool.Query(ctx, `
		SELECT numeric_id, sync_id, session_id, type, title, content, tool_name,
			scope, topic_key, revision_count, created_at::text, updated_at::text
		FROM observations
		WHERE project = $1 AND deleted_at IS NULL AND created_at > $2::timestamptz
		  AND (scope = 'project' OR (scope = 'personal' AND created_by = $3))
		ORDER BY created_at ASC
		LIMIT $4
	`, project, createdAt, userID, after)
	if err != nil {
		return nil, fmt.Errorf("timeline after: %w", err)
	}
	defer afterRows.Close()

	afterObs, err := scanObservationRows(afterRows)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"target": target,
		"before": beforeObs,
		"after":  afterObs,
	}, nil
}

// ─── Prompts (T11) ──────────────────────────────────────────────────────────

// ListRecentPrompts returns recent prompts for a user in a project.
func (s *Store) ListRecentPrompts(ctx context.Context, project, userID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}

	rows, err := s.pool.Query(ctx, `
		SELECT numeric_id, sync_id, session_id, content, project, created_at::text
		FROM prompts
		WHERE project = $1 AND user_id = $2
		ORDER BY created_at DESC
		LIMIT $3
	`, project, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list prompts: %w", err)
	}
	defer rows.Close()

	return scanPromptRows(rows)
}

// SearchPrompts searches prompt content using ILIKE.
func (s *Store) SearchPrompts(ctx context.Context, query, project, userID string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}

	rows, err := s.pool.Query(ctx, `
		SELECT numeric_id, sync_id, session_id, content, project, created_at::text
		FROM prompts
		WHERE project = $1 AND user_id = $2 AND content ILIKE '%' || $3 || '%'
		ORDER BY created_at DESC
		LIMIT $4
	`, project, userID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search prompts: %w", err)
	}
	defer rows.Close()

	return scanPromptRows(rows)
}

// ─── PassiveCapture (T26) ────────────────────────────────────────────────────

// PassiveCapture creates a session (if needed) and stores extracted observations atomically.
// This is a simplified cloud version — the learning extraction happens client-side;
// the server receives pre-extracted observations.
func (s *Store) PassiveCapture(ctx context.Context, req PassiveCaptureRequest, userID, project string) (*PassiveCaptureResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Ensure session exists.
	sessionSyncID := req.SessionID
	if sessionSyncID != "" {
		seq, seqErr := NextSeq(ctx, tx, project)
		if seqErr != nil {
			return nil, seqErr
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO sessions (sync_id, project, user_id, started_at, server_seq)
			VALUES ($1, $2, $3, now(), $4)
			ON CONFLICT (sync_id, project) DO NOTHING
		`, sessionSyncID, project, userID, seq)
		if err != nil {
			return nil, fmt.Errorf("ensure session: %w", err)
		}
	}

	// Insert each observation.
	saved := 0
	for _, obs := range req.Observations {
		seq, seqErr := NextSeq(ctx, tx, project)
		if seqErr != nil {
			return nil, seqErr
		}

		syncID := obs.SyncID
		if syncID == "" {
			syncID = "obs-" + randomHex(8)
		}
		scope := obs.Scope
		if scope == "" {
			scope = "project"
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO observations (sync_id, session_id, type, title, content, tool_name,
				project, scope, topic_key, created_by, updated_by, server_seq)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, $11)
			ON CONFLICT (sync_id, project) DO UPDATE SET
				title = EXCLUDED.title, content = EXCLUDED.content, type = EXCLUDED.type,
				updated_by = EXCLUDED.updated_by, updated_at = now(), server_seq = EXCLUDED.server_seq
		`, syncID, sessionSyncID, obs.Type, obs.Title, obs.Content,
			nullStr(obs.ToolName), project, scope, nullStr(obs.TopicKey), userID, seq)
		if err != nil {
			return nil, fmt.Errorf("insert observation: %w", err)
		}
		saved++
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Get session numeric_id.
	var sessionNumericID int64
	if sessionSyncID != "" {
		_ = s.pool.QueryRow(ctx,
			`SELECT numeric_id FROM sessions WHERE sync_id = $1 AND project = $2`,
			sessionSyncID, project).Scan(&sessionNumericID)
	}

	return &PassiveCaptureResult{
		SessionNumericID: sessionNumericID,
		Saved:            saved,
	}, nil
}

// PassiveCaptureRequest is the input for the passive-capture endpoint.
type PassiveCaptureRequest struct {
	SessionID    string                      `json:"session_id"`
	Observations []PassiveCaptureObservation `json:"observations"`
}

// PassiveCaptureObservation is a single observation in a passive capture request.
type PassiveCaptureObservation struct {
	SyncID   string `json:"sync_id,omitempty"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	ToolName string `json:"tool_name,omitempty"`
	Scope    string `json:"scope,omitempty"`
	TopicKey string `json:"topic_key,omitempty"`
}

// PassiveCaptureResult is the response from passive-capture.
type PassiveCaptureResult struct {
	SessionNumericID int64 `json:"session_numeric_id"`
	Saved            int   `json:"saved"`
}

// ─── MigrateProject (T27) ───────────────────────────────────────────────────

// MigrateProject renames all entities from oldProject to newProject.
func (s *Store) MigrateProject(ctx context.Context, oldProject, newProject, userID string) (*MigrateProjectResult, error) {
	if oldProject == "" || newProject == "" || oldProject == newProject {
		return &MigrateProjectResult{}, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var result MigrateProjectResult

	tag, err := tx.Exec(ctx, `UPDATE observations SET project = $1 WHERE project = $2`, newProject, oldProject)
	if err != nil {
		return nil, fmt.Errorf("migrate observations: %w", err)
	}
	result.ObservationsUpdated = tag.RowsAffected()

	tag, err = tx.Exec(ctx, `UPDATE sessions SET project = $1 WHERE project = $2`, newProject, oldProject)
	if err != nil {
		return nil, fmt.Errorf("migrate sessions: %w", err)
	}
	result.SessionsUpdated = tag.RowsAffected()

	tag, err = tx.Exec(ctx, `UPDATE prompts SET project = $1 WHERE project = $2`, newProject, oldProject)
	if err != nil {
		return nil, fmt.Errorf("migrate prompts: %w", err)
	}
	result.PromptsUpdated = tag.RowsAffected()

	tag, err = tx.Exec(ctx, `UPDATE observation_revisions SET project = $1 WHERE project = $2`, newProject, oldProject)
	if err != nil {
		return nil, fmt.Errorf("migrate revisions: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	result.Migrated = result.ObservationsUpdated > 0 || result.SessionsUpdated > 0 || result.PromptsUpdated > 0
	return &result, nil
}

// MigrateProjectResult is the response from project migration.
type MigrateProjectResult struct {
	Migrated            bool  `json:"migrated"`
	ObservationsUpdated int64 `json:"observations_updated"`
	SessionsUpdated     int64 `json:"sessions_updated"`
	PromptsUpdated      int64 `json:"prompts_updated"`
}

// ─── Row scanners ────────────────────────────────────────────────────────────

// scanObservationRows scans observation rows into maps. Expects columns:
// numeric_id, sync_id, session_id, type, title, content, tool_name,
// scope, topic_key, revision_count, created_at, updated_at
func scanObservationRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]map[string]any, error) {
	var result []map[string]any
	for rows.Next() {
		var (
			numericID                                            int64
			syncID, sessionID, typ, title, content               string
			toolName                                             *string
			scope                                                string
			topicKey                                             *string
			revisionCount                                        int
			createdAt, updatedAt                                 string
		)
		if err := rows.Scan(
			&numericID, &syncID, &sessionID, &typ, &title, &content,
			&toolName, &scope, &topicKey, &revisionCount,
			&createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan observation: %w", err)
		}
		result = append(result, map[string]any{
			"numeric_id":     numericID,
			"sync_id":        syncID,
			"session_id":     sessionID,
			"type":           typ,
			"title":          title,
			"content":        content,
			"tool_name":      toolName,
			"scope":          scope,
			"topic_key":      topicKey,
			"revision_count": revisionCount,
			"created_at":     createdAt,
			"updated_at":     updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result == nil {
		result = []map[string]any{}
	}
	return result, nil
}

// scanPromptRows scans prompt rows into maps. Expects columns:
// numeric_id, sync_id, session_id, content, project, created_at
func scanPromptRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]map[string]any, error) {
	var result []map[string]any
	for rows.Next() {
		var (
			numericID              int64
			syncID, sessionID      string
			content, project2      string
			createdAt              string
		)
		if err := rows.Scan(&numericID, &syncID, &sessionID, &content, &project2, &createdAt); err != nil {
			return nil, fmt.Errorf("scan prompt: %w", err)
		}
		result = append(result, map[string]any{
			"numeric_id": numericID,
			"sync_id":    syncID,
			"session_id": sessionID,
			"content":    content,
			"project":    project2,
			"created_at": createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result == nil {
		result = []map[string]any{}
	}
	return result, nil
}
