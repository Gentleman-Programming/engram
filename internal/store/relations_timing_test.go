package store

import (
	"reflect"
	"testing"
	"time"
)

func TestFindCandidatesTimingsPreserveLookup(t *testing.T) {
	s := setupRelationsStore(t)
	addTestObs(t, s, "Authentication session token storage", "decision", "testproject", "project")
	id, _ := addTestObs(t, s, "Authentication session token management", "decision", "testproject", "project")
	opts := CandidateOptions{SkipInsert: true}
	baseline, err := s.FindCandidates(id, opts)
	if err != nil || len(baseline) == 0 {
		t.Fatalf("baseline: %v, %v", baseline, err)
	}
	timings := CandidateTimings{Lookup: -time.Second, Inserts: -time.Second}
	opts.Timings = &timings
	got, err := s.FindCandidates(id, opts)
	if err != nil || !reflect.DeepEqual(got, baseline) {
		t.Fatalf("timing changed candidates: %v, %v", got, err)
	}
	if timings.Lookup < 0 || timings.Inserts != 0 {
		t.Fatalf("lookup timings: %v", timings)
	}
	count, err := s.CountRelations(ListRelationsOptions{})
	if err != nil || count != 0 {
		t.Fatalf("SkipInsert changed: count=%d err=%v", count, err)
	}
	opts.SkipInsert = false
	got, err = s.FindCandidates(id, opts)
	if err != nil || len(got) != len(baseline) || got[0].JudgmentID == "" {
		t.Fatalf("insert: %v, %v", got, err)
	}
	if timings.Lookup < 0 || timings.Inserts < 0 {
		t.Fatalf("insert timings: %v", timings)
	}
	// A sufficiently fast stage can measure zero; stale negative sentinels
	// still prove that the next call resets both duration fields.
	timings = CandidateTimings{Lookup: -time.Second, Inserts: -time.Second}
	_, err = s.FindCandidates(-1, opts)
	if err == nil {
		t.Fatal("missing observation must fail")
	}
	if timings.Lookup < 0 || timings.Inserts != 0 {
		t.Fatalf("error must reset timings: %v", timings)
	}
}

// TestFindCandidatesTimingsPreserveInsertion compares complete candidate and
// pending-relation semantics on equivalent independent stores. Generated sync
// IDs and timestamps are validated locally, then normalized for comparison.
func TestFindCandidatesTimingsPreserveInsertion(t *testing.T) {
	run := func(timed bool) ([]Candidate, []Relation) {
		t.Helper()
		s := setupRelationsStore(t)
		addTestObs(t, s, "Authentication session token storage", "decision", "testproject", "project")
		id, sourceSyncID := addTestObs(t, s, "Authentication session token management", "decision", "testproject", "project")
		opts := CandidateOptions{}
		var timings CandidateTimings
		if timed {
			opts.Timings = &timings
		}
		candidates, err := s.FindCandidates(id, opts)
		if err != nil || len(candidates) != 1 {
			t.Fatalf("timed=%v candidates=%v err=%v", timed, candidates, err)
		}
		if timed && (timings.Lookup < 0 || timings.Inserts < 0) {
			t.Fatalf("negative timings: %v", timings)
		}
		count, err := s.CountRelations(ListRelationsOptions{})
		if err != nil || count != len(candidates) {
			t.Fatalf("timed=%v relations=%d err=%v", timed, count, err)
		}
		relations := make([]Relation, 0, len(candidates))
		for i := range candidates {
			candidate := &candidates[i]
			observation, err := s.GetObservation(candidate.ID)
			if err != nil || candidate.SyncID == "" || candidate.SyncID != observation.SyncID || candidate.Title != observation.Title || candidate.Type != observation.Type {
				t.Fatalf("candidate does not identify stored observation: %v err=%v", candidate, err)
			}
			if candidate.JudgmentID == "" {
				t.Fatal("missing pending relation ID")
			}
			relation, err := s.GetRelation(candidate.JudgmentID)
			if err != nil || relation.SyncID != candidate.JudgmentID || relation.SourceID != sourceSyncID || relation.TargetID != candidate.SyncID || relation.Relation != RelationPending || relation.JudgmentStatus != JudgmentStatusPending || relation.CreatedAt == "" || relation.UpdatedAt == "" {
				t.Fatalf("pending relation effects changed: %v err=%v", relation, err)
			}
			candidate.SyncID = ""
			candidate.JudgmentID = ""
			relation.SyncID = ""
			relation.SourceID = "source"
			relation.TargetID = candidate.Title
			relation.CreatedAt = ""
			relation.UpdatedAt = ""
			relations = append(relations, *relation)
		}
		return candidates, relations
	}
	baselineCandidates, baselineRelations := run(false)
	timedCandidates, timedRelations := run(true)
	if !reflect.DeepEqual(timedCandidates, baselineCandidates) {
		t.Fatalf("candidate parity: timed=%v untimed=%v", timedCandidates, baselineCandidates)
	}
	if !reflect.DeepEqual(timedRelations, baselineRelations) {
		t.Fatalf("pending relation parity: timed=%v untimed=%v", timedRelations, baselineRelations)
	}
}
