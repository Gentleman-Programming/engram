package cloudstore

import (
	"context"
	cryptoRand "crypto/rand"
	"fmt"

	"github.com/Gentleman-Programming/engram/internal/format"
	"github.com/Gentleman-Programming/engram/internal/types"
)

// CreateObservation creates an observation directly (for cloud-only clients).
// Applies LWW + revision logic for topic_key conflicts, same as push protocol.
func (s *Store) CreateObservation(ctx context.Context, p types.AddObservationParams, project, userID string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	seq, err := NextSeq(ctx, tx, project)
	if err != nil {
		return "", err
	}

	scope := p.Scope
	if scope == "" {
		scope = "project"
	}

	syncID := "obs-" + randomHex(8)
	topicKey := nullStr(p.TopicKey)

	// Check topic_key conflict
	if p.TopicKey != "" {
		var existingID string
		err := tx.QueryRow(ctx, `
			SELECT id FROM observations
			WHERE topic_key = $1 AND project = $2 AND scope = $3 AND deleted_at IS NULL
			FOR UPDATE
		`, p.TopicKey, project, scope).Scan(&existingID)

		if err == nil {
			// Conflict — save revision, then update
			if _, err := tx.Exec(ctx, `
				INSERT INTO observation_revisions (observation_id, project, title, content, type, topic_key, updated_by, revision_number)
				SELECT id, project, title, content, type, topic_key, updated_by, revision_count
				FROM observations WHERE id = $1
			`, existingID); err != nil {
				return "", fmt.Errorf("save revision: %w", err)
			}

			_, err = tx.Exec(ctx, `
				UPDATE observations SET
					sync_id = $1, session_id = $2, type = $3, title = $4, content = $5,
					tool_name = $6, scope = $7, topic_key = $8,
					updated_by = $9, updated_at = now(), server_seq = $10,
					revision_count = revision_count + 1
				WHERE id = $11
			`, syncID, p.SessionID, p.Type, p.Title, p.Content,
				nullStr(p.ToolName), scope, topicKey, userID, seq, existingID)
			if err != nil {
				return "", fmt.Errorf("update conflict obs: %w", err)
			}
			return existingID, tx.Commit(ctx)
		}
	}

	// No conflict — insert new
	var obsID string
	err = tx.QueryRow(ctx, `
		INSERT INTO observations (sync_id, session_id, type, title, content, tool_name, project, scope, topic_key, created_by, updated_by, server_seq)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, $11)
		RETURNING id
	`, syncID, p.SessionID, p.Type, p.Title, p.Content,
		nullStr(p.ToolName), project, scope, topicKey, userID, seq,
	).Scan(&obsID)
	if err != nil {
		return "", fmt.Errorf("insert obs: %w", err)
	}

	return obsID, tx.Commit(ctx)
}

// GetObservation retrieves a single observation by UUID.
func (s *Store) GetObservation(ctx context.Context, id, project, userID string) (*types.Observation, error) {
	var o types.Observation
	err := s.pool.QueryRow(ctx, `
		SELECT 0, sync_id, session_id, type, title, content, tool_name, project, scope, topic_key,
			revision_count, 0, NULL,
			created_at::text, updated_at::text, deleted_at::text
		FROM observations
		WHERE id = $1 AND project = $2
		  AND (scope = 'project' OR (scope = 'personal' AND created_by = $3))
	`, id, project, userID).Scan(
		&o.ID, &o.SyncID, &o.SessionID, &o.Type, &o.Title, &o.Content,
		&o.ToolName, &o.Project, &o.Scope, &o.TopicKey,
		&o.RevisionCount, &o.DuplicateCount, &o.LastSeenAt,
		&o.CreatedAt, &o.UpdatedAt, &o.DeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get observation: %w", err)
	}
	return &o, nil
}

// GetStats returns basic statistics for a project.
func (s *Store) GetStats(ctx context.Context, project, userID string) (*types.Stats, error) {
	var stats types.Stats

	s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM sessions WHERE project = $1
	`, project).Scan(&stats.TotalSessions)

	s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM observations
		WHERE project = $1 AND deleted_at IS NULL
		  AND (scope = 'project' OR (scope = 'personal' AND created_by = $2))
	`, project, userID).Scan(&stats.TotalObservations)

	s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM prompts WHERE project = $1 AND user_id = $2
	`, project, userID).Scan(&stats.TotalPrompts)

	rows, _ := s.pool.Query(ctx, "SELECT DISTINCT name FROM projects ORDER BY name")
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				stats.Projects = append(stats.Projects, name)
			}
		}
	}

	return &stats, nil
}

// GetContext returns formatted context for a project using the shared format package.
func (s *Store) GetContext(ctx context.Context, project, userID string) (string, error) {
	// Get recent sessions
	rows, err := s.pool.Query(ctx, `
		SELECT sync_id, project, started_at::text, ended_at::text, summary,
			(SELECT COUNT(*) FROM observations o WHERE o.project = s.project AND o.session_id = s.sync_id)
		FROM sessions s WHERE s.project = $1
		ORDER BY s.started_at DESC LIMIT 5
	`, project)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var sessions []types.SessionSummary
	for rows.Next() {
		var ss types.SessionSummary
		if err := rows.Scan(&ss.ID, &ss.Project, &ss.StartedAt, &ss.EndedAt, &ss.Summary, &ss.ObservationCount); err != nil {
			return "", err
		}
		sessions = append(sessions, ss)
	}

	// Get recent observations
	obsRows, err := s.pool.Query(ctx, `
		SELECT type, title, content FROM observations
		WHERE project = $1 AND deleted_at IS NULL
		  AND (scope = 'project' OR (scope = 'personal' AND created_by = $2))
		ORDER BY updated_at DESC LIMIT 20
	`, project, userID)
	if err != nil {
		return "", err
	}
	defer obsRows.Close()

	var obs []types.Observation
	for obsRows.Next() {
		var o types.Observation
		if err := obsRows.Scan(&o.Type, &o.Title, &o.Content); err != nil {
			return "", err
		}
		obs = append(obs, o)
	}

	// Get recent prompts (user's own only)
	promptRows, err := s.pool.Query(ctx, `
		SELECT content, created_at::text FROM prompts
		WHERE project = $1 AND user_id = $2
		ORDER BY created_at DESC LIMIT 10
	`, project, userID)
	if err != nil {
		return "", err
	}
	defer promptRows.Close()

	var prompts []types.Prompt
	for promptRows.Next() {
		var p types.Prompt
		if err := promptRows.Scan(&p.Content, &p.CreatedAt); err != nil {
			return "", err
		}
		prompts = append(prompts, p)
	}

	// Use shared format package
	return format.Context(sessions, obs, prompts), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = randReader(b)
	return fmt.Sprintf("%x", b)
}

// randReader is injected for testing
var randReader = cryptoRand.Read
