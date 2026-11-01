package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPassiveCaptureTitlePreservesUTF8(t *testing.T) {
	prefix := func(bytes int) string { return "one two three " + strings.Repeat("a", bytes-len("one two three ")) }
	for _, tt := range []struct {
		name, content, title string
	}{
		{"issue-example", "PRUEBA-UTF8-20261029: la sesión de prueba añade tildes, eñes y una flecha → para verificar la codificación del hook.", "PRUEBA-UTF8-20261029: la sesión de prueba añade tildes, e..."},
		{"two-byte-crossing", prefix(59) + "ñmore", prefix(59) + "..."},
		{"three-byte-crossing", prefix(58) + "→more", prefix(58) + "..."},
		{"four-byte-crossing", prefix(57) + "🧠more", prefix(57) + "..."},
		{"rune-ends-at-limit", prefix(58) + "ñmore", prefix(58) + "ñ..."},
		{"short-unicode", "La sesión añade eñes → 🧠", "La sesión añade eñes → 🧠"},
		{"short-ascii", "A short useful learning", "A short useful learning"},
		{"ascii-at-limit", prefix(60), prefix(60)},
		{"ascii-over-limit", prefix(61), prefix(60) + "..."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("passive", "target", t.TempDir()); err != nil {
				t.Fatal(err)
			}
			params := PassiveCaptureParams{SessionID: "passive", Project: "target", Source: "test", Content: "## Key Learnings\n1. " + tt.content}
			result, err := s.PassiveCapture(params)
			if err != nil || result.Saved != 1 || result.Extracted != 1 {
				t.Fatalf("capture = %+v, %v", result, err)
			}
			duplicate, err := s.PassiveCapture(params)
			if err != nil || duplicate.Saved != 0 || duplicate.Duplicates != 1 {
				t.Fatalf("duplicate capture = %+v, %v", duplicate, err)
			}
			check := func(s *Store) {
				t.Helper()
				observations, err := s.AllObservations("target", "project", 10)
				if err != nil || len(observations) != 1 {
					t.Fatalf("observations = %+v, %v", observations, err)
				}
				observation, err := s.GetObservation(observations[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				if !utf8.ValidString(observation.Title) || observation.Title != tt.title {
					t.Errorf("title = %q (%x), want %q", observation.Title, []byte(observation.Title), tt.title)
				}
				if observation.Content != tt.content || observation.Type != "passive" || observation.SessionID != "passive" {
					t.Errorf("capture content or attribution changed: %+v", observation)
				}
				stats, err := s.StatsProject("target")
				if err != nil || stats.TotalSessions != 1 || stats.TotalObservations != 1 {
					t.Fatalf("stats = %+v, %v", stats, err)
				}
			}
			check(s)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := New(s.cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			check(reopened)
		})
	}
}
