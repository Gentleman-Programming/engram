package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const legacyPromptDiscardReason = "explicit_legacy_empty_prompt_discard"

// LegacyEmptyPromptDiscardBlocker is a fail-closed planning or application error.
// Code is suitable for CLI reporting; no blocker implies remote absence.
type LegacyEmptyPromptDiscardBlocker struct {
	Code   string
	Detail string
}

func (e *LegacyEmptyPromptDiscardBlocker) Error() string {
	return e.Code + ": " + e.Detail
}

func discardBlocked(code, detail string) error {
	return &LegacyEmptyPromptDiscardBlocker{Code: code, Detail: detail}
}

// LegacyEmptyPromptDiscardPlan previews the exact canonical/FTS deletion,
// tombstone, superseded sequences and single new delete. Its private snapshot
// must survive unchanged from planning to apply. Plans are bound to the Store;
// callers cannot reconstruct a valid plan by deserializing public fields.
type LegacyEmptyPromptDiscardPlan struct {
	Project       string
	SelectedSeq   int64
	PromptID      int64
	SyncID        string
	SessionID     string
	SourceInboxID string
	Sequences     []int64
	snapshot      string
	owner         *Store
}

// LegacyEmptyPromptDiscardResult reports local effects, never remote delivery.
// BackupPath may remain populated on error: backups/reservations are not removed
// if creation fails or later transactional revalidation rejects the plan.
type LegacyEmptyPromptDiscardResult struct {
	Status     string
	Sequences  []int64
	DeleteSeq  int64
	BackupPath string
}

// PlanLegacyEmptyPromptDiscard is database- and filesystem-read-only. It accepts
// one exact project and journal sequence, never an implicit bulk selection.
func (s *Store) PlanLegacyEmptyPromptDiscard(project string, seq int64) (LegacyEmptyPromptDiscardPlan, error) {
	var plan LegacyEmptyPromptDiscardPlan
	err := s.withReadTx(func(tx *sql.Tx) error {
		var err error
		plan, err = s.planLegacyPromptDiscardTx(tx, project, seq)
		return err
	})
	return plan, err
}

// discardRows retains every database column, including nullable provenance and
// audit fields, not just the fields used in the eligibility verdict.
func discardRows(tx *sql.Tx, query string, args ...any) ([]map[string]any, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	// Early-return cleanup preserves the query error; exhausted rows report via Err.
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, name := range columns {
			if value, ok := values[i].([]byte); ok {
				values[i] = string(value)
			}
			row[name] = values[i]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func discardText(row map[string]any, key string) string {
	text, _ := row[key].(string)
	return text
}

// Decode strictly: duplicate or unsupported fields must not hide another
// identity or recoverable content behind the normal JSON last-value rule.
func decodeDiscardPrompt(raw string) (syncPromptPayload, error) {
	var payload syncPromptPayload
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return payload, fmt.Errorf("expected prompt object")
	}
	seen := map[string]bool{}
	allowed := map[string]bool{
		"sync_id": true, "session_id": true, "source_inbox_id": true,
		"content": true, "project": true, "created_at": true,
		"deleted": true, "hard_delete": true, "deleted_at": true,
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return payload, err
		}
		key, ok := token.(string)
		if !ok || seen[key] || !allowed[key] {
			return payload, fmt.Errorf("duplicate or unsupported prompt field")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return payload, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return payload, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return payload, fmt.Errorf("trailing prompt JSON")
	}
	decoder = json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return payload, err
	}
	return payload, nil
}

func (s *Store) planLegacyPromptDiscardTx(tx *sql.Tx, project string, seq int64) (LegacyEmptyPromptDiscardPlan, error) {
	plan := LegacyEmptyPromptDiscardPlan{Project: project, SelectedSeq: seq, owner: s}
	normalized, _ := NormalizeProject(project)
	if project == "" || normalized != project || seq <= 0 {
		return plan, discardBlocked("invalid_selection", "exact normalized project and positive sequence required")
	}
	selected, err := discardRows(tx, `SELECT * FROM sync_mutations WHERE seq = ?`, seq)
	if err != nil {
		return plan, err
	}
	if len(selected) != 1 {
		return plan, discardBlocked("missing_mutation", "selected sequence does not exist")
	}
	plan.SyncID = discardText(selected[0], "entity_key")
	if strings.TrimSpace(plan.SyncID) == "" || strings.TrimSpace(plan.SyncID) != plan.SyncID {
		return plan, discardBlocked("missing_identity", "mutation has no exact sync identity")
	}
	enrollment, err := discardRows(tx, `SELECT * FROM sync_enrolled_projects WHERE project = ?`, project)
	if err != nil {
		return plan, err
	}
	if len(enrollment) != 1 {
		return plan, discardBlocked("not_enrolled", "initial support requires an enrolled project")
	}
	canonical, err := discardRows(tx, `SELECT * FROM user_prompts WHERE sync_id = ? ORDER BY id`, plan.SyncID)
	if err != nil {
		return plan, err
	}
	if len(canonical) != 1 {
		return plan, discardBlocked("ambiguous_canonical", "exactly one canonical prompt required")
	}
	row := canonical[0]
	plan.PromptID, _ = row["id"].(int64)
	plan.SessionID = discardText(row, "session_id")
	plan.SourceInboxID = discardText(row, "source_inbox_id")
	if discardText(row, "project") != project || strings.TrimSpace(plan.SessionID) == "" ||
		strings.TrimSpace(plan.SessionID) != plan.SessionID || strings.TrimSpace(plan.SourceInboxID) != plan.SourceInboxID {
		return plan, discardBlocked("conflicting_identity", "canonical project/session/source does not match exact identity")
	}
	// Match the existing required-fields diagnostic normalization. Do not strip
	// private tags or truncate historical content: either could hide recovery.
	if strings.TrimSpace(discardText(row, "content")) != "" {
		return plan, discardBlocked("recoverable_content", "canonical content is recoverable")
	}
	for key, expected := range map[string]string{
		"local_creation_project":    project,
		"local_creation_session_id": plan.SessionID,
		"local_creation_inbox_id":   plan.SourceInboxID,
	} {
		if value := discardText(row, key); value != "" && value != expected {
			return plan, discardBlocked("conflicting_provenance", "canonical creation provenance conflicts")
		}
	}
	// A nonempty inbox identity is also a prompt identity in existing store
	// primitives. Refuse alternate sync keys that alias that same identity.
	canonical, err = discardRows(tx, `SELECT * FROM user_prompts
		WHERE sync_id = ? OR (? != '' AND session_id = ? AND source_inbox_id = ?) ORDER BY id`,
		plan.SyncID, plan.SourceInboxID, plan.SessionID, plan.SourceInboxID)
	if err != nil {
		return plan, err
	}
	if len(canonical) != 1 {
		return plan, discardBlocked("ambiguous_canonical", "canonical inbox identity has aliases")
	}
	sessions, err := discardRows(tx, `SELECT * FROM sessions WHERE id = ?`, plan.SessionID)
	if err != nil {
		return plan, err
	}
	if len(sessions) != 1 || discardText(sessions[0], "project") != project {
		return plan, discardBlocked("conflicting_session", "session must exist in the selected project")
	}
	if original := discardText(sessions[0], "local_creation_project"); original != "" && original != project {
		return plan, discardBlocked("conflicting_session", "session creation provenance conflicts with the project")
	}
	tombstones, err := discardRows(tx, `SELECT * FROM prompt_tombstones
		WHERE sync_id = ? OR (? != '' AND session_id = ? AND source_inbox_id = ?) ORDER BY sync_id`,
		plan.SyncID, plan.SourceInboxID, plan.SessionID, plan.SourceInboxID)
	if err != nil {
		return plan, err
	}
	if len(tombstones) != 0 {
		return plan, discardBlocked("prior_tombstone", "existing tombstone makes retirement ambiguous")
	}
	// Search by key, frozen sync identity and nonempty inbox identity across
	// targets/projects/sources/dispositions. Never filter historical conflicts.
	// json_each enumerates duplicate fields too: json_extract alone would use
	// the first value and could miss a later conflicting identity in a row.
	lineage, err := discardRows(tx, `SELECT * FROM sync_mutations
		WHERE entity_key = ? OR EXISTS (
			SELECT 1 FROM json_each(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END)
			WHERE lower(key) = 'sync_id' AND value = ?
		) OR (? != '' AND EXISTS (
			SELECT 1 FROM json_each(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END)
			WHERE lower(key) = 'session_id' AND value = ?
		) AND EXISTS (
			SELECT 1 FROM json_each(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END)
			WHERE lower(key) = 'source_inbox_id' AND value = ?
		)) ORDER BY seq`,
		plan.SyncID, plan.SyncID, plan.SourceInboxID, plan.SessionID, plan.SourceInboxID)
	if err != nil {
		return plan, err
	}
	selectedFound := false
	for _, mutation := range lineage {
		payload, err := decodeDiscardPrompt(discardText(mutation, "payload"))
		if err != nil {
			return plan, discardBlocked("malformed_payload", err.Error())
		}
		if strings.TrimSpace(payload.Content) != "" {
			return plan, discardBlocked("recoverable_content", "journal lineage contains recoverable content")
		}
		if discardText(mutation, "entity") != SyncEntityPrompt || discardText(mutation, "entity_key") != plan.SyncID ||
			discardText(mutation, "target_key") != DefaultSyncTargetKey || discardText(mutation, "source") != SyncSourceLocal ||
			discardText(mutation, "project") != project || discardText(mutation, "op") != SyncOpUpsert ||
			mutation["acked_at"] != nil || discardText(mutation, "disposition") != SyncMutationDispositionPending ||
			mutation["disposition_reason"] != nil || mutation["disposition_evidence"] != nil || mutation["disposition_at"] != nil {
			return plan, discardBlocked("unsupported_lineage", "all lineage must be unaudited pending unacknowledged local cloud prompt upserts")
		}
		if payload.SyncID != plan.SyncID || payload.SessionID != plan.SessionID || payload.Project == nil || *payload.Project != project ||
			payload.SourceInboxID != plan.SourceInboxID || payload.Deleted || payload.HardDelete || payload.DeletedAt != nil {
			return plan, discardBlocked("conflicting_payload_identity", "frozen deletion identity differs from canonical prompt")
		}
		mutationSeq, _ := mutation["seq"].(int64)
		plan.Sequences = append(plan.Sequences, mutationSeq)
		selectedFound = selectedFound || mutationSeq == seq
	}
	if !selectedFound {
		return plan, discardBlocked("unsupported_lineage", "selected mutation is outside the exact lineage")
	}
	snapshot, err := json.Marshal([]any{project, seq, canonical, lineage, enrollment, sessions, tombstones})
	if err != nil {
		return plan, err
	}
	plan.snapshot = string(snapshot)
	return plan, nil
}

func (s *Store) validateLegacyDiscardPlanTx(tx *sql.Tx, plan LegacyEmptyPromptDiscardPlan) error {
	if plan.owner != s || plan.snapshot == "" {
		return discardBlocked("invalid_plan", "plan must originate from this store")
	}
	current, err := s.planLegacyPromptDiscardTx(tx, plan.Project, plan.SelectedSeq)
	if err != nil {
		var blocker *LegacyEmptyPromptDiscardBlocker
		if errors.As(err, &blocker) {
			return discardBlocked("stale_plan", "current eligibility changed: "+err.Error())
		}
		return err
	}
	if !reflect.DeepEqual(current, plan) {
		return discardBlocked("stale_plan", "snapshot or public plan fields changed")
	}
	return nil
}

// ApplyLegacyEmptyPromptDiscard backs up first, then independently rederives
// eligibility inside the single mutation transaction. A changed/repeated plan
// fails with stale_plan and queues no duplicate. A preflight stale plan creates
// no backup; a change after backup may leave that backup on disk. There is no
// fencing of concurrent/in-flight exporters and no remote delivery guarantee.
func (s *Store) ApplyLegacyEmptyPromptDiscard(plan LegacyEmptyPromptDiscardPlan, backupPath string) (LegacyEmptyPromptDiscardResult, error) {
	var result LegacyEmptyPromptDiscardResult
	if err := s.withReadTx(func(tx *sql.Tx) error {
		return s.validateLegacyDiscardPlanTx(tx, plan)
	}); err != nil {
		return result, err
	}
	path, err := s.backupLegacyDiscard(backupPath)
	result.BackupPath = path
	if err != nil {
		return result, err
	}
	err = s.withTx(func(tx *sql.Tx) error {
		if err := s.validateLegacyDiscardPlanTx(tx, plan); err != nil {
			return err
		}
		now := Now()
		payload := syncPromptPayload{
			SyncID: plan.SyncID, SessionID: plan.SessionID, Project: nullableString(plan.Project),
			SourceInboxID: plan.SourceInboxID, Deleted: true, HardDelete: true, DeletedAt: &now,
		}
		evidence, err := json.Marshal(map[string]any{
			"trigger": legacyPromptDiscardReason, "project": plan.Project, "sync_id": plan.SyncID,
			"prompt_id": plan.PromptID, "session_id": plan.SessionID, "source_inbox_id": plan.SourceInboxID,
			"sequences": plan.Sequences, "snapshot_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(plan.snapshot))),
		})
		if err != nil {
			return err
		}
		// Eligibility validated the entire exact set matched by this helper.
		changed, err := s.supersedePendingLocalUpsertTx(tx, DefaultSyncTargetKey, SyncEntityPrompt,
			plan.SyncID, plan.Project, legacyPromptDiscardReason, string(evidence))
		if err != nil {
			return err
		}
		if !changed {
			return discardBlocked("stale_plan", "no pending upsert was retired")
		}
		if err := s.recordLocalPromptTombstoneTx(tx, plan.PromptID, plan.SyncID, plan.SessionID,
			payload.Project, plan.SourceInboxID, now); err != nil {
			return err
		}
		deleted, err := s.execHook(tx, `DELETE FROM user_prompts WHERE id = ?`, plan.PromptID)
		if err != nil {
			return err
		}
		n, err := deleted.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return discardBlocked("stale_plan", "canonical prompt changed")
		}
		if err := s.enqueueSyncMutationTx(tx, SyncEntityPrompt, plan.SyncID, SyncOpDelete, payload); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT last_enqueued_seq FROM sync_state WHERE target_key = ?`,
			DefaultSyncTargetKey).Scan(&result.DeleteSeq)
	})
	if err != nil {
		result.DeleteSeq = 0
		return result, err
	}
	result.Status = "delete_queued"
	result.Sequences = append([]int64(nil), plan.Sequences...)
	return result, nil
}

// backupLegacyDiscard never gives SQLite the operator-controlled destination.
// It snapshots into an operation-private directory (0700 where supported)
// beneath the trusted backing DB directory, then copies through the reserved
// handle, which stays open throughout publication. Destination/parent replacement
// can invalidate publication but cannot redirect the copy to another file.
// Trust boundary: the DB directory, its ancestors/access controls and this
// process are trusted (including inherited directory ACLs on Windows). This is
// not protection against malicious same-identity processes or administrators
// that can tamper with the trusted private snapshot namespace.
// The destination parent must exist. Errors retain the reservation; the returned
// path is the intended path, not proof that a replaced pathname contains backup.
func (s *Store) backupLegacyDiscard(path string) (resultPath string, resultErr error) {
	if strings.TrimSpace(path) == "" {
		return "", discardBlocked("backup_path_required", "explicit backup path required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve backup parent: %w", err)
	}
	destination := filepath.Join(parent, filepath.Base(absolute))
	// Protect the actual backing database, not merely the configured DataDir.
	var databaseSeq int
	var databaseName, databasePath string
	if err := s.db.QueryRow(`PRAGMA database_list`).Scan(&databaseSeq, &databaseName, &databasePath); err != nil {
		return "", err
	}
	source, err := filepath.EvalSymlinks(databasePath)
	if err != nil {
		return "", err
	}
	for _, protected := range []string{source, source + "-wal", source + "-shm"} {
		if strings.EqualFold(filepath.Clean(destination), filepath.Clean(protected)) {
			return "", discardBlocked("unsafe_backup_path", "destination aliases live SQLite storage")
		}
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", discardBlocked("backup_collision", "destination already exists, including symlinks")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("reserve backup destination: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	reserved, err := file.Stat()
	if err != nil {
		return destination, err
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return destination, err
	}
	verifyPublication := func() error {
		currentParent, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		actual, err := os.Lstat(destination)
		if err != nil {
			return err
		}
		if !currentParent.IsDir() || !os.SameFile(parentInfo, currentParent) ||
			!actual.Mode().IsRegular() || !os.SameFile(reserved, actual) {
			return discardBlocked("backup_changed", "reserved backup destination or parent changed")
		}
		return nil
	}
	if err := verifyPublication(); err != nil {
		return destination, err
	}

	privateDir, err := os.MkdirTemp(filepath.Dir(source), ".prompt-discard-backup-")
	if err != nil {
		return destination, err
	}
	privateRoot, err := os.OpenRoot(privateDir)
	if err != nil {
		return destination, errors.Join(err, os.Remove(privateDir))
	}
	// Root.Remove removes only the owned entry through the anchored directory;
	// it cannot follow a substituted snapshot symlink outside that directory.
	defer func() {
		removeErr := privateRoot.Remove("snapshot.db")
		if os.IsNotExist(removeErr) {
			removeErr = nil
		}
		resultErr = errors.Join(resultErr, removeErr, privateRoot.Close(), os.Remove(privateDir))
	}()
	if _, err := s.execHook(s.db, `VACUUM INTO ?`, filepath.Join(privateDir, "snapshot.db")); err != nil {
		return destination, fmt.Errorf("backup legacy discard: %w", err)
	}
	input, err := privateRoot.Open("snapshot.db")
	if err != nil {
		return destination, err
	}
	defer func() { resultErr = errors.Join(resultErr, input.Close()) }()
	info, err := input.Stat()
	if err != nil {
		return destination, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return destination, discardBlocked("backup_invalid", "private SQLite snapshot is not a nonempty regular file")
	}
	if err := verifyPublication(); err != nil {
		return destination, err
	}
	// No pathname reopen: even replacement during this copy cannot redirect
	// database bytes into a substituted file or directory. No overwrite occurs.
	copied, err := io.Copy(file, input)
	if err != nil {
		return destination, err
	}
	if copied != info.Size() {
		return destination, discardBlocked("backup_invalid", "private snapshot changed during copy")
	}
	if err := file.Sync(); err != nil {
		return destination, err
	}
	if err := verifyPublication(); err != nil {
		return destination, err
	}
	return destination, nil
}
