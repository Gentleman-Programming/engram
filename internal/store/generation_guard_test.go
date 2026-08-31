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
)

func TestDatabaseGenerationDetectsReplacedSidecar(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "engram.db")
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
			t.Fatalf("write %s: %v", filepath.Base(path), err)
		}
	}

	generation := newDatabaseGeneration(dbPath)
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

func TestDatabaseGenerationDoesNotScanAfterStickyFailure(t *testing.T) {
	generation := newDatabaseGeneration("engram.db")

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
	duringCall    func()
}

type generationGuardTestConn struct {
	tx           driver.Tx
	rows         driver.Rows
	closed       bool
	beginCalls   int
	beginTxCalls int
	queryCalls   int
	execCalls    int
	prepareCalls int
	preparedStmt driver.Stmt
	duringCall   func()
}

func (c *generationGuardTestConn) Prepare(string) (driver.Stmt, error) {
	c.prepareCalls++
	if c.duringCall != nil {
		c.duringCall()
	}
	return c.preparedStmt, nil
}

func (c *generationGuardTestConn) Close() error { c.closed = true; return nil }

func (c *generationGuardTestConn) Begin() (driver.Tx, error) {
	c.beginCalls++
	if c.duringCall != nil {
		c.duringCall()
	}
	return c.tx, nil
}

func (c *generationGuardTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.queryCalls++
	if c.duringCall != nil {
		c.duringCall()
	}
	return c.rows, nil
}

func (c *generationGuardTestConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.execCalls++
	if c.duringCall != nil {
		c.duringCall()
	}
	return driver.ResultNoRows, nil
}

type generationGuardTestConnBeginTx struct{ *generationGuardTestConn }

func (c *generationGuardTestConnBeginTx) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.beginTxCalls++
	if c.duringCall != nil {
		c.duringCall()
	}
	return c.tx, nil
}

type generationGuardTestDriver struct {
	conn       driver.Conn
	duringCall func()
}

func (d generationGuardTestDriver) Open(string) (driver.Conn, error) {
	if d.duringCall != nil {
		d.duringCall()
	}
	return d.conn, nil
}

type generationGuardTestStmt struct {
	rows       driver.Rows
	closed     bool
	duringCall func()
}

func (s *generationGuardTestStmt) Close() error { s.closed = true; return nil }
func (*generationGuardTestStmt) NumInput() int  { return -1 }
func (s *generationGuardTestStmt) Exec([]driver.Value) (driver.Result, error) {
	if s.duringCall != nil {
		s.duringCall()
	}
	return driver.ResultNoRows, nil
}
func (s *generationGuardTestStmt) Query([]driver.Value) (driver.Rows, error) {
	if s.duringCall != nil {
		s.duringCall()
	}
	return s.rows, nil
}
func (s *generationGuardTestStmt) QueryContext(context.Context, []driver.NamedValue) (driver.Rows, error) {
	if s.duringCall != nil {
		s.duringCall()
	}
	return s.rows, nil
}

func (t *generationGuardTestTx) Commit() error {
	t.commitCalls++
	if t.duringCall != nil {
		t.duringCall()
	}
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
	duringCall         func()
	nextErr            error
	nextResultSetErr   error
}

func (r *generationGuardTestRows) Columns() []string { return []string{"value"} }

func (r *generationGuardTestRows) Close() error {
	r.closed = true
	return nil
}

func (r *generationGuardTestRows) Next(dest []driver.Value) error {
	r.nextCalls++
	if r.duringCall != nil {
		r.duringCall()
	}
	if r.nextErr != nil {
		return r.nextErr
	}
	dest[0] = int64(42)
	return nil
}

func (r *generationGuardTestRows) HasNextResultSet() bool { return true }

func (r *generationGuardTestRows) NextResultSet() error {
	r.nextResultSetCalls++
	if r.duringCall != nil {
		r.duringCall()
	}
	return r.nextResultSetErr
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

func generationChangedDuringCall(t *testing.T) (*databaseGeneration, func()) {
	t.Helper()
	replaced := false
	original := readDatabaseFileID
	t.Cleanup(func() { readDatabaseFileID = original })
	readDatabaseFileID = func(string) (databaseFileIdentity, error) {
		if replaced {
			return "replacement", nil
		}
		return "original", nil
	}
	generation := newDatabaseGeneration("engram.db")
	if err := generation.capture(); err != nil {
		t.Fatalf("capture generation: %v", err)
	}
	return generation, func() { replaced = true }
}

func TestGenerationGuardRejectsSuccessAfterGenerationChangesDuringDriverCall(t *testing.T) {
	t.Run("connect closes connection and releases lease", func(t *testing.T) {
		generation, replace := generationChangedDuringCall(t)
		raw := &generationGuardTestConn{}
		dir := t.TempDir()
		connector := generationConnector{
			dsn:        filepath.Join(dir, "engram.db"),
			driver:     generationGuardTestDriver{conn: raw, duringCall: replace},
			generation: generation,
		}
		_, err := connector.Connect(context.Background())
		if !errors.Is(err, ErrDatabaseGenerationChanged) {
			t.Fatalf("connect error = %v, want ErrDatabaseGenerationChanged", err)
		}
		if !raw.closed {
			t.Fatal("connection was not closed")
		}
		lease, err := acquireStoreLease(dir, false)
		if err != nil {
			t.Fatalf("acquire released lease: %v", err)
		}
		if err := lease.Close(); err != nil {
			t.Fatalf("close released lease: %v", err)
		}
	})

	t.Run("exec", func(t *testing.T) {
		generation, replace := generationChangedDuringCall(t)
		raw := &generationGuardTestConn{duringCall: replace}
		_, err := (generationConn{Conn: raw, generation: generation}).ExecContext(context.Background(), "UPDATE observations", nil)
		if !errors.Is(err, ErrDatabaseGenerationChanged) {
			t.Fatalf("exec error = %v, want ErrDatabaseGenerationChanged", err)
		}
	})

	t.Run("query closes rows", func(t *testing.T) {
		generation, replace := generationChangedDuringCall(t)
		rawRows := &generationGuardTestRows{}
		raw := &generationGuardTestConn{rows: rawRows, duringCall: replace}
		_, err := (generationConn{Conn: raw, generation: generation}).QueryContext(context.Background(), "SELECT 1", nil)
		if !errors.Is(err, ErrDatabaseGenerationChanged) {
			t.Fatalf("query error = %v, want ErrDatabaseGenerationChanged", err)
		}
		if !rawRows.closed {
			t.Fatal("query rows were not closed")
		}
	})

	t.Run("prepare closes statement", func(t *testing.T) {
		generation, replace := generationChangedDuringCall(t)
		rawStmt := &generationGuardTestStmt{}
		raw := &generationGuardTestConn{preparedStmt: rawStmt, duringCall: replace}
		_, err := (generationConn{Conn: raw, generation: generation}).Prepare("SELECT 1")
		if !errors.Is(err, ErrDatabaseGenerationChanged) {
			t.Fatalf("prepare error = %v, want ErrDatabaseGenerationChanged", err)
		}
		if !rawStmt.closed {
			t.Fatal("prepared statement was not closed")
		}
	})

	t.Run("begin rolls back transaction", func(t *testing.T) {
		generation, replace := generationChangedDuringCall(t)
		rawTx := &generationGuardTestTx{}
		raw := &generationGuardTestConn{tx: rawTx, duringCall: replace}
		_, err := (generationConn{Conn: raw, generation: generation}).Begin()
		if !errors.Is(err, ErrDatabaseGenerationChanged) {
			t.Fatalf("begin error = %v, want ErrDatabaseGenerationChanged", err)
		}
		if rawTx.rollbackCalls != 1 {
			t.Fatalf("rollback calls = %d, want 1", rawTx.rollbackCalls)
		}
	})

	t.Run("commit", func(t *testing.T) {
		generation, replace := generationChangedDuringCall(t)
		rawTx := &generationGuardTestTx{duringCall: replace}
		err := (generationTx{Tx: rawTx, generation: generation}).Commit()
		if !errors.Is(err, ErrDatabaseGenerationChanged) {
			t.Fatalf("commit error = %v, want ErrDatabaseGenerationChanged", err)
		}
		if rawTx.commitCalls != 1 {
			t.Fatalf("commit calls = %d, want 1", rawTx.commitCalls)
		}
	})

	t.Run("rows next closes rows", func(t *testing.T) {
		generation, replace := generationChangedDuringCall(t)
		rawRows := &generationGuardTestRows{duringCall: replace}
		err := (generationRows{Rows: rawRows, generation: generation}).Next(make([]driver.Value, 1))
		if !errors.Is(err, ErrDatabaseGenerationChanged) {
			t.Fatalf("next error = %v, want ErrDatabaseGenerationChanged", err)
		}
		if !rawRows.closed {
			t.Fatal("rows were not closed")
		}
	})

	t.Run("rows next EOF closes rows", func(t *testing.T) {
		generation, replace := generationChangedDuringCall(t)
		rawRows := &generationGuardTestRows{duringCall: replace, nextErr: io.EOF}
		err := (generationRows{Rows: rawRows, generation: generation}).Next(make([]driver.Value, 1))
		if !errors.Is(err, ErrDatabaseGenerationChanged) {
			t.Fatalf("next EOF error = %v, want ErrDatabaseGenerationChanged", err)
		}
		if errors.Is(err, io.EOF) {
			t.Fatalf("next EOF error = %v, must not report EOF after generation change", err)
		}
		if !rawRows.closed {
			t.Fatal("rows were not closed")
		}
	})

	t.Run("next result set closes rows", func(t *testing.T) {
		generation, replace := generationChangedDuringCall(t)
		rawRows := &generationGuardTestRows{duringCall: replace, nextResultSetErr: io.EOF}
		err := (generationRows{Rows: rawRows, generation: generation}).NextResultSet()
		if !errors.Is(err, ErrDatabaseGenerationChanged) {
			t.Fatalf("next result set error = %v, want ErrDatabaseGenerationChanged", err)
		}
		if errors.Is(err, io.EOF) {
			t.Fatalf("next result set error = %v, must not report EOF after generation change", err)
		}
		if !rawRows.closed {
			t.Fatal("rows were not closed")
		}
	})
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
