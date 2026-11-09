package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrSessionAuthorityConflict means a session ID is already owned by another project.
var ErrSessionAuthorityConflict = errors.New("cloudstore: session authority conflict")

type SessionAuthority struct {
	SessionID    string
	OwnerProject string
	RegisteredBy string
	RegisteredAt time.Time
}

// RegisterSessionAuthority persists an already-authorized registration. Callers must
// independently authenticate the actor and authorize the owner project.
func (cs *CloudStore) RegisterSessionAuthority(ctx context.Context, sessionID, project, actor string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	sessionID, project, actor = strings.TrimSpace(sessionID), strings.TrimSpace(project), strings.TrimSpace(actor)
	if sessionID == "" || project == "" || actor == "" {
		return fmt.Errorf("cloudstore: session id, project and actor are required")
	}
	// The primary key serializes concurrent writers without modifying the
	// original registration on replay or conflict.
	var owner string
	err := cs.db.QueryRowContext(ctx, `
  INSERT INTO cloud_session_authority (session_id, owner_project, registered_by)
  VALUES ($1, $2, $3)
  ON CONFLICT (session_id) DO NOTHING
  RETURNING owner_project`, sessionID, project, actor).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		err = cs.db.QueryRowContext(ctx, `SELECT owner_project FROM cloud_session_authority WHERE session_id = $1`, sessionID).Scan(&owner)
	}
	if err != nil {
		return fmt.Errorf("cloudstore: register session authority: %w", err)
	}
	if owner != project {
		return fmt.Errorf("%w: session %q is owned by another project", ErrSessionAuthorityConflict, sessionID)
	}
	return nil
}

// GetSessionAuthority reports nil for an unregistered session, regardless of
// whether a chunk-derived session index contains that ID.
func (cs *CloudStore) GetSessionAuthority(ctx context.Context, sessionID string) (*SessionAuthority, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("cloudstore: session id is required")
	}
	var authority SessionAuthority
	err := cs.db.QueryRowContext(ctx, `SELECT session_id, owner_project, registered_by, registered_at FROM cloud_session_authority WHERE session_id = $1`, sessionID).
		Scan(&authority.SessionID, &authority.OwnerProject, &authority.RegisteredBy, &authority.RegisteredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: get session authority: %w", err)
	}
	return &authority, nil
}
