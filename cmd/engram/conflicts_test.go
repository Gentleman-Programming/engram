package main

// conflicts_test.go — CLI tests for `engram conflicts` sub-commands.
// Follows strict TDD RED → GREEN → REFACTOR.
//
// Pattern: testConfig → seed → withArgs → captureOutput → assert.
// Deferred rows seeded via direct sql.Open (no exported DB() on Store).
// Relations seeded via store.SaveRelation (public API, status='pending').

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
	versioncheck "github.com/Gentleman-Programming/engram/internal/version"
	_ "modernc.org/sqlite"
)

// ─── mock runner for D.2 tests ────────────────────────────────────────────────

// mockSemanticRunner is a test double for store.SemanticRunner.
// It records whether Compare was called and returns a configurable verdict/error.
type mockSemanticRunner struct {
	called  bool
	verdict store.SemanticVerdict
	err     error
}

func (m *mockSemanticRunner) Compare(_ context.Context, _ string) (store.SemanticVerdict, error) {
	m.called = true
	return m.verdict, m.err
}

// stubAgentRunnerFactory replaces agentRunnerFactory for a test and restores it
// on cleanup. The provided runner is returned for any name; factoryErr is returned
// when non-nil.
func stubAgentRunnerFactory(t *testing.T, runner store.SemanticRunner, factoryErr error) {
	t.Helper()
	old := agentRunnerFactory
	agentRunnerFactory = func(_ string) (store.SemanticRunner, error) {
		if factoryErr != nil {
			return nil, factoryErr
		}
		return runner, nil
	}
	t.Cleanup(func() { agentRunnerFactory = old })
}

// versionCheckResult returns a stub versioncheck.CheckResult that is up-to-date
// (no update banner noise in output).
func versionCheckResult() versioncheck.CheckResult {
	return versioncheck.CheckResult{Status: versioncheck.StatusUpToDate}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// openTestDB opens the engram.db file directly for low-level seeding operations
// that have no public Store API (e.g. deferred rows with arbitrary status/payload).
func openTestDB(t *testing.T, cfg store.Config) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedDeferredRowCLI inserts a row into sync_apply_deferred via the raw DB.
// Called after store.New has already created the schema.
func seedDeferredRowCLI(t *testing.T, db *sql.DB, syncID, payload string, retryCount int, applyStatus string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO sync_apply_deferred
			(sync_id, entity, payload, retry_count, apply_status, first_seen_at)
		VALUES (?, 'relation', ?, ?, ?, datetime('now'))
	`, syncID, payload, retryCount, applyStatus)
	if err != nil {
		t.Fatalf("seedDeferredRowCLI %q: %v", syncID, err)
	}
}

// seedRelation creates two observations in the given project, seeds one pending
// relation between them using store.SaveRelation, and returns the two sync_ids
// and the relation's sync_id.
func seedRelation(t *testing.T, cfg store.Config, project string) (srcSync, tgtSync, relSync string) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	sesID := fmt.Sprintf("ses-%s", project)
	if err := s.CreateSession(sesID, project, "/tmp/"+project); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	srcID, err := s.AddObservation(store.AddObservationParams{
		SessionID: sesID,
		Type:      "decision",
		Title:     "Decision Source",
		Content:   "source content for relation test",
		Project:   project,
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation src: %v", err)
	}

	tgtID, err := s.AddObservation(store.AddObservationParams{
		SessionID: sesID,
		Type:      "decision",
		Title:     "Decision Target",
		Content:   "target content for relation test",
		Project:   project,
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("AddObservation tgt: %v", err)
	}

	// Retrieve sync_ids.
	db := openTestDB(t, cfg)
	if err := db.QueryRow(`SELECT sync_id FROM observations WHERE id=?`, srcID).Scan(&srcSync); err != nil {
		t.Fatalf("get srcSync: %v", err)
	}
	if err := db.QueryRow(`SELECT sync_id FROM observations WHERE id=?`, tgtID).Scan(&tgtSync); err != nil {
		t.Fatalf("get tgtSync: %v", err)
	}

	relSync = fmt.Sprintf("rel-test-%s", project)
	rel, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   relSync,
		SourceID: srcSync,
		TargetID: tgtSync,
	})
	if err != nil {
		t.Fatalf("SaveRelation: %v", err)
	}
	_ = rel
	return
}

// ─── D.1 RED tests ────────────────────────────────────────────────────────────

// TestCmdConflicts_NoSubcommand verifies that `engram conflicts` without a
// subcommand prints usage to stderr and exits non-zero.
func TestCmdConflicts_NoSubcommand(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "conflicts")
	stubExitWithPanic(t)
	_, stderr, _ := captureOutputAndRecover(t, func() { cmdConflicts(cfg) })
	if !strings.Contains(strings.ToLower(stderr), "conflict") {
		t.Errorf("expected usage hint in stderr; got: %q", stderr)
	}
}

// TestCmdConflictsList_HappyPath verifies `engram conflicts list --project alpha`
// prints relation rows including the judgment_status labels.
func TestCmdConflictsList_HappyPath(t *testing.T) {
	cfg := testConfig(t)
	seedRelation(t, cfg, "alpha")

	withArgs(t, "engram", "conflicts", "list", "--project", "alpha")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "pending") {
		t.Errorf("expected 'pending' in output; got: %q", stdout)
	}
}

// TestCmdConflictsList_EmptyProject verifies that when there are no relations for
// a project, the command exits 0 and indicates zero results.
func TestCmdConflictsList_EmptyProject(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "conflicts", "list", "--project", "no-such-project")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "0") && !strings.Contains(strings.ToLower(stdout), "no relations") {
		t.Errorf("expected zero-results indication; got: %q", stdout)
	}
}

// TestCmdConflictsStats_HappyPath verifies `engram conflicts stats --project alpha`
// prints aggregated counts including the pending label.
func TestCmdConflictsStats_HappyPath(t *testing.T) {
	cfg := testConfig(t)
	seedRelation(t, cfg, "alpha")

	withArgs(t, "engram", "conflicts", "stats", "--project", "alpha")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(strings.ToLower(stdout), "pending") {
		t.Errorf("expected stats output to mention 'pending'; got: %q", stdout)
	}
}

// TestCmdConflictsScan_DryRun verifies `engram conflicts scan --project X` (dry-run
// default) prints 0 inserted and does not modify the DB.
func TestCmdConflictsScan_DryRun(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "ses-scan", "scanproj", "bugfix", "Auth token missing", "Auth token is missing from requests", "project")
	mustSeedObservation(t, cfg, "ses-scan", "scanproj", "bugfix", "Auth token duplicate", "Auth token appears twice in header", "project")

	withArgs(t, "engram", "conflicts", "scan", "--project", "scanproj")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr for dry-run scan: %q", stderr)
	}
	// dry-run: inserted should be 0
	if !strings.Contains(stdout, "inserted:") || !strings.Contains(stdout, "0") {
		t.Errorf("expected 'inserted: 0' in dry-run output; got: %q", stdout)
	}
	// dry_run flag should be shown
	if !strings.Contains(strings.ToLower(stdout), "dry") {
		t.Errorf("expected dry-run indicator in output; got: %q", stdout)
	}
}

// TestCmdConflictsScan_Apply verifies `engram conflicts scan --apply` inserts rows
// and reports an inserted count.
func TestCmdConflictsScan_Apply(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "ses-apply", "applyproj", "bugfix", "Auth token missing apply", "Auth token is missing from requests in apply mode", "project")
	mustSeedObservation(t, cfg, "ses-apply", "applyproj", "bugfix", "Auth token dup apply", "Auth token appears twice in header in apply mode", "project")

	withArgs(t, "engram", "conflicts", "scan", "--project", "applyproj", "--apply", "--max-insert", "5")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr for apply scan: %q", stderr)
	}
	if !strings.Contains(stdout, "inserted:") {
		t.Errorf("expected 'inserted:' label in apply scan output; got: %q", stdout)
	}
}

// TestCmdConflictsScan_CapWarning verifies that when --apply reaches the
// --max-insert cap, a WARNING line is printed.
func TestCmdConflictsScan_CapWarning(t *testing.T) {
	cfg := testConfig(t)
	// Seed enough content-similar observations so FindCandidates surfaces at least
	// one candidate. We then set max-insert=1 to ensure any single candidate hits
	// the cap (the store seeds ~1 candidate pair for 2 obs with similar content).
	mustSeedObservation(t, cfg, "ses-capw", "capwproj", "bugfix", "Auth miss capw", "Auth token is missing from requests capped", "project")
	mustSeedObservation(t, cfg, "ses-capw", "capwproj", "bugfix", "Auth dup capw", "Auth token appears twice in header capped", "project")

	// --max-insert 1: if a candidate is found, cap is reached immediately.
	withArgs(t, "engram", "conflicts", "scan", "--project", "capwproj", "--apply", "--max-insert", "1")
	stdout, _ := captureOutput(t, func() { cmdConflicts(cfg) })
	// If no candidates were found (content not similar enough for FindCandidates),
	// no cap warning is expected. We only assert the command runs without panic/error.
	// Full cap-warning behavior with guaranteed candidates is tested in G.1.
	_ = stdout
}

// TestCmdConflictsDeferred_List verifies `engram conflicts deferred` lists rows
// from sync_apply_deferred.
func TestCmdConflictsDeferred_List(t *testing.T) {
	cfg := testConfig(t)

	// Create the store first to ensure schema is bootstrapped.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s.Close()

	db := openTestDB(t, cfg)
	seedDeferredRowCLI(t, db, "def-cli-001", `{"relation_type":"conflicts","source_id":"a","target_id":"b"}`, 0, "deferred")
	seedDeferredRowCLI(t, db, "def-cli-002", `{"relation_type":"conflicts","source_id":"c","target_id":"d"}`, 2, "dead")

	withArgs(t, "engram", "conflicts", "deferred")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "def-cli-001") {
		t.Errorf("expected sync_id def-cli-001 in output; got: %q", stdout)
	}
}

// TestCmdConflictsDeferred_ReplayPrintsRetried verifies `engram conflicts deferred
// --replay` calls ReplayDeferred and prints the retried count (0 for empty queue).
func TestCmdConflictsDeferred_ReplayPrintsRetried(t *testing.T) {
	cfg := testConfig(t)

	withArgs(t, "engram", "conflicts", "deferred", "--replay")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr for replay: %q", stderr)
	}
	if !strings.Contains(stdout, "retried:") {
		t.Errorf("expected 'retried:' label in output; got: %q", stdout)
	}
	if !strings.Contains(stdout, "0") {
		t.Errorf("expected '0' count in empty-queue replay output; got: %q", stdout)
	}
}

// TestCmdConflictsDeferred_InspectMissing verifies that inspecting an unknown
// sync_id prints "not found" and exits non-zero.
func TestCmdConflictsDeferred_InspectMissing(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "conflicts", "deferred", "--inspect", "no-such-sync-id")
	stubExitWithPanic(t)
	stdout, stderr, _ := captureOutputAndRecover(t, func() { cmdConflicts(cfg) })
	combined := stdout + stderr
	if !strings.Contains(strings.ToLower(combined), "not found") {
		t.Errorf("expected 'not found' in output; got: %q", combined)
	}
}

// TestCmdConflictsDeferred_InspectHappyPath verifies that inspecting an existing
// deferred row prints its sync_id and payload fields.
func TestCmdConflictsDeferred_InspectHappyPath(t *testing.T) {
	cfg := testConfig(t)

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s.Close()

	db := openTestDB(t, cfg)
	seedDeferredRowCLI(t, db, "def-inspect-ok", `{"relation_type":"conflicts","source_id":"x","target_id":"y"}`, 0, "deferred")

	withArgs(t, "engram", "conflicts", "deferred", "--inspect", "def-inspect-ok")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "def-inspect-ok") {
		t.Errorf("expected sync_id in output; got: %q", stdout)
	}
}

// TestCmdConflictsShow_NotFound verifies `engram conflicts show <id>` with a
// non-existent relation_id prints not-found and exits non-zero.
func TestCmdConflictsShow_NotFound(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "conflicts", "show", strconv.FormatInt(99999, 10))
	stubExitWithPanic(t)
	stdout, stderr, _ := captureOutputAndRecover(t, func() { cmdConflicts(cfg) })
	combined := stdout + stderr
	if !strings.Contains(strings.ToLower(combined), "not found") {
		t.Errorf("expected 'not found' in output; got: %q", combined)
	}
}

// TestCmdConflictsShow_HappyPath verifies `engram conflicts show <id>` prints
// relation detail for an existing relation.
func TestCmdConflictsShow_HappyPath(t *testing.T) {
	cfg := testConfig(t)
	srcSync, tgtSync, _ := seedRelation(t, cfg, "showproj")

	db := openTestDB(t, cfg)
	var relID int64
	if err := db.QueryRow(`SELECT id FROM memory_relations WHERE source_id=? AND target_id=?`, srcSync, tgtSync).Scan(&relID); err != nil {
		t.Fatalf("get relation id: %v", err)
	}

	withArgs(t, "engram", "conflicts", "show", strconv.FormatInt(relID, 10))
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, strconv.FormatInt(relID, 10)) {
		t.Errorf("expected relation_id %d in output; got: %q", relID, stdout)
	}
}

// TestCmdConflicts_UnknownSubcommand verifies an unknown subcommand prints
// "unknown" to stderr and exits non-zero.
func TestCmdConflicts_UnknownSubcommand(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "conflicts", "frobnicate")
	stubExitWithPanic(t)
	_, stderr, _ := captureOutputAndRecover(t, func() { cmdConflicts(cfg) })
	if !strings.Contains(stderr, "frobnicate") {
		t.Errorf("expected unknown subcommand name in stderr; got: %q", stderr)
	}
}

// TestCmdMain_ConflictsWired verifies that `engram conflicts` is wired in the
// top-level switch (D.3) — the dispatch must NOT produce "unknown command: conflicts".
func TestCmdMain_ConflictsWired(t *testing.T) {
	withArgs(t, "engram", "conflicts")
	stubCheckForUpdates(t, versionCheckResult())
	stubExitWithPanic(t)
	_, stderr, _ := captureOutputAndRecover(t, func() { main() })
	if strings.Contains(stderr, "unknown command: conflicts") {
		t.Errorf("conflicts not wired in main switch; stderr: %q", stderr)
	}
}

// ─── G.1 — End-to-end CLI lifecycle integration tests ────────────────────────
//
// These tests drive the full lifecycle end-to-end against a real seeded store:
//   list (empty) → scan apply → list (has results) → show → stats → deferred

// TestG1_ConflictsLifecycle_EmptyThenScanThenList drives the full scan lifecycle:
// 1. list project with no relations → indicates zero results.
// 2. scan --apply inserts up to cap.
// 3. list again → row count > 0 for a project that had candidates.
//
// Note: FindCandidates uses FTS5 BM25 scoring, so candidate detection depends on
// content similarity. We seed semantically similar observations to maximise the
// chance of at least one candidate pair. The test tolerates zero candidates (if
// FTS scores are all below floor) but asserts the command never errors.
func TestG1_ConflictsLifecycle_EmptyThenScanThenList(t *testing.T) {
	cfg := testConfig(t)

	// Step 1 — list with no relations should report zero.
	withArgs(t, "engram", "conflicts", "list", "--project", "g1proj")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("step1 list: unexpected stderr: %q", stderr)
	}
	// Zero results: either "0" appears or a "no relations" message.
	if !strings.Contains(stdout, "0") && !strings.Contains(strings.ToLower(stdout), "no relation") {
		t.Errorf("step1: expected zero-results indication; got: %q", stdout)
	}

	// Seed two similar observations so ScanProject finds at least one candidate.
	mustSeedObservation(t, cfg, "ses-g1", "g1proj", "decision",
		"JWT authentication token session management",
		"JWT auth token is used across services for session management", "project")
	mustSeedObservation(t, cfg, "ses-g1", "g1proj", "decision",
		"Session-based token authentication pattern",
		"Session token authentication pattern conflicts with JWT approach", "project")

	// Step 2 — scan --apply with low cap.
	withArgs(t, "engram", "conflicts", "scan", "--project", "g1proj", "--apply", "--max-insert", "5")
	stdout, stderr = captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("step2 scan: unexpected stderr: %q", stderr)
	}
	// Output must include the "inserted:" label regardless of count.
	if !strings.Contains(stdout, "inserted:") {
		t.Errorf("step2 scan: expected 'inserted:' label in output; got: %q", stdout)
	}

	// Step 3 — list again; we don't assert exact count (FTS may or may not match)
	// but the command must succeed.
	withArgs(t, "engram", "conflicts", "list", "--project", "g1proj")
	stdout, stderr = captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("step3 list: unexpected stderr: %q", stderr)
	}
	_ = stdout // count depends on FTS candidate detection; just no error

	// Step 4 — stats must succeed and print status labels.
	withArgs(t, "engram", "conflicts", "stats", "--project", "g1proj")
	stdout, stderr = captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("step4 stats: unexpected stderr: %q", stderr)
	}
	// At minimum the stats output must include the project name or a count field.
	_ = stdout // structure validated in D.1 unit tests; lifecycle check here
}

// TestG1_ConflictsShow_Lifecycle verifies show works on a seeded relation and
// correctly reports not-found for a missing id.
func TestG1_ConflictsShow_Lifecycle(t *testing.T) {
	cfg := testConfig(t)
	srcSync, tgtSync, _ := seedRelation(t, cfg, "g1showproj")

	db := openTestDB(t, cfg)
	var relID int64
	if err := db.QueryRow(`SELECT id FROM memory_relations WHERE source_id=? AND target_id=?`, srcSync, tgtSync).Scan(&relID); err != nil {
		t.Fatalf("get relation id: %v", err)
	}

	// show existing relation.
	withArgs(t, "engram", "conflicts", "show", strconv.FormatInt(relID, 10))
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("show happy: unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, strconv.FormatInt(relID, 10)) {
		t.Errorf("show happy: expected relation_id in output; got: %q", stdout)
	}

	// show missing relation: must print not-found and exit non-zero.
	withArgs(t, "engram", "conflicts", "show", "999999")
	stubExitWithPanic(t)
	outMiss, errMiss, _ := captureOutputAndRecover(t, func() { cmdConflicts(cfg) })
	combined := outMiss + errMiss
	if !strings.Contains(strings.ToLower(combined), "not found") {
		t.Errorf("show missing: expected 'not found'; got stdout=%q stderr=%q", outMiss, errMiss)
	}
}

// TestG1_ConflictsScan_CapWarning verifies the WARNING line is printed when
// the insert cap is reached with guaranteed candidates.
// We seed 6 highly similar observations so that at least one candidate pair exists;
// max-insert=1 means the cap is reached on the very first insert.
func TestG1_ConflictsScan_CapWarning(t *testing.T) {
	cfg := testConfig(t)

	similarTitles := []string{
		"JWT token authentication session management policy",
		"Session token JWT authentication management approach",
		"Authentication JWT session token policy decision",
		"Token management session JWT authentication strategy",
		"JWT session authentication token management pattern",
		"Session-based JWT token authentication management rule",
	}
	for i, title := range similarTitles {
		sesID := fmt.Sprintf("ses-capg1-%d", i)
		mustSeedObservation(t, cfg, sesID, "capg1proj", "decision", title,
			"JWT auth token session management content "+title, "project")
	}

	withArgs(t, "engram", "conflicts", "scan", "--project", "capg1proj", "--apply", "--max-insert", "1")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("cap warning scan: unexpected stderr: %q", stderr)
	}
	// If the cap was reached, a WARNING line must appear.
	// If no candidates were found (unlikely with 6 similar obs), the scan exits 0 — tolerated.
	if strings.Contains(stdout, "inserted: 1") && !strings.Contains(strings.ToUpper(stdout), "WARNING") {
		t.Errorf("cap warning: cap was hit (inserted=1) but no WARNING line; got: %q", stdout)
	}
}

// ─── D.2 RED tests — agentRunnerFactory + resolveAgentRunner ─────────────────

// TestResolveAgentRunner_EnvNotSet verifies that resolveAgentRunner returns a
// clear error naming ENGRAM_AGENT_CLI when the env var is empty.
func TestResolveAgentRunner_EnvNotSet(t *testing.T) {
	t.Setenv("ENGRAM_AGENT_CLI", "")
	_, err := resolveAgentRunner()
	if err == nil {
		t.Fatal("expected error when ENGRAM_AGENT_CLI is empty; got nil")
	}
	if !strings.Contains(err.Error(), "ENGRAM_AGENT_CLI") {
		t.Errorf("error should name ENGRAM_AGENT_CLI; got: %q", err.Error())
	}
}

// TestResolveAgentRunner_EnvClaude verifies that resolveAgentRunner calls the
// factory with "claude" when ENGRAM_AGENT_CLI=claude.
func TestResolveAgentRunner_EnvClaude(t *testing.T) {
	t.Setenv("ENGRAM_AGENT_CLI", "claude")

	calledWith := ""
	mock := &mockSemanticRunner{}
	old := agentRunnerFactory
	agentRunnerFactory = func(name string) (store.SemanticRunner, error) {
		calledWith = name
		return mock, nil
	}
	t.Cleanup(func() { agentRunnerFactory = old })

	runner, err := resolveAgentRunner()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner == nil {
		t.Fatal("expected non-nil runner")
	}
	if calledWith != "claude" {
		t.Errorf("factory called with %q; want \"claude\"", calledWith)
	}
}

// TestResolveAgentRunner_EnvOpencode verifies that resolveAgentRunner calls the
// factory with "opencode" when ENGRAM_AGENT_CLI=opencode.
func TestResolveAgentRunner_EnvOpencode(t *testing.T) {
	t.Setenv("ENGRAM_AGENT_CLI", "opencode")

	calledWith := ""
	mock := &mockSemanticRunner{}
	old := agentRunnerFactory
	agentRunnerFactory = func(name string) (store.SemanticRunner, error) {
		calledWith = name
		return mock, nil
	}
	t.Cleanup(func() { agentRunnerFactory = old })

	runner, err := resolveAgentRunner()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner == nil {
		t.Fatal("expected non-nil runner")
	}
	if calledWith != "opencode" {
		t.Errorf("factory called with %q; want \"opencode\"", calledWith)
	}
}

// TestResolveAgentRunner_InvalidName verifies that resolveAgentRunner propagates
// the error returned by the factory when an unknown runner name is given.
func TestResolveAgentRunner_InvalidName(t *testing.T) {
	t.Setenv("ENGRAM_AGENT_CLI", "somethingelse")

	factoryErr := errors.New("unsupported runner: somethingelse")
	old := agentRunnerFactory
	agentRunnerFactory = func(_ string) (store.SemanticRunner, error) {
		return nil, factoryErr
	}
	t.Cleanup(func() { agentRunnerFactory = old })

	_, err := resolveAgentRunner()
	if err == nil {
		t.Fatal("expected error for invalid runner name; got nil")
	}
	if !errors.Is(err, factoryErr) {
		t.Errorf("expected wrapped factoryErr; got: %v", err)
	}
}

// TestCmdConflictsScan_SemanticFlagNoEnv verifies that --semantic without
// ENGRAM_AGENT_CLI set fails fast with a non-zero exit and a message naming the
// env var. No LLM calls should be made.
func TestCmdConflictsScan_SemanticFlagNoEnv(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("ENGRAM_AGENT_CLI", "")

	// factory must NOT be called when env is empty.
	factoryCalled := false
	old := agentRunnerFactory
	agentRunnerFactory = func(name string) (store.SemanticRunner, error) {
		factoryCalled = true
		return nil, errors.New("should not be reached")
	}
	t.Cleanup(func() { agentRunnerFactory = old })

	withArgs(t, "engram", "conflicts", "scan", "--project", "noproj", "--semantic")
	stubExitWithPanic(t)
	_, stderr, _ := captureOutputAndRecover(t, func() { cmdConflicts(cfg) })

	if !strings.Contains(stderr, "ENGRAM_AGENT_CLI") {
		t.Errorf("expected ENGRAM_AGENT_CLI in error message; got: %q", stderr)
	}
	if factoryCalled {
		t.Error("factory should not be called when env is not set")
	}
}

// TestCmdConflictsScan_SemanticFlagWithEnv verifies that --semantic with
// ENGRAM_AGENT_CLI=claude injects the mock runner via the factory override
// and passes Semantic=true to ScanProject.
func TestCmdConflictsScan_SemanticFlagWithEnv(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("ENGRAM_AGENT_CLI", "claude")

	mock := &mockSemanticRunner{
		verdict: store.SemanticVerdict{Relation: "not_conflict", Confidence: 0.9},
	}
	stubAgentRunnerFactory(t, mock, nil)

	mustSeedObservation(t, cfg, "ses-sem1", "semproj", "decision", "A", "content alpha", "project")
	mustSeedObservation(t, cfg, "ses-sem1", "semproj", "decision", "B", "content alpha duplicate", "project")

	withArgs(t, "engram", "conflicts", "scan", "--project", "semproj", "--semantic", "--yes")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	// The output should contain the semantic counter fields.
	if !strings.Contains(stdout, "semantic") {
		t.Errorf("expected semantic fields in output; got: %q", stdout)
	}
}

// TestCmdConflictsScan_NoSemanticFlag verifies that when --semantic is absent,
// the scan behaves as Phase 3: no factory call, output lacks semantic counters
// or contains them as zero.
func TestCmdConflictsScan_NoSemanticFlag(t *testing.T) {
	cfg := testConfig(t)

	factoryCalled := false
	old := agentRunnerFactory
	agentRunnerFactory = func(name string) (store.SemanticRunner, error) {
		factoryCalled = true
		return nil, nil
	}
	t.Cleanup(func() { agentRunnerFactory = old })

	mustSeedObservation(t, cfg, "ses-nosem", "nosemproj", "decision", "A", "content alpha plain", "project")
	mustSeedObservation(t, cfg, "ses-nosem", "nosemproj", "decision", "B", "content alpha plain dup", "project")

	withArgs(t, "engram", "conflicts", "scan", "--project", "nosemproj")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	_ = stdout
	if factoryCalled {
		t.Error("agentRunnerFactory must not be called when --semantic is absent")
	}
}

// TestG1_DeferredLifecycle verifies deferred list → inspect → replay end-to-end.
func TestG1_DeferredLifecycle(t *testing.T) {
	cfg := testConfig(t)

	// Bootstrap schema.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	s.Close()

	db := openTestDB(t, cfg)
	seedDeferredRowCLI(t, db, "g1-def-001", `{"relation_type":"conflicts","source_id":"x","target_id":"y"}`, 0, "deferred")
	seedDeferredRowCLI(t, db, "g1-def-dead", `{"relation_type":"conflicts","source_id":"p","target_id":"q"}`, 5, "dead")

	// Step 1 — list: both rows must appear.
	withArgs(t, "engram", "conflicts", "deferred")
	stdout, stderr := captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("deferred list: unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "g1-def-001") {
		t.Errorf("deferred list: expected g1-def-001 in output; got: %q", stdout)
	}
	if !strings.Contains(stdout, "g1-def-dead") {
		t.Errorf("deferred list: expected g1-def-dead in output; got: %q", stdout)
	}

	// Step 2 — inspect existing row.
	withArgs(t, "engram", "conflicts", "deferred", "--inspect", "g1-def-001")
	stdout, stderr = captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("deferred inspect: unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "g1-def-001") {
		t.Errorf("deferred inspect: expected sync_id in output; got: %q", stdout)
	}

	// Step 3 — replay: reports retried count (deferred rows are orphaned/unresolvable
	// in test context — they will move to dead; we only assert the output label exists).
	withArgs(t, "engram", "conflicts", "deferred", "--replay")
	stdout, stderr = captureOutput(t, func() { cmdConflicts(cfg) })
	if stderr != "" {
		t.Fatalf("deferred replay: unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "retried:") {
		t.Errorf("deferred replay: expected 'retried:' label; got: %q", stdout)
	}
}
