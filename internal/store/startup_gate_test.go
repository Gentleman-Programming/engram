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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func TestLockedVersionRereadSkipsRepairForNewerSchema(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()

	s, err := New(cfg)
	if err != nil {
		t.Fatalf("seed New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	beforeMigrate := migrateRunCount.Load()
	beforeRepair := startupRepairRunCount.Load()
	future := schemaVersion + 7
	startupMigrationBeforeLockHook = func() {
		raw, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
		if err != nil {
			t.Fatalf("raw open: %v", err)
		}
		defer raw.Close()
		if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", future)); err != nil {
			t.Fatalf("stamp future user_version: %v", err)
		}
	}
	t.Cleanup(func() { startupMigrationBeforeLockHook = nil })

	s, err = New(cfg)
	if err != nil {
		t.Fatalf("New after locked schema change: %v", err)
	}
	defer s.Close()

	if got := migrateRunCount.Load() - beforeMigrate; got != 0 {
		t.Errorf("migration suite ran %d times after locked re-read found newer schema, want 0", got)
	}
	if got := startupRepairRunCount.Load() - beforeRepair; got != 0 {
		t.Errorf("startup repair ran %d times after locked re-read found newer schema, want 0", got)
	}
	var got int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&got); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if got != future {
		t.Errorf("user_version after locked re-read = %d, want %d", got, future)
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
	childID := fmt.Sprintf("%d", os.Getpid())
	readyPath := filepath.Join(dir, ".coldstart-ready-"+childID)
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		t.Fatalf("write ready marker: %v", err)
	}
	releasePath := filepath.Join(dir, ".coldstart-release")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(releasePath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatalf("read release marker: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for parent cold-start release")
		}
		time.Sleep(10 * time.Millisecond)
	}

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

	exe := os.Args[0]
	if exe == "" {
		t.Fatal("test binary path is empty")
	}

	dir := t.TempDir()
	const procs = 3
	t.Setenv(coldStartChildEnv, dir)
	releasePath := filepath.Join(dir, ".coldstart-release")
	t.Cleanup(func() { _ = os.WriteFile(releasePath, []byte("release"), 0o600) })

	cmds := make([]*exec.Cmd, procs)
	outputs := make([]*strings.Builder, procs)
	for i := range cmds {
		outputs[i] = &strings.Builder{}
		cmd := exec.Command(exe, "-test.run=^TestColdStartChildProcess$", "-test.v", "-test.timeout=60s")
		cmd.Stdout = outputs[i]
		cmd.Stderr = outputs[i]
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child %d (%s): %v", i, cmd.String(), err)
		}
		cmds[i] = cmd
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read child readiness directory: %v", err)
		}
		ready := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".coldstart-ready-") {
				ready++
			}
		}
		if ready == procs {
			break
		}
		if time.Now().After(deadline) {
			var diagnostics strings.Builder
			for i, output := range outputs {
				fmt.Fprintf(&diagnostics, "child %d output:\n%s\n", i, output.String())
			}
			t.Fatalf("timed out waiting for %d child readiness markers; got %d; outputs:\n%s", procs, ready, diagnostics.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release child cold starts: %v", err)
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

func TestCheckDatabaseGenerationReportsReplacement(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "original.db")
	replacement := filepath.Join(dir, "replacement.db")
	if err := os.WriteFile(original, []byte("original"), 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	info, err := os.Stat(original)
	if err != nil {
		t.Fatalf("stat original: %v", err)
	}
	s := &Store{databasePath: replacement, databaseGenerationInfos: map[string]os.FileInfo{replacement: info}}
	err = s.CheckDatabaseGeneration()
	if !errors.Is(err, ErrDatabaseGenerationReplaced) {
		t.Fatalf("CheckDatabaseGeneration error = %v, want ErrDatabaseGenerationReplaced", err)
	}
	if !strings.Contains(err.Error(), "restart") {
		t.Errorf("replacement diagnostic = %q, want restart guidance", err)
	}
}

func TestLiveStoreDetectsReplacedDatabaseGeneration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit replacing an open SQLite database file")
	}
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	dbPath := filepath.Join(cfg.DataDir, "engram.db")
	replacement := filepath.Join(cfg.DataDir, "replacement.db")
	if err := os.WriteFile(replacement, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	if err := os.Rename(replacement, dbPath); err != nil {
		t.Fatalf("replace live database generation: %v", err)
	}
	if err := s.CreateSession("replacement-session", "replacement-project", cfg.DataDir); !errors.Is(err, ErrDatabaseGenerationReplaced) {
		t.Fatalf("CreateSession error = %v, want ErrDatabaseGenerationReplaced", err)
	}
}

func TestReadOperationsRefuseReplacedDatabaseGeneration(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	dbPath := filepath.Join(cfg.DataDir, "engram.db")
	replacementIdentity := filepath.Join(t.TempDir(), "replacement.db")
	if err := os.WriteFile(replacementIdentity, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement identity: %v", err)
	}
	replacementInfo, err := os.Stat(replacementIdentity)
	if err != nil {
		t.Fatalf("stat replacement identity: %v", err)
	}
	s.generationMu.Lock()
	s.databaseGenerationInfos = map[string]os.FileInfo{dbPath: replacementInfo}
	s.generationMu.Unlock()

	cases := []struct {
		name string
		read func() error
	}{
		{"recent observations", func() error {
			_, err := s.RecentObservations("", "", 1)
			return err
		}},
		{"get observation", func() error {
			_, err := s.GetObservation(1)
			return err
		}},
		{"get observation by sync ID", func() error {
			_, err := s.GetObservationBySyncID("missing")
			return err
		}},
		{"search topic-key branch", func() error {
			_, err := s.SearchContext(context.Background(), "project/topic", SearchOptions{})
			return err
		}},
		{"search FTS branch", func() error {
			_, err := s.SearchContext(context.Background(), "query", SearchOptions{})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.read()
			if !errors.Is(err, ErrDatabaseGenerationReplaced) {
				t.Fatalf("read error = %v, want ErrDatabaseGenerationReplaced", err)
			}
			if !strings.Contains(err.Error(), "restart") {
				t.Errorf("replacement diagnostic = %q, want restart guidance", err)
			}
		})
	}
}

func markDatabaseGenerationReplaced(t *testing.T, s *Store) {
	t.Helper()
	replacementIdentity := filepath.Join(t.TempDir(), "replacement.db")
	if err := os.WriteFile(replacementIdentity, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement identity: %v", err)
	}
	replacementInfo, err := os.Stat(replacementIdentity)
	if err != nil {
		t.Fatalf("stat replacement identity: %v", err)
	}
	s.generationMu.Lock()
	s.databaseGenerationInfos = map[string]os.FileInfo{s.databasePath: replacementInfo}
	s.generationMu.Unlock()
}

func TestRuntimeBoundaryRejectsReplacementWithoutWriteSideEffects(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("generation-boundary", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatalf("enroll project: %v", err)
	}
	first, err := s.AddObservation(AddObservationParams{SessionID: "generation-boundary", Type: "decision", Title: "v2 migration", Content: "first", Project: "engram", Scope: "project"})
	if err != nil {
		t.Fatalf("add first observation: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "generation-boundary", Type: "decision", Title: "v2 upgrade", Content: "second", Project: "engram", Scope: "project"}); err != nil {
		t.Fatalf("add second observation: %v", err)
	}
	markDatabaseGenerationReplaced(t, s)

	operations := []struct {
		name string
		run  func() error
	}{
		{"get session", func() error { _, err := s.GetSession("generation-boundary"); return err }},
		{"find candidates", func() error { _, err := s.FindCandidates(first, CandidateOptions{SkipInsert: true}); return err }},
		{"pin observation", func() error { return s.PinObservation(first) }},
		{"unenroll project", func() error { return s.UnenrollProject("engram") }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); !errors.Is(err, ErrDatabaseGenerationReplaced) {
				t.Fatalf("operation error = %v, want ErrDatabaseGenerationReplaced", err)
			}
		})
	}
	var pinned, enrolled int
	if err := s.DB().QueryRow(`SELECT pinned FROM observations WHERE id = ?`, first).Scan(&pinned); err != nil {
		t.Fatalf("read pinned state: %v", err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM sync_enrolled_projects WHERE project = 'engram'`).Scan(&enrolled); err != nil {
		t.Fatalf("read enrollment state: %v", err)
	}
	if pinned != 0 || enrolled != 1 {
		t.Fatalf("replacement mutated runtime state: pinned=%d enrolled=%d", pinned, enrolled)
	}
}

func TestWithReadPrefersPostReadGenerationReplacement(t *testing.T) {
	s := newTestStore(t)
	err := s.withRead(func() error {
		markDatabaseGenerationReplaced(t, s)
		return errors.New("synthetic read error")
	})
	if !errors.Is(err, ErrDatabaseGenerationReplaced) {
		t.Fatalf("withRead error = %v, want generation replacement", err)
	}
}

func TestAdditionalReadOperationsRefuseReplacedDatabaseGeneration(t *testing.T) {
	cases := []struct {
		name string
		read func(*Store) error
	}{
		{"format context", func(s *Store) error {
			_, err := s.FormatContext("", "")
			return err
		}},
		{"recent sessions", func(s *Store) error {
			_, err := s.RecentSessions("", 1)
			return err
		}},
		{"recent prompts", func(s *Store) error {
			_, err := s.RecentPrompts("", 1)
			return err
		}},
		{"stats", func(s *Store) error {
			_, err := s.Stats()
			return err
		}},
		{"export", func(s *Store) error {
			_, err := s.Export()
			return err
		}},
		{"list project names", func(s *Store) error {
			_, err := s.ListProjectNames()
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			markDatabaseGenerationReplaced(t, s)

			err := tc.read(s)
			if !errors.Is(err, ErrDatabaseGenerationReplaced) {
				t.Fatalf("read error = %v, want ErrDatabaseGenerationReplaced", err)
			}
		})
	}
}

func TestStatsRefusesReplacedDatabaseGenerationWithoutPartialData(t *testing.T) {
	s := newTestStore(t)
	markDatabaseGenerationReplaced(t, s)

	stats, err := s.Stats()
	if !errors.Is(err, ErrDatabaseGenerationReplaced) {
		t.Fatalf("Stats error = %v, want ErrDatabaseGenerationReplaced", err)
	}
	if stats != nil {
		t.Fatalf("Stats returned stale partial data: %#v", stats)
	}
}

func TestSaveRelationRefusesReplacedDatabaseGenerationWithoutWriting(t *testing.T) {
	s := newTestStore(t)
	originalExec := s.hooks.exec
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		result, err := originalExec(db, query, args...)
		if strings.Contains(query, "INSERT INTO memory_relations") {
			markDatabaseGenerationReplaced(t, s)
		}
		return result, err
	}

	_, err := s.SaveRelation(SaveRelationParams{
		SyncID:   "replaced-generation-relation",
		SourceID: "source",
		TargetID: "target",
	})
	if !errors.Is(err, ErrDatabaseGenerationReplaced) {
		t.Fatalf("SaveRelation error = %v, want ErrDatabaseGenerationReplaced", err)
	}

	var count int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM memory_relations WHERE sync_id = ?", "replaced-generation-relation").Scan(&count); err != nil {
		t.Fatalf("count replacement-generation relation: %v", err)
	}
	if count != 0 {
		t.Fatalf("relation persisted in replacement generation: count = %d, want 0", count)
	}
}

func TestReadGenerationGuardRejectsMidOperationReplacement(t *testing.T) {
	s := newTestStore(t)
	s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
		rows, err := db.Query(query, args...)
		if err != nil {
			return nil, err
		}
		markDatabaseGenerationReplaced(t, s)
		return sqlRowScanner{rows: rows}, nil
	}

	projects, err := s.ListProjectNames()
	if !errors.Is(err, ErrDatabaseGenerationReplaced) {
		t.Fatalf("ListProjectNames error = %v, want ErrDatabaseGenerationReplaced", err)
	}
	if projects != nil {
		t.Fatalf("ListProjectNames returned data from replaced generation: %v", projects)
	}
}

func TestStoreOpensQuestionMarkDataDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit '?' in file names")
	}
	cfg := mustDefaultConfig(t)
	cfg.DataDir = filepath.Join(t.TempDir(), "data?dir")

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

	dbPath := filepath.Join(cfg.DataDir, "engram.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database was not created at intended path %q: %v", dbPath, err)
	}
	raw, err := sql.Open("sqlite", storeDSN(dbPath))
	if err != nil {
		t.Fatalf("open intended database: %v", err)
	}
	defer raw.Close()
	var sessions int
	if err := raw.QueryRow("SELECT COUNT(*) FROM sessions WHERE id = ?", "question-mark-session").Scan(&sessions); err != nil {
		t.Fatalf("query intended database: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("sessions in intended database = %d, want 1", sessions)
	}
}

func TestCommittedTransactionKeepsTrustedDatabaseGeneration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit replacing an open SQLite database file")
	}
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	dbPath := filepath.Join(cfg.DataDir, "engram.db")
	replacement := filepath.Join(cfg.DataDir, "replacement.db")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	replaced := false
	s.hooks.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		if !replaced {
			replaced = true
			return os.Rename(replacement, dbPath)
		}
		return nil
	}

	if err := s.CreateSession("committed-session", "generation-project", cfg.DataDir); err != nil {
		t.Fatalf("CreateSession must preserve committed-write success: %v", err)
	}
	if !replaced {
		t.Fatal("commit hook did not replace the database generation")
	}
	if err := s.CreateSession("rejected-session", "generation-project", cfg.DataDir); !errors.Is(err, ErrDatabaseGenerationReplaced) {
		t.Fatalf("CreateSession after replacement error = %v, want ErrDatabaseGenerationReplaced", err)
	}
}

func TestCommittedTransactionSucceedsWhenReplacementFollowsCommit(t *testing.T) {
	s := newTestStore(t)
	originalCommit := s.hooks.commit
	s.hooks.commit = func(tx *sql.Tx) error {
		if err := originalCommit(tx); err != nil {
			return err
		}
		markDatabaseGenerationReplaced(t, s)
		return nil
	}

	if err := s.CreateSession("trusted-commit", "generation-project", "/tmp/generation-project"); err != nil {
		t.Fatalf("CreateSession returned an error after a trusted commit: %v", err)
	}

	var count int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM sessions WHERE id = ?", "trusted-commit").Scan(&count); err != nil {
		t.Fatalf("count committed session: %v", err)
	}
	if count != 1 {
		t.Fatalf("committed session count = %d, want 1", count)
	}
	if err := s.CreateSession("rejected-after-commit", "generation-project", "/tmp/generation-project"); !errors.Is(err, ErrDatabaseGenerationReplaced) {
		t.Fatalf("CreateSession after replacement error = %v, want ErrDatabaseGenerationReplaced", err)
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
	if !strings.Contains(err.Error(), filepath.Base(path)) {
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
	defer unlock1()

	acquired := make(chan error, 1)
	go func() {
		unlock2, err := acquireMigrationLock(path)
		if err != nil {
			acquired <- err
			return
		}
		unlock2()
		acquired <- nil
	}()

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second acquire: %v", err)
		}
		t.Fatal("second acquire succeeded while the first lock was still held")
	case <-time.After(150 * time.Millisecond):
		// Still blocked — expected.
	}

	unlock1()

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second acquire after release: %v", err)
		}
		// Granted after release — expected.
	case <-time.After(5 * time.Second):
		t.Fatal("second acquire did not proceed after the first lock was released")
	}
}

func TestImportRefusesReplacedDatabaseGeneration(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	original, err := os.Stat(filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("stat original generation: %v", err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement.db")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement generation: %v", err)
	}
	s.databasePath = replacement
	s.databaseGenerationInfos = map[string]os.FileInfo{replacement: original}

	_, err = s.Import(&ExportData{Sessions: []Session{{ID: "blocked-import", Project: "engram", Directory: cfg.DataDir}}})
	if !errors.Is(err, ErrDatabaseGenerationReplaced) {
		t.Fatalf("Import error = %v, want ErrDatabaseGenerationReplaced", err)
	}
	var sessions int
	if err := s.db.QueryRow(`SELECT count(*) FROM sessions WHERE id = 'blocked-import'`).Scan(&sessions); err != nil {
		t.Fatalf("count imports: %v", err)
	}
	if sessions != 0 {
		t.Fatalf("Import wrote %d sessions after detecting replacement", sessions)
	}
}

func TestImportRollsBackWhenGenerationChangesBeforeCommit(t *testing.T) {
	cfg := mustDefaultConfig(t)
	cfg.DataDir = t.TempDir()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	dbPath := filepath.Join(cfg.DataDir, "engram.db")
	replacement := filepath.Join(t.TempDir(), "replacement.db")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement identity: %v", err)
	}
	replacementInfo, err := os.Stat(replacement)
	if err != nil {
		t.Fatalf("stat replacement identity: %v", err)
	}
	originalExec := s.hooks.exec
	changed := false
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		result, execErr := originalExec(db, query, args...)
		if !changed && strings.Contains(query, "INSERT OR IGNORE INTO sessions") {
			changed = true
			s.generationMu.Lock()
			s.databaseGenerationInfos = map[string]os.FileInfo{dbPath: replacementInfo}
			s.generationMu.Unlock()
		}
		return result, execErr
	}

	_, err = s.Import(&ExportData{Sessions: []Session{{ID: "atomic-import", Project: "engram", Directory: cfg.DataDir}}})
	if !errors.Is(err, ErrDatabaseGenerationReplaced) {
		t.Fatalf("Import error = %v, want ErrDatabaseGenerationReplaced", err)
	}
	if !changed {
		t.Fatal("import did not reach the staged session write")
	}
	var sessions int
	if err := s.db.QueryRow(`SELECT count(*) FROM sessions WHERE id = 'atomic-import'`).Scan(&sessions); err != nil {
		t.Fatalf("count imported sessions: %v", err)
	}
	if sessions != 0 {
		t.Fatalf("Import committed %d session(s) after generation changed", sessions)
	}
}

func TestStoreGenerationLeaseBlocksDatabaseMove(t *testing.T) {
	origTimeout := migrationLockTimeout
	migrationLockTimeout = 300 * time.Millisecond
	t.Cleanup(func() { migrationLockTimeout = origTimeout })

	dir := t.TempDir()
	release, err := acquireStoreGenerationLease(filepath.Join(dir, ".generation.lock"))
	if err != nil {
		t.Fatalf("acquire store generation lease: %v", err)
	}
	defer release()

	_, err = AcquireDatabaseGenerationMoveLock(dir)
	if err == nil {
		t.Fatal("database mover acquired an exclusive lock while a Store lease was held")
	}
	if !strings.Contains(err.Error(), "database generation") {
		t.Errorf("generation move lock diagnostic = %q, want database generation context", err)
	}
}
