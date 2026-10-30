package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrPromptSourceAttestationUnbound = errors.New("cloudstore: prompt source attestation requires exact session authority and prompt pair claim")

// PromptSourceAttestation is an independent, append-only record of an explicit
// human source assertion; it does not alter registration or claim audit history.
type PromptSourceAttestation struct {
	ID            int64
	SessionID     string
	SourceInboxID string
	SyncID        string
	OwnerProject  string
	PromptProject string
	ActorID       string
	AttestedAt    time.Time
}

// VerifyPromptSourceAttestation checks only the immutable audit row identity.
// Authorization of both projects must happen before this global lookup.
func (cs *CloudStore) VerifyPromptSourceAttestation(ctx context.Context, id int64, sessionID, sourceInboxID, syncID, ownerProject, promptProject string) (bool, error) {
	if cs == nil || cs.db == nil {
		return false, fmt.Errorf("cloudstore: not initialized")
	}
	if id <= 0 || sessionID == "" || sourceInboxID == "" || syncID == "" || ownerProject == "" || promptProject == "" {
		return false, nil
	}
	var found int
	err := cs.db.QueryRowContext(ctx, `SELECT 1 FROM cloud_prompt_source_attestations
  WHERE id = $1 AND session_id = $2 AND source_inbox_id = $3 AND sync_id = $4
  AND owner_project = $5 AND prompt_project = $6`, id, sessionID, sourceInboxID, syncID, ownerProject, promptProject).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cloudstore: verify prompt source attestation: %w", err)
	}
	return found == 1, nil
}

// AttestPromptSource records an already-authorized human assertion. Storage
// does not check grants: the future authenticated server route must verify the
// actor's current grants to both owner and prompt projects before calling it.
func (cs *CloudStore) AttestPromptSource(ctx context.Context, sessionID, sourceInboxID, syncID, ownerProject, promptProject, actorID string) (*PromptSourceAttestation, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	values := []*string{&sessionID, &sourceInboxID, &syncID, &ownerProject, &promptProject, &actorID}
	for _, value := range values {
		*value = strings.TrimSpace(*value)
		if *value == "" {
			return nil, fmt.Errorf("cloudstore: all attestation fields are required")
		}
	}
	const query = `INSERT INTO cloud_prompt_source_attestations
		(session_id, source_inbox_id, sync_id, owner_project, prompt_project, actor_id)
		SELECT a.session_id, c.source_inbox_id, c.sync_id, a.owner_project, c.prompt_project, $6
		FROM cloud_session_authority a
		JOIN cloud_prompt_pair_claims c ON c.session_id = a.session_id
		WHERE a.session_id = $1 AND c.source_inbox_id = $2 AND c.sync_id = $3
		AND a.owner_project = $4 AND c.prompt_project = $5
		RETURNING id, session_id, source_inbox_id, sync_id, owner_project, prompt_project, actor_id, attested_at`
	var record PromptSourceAttestation
	err := cs.db.QueryRowContext(ctx, query, sessionID, sourceInboxID, syncID, ownerProject, promptProject, actorID).
		Scan(&record.ID, &record.SessionID, &record.SourceInboxID, &record.SyncID, &record.OwnerProject, &record.PromptProject, &record.ActorID, &record.AttestedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPromptSourceAttestationUnbound
	}
	if err != nil {
		return nil, fmt.Errorf("cloudstore: attest prompt source: %w", err)
	}
	return &record, nil
}
