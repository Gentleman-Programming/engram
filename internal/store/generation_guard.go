package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"sync"

	sqlite "modernc.org/sqlite"
)

// ErrDatabaseGenerationChanged means the database, WAL, or shared-memory file
// was replaced while this Store was live. Restart Engram to open the new generation.
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
	return &databaseGeneration{files: []generationFile{
		{name: "database", path: dbPath, required: true},
		{name: "WAL", path: dbPath + "-wal"},
		{name: "shared-memory", path: dbPath + "-shm"},
	}}
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
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return generationConn{Conn: conn, generation: c.generation}, nil
}

func (c generationConnector) Driver() driver.Driver {
	return c.driver
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
		return generationStmt{Stmt: stmt, generation: c.generation}, nil
	}
	return c.Prepare(query)
}

func (c generationConn) Begin() (driver.Tx, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	return c.Conn.Begin()
}

// ResetSession is called by database/sql before every reuse of a pooled connection.
func (c generationConn) ResetSession(ctx context.Context) error {
	if err := c.generation.check(); err != nil {
		return err
	}
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c generationConn) Ping(ctx context.Context) error {
	if err := c.generation.check(); err != nil {
		return err
	}
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c generationConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	if beginner, ok := c.Conn.(driver.ConnBeginTx); ok {
		return beginner.BeginTx(ctx, opts)
	}
	if opts.Isolation != driver.IsolationLevel(0) || opts.ReadOnly {
		return nil, errors.New("driver does not support transaction options")
	}
	return c.Conn.Begin()
}

func (c generationConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	if execer, ok := c.Conn.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c generationConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.generation.check(); err != nil {
		return nil, err
	}
	if queryer, ok := c.Conn.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c generationConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := c.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

func (c generationConn) IsValid() bool {
	if c.generation.check() != nil {
		return false
	}
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

type generationStmt struct {
	driver.Stmt
	generation *databaseGeneration
}

func (s generationStmt) Exec(args []driver.Value) (driver.Result, error) {
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	return s.Stmt.Exec(args)
}

func (s generationStmt) Query(args []driver.Value) (driver.Rows, error) {
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	return s.Stmt.Query(args)
}

func (s generationStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	if execer, ok := s.Stmt.(driver.StmtExecContext); ok {
		return execer.ExecContext(ctx, args)
	}
	return nil, driver.ErrSkip
}

func (s generationStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := s.generation.check(); err != nil {
		return nil, err
	}
	if queryer, ok := s.Stmt.(driver.StmtQueryContext); ok {
		return queryer.QueryContext(ctx, args)
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
