package store_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

const provenanceSequenceProject = "provenance-sequences"

func newProvenanceSequenceStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.New(store.FallbackConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	if err := s.CreateSession("sequence-session", provenanceSequenceProject, "/fixture"); err != nil {
		t.Fatal(err)
	}
	return s
}

func applyProvenanceSequence(t *testing.T, s *store.Store, seq int64, op, key string, link *string) {
	t.Helper()
	payload := map[string]any{
		"sync_id": key, "session_id": "sequence-session", "project": provenanceSequenceProject,
		"type": "note", "scope": "project", "title": fmt.Sprintf("Source %d", seq),
		"content": fmt.Sprintf("Source content %d", seq),
	}
	if op == store.SyncOpDelete {
		payload["deleted_at"] = "2026-01-02 00:00:00"
	}
	if link != nil {
		payload["superseded_by_sync_id"] = *link
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPulledMutation(store.DefaultSyncTargetKey, store.SyncMutation{
		Seq: seq, Entity: store.SyncEntityObservation, EntityKey: key,
		Project: provenanceSequenceProject, Op: op, Payload: string(raw),
	}); err != nil {
		t.Fatal(err)
	}
}

func provenanceSequenceExport(t *testing.T, s *store.Store, count int) []store.Observation {
	t.Helper()
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Observations) != count {
		t.Fatalf("exported observations = %d, want %d", len(data.Observations), count)
	}
	return data.Observations
}

func provenanceSequencePending(t *testing.T, s *store.Store, count int) []store.SyncMutation {
	t.Helper()
	pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != count {
		t.Fatalf("pending mutations = %+v, want %d", pending, count)
	}
	return pending
}

func assertProvenanceSequenceLink(t *testing.T, obs store.Observation, want string) {
	t.Helper()
	if obs.SupersededBySyncID == nil || *obs.SupersededBySyncID != want {
		t.Fatalf("observation %s supersession = %v, want %q", obs.SyncID, obs.SupersededBySyncID, want)
	}
}

func assertProvenanceSequenceCursor(t *testing.T, s *store.Store, want int64) {
	t.Helper()
	state, err := s.GetSyncState(store.DefaultSyncTargetKey)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastPulledSeq != want {
		t.Fatalf("pull cursor = %d, want %d", state.LastPulledSeq, want)
	}
}

func enrollProvenanceSequenceAndAckSession(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.EnrollProject(provenanceSequenceProject); err != nil {
		t.Fatal(err)
	}
	pending := provenanceSequencePending(t, s, 1)
	if pending[0].Entity != store.SyncEntitySession || pending[0].Op != store.SyncOpUpsert || pending[0].EntityKey != "sequence-session" {
		t.Fatalf("initial enrollment mutation = %+v", pending[0])
	}
	if err := s.AckSyncMutations(store.DefaultSyncTargetKey, pending[0].Seq); err != nil {
		t.Fatal(err)
	}
	provenanceSequencePending(t, s, 0)
}

func TestProvenanceSequenceConflictingUpsertThenOmittedLink(t *testing.T) {
	s := newProvenanceSequenceStore(t)
	first, latest := "survivor-a", "survivor-b"
	applyProvenanceSequence(t, s, 1, store.SyncOpUpsert, "source", &first)
	initial := provenanceSequenceExport(t, s, 1)[0]
	assertProvenanceSequenceLink(t, initial, first)
	for _, step := range []struct {
		seq  int64
		link *string
	}{{2, &latest}, {3, nil}} {
		applyProvenanceSequence(t, s, step.seq, store.SyncOpUpsert, "source", step.link)
		got := provenanceSequenceExport(t, s, 1)[0]
		if got.ID != initial.ID || got.SyncID != "source" || got.DeletedAt != nil ||
			got.Title != fmt.Sprintf("Source %d", step.seq) || got.Content != fmt.Sprintf("Source content %d", step.seq) {
			t.Fatalf("upsert %d observation = %+v", step.seq, got)
		}
		assertProvenanceSequenceLink(t, got, latest)
		assertProvenanceSequenceCursor(t, s, step.seq)
		provenanceSequencePending(t, s, 0)
	}
}

func TestProvenanceSequenceMarkedDeleteThenOmittedLink(t *testing.T) {
	s := newProvenanceSequenceStore(t)
	applyProvenanceSequence(t, s, 1, store.SyncOpUpsert, "source", nil)
	initial := provenanceSequenceExport(t, s, 1)[0]
	link := "survivor"
	for _, step := range []struct {
		seq  int64
		link *string
	}{{2, &link}, {3, nil}} {
		applyProvenanceSequence(t, s, step.seq, store.SyncOpDelete, "source", step.link)
		if _, err := s.GetObservationBySyncID("source"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("deleted source lookup error = %v, want sql.ErrNoRows", err)
		}
		got := provenanceSequenceExport(t, s, 1)[0]
		if got.ID != initial.ID || got.SyncID != "source" || got.DeletedAt == nil || *got.DeletedAt != "2026-01-02 00:00:00" || got.Title != initial.Title || got.Content != initial.Content {
			t.Fatalf("delete %d observation = %+v", step.seq, got)
		}
		assertProvenanceSequenceLink(t, got, link)
		assertProvenanceSequenceCursor(t, s, step.seq)
		provenanceSequencePending(t, s, 0)
	}
}

func TestProvenanceSequenceReEnrollmentOfResurrectedSource(t *testing.T) {
	s := newProvenanceSequenceStore(t)
	enrollProvenanceSequenceAndAckSession(t, s)
	if err := s.UnenrollProject(provenanceSequenceProject); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, title := range []string{"First source", "Second source"} {
		id, err := s.AddObservation(store.AddObservationParams{SessionID: "sequence-session", Project: provenanceSequenceProject, Type: "note", Title: title, Content: title + " content", Scope: "project"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	params := store.ConsolidateParams{IDs: ids}
	preview, err := s.ConsolidateObservations(params)
	if err != nil {
		t.Fatal(err)
	}
	params.Confirm, params.Fingerprint = true, preview.Plan.Fingerprint
	result, err := s.ConsolidateObservations(params)
	if err != nil {
		t.Fatal(err)
	}
	rows := provenanceSequenceExport(t, s, 3)
	var source store.Observation
	for _, row := range rows {
		if row.ID == ids[0] || row.ID == ids[1] {
			if row.DeletedAt == nil {
				t.Fatalf("consolidated source is still live: %+v", row)
			}
			assertProvenanceSequenceLink(t, row, result.Survivor.SyncID)
		}
		if row.ID == ids[0] {
			source = row
		}
	}
	provenanceSequencePending(t, s, 0)
	deletedAt, err := time.Parse("2006-01-02 15:04:05", *source.DeletedAt)
	if err != nil {
		t.Fatal(err)
	}
	source.DeletedAt, source.SupersededBySyncID = nil, nil
	source.UpdatedAt = deletedAt.Add(time.Second).Format("2006-01-02 15:04:05")
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPulledMutation(store.DefaultSyncTargetKey, store.SyncMutation{Seq: 1, Entity: store.SyncEntityObservation, EntityKey: source.SyncID, Project: provenanceSequenceProject, Op: store.SyncOpUpsert, Payload: string(raw)}); err != nil {
		t.Fatal(err)
	}
	live, err := s.GetObservationBySyncID(source.SyncID)
	if err != nil {
		t.Fatal(err)
	}
	if live.ID != ids[0] || live.DeletedAt != nil {
		t.Fatalf("resurrected source = %+v", live)
	}
	assertProvenanceSequenceLink(t, *live, result.Survivor.SyncID)
	assertProvenanceSequenceCursor(t, s, 1)
	provenanceSequencePending(t, s, 0)
	if err := s.EnrollProject(provenanceSequenceProject); err != nil {
		t.Fatal(err)
	}
	upserts, deletes := 0, 0
	seen := make(map[string]bool)
	for _, mutation := range provenanceSequencePending(t, s, 3) {
		if seen[mutation.EntityKey] {
			t.Fatalf("duplicate re-enrollment mutation: %+v", mutation)
		}
		seen[mutation.EntityKey] = true
		if mutation.Entity != store.SyncEntityObservation || mutation.Project != provenanceSequenceProject {
			t.Fatalf("unexpected re-enrollment mutation: %+v", mutation)
		}
		var payload store.Observation
		if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.SyncID != mutation.EntityKey || payload.Project == nil || *payload.Project != provenanceSequenceProject {
			t.Fatalf("backfill payload identity/ownership = %s", mutation.Payload)
		}
		switch mutation.Op {
		case store.SyncOpUpsert:
			upserts++
			if payload.DeletedAt != nil || (mutation.EntityKey != source.SyncID && mutation.EntityKey != result.Survivor.SyncID) {
				t.Fatalf("unexpected live backfill: %+v", mutation)
			}
		case store.SyncOpDelete:
			deletes++
			if payload.DeletedAt == nil || mutation.EntityKey == source.SyncID || mutation.EntityKey == result.Survivor.SyncID {
				t.Fatalf("unexpected deleted backfill: %+v", mutation)
			}
		default:
			t.Fatalf("unexpected mutation operation: %+v", mutation)
		}
		if mutation.EntityKey != result.Survivor.SyncID {
			assertProvenanceSequenceLink(t, payload, result.Survivor.SyncID)
		}
	}
	if upserts != 2 || deletes != 1 {
		t.Fatalf("re-enrollment upserts/deletes = %d/%d, want 2/1", upserts, deletes)
	}
	provenanceSequenceExport(t, s, 3)
}

func TestProvenanceSequenceOwnershipRescueJournalsLink(t *testing.T) {
	s := newProvenanceSequenceStore(t)
	enrollProvenanceSequenceAndAckSession(t, s)
	link := "survivor"
	imported, err := s.Import(&store.ExportData{Version: "0.1.0", Observations: []store.Observation{{
		SyncID: "legacy-source", SessionID: "sequence-session", Type: "note", Title: "Legacy source", Content: "Legacy content", Scope: "project",
		CreatedAt: "2026-01-01 00:00:00", UpdatedAt: "2026-01-01 00:00:00", SupersededBySyncID: &link,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if imported.ObservationsImported != 1 {
		t.Fatalf("import result = %+v", imported)
	}
	before := provenanceSequenceExport(t, s, 1)[0]
	if before.Project != nil || before.DeletedAt != nil {
		t.Fatalf("legacy source = %+v", before)
	}
	assertProvenanceSequenceLink(t, before, link)
	provenanceSequencePending(t, s, 0)
	result, err := s.RescueNullProjectOwnership(store.ProjectRescueParams{TargetProject: provenanceSequenceProject, ObservationIDs: []int64{before.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if result.RescuedObservations != 1 || result.RescuedSessions != 0 || result.RescuedPrompts != 0 || result.ConflictingRecords != 0 || result.SkippedRecords != 0 || len(result.Blocked) != 0 || !result.Complete || !result.Journaled {
		t.Fatalf("rescue result = %+v", result)
	}
	after := provenanceSequenceExport(t, s, 1)[0]
	if after.ID != before.ID || after.SyncID != before.SyncID || after.Project == nil || *after.Project != provenanceSequenceProject || after.DeletedAt != nil || after.Title != before.Title || after.Content != before.Content {
		t.Fatalf("rescued observation = %+v", after)
	}
	assertProvenanceSequenceLink(t, after, link)
	sessions, observations := 0, 0
	for _, mutation := range provenanceSequencePending(t, s, 2) {
		if mutation.Op != store.SyncOpUpsert || mutation.Project != provenanceSequenceProject {
			t.Fatalf("unexpected rescue mutation: %+v", mutation)
		}
		switch mutation.Entity {
		case store.SyncEntitySession:
			sessions++
			if mutation.EntityKey != "sequence-session" {
				t.Fatalf("unexpected session mutation: %+v", mutation)
			}
		case store.SyncEntityObservation:
			observations++
			var payload store.Observation
			if err := json.Unmarshal([]byte(mutation.Payload), &payload); err != nil {
				t.Fatal(err)
			}
			if mutation.EntityKey != before.SyncID || payload.SyncID != before.SyncID || payload.Project == nil || *payload.Project != provenanceSequenceProject || payload.DeletedAt != nil {
				t.Fatalf("rescued mutation payload = %s", mutation.Payload)
			}
			assertProvenanceSequenceLink(t, payload, link)
		default:
			t.Fatalf("unexpected rescue entity: %+v", mutation)
		}
	}
	if sessions != 1 || observations != 1 {
		t.Fatalf("rescue session/observation mutations = %d/%d, want 1/1", sessions, observations)
	}
}
