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
	"context"
	"database/sql"
	"fmt"
	"net/url"
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

func TestNewWithQuestionMarkInDataDirectory(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("Unix filesystem path behavior")
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = filepath.Join(t.TempDir(), "data?query")

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := s.CreateSession("question-mark-session", "question-mark-project", cfg.DataDir); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.Join(cfg.DataDir, "engram.db")}).String())
	if err != nil {
		t.Fatalf("open configured database: %v", err)
	}
	defer raw.Close()

	var sessions int
	if err := raw.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatalf("count sessions in configured database: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("sessions in configured database = %d, want 1", sessions)
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

func TestNewerSchemaVersionSkipsSchemaRepair(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()

	s1, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(`
		INSERT INTO sync_apply_deferred (sync_id, entity, payload)
		VALUES ('future-schema-repair', 'relation', '{"sync_id":"derived-by-migrate"}')
	`); err != nil {
		t.Fatalf("seed schema repair row: %v", err)
	}
	if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion+7)); err != nil {
		t.Fatalf("stamp future user_version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}

	s2, err := New(cfg)
	if err != nil {
		t.Fatalf("New on newer-schema database: %v", err)
	}
	defer s2.Close()

	var payloadSyncID string
	if err := s2.db.QueryRow(`SELECT payload_sync_id FROM sync_apply_deferred WHERE sync_id = 'future-schema-repair'`).Scan(&payloadSyncID); err != nil {
		t.Fatalf("read schema repair row: %v", err)
	}
	if payloadSyncID != "" {
		t.Fatalf("future-schema open derived payload_sync_id %q, want no schema repair write", payloadSyncID)
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

// TestConnectionReplacementKeepsConfiguration guards the pool-replacement
// regression: database/sql silently discards a modernc connection after a
// context-cancelled query interrupts it (IsValid/ResetSession fail once
// sqlite3_is_interrupted) and opens a fresh one. Because the pragmas travel
// in the DSN and persist-WAL is applied by a driver connection hook, the
// replacement connection must come up fully configured — busy_timeout 5000
// (#559) and persistent WAL (#477) intact.
func TestConnectionReplacementKeepsConfiguration(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	// Plant a session-scoped marker on the current physical connection.
	// PRAGMA cache_size is per-connection and not part of the DSN, so its
	// disappearance later proves the pool swapped in a new connection.
	if _, err := s.db.Exec("PRAGMA cache_size = -12345"); err != nil {
		t.Fatalf("set cache_size marker: %v", err)
	}
	var marker int
	if err := s.db.QueryRow("PRAGMA cache_size").Scan(&marker); err != nil {
		t.Fatalf("read cache_size marker: %v", err)
	}
	if marker != -12345 {
		t.Fatalf("cache_size marker = %d, want -12345", marker)
	}

	// Interrupt a long-running query via context timeout. modernc's
	// interruptOnDone calls sqlite3_interrupt, poisoning the connection so
	// the pool discards it on release.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var n int64
	if err := s.db.QueryRowContext(ctx,
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT count(*) FROM c`,
	).Scan(&n); err == nil {
		t.Fatal("expected the interrupted query to fail, it succeeded")
	}

	// The pool must have replaced the physical connection...
	var cacheSize int
	if err := s.db.QueryRow("PRAGMA cache_size").Scan(&cacheSize); err != nil {
		t.Fatalf("query after interruption: %v", err)
	}
	if cacheSize == -12345 {
		t.Fatal("cache_size marker survived — connection was not replaced; test cannot exercise the replacement path")
	}

	// ...and the replacement must be fully configured.
	var busy int
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busy != 5000 {
		t.Errorf("busy_timeout on replacement connection = %d, want 5000 (#559 regression)", busy)
	}
	var journalMode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Errorf("journal_mode on replacement connection = %q, want wal", journalMode)
	}
	var foreignKeys int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys on replacement connection = %d, want 1", foreignKeys)
	}

	// Persist-WAL must be held on the replacement connection (query mode -1).
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin replacement connection: %v", err)
	}
	if err := conn.Raw(func(driverConn any) error {
		fc, ok := driverConn.(interface {
			FileControlPersistWAL(string, int) (int, error)
		})
		if !ok {
			return fmt.Errorf("driver connection %T has no FileControlPersistWAL", driverConn)
		}
		mode, err := fc.FileControlPersistWAL("main", -1)
		if err != nil {
			return err
		}
		if mode != 1 {
			return fmt.Errorf("persist-WAL mode on replacement connection = %d, want 1 (#477 regression)", mode)
		}
		return nil
	}); err != nil {
		t.Error(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("release pinned connection: %v", err)
	}

	// End to end: write through the replacement connection, close, and the
	// -wal file must survive.
	if err := s.CreateSession("replacement-session", "replacement-project", cfg.DataDir); err != nil {
		t.Fatalf("CreateSession on replacement connection: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	walPath := filepath.Join(cfg.DataDir, "engram.db-wal")
	if _, err := os.Stat(walPath); err != nil {
		t.Fatalf("-wal file was unlinked on close after connection replacement — persist-WAL was lost: %v", err)
	}
}

func TestAcquireMigrationLockTimesOutWithDiagnostic(t *testing.T) {
	origTimeout := migrationLockTimeout
	migrationLockTimeout = 300 * time.Millisecond
	t.Cleanup(func() { migrationLockTimeout = origTimeout })

	path := filepath.Join(t.TempDir(), ".migrate.lock")

	unlock, err := acquireMigrationLock(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer unlock()

	start := time.Now()
	_, err = acquireMigrationLock(path)
	if err == nil {
		t.Fatal("second acquire succeeded while the first lock was held; want timeout error")
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("timed out after %s, want at least the %s budget", elapsed, 300*time.Millisecond)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("timeout error does not name the lock file: %v", err)
	}
	if !strings.Contains(err.Error(), "stuck engram process") {
		t.Errorf("timeout error does not point at a stuck process: %v", err)
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
