package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const sessionIdentityMigrationReason = "session_identity_migrated"

// SessionIdentityRepairPlan records the read-only impact of replacing one blank
// identity. Apply rechecks the complete evidence under the SQLite writer lock.
type SessionIdentityRepairPlan struct {
	SourceID         string `json:"source_id"`
	ReplacementID    string `json:"replacement_id"`
	Project          string `json:"project"`
	Observations     int64  `json:"observations"`
	Prompts          int64  `json:"prompts"`
	RetiredMutations int64  `json:"retired_mutations"`
	Enrolled         bool   `json:"enrolled"`
	fingerprint      [32]byte
}

type SessionIdentityRepairResult struct {
	SessionIdentityRepairPlan
	BackupPath         string `json:"backup_path"`
	PublishedMutations int64  `json:"published_mutations"`
}

type identityMutation struct {
	seq     int64
	entity  string
	key     string
	payload string
}

type identitySnapshot struct {
	plan         SessionIdentityRepairPlan
	session      syncSessionPayload
	observations []syncObservationPayload
	prompts      []syncPromptPayload
	mutations    []identityMutation
}

func (s *Store) PlanSessionIdentityRepair(sourceID, replacementID string) (SessionIdentityRepairPlan, error) {
	var snapshot identitySnapshot
	err := s.withReadTx(func(tx *sql.Tx) error {
		var err error
		snapshot, err = s.inspectSessionIdentityTx(tx, sourceID, replacementID)
		return err
	})
	if err != nil {
		return SessionIdentityRepairPlan{}, err
	}
	return snapshot.plan, nil
}

// ApplySessionIdentityRepair leaves its pre-apply backup on disk even if the
// transaction fails. No corrected mutation is published before commit.
func (s *Store) ApplySessionIdentityRepair(plan SessionIdentityRepairPlan) (SessionIdentityRepairResult, error) {
	if plan.fingerprint == ([32]byte{}) {
		return SessionIdentityRepairResult{}, errors.New("session identity repair requires a store plan")
	}
	backup, err := s.BackupSQLite()
	if err != nil {
		return SessionIdentityRepairResult{}, err
	}
	result := SessionIdentityRepairResult{BackupPath: backup}
	err = s.withTx(func(tx *sql.Tx) error {
		result.PublishedMutations = 0
		current, err := s.inspectSessionIdentityTx(tx, plan.SourceID, plan.ReplacementID)
		if err != nil {
			return err
		}
		if current.plan.fingerprint != plan.fingerprint {
			return errors.New("session identity repair evidence changed since plan")
		}
		result.SessionIdentityRepairPlan = current.plan
		insert, err := s.execHook(tx, `INSERT INTO sessions(id,project,ownership_mode,directory,started_at,ended_at,summary,runtime_lease_expires_at)
			SELECT ?,project,ownership_mode,directory,started_at,ended_at,summary,runtime_lease_expires_at FROM sessions WHERE id=?`, plan.ReplacementID, plan.SourceID)
		if err := identityAffected(insert, err, 1, "insert canonical session"); err != nil {
			return err
		}
		for _, child := range []struct {
			table string
			count int64
		}{{"observations", current.plan.Observations}, {"user_prompts", current.plan.Prompts}} {
			updated, err := s.execHook(tx, `UPDATE `+child.table+` SET session_id=? WHERE session_id=?`, plan.ReplacementID, plan.SourceID)
			if err := identityAffected(updated, err, child.count, "remap "+child.table); err != nil {
				return err
			}
		}
		for _, mutation := range current.mutations {
			updated, err := s.execHook(tx, `UPDATE sync_mutations SET disposition=?, disposition_reason=?, disposition_evidence=?, disposition_at=datetime('now')
				WHERE seq=? AND target_key=? AND source=? AND op=? AND disposition=? AND acked_at IS NULL`,
				SyncMutationDispositionSuperseded, sessionIdentityMigrationReason,
				fmt.Sprintf("replacement_id=%q", plan.ReplacementID), mutation.seq,
				DefaultSyncTargetKey, SyncSourceLocal, SyncOpUpsert, SyncMutationDispositionPending)
			if err := identityAffected(updated, err, 1, fmt.Sprintf("retire journal seq %d", mutation.seq)); err != nil {
				return err
			}
		}
		deleted, err := s.execHook(tx, `DELETE FROM sessions WHERE id=?`, plan.SourceID)
		if err := identityAffected(deleted, err, 1, "remove legacy session"); err != nil {
			return err
		}
		if !current.plan.Enrolled {
			return s.refreshIdentityRepairLifecycleTx(tx, current.plan.Project)
		}
		session := current.session
		if err := s.appendIdentityRepairMutationTx(tx, SyncEntitySession, session.ID, session.Project, session); err != nil {
			return err
		}
		result.PublishedMutations++
		for _, observation := range current.observations {
			observation.SessionID = session.ID
			if err := s.appendIdentityRepairMutationTx(tx, SyncEntityObservation, observation.SyncID, session.Project, observation); err != nil {
				return err
			}
			result.PublishedMutations++
		}
		for _, prompt := range current.prompts {
			prompt.SessionID = session.ID
			if err := s.appendIdentityRepairMutationTx(tx, SyncEntityPrompt, prompt.SyncID, session.Project, prompt); err != nil {
				return err
			}
			result.PublishedMutations++
		}
		return s.refreshIdentityRepairLifecycleTx(tx, current.plan.Project)
	})
	return result, err
}

func (s *Store) refreshIdentityRepairLifecycleTx(tx *sql.Tx, project string) error {
	if err := s.refreshSyncLifecycleTx(tx, DefaultSyncTargetKey); err != nil {
		return err
	}
	return s.refreshProjectSyncStateTx(tx, project)
}

func identityAffected(result sql.Result, err error, expected int64, action string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if count != expected {
		return fmt.Errorf("%s: affected %d rows, expected %d", action, count, expected)
	}
	return nil
}

// Do not use enqueueSyncMutationTx: its session-upsert path deletes pending
// rows, whereas this migration must preserve every retired sequence and payload.
func (s *Store) appendIdentityRepairMutationTx(tx *sql.Tx, entity, key, project string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	insert, err := s.execHook(tx, `INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project)
		VALUES (?,?,?,?,?,'local',?)`, DefaultSyncTargetKey, entity, key, SyncOpUpsert, string(raw), project)
	if err := identityAffected(insert, err, 1, "append repaired mutation"); err != nil {
		return err
	}
	seq, err := insert.LastInsertId()
	if err != nil {
		return err
	}
	updated, err := s.execHook(tx, `UPDATE sync_state SET lifecycle=?,last_enqueued_seq=?,updated_at=datetime('now') WHERE target_key=?`,
		SyncLifecyclePending, seq, DefaultSyncTargetKey)
	return identityAffected(updated, err, 1, "update cloud sync state")
}

func (s *Store) inspectSessionIdentityTx(tx *sql.Tx, source, replacement string) (identitySnapshot, error) {
	var snapshot identitySnapshot
	if !isBlankSessionID(source) || validateSessionID(replacement) != nil || source == replacement {
		return snapshot, errors.New("invalid session identity repair IDs")
	}
	plan := SessionIdentityRepairPlan{SourceID: source, ReplacementID: replacement}
	var mode, directory, started string
	var ended, summary, lease sql.NullString
	err := tx.QueryRow(`SELECT ifnull(project,''),ifnull(ownership_mode,''),directory,started_at,ended_at,summary,runtime_lease_expires_at
		FROM sessions WHERE id=?`, source).Scan(&plan.Project, &mode, &directory, &started, &ended, &summary, &lease)
	if err != nil {
		return snapshot, fmt.Errorf("session identity source: %w", err)
	}
	normalizedProject, _ := NormalizeProject(plan.Project)
	if strings.TrimSpace(plan.Project) == "" || normalizedProject != plan.Project || strings.TrimSpace(directory) == "" {
		return snapshot, errors.New("session identity source lacks canonical project or directory")
	}
	var collision int
	if err := tx.QueryRow(`SELECT count(*) FROM sessions WHERE id=?`, replacement).Scan(&collision); err != nil {
		return snapshot, err
	}
	if collision != 0 {
		return snapshot, errors.New("replacement session identity already exists")
	}
	snapshot.session = syncSessionPayload{ID: replacement, Project: plan.Project, OwnershipMode: mode,
		Directory: directory, StartedAt: started, EndedAt: nullStringPointer(ended), Summary: nullStringPointer(summary)}
	// Fingerprint source values and all dependent columns, not merely their counts.
	// A matching hash is a stale-plan check, not proof that a row is safe: the
	// explicit ownership, journal, tombstone, and delivery checks below provide that.
	evidence := []any{source, replacement, plan.Project, mode, directory, started, ended, summary, lease}
	if err := inspectIdentityObservations(tx, source, &snapshot, &evidence); err != nil {
		return snapshot, err
	}
	if err := inspectIdentityPrompts(tx, source, &snapshot, &evidence); err != nil {
		return snapshot, err
	}
	plan.Observations, plan.Prompts = int64(len(snapshot.observations)), int64(len(snapshot.prompts))
	var relations int
	if err := tx.QueryRow(`SELECT count(*) FROM memory_relations WHERE session_id=?`, source).Scan(&relations); err != nil {
		return snapshot, err
	}
	if relations != 0 {
		return snapshot, errors.New("session identity has relation references (not safely publishable)")
	}
	if err := inspectIdentityBlockers(tx, source, replacement, plan.Project, &evidence); err != nil {
		return snapshot, err
	}
	if err := inspectIdentityMutations(tx, source, plan.Project, &snapshot, &evidence); err != nil {
		return snapshot, err
	}
	plan.RetiredMutations = int64(len(snapshot.mutations))
	plan.Enrolled, err = isProjectEnrolledTx(tx, plan.Project)
	if err != nil {
		return snapshot, err
	}
	evidence = append(evidence, plan.Enrolled)
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return snapshot, err
	}
	plan.fingerprint = sha256.Sum256(encoded)
	snapshot.plan = plan
	return snapshot, nil
}

func inspectIdentityObservations(tx *sql.Tx, source string, snapshot *identitySnapshot, evidence *[]any) (retErr error) {
	rows, err := tx.Query(`SELECT id,ifnull(sync_id,''),session_id,type,title,content,tool_name,project,scope,topic_key,
		normalized_hash,revision_count,duplicate_count,last_seen_at,pinned,created_at,updated_at,deleted_at
		FROM observations WHERE session_id=? ORDER BY id`, source)
	if err != nil {
		return err
	}
	defer func() {
		if err := rows.Close(); retErr == nil {
			retErr = err
		}
	}()
	for rows.Next() {
		var id, revision, duplicates, pinned int64
		var syncID, sessionID, kind, title, content, scope, created, updated string
		var tool, project, topic, hash, lastSeen, deleted sql.NullString
		if err := rows.Scan(&id, &syncID, &sessionID, &kind, &title, &content, &tool, &project, &scope,
			&topic, &hash, &revision, &duplicates, &lastSeen, &pinned, &created, &updated, &deleted); err != nil {
			return err
		}
		*evidence = append(*evidence, []any{id, syncID, sessionID, kind, title, content, tool, project, scope,
			topic, hash, revision, duplicates, lastSeen, pinned, created, updated, deleted})
		if deleted.Valid || syncID == "" || project.String != snapshot.session.Project || sessionID != source {
			return fmt.Errorf("unsafe observation source %d", id)
		}
		payload := syncObservationPayload{SyncID: syncID, SessionID: source, Type: kind, Title: title,
			Content: content, ToolName: nullStringPointer(tool), Project: &snapshot.session.Project,
			Scope: scope, TopicKey: nullStringPointer(topic), RevisionCount: int(revision),
			DuplicateCount: int(duplicates), LastSeenAt: nullStringPointer(lastSeen), CreatedAt: created, UpdatedAt: updated}
		payload.SessionID = snapshot.session.ID
		if validation := ValidateSyncMutationPayload(SyncEntityObservation, SyncOpUpsert, mustIdentityJSON(payload), syncID); validation.ReasonCode != "" {
			return fmt.Errorf("observation %d cannot be published: %s", id, validation.Message)
		}
		snapshot.observations = append(snapshot.observations, payload)
	}
	return rows.Err()
}

func inspectIdentityPrompts(tx *sql.Tx, source string, snapshot *identitySnapshot, evidence *[]any) (retErr error) {
	rows, err := tx.Query(`SELECT id,ifnull(sync_id,''),session_id,content,project,created_at
		FROM user_prompts WHERE session_id=? ORDER BY id`, source)
	if err != nil {
		return err
	}
	defer func() {
		if err := rows.Close(); retErr == nil {
			retErr = err
		}
	}()
	for rows.Next() {
		var id int64
		var syncID, sessionID, content, created string
		var project sql.NullString
		if err := rows.Scan(&id, &syncID, &sessionID, &content, &project, &created); err != nil {
			return err
		}
		*evidence = append(*evidence, []any{id, syncID, sessionID, content, project, created})
		if syncID == "" || project.String != snapshot.session.Project || sessionID != source {
			return fmt.Errorf("unsafe prompt source %d", id)
		}
		payload := syncPromptPayload{SyncID: syncID, SessionID: source, Content: content,
			Project: &snapshot.session.Project, CreatedAt: created}
		payload.SessionID = snapshot.session.ID
		if validation := ValidateSyncMutationPayload(SyncEntityPrompt, SyncOpUpsert, mustIdentityJSON(payload), syncID); validation.ReasonCode != "" {
			return fmt.Errorf("prompt %d cannot be published: %s", id, validation.Message)
		}
		snapshot.prompts = append(snapshot.prompts, payload)
	}
	return rows.Err()
}

func mustIdentityJSON(value any) string {
	encoded, _ := json.Marshal(value) // All store payload fields have JSON-compatible types.
	return string(encoded)
}

// Same-project deferred payloads with a session_id field are conservatively
// blocked, even when that ID differs: replay provenance cannot be proved here.
// Unscoped or other-project rows naming the source ID are also blocked. Decode
// both direct and JSON-string payloads, matching the deferred replay reader.
// Inactive tombstones carry remote delivery floors and cannot be rewritten here.
func inspectIdentityBlockers(tx *sql.Tx, source, replacement, project string, evidence *[]any) (retErr error) {
	checks := []struct {
		name  string
		query string
		args  []any
	}{
		{"linked deferred key", `SELECT count(*) FROM sync_apply_deferred WHERE (entity='session' AND entity_key=?) OR (entity='observation' AND entity_key IN (SELECT sync_id FROM observations WHERE session_id=?)) OR (entity='prompt' AND entity_key IN (SELECT sync_id FROM user_prompts WHERE session_id=?))`, []any{source, source, source}},
		{"prompt tombstones", `SELECT count(*) FROM prompt_tombstones WHERE session_id=? OR sync_id IN (SELECT sync_id FROM user_prompts WHERE session_id=?)`, []any{source, source}},
		{"delete tombstones", `SELECT count(*) FROM sync_delete_tombstones WHERE session_id=? OR (entity='session' AND entity_key IN (?,?)) OR (entity='observation' AND entity_key IN (SELECT sync_id FROM observations WHERE session_id=?))`, []any{source, source, replacement, source}},
		{"remote floors", `SELECT count(*) FROM sync_delete_tombstone_remote_floors WHERE (entity='session' AND entity_key IN (?,?)) OR (entity='observation' AND entity_key IN (SELECT sync_id FROM observations WHERE session_id=?))`, []any{source, replacement, source}},
		{"duplicate observation identity", `SELECT count(*) FROM observations WHERE sync_id IN (SELECT sync_id FROM observations WHERE session_id=?) AND session_id<>?`, []any{source, source}},
		{"duplicate prompt identity", `SELECT count(*) FROM user_prompts WHERE sync_id IN (SELECT sync_id FROM user_prompts WHERE session_id=?) AND session_id<>?`, []any{source, source}},
	}
	for _, check := range checks {
		var count int64
		if err := tx.QueryRow(check.query, check.args...).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("session identity blocked by %s (%d)", check.name, count)
		}
		*evidence = append(*evidence, count)
	}
	rows, err := tx.Query(`SELECT sync_id, project, payload FROM sync_apply_deferred ORDER BY sync_id`)
	if err != nil {
		return err
	}
	defer func() {
		if err := rows.Close(); retErr == nil {
			retErr = err
		}
	}()
	for rows.Next() {
		var syncID, owner, raw string
		if err := rows.Scan(&syncID, &owner, &raw); err != nil {
			return err
		}
		var payload map[string]json.RawMessage
		if err := decodeSyncPayload([]byte(raw), &payload); err != nil {
			if owner == project || owner == "" {
				return fmt.Errorf("ambiguous deferred payload %q: %w", syncID, err)
			}
			continue
		}
		*evidence = append(*evidence, []any{syncID, owner, raw})
		if sessionRaw, carriesSession := payload["session_id"]; carriesSession {
			var sessionID string
			if err := json.Unmarshal(sessionRaw, &sessionID); err != nil || owner == project || sessionID == source {
				return fmt.Errorf("session identity blocked by deferred payload %q", syncID)
			}
		}
	}
	return rows.Err()
}

func inspectIdentityMutations(tx *sql.Tx, source, project string, snapshot *identitySnapshot, evidence *[]any) (retErr error) {
	rows, err := tx.Query(`SELECT seq,entity,entity_key,op,payload,source,project,acked_at,disposition,target_key,
		occurred_at,disposition_reason,disposition_evidence,disposition_at
		FROM sync_mutations ORDER BY seq`)
	if err != nil {
		return err
	}
	defer func() {
		if err := rows.Close(); retErr == nil {
			retErr = err
		}
	}()
	observations := make(map[string]bool, len(snapshot.observations))
	prompts := make(map[string]bool, len(snapshot.prompts))
	for _, observation := range snapshot.observations {
		observations[observation.SyncID] = true
	}
	for _, prompt := range snapshot.prompts {
		prompts[prompt.SyncID] = true
	}
	for rows.Next() {
		var mutation identityMutation
		var op, origin, owner, disposition, target, occurred string
		var ack, reason, dispositionEvidence, dispositionAt sql.NullString
		if err := rows.Scan(&mutation.seq, &mutation.entity, &mutation.key, &op, &mutation.payload,
			&origin, &owner, &ack, &disposition, &target, &occurred, &reason, &dispositionEvidence, &dispositionAt); err != nil {
			return err
		}
		*evidence = append(*evidence, []any{mutation.seq, mutation.entity, mutation.key, op,
			mutation.payload, origin, owner, ack, disposition, target, occurred, reason, dispositionEvidence, dispositionAt})
		linked := false
		valid := false
		switch mutation.entity {
		case SyncEntitySession:
			linked = mutation.key == source
			var payload syncSessionPayload
			if err := json.Unmarshal([]byte(mutation.payload), &payload); err != nil {
				if linked {
					return fmt.Errorf("ambiguous session mutation %d: %w", mutation.seq, err)
				}
				continue
			}
			linked = linked || payload.ID == source
			valid = mutation.key == source && payload.ID == source && payload.Project == project && !payload.Deleted && !payload.HardDelete
		case SyncEntityObservation:
			linked = observations[mutation.key]
			var payload syncObservationPayload
			if err := json.Unmarshal([]byte(mutation.payload), &payload); err != nil {
				if linked {
					return fmt.Errorf("ambiguous observation mutation %d: %w", mutation.seq, err)
				}
				continue
			}
			linked = linked || payload.SessionID == source
			valid = observations[mutation.key] && payload.SyncID == mutation.key && payload.SessionID == source &&
				payload.Project != nil && *payload.Project == project && !payload.Deleted && !payload.HardDelete
		case SyncEntityPrompt:
			linked = prompts[mutation.key]
			var payload syncPromptPayload
			if err := json.Unmarshal([]byte(mutation.payload), &payload); err != nil {
				if linked {
					return fmt.Errorf("ambiguous prompt mutation %d: %w", mutation.seq, err)
				}
				continue
			}
			linked = linked || payload.SessionID == source
			valid = prompts[mutation.key] && payload.SyncID == mutation.key && payload.SessionID == source &&
				payload.Project != nil && *payload.Project == project && !payload.Deleted && !payload.HardDelete
		case SyncEntityRelation:
			// A relation has an optional session_id but this repair does not
			// republish relations, even if no local relation row survives.
			var payload syncRelationPayload
			if err := json.Unmarshal([]byte(mutation.payload), &payload); err != nil {
				if owner == project {
					return fmt.Errorf("ambiguous relation mutation %d: %w", mutation.seq, err)
				}
				continue
			}
			if payload.SessionID != nil && *payload.SessionID == source {
				return fmt.Errorf("linked relation mutation %d cannot be repaired", mutation.seq)
			}
		}
		if !linked {
			continue
		}
		if target != DefaultSyncTargetKey || owner != project || origin != SyncSourceLocal ||
			op != SyncOpUpsert || ack.Valid || disposition != SyncMutationDispositionPending ||
			reason.Valid || dispositionEvidence.Valid || dispositionAt.Valid || !valid {
			return fmt.Errorf("unsafe linked mutation %d", mutation.seq)
		}
		snapshot.mutations = append(snapshot.mutations, mutation)
	}
	return rows.Err()
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}
