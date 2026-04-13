package cloudstore

import (
	"context"
	"fmt"

	"github.com/Gentleman-Programming/engram/internal/types"
)

// Search performs full-text search using PostgreSQL tsvector.
// Normalizes user queries to plainto_tsquery (simple, language-agnostic).
// Filters by project and respects scope visibility for the given user.
func (s *Store) Search(ctx context.Context, query, project, userID string, limit int) ([]types.SearchResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	rows, err := s.pool.Query(ctx, `
		SELECT 0, o.sync_id, o.session_id, o.type, o.title, o.content,
			o.tool_name, o.project, o.scope, o.topic_key,
			o.revision_count, 0 AS duplicate_count,
			NULL AS last_seen_at,
			o.created_at::text, o.updated_at::text, o.deleted_at::text,
			ts_rank(o.search_vector, plainto_tsquery('simple', $1)) AS rank
		FROM observations o
		WHERE o.search_vector @@ plainto_tsquery('simple', $1)
		  AND o.project = $2
		  AND o.deleted_at IS NULL
		  AND (
			  o.scope = 'project'
			  OR (o.scope = 'personal' AND o.created_by = $3)
		  )
		ORDER BY rank DESC, o.updated_at DESC
		LIMIT $4
	`, query, project, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var results []types.SearchResult
	for rows.Next() {
		var r types.SearchResult
		if err := rows.Scan(
			&r.ID, &r.SyncID, &r.SessionID, &r.Type, &r.Title, &r.Content,
			&r.ToolName, &r.Project, &r.Scope, &r.TopicKey,
			&r.RevisionCount, &r.DuplicateCount,
			&r.LastSeenAt,
			&r.CreatedAt, &r.UpdatedAt, &r.DeletedAt,
			&r.Rank,
		); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
