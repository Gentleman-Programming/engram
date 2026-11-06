package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestReviewProjectGuardRejectsEmptyWithoutMutation(t *testing.T) {
	for _, project := range []string{"", " \t "} {
		t.Run(project, func(t *testing.T) {
			cfg := mustDefaultConfig(t)
			cfg.DataDir = t.TempDir()
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if err := s.CreateSession("review", "alpha", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.EnrollProject("alpha"); err != nil {
				t.Fatal(err)
			}
			id, err := s.AddObservation(AddObservationParams{SessionID: "review", Project: "alpha", Type: "decision", Title: "guard", Content: "must remain unchanged"})
			if err != nil {
				t.Fatal(err)
			}
			snapshot := func() []any {
				t.Helper()
				data, err := s.Export()
				if err != nil {
					t.Fatal(err)
				}
				data.ExportedAt = ""
				pending, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 1000)
				if err != nil {
					t.Fatal(err)
				}
				stats, err := s.Stats()
				if err != nil {
					t.Fatal(err)
				}
				seq, err := s.MaxPendingSyncMutationSeq("")
				if err != nil {
					t.Fatal(err)
				}
				return []any{data, pending, stats, seq}
			}
			before := snapshot()
			if err := s.MarkReviewedForProject(id, project); !errors.Is(err, ErrProjectRequired) {
				t.Fatalf("empty project error = %v, want %v", err, ErrProjectRequired)
			}
			if after := snapshot(); !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal mutated state: before=%+v after=%+v", before, after)
			}
			// Existing sync/local callers deliberately retain the unscoped API.
			if err := s.MarkReviewed(id); err != nil {
				t.Fatalf("legacy unscoped control: %v", err)
			}
			if err := s.MarkReviewedForProject(id, " ALPHA "); err != nil {
				t.Fatalf("canonical scoped control: %v", err)
			}
		})
	}
}
