package store

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

func testSQLiteDSN(dbPath string, immediate bool) string {
	q := url.Values{}
	if immediate {
		q.Set("_txlock", "immediate")
	}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	if filepath.Separator == '/' {
		return (&url.URL{Scheme: "file", Path: dbPath}).String() + "?" + q.Encode()
	}
	return dbPath + "?" + q.Encode()
}

func assertBusyBegin(t *testing.T, begin func() error) {
	t.Helper()
	started := time.Now()
	err := begin()
	waited := time.Since(started)
	if err == nil {
		t.Fatal("contending BEGIN succeeded while the first transaction held the lock")
	}
	if !isRetryableSQLiteLockError(err) {
		t.Fatalf("contending BEGIN error = %v, want SQLITE_BUSY", err)
	}
	if waited < 75*time.Millisecond {
		t.Fatalf("contending BEGIN returned after %s, want a busy-timeout wait", waited)
	}
}

// TestSQLiteBeginDeferredDeadlockRepro demonstrates that two connections performing
// read-before-write in a standard transaction (BEGIN DEFERRED) will deadlock and trigger
// immediate SQLITE_BUSY, completely bypassing busy_timeout!
func TestSQLiteBeginDeferredDeadlockRepro(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "stock_deferred.db")

	// Stock SQLite DSN without _txlock=immediate (defaults to BEGIN DEFERRED).
	dsn := testSQLiteDSN(dbPath, false)

	dbA, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbA: %v", err)
	}
	defer func() { _ = dbA.Close() }()

	dbB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbB: %v", err)
	}
	defer func() { _ = dbB.Close() }()

	if _, err := dbA.Exec("CREATE TABLE sessions (id TEXT PRIMARY KEY, summary TEXT); INSERT INTO sessions (id, summary) VALUES ('test-session', 'init');"); err != nil {
		t.Fatalf("init table: %v", err)
	}

	// 1. Transaction A begins (DEFERRED)
	txA, err := dbA.Begin()
	if err != nil {
		t.Fatalf("txA begin: %v", err)
	}
	defer func() { _ = txA.Rollback() }()

	// 2. Transaction B begins (DEFERRED)
	txB, err := dbB.Begin()
	if err != nil {
		t.Fatalf("txB begin: %v", err)
	}
	defer func() { _ = txB.Rollback() }()

	// 3. Both do a SELECT (both acquire SHARED read locks)
	var countA, countB int
	if err := txA.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&countA); err != nil {
		t.Fatalf("txA select: %v", err)
	}
	if err := txB.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&countB); err != nil {
		t.Fatalf("txB select: %v", err)
	}

	// 4. txA attempts to write (promotes SHARED -> RESERVED)
	if _, err := txA.Exec("UPDATE sessions SET summary = 'summary A' WHERE id = 'test-session'"); err != nil {
		t.Fatalf("txA exec: %v", err)
	}

	// 5. Now txB attempts to write (needs RESERVED lock, but txA has it, and txA cannot commit because txB holds SHARED lock)
	// SQLite detects the deadlock and returns SQLITE_BUSY IMMEDIATELY in ~0.03s, bypassing busy_timeout!
	_, errB := txB.Exec("UPDATE sessions SET summary = 'summary B' WHERE id = 'test-session'")
	if errB == nil {
		t.Fatalf("Expected txB to fail with SQLITE_BUSY deadlock, but it succeeded!")
	}

	t.Logf("Observed immediate deadlock failure on txB: %v", errB)
	if !isRetryableSQLiteLockError(errB) {
		t.Fatalf("Expected retryable SQLite lock error, got %v", errB)
	}
}

// TestSQLiteBeginImmediatePreventsDeadlock demonstrates that when transactions use
// BEGIN IMMEDIATE, SQLite serializes writers gracefully via busy_timeout instead of deadlocking!
func TestSQLiteBeginImmediatePreventsDeadlock(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "immediate.db")

	dsn := testSQLiteDSN(dbPath, false)

	dbA, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbA: %v", err)
	}
	defer func() { _ = dbA.Close() }()

	dbB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbB: %v", err)
	}
	defer func() { _ = dbB.Close() }()

	if _, err := dbA.Exec("CREATE TABLE sessions (id TEXT PRIMARY KEY, summary TEXT); INSERT INTO sessions (id, summary) VALUES ('test-session', 'init');"); err != nil {
		t.Fatalf("init table: %v", err)
	}

	connA, err := dbA.Conn(context.Background())
	if err != nil {
		t.Fatalf("connA: %v", err)
	}
	defer func() { _ = connA.Close() }()

	connB, err := dbB.Conn(context.Background())
	if err != nil {
		t.Fatalf("connB: %v", err)
	}
	defer func() { _ = connB.Close() }()

	// 1. Transaction A acquires RESERVED write lock immediately at the start of transaction
	if _, err := connA.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("connA BEGIN IMMEDIATE: %v", err)
	}

	if _, err := connB.ExecContext(context.Background(), "PRAGMA busy_timeout = 100"); err != nil {
		t.Fatalf("set connB busy_timeout: %v", err)
	}

	// 2. The bounded busy-timeout failure proves SQLite received B's BEGIN while
	// A still held its write lock; it is not merely evidence that a goroutine ran.
	assertBusyBegin(t, func() error {
		_, err := connB.ExecContext(context.Background(), "BEGIN IMMEDIATE")
		return err
	})

	// connA reads and writes while holding the RESERVED write lock.
	var countA int
	if err := connA.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sessions").Scan(&countA); err != nil {
		t.Fatalf("connA select: %v", err)
	}
	if _, err := connA.ExecContext(context.Background(), "UPDATE sessions SET summary = 'summary A' WHERE id = 'test-session'"); err != nil {
		t.Fatalf("connA update: %v", err)
	}
	if _, err := connA.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatalf("connA commit: %v", err)
	}

	// Retrying B after A releases the lock must now acquire, update, and commit.
	if _, err := connB.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("connB retry BEGIN IMMEDIATE: %v", err)
	}
	var countB int
	if err := connB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sessions").Scan(&countB); err != nil {
		t.Fatalf("connB select: %v", err)
	}
	if _, err := connB.ExecContext(context.Background(), "UPDATE sessions SET summary = 'summary B' WHERE id = 'test-session'"); err != nil {
		t.Fatalf("connB update: %v", err)
	}
	if _, err := connB.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatalf("connB commit: %v", err)
	}
}

// TestTxLockImmediateInDSN verifies that configuring _txlock=immediate in the DSN
// makes standard db.Begin() transactions safe against read-before-write deadlocks!
func TestTxLockImmediateInDSN(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "test_txlock.db")

	dsn := testSQLiteDSN(dbPath, true)

	dbA, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbA: %v", err)
	}
	defer func() { _ = dbA.Close() }()

	dbB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbB: %v", err)
	}
	defer func() { _ = dbB.Close() }()

	if _, err := dbA.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, val TEXT); INSERT INTO items (val) VALUES ('init');"); err != nil {
		t.Fatalf("init table: %v", err)
	}

	// dbA begins transaction with db.Begin() (which automatically uses BEGIN IMMEDIATE because of _txlock=immediate!)
	txA, err := dbA.Begin()
	if err != nil {
		t.Fatalf("txA begin: %v", err)
	}
	defer func() { _ = txA.Rollback() }()

	// Keep B on one connection so the bounded timeout applies to its Begin call.
	dbB.SetMaxOpenConns(1)
	if _, err := dbB.Exec("PRAGMA busy_timeout = 100"); err != nil {
		t.Fatalf("set dbB busy_timeout: %v", err)
	}
	assertBusyBegin(t, func() error {
		txB, err := dbB.Begin()
		if txB != nil {
			_ = txB.Rollback()
		}
		return err
	})

	// txA reads and writes while holding the immediate lock.
	var valA string
	if err := txA.QueryRow("SELECT val FROM items WHERE id = 1").Scan(&valA); err != nil {
		t.Fatalf("txA read: %v", err)
	}
	if _, err := txA.Exec("UPDATE items SET val = 'from A' WHERE id = 1"); err != nil {
		t.Fatalf("txA write: %v", err)
	}
	if err := txA.Commit(); err != nil {
		t.Fatalf("txA commit: %v", err)
	}

	// Retrying through db.Begin after release must succeed with _txlock=immediate.
	txB, err := dbB.Begin()
	if err != nil {
		t.Fatalf("txB retry begin: %v", err)
	}
	defer func() { _ = txB.Rollback() }()
	var valB string
	if err := txB.QueryRow("SELECT val FROM items WHERE id = 1").Scan(&valB); err != nil {
		t.Fatalf("txB read: %v", err)
	}
	if _, err := txB.Exec("UPDATE items SET val = 'from B' WHERE id = 1"); err != nil {
		t.Fatalf("txB update: %v", err)
	}
	if err := txB.Commit(); err != nil {
		t.Fatalf("txB commit: %v", err)
	}
}

// TestStoreConcurrentWritesWithTxLock verifies that multiple concurrent Store instances
// using storeDSN() with _txlock=immediate can write concurrently without SQLITE_BUSY deadlocks.
func TestStoreConcurrentWritesWithTxLock(t *testing.T) {
	dataDir := t.TempDir()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dataDir
	cfg.DedupeWindow = time.Millisecond

	initStore, err := New(cfg)
	if err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}
	if err := initStore.CreateSession("concurrent-session", "test-project", "/tmp/test"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_ = initStore.Close()

	const numWriters = 6
	const writesPerWorker = 10

	var wg sync.WaitGroup
	errCh := make(chan error, numWriters*writesPerWorker)

	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			s, err := New(cfg)
			if err != nil {
				errCh <- fmt.Errorf("worker %d New(): %w", workerID, err)
				return
			}
			defer func() { _ = s.Close() }()

			for i := 0; i < writesPerWorker; i++ {
				_, err := s.AddObservation(AddObservationParams{
					SessionID: "concurrent-session",
					Project:   "test-project",
					Type:      "decision",
					Title:     fmt.Sprintf("Worker %d write %d", workerID, i),
					Content:   fmt.Sprintf("Payload from worker %d write %d", workerID, i),
				})
				if err != nil {
					errCh <- fmt.Errorf("worker %d write %d AddObservation: %w", workerID, i, err)
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent write error: %v", err)
	}

	// Verify all concurrent writes were correctly persisted and no observations were dropped or duplicated
	verifyStore, err := New(cfg)
	if err != nil {
		t.Fatalf("open verify store: %v", err)
	}
	defer func() { _ = verifyStore.Close() }()

	const expectedTotal = numWriters * writesPerWorker

	var busyTimeout int
	if err := verifyStore.DB().QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("read Store busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("Store busy_timeout = %d, want 5000", busyTimeout)
	}

	var count int
	if err := verifyStore.DB().QueryRow("SELECT COUNT(*) FROM observations WHERE session_id = ?", "concurrent-session").Scan(&count); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if count != expectedTotal {
		t.Fatalf("expected %d persisted observations, got %d", expectedTotal, count)
	}

	var distinctTitles int
	if err := verifyStore.DB().QueryRow("SELECT COUNT(DISTINCT title) FROM observations WHERE session_id = ?", "concurrent-session").Scan(&distinctTitles); err != nil {
		t.Fatalf("count distinct observation titles: %v", err)
	}
	if distinctTitles != expectedTotal {
		t.Fatalf("expected %d distinct observation titles, got %d", expectedTotal, distinctTitles)
	}
}

const storeContentionChildEnv = "ENGRAM_TEST_STORE_CONTENTION_DIR"

func TestStoreContentionChildProcess(t *testing.T) {
	dataDir := os.Getenv(storeContentionChildEnv)
	if dataDir == "" {
		t.Skip("runs only as a child of TestStoreContentionAcrossProcesses")
	}
	childID := os.Getenv(storeContentionChildEnv + "_ID")
	if childID == "" {
		t.Fatal("missing child identifier")
	}

	cfg := mustDefaultConfig(t)
	cfg.DataDir = dataDir
	cfg.DedupeWindow = time.Hour
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("child New: %v", err)
	}
	defer func() { _ = s.Close() }()

	var busyTimeout int
	if err := s.DB().QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("child read Store busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("child Store busy_timeout = %d, want 5000", busyTimeout)
	}

	if _, err := s.AddObservation(AddObservationParams{
		SessionID: "contention-session",
		Project:   "contention-project",
		Type:      "decision",
		Title:     "child-process-" + childID,
		Content:   "child-process-content-" + childID,
	}); err != nil {
		t.Fatalf("child AddObservation: %v", err)
	}
}

func TestStoreContentionAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("runs child Store processes")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dataDir := t.TempDir()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dataDir
	cfg.DedupeWindow = time.Hour
	bootstrap, err := New(cfg)
	if err != nil {
		t.Fatalf("bootstrap New: %v", err)
	}
	if err := bootstrap.CreateSession("contention-session", "contention-project", dataDir); err != nil {
		_ = bootstrap.Close()
		t.Fatalf("create contention session: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap Store: %v", err)
	}

	const childCount = 3
	cmds := make([]*exec.Cmd, childCount)
	outputs := make([]*strings.Builder, childCount)
	childrenWaited := false
	t.Cleanup(func() {
		if childrenWaited {
			return
		}
		for _, cmd := range cmds {
			if cmd != nil && cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
			}
		}
		for _, cmd := range cmds {
			if cmd != nil && cmd.ProcessState == nil {
				_ = cmd.Wait()
			}
		}
	})
	for i := range cmds {
		outputs[i] = &strings.Builder{}
		cmd := exec.Command(exe, "-test.run", "^TestStoreContentionChildProcess$", "-test.v", "-test.timeout", "60s")
		cmd.Env = append(os.Environ(), storeContentionChildEnv+"="+dataDir, fmt.Sprintf("%s_ID=%d", storeContentionChildEnv, i))
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
	childrenWaited = true
	if t.Failed() {
		return
	}

	verify, err := New(cfg)
	if err != nil {
		t.Fatalf("open verification Store: %v", err)
	}
	defer func() { _ = verify.Close() }()

	var count, distinctTitles int
	if err := verify.DB().QueryRow("SELECT COUNT(*), COUNT(DISTINCT title) FROM observations WHERE session_id = ?", "contention-session").Scan(&count, &distinctTitles); err != nil {
		t.Fatalf("verify child observations: %v", err)
	}
	if count != childCount || distinctTitles != childCount {
		t.Fatalf("child observations count=%d distinct titles=%d, want %d", count, distinctTitles, childCount)
	}
}
