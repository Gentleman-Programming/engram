package store

import (
	"testing"
	"time"
)

// TestLookupActiveSessionReturnsMatchingOpenSession verifies that
// LookupActiveSession returns the ID of an open session matching
// (project, directory).
func TestLookupActiveSessionReturnsMatchingOpenSession(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("uuid-session-1", "myproject", "/work/myproject"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := s.LookupActiveSession("myproject", "/work/myproject")
	if err != nil {
		t.Fatalf("LookupActiveSession: %v", err)
	}
	if got != "uuid-session-1" {
		t.Fatalf("expected uuid-session-1, got %q", got)
	}
}

// TestLookupActiveSessionReturnsMostRecentWhenMultipleOpen verifies that
// when multiple open sessions share (project, directory), the most recent
// one (by started_at) is returned.
func TestLookupActiveSessionReturnsMostRecentWhenMultipleOpen(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("uuid-old", "myproject", "/work/myproject"); err != nil {
		t.Fatalf("CreateSession old: %v", err)
	}
	// Small sleep to ensure started_at differs — SQLite datetime('now') has 1-second resolution.
	time.Sleep(1100 * time.Millisecond)
	if err := s.CreateSession("uuid-new", "myproject", "/work/myproject"); err != nil {
		t.Fatalf("CreateSession new: %v", err)
	}

	got, err := s.LookupActiveSession("myproject", "/work/myproject")
	if err != nil {
		t.Fatalf("LookupActiveSession: %v", err)
	}
	if got != "uuid-new" {
		t.Fatalf("expected uuid-new (most recent), got %q", got)
	}
}

// TestLookupActiveSessionReturnsEmptyWhenNoMatch verifies that "" is returned
// (no error) when no session matches the given (project, directory).
func TestLookupActiveSessionReturnsEmptyWhenNoMatch(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("uuid-session-1", "myproject", "/work/myproject"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := s.LookupActiveSession("myproject", "/other/dir")
	if err != nil {
		t.Fatalf("LookupActiveSession: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string for no match, got %q", got)
	}
}

// TestLookupActiveSessionExcludesEnded verifies that sessions with ended_at
// set are not returned.
func TestLookupActiveSessionExcludesEnded(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("uuid-ended", "myproject", "/work/myproject"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.EndSession("uuid-ended", ""); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	got, err := s.LookupActiveSession("myproject", "/work/myproject")
	if err != nil {
		t.Fatalf("LookupActiveSession: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string (session ended), got %q", got)
	}
}

// TestLookupActiveSessionExcludesDirectoryMismatch verifies exact directory match.
func TestLookupActiveSessionExcludesDirectoryMismatch(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("uuid-session-1", "myproject", "/work/myproject"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := s.LookupActiveSession("myproject", "/work/myproject/subdir")
	if err != nil {
		t.Fatalf("LookupActiveSession: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string (directory mismatch), got %q", got)
	}
}

// TestLookupActiveSessionExcludesProjectMismatch verifies exact project match.
func TestLookupActiveSessionExcludesProjectMismatch(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("uuid-session-1", "myproject", "/work/myproject"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := s.LookupActiveSession("otherproject", "/work/myproject")
	if err != nil {
		t.Fatalf("LookupActiveSession: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string (project mismatch), got %q", got)
	}
}

// TestLookupActiveSessionReturnsEmptyForBlankInputs verifies that empty
// project or directory returns "" without error (no DB query).
func TestLookupActiveSessionReturnsEmptyForBlankInputs(t *testing.T) {
	s := newTestStore(t)

	// empty project
	got, err := s.LookupActiveSession("", "/work/myproject")
	if err != nil {
		t.Fatalf("LookupActiveSession empty project: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string for empty project, got %q", got)
	}

	// empty directory
	got, err = s.LookupActiveSession("myproject", "")
	if err != nil {
		t.Fatalf("LookupActiveSession empty directory: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string for empty directory, got %q", got)
	}
}
