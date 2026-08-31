package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func disableDatabaseGenerationCheckThrottle(generation *databaseGeneration) {
	generation.checkInterval = 0
}

func TestDatabaseGenerationDetectsReplacedSidecar(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatalf("write %s: %v", filepath.Base(path), err)
		}
	}

	generation := newDatabaseGeneration(dbPath)
	disableDatabaseGenerationCheckThrottle(generation)
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}
	replacement := dbPath + "-wal.replacement"
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement WAL: %v", err)
	}
	if err := os.Remove(dbPath + "-wal"); err != nil {
		t.Fatalf("remove original WAL: %v", err)
	}
	if err := os.Rename(replacement, dbPath+"-wal"); err != nil {
		t.Fatalf("replace WAL: %v", err)
	}

	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("generation check error = %v, want ErrDatabaseGenerationChanged", err)
	}
}

// This cross-platform test proves that WAL and SHM are optional at startup and
// become tracked on their first legitimate appearance. It does not model SQLite mmap behavior.
func TestDatabaseGenerationAllowsLateSidecars(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "engram.db")
	if err := os.WriteFile(dbPath, []byte("database"), 0o600); err != nil {
		t.Fatalf("write database: %v", err)
	}
	generation := newDatabaseGeneration(dbPath)
	disableDatabaseGenerationCheckThrottle(generation)
	if err := generation.capture(); err != nil {
		t.Fatalf("capture without sidecars: %v", err)
	}
	if err := generation.check(); err != nil {
		t.Fatalf("check without sidecars: %v", err)
	}
	if err := os.WriteFile(dbPath+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatalf("create WAL: %v", err)
	}
	if err := generation.check(); err != nil {
		t.Fatalf("accept first WAL appearance: %v", err)
	}
	if err := os.WriteFile(dbPath+"-shm", []byte("shm"), 0o600); err != nil {
		t.Fatalf("create SHM: %v", err)
	}
	if err := generation.check(); err != nil {
		t.Fatalf("accept first SHM appearance: %v", err)
	}
}

func TestDatabaseGenerationRejectsSidecarDisappearance(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatalf("write %s: %v", filepath.Base(path), err)
		}
	}
	generation := newDatabaseGeneration(dbPath)
	disableDatabaseGenerationCheckThrottle(generation)
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}
	if err := os.Remove(dbPath + "-shm"); err != nil {
		t.Fatalf("remove SHM: %v", err)
	}
	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("disappearance error = %v, want ErrDatabaseGenerationChanged", err)
	}
	if err := os.WriteFile(dbPath+"-shm", []byte("replacement"), 0o600); err != nil {
		t.Fatalf("recreate SHM: %v", err)
	}
	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("reappearance error = %v, want ErrDatabaseGenerationChanged", err)
	}
}

func TestDatabaseGenerationRejectsDatabaseDisappearance(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "engram.db")
	if err := os.WriteFile(dbPath, []byte("database"), 0o600); err != nil {
		t.Fatalf("write database: %v", err)
	}
	generation := newDatabaseGeneration(dbPath)
	disableDatabaseGenerationCheckThrottle(generation)
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove database: %v", err)
	}

	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("disappearance error = %v, want ErrDatabaseGenerationChanged", err)
	}
	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("sticky disappearance error = %v, want ErrDatabaseGenerationChanged", err)
	}
}

func TestDatabaseGenerationPreservesIdentityReadErrors(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "engram.db")
	if err := os.WriteFile(dbPath, []byte("database"), 0o600); err != nil {
		t.Fatalf("write database: %v", err)
	}
	generation := newDatabaseGeneration(dbPath)
	disableDatabaseGenerationCheckThrottle(generation)
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}
	readErr := errors.New("permission denied")
	original := readDatabaseFileID
	t.Cleanup(func() { readDatabaseFileID = original })
	readDatabaseFileID = func(path string) (databaseFileIdentity, error) {
		if path == dbPath+"-wal" {
			return "", readErr
		}
		return original(path)
	}

	err := generation.check()
	if !errors.Is(err, readErr) {
		t.Fatalf("check error = %v, want wrapped read error", err)
	}
	if errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("check error = %v, must not be a generation replacement", err)
	}
}

func TestDatabaseGenerationThrottlesIdentityScans(t *testing.T) {
	now := time.Unix(1, 0)
	generation := newDatabaseGeneration("engram.db")
	generation.now = func() time.Time { return now }

	reads := 0
	original := readDatabaseFileID
	t.Cleanup(func() { readDatabaseFileID = original })
	readDatabaseFileID = func(path string) (databaseFileIdentity, error) {
		reads++
		return databaseFileIdentity(path), nil
	}
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}
	if reads != 3 {
		t.Fatalf("capture identity reads = %d, want 3", reads)
	}
	if err := generation.check(); err != nil {
		t.Fatalf("throttled check: %v", err)
	}
	if reads != 3 {
		t.Fatalf("throttled identity reads = %d, want 3", reads)
	}

	now = now.Add(databaseGenerationCheckInterval)
	if err := generation.check(); err != nil {
		t.Fatalf("interval check: %v", err)
	}
	if reads != 6 {
		t.Fatalf("interval identity reads = %d, want 6", reads)
	}
}

func TestDatabaseGenerationDetectsReplacementAfterThrottleInterval(t *testing.T) {
	now := time.Unix(1, 0)
	generation := newDatabaseGeneration("engram.db")
	generation.now = func() time.Time { return now }

	replaced := false
	original := readDatabaseFileID
	t.Cleanup(func() { readDatabaseFileID = original })
	readDatabaseFileID = func(path string) (databaseFileIdentity, error) {
		if path == "engram.db" && replaced {
			return "replacement", nil
		}
		return "original", nil
	}
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}

	replaced = true
	if err := generation.check(); err != nil {
		t.Fatalf("throttled replacement check: %v", err)
	}
	now = now.Add(databaseGenerationCheckInterval)
	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("interval replacement check = %v, want ErrDatabaseGenerationChanged", err)
	}
}

func TestDatabaseGenerationDetectsDatabaseDisappearanceAfterThrottleInterval(t *testing.T) {
	now := time.Unix(1, 0)
	generation := newDatabaseGeneration("engram.db")
	generation.now = func() time.Time { return now }

	missing := false
	original := readDatabaseFileID
	t.Cleanup(func() { readDatabaseFileID = original })
	readDatabaseFileID = func(path string) (databaseFileIdentity, error) {
		if path == "engram.db" && missing {
			return "", os.ErrNotExist
		}
		return "original", nil
	}
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}

	missing = true
	now = now.Add(databaseGenerationCheckInterval)
	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("interval disappearance check = %v, want ErrDatabaseGenerationChanged", err)
	}
}

func TestDatabaseGenerationAdoptsLateSidecarAfterThrottleInterval(t *testing.T) {
	now := time.Unix(1, 0)
	generation := newDatabaseGeneration("engram.db")
	generation.now = func() time.Time { return now }

	walPresent := false
	original := readDatabaseFileID
	t.Cleanup(func() { readDatabaseFileID = original })
	readDatabaseFileID = func(path string) (databaseFileIdentity, error) {
		if path == "engram.db-wal" && !walPresent {
			return "", os.ErrNotExist
		}
		return databaseFileIdentity(path), nil
	}
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}

	walPresent = true
	now = now.Add(databaseGenerationCheckInterval)
	if err := generation.check(); err != nil {
		t.Fatalf("adopt late WAL: %v", err)
	}
	if !generation.files[1].present {
		t.Fatal("late WAL was not recorded")
	}
}

func TestDatabaseGenerationDoesNotScanAfterStickyFailure(t *testing.T) {
	now := time.Unix(1, 0)
	generation := newDatabaseGeneration("engram.db")
	generation.now = func() time.Time { return now }

	reads := 0
	replaced := false
	original := readDatabaseFileID
	t.Cleanup(func() { readDatabaseFileID = original })
	readDatabaseFileID = func(path string) (databaseFileIdentity, error) {
		reads++
		if path == "engram.db" && replaced {
			return "replacement", nil
		}
		return "original", nil
	}
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}

	replaced = true
	now = now.Add(databaseGenerationCheckInterval)
	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("replacement check = %v, want ErrDatabaseGenerationChanged", err)
	}
	readsAfterFailure := reads
	if err := generation.check(); !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("sticky check = %v, want ErrDatabaseGenerationChanged", err)
	}
	if reads != readsAfterFailure {
		t.Fatalf("identity reads after sticky failure = %d, want %d", reads, readsAfterFailure)
	}
}

type generationGuardTestTx struct {
	commitCalls   int
	rollbackCalls int
	commitErr     error
	rollbackErr   error
}

type generationGuardTestConn struct {
	tx           driver.Tx
	rows         driver.Rows
	beginCalls   int
	beginTxCalls int
	queryCalls   int
	preparedStmt driver.Stmt
}

func (c *generationGuardTestConn) Prepare(string) (driver.Stmt, error) {
	return c.preparedStmt, nil
}

func (*generationGuardTestConn) Close() error { return nil }

func (c *generationGuardTestConn) Begin() (driver.Tx, error) {
	c.beginCalls++
	return c.tx, nil
}

func (c *generationGuardTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.queryCalls++
	return c.rows, nil
}

type generationGuardTestConnBeginTx struct{ *generationGuardTestConn }

func (c *generationGuardTestConnBeginTx) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.beginTxCalls++
	return c.tx, nil
}

type generationGuardTestStmt struct{ rows driver.Rows }

func (*generationGuardTestStmt) Close() error  { return nil }
func (*generationGuardTestStmt) NumInput() int { return -1 }
func (*generationGuardTestStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.ResultNoRows, nil
}
func (s *generationGuardTestStmt) Query([]driver.Value) (driver.Rows, error) { return s.rows, nil }
func (s *generationGuardTestStmt) QueryContext(context.Context, []driver.NamedValue) (driver.Rows, error) {
	return s.rows, nil
}

func (t *generationGuardTestTx) Commit() error {
	t.commitCalls++
	return t.commitErr
}

func (t *generationGuardTestTx) Rollback() error {
	t.rollbackCalls++
	return t.rollbackErr
}

type generationGuardTestRows struct {
	nextCalls          int
	nextResultSetCalls int
	closed             bool
}

func (r *generationGuardTestRows) Columns() []string { return []string{"value"} }

func (r *generationGuardTestRows) Close() error {
	r.closed = true
	return nil
}

func (r *generationGuardTestRows) Next(dest []driver.Value) error {
	r.nextCalls++
	dest[0] = int64(42)
	return nil
}

func (r *generationGuardTestRows) HasNextResultSet() bool { return true }

func (r *generationGuardTestRows) NextResultSet() error {
	r.nextResultSetCalls++
	return nil
}

func (r *generationGuardTestRows) ColumnTypeScanType(int) reflect.Type {
	return reflect.TypeOf(int64(0))
}

func (r *generationGuardTestRows) ColumnTypeDatabaseTypeName(int) string { return "INTEGER" }

func (r *generationGuardTestRows) ColumnTypeLength(int) (int64, bool) { return 42, true }

func (r *generationGuardTestRows) ColumnTypeNullable(int) (bool, bool) { return true, true }

func (r *generationGuardTestRows) ColumnTypePrecisionScale(int) (int64, int64, bool) {
	return 12, 3, true
}

func changedDatabaseGeneration() *databaseGeneration {
	return &databaseGeneration{enabled: true, changed: true}
}

func TestGenerationTxCommitBlocksChangedGenerationAndRollsBack(t *testing.T) {
	tx := &generationGuardTestTx{}
	err := (generationTx{Tx: tx, generation: changedDatabaseGeneration()}).Commit()
	if !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("commit error = %v, want ErrDatabaseGenerationChanged", err)
	}
	if tx.commitCalls != 0 {
		t.Fatalf("underlying commit calls = %d, want 0", tx.commitCalls)
	}
	if tx.rollbackCalls != 1 {
		t.Fatalf("underlying rollback calls = %d, want 1", tx.rollbackCalls)
	}
}

func TestGenerationTxRollbackPreservesGenerationAndCleanupErrors(t *testing.T) {
	rollbackErr := errors.New("rollback failed")
	tx := &generationGuardTestTx{rollbackErr: rollbackErr}
	err := (generationTx{Tx: tx, generation: changedDatabaseGeneration()}).Rollback()
	if !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("rollback error = %v, want ErrDatabaseGenerationChanged", err)
	}
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback error = %v, want wrapped cleanup error", err)
	}
	if tx.rollbackCalls != 1 {
		t.Fatalf("underlying rollback calls = %d, want 1", tx.rollbackCalls)
	}
}

func TestGenerationTxDelegatesNormalCommitAndRollback(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		tx := &generationGuardTestTx{}
		if err := (generationTx{Tx: tx, generation: &databaseGeneration{}}).Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if tx.commitCalls != 1 || tx.rollbackCalls != 0 {
			t.Fatalf("commit calls = %d, rollback calls = %d", tx.commitCalls, tx.rollbackCalls)
		}
	})
	t.Run("rollback", func(t *testing.T) {
		tx := &generationGuardTestTx{}
		if err := (generationTx{Tx: tx, generation: &databaseGeneration{}}).Rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
			t.Fatalf("commit calls = %d, rollback calls = %d", tx.commitCalls, tx.rollbackCalls)
		}
	})
}

func TestGenerationConnWrapsBeginAndBeginTx(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		raw := &generationGuardTestConn{tx: &generationGuardTestTx{}}
		tx, err := (generationConn{Conn: raw, generation: &databaseGeneration{}}).Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, ok := tx.(generationTx); !ok {
			t.Fatalf("begin transaction type = %T, want generationTx", tx)
		}
		if raw.beginCalls != 1 {
			t.Fatalf("underlying begin calls = %d, want 1", raw.beginCalls)
		}
	})
	t.Run("begin transaction fallback", func(t *testing.T) {
		raw := &generationGuardTestConn{tx: &generationGuardTestTx{}}
		tx, err := (generationConn{Conn: raw, generation: &databaseGeneration{}}).BeginTx(context.Background(), driver.TxOptions{})
		if err != nil {
			t.Fatalf("begin transaction: %v", err)
		}
		if _, ok := tx.(generationTx); !ok {
			t.Fatalf("begin transaction type = %T, want generationTx", tx)
		}
		if raw.beginCalls != 1 {
			t.Fatalf("underlying begin calls = %d, want 1", raw.beginCalls)
		}
	})
	t.Run("begin transaction native", func(t *testing.T) {
		raw := &generationGuardTestConnBeginTx{generationGuardTestConn: &generationGuardTestConn{tx: &generationGuardTestTx{}}}
		tx, err := (generationConn{Conn: raw, generation: &databaseGeneration{}}).BeginTx(context.Background(), driver.TxOptions{})
		if err != nil {
			t.Fatalf("begin transaction: %v", err)
		}
		if _, ok := tx.(generationTx); !ok {
			t.Fatalf("begin transaction type = %T, want generationTx", tx)
		}
		if raw.beginTxCalls != 1 {
			t.Fatalf("underlying begin transaction calls = %d, want 1", raw.beginTxCalls)
		}
	})
}

func TestGenerationConnAndStmtWrapRows(t *testing.T) {
	rawRows := &generationGuardTestRows{}
	rawConn := &generationGuardTestConn{rows: rawRows}
	guardedConn := generationConn{Conn: rawConn, generation: &databaseGeneration{}}
	rows, err := guardedConn.QueryContext(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatalf("connection query: %v", err)
	}
	if _, ok := rows.(generationRows); !ok {
		t.Fatalf("connection rows type = %T, want generationRows", rows)
	}

	guardedStmt := generationStmt{Stmt: &generationGuardTestStmt{rows: rawRows}, generation: &databaseGeneration{}}
	for _, query := range []struct {
		name string
		run  func() (driver.Rows, error)
	}{
		{name: "query", run: func() (driver.Rows, error) { return guardedStmt.Query(nil) }},
		{name: "query context", run: func() (driver.Rows, error) { return guardedStmt.QueryContext(context.Background(), nil) }},
	} {
		t.Run(query.name, func(t *testing.T) {
			rows, err := query.run()
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if _, ok := rows.(generationRows); !ok {
				t.Fatalf("statement rows type = %T, want generationRows", rows)
			}
		})
	}
}

func TestGenerationRowsBlocksNextAfterGenerationChange(t *testing.T) {
	rows := &generationGuardTestRows{}
	err := (generationRows{Rows: rows, generation: changedDatabaseGeneration()}).Next(make([]driver.Value, 1))
	if !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("next error = %v, want ErrDatabaseGenerationChanged", err)
	}
	if rows.nextCalls != 0 {
		t.Fatalf("underlying next calls = %d, want 0", rows.nextCalls)
	}
}

func TestGenerationRowsDelegatesAndPreservesOptionalInterfaces(t *testing.T) {
	raw := &generationGuardTestRows{}
	rows := generationRows{Rows: raw, generation: &databaseGeneration{}}
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil {
		t.Fatalf("next: %v", err)
	}
	if raw.nextCalls != 1 || dest[0] != int64(42) {
		t.Fatalf("next calls = %d, destination = %v", raw.nextCalls, dest)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !raw.closed {
		t.Fatal("underlying rows were not closed")
	}

	var driverRows driver.Rows = rows
	if got := driverRows.(driver.RowsColumnTypeScanType).ColumnTypeScanType(0); got != reflect.TypeOf(int64(0)) {
		t.Fatalf("scan type = %v, want int64", got)
	}
	if got := driverRows.(driver.RowsColumnTypeDatabaseTypeName).ColumnTypeDatabaseTypeName(0); got != "INTEGER" {
		t.Fatalf("database type = %q, want INTEGER", got)
	}
	if length, ok := driverRows.(driver.RowsColumnTypeLength).ColumnTypeLength(0); length != 42 || !ok {
		t.Fatalf("length = (%d, %t), want (42, true)", length, ok)
	}
	if nullable, ok := driverRows.(driver.RowsColumnTypeNullable).ColumnTypeNullable(0); !nullable || !ok {
		t.Fatalf("nullable = (%t, %t), want (true, true)", nullable, ok)
	}
	if precision, scale, ok := driverRows.(driver.RowsColumnTypePrecisionScale).ColumnTypePrecisionScale(0); precision != 12 || scale != 3 || !ok {
		t.Fatalf("precision and scale = (%d, %d, %t), want (12, 3, true)", precision, scale, ok)
	}
	resultSets := driverRows.(driver.RowsNextResultSet)
	if !resultSets.HasNextResultSet() {
		t.Fatal("next result set support was not preserved")
	}
	if err := resultSets.NextResultSet(); err != nil {
		t.Fatalf("next result set: %v", err)
	}
	if raw.nextResultSetCalls != 1 {
		t.Fatalf("underlying next result set calls = %d, want 1", raw.nextResultSetCalls)
	}
}

func TestGenerationRowsNextResultSetBlocksChangedGeneration(t *testing.T) {
	raw := &generationGuardTestRows{}
	err := (generationRows{Rows: raw, generation: changedDatabaseGeneration()}).NextResultSet()
	if !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("next result set error = %v, want ErrDatabaseGenerationChanged", err)
	}
	if raw.nextResultSetCalls != 0 {
		t.Fatalf("underlying next result set calls = %d, want 0", raw.nextResultSetCalls)
	}
}

func TestGenerationRowsReturnsEOFWithoutMultipleResultSets(t *testing.T) {
	rows := generationRows{Rows: &generationGuardTestRows{}, generation: &databaseGeneration{}}
	rows.Rows = generationGuardRowsWithoutResultSets{}
	if err := rows.NextResultSet(); !errors.Is(err, io.EOF) {
		t.Fatalf("next result set error = %v, want io.EOF", err)
	}
}

type generationGuardRowsWithoutResultSets struct{}

func (generationGuardRowsWithoutResultSets) Columns() []string         { return []string{"value"} }
func (generationGuardRowsWithoutResultSets) Close() error              { return nil }
func (generationGuardRowsWithoutResultSets) Next([]driver.Value) error { return io.EOF }
