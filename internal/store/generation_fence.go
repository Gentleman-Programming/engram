package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"reflect"
	"sync"

	sqlite "modernc.org/sqlite"
)

// ErrDatabaseGenerationChanged means Engram's SQLite files were replaced while
// this process was running. Restart Engram before accessing the store again.
var ErrDatabaseGenerationChanged = errors.New("Engram database generation changed; restart Engram")

var statFile = os.Stat

type databaseGeneration struct {
	paths    []string
	files    []os.FileInfo
	observed []bool
	mu       sync.Mutex
	err      error
}

func newDatabaseGeneration(dbPath string) (*databaseGeneration, error) {
	generation := &databaseGeneration{
		paths: []string{dbPath, dbPath + "-wal", dbPath + "-shm"},
		files: make([]os.FileInfo, 3), observed: make([]bool, 3),
	}
	if err := generation.check(); err != nil {
		return nil, err
	}
	return generation, nil
}

func (g *databaseGeneration) check() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	for i, path := range g.paths {
		info, err := statFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				if i > 0 && !g.observed[i] {
					continue
				}
				g.err = ErrDatabaseGenerationChanged
				return g.err
			}
			return err
		}
		if !g.observed[i] {
			// Resolve Windows' deferred file ID before this path can be replaced.
			_ = os.SameFile(info, info)
			g.files[i], g.observed[i] = info, true
			continue
		}
		if !os.SameFile(g.files[i], info) {
			g.err = ErrDatabaseGenerationChanged
			return g.err
		}
	}
	return nil
}

func ensureDatabaseFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	return file.Close()
}

var openDB = func(dbPath string, generation *databaseGeneration) (*sql.DB, error) {
	sqliteDriver := &sqlite.Driver{}
	sqliteDriver.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		if fc, ok := conn.(sqlite.FileControl); ok {
			_, _ = fc.FileControlPersistWAL("main", 1)
		}
		return nil
	})
	d := &generationDriver{Driver: sqliteDriver, generation: generation}
	return sql.OpenDB(generationConnector{driver: d, name: storeDSN(dbPath)}), nil
}

type generationConnector struct {
	driver *generationDriver
	name   string
}

func (c generationConnector) Connect(context.Context) (driver.Conn, error) {
	return c.driver.Open(c.name)
}
func (c generationConnector) Driver() driver.Driver { return c.driver }

type generationDriver struct {
	driver.Driver
	generation *databaseGeneration
}

func (d *generationDriver) Open(name string) (driver.Conn, error) {
	if err := d.generation.check(); err != nil {
		return nil, err
	}
	conn, err := d.Driver.Open(name)
	if err != nil {
		return nil, err
	}
	if err := d.generation.check(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return generationConn{Conn: conn, generation: d.generation}, nil
}

type generationConn struct {
	driver.Conn
	generation *databaseGeneration
}

func (c generationConn) Prepare(query string) (driver.Stmt, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	if err := c.generation.check(); err != nil {
		_ = stmt.Close()
		return nil, err
	}
	return generationStmt{Stmt: stmt, generation: c.generation}, nil
}

func (c generationConn) Close() error {
	return c.Conn.Close()
}

func (c generationConn) Begin() (driver.Tx, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	if err := c.generation.check(); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return generationTx{Tx: tx, generation: c.generation}, nil
}

func (c generationConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if prepared, ok := c.Conn.(driver.ConnPrepareContext); ok {
		if err := c.generation.check(); err != nil {
			return nil, err
		}
		stmt, err := prepared.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		if err := c.generation.check(); err != nil {
			_ = stmt.Close()
			return nil, err
		}
		return generationStmt{Stmt: stmt, generation: c.generation}, nil
	}
	return c.Prepare(query)
}

func (c generationConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	execer, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	result, err := execer.ExecContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	return result, nil
}

func (c generationConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := queryer.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	if err := c.generation.check(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	return generationRows{Rows: rows, generation: c.generation}, nil
}

func (c generationConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if begin, ok := c.Conn.(driver.ConnBeginTx); ok {
		if err := c.generation.check(); err != nil {
			return nil, err
		}
		tx, err := begin.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		if err := c.generation.check(); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		return generationTx{Tx: tx, generation: c.generation}, nil
	}
	return c.Begin()
}

func (c generationConn) Ping(ctx context.Context) error {
	if err := c.generation.check(); err != nil {
		return err
	}
	pinger, ok := c.Conn.(driver.Pinger)
	if !ok {
		return driver.ErrSkip
	}
	if err := pinger.Ping(ctx); err != nil {
		return err
	}
	return c.generation.check()
}

func (c generationConn) ResetSession(ctx context.Context) error {
	if err := c.generation.check(); err != nil {
		return err
	}
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		if err := resetter.ResetSession(ctx); err != nil {
			return err
		}
	}
	return c.generation.check()
}

func (c generationConn) IsValid() bool {
	if c.generation.check() != nil {
		return false
	}
	validator, ok := c.Conn.(driver.Validator)
	return !ok || validator.IsValid()
}

func (c generationConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

// FileControlPersistWAL forwards modernc's optional FileControl interface
// through the generation fence. database/sql exposes this wrapped connection
// to primeConnection via Conn.Raw, so omitting it would make persistent WAL
// unavailable whenever generation fencing is enabled.
func (c generationConn) FileControlPersistWAL(dbName string, mode int) (int, error) {
	if err := c.generation.check(); err != nil {
		return 0, err
	}
	fc, ok := c.Conn.(sqlite.FileControl)
	if !ok {
		return 0, errors.New("database connection does not implement sqlite.FileControl")
	}
	result, err := fc.FileControlPersistWAL(dbName, mode)
	if err != nil {
		return 0, err
	}
	if err := c.generation.check(); err != nil {
		return 0, err
	}
	return result, nil
}

type generationStmt struct {
	driver.Stmt
	generation *databaseGeneration
}

func (s generationStmt) Close() error {
	err := s.Stmt.Close()
	if generationErr := s.generation.check(); generationErr != nil {
		return generationErr
	}
	return err
}

func (s generationStmt) Exec(args []driver.Value) (driver.Result, error) {
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	result, err := s.Stmt.Exec(args)
	if err != nil {
		return nil, err
	}
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s generationStmt) Query(args []driver.Value) (driver.Rows, error) {
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	rows, err := s.Stmt.Query(args)
	if err != nil {
		return nil, err
	}
	if err := s.generation.check(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	return generationRows{Rows: rows, generation: s.generation}, nil
}

func (s generationStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	execer, ok := s.Stmt.(driver.StmtExecContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	result, err := execer.ExecContext(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s generationStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	queryer, ok := s.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := queryer.QueryContext(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := s.generation.check(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	return generationRows{Rows: rows, generation: s.generation}, nil
}

type generationTx struct {
	driver.Tx
	generation *databaseGeneration
}

func (t generationTx) Commit() error {
	if err := t.generation.check(); err != nil {
		_ = t.Tx.Rollback()
		return err
	}
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	return t.generation.check()
}

func (t generationTx) Rollback() error {
	err := t.Tx.Rollback()
	if generationErr := t.generation.check(); generationErr != nil {
		return generationErr
	}
	return err
}

type generationRows struct {
	driver.Rows
	generation *databaseGeneration
}

func (r generationRows) Close() error {
	err := r.Rows.Close()
	if generationErr := r.generation.check(); generationErr != nil {
		return generationErr
	}
	return err
}

func (r generationRows) Next(dest []driver.Value) error {
	if err := r.generation.check(); err != nil {
		return err
	}
	err := r.Rows.Next(dest)
	if generationErr := r.generation.check(); generationErr != nil {
		return generationErr
	}
	return err
}

func (r generationRows) HasNextResultSet() bool {
	rows, ok := r.Rows.(driver.RowsNextResultSet)
	return ok && rows.HasNextResultSet()
}

func (r generationRows) NextResultSet() error {
	if err := r.generation.check(); err != nil {
		return err
	}
	rows, ok := r.Rows.(driver.RowsNextResultSet)
	if !ok {
		return io.EOF
	}
	err := rows.NextResultSet()
	if generationErr := r.generation.check(); generationErr != nil {
		_ = r.Rows.Close()
		return generationErr
	}
	return err
}

func (r generationRows) ColumnTypeDatabaseTypeName(index int) string {
	if rows, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return rows.ColumnTypeDatabaseTypeName(index)
	}
	return ""
}

func (r generationRows) ColumnTypeLength(index int) (int64, bool) {
	if rows, ok := r.Rows.(driver.RowsColumnTypeLength); ok {
		return rows.ColumnTypeLength(index)
	}
	return 0, false
}

func (r generationRows) ColumnTypeNullable(index int) (bool, bool) {
	if rows, ok := r.Rows.(driver.RowsColumnTypeNullable); ok {
		return rows.ColumnTypeNullable(index)
	}
	return false, false
}

func (r generationRows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	if rows, ok := r.Rows.(driver.RowsColumnTypePrecisionScale); ok {
		return rows.ColumnTypePrecisionScale(index)
	}
	return 0, 0, false
}

func (r generationRows) ColumnTypeScanType(index int) reflect.Type {
	if rows, ok := r.Rows.(driver.RowsColumnTypeScanType); ok {
		return rows.ColumnTypeScanType(index)
	}
	return nil
}
