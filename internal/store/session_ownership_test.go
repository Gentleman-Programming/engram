package store

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestEndedLegacyClaimRefreshesPendingSyncMutation(t *testing.T) {
	s := newTestStore(t)
	enrollTestProject(t, s, "project-a")
	const id = "ended-journaled"
	if _, err := s.DB().Exec(`INSERT INTO sessions(id, project, directory, started_at, ended_at) VALUES (?, '', '/legacy', '2024-01-01', '2024-01-02')`, id); err != nil {
		t.Fatal(err)
	}
	// An old pending upsert must be replaced rather than duplicated.
	if _, err := s.DB().Exec(`INSERT INTO sync_mutations(target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`, DefaultSyncTargetKey, SyncEntitySession, id, SyncOpUpsert, `{"id":"ended-journaled","project":"project-a"}`, SyncSourceLocal, "project-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.StartSessionWithOwnershipMode(id, "project-a", "/new", SessionOwnershipProjectOwned); !errors.Is(err, ErrSessionAlreadyEnded) {
		t.Fatalf("claim = %v", err)
	}
	var count int
	var project, mode string
	if err := s.DB().QueryRow(`SELECT count(*), max(project), max(json_extract(payload, '$.ownership_mode')) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND acked_at IS NULL AND disposition = 'pending'`, SyncEntitySession, id).Scan(&count, &project, &mode); err != nil {
		t.Fatal(err)
	}
	if count != 1 || project != "project-a" || mode != SessionOwnershipProjectOwned {
		t.Fatalf("pending mutation count=%d project=%q mode=%q", count, project, mode)
	}
}

func TestEndedLegacyClaimRollsBackOnJournalFailure(t *testing.T) {
	s := newTestStore(t)
	enrollTestProject(t, s, "project-a")
	const id = "ended-journal-error"
	if _, err := s.DB().Exec(`INSERT INTO sessions(id, project, directory, started_at, ended_at) VALUES (?, '', '/legacy', '2024-01-01', '2024-01-02')`, id); err != nil {
		t.Fatal(err)
	}
	original := s.hooks.exec
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO sync_mutations") {
			return nil, errors.New("journal unavailable")
		}
		return original(db, query, args...)
	}
	t.Cleanup(func() { s.hooks.exec = original })
	err := s.StartSessionWithOwnershipMode(id, "project-a", "/new", SessionOwnershipProjectOwned)
	if err == nil || errors.Is(err, ErrSessionAlreadyEnded) || !strings.Contains(err.Error(), "journal unavailable") {
		t.Fatalf("failed journal claim = %v", err)
	}
	var project, mode string
	if err := s.DB().QueryRow(`SELECT ifnull(project, ''), ifnull(ownership_mode, '') FROM sessions WHERE id = ?`, id).Scan(&project, &mode); err != nil {
		t.Fatal(err)
	}
	if project != "" || mode != "" {
		t.Fatalf("rolled-back owner project=%q mode=%q", project, mode)
	}
}

func TestEndedUnownedSessionCannotClaimForeignChild(t *testing.T) {
	for _, kind := range []string{"observation", "prompt"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestStore(t)
			const id = "ended-with-child"
			if _, err := s.DB().Exec(`INSERT INTO sessions(id, project, directory, started_at, ended_at) VALUES (?, '', '', '2024-01-01', '2024-01-02')`, id); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "observation" {
				_, err = s.DB().Exec(`INSERT INTO observations(sync_id, session_id, type, title, content, project, scope) VALUES ('owned-observation', ?, 'note', 'owned', 'content', 'project-a', 'project')`, id)
			} else {
				_, err = s.DB().Exec(`INSERT INTO user_prompts(sync_id, session_id, content, project) VALUES ('owned-prompt', ?, 'content', 'project-a')`, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			var before, after int
			if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			err = s.StartSessionWithOwnershipMode(id, "project-b", "/tmp", SessionOwnershipProjectOwned)
			var conflict *SessionProjectConflictError
			if !errors.As(err, &conflict) || conflict.OwnerProject != "project-a" || conflict.RequestedProject != "project-b" {
				t.Fatalf("foreign claim = %v", err)
			}
			var project string
			if err := s.DB().QueryRow(`SELECT ifnull(project, '') FROM sessions WHERE id = ?`, id).Scan(&project); err != nil {
				t.Fatal(err)
			}
			if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if project != "" || after != before {
				t.Fatalf("foreign claim project=%q mutations=%d before=%d", project, after, before)
			}
			if err := s.StartSessionWithOwnershipMode(id, "project-a", "/tmp", SessionOwnershipProjectOwned); !errors.Is(err, ErrSessionAlreadyEnded) {
				t.Fatalf("matching claim = %v", err)
			}
		})
	}
}

func TestEndedUnownedSessionConcurrentProjectClaim(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	const id = "ended-legacy"
	const ended = "2024-01-02 03:04:05"
	if _, err := a.DB().Exec(`INSERT INTO sessions(id, project, directory, started_at, ended_at) VALUES (?, '', '', ?, ?)`, id, ended, ended); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, 2)
	for i, st := range []*Store{a, b} {
		wg.Add(1)
		go func(i int, st *Store) {
			defer wg.Done()
			<-start
			results[i] = st.StartSessionWithOwnershipMode(id, []string{"project-a", "project-b"}[i], "/tmp", SessionOwnershipProjectOwned)
		}(i, st)
	}
	close(start)
	wg.Wait()
	owner := ""
	for i, err := range results {
		if errors.Is(err, ErrSessionAlreadyEnded) {
			owner = []string{"project-a", "project-b"}[i]
		}
	}
	if owner == "" {
		t.Fatalf("no terminal winner: %v", results)
	}
	loser := "project-a"
	if owner == loser {
		loser = "project-b"
	}
	for i, project := range []string{"project-a", "project-b"} {
		if project == owner {
			continue
		}
		var conflict *SessionProjectConflictError
		if !errors.As(results[i], &conflict) || conflict.OwnerProject != owner || conflict.RequestedProject != loser {
			t.Fatalf("loser error = %v, owner %q", results[i], owner)
		}
	}
	var project, mode, gotEnded string
	if err := b.DB().QueryRow(`SELECT project, ownership_mode, ended_at FROM sessions WHERE id = ?`, id).Scan(&project, &mode, &gotEnded); err != nil {
		t.Fatal(err)
	}
	if project != owner || mode != SessionOwnershipProjectOwned || gotEnded != ended {
		t.Fatalf("persisted: project=%q mode=%q ended=%q", project, mode, gotEnded)
	}
	if err := b.StartSessionWithOwnershipMode(id, loser, "/tmp", SessionOwnershipProjectOwned); !errors.Is(err, ErrSessionOwnershipMismatch) {
		t.Fatalf("repeated loser = %v", err)
	}
	if _, err := b.AddObservation(AddObservationParams{SessionID: id, Project: loser, Type: "manual", Title: "blocked", Content: "blocked", Scope: "project"}); !errors.Is(err, ErrSessionOwnershipMismatch) {
		t.Fatalf("loser observation = %v", err)
	}
	if _, err := b.AddPrompt(AddPromptParams{SessionID: id, Project: loser, Content: "blocked"}); !errors.Is(err, ErrSessionOwnershipMismatch) {
		t.Fatalf("loser prompt = %v", err)
	}
}

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

func TestStrictProjectOwnedRegistrationRejectsSharedSessionProjectConflictWithoutMutation(t *testing.T) {
	s := newTestStore(t)
	const sessionID = "runtime-session"
	if err := s.CreateSession(sessionID, "project-a", "/tmp/a"); err != nil {
		t.Fatalf("create shared session: %v", err)
	}

	if err := s.CreateSessionWithOwnershipMode(sessionID, "project-a", "/tmp/a", SessionOwnershipProjectOwned); err != nil {
		t.Fatalf("same-project strict registration: %v", err)
	}
	var mutationsBefore int
	if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsBefore); err != nil {
		t.Fatalf("count mutations before conflict: %v", err)
	}
	err := s.CreateSessionWithOwnershipMode(sessionID, "project-b", "/tmp/b", SessionOwnershipProjectOwned)
	if !errors.Is(err, ErrSessionOwnershipMismatch) {
		t.Fatalf("strict conflicting registration error = %v, want ErrSessionOwnershipMismatch", err)
	}
	var conflict *SessionProjectConflictError
	if !errors.As(err, &conflict) || conflict.SessionID != sessionID || conflict.OwnerProject != "project-a" || conflict.RequestedProject != "project-b" {
		t.Fatalf("strict conflict = %#v, want structured project-a ownership", conflict)
	}

	session, err := s.GetSession(sessionID)
	if err != nil || session.Project != "project-a" || session.OwnershipMode != SessionOwnershipShared || session.Directory != "/tmp/a" {
		t.Fatalf("session after strict conflict = %#v, %v; want unchanged project-a shared session", session, err)
	}
	var mutationsAfter int
	if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsAfter); err != nil {
		t.Fatalf("count mutations after conflict: %v", err)
	}
	if mutationsAfter != mutationsBefore {
		t.Fatalf("strict conflict changed sync mutations from %d to %d", mutationsBefore, mutationsAfter)
	}

	if err := s.CreateSessionWithOwnershipMode(sessionID, "project-b", "/tmp/b", SessionOwnershipShared); err != nil {
		t.Fatalf("shared registration must remain compatible: %v", err)
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

func TestProjectOwnedSessionRejectsMismatchedPromptWithoutMutation(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSessionWithOwnershipMode("manual-save-project-a", "project-a", "/tmp/a", SessionOwnershipProjectOwned); err != nil {
		t.Fatalf("create project-owned session: %v", err)
	}

	var observationsBefore, promptsBefore, mutationsBefore int
	if err := s.DB().QueryRow(`SELECT count(*) FROM observations`).Scan(&observationsBefore); err != nil {
		t.Fatalf("count observations before mismatch: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM user_prompts`).Scan(&promptsBefore); err != nil {
		t.Fatalf("count prompts before mismatch: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsBefore); err != nil {
		t.Fatalf("count mutations before mismatch: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "manual-save-project-a", Content: "blocked", Project: "project-b"}); !errors.Is(err, ErrSessionOwnershipMismatch) {
		t.Fatalf("mismatched prompt error = %v, want ErrSessionOwnershipMismatch", err)
	}

	var observationsAfter, promptsAfter, mutationsAfter int
	if err := s.DB().QueryRow(`SELECT count(*) FROM observations`).Scan(&observationsAfter); err != nil {
		t.Fatalf("count observations after mismatch: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM user_prompts`).Scan(&promptsAfter); err != nil {
		t.Fatalf("count prompts after mismatch: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsAfter); err != nil {
		t.Fatalf("count mutations after mismatch: %v", err)
	}
	if observationsAfter != observationsBefore || promptsAfter != promptsBefore || mutationsAfter != mutationsBefore {
		t.Fatalf("mismatched prompt wrote observations=%d prompts=%d mutations=%d, want observations=%d prompts=%d mutations=%d", observationsAfter, promptsAfter, mutationsAfter, observationsBefore, promptsBefore, mutationsBefore)
	}
	session, err := s.GetSession("manual-save-project-a")
	if err != nil || session.Project != "project-a" || session.OwnershipMode != SessionOwnershipProjectOwned {
		t.Fatalf("session after mismatched prompt = %#v, %v", session, err)
	}
}

func TestUnclassifiedSessionRejectsMismatchedWritesWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode any
	}{
		{name: "null mode", mode: nil},
		{name: "blank mode", mode: " \t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			const sessionID = "legacy-unclassified"
			if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory, ownership_mode) VALUES (?, ?, ?, ?)`, sessionID, "project-a", "/tmp/a", tc.mode); err != nil {
				t.Fatalf("seed unclassified session: %v", err)
			}

			counts := func() (sessions, observations, prompts, mutations int) {
				t.Helper()
				for table, target := range map[string]*int{
					"sessions":       &sessions,
					"observations":   &observations,
					"user_prompts":   &prompts,
					"sync_mutations": &mutations,
				} {
					if err := s.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(target); err != nil {
						t.Fatalf("count %s: %v", table, err)
					}
				}
				return
			}
			sessionsBefore, observationsBefore, promptsBefore, mutationsBefore := counts()
			sessionBefore, err := s.GetSession(sessionID)
			if err != nil {
				t.Fatalf("read session before strict mismatch: %v", err)
			}

			err = s.CreateSessionWithOwnershipMode(sessionID, "project-b", "/tmp/b", SessionOwnershipProjectOwned)
			if !errors.Is(err, ErrSessionOwnershipMismatch) {
				t.Fatalf("mismatched strict registration error = %v, want ErrSessionOwnershipMismatch", err)
			}
			var conflict *SessionProjectConflictError
			if !errors.As(err, &conflict) || conflict.SessionID != sessionID || conflict.OwnerProject != "project-a" || conflict.RequestedProject != "project-b" {
				t.Fatalf("mismatched strict registration conflict = %#v", conflict)
			}
			if _, err := s.AddObservation(AddObservationParams{SessionID: sessionID, Type: "manual", Title: "blocked", Content: "blocked", Project: "project-b", Scope: "project"}); !errors.Is(err, ErrProjectOwnershipAmbiguous) {
				t.Fatalf("mismatched observation error = %v, want ErrProjectOwnershipAmbiguous", err)
			}
			if _, err := s.AddPrompt(AddPromptParams{SessionID: sessionID, Content: "blocked", Project: "project-b"}); !errors.Is(err, ErrProjectOwnershipAmbiguous) {
				t.Fatalf("mismatched prompt error = %v, want ErrProjectOwnershipAmbiguous", err)
			}
			sessionsAfter, observationsAfter, promptsAfter, mutationsAfter := counts()
			if sessionsAfter != sessionsBefore || observationsAfter != observationsBefore || promptsAfter != promptsBefore || mutationsAfter != mutationsBefore {
				t.Fatalf("rejected mismatches changed sessions=%d observations=%d prompts=%d mutations=%d; want %d %d %d %d", sessionsAfter, observationsAfter, promptsAfter, mutationsAfter, sessionsBefore, observationsBefore, promptsBefore, mutationsBefore)
			}
			sessionAfter, err := s.GetSession(sessionID)
			if err != nil || *sessionAfter != *sessionBefore {
				t.Fatalf("rejected strict registration changed session from %#v to %#v, err=%v", sessionBefore, sessionAfter, err)
			}

			if _, err := s.AddObservation(AddObservationParams{SessionID: sessionID, Type: "manual", Title: "allowed", Content: "allowed", Project: "project-a", Scope: "project"}); err != nil {
				t.Fatalf("same-project observation through unclassified session: %v", err)
			}
		})
	}
}

func TestCreateSessionWithOwnershipModeClassifiesBlankLegacyMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode any
	}{
		{name: "null mode", mode: nil},
		{name: "blank mode", mode: " \t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			const sessionID = "legacy-blank-mode"
			if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory, ownership_mode) VALUES (?, ?, ?, ?)`, sessionID, "project-a", "/tmp/a", tc.mode); err != nil {
				t.Fatalf("seed blank-mode session: %v", err)
			}

			if err := s.CreateSessionWithOwnershipMode(sessionID, "project-a", "/tmp/a", SessionOwnershipProjectOwned); err != nil {
				t.Fatalf("classify blank legacy mode: %v", err)
			}
			session, err := s.GetSession(sessionID)
			if err != nil || session.OwnershipMode != SessionOwnershipProjectOwned {
				t.Fatalf("classified session = %#v, %v; want project-owned", session, err)
			}
		})
	}
}

func TestStartSessionRejectsUnclassifiedProjectMismatchWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode any
	}{
		{name: "null mode", mode: nil},
		{name: "blank mode", mode: " \t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			const sessionID = "legacy-start-unclassified"
			if _, err := s.DB().Exec(`INSERT INTO sessions (id, project, directory, ownership_mode) VALUES (?, ?, ?, ?)`, sessionID, "project-a", "/tmp/a", tc.mode); err != nil {
				t.Fatalf("seed unclassified session: %v", err)
			}
			var mutationsBefore int
			if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsBefore); err != nil {
				t.Fatalf("count mutations before mismatch: %v", err)
			}

			if err := s.StartSession(sessionID, "project-b", "/tmp/b"); !errors.Is(err, ErrProjectOwnershipAmbiguous) {
				t.Fatalf("mismatched StartSession error = %v, want ErrProjectOwnershipAmbiguous", err)
			}
			var mutationsAfter int
			if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations`).Scan(&mutationsAfter); err != nil {
				t.Fatalf("count mutations after mismatch: %v", err)
			}
			if mutationsAfter != mutationsBefore {
				t.Fatalf("mismatched StartSession changed mutations from %d to %d", mutationsBefore, mutationsAfter)
			}
		})
	}
}

func TestSharedSessionAllowsCrossProjectWrites(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("runtime-session", "project-a", "/tmp/a"); err != nil {
		t.Fatalf("create shared session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "runtime-session", Type: "manual", Title: "Cross-project write", Content: "content", Project: "project-b", Scope: "project"}); err != nil {
		t.Fatalf("cross-project write through shared session: %v", err)
	}
	session, err := s.GetSession("runtime-session")
	if err != nil || session.Project != "project-a" || session.OwnershipMode != SessionOwnershipShared {
		t.Fatalf("shared session after cross-project write = %#v, %v", session, err)
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
