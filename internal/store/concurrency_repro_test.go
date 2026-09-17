package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestSQLiteBeginDeferredDeadlockRepro demonstrates that two connections performing
// read-before-write in a standard transaction (BEGIN DEFERRED) will deadlock and trigger
// immediate SQLITE_BUSY, completely bypassing busy_timeout!
func TestSQLiteBeginDeferredDeadlockRepro(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "stock_deferred.db")

	// Stock SQLite DSN without _txlock=immediate (defaults to BEGIN DEFERRED)
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	dsn := (&url.URL{Scheme: "file", Path: dbPath}).String() + "?" + q.Encode()

	dbA, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbA: %v", err)
	}
	defer dbA.Close()

	dbB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbB: %v", err)
	}
	defer dbB.Close()

	if _, err := dbA.Exec("CREATE TABLE sessions (id TEXT PRIMARY KEY, summary TEXT); INSERT INTO sessions (id, summary) VALUES ('test-session', 'init');"); err != nil {
		t.Fatalf("init table: %v", err)
	}

	// 1. Transaction A begins (DEFERRED)
	txA, err := dbA.Begin()
	if err != nil {
		t.Fatalf("txA begin: %v", err)
	}
	defer txA.Rollback()

	// 2. Transaction B begins (DEFERRED)
	txB, err := dbB.Begin()
	if err != nil {
		t.Fatalf("txB begin: %v", err)
	}
	defer txB.Rollback()

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

	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	dsn := (&url.URL{Scheme: "file", Path: dbPath}).String() + "?" + q.Encode()

	dbA, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbA: %v", err)
	}
	defer dbA.Close()

	dbB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbB: %v", err)
	}
	defer dbB.Close()

	if _, err := dbA.Exec("CREATE TABLE sessions (id TEXT PRIMARY KEY, summary TEXT); INSERT INTO sessions (id, summary) VALUES ('test-session', 'init');"); err != nil {
		t.Fatalf("init table: %v", err)
	}

	connA, err := dbA.Conn(context.Background())
	if err != nil {
		t.Fatalf("connA: %v", err)
	}
	defer connA.Close()

	connB, err := dbB.Conn(context.Background())
	if err != nil {
		t.Fatalf("connB: %v", err)
	}
	defer connB.Close()

	// 1. Transaction A acquires RESERVED write lock immediately at the start of transaction
	if _, err := connA.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("connA BEGIN IMMEDIATE: %v", err)
	}

	txBAttemptStarted := make(chan struct{})
	txBDone := make(chan error, 1)

	// 2. Transaction B also attempts BEGIN IMMEDIATE in another goroutine.
	// Since connA holds the RESERVED lock, connB enters busy_timeout (waits up to 5s) instead of deadlocking!
	go func() {
		close(txBAttemptStarted)
		_, err := connB.ExecContext(context.Background(), "BEGIN IMMEDIATE")
		if err != nil {
			txBDone <- fmt.Errorf("connB BEGIN IMMEDIATE failed: %w", err)
			return
		}
		var count int
		_ = connB.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sessions").Scan(&count)
		_, err = connB.ExecContext(context.Background(), "UPDATE sessions SET summary = 'summary B' WHERE id = 'test-session'")
		if err != nil {
			txBDone <- fmt.Errorf("connB update failed: %w", err)
			return
		}
		_, err = connB.ExecContext(context.Background(), "COMMIT")
		txBDone <- err
	}()

	// Wait until goroutine B has started its BEGIN IMMEDIATE attempt
	<-txBAttemptStarted

	// connA reads and writes while holding the RESERVED write lock
	var countA int
	if err := connA.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sessions").Scan(&countA); err != nil {
		t.Fatalf("connA select: %v", err)
	}
	if _, err := connA.ExecContext(context.Background(), "UPDATE sessions SET summary = 'summary A' WHERE id = 'test-session'"); err != nil {
		t.Fatalf("connA update: %v", err)
	}

	// Verify connB has not finished prematurely (it must be blocked waiting on connA)
	select {
	case err := <-txBDone:
		t.Fatalf("connB should still be waiting on connA, but returned early: %v", err)
	default:
	}

	// connA commits and releases write lock
	if _, err := connA.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatalf("connA commit: %v", err)
	}

	// Now connB unblocks, finishes, and commits without any error!
	select {
	case err := <-txBDone:
		if err != nil {
			t.Fatalf("connB failed: %v", err)
		}
		t.Logf("connB completed successfully after waiting for connA!")
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for connB")
	}
}

// TestTxLockImmediateInDSN verifies that configuring _txlock=immediate in the DSN
// makes standard db.Begin() transactions safe against read-before-write deadlocks!
func TestTxLockImmediateInDSN(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "test_txlock.db")

	q := url.Values{}
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	dsn := (&url.URL{Scheme: "file", Path: dbPath}).String() + "?" + q.Encode()

	dbA, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbA: %v", err)
	}
	defer dbA.Close()

	dbB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open dbB: %v", err)
	}
	defer dbB.Close()

	if _, err := dbA.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, val TEXT); INSERT INTO items (val) VALUES ('init');"); err != nil {
		t.Fatalf("init table: %v", err)
	}

	// dbA begins transaction with db.Begin() (which automatically uses BEGIN IMMEDIATE because of _txlock=immediate!)
	txA, err := dbA.Begin()
	if err != nil {
		t.Fatalf("txA begin: %v", err)
	}

	txBAttemptStarted := make(chan struct{})
	txBDone := make(chan error, 1)
	go func() {
		close(txBAttemptStarted)
		// dbB also calls db.Begin(). Because _txlock=immediate is active, it enters busy_timeout waiting for txA!
		txB, err := dbB.Begin()
		if err != nil {
			txBDone <- fmt.Errorf("txB begin: %w", err)
			return
		}
		var val string
		_ = txB.QueryRow("SELECT val FROM items WHERE id = 1").Scan(&val)
		_, err = txB.Exec("UPDATE items SET val = 'from B' WHERE id = 1")
		if err != nil {
			txBDone <- fmt.Errorf("txB update: %w", err)
			_ = txB.Rollback()
			return
		}
		txBDone <- txB.Commit()
	}()

	// Wait until goroutine B has started its dbB.Begin() attempt
	<-txBAttemptStarted

	// txA reads and writes while holding the immediate lock
	var valA string
	if err := txA.QueryRow("SELECT val FROM items WHERE id = 1").Scan(&valA); err != nil {
		t.Fatalf("txA read: %v", err)
	}
	if _, err := txA.Exec("UPDATE items SET val = 'from A' WHERE id = 1"); err != nil {
		t.Fatalf("txA write: %v", err)
	}

	// Verify txB has not finished prematurely (it must be blocked waiting on txA)
	select {
	case err := <-txBDone:
		t.Fatalf("txB should still be waiting on txA, but returned early: %v", err)
	default:
	}

	if err := txA.Commit(); err != nil {
		t.Fatalf("txA commit: %v", err)
	}

	// txB should unblock and succeed cleanly!
	select {
	case err := <-txBDone:
		if err != nil {
			t.Fatalf("txB failed: %v", err)
		}
		t.Logf("txB with _txlock=immediate succeeded without SQLITE_BUSY deadlock!")
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for txB")
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
			defer s.Close()

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
	defer verifyStore.Close()

	const expectedTotal = numWriters * writesPerWorker

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
