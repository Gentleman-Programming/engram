package store

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Compare the public API against the frozen original SQL, including every
// returned field and exact raw BM25 scores. The reference is not production SQL.
func TestFindCandidatesOriginalQueryParity(t *testing.T) {
	s := setupRelationsStore(t)
	title := "anchor beacon canyon delta ember forest galaxy harbor"
	sourceID, sourceSync := addTestObs(t, s, title, "decision", "testproject", "project")
	var targets []string
	for i := 0; i < 12; i++ {
		_, syncID := addTestObs(t, s, fmt.Sprintf("%s variant %d", strings.Join(strings.Fields(title)[:1+i%8], " "), i), "decision", "testproject", "project")
		targets = append(targets, syncID)
	}
	addTestObs(t, s, title+" other project", "decision", "otherproject", "project")
	addTestObs(t, s, title+" other scope", "decision", "testproject", "personal")
	deleted, _ := addTestObs(t, s, title+" deleted", "decision", "testproject", "project")
	if err := s.DeleteObservation(deleted, false); err != nil {
		t.Fatal(err)
	}
	// Judged pairs are excluded in either direction; pending pairs remain visible
	// for SkipInsert lookups, but cannot generate duplicate inserts.
	for i, status := range []string{"judged", "judged", "pending"} {
		src, dst := sourceSync, targets[i]
		if i == 1 {
			src, dst = dst, src
		}
		relation, err := s.SaveRelation(SaveRelationParams{SyncID: fmt.Sprintf("rel-parity-%d", i), SourceID: src, TargetID: dst})
		if err != nil {
			t.Fatal(err)
		}
		if status == "judged" {
			if _, err := s.JudgeRelation(JudgeRelationParams{JudgmentID: relation.SyncID, Relation: "related", MarkedByActor: "test", MarkedByKind: "human"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	options := []struct {
		name string
		opts CandidateOptions
	}{
		{"default", CandidateOptions{}},
		{"all", CandidateOptions{Limit: 100}},
		{"other project", CandidateOptions{Project: "otherproject"}},
		{"other scope", CandidateOptions{Scope: "personal"}},
		{"absent project", CandidateOptions{Project: "absent"}},
		{"query override", CandidateOptions{Query: "beacon beacon canyon"}},
		{"no match", CandidateOptions{Query: "zzzzunmatchedzzzz"}},
		{"explicit zero max", CandidateOptions{BM25MaxRank: ptrFloat64(0)}},
		{"explicit zero floor", CandidateOptions{BM25Floor: ptrFloat64(0)}},
		{"negative max", CandidateOptions{BM25MaxRank: ptrFloat64(-0.00001), Limit: 2}},
		{"negative floor", CandidateOptions{BM25Floor: ptrFloat64(-0.00001), Limit: 2}},
	}
	for _, tc := range options {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.SkipInsert = true
			project, scope, queryText := "testproject", "project", title
			if opts.Project != "" {
				project = opts.Project
			}
			if opts.Scope != "" {
				scope = opts.Scope
			}
			if strings.TrimSpace(opts.Query) != "" {
				queryText = opts.Query
			}
			limit := opts.Limit
			if limit <= 0 {
				limit = defaultCandidateLimit
			}
			operator, threshold := "<=", 0.0
			if opts.BM25MaxRank != nil {
				threshold = *opts.BM25MaxRank
			}
			if opts.BM25Floor != nil {
				operator, threshold = ">=", *opts.BM25Floor
			}
			rows, err := s.db.Query(fmt.Sprintf(candidateOriginalFTSQuery, operator), sanitizeFTSCandidates(queryText), sourceID, sourceSync, sourceSync, project, scope, threshold, limit)
			if err != nil {
				t.Fatal(err)
			}
			var expected []Candidate
			for rows.Next() {
				var c Candidate
				if err := rows.Scan(&c.ID, &c.SyncID, &c.Title, &c.Type, &c.TopicKey, &c.Score); err != nil {
					_ = rows.Close()
					t.Fatal(err)
				}
				expected = append(expected, c)
			}
			rowErr := rows.Err()
			closeErr := rows.Close()
			if rowErr != nil {
				t.Fatal(rowErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			got, err := s.FindCandidates(sourceID, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("got %+v, want original candidates %+v", got, expected)
			}
		})
	}
}

// Alternating order reduces systematic baseline-first warm-cache bias. This
// benchmark measures candidate SQL, not writes or MCP transport latency.
func BenchmarkCandidateRankComparison(b *testing.B) {
	for _, selective := range []bool{false, true} {
		b.Run(fmt.Sprintf("Selective%t", selective), func(b *testing.B) {
			s, id := candidateBenchCorpus(b, 15000, 20, selective)
			queries := candidateBenchmarkQueries()
			for _, order := range [][]string{{"Baseline", "FilteredMaterializedRank"}, {"FilteredMaterializedRank", "Baseline"}} {
				b.Run(strings.Join(order, "Then"), func(b *testing.B) {
					for _, name := range order {
						b.Run(name, func(b *testing.B) {
							query := queries[name]
							b.ReportAllocs()
							for i := 0; i < b.N; i++ {
								got, err := candidateBenchmarkLookup(s, id, query)
								if err != nil {
									b.Fatal(err)
								}
								if len(got) != 5 {
									b.Fatalf("got %d candidates", len(got))
								}
							}
						})
					}
				})
			}
		})
	}
}
