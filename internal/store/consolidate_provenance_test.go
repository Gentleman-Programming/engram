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
	placeholders := strings.Repeat("?,", len(syncIDs)-1) + "?"
	args := make([]any, 0, 2+len(syncIDs))
	args = append(args, SyncEntityObservation, SyncOpDelete)
	for _, id := range syncIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query(
		`SELECT payload FROM sync_mutations WHERE entity = ? AND op = ? AND entity_key IN (`+placeholders+`)`,
		args...,
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
// insert branch and the update branch. Contract agreed 2026-10-12 (#242):
// a missing or null field never erases an existing link on any apply path;
// a conflicting nonempty link follows last-applied ordering. Clearing
// provenance is outside the consolidation contract entirely.
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

	// Update branch: a full-row upsert without the field (a stale peer row
	// predating the consolidation) must preserve the existing link.
	payload.SupersededBySyncID = nil
	payload.Title = "Peer row edited"
	apply(payload)
	if err := s.db.QueryRow(`SELECT superseded_by_sync_id FROM observations WHERE sync_id = 'obs-peer-1'`).Scan(&got); err != nil {
		t.Fatalf("select after null-field update apply: %v", err)
	}
	if !got.Valid || got.String != superseded {
		t.Fatalf("update branch without the field must preserve superseded_by_sync_id, got %v", got)
	}

	// A conflicting nonempty link follows last-applied ordering.
	otherSurvivor := "obs-superseded-other"
	payload.SupersededBySyncID = &otherSurvivor
	payload.Title = "Peer row re-consolidated"
	apply(payload)
	if err := s.db.QueryRow(`SELECT superseded_by_sync_id FROM observations WHERE sync_id = 'obs-peer-1'`).Scan(&got); err != nil {
		t.Fatalf("select after conflicting update apply: %v", err)
	}
	if !got.Valid || got.String != otherSurvivor {
		t.Fatalf("conflicting nonempty link must follow last-applied ordering, got %v", got)
	}

	// A later field-less upsert still preserves the newest link.
	payload.SupersededBySyncID = nil
	payload.Title = "Peer row edited again"
	apply(payload)
	if err := s.db.QueryRow(`SELECT superseded_by_sync_id FROM observations WHERE sync_id = 'obs-peer-1'`).Scan(&got); err != nil {
		t.Fatalf("select after second null-field update apply: %v", err)
	}
	if !got.Valid || got.String != otherSurvivor {
		t.Fatalf("null-field upsert after re-consolidation must preserve the newest link, got %v", got)
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

// Contract (#242, 2026-10-12): a later peer upsert may restore a consolidated
// source, but its known provenance remains available. The resurrection upsert
// is a full row predating the consolidation (no field, not deleted): it
// restores liveness while the marker survives, so the one-query detector
// surfaces the anomaly instead of hiding it.
func TestApplyObservationUpsertTx_ResurrectionKeepsProvenanceDetectable(t *testing.T) {
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

	stale := syncObservationPayload{
		SyncID: syncIDs[0], SessionID: "ses-cons", Type: "decision",
		Title: "Stale source", Content: "Stale source content",
		Project: nullableString("proj-cons"), Scope: "project",
	}
	tx, err := s.beginTxHook()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := s.applyObservationUpsertTx(tx, stale); err != nil {
		t.Fatalf("apply resurrection upsert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var deletedAt sql.NullString
	var marker sql.NullString
	if err := s.db.QueryRow(
		`SELECT deleted_at, superseded_by_sync_id FROM observations WHERE sync_id = ?`, syncIDs[0],
	).Scan(&deletedAt, &marker); err != nil {
		t.Fatalf("select resurrected source: %v", err)
	}
	if deletedAt.Valid {
		t.Fatalf("resurrection upsert must restore liveness, deleted_at=%q", deletedAt.String)
	}
	if !marker.Valid || marker.String != res.Survivor.SyncID {
		t.Fatalf("resurrection upsert must keep superseded_by_sync_id=%s, got %v", res.Survivor.SyncID, marker)
	}

	got, err := s.DetectResurrectedObservations()
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(got) != 1 || got[0].ID != ids[0] {
		t.Fatalf("detector must return exactly the resurrected source, got %+v", got)
	}
	if got[0].SupersededBySyncID == nil || *got[0].SupersededBySyncID != res.Survivor.SyncID {
		t.Fatalf("detector result must expose the surviving provenance, got %+v", got[0])
	}
}

// Contract point 6, backup round-trip: export/import preserves the provenance
// column. The field rides the full Observation struct in ExportData, so this
// pins the surface (it cannot regress silently).
func TestExportImportRoundTripPreservesSupersededBy(t *testing.T) {
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

	data, err := s.Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	fresh := newTestStore(t)
	if _, err := fresh.Import(data); err != nil {
		t.Fatalf("import: %v", err)
	}
	var got sql.NullString
	if err := fresh.db.QueryRow(
		`SELECT superseded_by_sync_id FROM observations WHERE sync_id = ?`, res.Survivor.SyncID,
	).Scan(&got); err != nil {
		t.Fatalf("select imported survivor: %v", err)
	}
	if got.Valid {
		t.Fatalf("imported survivor must not be superseded, got %q", got.String)
	}
	for _, id := range ids {
		src := s.getObservationByIDOrFatal(t, id)
		if err := fresh.db.QueryRow(
			`SELECT superseded_by_sync_id FROM observations WHERE sync_id = ?`, src.SyncID,
		).Scan(&got); err != nil {
			t.Fatalf("select imported source %d: %v", id, err)
		}
		if !got.Valid || got.String != res.Survivor.SyncID {
			t.Fatalf("import must preserve superseded_by_sync_id for source %d, got %v", id, got)
		}
	}
}

// Contract point 6, out-of-order delivery: a consolidation delete that
// arrives before its source is a no-op (no placeholder rows); when the stale
// source upsert lands afterwards it becomes a live, unmarked row — the
// durable provenance record for that window is the survivor's
// consolidated_from footer.
func TestApplyObservationDeleteTx_UnseenSourceNoOpThenStaleUpsert(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-peer3", "proj-peer3", "/tmp/peer3"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	deletedAt := "2026-10-12 09:00:00"
	survivor := "obs-survivor-unseen"
	del := syncObservationPayload{
		SyncID: "obs-unseen", SessionID: "ses-peer3", Type: "decision",
		Title: "Unseen source", Content: "Unseen source content",
		Project: nullableString("proj-peer3"), Deleted: true, DeletedAt: &deletedAt,
		SupersededBySyncID: &survivor,
	}
	tx, err := s.beginTxHook()
	if err != nil {
		t.Fatalf("begin delete: %v", err)
	}
	if err := s.applyObservationDeleteTx(tx, del); err != nil {
		t.Fatalf("apply unseen delete: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit delete: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM observations WHERE sync_id = 'obs-unseen'`).Scan(&n); err != nil {
		t.Fatalf("count placeholders: %v", err)
	}
	if n != 0 {
		t.Fatalf("delete-before-source must not create placeholder rows, got %d", n)
	}

	stale := del
	stale.Deleted = false
	stale.DeletedAt = nil
	stale.SupersededBySyncID = nil
	tx, err = s.beginTxHook()
	if err != nil {
		t.Fatalf("begin upsert: %v", err)
	}
	if err := s.applyObservationUpsertTx(tx, stale); err != nil {
		t.Fatalf("apply stale upsert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit upsert: %v", err)
	}
	var deletedAtAfter sql.NullString
	if err := s.db.QueryRow(
		`SELECT deleted_at FROM observations WHERE sync_id = 'obs-unseen'`,
	).Scan(&deletedAtAfter); err != nil {
		t.Fatalf("select after stale upsert: %v", err)
	}
	if deletedAtAfter.Valid {
		t.Fatalf("stale upsert after unseen delete must land live, deleted_at=%q", deletedAtAfter.String)
	}
}

// getObservationByIDOrFatal loads an Observation by row id for round-trip
// assertions.
func (s *Store) getObservationByIDOrFatal(t *testing.T, id int64) Observation {
	t.Helper()
	var o Observation
	if err := s.db.QueryRow(
		`SELECT id, sync_id, session_id, type, title FROM observations WHERE id = ?`, id,
	).Scan(&o.ID, &o.SyncID, &o.SessionID, &o.Type, &o.Title); err != nil {
		t.Fatalf("load observation %d: %v", id, err)
	}
	return o
}

// Backfill re-journals existing rows; the re-journalled payloads must carry
// the provenance link for both live and deleted observations (issue #242
// slice 2, contract point 3).
func TestBackfillObservationSyncMutationsCarriesSupersededBy(t *testing.T) {
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
	// Force backfill to re-journal: drop the pending observation mutations.
	if _, err := s.db.Exec(`DELETE FROM sync_mutations WHERE entity = ?`, SyncEntityObservation); err != nil {
		t.Fatalf("wipe observation mutations: %v", err)
	}
	tx, err := s.beginTxHook()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := s.backfillObservationSyncMutationsTx(tx, "proj-cons"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("backfill: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	for _, id := range ids {
		src := s.getObservationByIDOrFatal(t, id)
		var payload string
		if err := s.db.QueryRow(
			`SELECT payload FROM sync_mutations WHERE entity = ? AND entity_key = ? AND op = ?`,
			SyncEntityObservation, src.SyncID, SyncOpDelete,
		).Scan(&payload); err != nil {
			t.Fatalf("select backfilled delete for %s: %v", src.SyncID, err)
		}
		var decoded syncObservationPayload
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if decoded.SupersededBySyncID == nil || *decoded.SupersededBySyncID != res.Survivor.SyncID {
			t.Fatalf("backfilled delete must carry superseded_by_sync_id=%s for %s, got %v", res.Survivor.SyncID, src.SyncID, decoded.SupersededBySyncID)
		}
	}
}

// The project-rescue re-journal path must preserve the provenance link on
// rescued observation mutations (issue #242 slice 2, contract point 3).
func TestRescueNullProjectOwnershipCarriesSupersededBy(t *testing.T) {
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
	if _, err := s.db.Exec(`DELETE FROM sync_mutations WHERE entity = ?`, SyncEntityObservation); err != nil {
		t.Fatalf("wipe observation mutations: %v", err)
	}
	if _, err := s.RescueNullProjectOwnership(ProjectRescueParams{
		TargetProject:  "proj-cons",
		ObservationIDs: ids,
	}); err != nil {
		t.Fatalf("rescue: %v", err)
	}

	for _, id := range ids {
		src := s.getObservationByIDOrFatal(t, id)
		var payload string
		if err := s.db.QueryRow(
			`SELECT payload FROM sync_mutations WHERE entity = ? AND entity_key = ?`,
			SyncEntityObservation, src.SyncID,
		).Scan(&payload); err != nil {
			t.Fatalf("select rescued mutation for %s: %v", src.SyncID, err)
		}
		var decoded syncObservationPayload
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if decoded.SupersededBySyncID == nil || *decoded.SupersededBySyncID != res.Survivor.SyncID {
			t.Fatalf("rescued mutation must carry superseded_by_sync_id=%s for %s, got %v", res.Survivor.SyncID, src.SyncID, decoded.SupersededBySyncID)
		}
	}
}
