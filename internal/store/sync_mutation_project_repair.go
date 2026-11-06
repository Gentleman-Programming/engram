package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SyncMutationProjectRepairAction identifies a journal owner restored from a session.
type SyncMutationProjectRepairAction struct {
	Seq       int64  `json:"seq"`
	Project   string `json:"project"`
	EntityKey string `json:"entity_key"`
}

// RepairPendingSessionProjects restores only complete, authoritative Cloud journal
// owners. Payload bytes and acknowledgement/disposition metadata stay unchanged.
func (s *Store) RepairPendingSessionProjects(project string, apply bool) ([]SyncMutationProjectRepairAction, error) {
	project, _ = NormalizeProject(project)
	actions := make([]SyncMutationProjectRepairAction, 0)
	run := func(tx *sql.Tx) error {
		var enrolled int
		if err := tx.QueryRow(`SELECT count(*) FROM sync_enrolled_projects`).Scan(&enrolled); err != nil {
			return err
		}
		if enrolled == 0 {
			return nil
		}
		mutations, err := s.listPendingProjectMutationsTxLike(tx, "")
		if err != nil {
			return err
		}
		for _, mutation := range mutations {
			if mutation.Project != "" || mutation.Disposition != SyncMutationDispositionPending || mutation.Entity != SyncEntitySession || mutation.Op != SyncOpUpsert {
				continue
			}
			if ValidateSyncMutationPayload(mutation.Entity, mutation.Op, mutation.Payload, mutation.EntityKey).ReasonCode != "" {
				continue
			}
			var body syncSessionPayload
			if decodeSyncPayload([]byte(mutation.Payload), &body) != nil || body.ID != mutation.EntityKey || strings.TrimSpace(body.Directory) == "" {
				continue
			}
			var owner, directory string
			err := tx.QueryRow(`SELECT project, ifnull(directory, '') FROM sessions WHERE id = ?`, body.ID).Scan(&owner, &directory)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			owner, _ = NormalizeProject(owner)
			payloadProject, _ := NormalizeProject(body.Project)
			if owner == "" || payloadProject != owner || strings.TrimSpace(directory) != body.Directory || (project != "" && project != owner) {
				continue
			}
			eval, err := s.evaluateCloudUpgradeLegacyMutationTx(tx, mutation)
			if err != nil {
				return err
			}
			if !eval.canRepair {
				continue
			}
			if apply {
				result, err := s.execHook(tx, `UPDATE sync_mutations SET project = ? WHERE seq = ? AND target_key = ? AND project = '' AND acked_at IS NULL AND disposition = ?`, owner, mutation.Seq, DefaultSyncTargetKey, SyncMutationDispositionPending)
				if err != nil {
					return err
				}
				if n, err := result.RowsAffected(); err != nil || n != 1 {
					return fmt.Errorf("restore session mutation project for seq %d: changed=%d: %v", mutation.Seq, n, err)
				}
			}
			actions = append(actions, SyncMutationProjectRepairAction{Seq: mutation.Seq, Project: owner, EntityKey: mutation.EntityKey})
		}
		return nil
	}
	var err error
	if apply {
		err = s.withTx(run)
	} else {
		err = s.withReadTx(run)
	}
	return actions, err
}
