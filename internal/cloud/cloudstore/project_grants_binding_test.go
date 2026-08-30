package cloudstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"testing"
	"time"
)

const projectGrantBindingDriverName = "cloudstore-project-grant-binding"

var projectGrantBindingDriver = &capturingProjectGrantDriver{}

func init() {
	sql.Register(projectGrantBindingDriverName, projectGrantBindingDriver)
}

type capturingProjectGrantDriver struct {
	argument   any
	queryCalls int
	execCalls  int
	grants     map[int64]map[string]struct{}
}

func (d *capturingProjectGrantDriver) Open(string) (driver.Conn, error) {
	return projectGrantBindingConn{driver: d}, nil
}

type projectGrantBindingConn struct {
	driver *capturingProjectGrantDriver
}

func (c projectGrantBindingConn) Prepare(string) (driver.Stmt, error) {
	return nil, driver.ErrSkip
}

func (projectGrantBindingConn) Close() error { return nil }

func (projectGrantBindingConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

func (c projectGrantBindingConn) QueryContext(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
	c.driver.queryCalls++
	c.driver.argument = args[0].Value

	principalID, ok := args[0].Value.(int64)
	if !ok {
		return &projectGrantBindingRows{}, nil
	}
	grants := c.driver.grants[principalID]
	rows := make([][]driver.Value, 0, len(grants))
	for project := range grants {
		rows = append(rows, []driver.Value{principalID, project, "", time.Unix(0, 0).UTC()})
	}
	return &projectGrantBindingRows{values: rows}, nil
}

func (c projectGrantBindingConn) ExecContext(_ context.Context, _ string, args []driver.NamedValue) (driver.Result, error) {
	c.driver.execCalls++
	c.driver.argument = args[0].Value

	principalID, ok := args[0].Value.(int64)
	if !ok {
		return driver.RowsAffected(0), nil
	}
	project := args[1].Value.(string)
	delete(c.driver.grants[principalID], project)
	return driver.RowsAffected(1), nil
}

type projectGrantBindingRows struct {
	values [][]driver.Value
}

func (projectGrantBindingRows) Columns() []string {
	return []string{"principal_id", "project", "granted_by_principal_id", "created_at"}
}

func (projectGrantBindingRows) Close() error { return nil }

func (r *projectGrantBindingRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

func resetProjectGrantBindingDriver() {
	projectGrantBindingDriver.argument = nil
	projectGrantBindingDriver.queryCalls = 0
	projectGrantBindingDriver.execCalls = 0
	projectGrantBindingDriver.grants = make(map[int64]map[string]struct{})
}

func seedProjectGrantBindingDriver(principalID int64, project string) {
	if projectGrantBindingDriver.grants[principalID] == nil {
		projectGrantBindingDriver.grants[principalID] = make(map[string]struct{})
	}
	projectGrantBindingDriver.grants[principalID][project] = struct{}{}
}

func TestListProjectGrantsBindsNumericPrincipalID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{name: "trims padded ID", input: " 3 ", want: 3},
		{name: "accepts maximum signed bigint", input: "9223372036854775807", want: 9223372036854775807},
		{name: "accepts minimum signed bigint", input: "-9223372036854775808", want: -9223372036854775808},
		{name: "rejects non-numeric ID", input: "not-a-number", wantErr: true},
		{name: "rejects out-of-range ID", input: "9223372036854775808", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetProjectGrantBindingDriver()
			db, err := sql.Open(projectGrantBindingDriverName, "")
			if err != nil {
				t.Fatalf("open capture database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			store := &CloudStore{db: db}
			_, err = store.ListProjectGrants(context.Background(), tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ListProjectGrants returned nil error")
				}
				if projectGrantBindingDriver.queryCalls != 0 {
					t.Fatalf("QueryContext called %d times; want 0", projectGrantBindingDriver.queryCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("list project grants: %v", err)
			}
			if projectGrantBindingDriver.queryCalls != 1 {
				t.Fatalf("QueryContext called %d times; want 1", projectGrantBindingDriver.queryCalls)
			}
			bound, ok := projectGrantBindingDriver.argument.(int64)
			if !ok {
				t.Fatalf("ListProjectGrants bound principal ID as %T; want int64", projectGrantBindingDriver.argument)
			}
			if bound != tt.want {
				t.Fatalf("ListProjectGrants bound principal ID as %d; want %d", bound, tt.want)
			}
		})
	}
}

func TestRevokeProjectGrantBindsNumericPrincipalID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{name: "trims padded ID", input: " 3 ", want: 3},
		{name: "accepts maximum signed bigint", input: "9223372036854775807", want: 9223372036854775807},
		{name: "accepts minimum signed bigint", input: "-9223372036854775808", want: -9223372036854775808},
		{name: "rejects non-numeric ID", input: "not-a-number", wantErr: true},
		{name: "rejects out-of-range ID", input: "9223372036854775808", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetProjectGrantBindingDriver()
			db, err := sql.Open(projectGrantBindingDriverName, "")
			if err != nil {
				t.Fatalf("open capture database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			store := &CloudStore{db: db}
			err = store.RevokeProjectGrant(context.Background(), tt.input, "alpha-project")
			if tt.wantErr {
				if err == nil {
					t.Fatal("RevokeProjectGrant returned nil error")
				}
				if projectGrantBindingDriver.execCalls != 0 {
					t.Fatalf("ExecContext called %d times; want 0", projectGrantBindingDriver.execCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("revoke project grant: %v", err)
			}
			if projectGrantBindingDriver.execCalls != 1 {
				t.Fatalf("ExecContext called %d times; want 1", projectGrantBindingDriver.execCalls)
			}
			bound, ok := projectGrantBindingDriver.argument.(int64)
			if !ok {
				t.Fatalf("RevokeProjectGrant bound principal ID as %T; want int64", projectGrantBindingDriver.argument)
			}
			if bound != tt.want {
				t.Fatalf("RevokeProjectGrant bound principal ID as %d; want %d", bound, tt.want)
			}
		})
	}
}

func TestRevokeProjectGrantRemovesGrantedRow(t *testing.T) {
	resetProjectGrantBindingDriver()
	seedProjectGrantBindingDriver(3, "alpha-project")
	db, err := sql.Open(projectGrantBindingDriverName, "")
	if err != nil {
		t.Fatalf("open capture database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := &CloudStore{db: db}
	grants, err := store.ListProjectGrants(context.Background(), " 3 ")
	if err != nil {
		t.Fatalf("list project grants before revoke: %v", err)
	}
	if len(grants) != 1 || grants[0].Project != "alpha-project" {
		t.Fatalf("expected granted project before revoke, got %+v", grants)
	}
	if err := store.RevokeProjectGrant(context.Background(), " 3 ", "alpha-project"); err != nil {
		t.Fatalf("revoke project grant: %v", err)
	}
	grants, err = store.ListProjectGrants(context.Background(), " 3 ")
	if err != nil {
		t.Fatalf("list project grants after revoke: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("revoked grant should not list, got %+v", grants)
	}
}
