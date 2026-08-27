package store

import (
	"fmt"
	"math"
	"testing"
)

func TestFindCandidatesRankingThresholdsRunBeforeLimit(t *testing.T) {
	s := setupRelationsStore(t)
	const title = "anchor beacon canyon delta ember forest galaxy harbor"
	for i := 0; i < 8; i++ {
		addTestObs(t, s, fmt.Sprintf("%s strong %d", title, i), "decision", "testproject", "project")
		addTestObs(t, s, fmt.Sprintf("anchor unrelated candidate %d", i), "decision", "testproject", "project")
	}
	savedID, _ := addTestObs(t, s, title, "decision", "testproject", "project")
	rows, err := s.db.Query(`SELECT fts.rank FROM observations_fts fts CROSS JOIN observations o ON o.id=fts.rowid
		WHERE observations_fts MATCH ? AND o.id != ? AND o.deleted_at IS NULL
		AND ifnull(o.project,'')=ifnull(?,'') AND o.scope=? ORDER BY fts.rank`, sanitizeFTSCandidates(title), savedID, "testproject", "project")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ranks []float64
	for rows.Next() {
		var rank float64
		if err := rows.Scan(&rank); err != nil {
			t.Fatal(err)
		}
		ranks = append(ranks, rank)
	}
	threshold := (ranks[0] + ranks[len(ranks)-1]) / 2
	lower := 0
	for _, rank := range ranks {
		if rank <= threshold {
			lower++
		}
	}
	if lower < 6 {
		t.Fatalf("fixture needs a rejected prefix: %d ranks", lower)
	}
	for _, tc := range []struct {
		opts CandidateOptions
		keep func(float64) bool
	}{
		{CandidateOptions{Project: "testproject", Scope: "project", Limit: 2, BM25MaxRank: &threshold, SkipInsert: true}, func(rank float64) bool { return rank <= threshold }},
		{CandidateOptions{Project: "testproject", Scope: "project", Limit: 2, BM25Floor: &threshold, SkipInsert: true}, func(rank float64) bool { return rank >= threshold }},
	} {
		got, err := s.FindCandidates(savedID, tc.opts)
		if err != nil || len(got) != 2 || !tc.keep(got[0].Score) || !tc.keep(got[1].Score) {
			t.Fatalf("rank predicate must run before limit: candidates=%+v err=%v", got, err)
		}
	}
}

func TestCandidateRankQueryRejectsInvalidOptions(t *testing.T) {
	zero := 0.0
	for _, rank := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, opts := range []CandidateOptions{{BM25MaxRank: &rank}, {BM25Floor: &rank}} {
			if _, _, err := candidateRankQuery(opts); err == nil {
				t.Fatalf("expected invalid rank %v to fail", rank)
			}
		}
	}
	if _, _, err := candidateRankQuery(CandidateOptions{BM25MaxRank: &zero, BM25Floor: &zero}); err == nil {
		t.Fatal("expected max rank and legacy floor together to fail")
	}
}
