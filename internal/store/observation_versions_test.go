package store

import (
	"errors"
	"fmt"
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

// seedVersionHistory creates an observation with versions whose contents are
// "state-00" ... "state-N", and returns the observation id.
func seedVersionHistory(t *testing.T, s *Store, versionCount int) int64 {
	t.Helper()
	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	var id int64
	var err error
	for i := 0; i <= versionCount; i++ {
		id, err = s.AddObservation(AddObservationParams{
			SessionID: "s1",
			Type:      "architecture",
			Title:     "Auth architecture",
			Content:   fmt.Sprintf("state-%02d", i),
			Project:   "engram",
			Scope:     "project",
			TopicKey:  "architecture/auth-model",
		})
		if err != nil {
			t.Fatalf("add state %d: %v", i, err)
		}
	}
	return id
}

func TestGetObservationVersionPageBoundsToMostRecent(t *testing.T) {
	s := newTestStore(t)
	id := seedVersionHistory(t, s, 60)

	page, err := s.GetObservationVersionPage(id, DefaultObservationVersionPageSize, 0)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if page.Total != 60 {
		t.Fatalf("total = %d, want 60", page.Total)
	}
	if !page.HasMore {
		t.Fatalf("expected HasMore when older versions exist")
	}
	if page.NextCursor != 11 {
		t.Fatalf("next cursor = %d, want 11", page.NextCursor)
	}
	if len(page.Versions) != DefaultObservationVersionPageSize {
		t.Fatalf("page length = %d, want %d", len(page.Versions), DefaultObservationVersionPageSize)
	}
	for i, v := range page.Versions {
		wantVersion := i + 11
		if v.Version != wantVersion {
			t.Fatalf("versions[%d].Version = %d, want %d", i, v.Version, wantVersion)
		}
		// Capture stores the pre-overwrite content: version N holds the state
		// saved N saves before the current one (state-(N-1)).
		wantContent := fmt.Sprintf("state-%02d", wantVersion-1)
		if v.Content != wantContent {
			t.Fatalf("versions[%d].Content = %q, want %q", i, v.Content, wantContent)
		}
	}

	older, err := s.GetObservationVersionPage(id, DefaultObservationVersionPageSize, page.NextCursor)
	if err != nil {
		t.Fatalf("older page: %v", err)
	}
	if older.Total != 60 {
		t.Fatalf("older total = %d, want 60", older.Total)
	}
	if older.HasMore {
		t.Fatalf("expected no continuation after the final page")
	}
	if older.NextCursor != 0 {
		t.Fatalf("final page cursor = %d, want 0", older.NextCursor)
	}
	if len(older.Versions) != 10 {
		t.Fatalf("older page length = %d, want 10", len(older.Versions))
	}
	for i, v := range older.Versions {
		wantVersion := i + 1
		if v.Version != wantVersion {
			t.Fatalf("older versions[%d].Version = %d, want %d", i, v.Version, wantVersion)
		}
	}
}

func TestGetObservationVersionPageFitsSinglePageHasNoCursor(t *testing.T) {
	s := newTestStore(t)
	id := seedVersionHistory(t, s, 2)

	page, err := s.GetObservationVersionPage(id, DefaultObservationVersionPageSize, 0)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("total = %d, want 2", page.Total)
	}
	if page.HasMore {
		t.Fatalf("2 versions must fit one page without a continuation cursor")
	}
	if page.NextCursor != 0 {
		t.Fatalf("cursor = %d, want 0 when nothing remains", page.NextCursor)
	}
	if len(page.Versions) != 2 || page.Versions[0].Version != 1 || page.Versions[1].Version != 2 {
		t.Fatalf("unexpected page: %+v", page.Versions)
	}
}

func TestGetObservationVersionPageEmptyForUntouchedObservation(t *testing.T) {
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

	page, err := s.GetObservationVersionPage(id, DefaultObservationVersionPageSize, 0)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.Total != 0 || page.HasMore || len(page.Versions) != 0 || page.NextCursor != 0 {
		t.Fatalf("untouched observation page = %+v, want empty page without continuation", page)
	}
}

func TestGetObservationVersionPageValidatesLimitAndCursor(t *testing.T) {
	s := newTestStore(t)
	id := seedVersionHistory(t, s, 3)

	for _, limit := range []int{0, -1, MaxObservationVersionPageSize + 1} {
		if _, err := s.GetObservationVersionPage(id, limit, 0); err == nil {
			t.Fatalf("limit %d: expected validation error", limit)
		}
	}
	if _, err := s.GetObservationVersionPage(id, 10, -1); err == nil {
		t.Fatal("negative cursor: expected validation error")
	}
}

func TestGetObservationVersionPagePropagatesQueryFailure(t *testing.T) {
	s := newTestStore(t)
	id := seedVersionHistory(t, s, 1)

	wantErr := errors.New("history query failed")
	oldQueryIt := s.hooks.queryIt
	s.hooks.queryIt = func(queryer, string, ...any) (rowScanner, error) {
		return nil, wantErr
	}
	t.Cleanup(func() { s.hooks.queryIt = oldQueryIt })

	if _, err := s.GetObservationVersionPage(id, DefaultObservationVersionPageSize, 0); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestTitleOnlyUpdateCapturesPriorTitleAndContent(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Old title",
		Content:   "Old body.",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}

	newTitle := "New title"
	if _, err := s.UpdateObservation(id, UpdateObservationParams{Title: &newTitle}); err != nil {
		t.Fatalf("title-only update: %v", err)
	}

	page, err := s.GetObservationVersionPage(id, DefaultObservationVersionPageSize, 0)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.Total != 1 || len(page.Versions) != 1 {
		t.Fatalf("expected exactly 1 version after title-only update, got %d total / %d rows", page.Total, len(page.Versions))
	}
	v := page.Versions[0]
	if v.Title != "Old title" {
		t.Fatalf("captured version title = %q, want prior title %q", v.Title, "Old title")
	}
	if v.Content != "Old body." {
		t.Fatalf("captured version content = %q, want prior content %q", v.Content, "Old body.")
	}
}
