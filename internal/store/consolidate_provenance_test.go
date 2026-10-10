package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// Tests for the column provenance of mem_consolidate (issue #242, design
// option (c)): the superseded_by_sync_id migration, in-transaction writes on
// consolidation, sync payload replication in both apply directions, and the
// one-query resurrection detector.

func observationSupersededBy(t *testing.T, s *Store, id int64) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := s.db.QueryRow(`SELECT superseded_by_sync_id FROM observations WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("select superseded_by_sync_id for %d: %v", id, err)
	}
	return v
}

// The migration adds superseded_by_sync_id to observations, idempotently
// (monolithic DDL + addColumnIfNotExists precedent; no schemaVersion bump).
func TestMigrate_AddsSupersededBySyncIDColumn(t *testing.T) {
	s := newTestStore(t)
	var present bool
	rows, err := s.db.Query(`PRAGMA table_info(observations)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan pragma: %v", err)
		}
		if name == "superseded_by_sync_id" {
			present = true
		}
	}
	if !present {
		t.Fatal("migrate() must add the superseded_by_sync_id column")
	}
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate must stay idempotent: %v", err)
	}
}

// A confirmed consolidation sets superseded_by_sync_id on every source in the
// same transaction; the survivor carries none.
func TestConsolidateObservations_SetsSupersededByOnSources(t *testing.T) {
	s, ids, _ := consolidateTestStore(t)
	plan, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := s.ConsolidateObservations(ConsolidateParams{
		IDs:         ids,
		Confirm:     true,
		Fingerprint: plan.Plan.Fingerprint,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, id := range ids {
		got := observationSupersededBy(t, s, id)
		if !got.Valid || got.String != res.Survivor.SyncID {
			t.Fatalf("source %d superseded_by: got %v, want %s", id, got, res.Survivor.SyncID)
		}
	}
	if got := observationSupersededBy(t, s, res.Survivor.ID); got.Valid {
		t.Fatalf("survivor must not be superseded, got %q", got.String)
	}
}

// The delete journal rows carry superseded_by_sync_id so peers can mark their
// copies; the survivor upsert payload carries none.
func TestConsolidateProvenance_DeleteJournalRidesSupersededBy(t *testing.T) {
	s, ids, syncIDs := consolidateTestStore(t)
	plan, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	res, err := s.ConsolidateObservations(ConsolidateParams{
		IDs:         ids,
		Confirm:     true,
		Fingerprint: plan.Plan.Fingerprint,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	surv := res.Survivor

	var deletePayloads []string
	rows, err := s.db.Query(
		`SELECT payload FROM sync_mutations WHERE entity = ? AND op = ? AND seq > ?`,
		SyncEntityObservation, SyncOpDelete, maxSyncMutationSeq(t, s)-3,
	)
	if err != nil {
		t.Fatalf("select delete payloads: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan payload: %v", err)
		}
		deletePayloads = append(deletePayloads, payload)
	}
	if len(deletePayloads) != len(syncIDs) {
		t.Fatalf("delete payloads: got %d, want %d", len(deletePayloads), len(syncIDs))
	}
	for _, payload := range deletePayloads {
		var decoded syncObservationPayload
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if decoded.SupersededBySyncID == nil || *decoded.SupersededBySyncID != surv.SyncID {
			t.Fatalf("delete payload must carry superseded_by_sync_id=%s, got %v", surv.SyncID, decoded.SupersededBySyncID)
		}
	}

	var survivorPayload string
	if err := s.db.QueryRow(
		`SELECT payload FROM sync_mutations WHERE entity_key = ? AND op = ?`,
		surv.SyncID, SyncOpUpsert,
	).Scan(&survivorPayload); err != nil {
		t.Fatalf("select survivor payload: %v", err)
	}
	if strings.Contains(survivorPayload, "superseded_by_sync_id") {
		t.Fatalf("survivor upsert payload must not carry superseded_by_sync_id: %s", survivorPayload)
	}
}

// Peer apply, upsert direction: superseded_by_sync_id replicates on both the
// insert branch and the update branch (full-row last-write-wins: an upsert
// without the field clears it, like every other column).
func TestApplyObservationUpsertTx_ReplicatesSupersededBy(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-peer", "proj-peer", "/tmp/peer"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	superseded := "obs-superseded-peer"

	apply := func(payload syncObservationPayload) {
		t.Helper()
		tx, err := s.beginTxHook()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := s.applyObservationUpsertTx(tx, payload); err != nil {
			t.Fatalf("applyObservationUpsertTx: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	payload := syncObservationPayload{
		SyncID: "obs-peer-1", SessionID: "ses-peer", Type: "decision",
		Title: "Peer row", Content: "Peer content", Project: nullableString("proj-peer"),
		SupersededBySyncID: &superseded,
	}
	apply(payload)
	var got sql.NullString
	if err := s.db.QueryRow(`SELECT superseded_by_sync_id FROM observations WHERE sync_id = 'obs-peer-1'`).Scan(&got); err != nil {
		t.Fatalf("select after insert-branch apply: %v", err)
	}
	if !got.Valid || got.String != superseded {
		t.Fatalf("insert branch must persist superseded_by_sync_id, got %v", got)
	}

	// Update branch: same sync_id, field cleared — last-write-wins.
	payload.SupersededBySyncID = nil
	payload.Title = "Peer row edited"
	apply(payload)
	if err := s.db.QueryRow(`SELECT superseded_by_sync_id FROM observations WHERE sync_id = 'obs-peer-1'`).Scan(&got); err != nil {
		t.Fatalf("select after update-branch apply: %v", err)
	}
	if got.Valid {
		t.Fatalf("update branch without the field must clear it (LWW), got %q", got.String)
	}
}

// Peer apply, delete direction: a consolidation delete marks superseded_by on
// the peer copy; a plain delete (no field) preserves an existing value.
func TestApplyObservationDeleteTx_MarksSupersededBy(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-peer2", "proj-peer2", "/tmp/peer2"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	superseded := "obs-superseded-peer2"

	seed := syncObservationPayload{
		SyncID: "obs-peer-2", SessionID: "ses-peer2", Type: "decision",
		Title: "Peer source", Content: "Peer source content", Project: nullableString("proj-peer2"),
	}
	tx, err := s.beginTxHook()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := s.applyObservationUpsertTx(tx, seed); err != nil {
		t.Fatalf("seed apply: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	deletedAt := "2026-10-10 09:00:00"
	del := seed
	del.Deleted = true
	del.DeletedAt = &deletedAt
	del.SupersededBySyncID = &superseded
	tx, err = s.beginTxHook()
	if err != nil {
		t.Fatalf("begin delete: %v", err)
	}
	if err := s.applyObservationDeleteTx(tx, del); err != nil {
		t.Fatalf("applyObservationDeleteTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit delete: %v", err)
	}
	var got sql.NullString
	if err := s.db.QueryRow(`SELECT superseded_by_sync_id FROM observations WHERE sync_id = 'obs-peer-2'`).Scan(&got); err != nil {
		t.Fatalf("select after delete apply: %v", err)
	}
	if !got.Valid || got.String != superseded {
		t.Fatalf("consolidation delete must set superseded_by_sync_id, got %v", got)
	}

	// A later plain delete (no field) must not clobber the provenance.
	plain := del
	plain.SupersededBySyncID = nil
	tx, err = s.beginTxHook()
	if err != nil {
		t.Fatalf("begin plain delete: %v", err)
	}
	if err := s.applyObservationDeleteTx(tx, plain); err != nil {
		t.Fatalf("applyObservationDeleteTx plain: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit plain: %v", err)
	}
	if err := s.db.QueryRow(`SELECT superseded_by_sync_id FROM observations WHERE sync_id = 'obs-peer-2'`).Scan(&got); err != nil {
		t.Fatalf("select after plain delete: %v", err)
	}
	if !got.Valid || got.String != superseded {
		t.Fatalf("plain delete must preserve superseded_by_sync_id, got %v", got)
	}
}

// The resurrection detector: one query returning live rows that carry
// superseded_by_sync_id (the documented peer-edit anomaly surface).
func TestDetectResurrectedObservations(t *testing.T) {
	s, ids, _ := consolidateTestStore(t)
	plan, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := s.ConsolidateObservations(ConsolidateParams{
		IDs:         ids,
		Confirm:     true,
		Fingerprint: plan.Plan.Fingerprint,
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got, err := s.DetectResurrectedObservations(); err != nil {
		t.Fatalf("detect after consolidation: %v", err)
	} else if len(got) != 0 {
		t.Fatalf("deleted sources are not resurrections, got %d", len(got))
	}

	// Simulate the anomaly: a peer upsert resurrects a superseded source.
	if _, err := s.db.Exec(
		`UPDATE observations SET deleted_at = NULL, updated_at = datetime('now') WHERE id = ?`, ids[0],
	); err != nil {
		t.Fatalf("seed resurrection: %v", err)
	}
	got, err := s.DetectResurrectedObservations()
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(got) != 1 || got[0].ID != ids[0] {
		t.Fatalf("detector must return exactly the resurrected source, got %+v", got)
	}
	if got[0].SupersededBySyncID == nil {
		t.Fatal("detector result must expose superseded_by_sync_id")
	}
}
