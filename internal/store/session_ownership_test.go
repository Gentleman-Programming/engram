package store

import (
	"errors"
	"strings"
	"testing"
)

func TestSessionOwnershipModeCreationAndMigration(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("runtime-session", "project-a", "/tmp/a"); err != nil {
		t.Fatalf("create shared session: %v", err)
	}
	if err := s.CreateSessionWithOwnershipMode("manual-save-project-b", "project-b", "/tmp/b", SessionOwnershipProjectOwned); err != nil {
		t.Fatalf("create project-owned session: %v", err)
	}
	for _, tc := range []struct{ id, want string }{
		{"runtime-session", SessionOwnershipShared},
		{"manual-save-project-b", SessionOwnershipProjectOwned},
	} {
		session, err := s.GetSession(tc.id)
		if err != nil || session.OwnershipMode != tc.want {
			t.Fatalf("session %q = %#v, %v; want mode %q", tc.id, session, err, tc.want)
		}
	}

	if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory, ownership_mode) VALUES
		('manual-save-project-c', 'project-c', '/tmp/c', NULL),
		('manual-save-other', 'project-d', '/tmp/d', NULL),
		('legacy-runtime', 'project-e', '/tmp/e', NULL)`); err != nil {
		t.Fatalf("seed legacy sessions: %v", err)
	}
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate ownership modes: %v", err)
	}
	for _, tc := range []struct{ id, want string }{
		{"manual-save-project-c", SessionOwnershipProjectOwned},
		{"manual-save-other", ""},
		{"legacy-runtime", SessionOwnershipShared},
	} {
		session, err := s.GetSession(tc.id)
		if err != nil || session.OwnershipMode != tc.want {
			t.Fatalf("migrated session %q = %#v, %v; want mode %q", tc.id, session, err, tc.want)
		}
	}
}

func TestCreateSessionWithOwnershipModeRejectsInvalidModesWithoutCreatingSession(t *testing.T) {
	s := newTestStore(t)
	for _, tc := range []struct {
		name string
		mode string
	}{
		{name: "empty", mode: ""},
		{name: "unknown", mode: "exclusive"},
		{name: "wrong case", mode: "SHARED"},
		{name: "surrounding whitespace", mode: " project_owned "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "invalid-mode-" + strings.ReplaceAll(tc.name, " ", "-")
			err := s.CreateSessionWithOwnershipMode(id, "project-a", "/tmp/a", tc.mode)
			if !errors.Is(err, ErrInvalidSessionOwnershipMode) {
				t.Fatalf("CreateSessionWithOwnershipMode(%q) error = %v, want ErrInvalidSessionOwnershipMode", tc.mode, err)
			}
			var count int
			if err := s.DB().QueryRow(`SELECT count(*) FROM sessions WHERE id = ?`, id).Scan(&count); err != nil {
				t.Fatalf("count session rows: %v", err)
			}
			if count != 0 {
				t.Fatalf("invalid mode %q created %d session row(s)", tc.mode, count)
			}
		})
	}
}

func TestProjectOwnedSessionRejectsMismatchedWriteWithoutMutation(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSessionWithOwnershipMode("manual-save-project-a", "project-a", "/tmp/a", SessionOwnershipProjectOwned); err != nil {
		t.Fatalf("create project-owned session: %v", err)
	}
	_, err := s.AddObservation(AddObservationParams{SessionID: "manual-save-project-a", Type: "manual", Title: "Rejected write", Content: "content", Project: "project-b", Scope: "project"})
	if !errors.Is(err, ErrSessionOwnershipMismatch) {
		t.Fatalf("mismatched write error = %v, want ErrSessionOwnershipMismatch", err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM observations WHERE session_id = ?`, "manual-save-project-a").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected write observations = %d, %v; want 0", count, err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "manual-save-project-a", Type: "manual", Title: "Inherited write", Content: "content", Scope: "project"}); err != nil {
		t.Fatalf("inherited project write: %v", err)
	}
	observations, err := s.RecentObservations("project-a", "project", 1)
	if err != nil || len(observations) != 1 || observations[0].Project == nil || *observations[0].Project != "project-a" {
		t.Fatalf("inherited observation = %#v, %v; want persisted project-a", observations, err)
	}
}

func TestImportedLegacyModeDoesNotDowngradeProjectOwnedSession(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSessionWithOwnershipMode("manual-save-project-a", "project-a", "/tmp/a", SessionOwnershipProjectOwned); err != nil {
		t.Fatalf("create project-owned session: %v", err)
	}
	if _, err := s.Import(&ExportData{Sessions: []Session{{ID: "manual-save-project-a", Project: "project-a", Directory: "/tmp/a"}}}); err != nil {
		t.Fatalf("import legacy session: %v", err)
	}
	session, err := s.GetSession("manual-save-project-a")
	if err != nil || session.OwnershipMode != SessionOwnershipProjectOwned {
		t.Fatalf("imported session = %#v, %v; want project-owned", session, err)
	}
}

func TestPulledSessionPayloadPreservesProjectOwnedProject(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSessionWithOwnershipMode("manual-save-project-a", "project-a", "/tmp/a", SessionOwnershipProjectOwned); err != nil {
		t.Fatalf("create project-owned session: %v", err)
	}
	mutation := SyncMutation{
		Seq:       1,
		Entity:    SyncEntitySession,
		EntityKey: "manual-save-project-a",
		Op:        SyncOpUpsert,
		Payload:   `{"id":"manual-save-project-a","project":"project-b","ownership_mode":"shared","directory":"/tmp/b"}`,
	}
	if err := s.ApplyPulledMutation(LocalChunkTargetKey, mutation); err != nil {
		t.Fatalf("apply conflicting session payload: %v", err)
	}
	session, err := s.GetSession("manual-save-project-a")
	if err != nil || session.Project != "project-a" || session.OwnershipMode != SessionOwnershipProjectOwned {
		t.Fatalf("session after pulled payload = %#v, %v; want project-owned project-a", session, err)
	}
	_, err = s.AddObservation(AddObservationParams{SessionID: session.ID, Type: "manual", Title: "must reject", Content: "content", Project: "project-b", Scope: "project"})
	if !errors.Is(err, ErrSessionOwnershipMismatch) {
		t.Fatalf("write after conflicting payload error = %v, want ErrSessionOwnershipMismatch", err)
	}
}

func TestPulledLegacySessionPayloadPreservesSharedOwnershipMode(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("shared-session", "project-a", "/tmp/a"); err != nil {
		t.Fatalf("create shared session: %v", err)
	}
	if err := s.ApplyPulledMutation(LocalChunkTargetKey, SyncMutation{Seq: 1, Entity: SyncEntitySession, EntityKey: "shared-session", Op: SyncOpUpsert, Payload: `{"id":"shared-session","project":"project-a","directory":"/tmp/legacy"}`}); err != nil {
		t.Fatalf("apply legacy session payload: %v", err)
	}
	session, err := s.GetSession("shared-session")
	if err != nil || session.OwnershipMode != SessionOwnershipShared {
		t.Fatalf("session after legacy payload = %#v, %v; want shared", session, err)
	}
}

func TestPulledSessionPayloadOwnershipModeValidation(t *testing.T) {
	t.Run("invalid mode leaves no row or sync state", func(t *testing.T) {
		s := newTestStore(t)
		err := s.ApplyPulledMutation(LocalChunkTargetKey, SyncMutation{
			Seq:       1,
			Entity:    SyncEntitySession,
			EntityKey: "invalid-pulled-session",
			Op:        SyncOpUpsert,
			Payload:   `{"id":"invalid-pulled-session","project":"project-a","ownership_mode":"exclusive","directory":"/tmp/a"}`,
		})
		if !errors.Is(err, ErrInvalidSessionOwnershipMode) {
			t.Fatalf("ApplyPulledMutation invalid ownership mode error = %v, want ErrInvalidSessionOwnershipMode", err)
		}

		var sessionCount, stateCount int
		if err := s.DB().QueryRow(`SELECT count(*) FROM sessions WHERE id = ?`, "invalid-pulled-session").Scan(&sessionCount); err != nil {
			t.Fatalf("count invalid session rows: %v", err)
		}
		if err := s.DB().QueryRow(`SELECT count(*) FROM sync_state WHERE target_key = ?`, LocalChunkTargetKey).Scan(&stateCount); err != nil {
			t.Fatalf("count local sync state rows: %v", err)
		}
		if sessionCount != 0 || stateCount != 0 {
			t.Fatalf("invalid payload left sessionCount=%d stateCount=%d, want both 0", sessionCount, stateCount)
		}
	})

	t.Run("omitted mode creates shared session", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.ApplyPulledMutation(LocalChunkTargetKey, SyncMutation{
			Seq:       1,
			Entity:    SyncEntitySession,
			EntityKey: "legacy-shared-session",
			Op:        SyncOpUpsert,
			Payload:   `{"id":"legacy-shared-session","project":"project-a","directory":"/tmp/a"}`,
		}); err != nil {
			t.Fatalf("apply legacy session payload: %v", err)
		}
		session, err := s.GetSession("legacy-shared-session")
		if err != nil || session.OwnershipMode != SessionOwnershipShared {
			t.Fatalf("legacy session = %#v, %v; want shared", session, err)
		}
	})
}

func TestImportedSessionOwnershipModeValidation(t *testing.T) {
	t.Run("invalid mode leaves every import entity unchanged", func(t *testing.T) {
		s := newTestStore(t)
		_, err := s.Import(&ExportData{
			Sessions: []Session{{ID: "invalid-import-session", Project: "project-a", OwnershipMode: "exclusive", Directory: "/tmp/a"}},
			Observations: []Observation{{
				SyncID:    "invalid-import-observation",
				SessionID: "invalid-import-session",
				Type:      "manual",
				Title:     "blocked",
				Content:   "blocked",
				Scope:     "project",
			}},
			Prompts: []Prompt{{SyncID: "invalid-import-prompt", SessionID: "invalid-import-session", Content: "blocked"}},
		})
		if !errors.Is(err, ErrInvalidSessionOwnershipMode) {
			t.Fatalf("Import invalid ownership mode error = %v, want ErrInvalidSessionOwnershipMode", err)
		}

		for _, tc := range []struct {
			table string
			key   string
			value string
		}{
			{table: "sessions", key: "id", value: "invalid-import-session"},
			{table: "observations", key: "sync_id", value: "invalid-import-observation"},
			{table: "user_prompts", key: "sync_id", value: "invalid-import-prompt"},
		} {
			var count int
			if err := s.DB().QueryRow(`SELECT count(*) FROM `+tc.table+` WHERE `+tc.key+` = ?`, tc.value).Scan(&count); err != nil {
				t.Fatalf("count %s rows: %v", tc.table, err)
			}
			if count != 0 {
				t.Fatalf("invalid import created %d %s row(s), want 0", count, tc.table)
			}
		}
	})

	t.Run("omitted mode imports as shared", func(t *testing.T) {
		s := newTestStore(t)
		if _, err := s.Import(&ExportData{Sessions: []Session{{ID: "legacy-import-session", Project: "project-a", Directory: "/tmp/a"}}}); err != nil {
			t.Fatalf("import legacy session: %v", err)
		}
		session, err := s.GetSession("legacy-import-session")
		if err != nil || session.OwnershipMode != SessionOwnershipShared {
			t.Fatalf("legacy imported session = %#v, %v; want shared", session, err)
		}
	})
}
