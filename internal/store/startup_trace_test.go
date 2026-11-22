package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStartupTraceReportsSuccessfulStoreStartup(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	trace := NewStartupTrace(&out, time.Now())
	s, err := NewWithStartupContext(trace.Context(context.Background()), FallbackConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	want := strings.Fields("data-directory identity advisory-instance-id-open advisory-instance-id-acquired " +
		"database-file database-generation sqlite-open sqlite-prime startup-migrations advisory-migration-open " +
		"advisory-migration-acquired migration-work migration-work-complete store-ready")
	if got := startupTraceLabels(t, out.String()); !slices.Equal(got, want) {
		t.Fatalf("startup phases = %v, want %v", got, want)
	}
}

func TestStartupTraceDistinguishesAdvisoryContention(t *testing.T) {
	dir := t.TempDir()
	unlock, err := acquireMigrationLock(filepath.Join(dir, ".instance-id.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	budget, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(budget)
	defer cancel()
	out := &cancelOnAdvisoryRetry{cancel: cancel}
	trace := NewStartupTrace(out, time.Now())
	_, err = NewWithStartupContext(trace.Context(ctx), FallbackConfig(dir))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("contended startup = %v", err)
	}
	want := strings.Fields("data-directory identity advisory-instance-id-open advisory-instance-id-retrying")
	if got := startupTraceLabels(t, out.String()); !slices.Equal(got, want) {
		t.Fatalf("startup phases = %v, want %v", got, want)
	}
}

type cancelOnAdvisoryRetry struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (out *cancelOnAdvisoryRetry) Write(line []byte) (int, error) {
	if bytes.HasSuffix(line, []byte(" advisory-instance-id-retrying\n")) {
		out.cancel()
	}
	return out.Buffer.Write(line)
}

func TestStartupTraceClassifiesTypedSQLiteLocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "engram.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	contender, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = contender.Close() }()
	if _, err := db.ExecContext(ctx, "CREATE TABLE entries (value INTEGER); INSERT INTO entries VALUES (1), (2)"); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()
	_, busy := contender.ExecContext(ctx, "INSERT INTO entries VALUES (3)")
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.QueryContext(ctx, "SELECT value FROM entries")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatalf("expected a live cursor, got %v", rows.Err())
	}
	_, locked := conn.ExecContext(ctx, "DROP TABLE entries")
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	_, missing := conn.ExecContext(ctx, "SELECT * FROM table_that_does_not_exist")

	for _, test := range []struct {
		name string
		err  error
		code int
		want string
	}{
		{name: "busy", err: busy, code: sqlitePrimaryBusy, want: "sqlite-prime-lock-result"},
		{name: "wrapped locked", err: fmt.Errorf("migration: %w", locked), code: sqlitePrimaryLocked, want: "sqlite-prime-lock-result"},
		{name: "non-lock", err: missing, code: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if code, ok := sqlitePrimaryErrorCode(test.err); !ok || code != test.code {
				t.Fatalf("fixture SQLite code = %d (%v), error %v", code, ok, test.err)
			}
			var out bytes.Buffer
			trace := NewStartupTrace(&out, time.Now())
			trace.markSQLiteLock("sqlite-prime-lock-result", test.err)
			if test.want == "" && out.Len() == 0 {
				return
			}
			if got := startupTraceLabels(t, out.String()); !slices.Equal(got, []string{test.want}) {
				t.Fatalf("lock phases = %v, want %q", got, test.want)
			}
		})
	}
}

func TestStartupTraceDoesNotClassifySQLiteErrorsByPath(t *testing.T) {
	var out bytes.Buffer
	trace := NewStartupTrace(&out, time.Now())
	err := &os.PathError{Op: "open", Path: "sqlite_busy/engram.db", Err: fs.ErrNotExist}
	trace.markSQLiteLock("sqlite-prime-lock-result", err)
	if out.Len() != 0 {
		t.Fatalf("filesystem error emitted a SQLite lock result: %s", out.String())
	}
}

func startupTraceLabels(t *testing.T, trace string) []string {
	t.Helper()
	pattern := regexp.MustCompile(`^ENGRAM_STARTUP_PHASE v1 [0-9]+ ([a-z-]+)$`)
	var labels []string
	for _, line := range strings.Split(strings.TrimSpace(trace), "\n") {
		match := pattern.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("startup trace contains an unexpected line %q", line)
		}
		labels = append(labels, match[1])
	}
	return labels
}
