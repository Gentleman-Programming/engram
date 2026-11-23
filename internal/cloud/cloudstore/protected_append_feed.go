package cloudstore

import (
	"context"
	"database/sql"
)

type protectedReceipt struct {
	projects           []string
	snapshot, identity string
}

const receiptAppendPendingXIDCap = 128

func captureProtectedReceipt(ctx context.Context, db *sql.DB, projects []string) (protectedReceipt, error) {
	projects = protectedProjects(projects)
	if len(projects) == 0 {
		return protectedReceipt{}, errReceiptAppendUnproven
	}
	tx, err := startupSnapshot(ctx, db)
	if err != nil {
		return protectedReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, identity, err := protectedCatalogSnapshot(ctx, tx, "")
	if err != nil {
		return protectedReceipt{}, err
	}
	if err := verifySessionIndex(ctx, tx); err != nil {
		return protectedReceipt{}, err
	}
	for _, project := range projects {
		signatures, err := existingChunkMutationSignaturesWith(ctx, tx, project, nil)
		if err != nil {
			return protectedReceipt{}, err
		}
		var report MutationChunkBackfillReport
		missing, err := readMutationChunkBackfillCandidates(ctx, tx, project, signatures, &report, nil)
		if err != nil {
			return protectedReceipt{}, err
		}
		if len(missing) != 0 {
			return protectedReceipt{}, errReceiptAppendUnproven
		}
	}
	return protectedReceipt{projects, snapshot, identity}, tx.Commit()
}

func protectedCatalogSnapshot(ctx context.Context, tx *sql.Tx, prior string) (string, string, error) {
	if err := requireProtectedStartupRole(ctx, tx); err != nil {
		return "", "", err
	}
	var snapshot, identity string
	var exact bool
	err := tx.QueryRowContext(ctx, `
		WITH snapshot AS (
			SELECT pg_current_snapshot() AS current,
			       coalesce(nullif($1,'')::pg_snapshot,pg_current_snapshot()) AS prior
		)
		SELECT current::text, receipt_audit.identity(), receipt_audit.catalog()=expected
		       AND pg_snapshot_xmin(current)>=retained_from AND pg_snapshot_xmin(prior)>=retained_from
		       AND (SELECT count(*) FROM (SELECT pg_snapshot_xip(prior) LIMIT $2) pending)<$2
		FROM receipt_audit.enrollment CROSS JOIN snapshot`, prior, receiptAppendPendingXIDCap+1).Scan(&snapshot, &identity, &exact)
	if err != nil {
		return "", "", err
	}
	if !exact || identity == "" {
		return "", "", errReceiptAppendUnproven
	}
	return snapshot, identity, nil
}

// Try a bounded proof and commit its read-only snapshot before advancing the
// test receipt. Real fallback may write and runs only after this tx returns.
func tryProtectedDelta(ctx context.Context, db *sql.DB, prior protectedReceipt) (protectedReceipt, []receiptAppendEvent, error) {
	tx, err := startupSnapshot(ctx, db)
	if err != nil {
		return prior, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, identity, err := protectedCatalogSnapshot(ctx, tx, prior.snapshot)
	if err != nil {
		return prior, nil, err
	}
	if prior.identity != identity {
		return prior, nil, errReceiptAppendUnproven
	}
	events, err := readProtectedAppendEvents(ctx, tx, prior.snapshot)
	if err != nil {
		return prior, nil, err
	}
	if err := verifyAppendMembership(ctx, tx, prior.projects, events); err != nil {
		return prior, events, err
	}
	if err := tx.Commit(); err != nil {
		return prior, events, err
	}
	return protectedReceipt{prior.projects, snapshot, identity}, events, nil
}

func readProtectedAppendEvents(ctx context.Context, tx *sql.Tx, snapshot string) ([]receiptAppendEvent, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT kind, coalesce(project,''), coalesce(id,''), coalesce(seq,0) FROM (
			(SELECT kind,project,id,seq FROM receipt_audit.events
			 WHERE xid>=pg_snapshot_xmax($1::pg_snapshot) ORDER BY xid LIMIT $2)
			UNION ALL
			(SELECT e.kind,e.project,e.id,e.seq FROM pg_snapshot_xip($1::pg_snapshot) AS pending(xid)
			 CROSS JOIN LATERAL (SELECT kind,project,id,seq FROM receipt_audit.events WHERE xid=pending.xid LIMIT $2) e
			 WHERE pending.xid<pg_snapshot_xmax($1::pg_snapshot) LIMIT $2)
		) events
		LIMIT $2`, snapshot, receiptAppendEventCap+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []receiptAppendEvent
	for rows.Next() {
		var event receiptAppendEvent
		if err := rows.Scan(&event.kind, &event.project, &event.id, &event.seq); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
