package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sqlite "modernc.org/sqlite"
)

func TestDatabaseGeneration(t *testing.T) {
	t.Run("adopts sidecars when first observed", func(t *testing.T) {
		generation, dbPath := newTestDatabaseGeneration(t, false, false)
		if err := generation.check(); err != nil {
			t.Fatalf("initial check: %v", err)
		}
		writeTestFile(t, dbPath+"-wal")
		writeTestFile(t, dbPath+"-shm")
		if err := generation.check(); err != nil {
			t.Fatalf("adopt sidecars: %v", err)
		}
	})

	for _, sidecar := range []string{"", "-wal", "-shm"} {
		sidecar := sidecar
		t.Run("detects replacement "+sidecarName(sidecar), func(t *testing.T) {
			generation, dbPath := newTestDatabaseGeneration(t, sidecar == "-wal", sidecar == "-shm")
			replaceTestFile(t, dbPath+sidecar)
			assertGenerationChanged(t, generation.check())
			assertGenerationChanged(t, generation.check())
		})

		t.Run("detects disappearance "+sidecarName(sidecar), func(t *testing.T) {
			generation, dbPath := newTestDatabaseGeneration(t, sidecar == "-wal", sidecar == "-shm")
			if err := os.Remove(dbPath + sidecar); err != nil {
				t.Fatalf("remove generation file: %v", err)
			}
			assertGenerationChanged(t, generation.check())
			if sidecar != "" {
				writeTestFile(t, dbPath+sidecar)
				assertGenerationChanged(t, generation.check())
			}
		})
	}
}

func TestDatabaseGenerationRecoversFromStatError(t *testing.T) {
	generation, dbPath := newTestDatabaseGeneration(t, false, false)
	original := statFile
	t.Cleanup(func() { statFile = original })
	statErr := errors.New("transient stat error")
	failed := false
	statFile = func(path string) (os.FileInfo, error) {
		if path == dbPath && !failed {
			failed = true
			return nil, statErr
		}
		return original(path)
	}
	if err := generation.check(); err != statErr {
		t.Fatalf("check error = %v, want transient stat error", err)
	}
	if err := generation.check(); err != nil {
		t.Fatalf("check after transient stat error: %v", err)
	}
}

func TestGenerationFenceRejectsUnsafeOperations(t *testing.T) {
	t.Run("rejects before operation", func(t *testing.T) {
		generation, dbPath := newTestDatabaseGeneration(t, false, false)
		called := false
		conn := generationConn{Conn: &testFenceConn{exec: func() { called = true }}, generation: generation}
		replaceTestFile(t, dbPath)
		_, err := conn.ExecContext(context.Background(), "UPDATE test", nil)
		assertGenerationChanged(t, err)
		if called {
			t.Fatal("operation ran after generation change")
		}
	})

	t.Run("rejects after write", func(t *testing.T) {
		generation, dbPath := newTestDatabaseGeneration(t, false, false)
		conn := generationConn{Conn: &testFenceConn{exec: func() { replaceTestFile(t, dbPath) }}, generation: generation}
		_, err := conn.ExecContext(context.Background(), "UPDATE test", nil)
		assertGenerationChanged(t, err)
	})

	t.Run("rejects after commit", func(t *testing.T) {
		generation, dbPath := newTestDatabaseGeneration(t, false, false)
		tx := generationTx{Tx: &testFenceTx{commit: func() { replaceTestFile(t, dbPath) }}, generation: generation}
		assertGenerationChanged(t, tx.Commit())
	})

	t.Run("rejects while iterating rows", func(t *testing.T) {
		generation, dbPath := newTestDatabaseGeneration(t, false, false)
		conn := generationConn{Conn: &testFenceConn{rows: &testFenceRows{next: func() { replaceTestFile(t, dbPath) }}}, generation: generation}
		rows, err := conn.QueryContext(context.Background(), "SELECT test", nil)
		if err != nil {
			t.Fatalf("query rows: %v", err)
		}
		assertGenerationChanged(t, rows.Next(make([]driver.Value, 1)))
	})
}

func TestGenerationConnExposesFileControlForPrimeConnection(t *testing.T) {
	generation, _ := newTestDatabaseGeneration(t, false, false)
	base := &testFenceFileControlConn{}
	conn := generationConn{Conn: base, generation: generation}

	fc, ok := any(conn).(sqlite.FileControl)
	if !ok {
		t.Fatal("generation connection does not expose sqlite.FileControl")
	}
	mode, err := fc.FileControlPersistWAL("main", 1)
	if err != nil {
		t.Fatalf("FileControlPersistWAL: %v", err)
	}
	if mode != 1 || base.dbName != "main" || base.mode != 1 {
		t.Fatalf("persist WAL = (%d, %q, %d), want (1, main, 1)", mode, base.dbName, base.mode)
	}
}

func TestNewRejectsGenerationChangedBeforeSQLiteOpens(t *testing.T) {
	original := openDB
	t.Cleanup(func() { openDB = original })
	openDB = func(dbPath string, generation *databaseGeneration) (*sql.DB, error) {
		replaceTestFile(t, dbPath)
		return original(dbPath, generation)
	}
	cfg := FallbackConfig(t.TempDir())
	_, err := New(cfg)
	assertGenerationChanged(t, err)
}

func TestStatsPropagatesGenerationChange(t *testing.T) {
	s := newTestStore(t)
	s.hooks.queryIt = func(queryer, string, ...any) (rowScanner, error) { return nil, ErrDatabaseGenerationChanged }
	_, err := s.Stats()
	assertGenerationChanged(t, err)
}

func TestGenerationRowsNextResultSetIsFenced(t *testing.T) {
	generation, dbPath := newTestDatabaseGeneration(t, false, false)
	base := &testFenceRows{hasNextResultSet: true, nextResultSet: func() { replaceTestFile(t, dbPath) }}
	rows := generationRows{Rows: base, generation: generation}
	if !rows.HasNextResultSet() {
		t.Fatal("HasNextResultSet = false, want true")
	}
	assertGenerationChanged(t, rows.NextResultSet())
	if !base.closed {
		t.Fatal("rows were not closed after generation change")
	}
}

func newTestDatabaseGeneration(t *testing.T, wal, shm bool) (*databaseGeneration, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "engram.db")
	writeTestFile(t, dbPath)
	if wal {
		writeTestFile(t, dbPath+"-wal")
	}
	if shm {
		writeTestFile(t, dbPath+"-shm")
	}
	generation, err := newDatabaseGeneration(dbPath)
	if err != nil {
		t.Fatalf("capture database generation: %v", err)
	}
	return generation, dbPath
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("database generation"), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func replaceTestFile(t *testing.T, path string) {
	t.Helper()
	previous, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	_ = os.SameFile(previous, previous)
	replacement := path + ".replacement"
	writeTestFile(t, replacement)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("replace %s: %v", path, err)
	}
	current, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat replacement %s: %v", path, err)
	}
	if os.SameFile(previous, current) {
		t.Fatalf("replacement for %s retained its identity", path)
	}
}

func sidecarName(sidecar string) string {
	if sidecar == "" {
		return "database"
	}
	return sidecar[1:]
}

func assertGenerationChanged(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrDatabaseGenerationChanged) {
		t.Fatalf("error = %v, want ErrDatabaseGenerationChanged", err)
	}
}

type testFenceConn struct {
	exec func()
	rows *testFenceRows
}

type testFenceFileControlConn struct {
	testFenceConn
	dbName string
	mode   int
}

func (c *testFenceFileControlConn) FileControlPersistWAL(dbName string, mode int) (int, error) {
	c.dbName, c.mode = dbName, mode
	return mode, nil
}

func (c *testFenceConn) Prepare(string) (driver.Stmt, error) { return testFenceStmt{}, nil }
func (c *testFenceConn) Close() error                        { return nil }
func (c *testFenceConn) Begin() (driver.Tx, error)           { return testFenceTx{}, nil }
func (c *testFenceConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	if c.exec != nil {
		c.exec()
	}
	return driver.RowsAffected(1), nil
}
func (c *testFenceConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return c.rows, nil
}

type testFenceStmt struct{}

func (testFenceStmt) Close() error                               { return nil }
func (testFenceStmt) NumInput() int                              { return -1 }
func (testFenceStmt) Exec([]driver.Value) (driver.Result, error) { return driver.RowsAffected(1), nil }
func (testFenceStmt) Query([]driver.Value) (driver.Rows, error)  { return &testFenceRows{}, nil }

type testFenceTx struct{ commit func() }

func (t testFenceTx) Commit() error {
	if t.commit != nil {
		t.commit()
	}
	return nil
}
func (testFenceTx) Rollback() error { return nil }

type testFenceRows struct {
	next, nextResultSet      func()
	hasNextResultSet, closed bool
}

func (r *testFenceRows) Columns() []string { return []string{"value"} }
func (r *testFenceRows) Close() error      { r.closed = true; return nil }
func (r *testFenceRows) Next([]driver.Value) error {
	if r.next != nil {
		r.next()
	}
	return nil
}
func (r *testFenceRows) HasNextResultSet() bool { return r.hasNextResultSet }
func (r *testFenceRows) NextResultSet() error {
	if r.nextResultSet != nil {
		r.nextResultSet()
	}
	return nil
}
