package store

import (
	"fmt"
	"strings"
	"testing"
)

func TestFindCandidatesSaveRelevanceBeforeLimitAndInsert(t *testing.T) {
	s := setupRelationsStore(t)
	const sourceTitle = "Browser keyboard shortcuts"
	for i := 0; i < 4; i++ {
		_, err := s.AddObservation(AddObservationParams{
			SessionID: "ses-rel-test", Type: "decision", Project: "testproject", Scope: "project",
			Title:   fmt.Sprintf("Unrelated backup retention %d", i),
			Content: fmt.Sprintf("Backup %d: ", i) + strings.Repeat(sourceTitle+" ", 30),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	eligibleID, _ := addTestObs(t, s, "Browser keyboard controls", "decision", "testproject", "project")
	sourceID, _ := addTestObs(t, s, sourceTitle, "decision", "testproject", "project")
	broad, err := s.FindCandidates(sourceID, CandidateOptions{Limit: 1, SkipInsert: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(broad) != 1 || broad[0].ID == eligibleID {
		t.Fatalf("fixture must put an irrelevant content match first: %+v", broad)
	}
	var timings CandidateTimings
	got, err := s.FindCandidates(sourceID, CandidateOptions{Limit: 1, RequireSaveRelevance: true, Timings: &timings})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != eligibleID || got[0].JudgmentID == "" {
		t.Fatalf("eligible candidate must survive filtering before limit: %+v", got)
	}
	if timings.RowsRead != 1 {
		t.Errorf("rows consumed = %d, want only one eligible row", timings.RowsRead)
	}
	count, err := s.CountRelations(ListRelationsOptions{Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("pending relations = %d, want only eligible candidate", count)
	}
	// The opt-in must not silently narrow other callers' broad recall.
	after, err := s.FindCandidates(sourceID, CandidateOptions{Limit: 1, SkipInsert: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != broad[0].ID || after[0].Score != broad[0].Score {
		t.Fatalf("default retrieval changed: before=%+v after=%+v", broad, after)
	}
}

func TestFindCandidatesSaveRelevanceNoEligibleRowsConsumed(t *testing.T) {
	s := setupRelationsStore(t)
	for i := 0; i < 32; i++ {
		_, err := s.AddObservation(AddObservationParams{
			SessionID: "ses-rel-test", Type: "decision", Project: "testproject", Scope: "project",
			Title:   fmt.Sprintf("Database retention policy %d", i),
			Content: fmt.Sprintf("Policy %d: browser keyboard shortcuts", i),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sourceID, _ := addTestObs(t, s, "Browser keyboard shortcuts", "decision", "testproject", "project")
	var timings CandidateTimings
	got, err := s.FindCandidates(sourceID, CandidateOptions{Limit: 1, RequireSaveRelevance: true, Timings: &timings})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || timings.RowsRead != 0 {
		t.Fatalf("irrelevant rows must be excluded in SQL: candidates=%+v rows=%d", got, timings.RowsRead)
	}
	count, err := s.CountRelations(ListRelationsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unexpected pending relations: %d", count)
	}
}

func TestFindCandidatesSaveRelevanceTopicSignal(t *testing.T) {
	for _, tc := range []struct {
		name, candidateTitle, sourceTopic, targetTopic string
		want                                           bool
	}{
		{"same topic content retrieval", "Database retention policy", "policy/keyboard", "policy/keyboard", true},
		{"different topics real title overlap", "Browser keyboard controls", "policy/keyboard", "policy/controls", true},
		{"different topics incidental content", "Database retention policy", "policy/keyboard", "policy/database", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := setupRelationsStore(t)
			candidateID, err := s.AddObservation(AddObservationParams{
				SessionID: "ses-rel-test", Type: "decision", Project: "testproject", Scope: "project",
				Title: tc.candidateTitle, Content: "Browser keyboard shortcuts policy",
			})
			if err != nil {
				t.Fatal(err)
			}
			sourceID, _ := addTestObs(t, s, "Browser keyboard shortcuts", "decision", "testproject", "project")
			// Updates can associate pre-existing records with the same topic; normal
			// AddObservation saves with a topic key instead revise one record.
			for id, topic := range map[int64]string{sourceID: tc.sourceTopic, candidateID: tc.targetTopic} {
				if _, err := s.UpdateObservationForProject(id, "testproject", UpdateObservationParams{TopicKey: &topic}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.FindCandidates(sourceID, CandidateOptions{RequireSaveRelevance: true})
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			if tc.want {
				wantCount = 1
			}
			if len(got) != wantCount {
				t.Fatalf("candidates = %+v, want %d", got, wantCount)
			}
			count, err := s.CountRelations(ListRelationsOptions{Status: "pending"})
			if err != nil {
				t.Fatal(err)
			}
			if count != wantCount {
				t.Fatalf("pending relations = %d, want %d", count, wantCount)
			}
		})
	}
}

func TestFindCandidatesSaveRelevanceKeepsBM25Order(t *testing.T) {
	for _, title := range []string{
		"Browser keyboard shortcuts",
		"Use browser keyboard browser shortcuts",
		"Browser keyboard-shortcuts",
	} {
		t.Run(title, func(t *testing.T) {
			s := setupRelationsStore(t)
			addTestObs(t, s, "Browser keyboard shortcuts settings", "decision", "testproject", "project")
			addTestObs(t, s, "Browser keyboard controls", "decision", "testproject", "project")
			sourceID, _ := addTestObs(t, s, title, "decision", "testproject", "project")
			opts := CandidateOptions{Limit: 2, SkipInsert: true}
			broad, err := s.FindCandidates(sourceID, opts)
			if err != nil {
				t.Fatal(err)
			}
			opts.RequireSaveRelevance = true
			gated, err := s.FindCandidates(sourceID, opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(broad) != 2 || len(gated) != 2 {
				t.Fatalf("expected both relevant candidates: broad=%+v gated=%+v", broad, gated)
			}
			for i := range broad {
				if broad[i].ID != gated[i].ID || broad[i].Score != gated[i].Score {
					t.Fatalf("BM25 ordering changed: broad=%+v gated=%+v", broad, gated)
				}
			}
			count, err := s.CountRelations(ListRelationsOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("SkipInsert created %d relations", count)
			}
		})
	}
}
