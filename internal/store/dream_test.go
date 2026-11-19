package store

import (
	"errors"
	"reflect"
	"testing"
)

type dreamSeed struct {
	project, scope, title, content, topicKey string
}

// dreamAdd inserts one observation per seed and returns their IDs and sync IDs.
func dreamAdd(t *testing.T, s *Store, seeds ...dreamSeed) ([]int64, []string) {
	t.Helper()
	var ids []int64
	var syncIDs []string
	for _, sd := range seeds {
		session := "ses-dream-" + sd.project
		if err := s.CreateSession(session, sd.project, "/tmp/dream"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		content := sd.content
		if content == "" {
			content = "Body of " + sd.title
		}
		id, err := s.AddObservation(AddObservationParams{
			SessionID: session, Type: "decision", Title: sd.title, Content: content,
			Project: sd.project, Scope: sd.scope, TopicKey: sd.topicKey,
		})
		if err != nil {
			t.Fatalf("AddObservation(%q): %v", sd.title, err)
		}
		obs, err := s.GetObservation(id)
		if err != nil {
			t.Fatalf("GetObservation: %v", err)
		}
		ids = append(ids, id)
		syncIDs = append(syncIDs, obs.SyncID)
	}
	return ids, syncIDs
}

// dreamRelate links two observations with a pending candidate relation and
// returns the relation's sync ID.
func dreamRelate(t *testing.T, s *Store, source, target string) string {
	t.Helper()
	syncID := "rel-" + source + "-" + target
	if _, err := s.SaveRelation(SaveRelationParams{SyncID: syncID, SourceID: source, TargetID: target}); err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}
	return syncID
}

func dreamIDs(groups []DreamGroup) [][]int64 {
	out := make([][]int64, len(groups))
	for i, g := range groups {
		out[i] = g.IDs
	}
	return out
}

func TestDreamGroups_ProjectRequired(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DreamGroups("  "); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("DreamGroups: err = %v, want ErrProjectRequired", err)
	}
	if _, err := s.DreamFocusGroups("", "x"); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("DreamFocusGroups: err = %v, want ErrProjectRequired", err)
	}
}

func TestDreamGroups_TransitiveComponentsOrderedBySmallestID(t *testing.T) {
	s := newTestStore(t)
	ids, sync := dreamAdd(t, s,
		dreamSeed{project: "dp", scope: "project", title: "alpha one"},
		dreamSeed{project: "dp", scope: "project", title: "beta one"},
		dreamSeed{project: "dp", scope: "project", title: "alpha two"},
		dreamSeed{project: "dp", scope: "project", title: "beta two"},
		dreamSeed{project: "dp", scope: "project", title: "alpha three"},
		dreamSeed{project: "dp", scope: "project", title: "lonely"},
	)
	// alpha chain declared out of order and in both directions: 4-2, 0-2.
	dreamRelate(t, s, sync[4], sync[2])
	dreamRelate(t, s, sync[0], sync[2])
	dreamRelate(t, s, sync[3], sync[1])

	groups, err := s.DreamGroups("DP") // project match is case-insensitive
	if err != nil {
		t.Fatalf("DreamGroups: %v", err)
	}
	want := [][]int64{{ids[0], ids[2], ids[4]}, {ids[1], ids[3]}}
	if got := dreamIDs(groups); !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	for _, g := range groups {
		if g.Scope != "project" {
			t.Fatalf("scope = %q, want project", g.Scope)
		}
	}
}

func TestDreamGroups_NeverCrossesProjectOrScope(t *testing.T) {
	s := newTestStore(t)
	ids, sync := dreamAdd(t, s,
		dreamSeed{project: "dp", scope: "project", title: "a project"},
		dreamSeed{project: "dp", scope: "personal", title: "a personal"},
		dreamSeed{project: "other", scope: "project", title: "a other"},
		dreamSeed{project: "dp", scope: "project", title: "b project"},
	)
	dreamRelate(t, s, sync[0], sync[1]) // same project, different scope
	dreamRelate(t, s, sync[0], sync[2]) // different project
	dreamRelate(t, s, sync[0], sync[3]) // valid

	groups, err := s.DreamGroups("dp")
	if err != nil {
		t.Fatalf("DreamGroups: %v", err)
	}
	if got, want := dreamIDs(groups), [][]int64{{ids[0], ids[3]}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
}

func TestDreamGroups_SkipsPinnedDeletedAndNonLinkingRelations(t *testing.T) {
	s := newTestStore(t)
	ids, sync := dreamAdd(t, s,
		dreamSeed{project: "dp", scope: "project", title: "keep one"},
		dreamSeed{project: "dp", scope: "project", title: "keep two"},
		dreamSeed{project: "dp", scope: "project", title: "pinned"},
		dreamSeed{project: "dp", scope: "project", title: "deleted"},
		dreamSeed{project: "dp", scope: "project", title: "not a conflict"},
	)
	dreamRelate(t, s, sync[0], sync[1])
	dreamRelate(t, s, sync[0], sync[2])
	dreamRelate(t, s, sync[1], sync[3])
	judged := dreamRelate(t, s, sync[0], sync[4])

	if err := s.PinObservation(ids[2]); err != nil {
		t.Fatalf("PinObservation: %v", err)
	}
	if err := s.DeleteObservation(ids[3], false); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}
	if _, err := s.JudgeRelation(JudgeRelationParams{JudgmentID: judged, Relation: "not_conflict", MarkedByActor: "user"}); err != nil {
		t.Fatalf("JudgeRelation: %v", err)
	}

	groups, err := s.DreamGroups("dp")
	if err != nil {
		t.Fatalf("DreamGroups: %v", err)
	}
	if got, want := dreamIDs(groups), [][]int64{{ids[0], ids[1]}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}

	// A judged linking verdict keeps joining.
	if _, err := s.JudgeRelation(JudgeRelationParams{JudgmentID: judged, Relation: "supersedes", MarkedByActor: "user"}); err != nil {
		t.Fatalf("JudgeRelation: %v", err)
	}
	groups, err = s.DreamGroups("dp")
	if err != nil {
		t.Fatalf("DreamGroups: %v", err)
	}
	if got, want := dreamIDs(groups), [][]int64{{ids[0], ids[1], ids[4]}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups after supersedes = %v, want %v", got, want)
	}
}

func TestDreamFocusGroups_MatchesTitleContentAndTopicKeyWithoutRelations(t *testing.T) {
	s := newTestStore(t)
	ids, _ := dreamAdd(t, s,
		dreamSeed{project: "dp", scope: "project", title: "Fireplace hood v1"},
		dreamSeed{project: "dp", scope: "project", title: "second note", content: "the FIREPLACE table height"},
		dreamSeed{project: "dp", scope: "project", title: "third note", topicKey: "design/fireplace-budget"},
		dreamSeed{project: "dp", scope: "project", title: "unrelated"},
		dreamSeed{project: "other", scope: "project", title: "fireplace elsewhere"},
		dreamSeed{project: "other", scope: "project", title: "fireplace elsewhere two"},
	)

	groups, err := s.DreamFocusGroups("dp", "firePLACE")
	if err != nil {
		t.Fatalf("DreamFocusGroups: %v", err)
	}
	if got, want := dreamIDs(groups), [][]int64{{ids[0], ids[1], ids[2]}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
}

func TestDreamFocusGroups_SplitsByScopeAndDropsSingletons(t *testing.T) {
	s := newTestStore(t)
	ids, _ := dreamAdd(t, s,
		dreamSeed{project: "dp", scope: "project", title: "focus p1"},
		dreamSeed{project: "dp", scope: "personal", title: "focus solo"},
		dreamSeed{project: "dp", scope: "project", title: "focus p2"},
		dreamSeed{project: "dp", scope: "project", title: "focus pinned"},
		dreamSeed{project: "dp", scope: "project", title: "focus deleted"},
	)
	if err := s.PinObservation(ids[3]); err != nil {
		t.Fatalf("PinObservation: %v", err)
	}
	if err := s.DeleteObservation(ids[4], false); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}

	groups, err := s.DreamFocusGroups("dp", "focus")
	if err != nil {
		t.Fatalf("DreamFocusGroups: %v", err)
	}
	if got, want := dreamIDs(groups), [][]int64{{ids[0], ids[2]}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	if groups[0].Scope != "project" {
		t.Fatalf("scope = %q, want project", groups[0].Scope)
	}
}

func TestDreamFocusGroups_WildcardsAndQuotesMatchLiterally(t *testing.T) {
	s := newTestStore(t)
	ids, _ := dreamAdd(t, s,
		dreamSeed{project: "dp", scope: "project", title: "rate 50% off"},
		dreamSeed{project: "dp", scope: "project", title: "again 50% off"},
		dreamSeed{project: "dp", scope: "project", title: "rate 50 percent off"},
		dreamSeed{project: "dp", scope: "project", title: "snake_case name"},
		dreamSeed{project: "dp", scope: "project", title: "more snake_case"},
		dreamSeed{project: "dp", scope: "project", title: "snakeXcase name"},
		dreamSeed{project: "dp", scope: "project", title: `it's a "quoted" one`},
		dreamSeed{project: "dp", scope: "project", title: `it's a "quoted" two`},
		dreamSeed{project: "dp", scope: "project", title: `back\slash a`},
		dreamSeed{project: "dp", scope: "project", title: `back\slash b`},
	)
	cases := []struct {
		focus string
		want  []int64
	}{
		{"50% off", []int64{ids[0], ids[1]}},
		{"snake_case", []int64{ids[3], ids[4]}},
		{`it's a "quoted"`, []int64{ids[6], ids[7]}},
		{`back\slash`, []int64{ids[8], ids[9]}},
		{`x' OR '1'='1`, nil},
	}
	for _, tc := range cases {
		groups, err := s.DreamFocusGroups("dp", tc.focus)
		if err != nil {
			t.Fatalf("DreamFocusGroups(%q): %v", tc.focus, err)
		}
		var got []int64
		if len(groups) > 0 {
			got = groups[0].IDs
		}
		if len(groups) > 1 || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("focus %q: groups = %v, want [%v]", tc.focus, dreamIDs(groups), tc.want)
		}
	}
}
