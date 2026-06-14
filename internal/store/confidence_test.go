package store

import "testing"

func obsWith(dup int, pinned bool, reviewAfter *string) Observation {
	return Observation{
		DuplicateCount: dup,
		Pinned:         pinned,
		ReviewAfter:    reviewAfter,
	}
}

func TestComputeConfidence(t *testing.T) {
	past := "2000-01-01T00:00:00Z"   // review_after in the past → needs_review (stale)
	future := "2999-01-01T00:00:00Z" // review_after in the future → active

	cases := []struct {
		name string
		obs  Observation
		want float64
	}{
		{"one-off active", obsWith(1, false, nil), 0.5},
		{"confirmed x3", obsWith(3, false, &future), 0.5 + 2*confidenceDupStep},
		{"confirmation capped", obsWith(50, false, &future), 0.5 + confidenceDupMaxSteps*confidenceDupStep},
		{"pinned", obsWith(1, true, nil), 0.5 + confidencePinnedBoost},
		{"stale penalized", obsWith(1, false, &past), 0.5 - confidenceStalenessPenalty},
		{"clamped to 1", obsWith(50, true, &future), 1.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeConfidence(tc.obs)
			if got < 0 || got > 1 {
				t.Fatalf("confidence out of [0,1]: %v", got)
			}
			if !almostEqual(got, tc.want) {
				t.Errorf("computeConfidence = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfidenceLabel(t *testing.T) {
	cases := map[float64]string{
		0.95: "confirmed",
		0.80: "confirmed",
		0.70: "likely",
		0.60: "likely",
		0.50: "tentative",
		0.40: "tentative",
		0.30: "unverified",
		0.00: "unverified",
	}
	for score, want := range cases {
		if got := confidenceLabel(score); got != want {
			t.Errorf("confidenceLabel(%v) = %q, want %q", score, got, want)
		}
	}
}

// Within a graded candidate pool, a confirmed result whose lexical relevance is
// only marginally below the top should overtake the one-off top hit once
// confidence is blended in. (With a wide relevance gap, relevance still wins —
// that is by design; see the weights.)
func TestRerankByConfidenceLiftsConfirmed(t *testing.T) {
	future := "2999-01-01T00:00:00Z"
	mk := func(dup int, rank float64) SearchResult {
		o := obsWith(dup, false, &future)
		return SearchResult{Observation: o, Rank: rank, Confidence: computeConfidence(o)}
	}
	results := []SearchResult{
		mk(1, -5.0), // one-off, top lexical relevance
		mk(6, -4.8), // confirmed x6, marginally less relevant
		mk(1, -2.0), // filler
		mk(1, -1.0), // filler (defines the spread)
	}
	rerankByConfidence(results)
	if results[0].DuplicateCount != 6 {
		t.Errorf("expected confirmed memory first, got DuplicateCount=%d", results[0].DuplicateCount)
	}
}

// Topic-key hits (topicRankBoost) must stay pinned at the front, untouched.
func TestRerankPreservesTopicHits(t *testing.T) {
	future := "2999-01-01T00:00:00Z"
	results := []SearchResult{
		{Observation: Observation{ID: 100, DuplicateCount: 1, ReviewAfter: &future}, Rank: topicRankBoost},
		{Observation: obsWith(9, true, &future), Rank: -1.0, Confidence: 1.0},
		{Observation: obsWith(1, false, &future), Rank: -0.1, Confidence: 0.5},
	}
	rerankByConfidence(results)
	if results[0].ID != 100 || results[0].Rank != topicRankBoost {
		t.Errorf("topic hit should remain first, got ID=%d rank=%v", results[0].ID, results[0].Rank)
	}
}

func almostEqual(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}
