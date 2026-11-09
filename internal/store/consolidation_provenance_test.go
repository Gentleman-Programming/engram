package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func applyProvenanceMutation(t *testing.T, s *Store, seq int64, op, key, fields string) {
	t.Helper()
	payload := fmt.Sprintf(`{"sync_id":%q,"session_id":"ses-prov","project":"proj-prov",%s}`, key, fields)
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{Seq: seq, Entity: SyncEntityObservation, EntityKey: key, Project: "proj-prov", Op: op, Payload: payload}); err != nil {
		t.Fatal(err)
	}
}

func TestProvenanceReceivingLifecycle(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-prov", "proj-prov", "/tmp/prov"); err != nil {
		t.Fatal(err)
	}
	applyProvenanceMutation(t, s, 1, SyncOpDelete, "obs-prov", `"deleted_at":"2026-01-01 00:00:00","superseded_by_sync_id":"obs-survivor"`)
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Observations) != 0 {
		t.Fatal("delete created a placeholder")
	}
	applyProvenanceMutation(t, s, 2, SyncOpUpsert, "obs-prov", `"type":"decision","title":"Restored","content":"source"`)
	data, err = s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Observations) != 1 || derefString(data.Observations[0].SupersededBySyncID) != "obs-survivor" || data.Observations[0].DeletedAt != nil {
		t.Fatalf("restored source = %+v", data.Observations)
	}
	for i, fields := range []string{
		`"type":"decision","title":"Legacy","content":"source"`,
		`"type":"decision","title":"Null","content":"source","superseded_by_sync_id":null`,
		`"type":"decision","title":"Empty","content":"source","superseded_by_sync_id":""`,
	} {
		applyProvenanceMutation(t, s, int64(i+3), SyncOpUpsert, "obs-prov", fields)
		data, err = s.Export()
		if err != nil {
			t.Fatal(err)
		}
		if derefString(data.Observations[0].SupersededBySyncID) != "obs-survivor" {
			t.Fatal("legacy payload erased provenance")
		}
	}
	applyProvenanceMutation(t, s, 6, SyncOpDelete, "obs-prov", `"superseded_by_sync_id":"obs-new-survivor"`)
	data, err = s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if derefString(data.Observations[0].SupersededBySyncID) != "obs-new-survivor" {
		t.Fatal("conflicting deletion did not replace provenance")
	}
	applyProvenanceMutation(t, s, 7, SyncOpUpsert, "obs-prov", `"type":"decision","title":"Restored again","content":"source"`)
	data, err = s.Export()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var backup ExportData
	if err := json.Unmarshal(raw, &backup); err != nil {
		t.Fatal(err)
	}
	restored := newTestStore(t)
	if _, err := restored.Import(&backup); err != nil {
		t.Fatal(err)
	}
	got, err := restored.GetObservation(data.Observations[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if derefString(got.SupersededBySyncID) != "obs-new-survivor" {
		t.Fatalf("backup provenance = %v", got.SupersededBySyncID)
	}
}

func TestConsolidationStructuredProvenance(t *testing.T) {
	s, ids, _ := consolidateTestStore(t)
	plan, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids, Confirm: true, Fingerprint: plan.Plan.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var exported struct {
		Observations []map[string]any `json:"observations"`
	}
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, obs := range exported.Observations {
		if obs["deleted_at"] != nil {
			count++
			if obs["superseded_by_sync_id"] != result.Survivor.SyncID {
				t.Fatalf("source provenance = %v; want %s", obs["superseded_by_sync_id"], result.Survivor.SyncID)
			}
		}
	}
	if count != len(ids) {
		t.Fatalf("deleted sources = %d", count)
	}
	mutations, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 1000)
	if err != nil {
		t.Fatal(err)
	}
	deletes := 0
	for _, mutation := range mutations {
		if mutation.Entity != SyncEntityObservation || mutation.Op != SyncOpDelete {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["superseded_by_sync_id"] != result.Survivor.SyncID {
			t.Fatalf("delete payload lost provenance: %s", mutation.Payload)
		}
		deletes++
	}
	if deletes != len(ids) {
		t.Fatalf("source delete mutations = %d", deletes)
	}
	restored := newTestStore(t)
	var backup ExportData
	if err := json.Unmarshal(raw, &backup); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Import(&backup); err != nil {
		t.Fatal(err)
	}
	copy, err := restored.Export()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(copy.Observations, data.Observations) {
		t.Fatal("backup did not preserve soft-deleted source provenance")
	}
}

func TestProvenanceDeletionAndEnrollmentProjections(t *testing.T) {
	s, ids, _ := consolidateTestStore(t)
	if err := s.UnenrollProject("proj-cons"); err != nil {
		t.Fatal(err)
	}
	preview, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids, Confirm: true, Fingerprint: preview.Plan.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservation(ids[0], true); err != nil {
		t.Fatal(err)
	}
	tombstones, err := s.ExportLocalDeleteTombstones("proj-cons")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mutation := range tombstones {
		if mutation.Entity != SyncEntityObservation {
			continue
		}
		var payload syncObservationPayload
		if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		if derefString(payload.SupersededBySyncID) != result.Survivor.SyncID {
			t.Fatal("hard deletion tombstone lost provenance")
		}
		found = true
	}
	if !found {
		t.Fatal("hard deletion tombstone missing")
	}
	if err := s.EnrollProject("proj-cons"); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 1000)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, mutation := range pending {
		if mutation.Entity != SyncEntityObservation || mutation.Op != SyncOpDelete {
			continue
		}
		var payload syncObservationPayload
		if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		if derefString(payload.SupersededBySyncID) != result.Survivor.SyncID {
			t.Fatalf("backfilled deletion lost provenance: %s", mutation.Payload)
		}
		count++
	}
	if count != len(ids) {
		t.Fatalf("backfilled deletions = %d", count)
	}
}

func TestProvenanceFullRowJournal(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("ses-prov", "proj-prov", "/tmp/prov"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("proj-prov"); err != nil {
		t.Fatal(err)
	}
	applyProvenanceMutation(t, s, 1, SyncOpUpsert, "obs-prov", `"type":"decision","title":"Source","content":"source","superseded_by_sync_id":"obs-target"`)
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	title := "Updated source"
	got, err := s.UpdateObservation(data.Observations[0].ID, UpdateObservationParams{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	if derefString(got.SupersededBySyncID) != "obs-target" {
		t.Fatal("local edit erased provenance")
	}
	pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mutation := range pending {
		if mutation.Entity != SyncEntityObservation || mutation.EntityKey != "obs-prov" || mutation.Op != SyncOpUpsert {
			continue
		}
		var payload syncObservationPayload
		if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		if derefString(payload.SupersededBySyncID) != "obs-target" {
			t.Fatal("full-row mutation lost provenance")
		}
		found = true
	}
	if !found {
		t.Fatal("full-row mutation missing")
	}
	if err := s.DeleteObservation(got.ID, false); err != nil {
		t.Fatal(err)
	}
	data, err = s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if derefString(data.Observations[0].SupersededBySyncID) != "obs-target" {
		t.Fatal("local soft-delete erased provenance")
	}
}

func TestProvenanceInvalidatesPreview(t *testing.T) {
	s, ids, _ := consolidateTestStore(t)
	preview, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.GetObservation(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	source.SupersededBySyncID = nullableString("obs-prior-survivor")
	payload, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPulledMutation(DefaultSyncTargetKey, SyncMutation{Seq: 1, Entity: SyncEntityObservation, EntityKey: source.SyncID, Project: "proj-cons", Op: SyncOpUpsert, Payload: string(payload)}); err != nil {
		t.Fatal(err)
	}
	before := snapshotConsolidation(t, s)
	result, err := s.ConsolidateObservations(ConsolidateParams{IDs: ids, Confirm: true, Fingerprint: preview.Plan.Fingerprint})
	if !errors.Is(err, ErrConsolidateFingerprintMismatch) || result == nil || result.Survivor != nil {
		t.Fatalf("confirmation = %+v, %v", result, err)
	}
	assertConsolidationUnchanged(t, s, before)
	if result.Plan.Fingerprint == preview.Plan.Fingerprint {
		t.Fatal("provenance change did not invalidate preview")
	}
}

func TestProvenanceMigrationDoesNotInferLinks(t *testing.T) {
	s := newTestStoreWithLegacySchema(t, []legacyObsRow{{syncID: "obs-legacy-provenance", sessionID: "ses-migration-test", obsType: "decision", title: "Legacy", content: "source\n\n## consolidated_from\n- obs-old-survivor", project: "engram", scope: "project"}})
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Observations) != 1 || data.Observations[0].SupersededBySyncID != nil {
		t.Fatal("migration inferred provenance")
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetObservation(data.Observations[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SupersededBySyncID != nil {
		t.Fatal("repeat migration changed provenance")
	}
}
