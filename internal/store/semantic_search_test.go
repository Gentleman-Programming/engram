package store

import (
	"os"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store/turboquant"
)

func TestTurboQuant(t *testing.T) {
	// Setup a temporary store with defaults handled
	tmpDir, err := os.MkdirTemp("", "engram-turboquant-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	s, err := New(Config{
		DataDir:           tmpDir,
		MaxSearchResults:  10,
		MaxContextResults: 10,
	})
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer s.Close()

	// 0. Setup test data (Create Session)
	err = s.CreateSession("test-session", "test-project", "/tmp/test")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Memory Inoculation
	memories := []struct {
		project string
		title   string
		content string
	}{
		{"ProjectA", "Auth Logic", "OAuth2 implementation using JWT tokens and RS256."},
		{"ProjectB", "Auth Logic Duplicate", "OAuth2 implementation using JWT tokens and RS256."},
		{"Global", "Database", "PostgreSQL database using GORM and migrations."},
		{"Global", "Frontend", "React application using Tailwind CSS and Vite."},
	}

	for _, m := range memories {
		_, err = s.AddObservation(AddObservationParams{
			SessionID: "test-session",
			Type:      "architecture",
			Title:     m.title,
			Content:   m.content,
			Project:   m.project,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// SUB-TEST: Semantic Precision
	t.Run("SemanticExpansion_Precision", func(t *testing.T) {
		query := "tokens JWT rs256"
		results, err := s.Search(query, SearchOptions{Limit: 5})
		if err != nil {
			t.Fatal(err)
		}

		t.Logf("Search results count: %d", len(results))
		for _, res := range results {
			project := ""
			if res.Project != nil {
				project = *res.Project
			}
			t.Logf(" - Found: %s in %s (Rank: %f)", res.Title, project, res.Rank)
		}

		found := false
		for _, res := range results {
			project := ""
			if res.Project != nil {
				project = *res.Project
			}
			if project == "projecta" || project == "projectb" {
				found = true
				dist := turboquant.HammingDistance(turboquant.ComputeSimHash(query), turboquant.BlockSignature(res.SimHash))
				t.Logf("SUCCESS: Found concept in '%s' with distance %d", project, dist)
			}
		}

		if !found {
			t.Error("FAIL: Could not find semantic match for tokens and JWT")
		}
	})

	// SUB-TEST: Metadata Filtering (The "otra prueba mas")
	t.Run("MetadataFiltering", func(t *testing.T) {
		query := "RS256 JWT tokens" // Identical semantic query
		
		// SEARCH ONLY IN PROJECT A
		results, err := s.Search(query, SearchOptions{Limit: 5, Project: "ProjectA"}) // Case normalization handled by Store
		if err != nil {
			t.Fatal(err)
		}

		if len(results) != 1 {
			t.Errorf("FAIL: Expected 1 result for ProjectA, but got %d", len(results))
		} else {
			project := ""
			if results[0].Project != nil {
				project = *results[0].Project
			}
			if project != "projecta" {
				t.Errorf("FAIL: Expected projecta, but got %s", project)
			} else {
				t.Log("SUCCESS: Metadata correctly filtered conceptual matches")
			}
		}
	})

	// SUB-TEST: Negative Matches (Noise Exclusion)
	t.Run("NoiseExclusion", func(t *testing.T) {
		query := "como plantar tomates en el jardin"
		results, err := s.Search(query, SearchOptions{Limit: 5})
		if err != nil {
			t.Fatal(err)
		}

		if len(results) > 0 {
			t.Errorf("FAIL: Irrelevant query returned %d results", len(results))
		} else {
			t.Log("SUCCESS: Noise query returned zero results")
		}
	})
	
	// SUB-TEST: Sorting and Priority (Hamming Boost)
	t.Run("HammingPriority", func(t *testing.T) {
		// Insert exact match to check sorting priority over older near-matches
		// Adding more unique words to make the semantic density very different
		exactContent := "PostgreSQL migrations GORM. This is the exact technical stack for database handling."
		_, err := s.AddObservation(AddObservationParams{
			SessionID: "test-session",
			Type:      "architecture",
			Title:     "PreciseDB",
			Content:   exactContent,
			Project:   "Global",
		})
		if err != nil {
			t.Fatal(err)
		}
		
		query := "PostgreSQL migrations GORM"
		results, err := s.Search(query, SearchOptions{Limit: 5, Project: "global"})
		if err != nil {
			t.Fatal(err)
		}
		
		if len(results) < 2 {
			t.Fatalf("FAIL: Expected at least 2 results for DB query, got %d", len(results))
		}
		
		// The most precise Hamming distance should be first (Rank lower is better)
		t.Logf("Rank 1st: %s (%f), Rank 2nd: %s (%f)", results[0].Title, results[0].Rank, results[1].Title, results[1].Rank)
		if results[0].Title != "PreciseDB" {
			t.Errorf("FAIL: Precise match '%s' should be ranked before general match '%s'", results[0].Title, results[1].Title)
		} else {
			t.Log("SUCCESS: Ranking prioritized the lower Hamming distance")
		}
	})

	// SUB-TEST: Full Reindexing (the "reindexa bien" part)
	t.Run("FullReindexing", func(t *testing.T) {
		// 1. Manually corrupt SimHash in DB to simulate stale or missing hashes
		_, err := s.db.Exec("UPDATE observations SET simhash = 0")
		if err != nil {
			t.Fatal(err)
		}

		// 2. Run Reindex
		count, err := s.ReindexTurboQuant()
		if err != nil {
			t.Fatalf("FAIL: Reindex failed: %v", err)
		}

		if count < 5 {
			t.Errorf("FAIL: Reindex processed only %d observations, expected at least 5", count)
		}

		// 3. Verify that search STILL works (meaning signatures were restored in cache)
		query := "RS256 JWT tokens"
		results, err := s.Search(query, SearchOptions{Limit: 1, Project: "ProjectA"})
		if err != nil {
			t.Fatal(err)
		}

		if len(results) == 0 {
			t.Error("FAIL: Search found nothing after reindexing")
		} else if results[0].Title != "Auth Logic" {
			t.Errorf("FAIL: Search found wrong result '%s' after reindexing", results[0].Title)
		} else {
			t.Logf("SUCCESS: Reindexed %d memories and verified search accuracy", count)
		}
	})
}
