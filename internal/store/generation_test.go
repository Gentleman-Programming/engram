package store // generation guards: prevention via file identity, detection via fresh connections (#477/#571)

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// replaceDBFile simulates an external actor replacing the live database file
// with a fresh generation at the same path.
func replaceDBFile(t *testing.T, dbPath string) {
	t.Helper()
	tmp := dbPath + ".replacement"
	if err := os.WriteFile(tmp, nil, 0o644); err != nil {
		t.Fatalf("write replacement file: %v", err)
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		t.Fatalf("rename replacement over live db: %v", err)
	}
}

func requireUnixFileIdentity(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file identity check is unix-only; post-commit verification still runs on windows")
	}
}

// TestCheckGeneration_RejectsCriticalWritesAfterDBFileReplaced proves the
// deterministic prevention gate: once the database file at dbPath is a
// different generation than the one this Store opened, critical writes fail
// loudly instead of writing into a dead generation (#477 silent write loss).
func TestCheckGeneration_RejectsCriticalWritesAfterDBFileReplaced(t *testing.T) {
	requireUnixFileIdentity(t)
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session on healthy store: %v", err)
	}

	replaceDBFile(t, s.dbPath)

	err := s.CreateSession("s2", "engram", "/tmp/engram")
	if !errors.Is(err, ErrDBGenerationReplaced) {
		t.Fatalf("CreateSession after replacement: got %v, want ErrDBGenerationReplaced", err)
	}

	_, err = s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Lost write",
		Content:   "This write must be rejected, not silently lost",
		Project:   "engram",
		Scope:     "project",
	})
	if !errors.Is(err, ErrDBGenerationReplaced) {
		t.Fatalf("AddObservation after replacement: got %v, want ErrDBGenerationReplaced", err)
	}
}

// TestCriticalWrites_VerifyFreshConnectionNoFalsePositive proves the healthy
// path: with verification active, committed rows are visible to a fresh
// independent connection and every critical write still succeeds.
func TestCriticalWrites_VerifyFreshConnectionNoFalsePositive(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "session_summary",
		Title:     "Session",
		Content:   "Summary committed while the WAL is live",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if id == 0 {
		t.Fatal("observation id must be set")
	}
}

// TestVerifyVisible_MapsMissingTableToGenerationReplaced proves a replaced
// generation without schema (empty file) surfaces as ErrDBGenerationReplaced,
// not as a raw sqlite error (#571 loud failure contract).
func TestVerifyVisible_MapsMissingTableToGenerationReplaced(t *testing.T) {
	s := newTestStore(t)

	// Point verification at a fresh empty file: same situation as an external
	// replacement with a new, unmigrated generation.
	empty := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty db: %v", err)
	}
	saved := s.dbPath
	s.dbPath = empty
	t.Cleanup(func() { s.dbPath = saved })

	err := s.verifyVisible(`SELECT 1 FROM observations WHERE id = ?`, int64(1))
	if !errors.Is(err, ErrDBGenerationReplaced) {
		t.Fatalf("verifyVisible against schemaless generation: got %v, want ErrDBGenerationReplaced", err)
	}
}

// TestAddObservation_DetectsInvisibleCommitAfterWALReplaced reproduces the
// #477 failure mode deterministically: the db file identity is unchanged, the
// write commits on the live connection, but the committed row is invisible to
// a fresh connection because the WAL generation was replaced underneath. The
// store must fail loudly with restart guidance instead of acknowledging the
// write.
func TestAddObservation_DetectsInvisibleCommitAfterWALReplaced(t *testing.T) {
	requireUnixFileIdentity(t)
	s := newTestStore(t)

	if err := s.CreateSession("s1", "engram", "/tmp/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "bugfix",
		Title:     "Anchor",
		Content:   "Keeps the WAL uncheckpointed",
		Project:   "engram",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("anchor observation: %v", err)
	}

	// External actor walks away with the live WAL: the db file identity is
	// unchanged, so only post-commit verification can catch this.
	walPath := s.dbPath + "-wal"
	if err := os.Rename(walPath, walPath+".gone"); err != nil {
		t.Fatalf("rename wal away: %v", err)
	}

	_, err := s.AddObservation(AddObservationParams{
		SessionID: "s1",
		Type:      "session_summary",
		Title:     "Would be lost",
		Content:   "Committed on the live connection, invisible to fresh readers",
		Project:   "engram",
		Scope:     "project",
	})
	if !errors.Is(err, ErrDBGenerationReplaced) {
		t.Fatalf("AddObservation after WAL replacement: got %v, want ErrDBGenerationReplaced", err)
	}
}
