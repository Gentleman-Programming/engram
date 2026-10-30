package store

import (
	"database/sql"
)

// observationProvenanceTx resolves partial payload metadata without inferring
// links from content. Nonempty input wins in existing last-applied order.
func (s *Store) observationProvenanceTx(tx *sql.Tx, syncID string, incoming *string) (*string, error) {
	if incoming != nil && *incoming != "" {
		return incoming, nil
	}
	var link *string
	err := tx.QueryRow(`SELECT COALESCE(
  (SELECT superseded_by_sync_id FROM observations WHERE sync_id = ?),
  (SELECT superseded_by_sync_id FROM sync_delete_tombstones WHERE entity = 'observation' AND entity_key = ?))`, syncID, syncID).Scan(&link)
	return link, err
}

// preserveObservationProvenanceTx runs before tombstone deactivation so a
// delete delivered before its source can supply the later restored row's link.
func (s *Store) preserveObservationProvenanceTx(tx *sql.Tx, syncID string, incoming *string) error {
	link, err := s.observationProvenanceTx(tx, syncID, incoming)
	if err != nil {
		return err
	}
	_, err = s.execHook(tx, `UPDATE observations SET superseded_by_sync_id = ? WHERE sync_id = ?`, link, syncID)
	return err
}

// preserveObservationDeleteProvenanceTx supplements the existing tombstone
// lifecycle; it never creates placeholder observations or changes its floors.
func (s *Store) preserveObservationDeleteProvenanceTx(tx *sql.Tx, payload syncObservationPayload, active bool) error {
	if payload.SupersededBySyncID == nil || *payload.SupersededBySyncID == "" {
		return nil
	}
	deletedAt := derefString(payload.DeletedAt)
	if deletedAt == "" {
		deletedAt = Now()
	}
	_, err := s.execHook(tx, `INSERT INTO sync_delete_tombstones
  (entity, entity_key, session_id, project, deleted_at, hard_delete, active, superseded_by_sync_id)
  VALUES ('observation', ?, ?, ?, ?, ?, ?, ?)
  ON CONFLICT(entity, entity_key) DO UPDATE SET superseded_by_sync_id = excluded.superseded_by_sync_id`,
		payload.SyncID, nullableString(payload.SessionID), derefString(payload.Project), deletedAt, payload.HardDelete, active, payload.SupersededBySyncID)
	return err
}
