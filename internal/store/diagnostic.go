package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DiagnosticSessionEvidence is the read-only session projection used by
// operational diagnostics. It intentionally avoids observation/prompt payloads.
type DiagnosticSessionEvidence struct {
	ID        string `json:"id"`
	Project   string `json:"project"`
	Directory string `json:"directory"`
	Name      string `json:"name"`
}

// SyncMutationPayloadValidation describes deterministic required-field issues
// in a pending sync mutation payload.
type SyncMutationPayloadValidation struct {
	Entity        string   `json:"entity"`
	Op            string   `json:"op"`
	EntityKey     string   `json:"entity_key,omitempty"`
	MissingFields []string `json:"missing_fields,omitempty"`
	ReasonCode    string   `json:"reason_code,omitempty"`
	Message       string   `json:"message,omitempty"`
}

// InvalidSessionIdentityEvidence describes a corrupt source session and the
// dependent local data that cannot be repaired without a canonical ID.
type InvalidSessionIdentityEvidence struct {
	Project             string `json:"project"`
	SessionID           string `json:"session_id"`
	ObservationCount    int64  `json:"observation_count"`
	PromptCount         int64  `json:"prompt_count"`
	InvalidJournalCount int64  `json:"invalid_journal_count"`
}

// QuarantinedPulledSessionEvidence describes a pulled session mutation that was
// skipped because its identity is blank or inconsistent. The pull cursor
// advances past such a mutation instead of halting, so this row is the only
// record that remote data was dropped.
type QuarantinedPulledSessionEvidence struct {
	SyncID      string `json:"sync_id"`
	TargetKey   string `json:"target_key"`
	Project     string `json:"project"`
	EntityKey   string `json:"entity_key"`
	Op          string `json:"op"`
	RemoteSeq   int64  `json:"remote_seq"`
	ReasonCode  string `json:"reason_code"`
	FirstSeenAt string `json:"first_seen_at"`
}

// SQLiteLockSnapshot captures conservative SQLite lock/contention indicators.
// wal_checkpoint(PASSIVE) is an observational probe for this diagnostic surface;
// callers must not interpret it as a repair action.
type SQLiteLockSnapshot struct {
	JournalMode        string `json:"journal_mode"`
	BusyTimeoutMS      int    `json:"busy_timeout_ms"`
	CheckpointBusy     int    `json:"checkpoint_busy"`
	CheckpointLog      int    `json:"checkpoint_log"`
	CheckpointedFrames int    `json:"checkpointed_frames"`
}

type SessionProjectReclassification struct {
	SessionID   string
	FromProject string
	ToProject   string
}

type SessionProjectReclassificationCounts struct {
	Sessions     int64
	Observations int64
	Prompts      int64
}

type SessionProjectReclassificationResult struct {
	Counts     SessionProjectReclassificationCounts
	BackupPath string
}

// ListDiagnosticSessions returns session evidence scoped by project when
// provided. The query is read-only and ordered for deterministic diagnostics.
func (s *Store) ListDiagnosticSessions(project string) ([]DiagnosticSessionEvidence, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	query := `SELECT id, project, ifnull(directory, ''), id FROM sessions`
	args := []any{}
	if project != "" {
		query += ` WHERE project = ?`
		args = append(args, project)
	}
	query += ` ORDER BY started_at DESC, id ASC`

	rows, err := s.queryItHook(s.db, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make([]DiagnosticSessionEvidence, 0)
	for rows.Next() {
		var ev DiagnosticSessionEvidence
		if err := rows.Scan(&ev.ID, &ev.Project, &ev.Directory, &ev.Name); err != nil {
			return nil, err
		}
		sessions = append(sessions, ev)
	}
	return sessions, rows.Err()
}

// ListPendingProjectMutations returns pending cloud mutations for one project,
// or all projects when project is empty, without enrollment filtering. Doctor
// needs to diagnose blocked metadata even when a project is not enrolled.
func (s *Store) ListPendingProjectMutations(project string) ([]SyncMutation, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	return s.listPendingProjectMutationsTxLike(s.db, project)
}

// ListInvalidSessionIdentityEvidence reports blank source session IDs together
// with affected references and invalid session journal entries. It is read-only.
//
// The source-row predicate uses the shared whitespace trim set rather than
// SQLite's bare trim(), so a legacy identity made of tabs, newlines or carriage
// returns cannot bypass the scan while still being rejected by the Go guards.
func (s *Store) ListInvalidSessionIdentityEvidence(project string) ([]InvalidSessionIdentityEvidence, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	query := `SELECT id, project FROM sessions WHERE ` + sqlSessionIDBlank("id")
	args := []any{sqlWhitespaceTrimSet}
	if project != "" {
		query += ` AND project = ?`
		args = append(args, project)
	}
	rows, err := s.queryItHook(s.db, query, args...)
	if err != nil {
		return nil, err
	}
	evidence := make([]InvalidSessionIdentityEvidence, 0)
	for rows.Next() {
		var item InvalidSessionIdentityEvidence
		if err := rows.Scan(&item.SessionID, &item.Project); err != nil {
			_ = rows.Close()
			return nil, err
		}
		evidence = append(evidence, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type sessionMutationIdentity struct {
		entityKey      string
		payloadID      string
		payloadDecoded bool
		invalid        bool
	}
	mutationQuery := `SELECT entity_key, payload FROM sync_mutations WHERE entity = ?`
	mutationArgs := []any{SyncEntitySession}
	if project != "" {
		mutationQuery += ` AND project = ?`
		mutationArgs = append(mutationArgs, project)
	}
	mutationRows, err := s.queryItHook(s.db, mutationQuery, mutationArgs...)
	if err != nil {
		return nil, err
	}
	mutations := make([]sessionMutationIdentity, 0)
	for mutationRows.Next() {
		var mutation sessionMutationIdentity
		var payloadRaw string
		if err := mutationRows.Scan(&mutation.entityKey, &payloadRaw); err != nil {
			return nil, closeRowsWithError(mutationRows, err)
		}
		var payload syncSessionPayload
		if err := decodeSyncPayload([]byte(payloadRaw), &payload); err != nil {
			mutation.invalid = true
		} else {
			mutation.payloadID = payload.ID
			mutation.payloadDecoded = true
			mutation.invalid = validateSessionMutationIdentity(payload.ID, mutation.entityKey) != nil
		}
		mutations = append(mutations, mutation)
	}
	if err := mutationRows.Close(); err != nil {
		return nil, err
	}
	if err := mutationRows.Err(); err != nil {
		return nil, err
	}

	evidenceBySessionID := make(map[string]int, len(evidence))
	for i := range evidence {
		evidenceBySessionID[evidence[i].SessionID] = i
	}
	for _, mutation := range mutations {
		if !mutation.invalid {
			continue
		}
		matched := make(map[int]struct{}, 2)
		if i, ok := evidenceBySessionID[mutation.entityKey]; ok {
			matched[i] = struct{}{}
		}
		if mutation.payloadDecoded {
			if i, ok := evidenceBySessionID[mutation.payloadID]; ok {
				matched[i] = struct{}{}
			}
		}
		for i := range matched {
			evidence[i].InvalidJournalCount++
		}
	}

	for i := range evidence {
		item := &evidence[i]
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE session_id = ?`, item.SessionID).Scan(&item.ObservationCount); err != nil {
			return nil, err
		}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM user_prompts WHERE session_id = ?`, item.SessionID).Scan(&item.PromptCount); err != nil {
			return nil, err
		}
	}
	return evidence, nil
}

// ListQuarantinedPulledSessionEvidence returns the pulled session mutations the
// apply path skipped because their identity is blank or inconsistent.
//
// The pull deliberately does not fail closed on these mutations: halting would
// pin the cursor forever on a historical chunk written before the identity rule
// existed. Instead each one is quarantined here so doctor can report exactly
// what remote data was dropped. It is read-only.
func (s *Store) ListQuarantinedPulledSessionEvidence(project string) ([]QuarantinedPulledSessionEvidence, error) {
	project, _ = NormalizeProject(project)
	project = strings.TrimSpace(project)
	query := `SELECT sync_id, target_key, ifnull(project, ''), entity_key, op, remote_seq, reason_code, ifnull(first_seen_at, '')
		FROM sync_apply_deferred
		WHERE entity = ? AND reason_code = ?`
	args := []any{SyncEntitySession, SyncSessionIdentityInvalidReasonCode}
	if project != "" {
		query += ` AND project = ?`
		args = append(args, project)
	}
	query += ` ORDER BY target_key ASC, remote_seq ASC, sync_id ASC`

	rows, err := s.queryItHook(s.db, query, args...)
	if err != nil {
		return nil, err
	}
	quarantined := make([]QuarantinedPulledSessionEvidence, 0)
	for rows.Next() {
		var item QuarantinedPulledSessionEvidence
		if err := rows.Scan(&item.SyncID, &item.TargetKey, &item.Project, &item.EntityKey, &item.Op, &item.RemoteSeq, &item.ReasonCode, &item.FirstSeenAt); err != nil {
			return nil, closeRowsWithError(rows, err)
		}
		quarantined = append(quarantined, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return quarantined, rows.Err()
}

type rowQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func (s *Store) listPendingProjectMutationsTxLike(q rowQuerier, project string) ([]SyncMutation, error) {
	query := `
		SELECT seq, target_key, entity, entity_key, op, payload, source, project, occurred_at, acked_at, disposition, ifnull(disposition_reason, ''), ifnull(disposition_evidence, ''), disposition_at
		FROM sync_mutations
		WHERE target_key = ? AND acked_at IS NULL`
	args := []any{DefaultSyncTargetKey}
	if project != "" {
		query += ` AND project = ?`
		args = append(args, project)
	}
	query += ` ORDER BY seq ASC`
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	mutations := make([]SyncMutation, 0)
	for rows.Next() {
		var m SyncMutation
		if err := rows.Scan(&m.Seq, &m.TargetKey, &m.Entity, &m.EntityKey, &m.Op, &m.Payload, &m.Source, &m.Project, &m.OccurredAt, &m.AckedAt, &m.Disposition, &m.DispositionReason, &m.DispositionEvidence, &m.DispositionAt); err != nil {
			return nil, err
		}
		mutations = append(mutations, m)
	}
	return mutations, rows.Err()
}

// ValidateSyncMutationPayload performs pure required-field validation for sync
// payloads. It is intentionally conservative: malformed/empty/unsupported
// payloads are reported as manual blocks, while complete payloads return an
// empty validation.
func ValidateSyncMutationPayload(entity, op, payload, entityKey string) SyncMutationPayloadValidation {
	entity = strings.TrimSpace(entity)
	op = strings.TrimSpace(op)
	result := SyncMutationPayloadValidation{Entity: entity, Op: op, EntityKey: entityKey}
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		result.ReasonCode = UpgradeReasonBlockedLegacyMutationManual
		result.Message = "sync mutation payload is empty"
		return result
	}

	var body map[string]any
	if err := decodeSyncPayload([]byte(trimmed), &body); err != nil {
		result.ReasonCode = UpgradeReasonBlockedLegacyMutationManual
		result.Message = fmt.Sprintf("decode sync mutation payload: %v", err)
		return result
	}
	field := func(name string) string {
		v, ok := body[name]
		if !ok || v == nil {
			return ""
		}
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
		encoded, _ := json.Marshal(v)
		return strings.TrimSpace(string(encoded))
	}
	rawStringField := func(name string) string {
		v, ok := body[name]
		if !ok || v == nil {
			return ""
		}
		s, _ := v.(string)
		return s
	}
	missing := make([]string, 0)
	require := func(name string) {
		if field(name) == "" {
			missing = append(missing, name)
		}
	}

	switch entity {
	case SyncEntitySession:
		payloadID := rawStringField("id")
		if strings.TrimSpace(payloadID) == "" {
			missing = append(missing, "id")
		}
		if strings.TrimSpace(entityKey) == "" {
			missing = append(missing, "entity_key")
		}
		if len(missing) == 0 && payloadID != entityKey {
			result.ReasonCode = "sync_session_mutation_identity_mismatch"
			result.Message = fmt.Sprintf("session entity_key %q does not match payload id %q", entityKey, payloadID)
			return result
		}
		if op == SyncOpUpsert {
			require("directory")
		}
	case SyncEntityObservation:
		if field("sync_id") == "" && strings.TrimSpace(entityKey) == "" {
			missing = append(missing, "sync_id")
		}
		if op == SyncOpUpsert {
			require("session_id")
			require("type")
			require("title")
			require("content")
			require("scope")
		}
	case SyncEntityPrompt:
		if field("sync_id") == "" && strings.TrimSpace(entityKey) == "" {
			missing = append(missing, "sync_id")
		}
		if op == SyncOpUpsert {
			require("session_id")
			require("content")
		}
	case SyncEntityRelation:
		if op == SyncOpUpsert {
			require("sync_id")
			require("source_id")
			require("target_id")
			require("relation")
			require("judgment_status")
			require("marked_by_actor")
			require("marked_by_kind")
			require("project")
		}
	default:
		result.ReasonCode = UpgradeReasonBlockedLegacyMutationManual
		result.Message = fmt.Sprintf("unsupported sync mutation %q/%q", entity, op)
		return result
	}

	if len(missing) > 0 {
		result.MissingFields = missing
		result.ReasonCode = "sync_mutation_payload_missing_required_fields"
		result.Message = fmt.Sprintf("%s payload missing required fields: %s", entity, strings.Join(missing, ", "))
	}
	return result
}

// ReadSQLiteLockSnapshot returns SQLite lock-related PRAGMA values without
// starting an application write transaction.
func (s *Store) ReadSQLiteLockSnapshot(ctx context.Context) (SQLiteLockSnapshot, error) {
	var snapshot SQLiteLockSnapshot
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&snapshot.JournalMode); err != nil {
		return snapshot, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&snapshot.BusyTimeoutMS); err != nil {
		return snapshot, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&snapshot.CheckpointBusy, &snapshot.CheckpointLog, &snapshot.CheckpointedFrames); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (s *Store) EstimateSessionProjectReclassification(actions []SessionProjectReclassification) (SessionProjectReclassificationCounts, error) {
	var counts SessionProjectReclassificationCounts
	for _, action := range normalizeSessionProjectReclassificationActions(actions) {
		var n int64
		if err := s.db.QueryRow(`SELECT count(*) FROM sessions WHERE id = ? AND project = ?`, action.SessionID, action.FromProject).Scan(&n); err != nil {
			return counts, fmt.Errorf("estimate sessions: %w", err)
		}
		counts.Sessions += n
		if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE session_id = ? AND project = ? AND deleted_at IS NULL`, action.SessionID, action.FromProject).Scan(&n); err != nil {
			return counts, fmt.Errorf("estimate observations: %w", err)
		}
		counts.Observations += n
		if err := s.db.QueryRow(`SELECT count(*) FROM user_prompts WHERE session_id = ? AND project = ?`, action.SessionID, action.FromProject).Scan(&n); err != nil {
			return counts, fmt.Errorf("estimate prompts: %w", err)
		}
		counts.Prompts += n
	}
	return counts, nil
}

func (s *Store) ApplySessionProjectReclassification(actions []SessionProjectReclassification) (SessionProjectReclassificationResult, error) {
	normalized := normalizeSessionProjectReclassificationActions(actions)
	backupPath, err := s.BackupSQLite()
	if err != nil {
		return SessionProjectReclassificationResult{}, err
	}
	var result SessionProjectReclassificationResult
	result.BackupPath = backupPath
	err = s.withTx(func(tx *sql.Tx) error {
		for _, action := range normalized {
			res, err := s.execHook(tx, `UPDATE sessions SET project = ? WHERE id = ? AND project = ?`, action.ToProject, action.SessionID, action.FromProject)
			if err != nil {
				return fmt.Errorf("reclassify session %q: %w", action.SessionID, err)
			}
			n, _ := res.RowsAffected()
			result.Counts.Sessions += n

			res, err = s.execHook(tx, `UPDATE observations SET project = ? WHERE session_id = ? AND project = ?`, action.ToProject, action.SessionID, action.FromProject)
			if err != nil {
				return fmt.Errorf("reclassify observations for session %q: %w", action.SessionID, err)
			}
			n, _ = res.RowsAffected()
			result.Counts.Observations += n

			res, err = s.execHook(tx, `UPDATE user_prompts SET project = ? WHERE session_id = ? AND project = ?`, action.ToProject, action.SessionID, action.FromProject)
			if err != nil {
				return fmt.Errorf("reclassify prompts for session %q: %w", action.SessionID, err)
			}
			n, _ = res.RowsAffected()
			result.Counts.Prompts += n
		}
		return nil
	})
	if err != nil {
		return SessionProjectReclassificationResult{}, err
	}
	return result, nil
}

func (s *Store) BackupSQLite() (string, error) {
	backupDir := filepath.Join(s.cfg.DataDir, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("create sqlite backup dir: %w", err)
	}
	path := filepath.Join(backupDir, "engram-repair-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".db")
	if _, err := s.execHook(s.db, `VACUUM INTO ?`, path); err != nil {
		return "", fmt.Errorf("backup sqlite database: %w", err)
	}
	return path, nil
}

func normalizeSessionProjectReclassificationActions(actions []SessionProjectReclassification) []SessionProjectReclassification {
	seen := make(map[string]struct{})
	out := make([]SessionProjectReclassification, 0, len(actions))
	for _, action := range actions {
		action.SessionID = strings.TrimSpace(action.SessionID)
		action.FromProject, _ = NormalizeProject(action.FromProject)
		action.ToProject, _ = NormalizeProject(action.ToProject)
		if action.SessionID == "" || action.FromProject == "" || action.ToProject == "" || action.FromProject == action.ToProject {
			continue
		}
		key := action.SessionID + "\x00" + action.FromProject + "\x00" + action.ToProject
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, action)
	}
	return out
}
