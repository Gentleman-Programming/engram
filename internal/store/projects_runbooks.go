package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var runbookIDPattern = regexp.MustCompile(`^RB-[0-9]{3}$`)

var runbookValidStatuses = map[string]bool{"draft": true, "verified": true, "outdated": true}

// RunbookIndexEntryInput is one entry of mem_runbook_index_sync's `entries`
// array (RFC §5.8).
type RunbookIndexEntryInput struct {
	ID              string
	VaultPath       string
	Title           string
	Service         string
	Category        string
	Pattern         string
	Severity        string
	Status          string
	Symptoms        []string
	Tags            []string
	Owner           string
	AutomationLevel string
	LastUpdated     string
	LastVerified    string
	NeedsReview     *bool
	AgeDays         *int
}

// RunbookIndexSyncParams holds mem_runbook_index_sync's input.
type RunbookIndexSyncParams struct {
	Project      string // optional filter; scopes prune_missing when set
	Source       string // "knowledge-mcp" | "vault-fs"
	PruneMissing bool
	Entries      []RunbookIndexEntryInput
}

// RunbookSkipped is one row of mem_runbook_index_sync's `skipped` array.
type RunbookSkipped struct {
	ID        string `json:"id,omitempty"`
	VaultPath string `json:"vault_path,omitempty"`
	Reason    string `json:"reason"`
}

// RunbookSyncResult is the outcome of SyncRunbookIndex.
type RunbookSyncResult struct {
	Upserted       int               `json:"upserted"`
	Unchanged      int               `json:"unchanged"`
	Pruned         int               `json:"pruned"`
	Skipped        []RunbookSkipped  `json:"skipped"`
	StaleCount     int               `json:"stale_count"`
	ExecRecomputed int               `json:"exec_recomputed"`
}

// SyncRunbookIndex rebuilds the runbook index from entries obtained
// externally from the knowledge vault (RFC §5.8). Templates and malformed
// entries are reported in `skipped` rather than rejecting the whole call.
func (s *Store) SyncRunbookIndex(p RunbookIndexSyncParams) (RunbookSyncResult, error) {
	var result RunbookSyncResult
	processedIDs := map[string]bool{}
	touchedProjects := map[string]bool{}

	for _, e := range p.Entries {
		if hasTemplateTag(e.Tags) || strings.HasPrefix(e.VaultPath, "Runbooks/Templates/") {
			result.Skipped = append(result.Skipped, RunbookSkipped{ID: e.ID, VaultPath: e.VaultPath, Reason: "template"})
			continue
		}
		if !runbookIDPattern.MatchString(e.ID) {
			result.Skipped = append(result.Skipped, RunbookSkipped{ID: e.ID, VaultPath: e.VaultPath, Reason: "invalid_id"})
			continue
		}
		if !runbookValidStatuses[e.Status] {
			result.Skipped = append(result.Skipped, RunbookSkipped{ID: e.ID, VaultPath: e.VaultPath, Reason: "invalid_status"})
			continue
		}

		project, _ := NormalizeProject(e.Service)
		if project == "" {
			result.Skipped = append(result.Skipped, RunbookSkipped{ID: e.ID, VaultPath: e.VaultPath, Reason: "missing_service"})
			continue
		}
		if _, err := s.ensureMinimalProjectCard(project); err != nil {
			return result, err
		}
		touchedProjects[project] = true
		processedIDs[e.ID] = true

		stale := 0
		switch p.Source {
		case "vault-fs":
			if e.AgeDays != nil && *e.AgeDays > 90 {
				stale = 1
			}
		default: // "knowledge-mcp"
			if e.NeedsReview != nil && *e.NeedsReview {
				stale = 1
			}
		}
		if stale == 1 {
			result.StaleCount++
		}
		symptoms := strings.Join(e.Symptoms, "\n")

		changed, err := s.upsertRunbookIndexRow(e, project, symptoms, stale)
		if err != nil {
			return result, err
		}
		if changed {
			result.Upserted++
		} else {
			result.Unchanged++
		}

		execCount, lastExecAt, err := s.recomputeRunbookExec(e.ID)
		if err != nil {
			return result, err
		}
		if _, err := s.db.Exec(`UPDATE runbook_index SET exec_count = ?, last_exec_at = ? WHERE id = ?`,
			execCount, lastExecAt, e.ID); err != nil {
			return result, fmt.Errorf("engram-projects: update runbook exec stats: %w", err)
		}
		result.ExecRecomputed++
	}

	if p.PruneMissing {
		scope := touchedProjects
		if strings.TrimSpace(p.Project) != "" {
			normalized, _ := NormalizeProject(p.Project)
			scope = map[string]bool{normalized: true}
		}
		for project := range scope {
			rows, err := s.db.Query(`SELECT id FROM runbook_index WHERE project = ?`, project)
			if err != nil {
				return result, fmt.Errorf("engram-projects: list runbooks for prune: %w", err)
			}
			var toPrune []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return result, err
				}
				if !processedIDs[id] {
					toPrune = append(toPrune, id)
				}
			}
			rows.Close()
			for _, id := range toPrune {
				if _, err := s.db.Exec(`DELETE FROM runbook_index WHERE id = ?`, id); err != nil {
					return result, fmt.Errorf("engram-projects: prune runbook %s: %w", id, err)
				}
				result.Pruned++
			}
		}
	}

	return result, nil
}

func hasTemplateTag(tags []string) bool {
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), "template") {
			return true
		}
	}
	return false
}

// upsertRunbookIndexRow inserts or updates one runbook_index row, returning
// changed=true when the row was newly created or any comparable field
// differed from what was already stored.
func (s *Store) upsertRunbookIndexRow(e RunbookIndexEntryInput, project, symptoms string, stale int) (bool, error) {
	var existing struct {
		vaultPath, title, category, pattern, severity, status, symptoms, owner, automationLevel string
		lastUpdated, lastVerified                                                                string
		stale                                                                                     int
		ageDays                                                                                   sql.NullInt64
	}
	err := s.db.QueryRow(`
		SELECT vault_path, title, category, COALESCE(pattern, ''), COALESCE(severity, ''), status, symptoms,
		       COALESCE(owner, ''), COALESCE(automation_level, ''), COALESCE(last_updated, ''),
		       COALESCE(last_verified, ''), stale, age_days
		FROM runbook_index WHERE id = ?`, e.ID,
	).Scan(&existing.vaultPath, &existing.title, &existing.category, &existing.pattern, &existing.severity,
		&existing.status, &existing.symptoms, &existing.owner, &existing.automationLevel,
		&existing.lastUpdated, &existing.lastVerified, &existing.stale, &existing.ageDays)

	now := s.nowUTC()
	ageDays := any(nil)
	if e.AgeDays != nil {
		ageDays = *e.AgeDays
	}

	if errors.Is(err, sql.ErrNoRows) {
		_, insertErr := s.db.Exec(`
			INSERT INTO runbook_index (id, project, vault_path, title, category, pattern, severity, status,
				symptoms, owner, automation_level, last_updated, last_verified, stale, age_days, synced_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.ID, project, e.VaultPath, e.Title, e.Category, nullableEmptyStr(e.Pattern), nullableEmptyStr(e.Severity),
			e.Status, symptoms, nullableEmptyStr(e.Owner), nullableEmptyStr(e.AutomationLevel),
			nullableEmptyStr(e.LastUpdated), nullableEmptyStr(e.LastVerified), stale, ageDays, now)
		if insertErr != nil {
			return false, fmt.Errorf("engram-projects: insert runbook: %w", insertErr)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("engram-projects: read runbook: %w", err)
	}

	sameAge := (!existing.ageDays.Valid && e.AgeDays == nil) ||
		(existing.ageDays.Valid && e.AgeDays != nil && existing.ageDays.Int64 == int64(*e.AgeDays))
	unchanged := existing.vaultPath == e.VaultPath && existing.title == e.Title && existing.category == e.Category &&
		existing.pattern == e.Pattern && existing.severity == e.Severity && existing.status == e.Status &&
		existing.symptoms == symptoms && existing.owner == e.Owner && existing.automationLevel == e.AutomationLevel &&
		existing.lastUpdated == e.LastUpdated && existing.lastVerified == e.LastVerified &&
		existing.stale == stale && sameAge

	if unchanged {
		return false, nil
	}

	_, updateErr := s.db.Exec(`
		UPDATE runbook_index SET project = ?, vault_path = ?, title = ?, category = ?, pattern = ?, severity = ?,
			status = ?, symptoms = ?, owner = ?, automation_level = ?, last_updated = ?, last_verified = ?,
			stale = ?, age_days = ?, synced_at = ?
		WHERE id = ?`,
		project, e.VaultPath, e.Title, e.Category, nullableEmptyStr(e.Pattern), nullableEmptyStr(e.Severity),
		e.Status, symptoms, nullableEmptyStr(e.Owner), nullableEmptyStr(e.AutomationLevel),
		nullableEmptyStr(e.LastUpdated), nullableEmptyStr(e.LastVerified), stale, ageDays, now, e.ID)
	if updateErr != nil {
		return false, fmt.Errorf("engram-projects: update runbook: %w", updateErr)
	}
	return true, nil
}

func (s *Store) recomputeRunbookExec(id string) (int, any, error) {
	var count int
	var lastExecAt sql.NullString
	err := s.db.QueryRow(
		`SELECT COUNT(*), MAX(created_at) FROM observations WHERE topic_key LIKE ? AND deleted_at IS NULL`,
		"runbook/"+id+"/exec/%",
	).Scan(&count, &lastExecAt)
	if err != nil {
		return 0, nil, fmt.Errorf("engram-projects: recompute runbook exec: %w", err)
	}
	if lastExecAt.Valid {
		return count, lastExecAt.String, nil
	}
	return count, nil, nil
}

func nullableEmptyStr(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

// RunbookFindParams holds mem_runbook_find's input (RFC §5.9).
type RunbookFindParams struct {
	Query        string
	Project      string
	Category     string
	Pattern      string
	IncludeStale bool
	MatchMode    string // "all" | "any", default "any"
	Limit        int
}

// RunbookFindItem is one row of mem_runbook_find's items array.
type RunbookFindItem struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Project    string  `json:"project"`
	VaultPath  string  `json:"vault_path"`
	Category   string  `json:"category"`
	Pattern    *string `json:"pattern,omitempty"`
	Severity   *string `json:"severity,omitempty"`
	Status     string  `json:"status"`
	Stale      bool    `json:"stale"`
	AgeDays    *int    `json:"age_days,omitempty"`
	ExecCount  int     `json:"exec_count"`
	LastExecAt *string `json:"last_exec_at,omitempty"`
	Rank       float64 `json:"rank"`
}

// FindRunbooks ranks candidate runbooks by BM25 over runbook_index_fts.
func (s *Store) FindRunbooks(p RunbookFindParams) ([]RunbookFindItem, int, error) {
	matchMode := p.MatchMode
	if matchMode == "" {
		matchMode = "any"
	}
	ftsQuery := ftsMatchQuery(p.Query, matchMode)

	where := []string{"runbook_index_fts MATCH ?"}
	args := []any{ftsQuery}
	if p.Project != "" {
		where = append(where, "ri.project = ?")
		args = append(args, p.Project)
	}
	if p.Category != "" {
		where = append(where, "ri.category = ?")
		args = append(args, p.Category)
	}
	if p.Pattern != "" {
		where = append(where, "ri.pattern = ?")
		args = append(args, p.Pattern)
	}
	if !p.IncludeStale {
		where = append(where, "ri.stale = 0")
	}
	whereSQL := strings.Join(where, " AND ")

	limit := p.Limit
	if limit <= 0 {
		limit = 5
	}

	var total int
	countArgs := append([]any{}, args...)
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM runbook_index_fts JOIN runbook_index ri ON ri.seq = runbook_index_fts.rowid WHERE `+whereSQL,
		countArgs...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("engram-projects: count runbook matches: %w", err)
	}

	listArgs := append(append([]any{}, args...), limit)
	rows, err := s.db.Query(`
		SELECT ri.id, ri.title, ri.project, ri.vault_path, ri.category, ri.pattern, ri.severity, ri.status,
		       ri.stale, ri.age_days, ri.exec_count, ri.last_exec_at, bm25(runbook_index_fts) AS rank
		FROM runbook_index_fts
		JOIN runbook_index ri ON ri.seq = runbook_index_fts.rowid
		WHERE `+whereSQL+`
		ORDER BY rank LIMIT ?`, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("engram-projects: find runbooks: %w", err)
	}
	defer rows.Close()

	var items []RunbookFindItem
	for rows.Next() {
		var item RunbookFindItem
		var stale int
		if err := rows.Scan(&item.ID, &item.Title, &item.Project, &item.VaultPath, &item.Category,
			&item.Pattern, &item.Severity, &item.Status, &stale, &item.AgeDays, &item.ExecCount,
			&item.LastExecAt, &item.Rank); err != nil {
			return nil, 0, err
		}
		item.Stale = stale == 1
		items = append(items, item)
	}
	return items, total, rows.Err()
}

// ftsMatchQuery quotes every token (guarding interior quotes, same rule as
// sanitizeFTS) and joins them with AND or OR depending on matchMode.
func ftsMatchQuery(query, matchMode string) string {
	fields := strings.Fields(query)
	quoted := make([]string, len(fields))
	for i, w := range fields {
		w = strings.Trim(w, `"`)
		w = strings.ReplaceAll(w, `"`, `""`)
		quoted[i] = `"` + w + `"`
	}
	sep := " OR "
	if matchMode == "all" {
		sep = " AND "
	}
	return strings.Join(quoted, sep)
}
