package cloudstore

import (
	"context"
	"fmt"
	"math"
)

// PullEntity represents a single entity in a pull response with its type discriminator.
type PullEntity struct {
	EntityType string         `json:"entity_type"` // observation, session, prompt
	ServerSeq  int64          `json:"server_seq"`
	Data       map[string]any `json:"data"`
}

// PullResult is the response to a pull request.
type PullResult struct {
	Entities []PullEntity `json:"entities"`
	MaxSeq   int64        `json:"max_seq"`
	HasMore  bool         `json:"has_more"`
}

// Pull returns entities (observations, sessions, prompts) since the given server_seq.
// - Observations: project-scoped visible to all members; personal-scoped only to creator
// - Prompts: ALWAYS filtered by userID (privacy)
// - Soft-deleted observations are included (tombstone propagation)
// - MaxSeq is the MINIMUM of last server_seq per entity type (cursor safety)
func (s *Store) Pull(ctx context.Context, project string, sinceSeq int64, userID string, limit int) (*PullResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}

	// Query all three entity types with unified ordering
	rows, err := s.pool.Query(ctx, `
		(
			SELECT 'observation' AS entity_type, server_seq,
				sync_id, session_id, type, title, content, tool_name,
				project, scope, topic_key, revision_count,
				created_by::text, updated_by::text,
				created_at::text, updated_at::text, deleted_at::text
			FROM observations
			WHERE project = $1
			  AND server_seq > $2
			  AND (
				  scope = 'project'
				  OR (scope = 'personal' AND created_by = $3)
			  )
		)
		UNION ALL
		(
			SELECT 'session' AS entity_type, server_seq,
				sync_id, '' AS session_id, '' AS type, '' AS title, '' AS content, NULL AS tool_name,
				project, '' AS scope, NULL AS topic_key, 0 AS revision_count,
				user_id::text AS created_by, user_id::text AS updated_by,
				started_at::text AS created_at, COALESCE(ended_at::text, '') AS updated_at,
				summary AS deleted_at
			FROM sessions
			WHERE project = $1 AND server_seq > $2
		)
		UNION ALL
		(
			SELECT 'prompt' AS entity_type, server_seq,
				sync_id, session_id, '' AS type, '' AS title, content, NULL AS tool_name,
				project, '' AS scope, NULL AS topic_key, 0 AS revision_count,
				user_id::text AS created_by, user_id::text AS updated_by,
				created_at::text, '' AS updated_at, NULL AS deleted_at
			FROM prompts
			WHERE project = $1 AND server_seq > $2 AND user_id = $3
		)
		ORDER BY server_seq ASC
		LIMIT $4
	`, project, sinceSeq, userID, limit+1) // +1 to detect has_more
	if err != nil {
		return nil, fmt.Errorf("pull query: %w", err)
	}
	defer rows.Close()

	var entities []PullEntity
	for rows.Next() {
		var (
			entityType                                           string
			serverSeq                                            int64
			syncID, sessionID, typ, title, content               string
			toolName                                             *string
			project2, scope                                      string
			topicKey                                             *string
			revisionCount                                        int
			createdBy, updatedBy, createdAt, updatedAt           string
			deletedAt                                            *string
		)
		if err := rows.Scan(
			&entityType, &serverSeq,
			&syncID, &sessionID, &typ, &title, &content, &toolName,
			&project2, &scope, &topicKey, &revisionCount,
			&createdBy, &updatedBy, &createdAt, &updatedAt, &deletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan pull row: %w", err)
		}

		data := map[string]any{
			"sync_id":        syncID,
			"session_id":     sessionID,
			"type":           typ,
			"title":          title,
			"content":        content,
			"tool_name":      toolName,
			"project":        project2,
			"scope":          scope,
			"topic_key":      topicKey,
			"revision_count": revisionCount,
			"created_by":     createdBy,
			"updated_by":     updatedBy,
			"created_at":     createdAt,
			"updated_at":     updatedAt,
			"deleted_at":     deletedAt,
			"server_seq":     serverSeq,
		}

		entities = append(entities, PullEntity{
			EntityType: entityType,
			ServerSeq:  serverSeq,
			Data:       data,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pull rows: %w", err)
	}

	hasMore := len(entities) > limit
	if hasMore {
		entities = entities[:limit]
	}

	// Cursor safety: max_seq = MIN of last server_seq per entity type
	maxSeq := computeCursorSafeMaxSeq(entities)

	return &PullResult{
		Entities: entities,
		MaxSeq:   maxSeq,
		HasMore:  hasMore,
	}, nil
}

// computeCursorSafeMaxSeq returns the minimum of the last server_seq per entity type.
// This ensures no entity type is skipped between pages.
func computeCursorSafeMaxSeq(entities []PullEntity) int64 {
	if len(entities) == 0 {
		return 0
	}

	lastPerType := make(map[string]int64)
	for _, e := range entities {
		lastPerType[e.EntityType] = e.ServerSeq
	}

	if len(lastPerType) == 0 {
		return 0
	}

	minLast := int64(math.MaxInt64)
	for _, seq := range lastPerType {
		if seq < minLast {
			minLast = seq
		}
	}

	return minLast
}
