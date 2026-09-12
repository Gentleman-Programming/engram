package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const searchBenchObservations = 5_000

var (
	searchBenchOnce  sync.Once
	searchBenchStore *Store
	searchBenchDir   string
	searchBenchErr   error
)

// TestMain releases the shared benchmark corpus after all package tests finish.
func TestMain(m *testing.M) {
	code := m.Run()
	if searchBenchStore != nil {
		_ = searchBenchStore.Close()
	}
	if searchBenchDir != "" {
		_ = os.RemoveAll(searchBenchDir)
	}
	os.Exit(code)
}

func searchBenchCorpus(b *testing.B) *Store {
	b.Helper()
	searchBenchOnce.Do(buildSearchBenchCorpus)
	if searchBenchErr != nil {
		b.Fatalf("build search benchmark corpus: %v", searchBenchErr)
	}
	return searchBenchStore
}

func buildSearchBenchCorpus() {
	cfg, err := DefaultConfig()
	if err != nil {
		searchBenchErr = err
		return
	}
	searchBenchDir, err = os.MkdirTemp("", "engram-search-bench-")
	if err != nil {
		searchBenchErr = err
		return
	}
	cfg.DataDir = searchBenchDir
	searchBenchStore, err = New(cfg)
	if err != nil {
		searchBenchErr = err
		return
	}
	if err := searchBenchStore.CreateSession("search-bench-session", "search-bench", filepath.Join(searchBenchDir, "workdir")); err != nil {
		searchBenchErr = err
		return
	}

	types := []string{"decision", "pattern", "discovery"}
	searchBenchErr = searchBenchStore.withTx(func(tx *sql.Tx) error {
		for i := 0; i < searchBenchObservations; i++ {
			scope := "project"
			if i%2 == 0 {
				scope = "session"
			}
			_, err := tx.Exec(`INSERT INTO observations (sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at) VALUES (?, 'search-bench-session', ?, ?, ?, 'search-bench', ?, ?, 1, 1, datetime('now'), datetime('now'))`,
				fmt.Sprintf("search-bench-%06d", i),
				types[i%len(types)],
				fmt.Sprintf("memory search benchmark title %06d shared ranking vocabulary", i),
				fmt.Sprintf("content %06d unique-term-%06d shared benchmark content about queries, ranking, and hydration cost across the corpus", i, i),
				scope,
				fmt.Sprintf("search-bench-hash-%06d", i),
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func benchmarkSearch(b *testing.B, query string, opts SearchOptions) {
	s := searchBenchCorpus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Search(query, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkSearchContext(b *testing.B, query string, opts SearchOptions) {
	s := searchBenchCorpus(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.SearchContext(ctx, query, opts); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSearch_AllMode_Hit measures default FTS query construction, ranking,
// and result hydration against a deterministic corpus.
func BenchmarkSearch_AllMode_Hit(b *testing.B) {
	benchmarkSearch(b, "shared ranking vocabulary", SearchOptions{Project: "search-bench"})
}

func BenchmarkSearch_AnyMode_Hit(b *testing.B) {
	benchmarkSearch(b, "ranking hydration", SearchOptions{Project: "search-bench", MatchMode: "any"})
}

func BenchmarkSearch_NoHit(b *testing.B) {
	benchmarkSearch(b, "term-that-matches-nothing-here", SearchOptions{Project: "search-bench"})
}

func BenchmarkSearch_TypeFilter(b *testing.B) {
	benchmarkSearch(b, "shared ranking vocabulary", SearchOptions{Project: "search-bench", Type: "decision"})
}

func BenchmarkSearch_Limit20(b *testing.B) {
	benchmarkSearch(b, "shared ranking vocabulary", SearchOptions{Project: "search-bench", Limit: 20})
}

// BenchmarkSearchContext_AllMode_Hit measures the cancellable API used by
// request-scoped callers without introducing cancellation into the workload.
func BenchmarkSearchContext_AllMode_Hit(b *testing.B) {
	benchmarkSearchContext(b, "shared ranking vocabulary", SearchOptions{Project: "search-bench"})
}
