package store

import (
	"database/sql"
	"fmt"
	"testing"
)

const (
	scanPageTestObservations      = 250
	scanPageBenchmarkObservations = 5_000
)

func TestScanProject_PageProof(t *testing.T) {
	s, ids := scanPageStore(t, scanPageTestObservations)
	relations, mutations := scanPageRows(t, s, "memory_relations"), scanPageRows(t, s, "sync_mutations")
	result := scanPage(t, s, ScanOptions{Project: "scan-page"})
	scanPageRequire(t, result.Inspected == DefaultScanLimit, "inspected = %d", result.Inspected)
	scanPageRequire(t, result.RankedQueries == result.Inspected, "ranked queries = %d, inspected = %d", result.RankedQueries, result.Inspected)
	scanPageRequire(t, result.NextCursor != nil && *result.NextCursor == ids[DefaultScanLimit-1], "missing page cursor: %+v", result)
	scanPageRequire(t, result.DryRun && result.RelationsInserted == 0 && scanPageRows(t, s, "memory_relations") == relations && scanPageRows(t, s, "sync_mutations") == mutations, "dry run wrote rows: %+v", result)
}
func TestScanProject_KeysetPages(t *testing.T) {
	s, ids := scanPageStore(t, 3)
	first := scanPage(t, s, ScanOptions{Project: "scan-page", Limit: 2})
	scanPageRequire(t, first.Inspected == 2 && first.RankedQueries == 2 && first.NextCursor != nil && *first.NextCursor == ids[1], "first page: %+v", first)
	inserted := scanPageAdd(t, s, "shared scan vocabulary decision 0003", "shared scan vocabulary candidate ranking")
	second := scanPage(t, s, ScanOptions{Project: "scan-page", Limit: 2, Cursor: *first.NextCursor})
	scanPageRequire(t, second.Inspected == 2 && second.RankedQueries == 2 && second.NextCursor == nil, "second page: %+v", second)
	scanPageRequire(t, first.CandidatesFound == 4 && second.CandidatesFound == 6 && inserted > ids[2], "keyset pages overlap or missed an inserted ID: %+v %+v", first, second)
}
func TestScanProject_CapsHaveNoContinuation(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		t.Run(fmt.Sprintf("semantic=%t", semantic), func(t *testing.T) {
			s, _ := scanPageStore(t, 3)
			opts := ScanOptions{Project: "scan-page", Apply: true, MaxInsert: 1}
			if semantic {
				opts.Semantic, opts.MaxSemantic = true, 1
				opts.Runner = &verdictRunner{verdict: SemanticVerdict{Relation: RelationNotConflict}}
				opts.BuildPrompt = func(ObservationSnippet, ObservationSnippet) string { return "" }
			}
			result := scanPage(t, s, opts)
			scanPageRequire(t, result.Capped && result.NextCursor == nil, "capped page emitted an unsafe continuation: %+v", result)
		})
	}
}
func TestScanProject_ExactFinalCandidateIsNotCapped(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		t.Run(fmt.Sprintf("semantic=%t", semantic), func(t *testing.T) {
			s, _ := scanPageStore(t, 0)
			first := scanPageAdd(t, s, "alpha", "alpha")
			scanPageAdd(t, s, "unrelated", "alpha") // Sentinel for the one-observation page.
			opts := ScanOptions{Project: "scan-page", Apply: true, Limit: 1, MaxInsert: 1}
			if semantic {
				opts.Semantic, opts.MaxSemantic = true, 1
				opts.Runner = &verdictRunner{verdict: SemanticVerdict{Relation: RelationNotConflict}}
				opts.BuildPrompt = func(ObservationSnippet, ObservationSnippet) string { return "" }
			}
			result := scanPage(t, s, opts)
			scanPageRequire(t, !result.Capped && result.NextCursor != nil && *result.NextCursor == first, "exact final candidate falsely capped: %+v", result)
		})
	}
}
func BenchmarkScanProject_Page5000(b *testing.B) {
	s, _ := scanPageStore(b, scanPageBenchmarkObservations)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = scanPage(b, s, ScanOptions{Project: "scan-page"})
	}
}

// scanUniquePageStore builds the unique-vocabulary worst case for the batched
// scan: every observation title carries tokens that appear in no other
// observation, so the page's distinct-term vocabulary is maximal and no
// term's posting list is shared across sources.
func scanUniquePageStore(tb testing.TB, count int) (*Store, []int64) {
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
	if err := s.CreateSession("scan-uniq-session", "scan-uniq", "/tmp/scan-uniq"); err != nil {
		tb.Fatal(err)
	}
	ids := make([]int64, 0, count)
	if err := s.withTx(func(tx *sql.Tx) error {
		for i := 0; i < count; i++ {
			result, err := tx.Exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at) VALUES (?, ?, 'decision', ?, ?, 'scan-uniq', 'project', ?, 1, 1, datetime('now'), datetime('now'))`,
				fmt.Sprintf("scan-uniq-%04d", i), "scan-uniq-session",
				fmt.Sprintf("uniq%04da uniq%04db uniq%04dc", i, i, i),
				"scan benchmark body",
				fmt.Sprintf("scan-uniq-hash-%04d", i))
			if err != nil {
				return err
			}
			id, err := result.LastInsertId()
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	return s, ids
}

// BenchmarkScanProject_UniqueVocab5000 measures the batched scan on the
// unique-vocabulary worst case (review finding R4 on PR #1017): a full
// DefaultScanLimit page whose sources share no term, so Phase 1 issues one
// single-phrase query per distinct term with no shared posting lists.
func BenchmarkScanProject_UniqueVocab5000(b *testing.B) {
	s, _ := scanUniquePageStore(b, scanPageBenchmarkObservations)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = scanPage(b, s, ScanOptions{Project: "scan-uniq"})
	}
}

// BenchmarkScanLegacyRanking_UniqueVocab5000 measures the legacy per-source
// ranking cost on the same unique-vocabulary corpus: one FindCandidates call
// per inspected observation for a full page, the exact work ScanProject
// performed before the batched path. Comparing it with
// BenchmarkScanProject_UniqueVocab5000 bounds the worst-case tradeoff of the
// batching change instead of relying on the repetitive-vocabulary shape alone.
func BenchmarkScanLegacyRanking_UniqueVocab5000(b *testing.B) {
	s, ids := scanUniquePageStore(b, scanPageBenchmarkObservations)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < DefaultScanLimit; j++ {
			if _, err := s.FindCandidates(ids[j], CandidateOptions{Project: "scan-uniq", Scope: "project", Limit: scanCandidateLimit, SkipInsert: true}); err != nil {
				b.Fatal(err)
			}
		}
	}
}
func scanPage(tb testing.TB, s *Store, opts ScanOptions) ScanResult {
	tb.Helper()
	result, err := s.ScanProject(opts)
	if err != nil {
		tb.Fatal(err)
	}
	return result
}
func scanPageStore(tb testing.TB, count int) (*Store, []int64) {
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
	if err := s.CreateSession("scan-page-session", "scan-page", "/tmp/scan-page"); err != nil {
		tb.Fatal(err)
	}
	ids := make([]int64, 0, count)
	if err := s.withTx(func(tx *sql.Tx) error {
		for i := 0; i < count; i++ {
			result, err := tx.Exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at) VALUES (?, ?, 'decision', ?, ?, 'scan-page', 'project', ?, 1, 1, datetime('now'), datetime('now'))`, fmt.Sprintf("scan-page-%04d", i), "scan-page-session", fmt.Sprintf("shared scan vocabulary decision %04d", i), "shared scan vocabulary candidate ranking", fmt.Sprintf("scan-page-hash-%04d", i))
			if err != nil {
				return err
			}
			id, err := result.LastInsertId()
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	return s, ids
}
func scanPageAdd(tb testing.TB, s *Store, title, content string) int64 {
	tb.Helper()
	id, err := s.AddObservation(AddObservationParams{SessionID: "scan-page-session", Type: "decision", Title: title, Content: content, Project: "scan-page", Scope: "project"})
	if err != nil {
		tb.Fatal(err)
	}
	return id
}
func scanPageRows(t testing.TB, s *Store, table string) int {
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func scanPageRequire(t testing.TB, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}
