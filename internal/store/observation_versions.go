package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
)

const (
	defaultObservationVersionLimit = 50
	maxObservationVersionLimit     = 100
)

// ObservationVersion is an immutable local snapshot of an observation's
// user-editable state. HistoryComplete is false for a migrated baseline because
// revisions that predate local history were not available to capture.
type ObservationVersion struct {
	VersionID         string  `json:"version_id"`
	ObservationSyncID string  `json:"observation_sync_id"`
	SessionID         string  `json:"session_id"`
	Type              string  `json:"type"`
	Title             string  `json:"title"`
	Content           string  `json:"content"`
	ToolName          *string `json:"tool_name,omitempty"`
	Project           *string `json:"project,omitempty"`
	Scope             string  `json:"scope"`
	TopicKey          *string `json:"topic_key,omitempty"`
	RevisionCount     int     `json:"revision_count"`
	IsBaseline        bool    `json:"is_baseline"`
	HistoryComplete   bool    `json:"history_complete"`
	CapturedAt        string  `json:"captured_at"`
}

func (s *Store) appendObservationVersionTx(tx *sql.Tx, obs *Observation) error {
	versionID, err := newObservationVersionID()
	if err != nil {
		return err
	}
	complete, err := s.observationHistoryCompleteTx(tx, obs.ID)
	if err != nil {
		return err
	}
	_, err = s.execHook(tx, `
		INSERT INTO observation_versions (
			version_id, observation_id, observation_sync_id, session_id, type, title,
			content, tool_name, project, scope, topic_key, revision_count, is_baseline,
			history_complete
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		versionID, obs.ID, obs.SyncID, obs.SessionID, obs.Type, obs.Title,
		obs.Content, obs.ToolName, obs.Project, obs.Scope, obs.TopicKey,
		obs.RevisionCount, complete,
	)
	return err
}

func newObservationVersionID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	return hex.EncodeToString(bytes[0:4]) + "-" + hex.EncodeToString(bytes[4:6]) + "-" +
		hex.EncodeToString(bytes[6:8]) + "-" + hex.EncodeToString(bytes[8:10]) + "-" +
		hex.EncodeToString(bytes[10:]), nil
}

func (s *Store) observationHistoryCompleteTx(tx *sql.Tx, observationID int64) (bool, error) {
	var incomplete bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM observation_versions WHERE observation_id = ? AND history_complete = 0)`, observationID).Scan(&incomplete); err != nil {
		return false, err
	}
	return !incomplete, nil
}

// ObservationVersions returns the first newest-first page for one portable
// observation identity. Limits are capped so callers cannot request unbounded history.
func (s *Store) ObservationVersions(syncID string, limit int) ([]ObservationVersion, error) {
	return s.ObservationVersionsPage(syncID, 0, "", limit)
}

// ObservationVersionsPage continues before the supplied revision/version pair.
// Callers pass the final version from the previous page for deterministic export.
func (s *Store) ObservationVersionsPage(syncID string, beforeRevision int, beforeVersionID string, limit int) ([]ObservationVersion, error) {
	if strings.TrimSpace(syncID) == "" {
		return nil, ErrObservationNotFound
	}
	if limit <= 0 {
		limit = defaultObservationVersionLimit
	}
	if limit > maxObservationVersionLimit {
		limit = maxObservationVersionLimit
	}
	query := `SELECT version_id, observation_sync_id, session_id, type, title, content, tool_name,
		project, scope, topic_key, revision_count, is_baseline, history_complete, captured_at
		FROM observation_versions WHERE observation_sync_id = ?`
	args := []any{syncID}
	if beforeVersionID != "" {
		query += ` AND (revision_count < ? OR (revision_count = ? AND version_id < ?))`
		args = append(args, beforeRevision, beforeRevision, beforeVersionID)
	}
	query += ` ORDER BY revision_count DESC, version_id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.queryItHook(s.db, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var versions []ObservationVersion
	for rows.Next() {
		var version ObservationVersion
		if err := rows.Scan(
			&version.VersionID, &version.ObservationSyncID, &version.SessionID,
			&version.Type, &version.Title, &version.Content, &version.ToolName,
			&version.Project, &version.Scope, &version.TopicKey, &version.RevisionCount,
			&version.IsBaseline, &version.HistoryComplete, &version.CapturedAt,
		); err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, ErrObservationNotFound
	}
	return versions, nil
}
