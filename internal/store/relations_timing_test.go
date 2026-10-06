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
	if timings.Lookup <= 0 || timings.Inserts != 0 {
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
	if timings.Lookup <= 0 || timings.Inserts <= 0 {
		t.Fatalf("insert timings: %v", timings)
	}
	_, err = s.FindCandidates(-1, opts)
	if err == nil {
		t.Fatal("missing observation must fail")
	}
	if timings.Lookup <= 0 || timings.Inserts != 0 {
		t.Fatalf("error must reset timings: %v", timings)
	}
}
