package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRegisterIsolatedSession(t *testing.T) {
	s := newTestStore(t)
	id, err := s.RegisterIsolatedSession("satellite", "target", true)
	if err != nil || id != "satellite" {
		t.Fatalf("new registration = %q, %v", id, err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET runtime_lease_expires_at = '2000-01-01 00:00:00' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterIsolatedSession(id, "target", true); err != nil {
		t.Fatal(err)
	}
	session, err := s.GetSession(id)
	if err != nil || session.Directory != "" || session.OwnershipMode != SessionOwnershipProjectOwned {
		t.Fatalf("isolated session = %#v, %v", session, err)
	}
	var renewed bool
	if err := s.db.QueryRow(`SELECT datetime(runtime_lease_expires_at) > datetime('now') FROM sessions WHERE id = ?`, id).Scan(&renewed); err != nil || !renewed {
		t.Fatalf("lease renewed = %t, %v", renewed, err)
	}
	if err := s.EndSession(id, "terminal"); err != nil {
		t.Fatal(err)
	}
	continuation, err := s.RegisterIsolatedSession(id, "target", true)
	if err != nil || continuation != id+":resume:2" {
		t.Fatalf("continuation = %q, %v", continuation, err)
	}
	var conflict *SessionProjectConflictError
	if _, err := s.RegisterIsolatedSession(id, "other", true); !errors.As(err, &conflict) {
		t.Fatalf("foreign ownership not rejected: %v", err)
	}
	session, err = s.GetSession(continuation)
	if err != nil || session.Directory != "" || session.Project != "target" {
		t.Fatalf("continuation = %#v, %v", session, err)
	}
}

// Snapshot every persisted field, including lease/ownership and complete journal rows.
func isolatedSnapshot(t *testing.T, s *Store) map[string][][]any {
	t.Helper()
	result := make(map[string][][]any)
	for _, table := range []string{"sessions", "sync_mutations"} {
		rows, err := s.db.Query("SELECT * FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			result[table] = append(result[table], values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func TestRegisterIsolatedSessionRejectedContinuationPreservesAllState(t *testing.T) {
	for _, ownerless := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "ownerless"}[ownerless], func(t *testing.T) {
			s := newTestStore(t)
			if _, err := s.RegisterIsolatedSession("satellite", "target", true); err != nil {
				t.Fatal(err)
			}
			if err := s.EndSession("satellite", "terminal"); err != nil {
				t.Fatal(err)
			}
			if err := s.StartSessionWithOwnershipMode("satellite:resume:2", "target", "/runtime", SessionOwnershipProjectOwned); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE sessions SET runtime_lease_expires_at = '2000-01-01 00:00:00' WHERE id = 'satellite:resume:2'`); err != nil {
				t.Fatal(err)
			}
			if ownerless {
				if _, err := s.db.Exec(`UPDATE sessions SET project = '', ownership_mode = '' WHERE id = 'satellite:resume:2'`); err != nil {
					t.Fatal(err)
				}
			}
			before := isolatedSnapshot(t, s)
			if _, err := s.RegisterIsolatedSession("satellite", "target", true); !errors.Is(err, ErrSessionIsolationConflict) {
				t.Fatalf("expected isolation conflict: %v", err)
			}
			if after := isolatedSnapshot(t, s); !reflect.DeepEqual(before, after) {
				t.Fatalf("rejection mutated sessions/leases/ownership/journal: %#v -> %#v", before, after)
			}
		})
	}
}

// publicRegistrationState captures registration effects only through public
// interfaces: every listed session (lease included), the pending journal and the
// aggregate counters.
type publicRegistrationState struct {
	Sessions map[string]*Session
	Journal  []SyncMutation
	Stats    *Stats
}

func capturePublicRegistrationState(t *testing.T, s *Store, ids ...string) publicRegistrationState {
	t.Helper()
	state := publicRegistrationState{Sessions: make(map[string]*Session)}
	for _, id := range ids {
		session, err := s.GetSession(id)
		if err != nil {
			session = nil
		}
		state.Sessions[id] = session
	}
	journal, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 1000)
	if err != nil {
		t.Fatal(err)
	}
	state.Journal = journal
	if state.Stats, err = s.Stats(); err != nil {
		t.Fatal(err)
	}
	return state
}

// A shared row, whatever produced it, is not an isolated identity: isolated
// registration must refuse it before any lease, ownership repair or journal write.
func TestRegisterIsolatedSessionRejectsExistingSharedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resumes []bool
		seed    func(t *testing.T, s *Store)
	}{
		{name: "live shared root", resumes: []bool{true, false}, seed: func(t *testing.T, s *Store) {
			if err := s.StartSession("satellite", "target", ""); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "ended shared root", resumes: []bool{true, false}, seed: func(t *testing.T, s *Store) {
			if err := s.StartSession("satellite", "target", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.EndSession("satellite", "terminal"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "shared continuation", resumes: []bool{true}, seed: func(t *testing.T, s *Store) {
			if _, err := s.RegisterIsolatedSession("satellite", "target", true); err != nil {
				t.Fatal(err)
			}
			if err := s.EndSession("satellite", "terminal"); err != nil {
				t.Fatal(err)
			}
			if err := s.StartSession("satellite:resume:2", "target", ""); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newTestStore(t)
			if err := s.EnrollProject("target"); err != nil {
				t.Fatal(err)
			}
			tc.seed(t, s)
			ids := []string{"satellite", "satellite:resume:2", "satellite:resume:3"}
			before := capturePublicRegistrationState(t, s, ids...)
			// Lease renewal has one-second resolution; let a renewal become observable.
			time.Sleep(1100 * time.Millisecond)
			for _, resume := range tc.resumes {
				if _, err := s.RegisterIsolatedSession("satellite", "target", resume); !errors.Is(err, ErrSessionIsolationConflict) {
					t.Fatalf("resume=%t: expected isolation conflict, got %v", resume, err)
				}
			}
			if after := capturePublicRegistrationState(t, s, ids...); !reflect.DeepEqual(before, after) {
				t.Fatalf("rejection mutated session/lease/owner/journal/counters:\nbefore=%#v\nafter=%#v", before, after)
			}
		})
	}
}

// Non-isolated shared registration and adoption of an unclassified blank
// identity keep their existing behavior.
func TestSharedIsolationGuardPreservesOtherRegistrations(t *testing.T) {
	t.Run("non-isolated shared registration", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.StartSession("runtime", "target", ""); err != nil {
			t.Fatal(err)
		}
		if err := s.StartSession("runtime", "target", "/repos/runtime"); err != nil {
			t.Fatalf("shared re-registration: %v", err)
		}
		if id, err := s.ResumeSessionWithOwnershipMode("runtime", "target", "/repos/runtime", SessionOwnershipShared); err != nil || id != "runtime" {
			t.Fatalf("shared resume = %q, %v", id, err)
		}
		session, err := s.GetSession("runtime")
		if err != nil || session.OwnershipMode != SessionOwnershipShared || session.Directory != "/repos/runtime" {
			t.Fatalf("shared session = %#v, %v", session, err)
		}
	})
	// Structural legacy-fixture evidence only, not public proof: no public API
	// creates a row with blank project and blank ownership mode, so the fixture
	// is written directly. Public adoption of such rows remains unproven.
	t.Run("structural legacy fixture: unclassified blank identity adoption", func(t *testing.T) {
		s := newTestStore(t)
		if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory, ownership_mode) VALUES ('satellite', '', '', '')`); err != nil {
			t.Fatal(err)
		}
		if id, err := s.RegisterIsolatedSession("satellite", "target", true); err != nil || id != "satellite" {
			t.Fatalf("adoption = %q, %v", id, err)
		}
		session, err := s.GetSession("satellite")
		if err != nil || session.Project != "target" || session.OwnershipMode != SessionOwnershipProjectOwned || session.Directory != "" {
			t.Fatalf("adopted session = %#v, %v", session, err)
		}
	})
}

func TestRegisterIsolatedSessionRefusesRuntimeBoundLegacyIdentity(t *testing.T) {
	for _, ended := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "ended"}[ended], func(t *testing.T) {
			s := newTestStore(t)
			// A legacy ownerless identity must not be repaired before isolation validation.
			if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory, ownership_mode, runtime_lease_expires_at) VALUES ('satellite', '', '/runtime', '', '2000-01-01 00:00:00')`); err != nil {
				t.Fatal(err)
			}
			if ended {
				if err := s.EndSession("satellite", "terminal"); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.GetSession("satellite")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.RegisterIsolatedSession("satellite", "target", true); !errors.Is(err, ErrSessionIsolationConflict) {
				t.Fatalf("expected isolation conflict: %v", err)
			}
			after, err := s.GetSession("satellite")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejection repaired/renewed identity: %#v -> %#v", before, after)
			}
			if _, err := s.GetSession("satellite:resume:2"); err == nil {
				t.Fatal("rejection created continuation")
			}
		})
	}
}
