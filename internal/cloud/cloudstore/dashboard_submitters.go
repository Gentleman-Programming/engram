package cloudstore

import (
	"context"
	"maps"
	"slices"
	"time"
)

// DashboardSubmitterRow counts submitted chunks by authenticated principal.
// It leaves the original credited author in the chunk manifest.
type DashboardSubmitterRow struct {
	PrincipalID int64
	DisplayName string
	Username    string
	Chunks      int
	Projects    int
	LastChunkAt time.Time
}

// ListAuthenticatedChunkSubmitters reports only verified submissions in the
// canonical projects granted to this managed dashboard session.
func (s *DashboardScopedStore) ListAuthenticatedChunkSubmitters(ctx context.Context) ([]DashboardSubmitterRow, error) {
	if len(s.allowed) == 0 {
		return []DashboardSubmitterRow{}, nil
	}
	_, all := s.allowed["*"]
	rows, err := s.base.db.QueryContext(ctx, `
		SELECT p.id, p.display_name, COALESCE(u.username, ''), c.chunks, c.projects, c.last_chunk_at
		FROM (
			SELECT submitted_by_principal_id, count(*) AS chunks,
				count(DISTINCT project_name COLLATE pg_catalog."C") AS projects, max(created_at) AS last_chunk_at
			FROM cloud_chunks
			WHERE submitted_by_principal_id IS NOT NULL
				AND ($1 OR project_name COLLATE pg_catalog."C" OPERATOR(pg_catalog.=) ANY($2::pg_catalog.text[]))
			GROUP BY submitted_by_principal_id
		) c
		JOIN cloud_principals p ON p.id = c.submitted_by_principal_id
		LEFT JOIN cloud_human_users u ON u.principal_id = p.id
		ORDER BY lower(p.display_name), p.id
	`, all, slices.Collect(maps.Keys(s.allowed)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var result []DashboardSubmitterRow
	for rows.Next() {
		var row DashboardSubmitterRow
		if err := rows.Scan(&row.PrincipalID, &row.DisplayName, &row.Username, &row.Chunks, &row.Projects, &row.LastChunkAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
