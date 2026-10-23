package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestActionableRelations(t *testing.T) {
	s := setupRelationsStore(t)
	_, a := addTestObs(t, s, "source", "decision", "alpha", "project")
	_, b := addTestObs(t, s, "target", "decision", "beta", "project")
	r, err := s.SaveRelation(SaveRelationParams{SyncID: newSyncID("rel"), SourceID: a, TargetID: b})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.ListPendingProjectMutations("")
	if err != nil {
		t.Fatal(err)
	}
	for _, verdict := range []string{RelationRelated, RelationCompatible, RelationScoped, RelationConflictsWith, RelationSupersedes, RelationNotConflict} {
		_, err := s.JudgeRelation(JudgeRelationParams{JudgmentID: r.SyncID, Relation: verdict})
		if !errors.Is(err, ErrCrossProjectRelation) {
			t.Fatalf("%s: %v", verdict, err)
		}
	}
	// The default evidence API remains inclusive.
	all, err := s.ListRelations(ListRelationsOptions{})
	if err != nil || len(all) != 1 {
		t.Fatalf("inclusive: %v %v", all, err)
	}
	opts := ListRelationsOptions{ActionableOnly: true}
	items, err := s.ListRelations(opts)
	if err != nil || len(items) != 0 {
		t.Fatalf("actionable: %v %v", items, err)
	}
	count, err := s.CountRelations(opts)
	if err != nil || count != 0 {
		t.Fatalf("count: %d %v", count, err)
	}
	stats, err := s.GetActionableRelationStats("")
	if err != nil || stats.ByJudgmentStatus["pending"] != 0 {
		t.Fatalf("stats: %v %v", stats, err)
	}
	after, err := s.GetRelation(r.SyncID)
	if err != nil || !reflect.DeepEqual(r, after) {
		t.Fatalf("relation changed: %v %v", after, err)
	}
	journal, err := s.ListPendingProjectMutations("")
	if err != nil || !reflect.DeepEqual(before, journal) {
		t.Fatalf("journal changed: %v", err)
	}
}

func TestActionableOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, source, target, status string
		missing                      bool
		want                         int
	}{
		{"same", "alpha", "alpha", "pending", false, 1},
		{"fallback differs", "", "alpha", "pending", false, 0},
		{"fallback same", "", "testproject", "pending", false, 1},
		{"judged cross project", "alpha", "beta", "judged", false, 1},
		{"missing endpoint", "alpha", "beta", "pending", true, 1},
		{"unknown ownership", "unknown", "beta", "pending", false, 1},
		{"both unknown", "unknown", "", "pending", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := setupRelationsStore(t)
			_, a := addTestObs(t, s, "source", "decision", "alpha", "project")
			_, b := addTestObs(t, s, "target", "decision", "beta", "project")
			if _, err := s.db.Exec("UPDATE observations SET project = ? WHERE sync_id = ?", tc.source, a); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("UPDATE observations SET project = ? WHERE sync_id = ?", tc.target, b); err != nil {
				t.Fatal(err)
			}
			if tc.source == "unknown" {
				if _, err := s.db.Exec("UPDATE sessions SET project = '' WHERE id = 'ses-rel-test'"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec("UPDATE observations SET project = '' WHERE sync_id = ?", a); err != nil {
					t.Fatal(err)
				}
			}
			if tc.missing {
				b = "missing-observation"
			}
			r, err := s.SaveRelation(SaveRelationParams{SyncID: newSyncID("rel"), SourceID: a, TargetID: b})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("UPDATE memory_relations SET judgment_status = ? WHERE sync_id = ?", tc.status, r.SyncID); err != nil {
				t.Fatal(err)
			}
			opts := ListRelationsOptions{ActionableOnly: true}
			items, err := s.ListRelations(opts)
			if err != nil || len(items) != tc.want {
				t.Fatalf("list: %v %v", items, err)
			}
			count, err := s.CountRelations(opts)
			if err != nil || count != tc.want {
				t.Fatalf("count: %d %v", count, err)
			}
			inclusive, err := s.GetRelationStats("")
			if err != nil || inclusive.ByJudgmentStatus[tc.status] != 1 {
				t.Fatalf("inclusive stats: %v %v", inclusive, err)
			}
			stats, err := s.GetActionableRelationStats("")
			if err != nil || stats.ByJudgmentStatus[tc.status] != tc.want {
				t.Fatalf("stats: %v %v", stats, err)
			}
		})
	}
}
