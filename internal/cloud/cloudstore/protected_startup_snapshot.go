package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	engramsync "github.com/Gentleman-Programming/engram/v3/internal/sync"
)

func startupSnapshot(ctx context.Context, db *sql.DB, readOnly bool) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: readOnly})
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "LOCK TABLE cloud_chunks, cloud_mutations, cloud_project_sessions IN ACCESS SHARE MODE"); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	// Resolve and lock the same source relations as the ordinary startup
	// path before applying the compiler's fixed deparser search path.
	var matchesOrdinaryPath bool
	err = tx.QueryRowContext(ctx, `
		SELECT pg_catalog.current_setting('server_encoding') OPERATOR(pg_catalog.=) 'UTF8'
		   AND pg_catalog.current_setting('client_encoding') OPERATOR(pg_catalog.=) 'UTF8'
		   AND 'cloud_chunks'::pg_catalog.regclass OPERATOR(pg_catalog.=) 'public.cloud_chunks'::pg_catalog.regclass
		   AND 'cloud_mutations'::pg_catalog.regclass OPERATOR(pg_catalog.=) 'public.cloud_mutations'::pg_catalog.regclass
		   AND 'cloud_project_sessions'::pg_catalog.regclass OPERATOR(pg_catalog.=) 'public.cloud_project_sessions'::pg_catalog.regclass
		   AND (pg_catalog.to_regoperator('=(pg_catalog.text,pg_catalog.text)') OPERATOR(pg_catalog.=)
		        pg_catalog.to_regoperator('pg_catalog.=(pg_catalog.text,pg_catalog.text)')) IS TRUE
		   AND (
		      SELECT count(*) OPERATOR(pg_catalog.=) 2 FROM pg_catalog.pg_attribute a
		      WHERE a.atttypid OPERATOR(pg_catalog.=) 'pg_catalog.text'::pg_catalog.regtype
		        AND ((a.attrelid OPERATOR(pg_catalog.=) 'public.cloud_chunks'::pg_catalog.regclass AND a.attname OPERATOR(pg_catalog.=) 'project_name')
		          OR (a.attrelid OPERATOR(pg_catalog.=) 'public.cloud_mutations'::pg_catalog.regclass AND a.attname OPERATOR(pg_catalog.=) 'project'))
		   )
	`).Scan(&matchesOrdinaryPath)
	if err != nil || !matchesOrdinaryPath {
		_ = tx.Rollback()
		return nil, errors.New("startup encoding, relations, or project equality differ from the proof requirements")
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL search_path TO pg_catalog, public, pg_temp"); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func verifySessionIndex(ctx context.Context, tx *sql.Tx) error {
	known, err := readSessionIndex(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT project_name, payload FROM cloud_chunks")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var project string
		var payload []byte
		if err := rows.Scan(&project, &payload); err != nil {
			return err
		}
		if strings.TrimSpace(project) == "" {
			return errors.New("chunk has no project")
		}
		// Padded projects would attempt an insert on the ordinary path.
		chunk, _ := engramsync.DecodeHistoricalChunk(payload)
		for id := range collectSessionIDs(chunk) {
			if _, ok := known[project][id]; !ok || project != strings.TrimSpace(project) {
				return errors.New("ordinary session index requires work")
			}
		}
	}
	return rows.Err()
}

func readSessionIndex(ctx context.Context, tx *sql.Tx) (map[string]map[string]struct{}, error) {
	rows, err := tx.QueryContext(ctx, "SELECT project_name, session_id FROM cloud_project_sessions")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	known := make(map[string]map[string]struct{})
	for rows.Next() {
		var project, id string
		if err := rows.Scan(&project, &id); err != nil {
			return nil, err
		}
		if known[project] == nil {
			known[project] = make(map[string]struct{})
		}
		known[project][id] = struct{}{}
	}
	return known, rows.Err()
}
