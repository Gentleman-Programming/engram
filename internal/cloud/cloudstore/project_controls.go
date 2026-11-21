package cloudstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ProjectSyncControl holds the per-project sync enable/pause record.
// The backing table is cloud_project_controls (Postgres, added in migrate()).
type ProjectSyncControl struct {
	Project      string
	SyncEnabled  bool
	PausedReason *string
	UpdatedAt    string
	UpdatedBy    *string
}

// IsProjectSyncEnabled returns whether sync is enabled for the project.
// An absent row defaults to enabled=true (safe default).
func (cs *CloudStore) IsProjectSyncEnabled(project string) (bool, error) {
	if cs == nil || cs.db == nil {
		return true, nil
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return true, nil
	}
	var enabled bool
	err := cs.db.QueryRowContext(
		context.Background(),
		`SELECT sync_enabled FROM cloud_project_controls WHERE project = $1`,
		project,
	).Scan(&enabled)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		// BW8: Fail closed — return (false, error) so callers that ignore the error
		// do not silently permit mutations for projects with unknown sync state.
		return false, fmt.Errorf("cloudstore: IsProjectSyncEnabled: %w", err)
	}
	return enabled, nil
}

// SetProjectSyncEnabled upserts the project sync control record.
func (cs *CloudStore) SetProjectSyncEnabled(project string, enabled bool, updatedBy, reason string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("cloudstore: SetProjectSyncEnabled: project is empty")
	}
	var reasonPtr *string
	if r := strings.TrimSpace(reason); r != "" {
		reasonPtr = &r
	}
	var updatedByPtr *string
	if u := strings.TrimSpace(updatedBy); u != "" {
		updatedByPtr = &u
	}
	_, err := cs.db.ExecContext(
		context.Background(),
		`INSERT INTO cloud_project_controls (project, sync_enabled, paused_reason, updated_by, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (project) DO UPDATE SET
		     sync_enabled  = EXCLUDED.sync_enabled,
		     paused_reason = EXCLUDED.paused_reason,
		     updated_by    = EXCLUDED.updated_by,
		     updated_at    = EXCLUDED.updated_at`,
		project, enabled, reasonPtr, updatedByPtr, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("cloudstore: SetProjectSyncEnabled: %w", err)
	}
	// W4: invalidate the dashboard read model cache so SystemHealth / paused_projects
	// reflect the change immediately without waiting for the next chunk write.
	cs.invalidateDashboardReadModel()
	return nil
}

// GetProjectSyncControl returns the control record for a project, or nil if absent.
func (cs *CloudStore) GetProjectSyncControl(project string) (*ProjectSyncControl, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return nil, nil
	}
	var ctrl ProjectSyncControl
	var updatedAt time.Time
	err := cs.db.QueryRowContext(
		context.Background(),
		`SELECT project, sync_enabled, paused_reason, updated_by, updated_at
		 FROM cloud_project_controls WHERE project = $1`,
		project,
	).Scan(&ctrl.Project, &ctrl.SyncEnabled, &ctrl.PausedReason, &ctrl.UpdatedBy, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: GetProjectSyncControl: %w", err)
	}
	ctrl.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return &ctrl, nil
}

// ListProjectSyncControls returns all project controls UNION DISTINCT projects
// known from cloud_chunks (projects with no explicit control row default to enabled).
func (cs *CloudStore) ListProjectSyncControls() ([]ProjectSyncControl, error) {
	return cs.listProjectSyncControls(context.Background())
}

func (cs *CloudStore) listProjectSyncControls(ctx context.Context) ([]ProjectSyncControl, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	// UNION ensures projects that have synced chunks appear even without a control row.
	rows, err := cs.db.QueryContext(ctx, `
		SELECT
		    p.project,
		    COALESCE(c.sync_enabled, TRUE)   AS sync_enabled,
		    c.paused_reason,
		    c.updated_by,
		    c.updated_at
		FROM (
		    SELECT DISTINCT project_name AS project FROM cloud_chunks
		    UNION
		    SELECT project FROM cloud_project_controls
		) p
		LEFT JOIN cloud_project_controls c ON c.project = p.project
		ORDER BY p.project
	`)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: ListProjectSyncControls: %w", err)
	}
	defer rows.Close()

	var result []ProjectSyncControl
	for rows.Next() {
		var ctrl ProjectSyncControl
		var updatedAt *time.Time
		if err := rows.Scan(
			&ctrl.Project,
			&ctrl.SyncEnabled,
			&ctrl.PausedReason,
			&ctrl.UpdatedBy,
			&updatedAt,
		); err != nil {
			return nil, fmt.Errorf("cloudstore: ListProjectSyncControls scan: %w", err)
		}
		if updatedAt != nil {
			ctrl.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		}
		result = append(result, ctrl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: ListProjectSyncControls iterate: %w", err)
	}
	return result, nil
}
