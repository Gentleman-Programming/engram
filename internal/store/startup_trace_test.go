package store

import (
	"bytes"
	"context"
	"errors"
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
	var out bytes.Buffer
	trace := NewStartupTrace(&out, time.Now())
	ctx, cancel := context.WithTimeout(trace.Context(context.Background()), 500*time.Millisecond)
	defer cancel()
	_, err = NewWithStartupContext(ctx, FallbackConfig(dir))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended startup = %v", err)
	}
	want := strings.Fields("data-directory identity advisory-instance-id-open advisory-instance-id-retrying")
	if got := startupTraceLabels(t, out.String()); !slices.Equal(got, want) {
		t.Fatalf("startup phases = %v, want %v", got, want)
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
