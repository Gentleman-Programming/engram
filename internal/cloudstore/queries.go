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
