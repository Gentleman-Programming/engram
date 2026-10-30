package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	sqlite "modernc.org/sqlite"
)

// One fake implements only the interfaces already forwarded by the fence.
type cantopenDriver struct{ err error }

func (f cantopenDriver) Open(string) (driver.Conn, error) { return nil, f.err }

type cantopenConn struct {
	driver.Conn
	err error
}

func (f cantopenConn) Prepare(string) (driver.Stmt, error)                         { return nil, f.err }
func (f cantopenConn) PrepareContext(context.Context, string) (driver.Stmt, error) { return nil, f.err }
func (f cantopenConn) Begin() (driver.Tx, error)                                   { return nil, f.err }
func (f cantopenConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return nil, f.err
}
func (f cantopenConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, f.err
}
func (f cantopenConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, f.err
}
func (f cantopenConn) Ping(context.Context) error                     { return f.err }
func (f cantopenConn) ResetSession(context.Context) error             { return f.err }
func (f cantopenConn) Close() error                                   { return f.err }
func (f cantopenConn) FileControlPersistWAL(string, int) (int, error) { return 0, f.err }
func (f cantopenConn) FileControlDataVersion(string) (uint32, error)  { return 0, f.err }

type cantopenStmt struct {
	driver.Stmt
	err error
}

func (f cantopenStmt) Exec([]driver.Value) (driver.Result, error) { return nil, f.err }
func (f cantopenStmt) Query([]driver.Value) (driver.Rows, error)  { return nil, f.err }
func (f cantopenStmt) ExecContext(context.Context, []driver.NamedValue) (driver.Result, error) {
	return nil, f.err
}
func (f cantopenStmt) QueryContext(context.Context, []driver.NamedValue) (driver.Rows, error) {
	return nil, f.err
}
func (f cantopenStmt) Close() error { return f.err }

type cantopenTx struct{ err error }

func (f cantopenTx) Commit() error   { return f.err }
func (f cantopenTx) Rollback() error { return f.err }

type cantopenRows struct {
	driver.Rows
	err error
}

func (f cantopenRows) Next([]driver.Value) error { return f.err }
func (f cantopenRows) NextResultSet() error      { return f.err }
func (f cantopenRows) HasNextResultSet() bool    { return true }
func (f cantopenRows) Close() error              { return f.err }

func TestCANTOPENFallbacks(t *testing.T) {
	ctx := context.Background()
	g := &databaseGeneration{}
	sentinel := errors.New("fallback")
	// Hide optional interfaces without changing the required interface methods.
	c := generationConn{Conn: struct{ driver.Conn }{cantopenConn{err: sentinel}}, generation: g}
	if _, e := c.PrepareContext(ctx, "private"); e != sentinel {
		t.Fatalf("prepare fallback: %v", e)
	}
	if _, e := c.BeginTx(ctx, driver.TxOptions{}); e != sentinel {
		t.Fatalf("begin fallback: %v", e)
	}
	if _, e := c.ExecContext(ctx, "", nil); e != driver.ErrSkip {
		t.Fatalf("exec fallback: %v", e)
	}
	if _, e := c.QueryContext(ctx, "", nil); e != driver.ErrSkip {
		t.Fatalf("query fallback: %v", e)
	}
	if e := c.Ping(ctx); e != driver.ErrSkip {
		t.Fatalf("ping fallback: %v", e)
	}
	if e := c.ResetSession(ctx); e != nil {
		t.Fatalf("reset fallback: %v", e)
	}
	s := generationStmt{Stmt: testFenceStmt{}, generation: g}
	if _, e := s.ExecContext(ctx, nil); e != driver.ErrSkip {
		t.Fatalf("stmt exec fallback: %v", e)
	}
	if _, e := s.QueryContext(ctx, nil); e != driver.ErrSkip {
		t.Fatalf("stmt query fallback: %v", e)
	}
	r := generationRows{Rows: struct{ driver.Rows }{cantopenRows{err: sentinel}}, generation: g}
	if e := r.NextResultSet(); e != io.EOF {
		t.Fatalf("result set fallback: %v", e)
	}
	g.err = ErrDatabaseGenerationChanged
	if e := s.Close(); e != ErrDatabaseGenerationChanged {
		t.Fatalf("close precedence: %v", e)
	}
	if e := r.Next(nil); e != ErrDatabaseGenerationChanged {
		t.Fatalf("row precedence: %v", e)
	}
	if _, e := c.ExecContext(ctx, "", nil); e != ErrDatabaseGenerationChanged {
		t.Fatalf("precheck precedence: %v", e)
	}
}

func TestCANTOPENBoundaries(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "missing-private-directory", "private.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close SQLite error fixture: %v", err)
		}
	}()
	original := db.Ping()
	var sqliteErr *sqlite.Error
	if !errors.As(original, &sqliteErr) || sqliteErr.Code()&255 != 14 {
		t.Fatalf("expected genuine CANTOPEN: %v", original)
	}
	private := fmt.Errorf("private SQL SELECT secret, path /private/db: %w", original)
	// Public Store propagation uses the same connector as normal SQLite opening.
	storeDB := sql.OpenDB(generationConnector{driver: &generationDriver{
		Driver: cantopenDriver{private}, generation: &databaseGeneration{},
	}})
	defer func() {
		if err := storeDB.Close(); err != nil {
			t.Errorf("close Store propagation fixture: %v", err)
		}
	}()
	st := &Store{db: storeDB}
	if _, err := st.Stats(); err == nil || !errors.Is(err, private) || strings.Contains(err.Error(), "private") {
		t.Fatalf("public Store propagation: %v", err)
	}
	inner := annotateSQLiteCantopen(private, "rows.next")
	if got := (generationConn{Conn: cantopenConn{err: inner}, generation: &databaseGeneration{}}).Ping(context.Background()); got != inner {
		t.Fatalf("outer boundary replaced innermost annotation: %v", got)
	}
	g := &databaseGeneration{} // No paths: no filesystem probes are needed by this fixture.
	ctx := context.Background()
	for _, input := range []error{private, new(sqlite.Error), errors.New("other"), driver.ErrSkip, io.EOF, nil} {
		c := generationConn{Conn: cantopenConn{err: input}, generation: g}
		s := generationStmt{Stmt: cantopenStmt{err: input}, generation: g}
		tx := generationTx{Tx: cantopenTx{err: input}, generation: g}
		rows := generationRows{Rows: cantopenRows{err: input}, generation: g}
		tests := []struct {
			op  string
			run func() error
		}{
			{"open", func() error {
				_, e := (&generationDriver{Driver: cantopenDriver{input}, generation: g}).Open("private DSN")
				return e
			}},
			{"conn.prepare", func() error { _, e := c.Prepare("private SQL"); return e }},
			{"conn.prepare_context", func() error { _, e := c.PrepareContext(ctx, "private SQL"); return e }},
			{"conn.begin", func() error { _, e := c.Begin(); return e }},
			{"conn.begin_tx", func() error { _, e := c.BeginTx(ctx, driver.TxOptions{}); return e }},
			{"conn.exec_context", func() error { _, e := c.ExecContext(ctx, "private SQL", nil); return e }},
			{"conn.query_context", func() error { _, e := c.QueryContext(ctx, "private SQL", nil); return e }},
			{"conn.ping", func() error { return c.Ping(ctx) }},
			{"conn.reset_session", func() error { return c.ResetSession(ctx) }},
			{"conn.close", c.Close},
			{"conn.persist_wal", func() error { _, e := c.FileControlPersistWAL("private", 1); return e }},
			{"conn.data_version", func() error { _, e := c.FileControlDataVersion("private"); return e }},
			{"stmt.exec", func() error { _, e := s.Exec(nil); return e }},
			{"stmt.query", func() error { _, e := s.Query(nil); return e }},
			{"stmt.exec_context", func() error { _, e := s.ExecContext(ctx, nil); return e }},
			{"stmt.query_context", func() error { _, e := s.QueryContext(ctx, nil); return e }},
			{"stmt.close", s.Close}, {"tx.commit", tx.Commit}, {"tx.rollback", tx.Rollback},
			{"rows.next", func() error { return rows.Next(nil) }}, {"rows.next_result_set", rows.NextResultSet}, {"rows.close", rows.Close},
		}
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s/%T/%v", tt.op, input, input == private), func(t *testing.T) {
				// Open requires a nonnil successful connection; this fake tests its error branch only.
				if input == nil && tt.op == "open" {
					return
				}
				got := tt.run()
				if input != private {
					if got != input {
						t.Fatalf("identity: got %v, want %v", got, input)
					}
					return
				}
				want := fmt.Sprintf("SQLite CANTOPEN during %s (code %d)", tt.op, sqliteErr.Code())
				if got == nil || got.Error() != want {
					t.Fatalf("got %v, want %s", got, want)
				}
				var recovered *sqlite.Error
				if !errors.Is(got, private) || !errors.As(got, &recovered) || recovered != sqliteErr {
					t.Fatal("lost original chain")
				}
				if strings.Contains(got.Error(), "private") {
					t.Fatal("private text escaped")
				}
			})
		}
	}
}
