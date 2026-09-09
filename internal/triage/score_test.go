package triage

import "testing"

func TestRankCandidates(t *testing.T) {
	target := Issue{Number: 10, Title: "App crashes when saving large notes", Body: "It crashes every time."}

	tests := []struct {
		name       string
		target     Issue
		found      []Issue
		wantNums   []int
		wantSim    map[int]float64
		wantShared map[int][]string
	}{
		{
			name:   "reworded same bug accepted: high similarity plus shared distinctive terms",
			target: target,
			found: []Issue{
				{Number: 11, Title: "App crashes when saving a large note", State: "open", Body: "Same here."},
			},
			wantNums:   []int{11},
			wantSim:    map[int]float64{11: 0.625},
			wantShared: map[int][]string{11: {"crashes", "saving", "large"}},
		},
		{
			name:   "false positive guard: similar words, different bug rejected",
			target: Issue{Number: 10, Title: "Search is slow with large projects", Body: ""},
			found: []Issue{
				{Number: 20, Title: "Sync is slow with large repositories", State: "open", Body: ""},
			},
			wantNums: nil,
		},
		{
			name:   "false negative guard: reworded same bug with shared snippet accepted below title threshold",
			target: Issue{Number: 10, Title: "engram serve fails to start with bind error after upgrade", Body: "fails: `bind: address already in use`"},
			found: []Issue{
				{Number: 12, Title: "engram serve fails to start after reboot, port bound", State: "closed", Body: "see `bind: address already in use`"},
			},
			wantNums:   []int{12},
			wantSim:    map[int]float64{12: 6.0 / 13.0},
			wantShared: map[int][]string{12: {"engram", "serve", "fails", "start"}},
		},
		{
			name:   "same reworded pair without shared snippet stays rejected",
			target: Issue{Number: 10, Title: "engram serve fails to start with bind error after upgrade", Body: "fails: `bind: address already in use`"},
			found: []Issue{
				{Number: 12, Title: "engram serve fails to start after reboot, port bound", State: "closed", Body: "different error: `connection refused`"},
			},
			wantNums: nil,
		},
		{
			name:   "open and closed eligible, self and pull requests excluded",
			target: target,
			found: []Issue{
				{Number: 10, Title: "App crashes when saving large notes", State: "open"},                      // self
				{Number: 12, Title: "App crashes when saving large notes", State: "closed", PullRequest: true}, // PR
				{Number: 11, Title: "App crashes when saving a large note", State: "open"},
				{Number: 13, Title: "App crashes when saving large notes again", State: "closed"},
			},
			wantNums: []int{13, 11},
		},
		{
			name:   "top three by score, deterministic tie-break by number",
			target: target,
			found: []Issue{
				{Number: 22, Title: "App crashes when saving large note", State: "open"},    // score 0.775
				{Number: 24, Title: "App crashes when saving large notes", State: "open"},   // ties #21 at 1.20
				{Number: 21, Title: "App crashes when saving large notes", State: "closed"}, // score 1.20
				{Number: 23, Title: "App crashes when saving larger notes", State: "open"},  // ties #22 at 0.775
			},
			wantNums: []int{21, 24, 22},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RankCandidates(tt.target, tt.found)
			nums := make([]int, 0, len(got))
			for _, m := range got {
				nums = append(nums, m.Issue.Number)
			}
			if len(nums) != len(tt.wantNums) {
				t.Fatalf("RankCandidates numbers = %v, want %v", nums, tt.wantNums)
			}
			for i, n := range tt.wantNums {
				if nums[i] != n {
					t.Fatalf("RankCandidates numbers = %v, want %v", nums, tt.wantNums)
				}
			}
			for _, m := range got {
				if want, ok := tt.wantSim[m.Issue.Number]; ok && m.TitleSimilarity != want {
					t.Errorf("candidate #%d similarity = %v, want %v", m.Issue.Number, m.TitleSimilarity, want)
				}
				if want, ok := tt.wantShared[m.Issue.Number]; ok {
					if len(m.SharedTerms) != len(want) {
						t.Errorf("candidate #%d shared terms = %v, want %v", m.Issue.Number, m.SharedTerms, want)
						continue
					}
					for i := range want {
						if m.SharedTerms[i] != want[i] {
							t.Errorf("candidate #%d shared terms = %v, want %v", m.Issue.Number, m.SharedTerms, want)
							break
						}
					}
				}
			}
		})
	}
}

func TestRankCandidatesSnippetEvidence(t *testing.T) {
	target := Issue{Number: 10, Title: "engram serve fails to start with bind error after upgrade", Body: "fails: `bind: address already in use`"}
	found := []Issue{
		{Number: 12, Title: "engram serve fails to start after reboot, port bound", State: "closed", Body: "see `bind: address already in use`"},
	}
	got := RankCandidates(target, found)
	if len(got) != 1 {
		t.Fatalf("expected one match, got %v", got)
	}
	if len(got[0].MatchingSnippets) != 1 || got[0].MatchingSnippets[0] != "bind: address already in use" {
		t.Errorf("MatchingSnippets = %v, want [bind: address already in use]", got[0].MatchingSnippets)
	}
}

func TestRankCandidatesMaxThree(t *testing.T) {
	target := Issue{Number: 10, Title: "App crashes when saving large notes"}
	found := make([]Issue, 0, 10)
	for i := 30; i < 40; i++ {
		found = append(found, Issue{Number: i, Title: "App crashes when saving large notes"})
	}
	if got := RankCandidates(target, found); len(got) != maxReportedCandidates {
		t.Fatalf("expected at most %d candidates, got %d", maxReportedCandidates, len(got))
	}
}
