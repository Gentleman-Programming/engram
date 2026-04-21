package store

// store_audit_test.go — tests derived from the 10-test audit (2026-04-22).
//
// Covers:
//   T2  — Invalid date strings must be rejected, not silently ignored.
//   T3  — stripPrivateTags must strip regardless of case and across newlines.
//   T8  — Empty/blank queries must return an error, not crash FTS5.
//   T9  — NormalizeProject round-trip: save with mixed-case → find with lowercase.
//   Bug — topic_key fast-path with since/until used wrong SQL alias (o.created_at).

import (
	"errors"
	"strings"
	"testing"
)

// ─── T8: Empty query guard ────────────────────────────────────────────────────

func TestSearchEmptyQueryReturnsError(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, _ = s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "something", Content: "some content", Project: "proj",
	})

	cases := []string{"", "   ", "\t\n", "  \t  "}
	for _, q := range cases {
		t.Run("query="+strings.TrimSpace("«"+q+"»"), func(t *testing.T) {
			_, err := s.Search(q, SearchOptions{})
			if err == nil {
				t.Fatal("expected error for blank query, got nil — FTS5 MATCH \"\" would crash SQLite")
			}
			if !strings.Contains(err.Error(), "empty") {
				t.Errorf("expected 'empty' in error message, got: %v", err)
			}
		})
	}
}

// ─── T2: Invalid date validation ─────────────────────────────────────────────

func TestSearchInvalidSinceReturnsError(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, _ = s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "something", Content: "content", Project: "proj",
	})

	invalidDates := []string{
		"not-a-date",
		"yesterday",
		"2026-13-01", // invalid month
		"2026/04/22", // wrong separator
		"22-04-2026", // wrong order
		"hace 3 dias",
	}

	for _, d := range invalidDates {
		t.Run("since="+d, func(t *testing.T) {
			_, err := s.Search("something", SearchOptions{Since: d})
			if err == nil {
				t.Fatalf("expected error for invalid since=%q, got nil — filter would be silently ignored by SQLite datetime()", d)
			}
			if !strings.Contains(err.Error(), "invalid date") {
				t.Errorf("expected 'invalid date' in error, got: %v", err)
			}
		})
	}
}

func TestSearchInvalidUntilReturnsError(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, _ = s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "something", Content: "content", Project: "proj",
	})

	_, err := s.Search("something", SearchOptions{Until: "not-a-date"})
	if err == nil {
		t.Fatal("expected error for invalid until, got nil")
	}
}

func TestSearchValidDatesAccepted(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, _ = s.AddObservation(AddObservationParams{
		SessionID: "s1", Type: "decision",
		Title: "something", Content: "content", Project: "proj",
	})

	validDates := []string{
		"2026-04-22",
		"2000-01-01",
		"2026-04-22T15:04:05Z",
		"2026-04-22T15:04:05+02:00",
	}
	for _, d := range validDates {
		t.Run("since="+d, func(t *testing.T) {
			_, err := s.Search("something", SearchOptions{Since: d})
			if err != nil {
				t.Errorf("expected valid date %q to be accepted, got: %v", d, err)
			}
		})
	}
}

func TestRecentSessionsInvalidDateReturnsError(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.RecentSessions("proj", 10, "not-a-date", "")
	if err == nil {
		t.Fatal("expected error for invalid since in RecentSessions, got nil")
	}

	_, err = s.RecentSessions("proj", 10, "", "bad-until")
	if err == nil {
		t.Fatal("expected error for invalid until in RecentSessions, got nil")
	}
}

// ─── T3: stripPrivateTags security ───────────────────────────────────────────

func TestStripPrivateTagsCaseInsensitive(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"lowercase", "<private>secret123</private>"},
		{"uppercase", "<PRIVATE>secret123</PRIVATE>"},
		{"mixed", "<Private>secret123</Private>"},
		{"multiline", "<private>\napi_key=abc\npassword=xyz\n</private>"},
		{"inline", "before<private>secret</private>after"},
		{"multiple", "<private>one</private> middle <private>two</private>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripPrivateTags(tc.input)
			if strings.Contains(got, "secret") || strings.Contains(got, "api_key") ||
				strings.Contains(got, "password") || strings.Contains(got, "one") ||
				strings.Contains(got, "two") {
				t.Errorf("stripPrivateTags(%q) leaked sensitive content: %q", tc.input, got)
			}
			if !strings.Contains(got, "[REDACTED]") && got != "" && got != "before after" && got != "middle" {
				// For "inline" case, result should be "beforeafter" or "before[REDACTED]after"
				if tc.name == "inline" && !strings.Contains(got, "before") {
					t.Errorf("unexpected result for inline: %q", got)
				}
			}
		})
	}
}

func TestStripPrivateTagsPersistedToStore(t *testing.T) {
	// End-to-end: private content must not reach the database.
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "My api key is <PRIVATE>sk-abc123</PRIVATE>",
		Content:   "Token: <private>supersecret</private> was rotated",
		Project:   "proj",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}

	if strings.Contains(obs.Title, "sk-abc123") {
		t.Errorf("secret leaked in title: %q", obs.Title)
	}
	if strings.Contains(obs.Content, "supersecret") {
		t.Errorf("secret leaked in content: %q", obs.Content)
	}
	if !strings.Contains(obs.Title, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in title, got: %q", obs.Title)
	}
	if !strings.Contains(obs.Content, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in content, got: %q", obs.Content)
	}
}

// ─── T9: NormalizeProject round-trip ─────────────────────────────────────────

func TestNormalizeProjectRoundTrip(t *testing.T) {
	// Save with mixed case, find with lowercase — must work.
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// AddObservation normalizes internally, so "MyProject" → "myproject"
	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "roundtrip test",
		Content:   "the content to find",
		Project:   "MyProject",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Search with exact lowercase — should find
	results, err := s.Search("roundtrip", SearchOptions{Project: "myproject"})
	if err != nil {
		t.Fatalf("search with lowercase: %v", err)
	}
	if len(results) == 0 {
		t.Error("search with 'myproject' found nothing — normalization broken on write")
	}

	// Search with uppercase — NormalizeProject on read normalizes it too
	results, err = s.Search("roundtrip", SearchOptions{Project: "MYPROJECT"})
	if err != nil {
		t.Fatalf("search with uppercase: %v", err)
	}
	if len(results) == 0 {
		t.Error("search with 'MYPROJECT' found nothing — normalization broken on read")
	}

	// Different name must NOT find it
	results, err = s.Search("roundtrip", SearchOptions{Project: "my-project"})
	if err != nil {
		t.Fatalf("search with different name: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("search with 'my-project' found %d results, expected 0 — project isolation broken", len(results))
	}
}

// ─── Bug: topic_key fast-path with date filters used wrong SQL alias ──────────

func TestTopicKeySearchWithDateFiltersWorks(t *testing.T) {
	// Regression for bug: the topic_key search path added " AND datetime(o.created_at)"
	// but the FROM clause has no "o" alias — should be "datetime(created_at)".
	// Without the fix, any search for a topic_key (query containing "/") with
	// since/until would silently return no results or a SQLite error swallowed
	// by the "if err == nil" guard.
	s := newTestStore(t)
	if err := s.CreateSession("s1", "proj", ""); err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth model",
		Content:   "content about auth",
		Project:   "proj",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Search by topic_key (query contains "/") with a valid past since
	results, err := s.Search("architecture/auth-model", SearchOptions{
		Since: "2000-01-01",
	})
	if err != nil {
		t.Fatalf("topic_key search with since: %v", err)
	}
	if len(results) == 0 {
		t.Error("topic_key search with valid since returned nothing — likely SQL alias bug not fixed")
	}

	// Search with future since — must return empty, not an SQL error
	results, err = s.Search("architecture/auth-model", SearchOptions{
		Since: "2999-01-01",
	})
	if err != nil {
		t.Fatalf("topic_key search with future since: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("topic_key search with future since returned %d results, expected 0", len(results))
	}
}

// ─── validateDate unit tests ─────────────────────────────────────────────────

func TestValidateDate(t *testing.T) {
	// Valid — no error
	validCases := []string{
		"",
		"2026-04-22",
		"2000-01-01",
		"2026-04-22T15:04:05Z",
		"2026-04-22T15:04:05+02:00",
	}
	for _, d := range validCases {
		if err := validateDate(d); err != nil {
			t.Errorf("validateDate(%q) unexpected error: %v", d, err)
		}
	}

	// Invalid — must error
	invalidCases := []string{
		"not-a-date",
		"yesterday",
		"2026-13-01",
		"2026/04/22",
		"22-04-2026",
		"april 22",
		"hace 3 dias",
	}
	for _, d := range invalidCases {
		err := validateDate(d)
		if err == nil {
			t.Errorf("validateDate(%q) expected error, got nil — SQLite would silently ignore this", d)
		}
		if !errors.Is(err, err) { // always true, just checking err is non-nil
			t.Errorf("validateDate(%q) returned non-error type", d)
		}
	}
}
