package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrSessionAuthorityNotFound = errors.New("cloudstore: session authority not found")
var ErrPromptPairClaimConflict = errors.New("cloudstore: prompt pair claim conflict")

type PromptPairClaim struct {
	SessionID     string
	SourceInboxID string
	SyncID        string
	PromptProject string
	ClaimedBy     string
	ClaimedAt     time.Time
}

// ClaimPromptPair persists an already-authorized binding. The caller must verify
// the actor's grants for both the registered owner and the prompt project.
func (cs *CloudStore) ClaimPromptPair(ctx context.Context, sessionID, sourceInboxID, syncID, promptProject, actor string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	sessionID, sourceInboxID, syncID, promptProject, actor = strings.TrimSpace(sessionID), strings.TrimSpace(sourceInboxID), strings.TrimSpace(syncID), strings.TrimSpace(promptProject), strings.TrimSpace(actor)
	if sessionID == "" || sourceInboxID == "" || syncID == "" || promptProject == "" || actor == "" {
		return fmt.Errorf("cloudstore: session id, inbox id, sync id, prompt project and actor are required")
	}
	var inserted string
	err := cs.db.QueryRowContext(ctx, `
  INSERT INTO cloud_prompt_pair_claims (session_id, source_inbox_id, sync_id, prompt_project, claimed_by)
  SELECT session_id, $2, $3, $4, $5 FROM cloud_session_authority WHERE session_id = $1
  ON CONFLICT DO NOTHING RETURNING sync_id`, sessionID, sourceInboxID, syncID, promptProject, actor).Scan(&inserted)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("cloudstore: insert prompt pair claim: %w", err)
	}
	existing, err := cs.GetPromptPairClaim(ctx, sessionID, sourceInboxID)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.SyncID == syncID && existing.PromptProject == promptProject {
			return nil
		}
		return fmt.Errorf("%w: pair already bound", ErrPromptPairClaimConflict)
	}
	var registered bool
	if err := cs.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM cloud_session_authority WHERE session_id = $1)`, sessionID).Scan(&registered); err != nil {
		return fmt.Errorf("cloudstore: check session authority: %w", err)
	}
	if !registered {
		return fmt.Errorf("%w: %q", ErrSessionAuthorityNotFound, sessionID)
	}
	return fmt.Errorf("%w: sync id already bound", ErrPromptPairClaimConflict)
}

// GetPromptPairClaim returns nil for an unclaimed pair; chunk indexes never confer a claim.
func (cs *CloudStore) GetPromptPairClaim(ctx context.Context, sessionID, sourceInboxID string) (*PromptPairClaim, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	sessionID, sourceInboxID = strings.TrimSpace(sessionID), strings.TrimSpace(sourceInboxID)
	if sessionID == "" || sourceInboxID == "" {
		return nil, fmt.Errorf("cloudstore: session id and inbox id are required")
	}
	var claim PromptPairClaim
	err := cs.db.QueryRowContext(ctx, `SELECT session_id, source_inbox_id, sync_id, prompt_project, claimed_by, claimed_at FROM cloud_prompt_pair_claims WHERE session_id = $1 AND source_inbox_id = $2`, sessionID, sourceInboxID).Scan(&claim.SessionID, &claim.SourceInboxID, &claim.SyncID, &claim.PromptProject, &claim.ClaimedBy, &claim.ClaimedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: get prompt pair claim: %w", err)
	}
	return &claim, nil
}
