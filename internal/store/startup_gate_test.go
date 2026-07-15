package store

// Tests for the SQLite-corruption fixes:
//
//  1. persistent WAL — closing the store must NOT unlink the -wal file
//     (mechanism behind upstream #477/#571),
//  2. cold-start concurrency — many stores opening the same fresh database
//     simultaneously, in-process and across child processes (#559), and
//  3. the user_version migration gate — the migration suite runs exactly
//     once per schema generation and is never run against a database
//     stamped by a newer engram.

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPersistentWALSurvivesClose(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.CreateSession("wal-session", "wal-project", cfg.DataDir); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	walPath := filepath.Join(cfg.DataDir, "engram.db-wal")
	if _, err := os.Stat(walPath); err != nil {
		t.Fatalf("-wal file missing while store is open: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(walPath); err != nil {
		t.Fatalf("-wal file was unlinked on close — persistent WAL is not active: %v", err)
	}
}

func TestUserVersionGateSkipsSecondOpen(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()

	before := migrateRunCount.Load()

	s1, err := New(cfg)
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	var v int
	if err := s1.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if v != schemaVersion {
		t.Fatalf("user_version after first open = %d, want %d", v, schemaVersion)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	afterFirst := migrateRunCount.Load()
	if got := afterFirst - before; got != 1 {
		t.Fatalf("migration suite ran %d times on a fresh database, want exactly 1", got)
	}

	s2, err := New(cfg)
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer s2.Close()

	if got := migrateRunCount.Load() - afterFirst; got != 0 {
		t.Fatalf("migration suite ran %d times on an already-migrated database, want 0", got)
	}

	// The gated (skipped-migration) store must still be fully usable.
	if err := s2.CreateSession("gate-session", "gate-project", cfg.DataDir); err != nil {
		t.Fatalf("CreateSession on gated store: %v", err)
	}
}

func TestNewerSchemaVersionIsLeftUntouched(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()

	s1, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Simulate a database already migrated by a NEWER engram.
	future := schemaVersion + 7
	raw, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", future)); err != nil {
		t.Fatalf("stamp future user_version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	before := migrateRunCount.Load()

	s2, err := New(cfg)
	if err != nil {
		t.Fatalf("New on newer-schema database: %v", err)
	}
	defer s2.Close()

	if got := migrateRunCount.Load() - before; got != 0 {
		t.Fatalf("migration suite ran %d times against a newer schema, want 0", got)
	}

	var v int
	if err := s2.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if v != future {
		t.Fatalf("user_version was rewritten to %d, want it left at %d", v, future)
	}
}

func TestConcurrentColdStartGoroutines(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()

	before := migrateRunCount.Load()

	const n = 8
	start := make(chan struct{})
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s, err := New(cfg)
			if err != nil {
				errs <- fmt.Errorf("goroutine %d: New: %w", i, err)
				return
			}
			defer s.Close()
			if err := s.CreateSession(fmt.Sprintf("cold-%d", i), "coldstart", cfg.DataDir); err != nil {
				errs <- fmt.Errorf("goroutine %d: CreateSession: %w", i, err)
				return
			}
			errs <- nil
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}

	if got := migrateRunCount.Load() - before; got != 1 {
		t.Errorf("migration suite ran %d times across %d concurrent cold starts, want exactly 1", got, n)
	}

	// Verify the resulting database is complete and healthy.
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("verification New: %v", err)
	}
	defer s.Close()

	var sessions int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != n {
		t.Errorf("sessions = %d, want %d", sessions, n)
	}
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if v != schemaVersion {
		t.Errorf("user_version = %d, want %d", v, schemaVersion)
	}
	var integrity string
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Errorf("integrity_check = %q, want ok", integrity)
	}
}

// coldStartChildEnv points a re-executed child copy of the test binary at the
// shared data directory used by TestConcurrentColdStartProcesses.
const coldStartChildEnv = "ENGRAM_TEST_COLDSTART_DIR"

// TestColdStartChildProcess is not a standalone test: it is re-executed as a
// child process by TestConcurrentColdStartProcesses and skips otherwise.
func TestColdStartChildProcess(t *testing.T) {
	dir := os.Getenv(coldStartChildEnv)
	if dir == "" {
		t.Skip("runs only as a child of TestConcurrentColdStartProcesses")
	}

	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = dir
	cfg.DedupeWindow = time.Hour

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("child cold-start New: %v", err)
	}
	defer s.Close()

	if err := s.CreateSession(fmt.Sprintf("proc-%d", os.Getpid()), "coldstart-proc", dir); err != nil {
		t.Fatalf("child CreateSession: %v", err)
	}
}

func TestConcurrentColdStartProcesses(t *testing.T) {
	if os.Getenv(coldStartChildEnv) != "" {
		t.Skip("child process mode")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	dir := t.TempDir()
	const procs = 3

	cmds := make([]*exec.Cmd, procs)
	outputs := make([]*strings.Builder, procs)
	for i := range cmds {
		outputs[i] = &strings.Builder{}
		cmd := exec.Command(exe, "-test.run", "^TestColdStartChildProcess$", "-test.v", "-test.timeout", "60s")
		cmd.Env = append(os.Environ(), coldStartChildEnv+"="+dir)
		cmd.Stdout = outputs[i]
		cmd.Stderr = outputs[i]
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child %d: %v", i, err)
		}
		cmds[i] = cmd
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("child process %d failed: %v\noutput:\n%s", i, err, outputs[i].String())
		}
	}
	if t.Failed() {
		return
	}

	// Every child cold-started against the same fresh database and wrote one
	// session. Open from the parent and verify the result is consistent.
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	cfg.DataDir = dir
	cfg.DedupeWindow = time.Hour

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("parent verification New: %v", err)
	}
	defer s.Close()

	var sessions int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != procs {
		t.Errorf("sessions = %d, want %d", sessions, procs)
	}
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if v != schemaVersion {
		t.Errorf("user_version = %d, want %d", v, schemaVersion)
	}
	var integrity string
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Errorf("integrity_check = %q, want ok", integrity)
	}
}

func TestAcquireMigrationLockExcludes(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".migrate.lock")

	unlock1, err := acquireMigrationLock(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		unlock2, err := acquireMigrationLock(path)
		if err != nil {
			t.Errorf("second acquire: %v", err)
			close(acquired)
			return
		}
		close(acquired)
		unlock2()
	}()

	select {
	case <-acquired:
		t.Fatal("second acquire succeeded while the first lock was still held")
	case <-time.After(150 * time.Millisecond):
		// Still blocked — expected.
	}

	unlock1()

	select {
	case <-acquired:
		// Granted after release — expected.
	case <-time.After(5 * time.Second):
		t.Fatal("second acquire did not proceed after the first lock was released")
	}
}
