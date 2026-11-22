package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRuntimeSessionResolveSelectsLiveOrdinalsWithoutAllocating(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t)
	const root, project = "host%_", "fixture-project"
	ended := []string{root, root + ":resume:2", root + ":resume:99999999999999999999"}
	live := []string{root + ":resume:3", root + ":resume:0003", root + ":resume:3extra"}
	for _, id := range append(ended, live...) {
		if err := s.StartSession(id, project, dir); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ended {
		if err := s.EndSession(id, "ended fixture"); err != nil {
			t.Fatal(err)
		}
	}
	// Equal numeric ordinals keep the existing literal ordering. A nonnumeric
	// suffix does not qualify as a continuation or affect future allocation.
	for _, expected := range []string{root + ":resume:0003", root + ":resume:3"} {
		id, err := s.ResolveRuntimeSessionWithOwnershipModeContext(t.Context(), root, project, dir, SessionOwnershipShared)
		if err != nil || id != expected {
			t.Fatalf("resolved %q, want %q: %v", id, expected, err)
		}
		id, err = s.EndRuntimeSessionIfCurrent(root, project, dir, SessionOwnershipShared, "ended fixture", expected)
		if err != nil || id != expected {
			t.Fatalf("ended %q, want %q: %v", id, expected, err)
		}
	}
	if id, err := s.ResolveRuntimeSessionWithOwnershipModeContext(t.Context(), root, project, dir, SessionOwnershipShared); id != "" || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no live continuation: %q, %v", id, err)
	}
	id, err := s.ResumeSessionWithOwnershipMode(root, project, dir, SessionOwnershipShared)
	if err != nil || id != root+":resume:100000000000000000000" {
		t.Fatalf("resume allocation lost its full history: %q, %v", id, err)
	}
}

func TestRuntimeSessionResolveContextCancelsConnectionWait(t *testing.T) {
	root := t.TempDir()
	s, err := New(FallbackConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession("host", "project", root); err != nil {
		t.Fatal(err)
	}
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	initialWaitCount := s.db.Stats().WaitCount
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.ResolveRuntimeSessionWithOwnershipModeContext(ctx, "host", "project", root, SessionOwnershipShared)
		done <- err
	}()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for s.db.Stats().WaitCount == initialWaitCount {
		select {
		case <-done:
			t.Fatal("resolution stopped before waiting for the only connection")
		case <-ctx.Done():
			t.Fatal("resolution did not wait for the connection")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("resolve = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled resolution did not release its connection wait")
	}
}

func TestRuntimeSessionScopeAndConcurrency(t *testing.T) {
	root := t.TempDir()
	s, err := New(FallbackConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	const project = "runtime-project"
	for _, host := range []string{"one", "two"} {
		if err := s.CreateSession(host, project, root); err != nil {
			t.Fatal(err)
		}
		if err := s.EndSession(host, "terminal root"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResumeSessionWithOwnershipMode(host, project, root, SessionOwnershipShared); err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range []string{"one", "two", "one"} {
		id, err := s.ResolveRuntimeSessionWithOwnershipMode(host, project, filepath.Join(root, "."), SessionOwnershipShared)
		if err != nil || id != host+":resume:2" {
			t.Fatalf("resolve %s = %q, %v", host, id, err)
		}
	}
	for _, selected := range []string{"one", "one:resume:2"} {
		for _, change := range []struct{ name, column, value string }{
			{"project", "project", "other"}, {"mode", "ownership_mode", SessionOwnershipProjectOwned},
			{"directory", "directory", filepath.Join(root, "other")}, {"blank directory", "directory", ""},
		} {
			t.Run(selected+"/"+change.name, func(t *testing.T) {
				requestedMode := SessionOwnershipShared
				if change.name == "project" {
					requestedMode = SessionOwnershipProjectOwned
					if _, err := s.db.Exec(`UPDATE sessions SET ownership_mode = ? WHERE id IN ('one','one:resume:2')`, requestedMode); err != nil {
						t.Fatal(err)
					}
					defer func() {
						_, _ = s.db.Exec(`UPDATE sessions SET ownership_mode = ? WHERE id IN ('one','one:resume:2')`, SessionOwnershipShared)
					}()
				}
				var original string
				if err := s.db.QueryRow("SELECT "+change.column+" FROM sessions WHERE id = ?", selected).Scan(&original); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec("UPDATE sessions SET "+change.column+" = ? WHERE id = ?", change.value, selected); err != nil {
					t.Fatal(err)
				}
				defer func() { _, _ = s.db.Exec("UPDATE sessions SET "+change.column+" = ? WHERE id = ?", original, selected) }()
				if _, err := s.ResolveRuntimeSessionWithOwnershipMode("one", project, root, requestedMode); !errors.Is(err, ErrRuntimeSessionScopeConflict) {
					t.Fatalf("resolve conflict = %v", err)
				}
				if _, err := s.EndRuntimeSession("one", project, root, requestedMode, ""); !errors.Is(err, ErrRuntimeSessionScopeConflict) {
					t.Fatalf("end conflict = %v", err)
				}
			})
		}
	}
	// Historical spelling is compared canonically without rewriting stored state.
	historical := root + string(filepath.Separator) + "."
	if _, err := s.db.Exec(`UPDATE sessions SET directory = ?, project = ?, ownership_mode = ? WHERE id IN ('one','one:resume:2')`, historical, " runtime-project ", " shared "); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveRuntimeSessionWithOwnershipMode("one", project, root, SessionOwnershipShared); err != nil {
		t.Fatal(err)
	}
	session, err := s.GetSession("one")
	if err != nil || session.Directory != historical {
		t.Fatalf("historical scope rewritten: %+v, %v", session, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = s.EndRuntimeSession("one", project, root, SessionOwnershipShared, "")
			} else {
				_, err = s.ResolveRuntimeSessionWithOwnershipMode("one", project, root, SessionOwnershipShared)
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("concurrent operation: %v", err)
			}
		}(i)
	}
	wg.Wait()
	for _, host := range []string{"one", "missing"} {
		if _, err := s.EndRuntimeSession(host, project, root, SessionOwnershipShared, ""); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("absent live identity: %v", err)
		}
	}
	if _, err := s.GetSession("one:resume:3"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("close advanced continuation: %v", err)
	}
	if id, err := s.ResolveRuntimeSessionWithOwnershipMode("two", project, root, SessionOwnershipShared); err != nil || id != "two:resume:2" {
		t.Fatalf("interleaved host changed: %q, %v", id, err)
	}
	if _, err := s.ResolveRuntimeSessionWithOwnershipMode("two", "other-project", root, SessionOwnershipShared); err != nil {
		t.Fatalf("shared cross-project resolve: %v", err)
	}
	if _, err := s.EndRuntimeSession("two", "other-project", root, SessionOwnershipShared, ""); err != nil {
		t.Fatalf("shared cross-project end: %v", err)
	}
	for _, id := range []string{"two", "two:resume:2"} {
		session, err := s.GetSession(id)
		if err != nil || session.Project != project || session.OwnershipMode != SessionOwnershipShared {
			t.Fatalf("shared ownership changed: %+v, %v", session, err)
		}
	}
}
