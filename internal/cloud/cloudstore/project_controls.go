package cloudstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrProjectSyncPaused = errors.New("cloudstore: project sync paused")

// ErrNotFound is returned when a requested resource does not exist in the store.
var ErrNotFound = errors.New("cloudstore: not found")

type ProjectSyncControl struct {
	Project      string  `json:"project"`
	SyncEnabled  bool    `json:"sync_enabled"`
	PausedReason *string `json:"paused_reason,omitempty"`
	UpdatedAt    string  `json:"updated_at"`
	UpdatedBy    *string `json:"updated_by,omitempty"`
}

func (cs *CloudStore) IsProjectSyncEnabled(project string) (bool, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		return true, nil
	}

	var enabled bool
	err := cs.db.QueryRow(
		`SELECT sync_enabled FROM cloud_project_controls WHERE project = $1`,
		project,
	).Scan(&enabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		return false, fmt.Errorf("cloudstore: get project control: %w", err)
	}
	return enabled, nil
}

func (cs *CloudStore) SetProjectSyncEnabled(project string, enabled bool, updatedBy, reason string) error {
	project = strings.TrimSpace(project)
	if project == "" {
		return fmt.Errorf("cloudstore: project must not be empty")
	}
	reason = strings.TrimSpace(reason)
	if enabled {
		reason = ""
	}

	_, err := cs.db.Exec(
		`INSERT INTO cloud_project_controls (project, sync_enabled, paused_reason, updated_by, updated_at)
		 VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, '')::uuid, NOW())
		 ON CONFLICT (project) DO UPDATE SET
		   sync_enabled = EXCLUDED.sync_enabled,
		   paused_reason = EXCLUDED.paused_reason,
		   updated_by = EXCLUDED.updated_by,
		   updated_at = NOW()`,
		project, enabled, reason, updatedBy,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: set project control: %w", err)
	}
	return nil
}

func (cs *CloudStore) GetProjectSyncControl(project string) (*ProjectSyncControl, error) {
	controls, err := cs.ListProjectSyncControls()
	if err != nil {
		return nil, err
	}
	for i := range controls {
		if controls[i].Project == project {
			return &controls[i], nil
		}
	}
	if strings.TrimSpace(project) == "" {
		return nil, nil
	}
	return &ProjectSyncControl{Project: project, SyncEnabled: true}, nil
}

func (cs *CloudStore) ListProjectSyncControls() ([]ProjectSyncControl, error) {
	rows, err := cs.db.Query(`
		WITH known_projects AS (
			SELECT DISTINCT project FROM cloud_sessions WHERE project <> ''
			UNION
			SELECT DISTINCT COALESCE(project, '') AS project FROM cloud_observations WHERE COALESCE(project, '') <> ''
			UNION
			SELECT DISTINCT COALESCE(project, '') AS project FROM cloud_prompts WHERE COALESCE(project, '') <> ''
			UNION
			SELECT project FROM cloud_project_controls
		)
		SELECT kp.project,
		       COALESCE(cpc.sync_enabled, TRUE) AS sync_enabled,
		       cpc.paused_reason,
		       COALESCE(to_char(cpc.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'), '') AS updated_at,
		       cu.username
		FROM known_projects kp
		LEFT JOIN cloud_project_controls cpc ON cpc.project = kp.project
		LEFT JOIN cloud_users cu ON cu.id = cpc.updated_by
		WHERE kp.project <> ''
		ORDER BY kp.project ASC`)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list project controls: %w", err)
	}
	defer rows.Close()

	var controls []ProjectSyncControl
	for rows.Next() {
		var control ProjectSyncControl
		if err := rows.Scan(&control.Project, &control.SyncEnabled, &control.PausedReason, &control.UpdatedAt, &control.UpdatedBy); err != nil {
			return nil, fmt.Errorf("cloudstore: scan project control: %w", err)
		}
		if control.UpdatedAt == "" {
			control.UpdatedAt = ""
		}
		controls = append(controls, control)
	}
	return controls, rows.Err()
}

func projectFromMutation(entity string, payload json.RawMessage) (string, error) {
	entity = strings.TrimSpace(entity)
	if len(payload) == 0 {
		return "", nil
	}
	var decoded struct {
		Project *string `json:"project"`
	}
	if err := decodeMutationPayload(payload, &decoded); err != nil {
		return "", err
	}
	if decoded.Project == nil {
		return "", nil
	}
	return strings.TrimSpace(*decoded.Project), nil
}

func (cs *CloudStore) ensureProjectSyncEnabled(entity string, payload json.RawMessage) error {
	project, err := projectFromMutation(entity, payload)
	if err != nil {
		return fmt.Errorf("cloudstore: decode project from mutation: %w", err)
	}
	if project == "" {
		return nil
	}
	enabled, err := cs.IsProjectSyncEnabled(project)
	if err != nil {
		return err
	}
	if !enabled {
		return fmt.Errorf("%w: %s", ErrProjectSyncPaused, project)
	}
	return nil
}
