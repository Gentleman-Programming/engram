package store

import "testing"

// TestSanitizeFTS verifies multi-word queries are joined with OR (not an
// implicit AND), while each term stays quoted to survive FTS5 special chars.
func TestSanitizeFTS(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"blank", "   ", ""},
		{"single", "auth", `"auth"`},
		{"multi", "fix auth bug", `"fix" OR "auth" OR "bug"`},
		{"strips existing quotes", `"fix" auth`, `"fix" OR "auth"`},
		{"collapses whitespace", "a   b", `"a" OR "b"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeFTS(tc.in); got != tc.want {
				t.Fatalf("sanitizeFTS(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSearchMatchesAnyTermInMultiWordQuery is a regression test for the
// implicit-AND bug: a natural-language query whose terms are spread across a
// document (and include terms absent from it) must still match. Under the old
// space-join (implicit AND) this returned 0 results.
func TestSearchMatchesAnyTermInMultiWordQuery(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "decision",
		Title:     "Auth middleware",
		Content:   "Keep auth middleware in project memory",
		Project:   "engram",
		Scope:     "personal",
	}); err != nil {
		t.Fatalf("add observation: %v", err)
	}

	// Only "auth" appears in the document; "database" and "migration" do not.
	// Implicit AND would require all three terms → 0 results.
	results, err := s.Search("auth database migration", SearchOptions{
		Project: "engram",
		Scope:   "personal",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for partial multi-word match, got %d", len(results))
	}
}
