package store

import (
	"fmt"
	"slices"
	"testing"
)

func TestHasObservationsForProjectMatchesListedNames(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("exists-fixture", "alpha", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for i, row := range []struct {
		name    string
		deleted bool
	}{
		{"alpha", false}, {"alpha", false}, {"deleted-only", true},
		{"LegacyName", false}, {"cafe\u0301", false}, {"literal%_name", false}, {"", false},
	} {
		var deleted any
		if row.deleted {
			deleted = "2026-01-01 00:00:00"
		}
		if _, err := s.db.Exec(`INSERT INTO observations(session_id,type,title,content,project,deleted_at)
			VALUES('exists-fixture','decision',?,'synthetic content',?,?)`, fmt.Sprintf("fixture-%d", i), row.name, deleted); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSession("session-only", "session-only", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO user_prompts(session_id,content,project) VALUES('exists-fixture','synthetic prompt','prompt-only')`); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("enrolled-only"); err != nil {
		t.Fatal(err)
	}
	names, err := s.ListProjectNames()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "missing", "deleted-only", "session-only", "prompt-only", "enrolled-only", "LegacyName", "legacyname", "cafe\u0301", "café", "literal%_name", "literalXXname", ""} {
		t.Run(name, func(t *testing.T) {
			got, err := s.HasObservationsForProject(name)
			if err != nil {
				t.Fatal(err)
			}
			if want := slices.Contains(names, name); got != want {
				t.Errorf("exists=%t want ListProjectNames membership=%t", got, want)
			}
		})
	}
}

func TestHasObservationsForProjectReflectsChanges(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("changes", "alpha", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddObservation(AddObservationParams{SessionID: "changes", Project: "alpha", Type: "decision", Title: "lifecycle", Content: "synthetic lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	if found, err := s.HasObservationsForProject("alpha"); err != nil || !found {
		t.Fatalf("live=%t %v", found, err)
	}
	if err := s.DeleteObservation(id, false); err != nil {
		t.Fatal(err)
	}
	if found, err := s.HasObservationsForProject("alpha"); err != nil || found {
		t.Fatalf("deleted=%t %v", found, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if found, err := s.HasObservationsForProject("alpha"); err == nil || found {
		t.Fatalf("closed=%t %v, want false/error", found, err)
	}
}
