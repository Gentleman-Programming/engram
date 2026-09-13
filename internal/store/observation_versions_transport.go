package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

type observationVersionScanner interface {
	Scan(dest ...any) error
}

func scanObservationVersion(scanner observationVersionScanner, version *ObservationVersion) error {
	return scanner.Scan(
		&version.VersionID, &version.ObservationSyncID, &version.SessionID,
		&version.Type, &version.Title, &version.Content, &version.ToolName,
		&version.Project, &version.Scope, &version.TopicKey, &version.RevisionCount,
		&version.IsBaseline, &version.HistoryComplete, &version.CapturedAt,
	)
}

func (s *Store) exportObservationVersions(observations []Observation) ([]ObservationVersion, error) {
	selected := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		selected[observation.SyncID] = struct{}{}
	}
	if len(selected) == 0 {
		return nil, nil
	}
	rows, err := s.queryItHook(s.db, `
		SELECT version_id, observation_sync_id, session_id, type, title, content,
		       tool_name, project, scope, topic_key, revision_count, is_baseline,
		       history_complete, captured_at
		FROM observation_versions
		ORDER BY observation_sync_id, revision_count DESC, version_id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []ObservationVersion
	for rows.Next() {
		var version ObservationVersion
		if err := scanObservationVersion(rows, &version); err != nil {
			return nil, err
		}
		if _, ok := selected[version.ObservationSyncID]; ok {
			versions = append(versions, version)
		}
	}
	return versions, rows.Err()
}

var observationVersionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func validateObservationVersion(version ObservationVersion) error {
	for name, value := range map[string]string{
		"version_id":          version.VersionID,
		"observation_sync_id": version.ObservationSyncID,
		"session_id":          version.SessionID,
		"type":                version.Type,
		"title":               version.Title,
		"content":             version.Content,
		"scope":               version.Scope,
		"captured_at":         version.CapturedAt,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%s is required", name)
		}
	}
	if !observationVersionIDPattern.MatchString(version.VersionID) {
		return fmt.Errorf("version_id must be a canonical UUIDv4")
	}
	if version.RevisionCount < 1 {
		return fmt.Errorf("revision_count must be positive")
	}
	return nil
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameObservationVersion(left, right ObservationVersion) bool {
	return left.VersionID == right.VersionID &&
		left.ObservationSyncID == right.ObservationSyncID && left.SessionID == right.SessionID &&
		left.Type == right.Type && left.Title == right.Title && left.Content == right.Content &&
		sameOptionalString(left.ToolName, right.ToolName) && sameOptionalString(left.Project, right.Project) &&
		sameOptionalString(left.TopicKey, right.TopicKey) && left.Scope == right.Scope &&
		left.RevisionCount == right.RevisionCount && left.IsBaseline == right.IsBaseline &&
		left.HistoryComplete == right.HistoryComplete && left.CapturedAt == right.CapturedAt
}

func (s *Store) importObservationVersionsTx(tx *sql.Tx, versions []ObservationVersion) (int, error) {
	imported := 0
	for _, version := range versions {
		if err := validateObservationVersion(version); err != nil {
			return 0, err
		}
		var observationID int64
		var sessionID string
		var project *string
		if err := tx.QueryRow(`SELECT id, session_id, project FROM observations WHERE sync_id = ? LIMIT 1`, version.ObservationSyncID).Scan(&observationID, &sessionID, &project); err != nil {
			if err == sql.ErrNoRows {
				return 0, fmt.Errorf("parent observation %q not found", version.ObservationSyncID)
			}
			return 0, err
		}
		if version.SessionID != sessionID || !sameOptionalString(version.Project, project) {
			return 0, fmt.Errorf("version %q parent metadata does not match observation", version.VersionID)
		}
		var existing ObservationVersion
		err := tx.QueryRow(`
			SELECT version_id, observation_sync_id, session_id, type, title, content,
			       tool_name, project, scope, topic_key, revision_count, is_baseline,
			       history_complete, captured_at
			FROM observation_versions WHERE version_id = ?`, version.VersionID).Scan(
			&existing.VersionID, &existing.ObservationSyncID, &existing.SessionID,
			&existing.Type, &existing.Title, &existing.Content, &existing.ToolName,
			&existing.Project, &existing.Scope, &existing.TopicKey, &existing.RevisionCount,
			&existing.IsBaseline, &existing.HistoryComplete, &existing.CapturedAt,
		)
		if err == nil {
			if !sameObservationVersion(existing, version) {
				return 0, fmt.Errorf("version %q conflicts with immutable stored snapshot", version.VersionID)
			}
			continue
		}
		if err != sql.ErrNoRows {
			return 0, err
		}
		result, err := s.execHook(tx, `
			INSERT INTO observation_versions (
				version_id, observation_id, observation_sync_id, session_id, type, title,
				content, tool_name, project, scope, topic_key, revision_count, is_baseline,
				history_complete, captured_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			version.VersionID, observationID, version.ObservationSyncID, version.SessionID,
			version.Type, version.Title, version.Content, version.ToolName, version.Project,
			version.Scope, version.TopicKey, version.RevisionCount, version.IsBaseline,
			version.HistoryComplete, version.CapturedAt,
		)
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		imported += int(count)
	}
	return imported, nil
}
