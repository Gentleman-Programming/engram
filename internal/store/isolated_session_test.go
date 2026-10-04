package store

import (
	"errors"
	"reflect"
	"testing"
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
