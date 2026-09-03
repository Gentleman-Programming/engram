package store

import (
	"strings"
	"testing"
)

// A database upgraded from the schema where sessions.project was nullable still
// carries rows that identify no project: the sessions table is only ever created
// with CREATE TABLE IF NOT EXISTS, which is a no-op on an existing table, so the
// current NOT NULL declaration never reaches a row written before it, and no
// migration rewrites or backfills the column.
//
// Every query that reads sessions.project must therefore treat it as nullable.
// This file is the single regression fixture for that class: each test drives one
// caller against a legacy NULL row, so a new raw scan of the column fails here
// rather than in a user's terminal.

// legacyNullProjectSession is the seed shape for one legacy session row.
// "<NULL>" seeds a genuine SQL NULL.
type legacyNullProjectSession struct{ id, project string }

// newLegacyNullProjectStore opens a store over a database whose sessions table
// still uses the nullable-project schema, carrying one unowned and one owned row.
func newLegacyNullProjectStore(t *testing.T) *Store {
	t.Helper()
	return newTestStoreWithNullableLegacySessions(t,
		legacyNullProjectSession{"null-session", "<NULL>"},
		legacyNullProjectSession{"owned-session", "engram"},
	)
}

// assertNoNullScanError fails with the original scan error rather than a generic
// message, so a regression names the column that regressed.
func assertNoNullScanError(t *testing.T, caller string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "converting NULL to string is unsupported") {
		t.Fatalf("%s scanned a legacy NULL sessions.project raw: %v", caller, err)
	}
	t.Fatalf("%s on legacy NULL project = %v, want success", caller, err)
}

// ListDiagnosticSessions is the doctor entry point from issue #841: a raw scan
// here aborted every check that reads sessions, so the whole report was lost on
// exactly the databases doctor exists to diagnose.
func TestListDiagnosticSessionsReadsLegacyNullProject(t *testing.T) {
	s := newLegacyNullProjectStore(t)

	sessions, err := s.ListDiagnosticSessions("")
	assertNoNullScanError(t, "ListDiagnosticSessions", err)
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v, want both the unowned and the owned session", sessions)
	}
	byID := map[string]DiagnosticSessionEvidence{}
	for _, session := range sessions {
		byID[session.ID] = session
	}
	if got := byID["null-session"].Project; got != "" {
		t.Fatalf("unowned session project = %q, want empty string for NULL ownership", got)
	}
	if got := byID["owned-session"].Project; got != "engram" {
		t.Fatalf("owned session project = %q, want engram", got)
	}
}

// The invalid-identity scan reads the same nullable column, so a corrupt session
// row that is also unowned must still be reported instead of aborting the check.
func TestListInvalidSessionIdentityEvidenceReadsLegacyNullProject(t *testing.T) {
	s := newTestStoreWithNullableLegacySessions(t, legacyNullProjectSession{" ", "<NULL>"})

	evidence, err := s.ListInvalidSessionIdentityEvidence("")
	assertNoNullScanError(t, "ListInvalidSessionIdentityEvidence", err)
	if len(evidence) != 1 {
		t.Fatalf("evidence = %+v, want the single blank-identity source row", evidence)
	}
	if evidence[0].SessionID != " " {
		t.Fatalf("evidence session id = %q, want the blank identity", evidence[0].SessionID)
	}
	if evidence[0].Project != "" {
		t.Fatalf("evidence project = %q, want empty string for NULL ownership", evidence[0].Project)
	}
}

// RecentSessions backs `engram context` and mem_context. An unscoped call reads
// every session row, so a single legacy row denied the user their whole context.
func TestRecentSessionsReadsLegacyNullProject(t *testing.T) {
	s := newLegacyNullProjectStore(t)

	sessions, err := s.RecentSessions("", 10)
	assertNoNullScanError(t, "RecentSessions", err)
	assertLegacySessionSummaries(t, sessions)
}

// AllSessions backs the TUI session browser, which always lists every project.
func TestAllSessionsReadsLegacyNullProject(t *testing.T) {
	s := newLegacyNullProjectStore(t)

	sessions, err := s.AllSessions("", 10)
	assertNoNullScanError(t, "AllSessions", err)
	assertLegacySessionSummaries(t, sessions)
}

func assertLegacySessionSummaries(t *testing.T, sessions []SessionSummary) {
	t.Helper()
	byID := map[string]SessionSummary{}
	for _, session := range sessions {
		byID[session.ID] = session
	}
	if len(byID) != 2 {
		t.Fatalf("sessions = %+v, want both the unowned and the owned session", sessions)
	}
	if got := byID["null-session"].Project; got != "" {
		t.Fatalf("unowned session project = %q, want empty string for NULL ownership", got)
	}
	if got := byID["owned-session"].Project; got != "engram" {
		t.Fatalf("owned session project = %q, want engram", got)
	}
}

// Export is the documented way to get data out before a repair, so it must not
// be the one path that refuses to read the rows the user is trying to rescue.
func TestExportReadsLegacyNullProject(t *testing.T) {
	s := newLegacyNullProjectStore(t)

	data, err := s.Export()
	assertNoNullScanError(t, "Export", err)
	byID := map[string]Session{}
	for _, session := range data.Sessions {
		byID[session.ID] = session
	}
	if len(byID) != 2 {
		t.Fatalf("exported sessions = %+v, want both the unowned and the owned session", data.Sessions)
	}
	if got := byID["null-session"].Project; got != "" {
		t.Fatalf("exported unowned session project = %q, want empty string for NULL ownership", got)
	}
}

// A legacy session that owns no project must still be deletable; refusing to
// read its ownership made it permanently undeletable.
func TestDeleteSessionReadsLegacyNullProject(t *testing.T) {
	s := newLegacyNullProjectStore(t)

	assertNoNullScanError(t, "DeleteSession", s.DeleteSession("null-session"))
	var remaining int
	if err := s.DB().QueryRow(`SELECT count(*) FROM sessions WHERE id = ?`, "null-session").Scan(&remaining); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("session rows after delete = %d, want 0", remaining)
	}
}

// Ending a legacy session read its ownership only to build the sync payload, so
// the raw scan rolled back an update that had already been applied.
func TestEndSessionReadsLegacyNullProject(t *testing.T) {
	s := newLegacyNullProjectStore(t)

	assertNoNullScanError(t, "EndSession", s.EndSession("null-session", "wrapped up"))
	var endedAt, summary *string
	if err := s.DB().QueryRow(`SELECT ended_at, summary FROM sessions WHERE id = ?`, "null-session").Scan(&endedAt, &summary); err != nil {
		t.Fatalf("read ended session: %v", err)
	}
	if endedAt == nil || summary == nil || *summary != "wrapped up" {
		t.Fatalf("ended_at=%v summary=%v, want the session ended with its summary", endedAt, summary)
	}
}

// Re-registering a session ID that already exists reads the persisted row back to
// build its sync payload. createSessionTx cannot heal a legacy NULL on the way
// through — `sessions.project = ”` is never true for NULL — so the readback has
// to tolerate the ownership the row still carries.
func TestCreateSessionReadbackReadsLegacyNullProject(t *testing.T) {
	s := newLegacyNullProjectStore(t)

	assertNoNullScanError(t, "CreateSession", s.CreateSession("null-session", "engram", "/tmp"))
}
