package store

import (
	"testing"
)

// historicalContent returns the content recorded by the most recent capture
// with the given version number, or "" when no such version exists.
func historicalContent(t *testing.T, s *Store, id int64, version int) string {
	t.Helper()
	versions, err := s.GetObservationVersions(id)
	if err != nil {
		t.Fatalf("get observation versions: %v", err)
	}
	for _, v := range versions {
		if v.Version == version {
			return v.Content
		}
	}
	return ""
}

func TestTopicKeyUpsertCapturesPreviousState(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Use middleware for JWT validation.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add first architecture: %v", err)
	}

	// The second save with the same topic_key rewrites the observation in
	// place. The previous title/content must be preserved as version 1.
	_, err = s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Move auth to gateway + middleware chain.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("upsert architecture: %v", err)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get upserted observation: %v", err)
	}
	if obs.RevisionCount != 2 {
		t.Fatalf("expected revision_count=2, got %d", obs.RevisionCount)
	}

	versions, err := s.GetObservationVersions(id)
	if err != nil {
		t.Fatalf("get observation versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected 1 version after one upsert, got %d", len(versions))
	}
	v := versions[0]
	if v.Version != 1 {
		t.Fatalf("expected first version number 1, got %d", v.Version)
	}
	if v.Content != "Use middleware for JWT validation." {
		t.Fatalf("expected previous content captured, got %q", v.Content)
	}
	if v.Title != "Auth architecture" {
		t.Fatalf("expected previous title captured, got %q", v.Title)
	}
	if v.ObservationID != id {
		t.Fatalf("expected version bound to observation %d, got %d", id, v.ObservationID)
	}
}

func TestRepeatedTopicKeyUpsertsIncrementVersions(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	states := []string{"state-a", "state-b", "state-c"}
	var id int64
	var err error
	for _, content := range states {
		id, err = s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "decision",
			Title:     "Evolving decision",
			Content:   content,
			Project:   "engram",
			Scope:     "project",
			TopicKey:  "architecture/auth-model",
		})
		if err != nil {
			t.Fatalf("add state %q: %v", content, err)
		}
	}

	versions, err := s.GetObservationVersions(id)
	if err != nil {
		t.Fatalf("get observation versions: %v", err)
	}
	if len(versions) != len(states)-1 {
		t.Fatalf("expected %d versions, got %d", len(states)-1, len(versions))
	}
	for i, want := range []string{"state-a", "state-b"} {
		got := historicalContent(t, s, id, i+1)
		if got != want {
			t.Fatalf("version %d content = %q, want %q", i+1, got, want)
		}
	}
}

func TestUpdateObservationContentChangeCapturesPreviousState(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Auth token handling",
		Content:   "Tokens are validated in middleware.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	content := "Tokens are validated twice: middleware + gateway."
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Content: &content}); err != nil {
		t.Fatalf("update content: %v", err)
	}

	versions, err := s.GetObservationVersions(id)
	if err != nil {
		t.Fatalf("get observation versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected 1 version after content update, got %d", len(versions))
	}
	if versions[0].Content != "Tokens are validated in middleware." {
		t.Fatalf("expected previous content captured, got %q", versions[0].Content)
	}
}

func TestUpdateObservationMetadataOnlyDoesNotCapture(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Auth token handling",
		Content:   "Tokens are validated in middleware.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	typ := "decision"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Type: &typ}); err != nil {
		t.Fatalf("update type: %v", err)
	}

	versions, err := s.GetObservationVersions(id)
	if err != nil {
		t.Fatalf("get observation versions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("expected no versions for metadata-only update, got %d", len(versions))
	}
}

func TestGetObservationVersionsEmptyForUntouchedObservation(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "Use middleware for JWT validation.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	versions, err := s.GetObservationVersions(id)
	if err != nil {
		t.Fatalf("get observation versions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("expected no versions for untouched observation, got %d", len(versions))
	}
}

func TestObservationVersionsSurviveSoftDeleteAndAreOrdered(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "JWT only.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add first: %v", err)
	}
	for _, content := range []string{"JWT + refresh rotation.", "OAuth2 via gateway."} {
		if _, err := s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "architecture",
			Title:     "Auth architecture",
			Content:   content,
			Project:   "engram",
			Scope:     "project",
			TopicKey:  "architecture/auth-model",
		}); err != nil {
			t.Fatalf("upsert %q: %v", content, err)
		}
	}

	versions, err := s.GetObservationVersions(id)
	if err != nil {
		t.Fatalf("get observation versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}
	for i, want := range []string{"JWT only.", "JWT + refresh rotation."} {
		if versions[i].Version != i+1 {
			t.Fatalf("version[%d].Version = %d, want %d", i, versions[i].Version, i+1)
		}
		if versions[i].Content != want {
			t.Fatalf("version[%d].Content = %q, want %q", i, versions[i].Content, want)
		}
	}

	// History must survive soft delete: GetObservationVersions reads the
	// version table directly, not through the active-observation view.
	if err := s.DeleteObservation(id, false); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	afterDelete, err := s.GetObservationVersions(id)
	if err != nil {
		t.Fatalf("get observation versions after soft delete: %v", err)
	}
	if len(afterDelete) != 2 {
		t.Fatalf("expected history to survive soft delete (2 versions), got %d", len(afterDelete))
	}
}

func TestHardDeleteRemovesObservationVersions(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "JWT only.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	})
	if err != nil {
		t.Fatalf("add first: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "architecture",
		Title:     "Auth architecture",
		Content:   "OAuth2 via gateway.",
		Project:   "engram",
		Scope:     "project",
		TopicKey:  "architecture/auth-model",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	before, err := s.GetObservationVersions(id)
	if err != nil {
		t.Fatalf("get observation versions: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("expected 1 version before hard delete, got %d", len(before))
	}

	// Hard delete physically removes the observation; its immutable versions
	// leave with it (ON DELETE CASCADE) so no orphaned history remains.
	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	after, err := s.GetObservationVersions(id)
	if err != nil {
		t.Fatalf("get observation versions after hard delete: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected no versions after hard delete, got %d", len(after))
	}
}