package cloudstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ─── Outcome and Action Constants ─────────────────────────────────────────────

// AuditOutcomeRejectedProjectPaused is the single outcome constant for v1.
// Used as the `outcome` column value when a push is rejected because the project sync is paused.
const AuditOutcomeRejectedProjectPaused = "rejected_project_paused"

// AuditActionMutationPush discriminates mutation push rejections.
const AuditActionMutationPush = "mutation_push"

// AuditActionChunkPush discriminates chunk push rejections.
const AuditActionChunkPush = "chunk_push"

const AuditActionDashboardDelete = "dashboard_delete"

// ─── Types ────────────────────────────────────────────────────────────────────

// AuditEntry is the write-side struct for inserting an audit log row.
type AuditEntry struct {
	Contributor string
	Project     string
	Action      string // use AuditAction* constants
	Outcome     string // use AuditOutcome* constants
	EntryCount  int
	ReasonCode  string
	Metadata    map[string]any // reserved for future use; nil is fine (stored as NULL)
}

// AuditFilter holds optional filter fields for ListAuditEntriesPaginated.
// All fields are independently optional; zero values mean "no filter".
type AuditFilter struct {
	Contributor    string
	Project        string
	Outcome        string
	OccurredAtFrom time.Time // zero value = no lower bound
	OccurredAtTo   time.Time // zero value = no upper bound
}

// DashboardAuditRow is the read-side struct returned from ListAuditEntriesPaginated.
// N6: Metadata is now included so callers have the full audit row.
// In v1 UI the field is present but not rendered; the API contract is complete.
type DashboardAuditRow struct {
	ID          int64
	OccurredAt  string // RFC3339 UTC
	Contributor string
	Project     string
	Action      string
	Outcome     string
	EntryCount  int
	ReasonCode  string
	Metadata    map[string]any // nil when NULL in DB
}

// ─── CloudStore Methods ───────────────────────────────────────────────────────

// InsertAuditEntry synchronously inserts one audit log row.
// On DB error the error is returned to the caller; do NOT suppress it.
// The caller is responsible for logging at WARN and deciding HTTP response.
// JW5: Metadata field is included in the INSERT via json.Marshal so that
// future-proofing data is not silently dropped.
// N5: nil or empty Metadata map is stored as NULL in the DB (not as "{}").
func (cs *CloudStore) InsertAuditEntry(ctx context.Context, entry AuditEntry) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: InsertAuditEntry: not initialized")
	}
	var reasonCode *string
	if r := strings.TrimSpace(entry.ReasonCode); r != "" {
		reasonCode = &r
	}
	var metadataJSON []byte
	if len(entry.Metadata) > 0 {
		var merr error
		metadataJSON, merr = json.Marshal(entry.Metadata)
		if merr != nil {
			return fmt.Errorf("cloudstore: InsertAuditEntry: marshal metadata: %w", merr)
		}
	}
	_, err := cs.db.ExecContext(ctx, `
		INSERT INTO cloud_sync_audit_log
			(contributor, project, action, outcome, entry_count, reason_code, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		entry.Contributor,
		entry.Project,
		entry.Action,
		entry.Outcome,
		entry.EntryCount,
		reasonCode,
		metadataJSON,
	)
	if err != nil {
		return fmt.Errorf("cloudstore: InsertAuditEntry: %w", err)
	}
	return nil
}

// ListAuditEntriesPaginated returns a page of audit rows matching the filter,
// sorted by occurred_at DESC, plus the total matching count.
// limit and offset are SQL LIMIT/OFFSET values.
func (cs *CloudStore) ListAuditEntriesPaginated(ctx context.Context, filter AuditFilter, limit, offset int) ([]DashboardAuditRow, int, error) {
	if cs == nil || cs.db == nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated: not initialized")
	}

	var conditions []string
	var args []any
	argIdx := 1

	if c := strings.TrimSpace(filter.Contributor); c != "" {
		conditions = append(conditions, fmt.Sprintf("contributor = $%d", argIdx))
		args = append(args, c)
		argIdx++
	}
	if p := strings.TrimSpace(filter.Project); p != "" {
		conditions = append(conditions, fmt.Sprintf("project = $%d", argIdx))
		args = append(args, p)
		argIdx++
	}
	if o := strings.TrimSpace(filter.Outcome); o != "" {
		conditions = append(conditions, fmt.Sprintf("outcome = $%d", argIdx))
		args = append(args, o)
		argIdx++
	}
	if !filter.OccurredAtFrom.IsZero() {
		conditions = append(conditions, fmt.Sprintf("occurred_at >= $%d", argIdx))
		args = append(args, filter.OccurredAtFrom.UTC())
		argIdx++
	}
	if !filter.OccurredAtTo.IsZero() {
		conditions = append(conditions, fmt.Sprintf("occurred_at <= $%d", argIdx))
		args = append(args, filter.OccurredAtTo.UTC())
		argIdx++
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// COUNT query for total.
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM cloud_sync_audit_log %s", where)
	var total int
	if err := cs.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated count: %w", err)
	}

	// Data query with LIMIT/OFFSET.
	// N6: metadata is now included in SELECT so callers receive the full audit row.
	dataQuery := fmt.Sprintf(`
		SELECT id, occurred_at, contributor, project, action, outcome, entry_count,
		       COALESCE(reason_code, ''), metadata
		FROM cloud_sync_audit_log
		%s
		ORDER BY occurred_at DESC
		LIMIT $%d OFFSET $%d`,
		where, argIdx, argIdx+1,
	)
	dataArgs := make([]any, len(args)+2)
	copy(dataArgs, args)
	dataArgs[len(args)] = limit
	dataArgs[len(args)+1] = offset

	dbRows, err := cs.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated query: %w", err)
	}
	defer dbRows.Close()

	var result []DashboardAuditRow
	for dbRows.Next() {
		var row DashboardAuditRow
		var occurredAt time.Time
		var metaBytes []byte
		if err := dbRows.Scan(
			&row.ID,
			&occurredAt,
			&row.Contributor,
			&row.Project,
			&row.Action,
			&row.Outcome,
			&row.EntryCount,
			&row.ReasonCode,
			&metaBytes,
		); err != nil {
			return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated scan: %w", err)
		}
		row.OccurredAt = occurredAt.UTC().Format(time.RFC3339)
		if metaBytes != nil {
			if err := json.Unmarshal(metaBytes, &row.Metadata); err != nil {
				return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated unmarshal metadata: %w", err)
			}
		}
		result = append(result, row)
	}
	if err := dbRows.Err(); err != nil {
		return nil, 0, fmt.Errorf("cloudstore: ListAuditEntriesPaginated iterate: %w", err)
	}

	if result == nil {
		result = []DashboardAuditRow{}
	}
	return result, total, nil
}
