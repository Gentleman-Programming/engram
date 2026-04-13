package store

import (
	"testing"
)

func strPtr(s string) *string { return &s }

// Task 2.6: Import observation with existing sync_id → UPDATE (not duplicate)
func TestImportIdempotent_ExistingSyncID(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-1", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// First import
	data := &ExportData{
		Observations: []Observation{
			{SyncID: "obs-aaa", SessionID: "sess-1", Type: "manual", Title: "Original", Content: "First version", Project: strPtr("proj"), Scope: "project"},
		},
	}
	res, err := s.Import(data)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if res.ObservationsImported != 1 {
		t.Fatalf("expected 1 imported, got %d", res.ObservationsImported)
	}

	// Second import with same sync_id but updated content
	data2 := &ExportData{
		Observations: []Observation{
			{SyncID: "obs-aaa", SessionID: "sess-1", Type: "manual", Title: "Updated", Content: "Second version", Project: strPtr("proj"), Scope: "project"},
		},
	}
	res2, err := s.Import(data2)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if res2.ObservationsUpdated != 1 {
		t.Fatalf("expected 1 updated, got %d", res2.ObservationsUpdated)
	}

	// Should have exactly 1 observation with sync_id='obs-aaa' (not 2)
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM observations WHERE sync_id = 'obs-aaa'").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 obs, got %d (duplicate!)", count)
	}

	// Content should be updated
	var content string
	if err := s.db.QueryRow("SELECT content FROM observations WHERE sync_id = 'obs-aaa'").Scan(&content); err != nil {
		t.Fatalf("get content: %v", err)
	}
	if content != "Second version" {
		t.Fatalf("expected 'Second version', got '%s'", content)
	}
}

// Task 2.7: Import observation with new sync_id → INSERT
func TestImportIdempotent_NewSyncID(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-1", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	data := &ExportData{
		Observations: []Observation{
			{SyncID: "obs-new-1", SessionID: "sess-1", Type: "manual", Title: "New obs", Content: "Content", Project: strPtr("proj"), Scope: "project"},
		},
	}
	res, err := s.Import(data)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.ObservationsImported != 1 {
		t.Fatalf("expected 1 imported, got %d", res.ObservationsImported)
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM observations WHERE sync_id = 'obs-new-1'").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 obs, got %d", count)
	}
}

// Task 2.8: Import then search → imported obs found via FTS5
func TestImportIdempotent_FTS5Searchable(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-1", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	data := &ExportData{
		Observations: []Observation{
			{SyncID: "obs-fts", SessionID: "sess-1", Type: "manual", Title: "JWT auth middleware", Content: "Implemented JWT validation", Project: strPtr("proj"), Scope: "project"},
		},
	}
	if _, err := s.Import(data); err != nil {
		t.Fatalf("import: %v", err)
	}

	results, err := s.Search("JWT auth", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected imported observation to be found via FTS5 search, got 0 results")
	}

	found := false
	for _, r := range results {
		if r.SyncID == "obs-fts" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("imported observation with sync_id='obs-fts' not found in search results")
	}
}

// Task 2.9: Import update then search → new content found
func TestImportIdempotent_FTS5UpdatedContent(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("sess-1", "proj", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Import original
	data := &ExportData{
		Observations: []Observation{
			{SyncID: "obs-fts-upd", SessionID: "sess-1", Type: "manual", Title: "Old auth pattern", Content: "Using session cookies", Project: strPtr("proj"), Scope: "project"},
		},
	}
	if _, err := s.Import(data); err != nil {
		t.Fatalf("first import: %v", err)
	}

	// Import updated version
	data2 := &ExportData{
		Observations: []Observation{
			{SyncID: "obs-fts-upd", SessionID: "sess-1", Type: "manual", Title: "New auth pattern", Content: "Switched to JWT tokens", Project: strPtr("proj"), Scope: "project"},
		},
	}
	if _, err := s.Import(data2); err != nil {
		t.Fatalf("second import: %v", err)
	}

	// Search for NEW content should find it
	results, err := s.Search("JWT tokens", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("search new: %v", err)
	}
	foundNew := false
	for _, r := range results {
		if r.SyncID == "obs-fts-upd" {
			foundNew = true
			break
		}
	}
	if !foundNew {
		t.Fatal("updated content 'JWT tokens' not found in FTS5 search")
	}

	// Search for OLD content should NOT find it
	oldResults, err := s.Search("session cookies", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("search old: %v", err)
	}
	for _, r := range oldResults {
		if r.SyncID == "obs-fts-upd" {
			t.Fatal("old content 'session cookies' still found in FTS5 after update — FTS5 not re-indexed")
		}
	}
}
