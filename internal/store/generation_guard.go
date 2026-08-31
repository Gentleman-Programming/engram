package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	sqlite "modernc.org/sqlite"
)

// ErrDatabaseGenerationChanged means the database, WAL, or shared-memory file
// was replaced while this Store was live. The guard is a detector, not an atomic
// fence: a file may change after a check but before SQLite uses it. A known WAL or
// shared-memory file disappearing is sticky, including when a driver discards its
// last connection and unlinks those sidecars; restart Engram to reopen the store.
var ErrDatabaseGenerationChanged = errors.New("database generation changed on disk; restart Engram to reopen the store")

type generationFile struct {
	name     string
	path     string
	required bool
	present  bool
	identity databaseFileIdentity
}

type databaseGeneration struct {
	mu      sync.Mutex
	files   []generationFile
	enabled bool
	changed bool
}

var readDatabaseFileID = databaseFileID

func newDatabaseGeneration(dbPath string) *databaseGeneration {
	return &databaseGeneration{
		files: []generationFile{
			{name: "database", path: dbPath, required: true},
			{name: "WAL", path: dbPath + "-wal"},
			{name: "shared-memory", path: dbPath + "-shm"},
		},
	}
}

func (g *databaseGeneration) capture() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	files := make([]generationFile, len(g.files))
	for i, file := range g.files {
		identity, err := readDatabaseFileID(file.path)
		if err != nil {
			if !file.required && errors.Is(err, os.ErrNotExist) {
				files[i] = file
				continue
			}
			return fmt.Errorf("inspect %s identity: %w", file.name, err)
		}
		files[i] = generationFile{
			name:     file.name,
			path:     file.path,
			required: file.required,
			present:  true,
			identity: identity,
		}
	}

	g.files = files
	g.enabled = true
	return nil
}

func (g *databaseGeneration) check() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.enabled {
		return nil
	}
	if g.changed {
		return ErrDatabaseGenerationChanged
	}
	for i := range g.files {
		file := &g.files[i]
		identity, err := readDatabaseFileID(file.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if file.required || file.present {
					return g.changedError(file.name, "disappeared")
				}
				continue
			}
			return fmt.Errorf("inspect %s identity: %w", file.name, err)
		}
		if !file.present {
			file.present = true
			file.identity = identity
			continue
		}
		if file.identity != identity {
			return g.changedError(file.name, "identity changed")
		}
	}
	return nil
}

func (g *databaseGeneration) changedError(name, reason string) error {
	g.changed = true
	return fmt.Errorf("%w: %s %s", ErrDatabaseGenerationChanged, name, reason)
}

type generationConnector struct {
	dsn        string
	driver     driver.Driver
	generation *databaseGeneration
}

func (c generationConnector) Connect(context.Context) (driver.Conn, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	lease, err := acquireStoreLease(filepath.Dir(c.dsn), false)
	if err != nil {
		return nil, fmt.Errorf("acquire connection store lease: %w", err)
	}
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	if err := c.generation.check(); err != nil {
		return nil, errors.Join(err, conn.Close(), lease.Close())
	}
	return generationConn{Conn: conn, generation: c.generation, lease: lease}, nil
}

func (c generationConnector) Driver() driver.Driver {
	return c.driver
}

type generationConn struct {
	driver.Conn
	generation *databaseGeneration
	lease      *storeLease
}

func (c generationConn) Close() error {
	var generationErr error
	if c.generation != nil {
		generationErr = c.generation.check()
	}
	closeErr := c.Conn.Close()
	if c.lease != nil {
		closeErr = errors.Join(closeErr, c.lease.Close())
	}
	if generationErr != nil || closeErr != nil {
		return errors.Join(generationErr, closeErr)
	}
	return nil
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
		return nil, errors.Join(err, stmt.Close())
	}
	return generationStmt{Stmt: stmt, generation: c.generation}, nil
}

func (c generationConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	if preparer, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err := preparer.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		if err := c.generation.check(); err != nil {
			return nil, errors.Join(err, stmt.Close())
		}
		return generationStmt{Stmt: stmt, generation: c.generation}, nil
	}
	return c.Prepare(query)
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
		return nil, errors.Join(err, tx.Rollback())
	}
	return generationTx{Tx: tx, generation: c.generation}, nil
}

// ResetSession is called by database/sql before every reuse of a pooled connection.
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

func (c generationConn) Ping(ctx context.Context) error {
	if err := c.generation.check(); err != nil {
		return err
	}
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		if err := pinger.Ping(ctx); err != nil {
			return err
		}
	}
	return c.generation.check()
}

func (c generationConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	if beginner, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err := beginner.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		if err := c.generation.check(); err != nil {
			return nil, errors.Join(err, tx.Rollback())
		}
		return generationTx{Tx: tx, generation: c.generation}, nil
	}
	if opts.Isolation != driver.IsolationLevel(0) || opts.ReadOnly {
		return nil, errors.New("driver does not support transaction options")
	}
	return c.Begin()
}

func (c generationConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	if execer, ok := c.Conn.(driver.ExecerContext); ok {
		result, err := execer.ExecContext(ctx, query, args)
		if err != nil {
			return nil, err
		}
		if err := c.generation.check(); err != nil {
			return nil, err
		}
		return result, nil
	}
	return nil, driver.ErrSkip
}

func (c generationConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	if queryer, ok := c.Conn.(driver.QueryerContext); ok {
		rows, err := queryer.QueryContext(ctx, query, args)
		if err != nil {
			return nil, err
		}
		if err := c.generation.check(); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		return generationRows{Rows: rows, generation: c.generation}, nil
	}
	return nil, driver.ErrSkip
}

func (c generationConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		if err := checker.CheckNamedValue(value); err != nil {
			return err
		}
		return c.generation.check()
	}
	return driver.ErrSkip
}

func (c generationConn) IsValid() bool {
	if c.generation.check() != nil {
		return false
	}
	if validator, ok := c.Conn.(driver.Validator); ok {
		if !validator.IsValid() {
			return false
		}
	}
	return c.generation.check() == nil
}

type generationTx struct {
	driver.Tx
	generation *databaseGeneration
}

func (t generationTx) Commit() error {
	if err := t.generation.check(); err != nil {
		return errors.Join(err, t.Tx.Rollback())
	}
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	return t.generation.check()
}

func (t generationTx) Rollback() error {
	generationErr := t.generation.check()
	rollbackErr := t.Tx.Rollback()
	if generationErr != nil || rollbackErr != nil {
		return errors.Join(generationErr, rollbackErr)
	}
	return t.generation.check()
}

// generationRows preserves the optional row interfaces implemented by modernc
// SQLite. For an unexpected Rows implementation that lacks one, it returns the
// corresponding database/sql zero-value behavior.
type generationRows struct {
	driver.Rows
	generation *databaseGeneration
}

func (r generationRows) Close() error {
	generationErr := r.generation.check()
	closeErr := r.Rows.Close()
	if generationErr != nil || closeErr != nil {
		return errors.Join(generationErr, closeErr)
	}
	return nil
}

func (r generationRows) Next(dest []driver.Value) error {
	if err := r.generation.check(); err != nil {
		return err
	}
	nextErr := r.Rows.Next(dest)
	if err := r.generation.check(); err != nil {
		if errors.Is(nextErr, io.EOF) {
			return errors.Join(err, r.Rows.Close())
		}
		return errors.Join(nextErr, err, r.Rows.Close())
	}
	return nextErr
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
	nextErr := rows.NextResultSet()
	if err := r.generation.check(); err != nil {
		if errors.Is(nextErr, io.EOF) {
			return errors.Join(err, r.Rows.Close())
		}
		return errors.Join(nextErr, err, r.Rows.Close())
	}
	return nextErr
}

func (r generationRows) ColumnTypeScanType(index int) reflect.Type {
	if rows, ok := r.Rows.(driver.RowsColumnTypeScanType); ok {
		return rows.ColumnTypeScanType(index)
	}
	return reflect.TypeFor[any]()
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

type generationStmt struct {
	driver.Stmt
	generation *databaseGeneration
}

func (s generationStmt) Close() error {
	generationErr := s.generation.check()
	closeErr := s.Stmt.Close()
	if generationErr != nil || closeErr != nil {
		return errors.Join(generationErr, closeErr)
	}
	return nil
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
		return nil, errors.Join(err, rows.Close())
	}
	return generationRows{Rows: rows, generation: s.generation}, nil
}

func (s generationStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	if execer, ok := s.Stmt.(driver.StmtExecContext); ok {
		result, err := execer.ExecContext(ctx, args)
		if err != nil {
			return nil, err
		}
		if err := s.generation.check(); err != nil {
			return nil, err
		}
		return result, nil
	}
	return nil, driver.ErrSkip
}

func (s generationStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	if queryer, ok := s.Stmt.(driver.StmtQueryContext); ok {
		rows, err := queryer.QueryContext(ctx, args)
		if err != nil {
			return nil, err
		}
		if err := s.generation.check(); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		return generationRows{Rows: rows, generation: s.generation}, nil
	}
	return nil, driver.ErrSkip
}

func openGenerationGuardedDB(dbPath string, generation *databaseGeneration) (*sql.DB, error) {
	return sql.OpenDB(generationConnector{
		dsn:        dbPath,
		driver:     &sqlite.Driver{},
		generation: generation,
	}), nil
}
