package cloudstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud/chunkcodec"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func observationVersionsFromPayload(project string, payload []byte) ([]store.ObservationVersion, error) {
	chunk, err := parseChunkData(payload)
	if err != nil || len(chunk.ObservationVersions) == 0 { return chunk.ObservationVersions, err }
	canonical, err := chunkcodec.CanonicalizeForProject(payload, project)
	if err != nil { return nil, err }
	chunk, err = parseChunkData(canonical)
	if err != nil { return nil, err }
	return chunk.ObservationVersions, nil
}

type chunkCursor struct { createdAt time.Time; chunkID string }

func (cs *CloudStore) materializeObservationVersions(ctx context.Context, tx *sql.Tx, project string, payload []byte, before *chunkCursor) error {
	versions, err := observationVersionsFromPayload(project, payload)
	if err != nil || len(versions) == 0 { return err }
	for _, version := range versions {
		if err := parentExists(ctx, tx, project, version, before); err != nil { return err }
		var hidden bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cloud_deleted_observations WHERE project = $1 AND observation_sync_id = $2)`, project, version.ObservationSyncID).Scan(&hidden); err != nil { return err }
		result, err := tx.ExecContext(ctx, `INSERT INTO cloud_observation_versions (version_id, project, observation_sync_id, session_id, type, title, content, tool_name, scope, topic_key, revision_count, is_baseline, history_complete, captured_at, hidden_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,CASE WHEN $15 THEN NOW() END) ON CONFLICT (version_id) DO NOTHING`,
			version.VersionID, project, version.ObservationSyncID, version.SessionID, version.Type, version.Title, version.Content, version.ToolName, version.Scope, version.TopicKey, version.RevisionCount, version.IsBaseline, version.HistoryComplete, version.CapturedAt, hidden)
		if err != nil { return fmt.Errorf("cloudstore: insert observation version %q: %w", version.VersionID, err) }
		if changed, _ := result.RowsAffected(); changed == 0 {
			if err := sameStoredObservationVersion(ctx, tx, project, version); err != nil { return err }
		}
	}
	return nil
}

type observationMutationParent struct { SyncID string `json:"sync_id"`; SessionID string `json:"session_id"`; Project *string `json:"project"`; Scope string `json:"scope"` }

func latestObservationMutation(ctx context.Context, tx *sql.Tx, project string, version store.ObservationVersion) (bool, bool, error) {
	var op, key, payload string
	err := tx.QueryRowContext(ctx, `SELECT op, entity_key, payload::text FROM cloud_mutations WHERE project = $1 AND entity = 'observation' AND entity_key = $2 ORDER BY seq DESC LIMIT 1`, project, version.ObservationSyncID).Scan(&op, &key, &payload)
	if err == sql.ErrNoRows { return false, false, nil }
	if err != nil || op != store.SyncOpUpsert { return true, false, err }
	var parent observationMutationParent
	if chunkcodec.DecodeSyncMutationPayload(payload, &parent) != nil { return true, false, nil }
	return true, parent.SyncID == version.ObservationSyncID && key == version.ObservationSyncID && parent.SessionID == version.SessionID && parent.Project != nil && *parent.Project == project && parent.Scope == "project", nil
}

func parentExists(ctx context.Context, tx *sql.Tx, project string, version store.ObservationVersion, before *chunkCursor) error {
	var session, observation bool
	if before == nil {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cloud_project_sessions WHERE project_name = $1 AND session_id = $2)`, project, version.SessionID).Scan(&session); err != nil { return err }
		found, valid, err := latestObservationMutation(ctx, tx, project, version)
		if err != nil { return err }
		observation = valid
		if !found { err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cloud_chunks WHERE project_name = $1 AND payload @> jsonb_build_object('observations', jsonb_build_array(jsonb_build_object('sync_id', $2::text))))`, project, version.ObservationSyncID).Scan(&observation); if err != nil { return err } }
	} else if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cloud_chunks WHERE project_name = $1 AND (created_at, chunk_id) < ($2, $3) AND payload @> jsonb_build_object('sessions', jsonb_build_array(jsonb_build_object('id', $4::text)))), EXISTS(SELECT 1 FROM cloud_chunks WHERE project_name = $1 AND (created_at, chunk_id) < ($2, $3) AND payload @> jsonb_build_object('observations', jsonb_build_array(jsonb_build_object('sync_id', $5::text))))`, project, before.createdAt, before.chunkID, version.SessionID, version.ObservationSyncID).Scan(&session, &observation); err != nil { return err }
	if !session || !observation { return fmt.Errorf("cloudstore: observation version %q parent session/observation not found", version.VersionID) }
	return nil
}

func sameStoredObservationVersion(ctx context.Context, tx *sql.Tx, project string, version store.ObservationVersion) error {
	var stored store.ObservationVersion
	var storedProject string
	err := tx.QueryRowContext(ctx, `SELECT project, observation_sync_id, session_id, type, title, content, tool_name, scope, topic_key, revision_count, is_baseline, history_complete, captured_at FROM cloud_observation_versions WHERE version_id = $1`, version.VersionID).Scan(&storedProject, &stored.ObservationSyncID, &stored.SessionID, &stored.Type, &stored.Title, &stored.Content, &stored.ToolName, &stored.Scope, &stored.TopicKey, &stored.RevisionCount, &stored.IsBaseline, &stored.HistoryComplete, &stored.CapturedAt)
	if err != nil { return fmt.Errorf("cloudstore: read existing observation version %q: %w", version.VersionID, err) }
	if storedProject != project || stored.ObservationSyncID != version.ObservationSyncID || stored.SessionID != version.SessionID || stored.Type != version.Type || stored.Title != version.Title || stored.Content != version.Content || !sameOptional(stored.ToolName, version.ToolName) || stored.Scope != version.Scope || !sameOptional(stored.TopicKey, version.TopicKey) || stored.RevisionCount != version.RevisionCount || stored.IsBaseline != version.IsBaseline || stored.HistoryComplete != version.HistoryComplete || stored.CapturedAt != version.CapturedAt {
		return fmt.Errorf("cloudstore: observation version %q conflicts with immutable stored snapshot", version.VersionID)
	}
	return nil
}

func sameOptional(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func applyObservationVersionVisibility(ctx context.Context, tx *sql.Tx, project string, entries []MutationEntry) error {
	for _, entry := range entries {
		if entry.Entity != store.SyncEntityObservation { continue }
		if entry.Op == store.SyncOpUpsert {
			if _, err := tx.ExecContext(ctx, `DELETE FROM cloud_deleted_observations WHERE project = $1 AND observation_sync_id = $2`, project, entry.EntityKey); err != nil { return err }
			if _, err := tx.ExecContext(ctx, `UPDATE cloud_observation_versions SET hidden_at = NULL WHERE project = $1 AND observation_sync_id = $2`, project, entry.EntityKey); err != nil { return err }
		}
		if entry.Op == store.SyncOpDelete {
			if _, err := tx.ExecContext(ctx, `INSERT INTO cloud_deleted_observations (project, observation_sync_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, project, entry.EntityKey); err != nil { return err }
			if _, err := tx.ExecContext(ctx, `UPDATE cloud_observation_versions SET hidden_at = NOW() WHERE project = $1 AND observation_sync_id = $2 AND hidden_at IS NULL`, project, entry.EntityKey); err != nil { return err }
		}
	}
	return nil
}

func (cs *CloudStore) backfillObservationVersions(ctx context.Context) error {
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil { return fmt.Errorf("cloudstore: begin observation version backfill: %w", err) }
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT project_name, chunk_id, created_at, payload FROM cloud_chunks ORDER BY created_at, chunk_id`)
	if err != nil { return fmt.Errorf("cloudstore: query observation version backfill: %w", err) }
	type chunkRow struct{ project, chunkID string; createdAt time.Time; payload []byte }
	var chunks []chunkRow
	for rows.Next() {
		var row chunkRow
		if err := rows.Scan(&row.project, &row.chunkID, &row.createdAt, &row.payload); err != nil { _ = rows.Close(); return err }
		chunks = append(chunks, row)
	}
	if err := rows.Err(); err != nil { _ = rows.Close(); return err }
	if err := rows.Close(); err != nil { return err }
	for _, row := range chunks {
		project, payload := row.project, row.payload
		chunk, err := parseChunkData(payload)
		if err != nil { return fmt.Errorf("cloudstore: parse observation version backfill: %w", err) }
		if err := cs.materializeObservationVersions(ctx, tx, project, payload, &chunkCursor{row.createdAt, row.chunkID}); err != nil { return err }
		if err := cs.indexChunkSessionsWith(ctx, tx, project, payload); err != nil { return err }
		entries, err := materializedChunkMutations(project, chunk)
		if err != nil { return err }
		if err := applyObservationVersionVisibility(ctx, tx, project, entries); err != nil { return err }
	}
	if err := rows.Err(); err != nil { return err }
	if err := tx.Commit(); err != nil { return fmt.Errorf("cloudstore: commit observation version backfill: %w", err) }
	return nil
}
