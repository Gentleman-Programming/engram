package main

// conflicts_prune_test.go — CLI tests for `engram conflicts prune` (issue #849).

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// seedAgedDeadRowCLI inserts one sync_apply_deferred row aged ageDays.
func seedAgedDeadRowCLI(t *testing.T, db *sql.DB, syncID, entity, targetKey, project, reasonCode, status string, ageDays int) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, target_key, project, reason_code, apply_status, first_seen_at)
		VALUES (?, ?, '{}', ?, ?, ?, ?, datetime('now', ?))
	`, syncID, entity, targetKey, project, reasonCode, status, fmt.Sprintf("-%d days", ageDays)); err != nil {
		t.Fatalf("seedAgedDeadRowCLI %q: %v", syncID, err)
	}
}

func deferredRowIDsCLI(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT sync_id FROM sync_apply_deferred ORDER BY sync_id`)
	if err != nil {
		t.Fatalf("deferredRowIDsCLI: %v", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("deferredRowIDsCLI: scan: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

// seedPruneFixture bootstraps the schema and seeds: one TTL-expired row and
// three fresh rows in cloud/proj-a, one TTL-expired legacy-unscoped row, and an
// ancient deferred row that must never be pruned.
func seedPruneFixture(t *testing.T, cfg store.Config) *sql.DB {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s.Close()
	db := openTestDB(t, cfg)
	seedAgedDeadRowCLI(t, db, "dead-a-old", "relation", "cloud", "proj-a", "relation_dead", "dead", 45)
	seedAgedDeadRowCLI(t, db, "dead-a-1", "relation", "cloud", "proj-a", "relation_dead", "dead", 10)
	seedAgedDeadRowCLI(t, db, "dead-a-2", "relation", "cloud", "proj-a", "relation_dead", "dead", 2)
	seedAgedDeadRowCLI(t, db, "dead-a-3", "relation", "cloud", "proj-a", "relation_dead", "dead", 1)
	seedAgedDeadRowCLI(t, db, "dead-legacy", "session", "", "", "sync_session_identity_invalid", "dead", 90)
	seedAgedDeadRowCLI(t, db, "retry-ancient", "relation", "cloud", "proj-a", "", "deferred", 400)
	return db
}

var allPruneFixtureRows = []string{"dead-a-1", "dead-a-2", "dead-a-3", "dead-a-old", "dead-legacy", "retry-ancient"}

func TestCmdConflictsPruneDefaultIsDryRun(t *testing.T) {
	cfg := testConfig(t)
	db := seedPruneFixture(t, cfg)

	withArgs(t, "engram", "conflicts", "prune")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	want := "Dead Row Prune (dry run)\n" +
		"  max_age_days:  30\n" +
		"  max_per_scope: 1000\n" +
		"  would_evict:   2\n" +
		"\n" +
		"  By scope:\n" +
		"    target=(none) project=(none) by_age=1 by_cap=0\n" +
		"    target=cloud project=proj-a by_age=1 by_cap=0\n" +
		"\n" +
		"  By reason:\n" +
		"    entity=relation reason_code=relation_dead age=30d+ count=1\n" +
		"    entity=session reason_code=sync_session_identity_invalid age=30d+ count=1\n" +
		"\n" +
		"Dry run: nothing was deleted. Re-run with --apply to delete these rows.\n"
	if stdout != want {
		t.Fatalf("stdout mismatch\n got: %q\nwant: %q", stdout, want)
	}
	if got := deferredRowIDsCLI(t, db); !reflect.DeepEqual(got, allPruneFixtureRows) {
		t.Fatalf("dry run changed rows: %v", got)
	}
}

func TestCmdConflictsPruneApplyDeletesSelectedDeadRows(t *testing.T) {
	cfg := testConfig(t)
	db := seedPruneFixture(t, cfg)

	withArgs(t, "engram", "conflicts", "prune", "--max-per-scope", "2", "--apply")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	want := "Dead Row Prune (applied)\n" +
		"  max_age_days:  30\n" +
		"  max_per_scope: 2\n" +
		"  evicted:       3\n" +
		"\n" +
		"  By scope:\n" +
		"    target=(none) project=(none) by_age=1 by_cap=0\n" +
		"    target=cloud project=proj-a by_age=1 by_cap=1\n" +
		"\n" +
		"  By reason:\n" +
		"    entity=relation reason_code=relation_dead age=7-29d count=1\n" +
		"    entity=relation reason_code=relation_dead age=30d+ count=1\n" +
		"    entity=session reason_code=sync_session_identity_invalid age=30d+ count=1\n"
	if stdout != want {
		t.Fatalf("stdout mismatch\n got: %q\nwant: %q", stdout, want)
	}
	if got, want := deferredRowIDsCLI(t, db), []string{"dead-a-2", "dead-a-3", "retry-ancient"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows after apply = %v, want %v", got, want)
	}
}

func TestCmdConflictsPruneHonorsMaxAgeDays(t *testing.T) {
	cfg := testConfig(t)
	db := seedPruneFixture(t, cfg)

	withArgs(t, "engram", "conflicts", "prune", "--max-age-days", "5", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	for _, line := range []string{"  max_age_days:  5\n", "  would_evict:   3\n", "    target=cloud project=proj-a by_age=2 by_cap=0\n"} {
		if !strings.Contains(stdout, line) {
			t.Errorf("missing %q in stdout: %q", line, stdout)
		}
	}
	if got := deferredRowIDsCLI(t, db); !reflect.DeepEqual(got, allPruneFixtureRows) {
		t.Fatalf("dry run changed rows: %v", got)
	}
}

func TestCmdConflictsPruneReportsEmptyBacklog(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "conflicts", "prune", "--apply")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	want := "Dead Row Prune (applied)\n" +
		"  max_age_days:  30\n" +
		"  max_per_scope: 1000\n" +
		"  evicted:       0\n" +
		"  Nothing to prune.\n"
	if stdout != want {
		t.Fatalf("stdout mismatch\n got: %q\nwant: %q", stdout, want)
	}
}

func TestCmdConflictsPruneRejectsMixedModesBeforeStoreOpen(t *testing.T) {
	for _, flags := range [][]string{{"--dry-run", "--apply"}, {"--apply", "--dry-run"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			cfg := testConfig(t)
			withArgs(t, append([]string{"engram", "conflicts", "prune"}, flags...)...)
			stubExitWithPanic(t)

			stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdConflicts(cfg) })
			if recovered != exitCode(1) || stderr != "error: --dry-run and --apply are mutually exclusive\n" || stdout != "" {
				t.Errorf("expected mode rejection: stdout=%q stderr=%q exit=%v", stdout, stderr, recovered)
			}
			if _, err := os.Stat(filepath.Join(cfg.DataDir, "engram.db")); !os.IsNotExist(err) {
				t.Errorf("conflicting modes must not create a database; stat error = %v", err)
			}
		})
	}
}

func TestCmdConflictsPruneRejectsInvalidLimitsBeforeStoreOpen(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--max-age-days", "abc"}, "error: --max-age-days must be a positive integer\n"},
		{[]string{"--max-age-days", "0"}, "error: --max-age-days must be a positive integer\n"},
		{[]string{"--max-age-days"}, "error: --max-age-days must be a positive integer\n"},
		{[]string{"--max-per-scope", "-3"}, "error: --max-per-scope must be a positive integer\n"},
		{[]string{"--max-per-scope", "1.5", "--apply"}, "error: --max-per-scope must be a positive integer\n"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			cfg := testConfig(t)
			withArgs(t, append([]string{"engram", "conflicts", "prune"}, tc.args...)...)
			stubExitWithPanic(t)

			stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdConflicts(cfg) })
			if recovered != exitCode(1) || stderr != tc.want || stdout != "" {
				t.Errorf("expected rejection: stdout=%q stderr=%q exit=%v", stdout, stderr, recovered)
			}
			if _, err := os.Stat(filepath.Join(cfg.DataDir, "engram.db")); !os.IsNotExist(err) {
				t.Errorf("invalid limits must not create a database; stat error = %v", err)
			}
		})
	}
}

// A rejected --apply on an existing database leaves every row in place.
func TestCmdConflictsPruneInvalidLimitLeavesRowsUnchanged(t *testing.T) {
	cfg := testConfig(t)
	db := seedPruneFixture(t, cfg)
	withArgs(t, "engram", "conflicts", "prune", "--max-per-scope", "0", "--apply")
	stubExitWithPanic(t)

	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdConflicts(cfg) })
	if recovered != exitCode(1) || stderr != "error: --max-per-scope must be a positive integer\n" {
		t.Fatalf("expected rejection: stderr=%q exit=%v", stderr, recovered)
	}
	if got := deferredRowIDsCLI(t, db); !reflect.DeepEqual(got, allPruneFixtureRows) {
		t.Fatalf("rejected prune changed rows: %v", got)
	}
}

func TestCmdConflictsUsageListsPrune(t *testing.T) {
	_, stderr := captureOutput(t, printConflictsUsage)
	if !strings.Contains(stderr, "subcommands: list, show, stats, scan, deferred, prune\n") {
		t.Errorf("usage does not list prune: %q", stderr)
	}
	if !strings.Contains(stderr, "  prune      [--max-age-days N]  [--max-per-scope N]  [--dry-run]  [--apply]\n") {
		t.Errorf("usage does not document prune flags: %q", stderr)
	}
	// Existing subcommands keep their documented lines.
	if !strings.Contains(stderr, "  deferred   [--status S]  [--limit N]  [--inspect SYNC_ID]  [--replay]\n") {
		t.Errorf("usage lost the deferred line: %q", stderr)
	}
}
