package store

import (
	"errors"
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
