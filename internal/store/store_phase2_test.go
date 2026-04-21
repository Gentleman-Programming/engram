package store

// store_phase2_test.go — Phase 2 audit tests (2026-04-22).
//
// 10 tests imprescindibles, en orden de prioridad:
//
//  T1 — RecentObservations valida fechas inválidas (NO cubiertas antes).
//  T2 — Import normaliza proyecto de data externa.
//  T3 — FormatContext con fecha inválida propaga error (integración).
//  T4 — Export incluye observaciones soft-deleted.
//  T5 — MergeProjects mueve prompts, no solo observations.
//  T6 — Search con caracteres especiales FTS5 (comillas, paréntesis).
//  T7 — AddObservation trunca contenido exactamente al límite.
//  T8 — UpdateObservation con ID inexistente devuelve error.
//  T9 — filterByProject no pierde sesiones referenciadas por obs de otro proyecto.
//  T10 — normalizeTime es idempotente (sync boundary).

import (
	"strings"
	"testing"
)

func strPtr(s string) *string { return &s }

// ─── T1: RecentObservations valida fechas ────────────────────────────────────

func TestRecentObservationsInvalidDateReturnsError(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, _ = s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "test", Content: "content", Project: "proj",
	})

	// Invalid since
	_, err := s.RecentObservations("proj", "", 10, "not-a-date", "")
	if err == nil {
		t.Fatal("expected error for invalid since in RecentObservations, got nil — filter would be silently ignored")
	}

	// Invalid until
	_, err = s.RecentObservations("proj", "", 10, "", "yesterday")
	if err == nil {
		t.Fatal("expected error for invalid until in RecentObservations, got nil")
	}

	// Valid dates still work
	obs, err := s.RecentObservations("proj", "", 10, "2000-01-01", "2999-12-31")
	if err != nil {
		t.Fatalf("valid dates should work: %v", err)
	}
	if len(obs) == 0 {
		t.Error("expected observations with valid date range, got 0")
	}
}

// ─── T2: Import normaliza proyectos ──────────────────────────────────────────

func TestImportNormalizesProjectNames(t *testing.T) {
	s := newTestStore(t)

	// Import data with mixed-case project name
	data := &ExportData{
		Version:    "0.1.0",
		ExportedAt: "2026-04-22T00:00:00Z",
		Sessions: []Session{
			{ID: "imported-sess", Project: "MyProject", Directory: "/tmp"},
		},
		Observations: []Observation{
			{
				SyncID:    "obs-sync-1",
				SessionID: "imported-sess",
				Type:      "decision",
				Title:     "imported obs",
				Content:   "content from import",
				Project:   strPtr("MyProject"),
				Scope:     "project",
				CreatedAt: "2026-04-22 00:00:01",
				UpdatedAt: "2026-04-22 00:00:01",
			},
		},
	}

	result, err := s.Import(data)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.SessionsImported != 1 {
		t.Fatalf("expected 1 session imported, got %d", result.SessionsImported)
	}

	// Session project should be normalized to lowercase
	sess, err := s.GetSession("imported-sess")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Project != "myproject" {
		t.Errorf("expected imported session project to be 'myproject', got %q", sess.Project)
	}

	// Search must find it with lowercase name
	results, err := s.Search("imported", SearchOptions{Project: "myproject"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Error("search with 'myproject' found nothing after import with 'MyProject'")
	}
}

// ─── T3: FormatContext integración de validación de fecha ─────────────────────

func TestFormatContextInvalidDatePropagatesError(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, _ = s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "test", Content: "content", Project: "proj",
	})

	// FormatContext calls RecentSessions (validated) and RecentObservations (validated).
	// Invalid date must propagate up.
	_, err := s.FormatContext("proj", "", "garbage-date", "")
	if err == nil {
		t.Fatal("expected error from FormatContext with invalid date, got nil")
	}
}

// ─── T4: Export incluye observaciones soft-deleted ───────────────────────────

func TestExportIncludesSoftDeletedObservations(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, _ := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "will be deleted", Content: "content", Project: "proj",
	})
	// Soft-delete
	if err := s.DeleteObservation(id, false); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	data, err := s.Export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Export should include ALL observations (including soft-deleted) for full backup
	found := false
	for _, obs := range data.Observations {
		if obs.Title == "will be deleted" {
			found = true
			if obs.DeletedAt == nil {
				t.Error("expected deleted_at to be set on soft-deleted observation in export")
			}
		}
	}
	if !found {
		t.Error("soft-deleted observation not included in export — data loss risk on restore")
	}
}

// ─── T5: MergeProjects mueve prompts ────────────────────────────────────────

func TestMergeProjectsMovesPromptsToo(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "old-name", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, _ = s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "obs1", Content: "content", Project: "old-name",
	})
	_, _ = s.AddPrompt(AddPromptParams{
		SessionID: "s1", Content: "user asked something", Project: "old-name",
	})

	result, err := s.MergeProjects([]string{"old-name"}, "new-name")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if result.PromptsUpdated == 0 {
		t.Error("MergeProjects did not move prompts — data orphaned under old name")
	}

	// Verify prompts are under new name
	prompts, _ := s.RecentPrompts("new-name", 10)
	if len(prompts) == 0 {
		t.Error("no prompts found under 'new-name' after merge")
	}
}

// ─── T6: Search con caracteres especiales FTS5 ─────────────────────────────

func TestSearchSpecialCharactersHandled(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, _ = s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "bugfix",
		Title:   "Fixed N+1 in user(list)",
		Content: "The query was SELECT * FROM users WHERE id IN (...)",
		Project: "proj",
	})

	// These characters would crash FTS5 without sanitizeFTS
	dangerousQueries := []string{
		`"fix auth"`,      // pre-quoted
		`user(list)`,      // parentheses
		`SELECT * FROM`,   // asterisk
		`N+1`,             // plus sign
		`error: "failed"`, // mixed quotes
	}

	for _, q := range dangerousQueries {
		t.Run("query="+q, func(t *testing.T) {
			// Must not panic or return a SQLite syntax error
			_, err := s.Search(q, SearchOptions{})
			if err != nil && strings.Contains(err.Error(), "fts5: syntax error") {
				t.Errorf("FTS5 syntax error for query %q — sanitizeFTS not effective: %v", q, err)
			}
		})
	}
}

// ─── T7: AddObservation trunca contenido al límite exacto ───────────────────

func TestAddObservationTruncatesAtExactLimit(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	cfg.MaxObservationLength = 100 // very low for testing
	cfg.DedupeWindow = 0

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}

	longContent := strings.Repeat("a", 200)
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "truncation test", Content: longContent, Project: "proj",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	obs, _ := s.GetObservation(id)
	if len(obs.Content) > 120 { // 100 + "... [truncated]" = 115 max
		t.Errorf("content not truncated: length %d, expected ≤ 120", len(obs.Content))
	}
	if !strings.HasSuffix(obs.Content, "[truncated]") {
		t.Errorf("expected truncation marker, got: ...%q", obs.Content[len(obs.Content)-20:])
	}
}

// ─── T8: UpdateObservation con ID inexistente ───────────────────────────────

func TestUpdateObservationNonExistentIDReturnsError(t *testing.T) {
	s := newTestStore(t)

	title := "new title"
	_, err := s.UpdateObservation(999999, UpdateObservationParams{
		Title: &title,
	})
	if err == nil {
		t.Fatal("expected error updating non-existent observation, got nil")
	}
}

// ─── T10: validateDate boundary ──────────────────────────────────────────────

func TestValidateDateBoundary(t *testing.T) {
	// Date-only without time component — valid
	if err := validateDate("2026-01-01"); err != nil {
		t.Errorf("YYYY-MM-DD should be valid: %v", err)
	}

	// Date with time in RFC3339 — valid
	if err := validateDate("2026-01-01T00:00:00Z"); err != nil {
		t.Errorf("RFC3339 should be valid: %v", err)
	}

	// Partial ISO date — invalid (common LLM mistake)
	if err := validateDate("2026-04"); err == nil {
		t.Error("YYYY-MM should be invalid, got nil")
	}

	// SQLite datetime format (not accepted as input) — invalid
	if err := validateDate("2026-04-22 15:04:05"); err == nil {
		t.Error("'YYYY-MM-DD HH:MM:SS' without T/Z should be invalid for input validation")
	}
}
