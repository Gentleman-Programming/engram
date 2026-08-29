package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Evidence mirrors the evidence row (RFC §5.6).
type Evidence struct {
	ID                    int64   `json:"id"`
	SyncID                string  `json:"sync_id"`
	Project               string  `json:"project"`
	TaskSyncID            string  `json:"task_sync_id"`
	Path                  string  `json:"path"`
	SHA256                string  `json:"sha256"`
	Kind                  string  `json:"kind"`
	Proves                string  `json:"proves"`
	ConfigStamp           *string `json:"config_stamp,omitempty"`
	CapturedAt            string  `json:"captured_at"`
	AttachedJira          bool    `json:"attached_jira"`
	AttachedConfluenceURL *string `json:"attached_confluence_url,omitempty"`
	SizeBytes             *int64  `json:"size_bytes,omitempty"`
	ManifestPath          *string `json:"manifest_path,omitempty"`
}

const evidenceSelectColumns = `id, sync_id, project, task_sync_id, path, sha256, kind, proves, config_stamp,
	captured_at, attached_jira, attached_confluence_url, size_bytes, manifest_path`

func scanEvidence(row interface{ Scan(dest ...any) error }) (Evidence, error) {
	var e Evidence
	var attachedJira int
	err := row.Scan(&e.ID, &e.SyncID, &e.Project, &e.TaskSyncID, &e.Path, &e.SHA256, &e.Kind, &e.Proves,
		&e.ConfigStamp, &e.CapturedAt, &attachedJira, &e.AttachedConfluenceURL, &e.SizeBytes, &e.ManifestPath)
	e.AttachedJira = attachedJira == 1
	return e, err
}

// evidenceKindByteLimits are the per-file caps from D-06 rule 3.
var evidenceKindByteLimits = map[string]int64{
	"png": 2097152,
	"gif": 5242880,
	"mp4": 8388608,
}

// evidenceTaskTotalByteLimit is D-06's total-per-task cap.
const evidenceTaskTotalByteLimit int64 = 41943040

// EvidenceLimits reports whether AddEvidence's D-06 size rules were
// respected. A violation never blocks the write — the caller (the
// capture-evidence skill) uses it to decide whether to fall back to
// Confluence.
type EvidenceLimits struct {
	OK         bool     `json:"ok"`
	Violations []string `json:"violations"`
}

// AddEvidenceParams holds mem_evidence_add's input, with Task already
// resolved by the MCP handler.
type AddEvidenceParams struct {
	Task                  Task
	Path                  string
	SHA256                string
	Kind                  string
	Proves                string
	ConfigStamp           *string
	CapturedAt            *string
	SizeBytes             *int64
	ManifestPath          *string
	AttachedJira          bool
	AttachedConfluenceURL *string
}

// AddEvidence registers a captured evidence file, idempotent by
// (task_sync_id, sha256) (RFC §5.6).
func (s *Store) AddEvidence(p AddEvidenceParams) (Evidence, bool, EvidenceLimits, error) {
	existing, err := scanEvidence(s.db.QueryRow(
		`SELECT `+evidenceSelectColumns+` FROM evidence WHERE task_sync_id = ? AND sha256 = ? AND deleted_at IS NULL`,
		p.Task.SyncID, p.SHA256))
	if err == nil {
		limits, limErr := s.evidenceLimitsForTask(p.Task.SyncID, "", 0)
		return existing, true, limits, limErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Evidence{}, false, EvidenceLimits{}, fmt.Errorf("engram-projects: check duplicate evidence: %w", err)
	}

	capturedAt := p.CapturedAt
	now := s.nowUTC()
	captured := now
	if capturedAt != nil && strings.TrimSpace(*capturedAt) != "" {
		captured = *capturedAt
	}

	syncID := newSyncID("evd")
	var attachedJira int
	if p.AttachedJira {
		attachedJira = 1
	}
	res, err := s.db.Exec(`
		INSERT INTO evidence (sync_id, project, task_id, task_sync_id, path, sha256, kind, proves,
			config_stamp, captured_at, attached_jira, attached_confluence_url, size_bytes, manifest_path, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		syncID, p.Task.Project, p.Task.ID, p.Task.SyncID, p.Path, p.SHA256, p.Kind, p.Proves,
		nullableStr(p.ConfigStamp), captured, attachedJira, nullableStr(p.AttachedConfluenceURL),
		nullableInt64(p.SizeBytes), nullableStr(p.ManifestPath), now)
	if err != nil {
		return Evidence{}, false, EvidenceLimits{}, fmt.Errorf("engram-projects: insert evidence: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Evidence{}, false, EvidenceLimits{}, err
	}

	limits, err := s.evidenceLimitsForTask(p.Task.SyncID, p.Kind, valInt64(p.SizeBytes))
	if err != nil {
		return Evidence{}, false, EvidenceLimits{}, err
	}

	saved, err := scanEvidence(s.db.QueryRow(`SELECT `+evidenceSelectColumns+` FROM evidence WHERE id = ?`, id))
	if err != nil {
		return Evidence{}, false, EvidenceLimits{}, fmt.Errorf("engram-projects: reload evidence: %w", err)
	}
	return saved, false, limits, nil
}

// evidenceLimitsForTask computes the D-06 violations for a task, optionally
// checking one additional (kind, size) pair that was just inserted (its
// size_bytes is already counted in the SUM, so extraKind/extraSize are only
// used to phrase the per-kind violation message; pass "" / 0 to skip).
func (s *Store) evidenceLimitsForTask(taskSyncID, extraKind string, extraSize int64) (EvidenceLimits, error) {
	limits := EvidenceLimits{OK: true}

	if extraKind != "" && extraSize > 0 {
		if max, ok := evidenceKindByteLimits[extraKind]; ok && extraSize > max {
			limits.OK = false
			limits.Violations = append(limits.Violations, fmt.Sprintf(
				"%s evidence exceeds %d bytes limit (attached size %d)", extraKind, max, extraSize))
		}
	}

	var totalBytes sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT SUM(size_bytes) FROM evidence WHERE task_sync_id = ? AND deleted_at IS NULL`, taskSyncID,
	).Scan(&totalBytes); err != nil {
		return limits, fmt.Errorf("engram-projects: sum evidence size: %w", err)
	}
	if totalBytes.Valid && totalBytes.Int64 > evidenceTaskTotalByteLimit {
		limits.OK = false
		limits.Violations = append(limits.Violations, fmt.Sprintf(
			"task evidence total exceeds %d bytes limit (current total %d)", evidenceTaskTotalByteLimit, totalBytes.Int64))
	}
	return limits, nil
}

// EvidenceListFilter holds mem_evidence_list's filter parameters.
type EvidenceListFilter struct {
	TaskSyncID   string // "" lists the whole project
	AttachedJira *bool
	Kind         string
	Limit        int
	Offset       int
}

// EvidenceListItem is one row of mem_evidence_list's items array.
type EvidenceListItem struct {
	Evidence
	JiraKey   *string `json:"jira_key,omitempty"`
	TaskTitle *string `json:"task_title,omitempty"`
}

// ListEvidence lists evidence for a project, optionally scoped to one task
// (RFC §5.7).
func (s *Store) ListEvidence(project string, f EvidenceListFilter) ([]EvidenceListItem, int, int64, error) {
	where := []string{"e.project = ?", "e.deleted_at IS NULL"}
	args := []any{project}
	if f.TaskSyncID != "" {
		where = append(where, "e.task_sync_id = ?")
		args = append(args, f.TaskSyncID)
	}
	if f.AttachedJira != nil {
		v := 0
		if *f.AttachedJira {
			v = 1
		}
		where = append(where, "e.attached_jira = ?")
		args = append(args, v)
	}
	if f.Kind != "" {
		where = append(where, "e.kind = ?")
		args = append(args, f.Kind)
	}
	whereSQL := strings.Join(where, " AND ")

	var total int
	var totalBytes sql.NullInt64
	if err := s.db.QueryRow(`SELECT COUNT(*), SUM(e.size_bytes) FROM evidence e WHERE `+whereSQL, args...).
		Scan(&total, &totalBytes); err != nil {
		return nil, 0, 0, fmt.Errorf("engram-projects: count evidence: %w", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	listArgs := append(append([]any{}, args...), limit, f.Offset)
	rows, err := s.db.Query(`
		SELECT e.id, e.sync_id, e.project, e.task_sync_id, e.path, e.sha256, e.kind, e.proves, e.config_stamp,
		       e.captured_at, e.attached_jira, e.attached_confluence_url, e.size_bytes, e.manifest_path,
		       t.jira_key, t.title
		FROM evidence e
		JOIN tasks t ON t.id = e.task_id
		WHERE `+whereSQL+`
		ORDER BY e.captured_at DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("engram-projects: list evidence: %w", err)
	}
	defer rows.Close()

	var items []EvidenceListItem
	for rows.Next() {
		var item EvidenceListItem
		var attachedJira int
		if err := rows.Scan(&item.ID, &item.SyncID, &item.Project, &item.TaskSyncID, &item.Path, &item.SHA256,
			&item.Kind, &item.Proves, &item.ConfigStamp, &item.CapturedAt, &attachedJira,
			&item.AttachedConfluenceURL, &item.SizeBytes, &item.ManifestPath, &item.JiraKey, &item.TaskTitle,
		); err != nil {
			return nil, 0, 0, err
		}
		item.AttachedJira = attachedJira == 1
		items = append(items, item)
	}
	return items, total, totalBytes.Int64, rows.Err()
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func valInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
