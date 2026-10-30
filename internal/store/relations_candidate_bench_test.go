package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Frozen pre-optimization query: do not derive the reference from production SQL.
const candidateOriginalFTSQuery = `
 SELECT o.id, ifnull(o.sync_id,''), o.title, o.type, o.topic_key, fts.rank
 FROM observations_fts fts CROSS JOIN observations o ON o.id=fts.rowid
 WHERE observations_fts MATCH ? AND o.id != ? AND NOT EXISTS (
 SELECT 1 FROM memory_relations r WHERE ((r.source_id=? AND r.target_id=ifnull(o.sync_id,'')) OR (r.source_id=ifnull(o.sync_id,'') AND r.target_id=?)) AND r.judgment_status='judged'
 ) AND o.deleted_at IS NULL AND ifnull(o.project,'')=ifnull(?,'') AND o.scope=?
 AND fts.rank %s ? ORDER BY fts.rank LIMIT ?
`

const candidateBenchTitle = "anchor beacon canyon delta ember forest galaxy harbor island jungle kernel lantern"

// Seed through one transaction so fixture construction is not the measured work.
// All data is synthetic; repeated terms model dense FTS position lists.
func candidateBenchCorpus(tb testing.TB, size, repetitions int, selective bool) (*Store, int64) {
	tb.Helper()
	cfg, err := DefaultConfig()
	if err != nil {
		tb.Fatal(err)
	}
	cfg.DataDir = tb.TempDir()
	s, err := New(cfg)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession("candidate-bench", "candidate-bench", filepath.Join(cfg.DataDir, "workdir")); err != nil {
		tb.Fatal(err)
	}
	var savedID int64
	err = s.withTx(func(tx *sql.Tx) error {
		for i := 0; i < size; i++ {
			body := strings.Repeat(candidateBenchTitle+" ", repetitions) + fmt.Sprintf(" unique content %d", i)
			project := "candidate-bench"
			if selective && i%10 != 0 {
				project = "other-project"
			}
			result, err := tx.Exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at) VALUES (?, 'candidate-bench', 'decision', ?, ?, ?, 'project', ?, 1, 1, datetime('now'), datetime('now'))`, fmt.Sprintf("candidate-bench-%d", i), fmt.Sprintf("%s %d", candidateBenchTitle, i), body, project, fmt.Sprintf("candidate-hash-%d", i))
			if err != nil {
				return err
			}
			if i == 0 {
				savedID, err = result.LastInsertId()
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatal(err)
	}
	return s, savedID
}

// Compare the frozen reference with the actual production query so future
// changes cannot leave this benchmark measuring an obsolete experimental copy.
func candidateBenchmarkQueries() map[string]string {
	return map[string]string{
		"Baseline":                 fmt.Sprintf(candidateOriginalFTSQuery, "<="),
		"FilteredMaterializedRank": fmt.Sprintf(findCandidatesFTSQuery, "<="),
	}
}

func candidateBenchmarkLookup(s *Store, savedID int64, query string) (candidates []Candidate, err error) {
	rows, err := s.db.Query(query, sanitizeFTSCandidates(candidateBenchTitle+" 0"), savedID, "candidate-bench-0", "candidate-bench-0", "candidate-bench", "project", 0.0, 5)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
	}()
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.ID, &c.SyncID, &c.Title, &c.Type, &c.TopicKey, &c.Score); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

func TestCandidateQueryBenchmarkMatchesPublicLookup(t *testing.T) {
	s, id := candidateBenchCorpus(t, 100, 3, false)
	expected, err := s.FindCandidates(id, CandidateOptions{SkipInsert: true, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(expected) != 5 {
		t.Fatalf("expected five candidates, got %d", len(expected))
	}
	for name, query := range candidateBenchmarkQueries() {
		t.Run(name, func(t *testing.T) {
			got, err := candidateBenchmarkLookup(s, id, query)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("candidate mismatch: got %+v want %+v", got, expected)
			}
		})
	}
}

func BenchmarkFindCandidatesQuery(b *testing.B) {
	for _, size := range []int{1000, 15000} {
		for _, repetitions := range []int{1, 20} {
			b.Run(fmt.Sprintf("Rows%d/Repeat%d", size, repetitions), func(b *testing.B) {
				s, id := candidateBenchCorpus(b, size, repetitions, false)
				for _, name := range []string{"Baseline", "FilteredMaterializedRank"} {
					query := candidateBenchmarkQueries()[name]
					b.Run(name, func(b *testing.B) {
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
	}
}
