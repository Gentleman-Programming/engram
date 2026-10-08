package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
)

func runtimeScopeTestPath(root, path string) string {
	relative := func(value string) string {
		rel, err := filepath.Rel(projectpkg.RuntimeWorktreeDirectory(root), value)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "<outside-test-root>"
		}
		return filepath.ToSlash(rel)
	}
	canonical := projectpkg.RuntimeWorktreeDirectory(path)
	return fmt.Sprintf("{stored=%q canonical=%q equal=%t}", relative(path), relative(canonical), path == canonical)
}

func runtimeSessionDiagnostic(root string, rootSession *Session, rootErr error, continuation *Session, continuationErr error, requested string) string {
	rootPath := "<missing>"
	if rootSession != nil {
		rootPath = runtimeScopeTestPath(root, rootSession.Directory)
	}
	continuationPath := "<missing>"
	if continuation != nil {
		continuationPath = runtimeScopeTestPath(root, continuation.Directory)
	}
	return fmt.Sprintf("runtime scope diagnostic: root=%s root_read=%v continuation=%s continuation_read=%v requested=%s", rootPath, rootErr, continuationPath, continuationErr, runtimeScopeTestPath(root, requested))
}

func TestRuntimeSessionScopeAndConcurrencyDiagnosticHandlesNilSessions(t *testing.T) {
	root := t.TempDir()
	diagnostic := runtimeSessionDiagnostic(root, nil, sql.ErrNoRows, nil, sql.ErrNoRows, filepath.Join(root, "."))
	for _, want := range []string{"root=<missing>", "root_read=sql: no rows in result set", "continuation=<missing>", "continuation_read=sql: no rows in result set", "requested={stored=\"", "canonical=\".\"", "equal=false}"} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("diagnostic %q missing %q", diagnostic, want)
		}
	}
	if strings.Contains(diagnostic, root) {
		t.Fatalf("diagnostic exposed temp root %q: %s", root, diagnostic)
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
			rootSession, rootErr := s.GetSession(host)
			continuation, continuationErr := s.GetSession(host + ":resume:2")
			t.Log(runtimeSessionDiagnostic(root, rootSession, rootErr, continuation, continuationErr, filepath.Join(root, ".")))
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
