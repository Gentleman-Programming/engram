package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	engrammcp "github.com/Gentleman-Programming/engram/internal/mcp"
	"github.com/Gentleman-Programming/engram/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
	_ "modernc.org/sqlite"
)

func seedDoctorSession(t *testing.T, cfg store.Config, id, project, directory string) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.CreateSession(id, project, directory); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
}

func newDoctorGitRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir git repo: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	run("remote", "add", "origin", "git@github.com:user/"+name+".git")
	return dir
}

// initDoctorStore creates the schema so raw seeding helpers can insert rows
// without going through a higher level command first.
func initDoctorStore(t *testing.T, cfg store.Config) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}
}

func seedDoctorPendingMutation(t *testing.T, cfg store.Config, project, entity, entityKey, op, payload string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES (?, ?, ?, ?, ?, ?, ?)`, store.DefaultSyncTargetKey, entity, entityKey, op, payload, store.SyncSourceLocal, project); err != nil {
		t.Fatalf("insert sync mutation: %v", err)
	}
}

func enrollDoctorProject(t *testing.T, cfg store.Config, project string) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.EnrollProject(project); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
}

// doctorValidObservationPayload builds a sync payload that passes required
// field validation, so the only findings a test can produce come from the
// non-enrolled backlog rule.
func doctorValidObservationPayload(syncID, project string) string {
	return `{"sync_id":"` + syncID + `","session_id":"session-` + syncID + `","type":"decision","title":"Valid","content":"Pending mutation","project":"` + project + `","scope":"project"}`
}

func decodeDoctorReport(t *testing.T, out string) map[string]any {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("doctor json invalid: %v\n%s", err, out)
	}
	return report
}

// doctorNonEnrolledCounts collects the per-project pending mutation counts
// reported by non_enrolled_pending_mutations findings, failing on any other
// finding so unrelated regressions cannot pass silently.
func doctorNonEnrolledCounts(t *testing.T, report map[string]any) map[string]float64 {
	t.Helper()
	checks, ok := report["checks"].([]any)
	if !ok || len(checks) != 1 {
		t.Fatalf("expected one check, got %v", report["checks"])
	}
	counts := map[string]float64{}
	findings, _ := checks[0].(map[string]any)["findings"].([]any)
	for _, raw := range findings {
		finding := raw.(map[string]any)
		if finding["reason_code"] != "non_enrolled_pending_mutations" {
			t.Fatalf("unexpected finding reason_code: %v", finding)
		}
		evidence := finding["evidence"].(map[string]any)
		counts[evidence["project"].(string)] = evidence["pending_mutations"].(float64)
	}
	return counts
}

func seedDoctorRepairRows(t *testing.T, cfg store.Config, id, project, directory string) {
	t.Helper()
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	if err := s.CreateSession(id, project, directory); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{SessionID: id, Type: "bugfix", Title: "repair", Content: "content", Project: project, Scope: "project"}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if _, err := s.AddPrompt(store.AddPromptParams{SessionID: id, Content: "prompt", Project: project}); err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}
}

func TestCmdDoctorRepairValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing mode", args: []string{"engram", "doctor", "repair", "--project", "sias-app", "--check", "session_project_directory_mismatch"}, want: "exactly one of --plan, --dry-run, or --apply is required"},
		{name: "multiple modes", args: []string{"engram", "doctor", "repair", "--project", "sias-app", "--check", "session_project_directory_mismatch", "--plan", "--apply"}, want: "exactly one of --plan, --dry-run, or --apply is required"},
		{name: "missing project", args: []string{"engram", "doctor", "repair", "--check", "session_project_directory_mismatch", "--plan"}, want: "--project is required"},
		{name: "unsupported check", args: []string{"engram", "doctor", "repair", "--project", "sias-app", "--check", "not_real", "--plan"}, want: "unsupported repair check"},
		{name: "orphaned observation session is report only", args: []string{"engram", "doctor", "repair", "--project", "sias-app", "--check", "orphaned_observation_session", "--apply"}, want: "unsupported repair check orphaned_observation_session"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			oldExit := exitFunc
			exited := false
			exitFunc = func(code int) { exited = code != 0 }
			t.Cleanup(func() { exitFunc = oldExit })
			withArgs(t, tc.args...)
			_, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
			if !exited || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exited=%v stderr=%q want %q", exited, stderr, tc.want)
			}
		})
	}
}

func TestCmdDoctorRepairPlanDryRunApplyJSON(t *testing.T) {
	cfg := testConfig(t)
	repo := newDoctorGitRepo(t, "engram")
	seedDoctorRepairRows(t, cfg, "repair-s1", "sias-app", repo)

	withArgs(t, "engram", "doctor", "repair", "--project", "sias-app", "--check", "session_project_directory_mismatch", "--plan")
	planOut, planErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if planErr != "" {
		t.Fatalf("plan stderr=%q", planErr)
	}
	plan := decodeRepairPlan(t, planOut)
	if plan["status"] != "planned" || plan["mode"] != "plan" || len(plan["actions"].([]any)) != 1 {
		t.Fatalf("plan=%v", plan)
	}
	counts := plan["counts"].(map[string]any)
	if counts["sessions_planned"] != float64(1) || counts["observations_planned"] != float64(1) || counts["prompts_planned"] != float64(1) {
		t.Fatalf("plan counts=%v", counts)
	}
	assertDoctorRepairProject(t, cfg, "repair-s1", "sias-app")

	withArgs(t, "engram", "doctor", "repair", "--project", "sias-app", "--check", "session_project_directory_mismatch", "--dry-run")
	dryOut, dryErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if dryErr != "" {
		t.Fatalf("dry-run stderr=%q", dryErr)
	}
	dry := decodeRepairPlan(t, dryOut)
	if dry["status"] != "dry_run" || dry["mode"] != "dry_run" {
		t.Fatalf("dry=%v", dry)
	}
	assertDoctorRepairProject(t, cfg, "repair-s1", "sias-app")

	withArgs(t, "engram", "doctor", "repair", "--project", "sias-app", "--check", "session_project_directory_mismatch", "--apply")
	applyOut, applyErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if applyErr != "" {
		t.Fatalf("apply stderr=%q", applyErr)
	}
	applied := decodeRepairPlan(t, applyOut)
	if applied["status"] != "applied" || applied["backup_path"] == "" {
		t.Fatalf("applied=%v", applied)
	}
	appliedCounts := applied["counts"].(map[string]any)
	if appliedCounts["sessions_applied"] != float64(1) || appliedCounts["observations_applied"] != float64(1) || appliedCounts["prompts_applied"] != float64(1) {
		t.Fatalf("applied counts=%v", appliedCounts)
	}
	if _, err := os.Stat(applied["backup_path"].(string)); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	assertDoctorRepairProject(t, cfg, "repair-s1", "engram")
}

func TestCmdDoctorRepairInvalidSessionIdentityReportsExplicitImpossibility(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close initialized store: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, project, directory) VALUES ('', 'engram', '/tmp/engram');
		INSERT INTO sync_enrolled_projects (project) VALUES ('engram');`); err != nil {
		_ = db.Close()
		t.Fatalf("seed corrupt session: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}

	for _, mode := range []string{"--plan", "--dry-run", "--apply"} {
		t.Run(mode, func(t *testing.T) {
			withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "invalid_session_identity", mode)
			stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
			if stderr != "" {
				t.Fatalf("stderr=%q", stderr)
			}
			plan := decodeRepairPlan(t, stdout)
			if plan["status"] != "noop" || len(plan["actions"].([]any)) != 0 {
				t.Fatalf("plan=%v", plan)
			}
			skipped := plan["skipped"].([]any)
			if len(skipped) != 1 || skipped[0].(map[string]any)["reason_code"] != "cannot_repair_without_explicit_canonical_session_id" {
				t.Fatalf("skipped=%v", skipped)
			}
		})
	}

	db, err = sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer db.Close()
	var sessions, mutations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ''`).Scan(&sessions); err != nil {
		t.Fatalf("count source sessions: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("repair unexpectedly changed source session count=%d", sessions)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ?`, store.SyncEntitySession).Scan(&mutations); err != nil {
		t.Fatalf("count session mutations: %v", err)
	}
	if mutations != 0 {
		t.Fatalf("doctor startup emitted %d broken session mutation(s)", mutations)
	}
}

func decodeRepairPlan(t *testing.T, out string) map[string]any {
	t.Helper()
	var plan map[string]any
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("repair json invalid: %v\n%s", err, out)
	}
	return plan
}

func assertDoctorRepairProject(t *testing.T, cfg store.Config, sessionID, wantProject string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	for _, query := range []string{`SELECT project FROM sessions WHERE id = ?`, `SELECT project FROM observations WHERE session_id = ?`, `SELECT project FROM user_prompts WHERE session_id = ?`} {
		var got string
		if err := db.QueryRow(query, sessionID).Scan(&got); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		if got != wantProject {
			t.Fatalf("project=%q want %q for query %q", got, wantProject, query)
		}
	}
}

func TestCmdDoctorJSONSingleCheckAndProjectScope(t *testing.T) {
	cfg := testConfig(t)
	otherRepo := newDoctorGitRepo(t, "other")
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", otherRepo)
	seedDoctorSession(t, cfg, "manual-save-other", "other", otherRepo)
	withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "session_project_directory_mismatch")

	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor json invalid: %v\n%s", err, stdout)
	}
	if report["status"] != "warning" || report["project"] != "engram" {
		t.Fatalf("report=%v", report)
	}
	checks := report["checks"].([]any)
	if len(checks) != 1 || checks[0].(map[string]any)["check_id"] != "session_project_directory_mismatch" {
		t.Fatalf("checks=%v", checks)
	}
}

func TestCmdDoctorTextOutput(t *testing.T) {
	cfg := testConfig(t)
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", "/work/engram")
	withArgs(t, "engram", "doctor", "--project", "engram", "--check", "manual_session_name_project_mismatch")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	if !strings.Contains(stdout, "Engram Doctor: ok") || !strings.Contains(stdout, "manual_session_name_project_mismatch") {
		t.Fatalf("stdout=%q", stdout)
	}
}

func TestCmdDoctorOrphanedObservationSessionRoutesWithoutRepair(t *testing.T) {
	cfg := testConfig(t)
	initDoctorStore(t, cfg)
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err := db.Exec(`
		PRAGMA foreign_keys = OFF;
		INSERT INTO observations
			(sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at)
		VALUES ('obs-orphan', 'missing-session', 'bugfix', 'orphan', 'content', 'engram', 'project', 'obs-orphan', 1, 1, datetime('now'), datetime('now'));
	`); err != nil {
		_ = db.Close()
		t.Fatalf("seed orphaned observation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}

	withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "orphaned_observation_session")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("json stderr=%q", stderr)
	}
	report := decodeDoctorReport(t, stdout)
	check := report["checks"].([]any)[0].(map[string]any)
	finding := check["findings"].([]any)[0].(map[string]any)
	if report["status"] != "warning" || check["check_id"] != "orphaned_observation_session" || finding["requires_confirmation"] != true {
		t.Fatalf("json report=%v", report)
	}

	withArgs(t, "engram", "doctor", "--project", "engram", "--check", "orphaned_observation_session")
	stdout, stderr = captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" || !strings.Contains(stdout, "orphaned_observation_session") || !strings.Contains(stdout, "missing-session") {
		t.Fatalf("text stderr=%q stdout=%q", stderr, stdout)
	}

	oldExit := exitFunc
	exited := false
	exitFunc = func(code int) { exited = code != 0 }
	t.Cleanup(func() { exitFunc = oldExit })
	withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "orphaned_observation_session", "--apply")
	stdout, stderr = captureOutput(t, func() { cmdDoctor(cfg) })
	if !exited || !strings.Contains(stdout, "usage: engram doctor") || !strings.Contains(stderr, "unsupported repair check orphaned_observation_session") {
		t.Fatalf("repair exited=%v stdout=%q stderr=%q", exited, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "backups")); !os.IsNotExist(err) {
		t.Fatalf("unsupported repair created backup directory: %v", err)
	}
	db, err = sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations WHERE sync_id = 'obs-orphan'`).Scan(&count); err != nil {
		t.Fatalf("count orphaned observations: %v", err)
	}
	if count != 1 {
		t.Fatalf("unsupported repair changed observations: count=%d", count)
	}
}

func TestCmdDoctorInvalidCheckFailsLoudly(t *testing.T) {
	cfg := testConfig(t)
	oldExit := exitFunc
	exited := false
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })
	withArgs(t, "engram", "doctor", "--check", "not_real")
	_, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if !exited || !strings.Contains(stderr, "invalid diagnostic check") {
		t.Fatalf("exited=%v stderr=%q", exited, stderr)
	}
}

func TestCmdDoctorJSONMatchesMemDoctorEnvelope(t *testing.T) {
	cfg := testConfig(t)
	otherRepo := newDoctorGitRepo(t, "other")
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", otherRepo)

	withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "session_project_directory_mismatch")
	cliStdout, cliStderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if cliStderr != "" {
		t.Fatalf("cli stderr=%q", cliStderr)
	}
	var cliEnvelope map[string]any
	if err := json.Unmarshal([]byte(cliStdout), &cliEnvelope); err != nil {
		t.Fatalf("cli json invalid: %v\n%s", err, cliStdout)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	mcpRes, err := engrammcp.DoctorToolHandler(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"project": "engram",
		"check":   "session_project_directory_mismatch",
	}}})
	if err != nil {
		t.Fatalf("mem_doctor handler: %v", err)
	}
	mcpText, ok := mcppkg.AsTextContent(mcpRes.Content[0])
	if !ok {
		t.Fatal("expected text content from mem_doctor")
	}
	var mcpEnvelope map[string]any
	if err := json.Unmarshal([]byte(mcpText.Text), &mcpEnvelope); err != nil {
		t.Fatalf("mcp json invalid: %v\n%s", err, mcpText.Text)
	}
	if !reflect.DeepEqual(cliEnvelope, mcpEnvelope) {
		t.Fatalf("CLI and MCP doctor envelopes differ\nCLI=%v\nMCP=%v", cliEnvelope, mcpEnvelope)
	}
}

func TestCmdDoctorSyncMutationRequiredFieldsBlockedEnvelope(t *testing.T) {
	cfg := testConfig(t)
	seedDoctorSession(t, cfg, "manual-save-engram", "engram", "/work/engram")
	seedDoctorPendingMutation(t, cfg, "engram", store.SyncEntityObservation, "obs-missing", store.SyncOpUpsert, `{"sync_id":"obs-missing"}`)

	withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "sync_mutation_required_fields")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}

	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor json invalid: %v\n%s", err, stdout)
	}
	if report["status"] != "blocked" {
		t.Fatalf("expected blocked report, got %v", report)
	}
	checks := report["checks"].([]any)
	if len(checks) != 1 {
		t.Fatalf("expected one check, got %v", checks)
	}
	check := checks[0].(map[string]any)
	if check["check_id"] != "sync_mutation_required_fields" || check["result"] != "blocked" || check["severity"] != "blocking" {
		t.Fatalf("unexpected check envelope: %v", check)
	}
	findings := check["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %v", findings)
	}
	finding := findings[0].(map[string]any)
	if finding["reason_code"] != "sync_mutation_payload_missing_required_fields" || finding["requires_confirmation"] != true {
		t.Fatalf("unexpected finding: %v", finding)
	}
	evidence := finding["evidence"].(map[string]any)
	if evidence["entity"] != store.SyncEntityObservation || evidence["entity_key"] != "obs-missing" {
		t.Fatalf("unexpected evidence: %v", evidence)
	}
}

func TestCmdDoctorRepairQuarantinesOnlyIrreparableMutations(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	seedDoctorPendingMutation(t, cfg, "", store.SyncEntitySession, "poison", store.SyncOpUpsert, `{"id":"poison"}`)
	seedDoctorPendingMutation(t, cfg, "", store.SyncEntitySession, "later", store.SyncOpDelete, `{"id":"later"}`)

	withArgs(t, "engram", "doctor", "repair", "--check", "sync_mutation_required_fields", "--dry-run")
	dryOut, dryErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if dryErr != "" {
		t.Fatalf("dry-run stderr=%q", dryErr)
	}
	dry := decodeRepairPlan(t, dryOut)
	if dry["applied"] != false || len(dry["actions"].([]any)) != 1 {
		t.Fatalf("dry-run=%v", dry)
	}

	withArgs(t, "engram", "doctor", "repair", "--check", "sync_mutation_required_fields", "--apply")
	applyOut, applyErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if applyErr != "" {
		t.Fatalf("apply stderr=%q", applyErr)
	}
	applied := decodeRepairPlan(t, applyOut)
	if applied["applied"] != true || len(applied["actions"].([]any)) != 1 {
		t.Fatalf("apply=%v", applied)
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var poison, later string
	if err := db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity_key = 'poison'`).Scan(&poison); err != nil {
		t.Fatalf("read poison: %v", err)
	}
	if err := db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity_key = 'later'`).Scan(&later); err != nil {
		t.Fatalf("read later: %v", err)
	}
	if poison != store.SyncMutationDispositionQuarantined || later != store.SyncMutationDispositionPending {
		t.Fatalf("dispositions poison=%q later=%q", poison, later)
	}
}

func TestCmdDoctorRepairRepairsTitleOnlyObservationMutation(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateSession("title-repair", "engram", "/work/engram"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{SessionID: "title-repair", Type: "bugfix", Title: "valid", Content: "Recovered title. More detail.", Project: "engram", Scope: "project"})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE observations SET title = '' WHERE id = ?`, id); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE sync_mutations SET payload = json_set(payload, '$.title', '') WHERE entity = ? AND entity_key = ?`, store.SyncEntityObservation, obs.SyncID); err != nil {
		t.Fatalf("seed mutation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "sync_mutation_required_fields", "--plan")
	planOut, planErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if planErr != "" {
		t.Fatalf("plan stderr=%q", planErr)
	}
	plan := decodeRepairPlan(t, planOut)
	repairs := plan["repairs"].([]any)
	if plan["applied"] != false || len(repairs) != 1 {
		t.Fatalf("plan=%v", plan)
	}
	repairedSeq := repairs[0].(map[string]any)["seq"]
	for _, action := range plan["actions"].([]any) {
		if action.(map[string]any)["seq"] == repairedSeq {
			t.Fatalf("repaired sequence %v remained in residual actions: %v", repairedSeq, plan)
		}
	}
	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("reopen store after plan: %v", err)
	}
	planned, err := s.GetObservation(id)
	if err != nil || planned.Title != "" {
		t.Fatalf("plan mutated observation=%+v err=%v", planned, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store after plan: %v", err)
	}
	withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "sync_mutation_required_fields", "--apply")
	out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	report := decodeRepairPlan(t, out)
	if report["applied"] != true || len(report["repairs"].([]any)) != 1 {
		t.Fatalf("report=%v", report)
	}
	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s.Close()
	updated, err := s.GetObservation(id)
	if err != nil || updated.Title != "Recovered title." {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func TestCmdDoctorRepairApplyUnblocksDoctorAndKeepsPendingWork(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	seedDoctorPendingMutation(t, cfg, "engram", store.SyncEntitySession, "poison", store.SyncOpUpsert, `{"id":"poison"}`)
	seedDoctorPendingMutation(t, cfg, "engram", store.SyncEntitySession, "keep", store.SyncOpUpsert, `{"id":"keep","directory":"/work/engram"}`)
	// `engram` is enrolled on purpose: the repair contract this test pins is the
	// cloud one, so the check must run past the cloud-sync gate instead of taking
	// the local-only early return.
	enrollDoctorProject(t, cfg, "engram")

	runDoctor := func(stage string) map[string]any {
		t.Helper()
		withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "sync_mutation_required_fields")
		stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
		if stderr != "" {
			t.Fatalf("%s stderr=%q", stage, stderr)
		}
		var report map[string]any
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("%s doctor json invalid: %v\n%s", stage, err, stdout)
		}
		return report
	}

	if report := runDoctor("before repair"); report["status"] != "blocked" {
		t.Fatalf("expected blocked doctor before repair, got %v", report)
	}

	withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "sync_mutation_required_fields", "--apply")
	applyOut, applyErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if applyErr != "" {
		t.Fatalf("apply stderr=%q", applyErr)
	}
	applied := decodeRepairPlan(t, applyOut)
	if applied["applied"] != true || len(applied["actions"].([]any)) != 1 {
		t.Fatalf("apply=%v", applied)
	}

	report := runDoctor("after repair")
	if report["status"] == "blocked" {
		t.Fatalf("doctor stayed blocked after quarantine repair: %v", report)
	}
	check := report["checks"].([]any)[0].(map[string]any)
	if check["result"] == "blocked" || check["severity"] == "blocking" {
		t.Fatalf("check stayed blocking after quarantine repair: %v", check)
	}
	findings := check["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("expected the quarantined row to remain visible as evidence, got %v", findings)
	}
	finding := findings[0].(map[string]any)
	if finding["severity"] != "info" || finding["reason_code"] != "sync_mutation_quarantined" || finding["requires_confirmation"] != false {
		t.Fatalf("unexpected quarantined finding: %v", finding)
	}
	evidence := finding["evidence"].(map[string]any)
	if evidence["entity_key"] != "poison" || evidence["disposition"] != store.SyncMutationDispositionQuarantined {
		t.Fatalf("quarantined evidence lost mutation identity: %v", evidence)
	}

	reopened, err := store.New(cfg)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	pending, err := reopened.HasPendingSyncMutationsForProject("engram")
	if err != nil || !pending {
		t.Fatalf("HasPendingSyncMutationsForProject=%v err=%v", pending, err)
	}
	for _, targetKey := range []string{store.DefaultSyncTargetKey, store.DefaultSyncTargetKey + ":engram"} {
		state, err := reopened.GetSyncState(targetKey)
		if err != nil {
			t.Fatalf("state for %q: %v", targetKey, err)
		}
		if state.Lifecycle != store.SyncLifecyclePending {
			t.Fatalf("quarantine repair masked pending work for %q: lifecycle=%q", targetKey, state.Lifecycle)
		}
	}
}

func TestPrintDoctorUsageMarksProjectOptionalOnlyForSyncMutationRepair(t *testing.T) {
	withArgs(t, "engram", "doctor", "--help")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(testConfig(t)) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	wantLines := []string{
		"usage: engram doctor [--json] [--project PROJECT] [--check CODE]",
		"       engram doctor repair --project PROJECT --check CODE (--plan|--dry-run|--apply)",
		"       engram doctor repair [--project PROJECT] --check sync_mutation_required_fields (--plan|--dry-run|--apply)",
	}
	for _, line := range wantLines {
		if !strings.Contains(stdout, line+"\n") {
			t.Fatalf("usage missing line %q\n%s", line, stdout)
		}
	}
	if !strings.Contains(stdout, "checks: ") {
		t.Fatalf("usage lost the registered check list\n%s", stdout)
	}
}

func TestCmdDoctorNonEnrolledPendingMutationsBlockedEnvelope(t *testing.T) {
	cfg := testConfig(t)
	seedDoctorSession(t, cfg, "manual-save-bootstrap", "bootstrap", "/work/bootstrap")
	seedDoctorPendingMutation(t, cfg, "unmanaged", store.SyncEntityObservation, "obs-valid", store.SyncOpUpsert, `{"sync_id":"obs-valid","session_id":"session-valid","type":"decision","title":"Valid","content":"Pending mutation","project":"unmanaged","scope":"project"}`)
	enrollDoctorProject(t, cfg, "cloud-synced")

	withArgs(t, "engram", "doctor", "--json", "--project", "unmanaged", "--check", "sync_mutation_required_fields")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}

	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor json invalid: %v\n%s", err, stdout)
	}
	if report["status"] != "blocked" {
		t.Fatalf("expected blocked report, got %v", report)
	}
	check := report["checks"].([]any)[0].(map[string]any)
	if check["result"] != "blocked" || check["severity"] != "blocking" || check["safe_next_step"] == "No action required." {
		t.Fatalf("unexpected check envelope: %v", check)
	}
	finding := check["findings"].([]any)[0].(map[string]any)
	if finding["reason_code"] != "non_enrolled_pending_mutations" || !strings.Contains(finding["safe_next_step"].(string), "engram cloud enroll <project>") {
		t.Fatalf("unexpected finding: %v", finding)
	}
	evidence := finding["evidence"].(map[string]any)
	if evidence["project"] != "unmanaged" || evidence["pending_mutations"] != float64(1) {
		t.Fatalf("unexpected evidence: %v", evidence)
	}

	withArgs(t, "engram", "doctor", "--project", "unmanaged", "--check", "sync_mutation_required_fields")
	stdout, stderr = captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" || !strings.Contains(stdout, "non_enrolled_pending_mutations") || !strings.Contains(stdout, "unmanaged") {
		t.Fatalf("stderr=%q stdout=%q", stderr, stdout)
	}
}

// TestCmdDoctorNonEnrolledPendingMutationsRespectsProjectScope proves that a
// scoped run only reports the requested project, so a non-enrolled backlog in
// another project never leaks into the findings.
func TestCmdDoctorNonEnrolledPendingMutationsRespectsProjectScope(t *testing.T) {
	cfg := testConfig(t)
	initDoctorStore(t, cfg)
	seedDoctorPendingMutation(t, cfg, "unmanaged", store.SyncEntityObservation, "obs-unmanaged", store.SyncOpUpsert, doctorValidObservationPayload("obs-unmanaged", "unmanaged"))
	seedDoctorPendingMutation(t, cfg, "out-of-scope", store.SyncEntityObservation, "obs-out-of-scope", store.SyncOpUpsert, doctorValidObservationPayload("obs-out-of-scope", "out-of-scope"))
	enrollDoctorProject(t, cfg, "cloud-synced")

	withArgs(t, "engram", "doctor", "--json", "--project", "unmanaged", "--check", "sync_mutation_required_fields")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}

	report := decodeDoctorReport(t, stdout)
	if report["status"] != "blocked" || report["project"] != "unmanaged" {
		t.Fatalf("unexpected report: %v", report)
	}
	counts := doctorNonEnrolledCounts(t, report)
	if len(counts) != 1 || counts["unmanaged"] != 1 {
		t.Fatalf("expected only the scoped project, got %v", counts)
	}
	if strings.Contains(stdout, "out-of-scope") {
		t.Fatalf("out-of-scope project leaked into scoped report: %s", stdout)
	}
}

// TestCmdDoctorNonEnrolledPendingMutationsReportsEveryProject proves an
// unscoped run reports every non-enrolled project with its own backlog count,
// and never reports enrolled projects.
func TestCmdDoctorNonEnrolledPendingMutationsReportsEveryProject(t *testing.T) {
	cfg := testConfig(t)
	initDoctorStore(t, cfg)
	seedDoctorPendingMutation(t, cfg, "alpha", store.SyncEntityObservation, "obs-alpha-1", store.SyncOpUpsert, doctorValidObservationPayload("obs-alpha-1", "alpha"))
	seedDoctorPendingMutation(t, cfg, "alpha", store.SyncEntityObservation, "obs-alpha-2", store.SyncOpUpsert, doctorValidObservationPayload("obs-alpha-2", "alpha"))
	seedDoctorPendingMutation(t, cfg, "beta", store.SyncEntityObservation, "obs-beta-1", store.SyncOpUpsert, doctorValidObservationPayload("obs-beta-1", "beta"))
	seedDoctorPendingMutation(t, cfg, "beta", store.SyncEntityObservation, "obs-beta-2", store.SyncOpUpsert, doctorValidObservationPayload("obs-beta-2", "beta"))
	seedDoctorPendingMutation(t, cfg, "beta", store.SyncEntityObservation, "obs-beta-3", store.SyncOpUpsert, doctorValidObservationPayload("obs-beta-3", "beta"))
	seedDoctorPendingMutation(t, cfg, "enrolled", store.SyncEntityObservation, "obs-enrolled", store.SyncOpUpsert, doctorValidObservationPayload("obs-enrolled", "enrolled"))
	enrollDoctorProject(t, cfg, "enrolled")

	withArgs(t, "engram", "doctor", "--json", "--check", "sync_mutation_required_fields")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}

	report := decodeDoctorReport(t, stdout)
	if report["status"] != "blocked" {
		t.Fatalf("expected blocked report, got %v", report)
	}
	counts := doctorNonEnrolledCounts(t, report)
	want := map[string]float64{"alpha": 2, "beta": 3}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("counts=%v want %v", counts, want)
	}
	if strings.Contains(stdout, `"project": "enrolled"`) {
		t.Fatalf("enrolled project reported as blocked: %s", stdout)
	}
}

// TestCmdDoctorNonEnrolledPendingMutationsTextGuidance proves the human
// readable output carries the enrollment guidance and the pending mutation
// count, not just the machine readable envelope.
func TestCmdDoctorNonEnrolledPendingMutationsTextGuidance(t *testing.T) {
	cfg := testConfig(t)
	initDoctorStore(t, cfg)
	seedDoctorPendingMutation(t, cfg, "unmanaged", store.SyncEntityObservation, "obs-one", store.SyncOpUpsert, doctorValidObservationPayload("obs-one", "unmanaged"))
	seedDoctorPendingMutation(t, cfg, "unmanaged", store.SyncEntityObservation, "obs-two", store.SyncOpUpsert, doctorValidObservationPayload("obs-two", "unmanaged"))
	enrollDoctorProject(t, cfg, "cloud-synced")

	withArgs(t, "engram", "doctor", "--project", "unmanaged", "--check", "sync_mutation_required_fields")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}

	for _, want := range []string{
		"Engram Doctor: blocked",
		"[blocked] sync_mutation_required_fields",
		"next: Run `engram cloud enroll <project>`",
		"- non_enrolled_pending_mutations:",
		`"pending_mutations":2`,
		`"project":"unmanaged"`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("doctor text missing %q\n%s", want, stdout)
		}
	}
}

// TestCmdDoctorLocalOnlyInstallIsNotBlockedByPendingMutations pins the local
// only contract for issue #688: an install that never enrolled any project for
// cloud sync keeps journaling mutations forever, so doctor must report a clean
// bill of health instead of demanding `engram cloud enroll` for a feature the
// user never opted into.
func TestCmdDoctorLocalOnlyInstallIsNotBlockedByPendingMutations(t *testing.T) {
	cfg := testConfig(t)
	initDoctorStore(t, cfg)
	seedDoctorPendingMutation(t, cfg, "local-only", store.SyncEntityObservation, "obs-one", store.SyncOpUpsert, doctorValidObservationPayload("obs-one", "local-only"))
	seedDoctorPendingMutation(t, cfg, "local-only", store.SyncEntityObservation, "obs-two", store.SyncOpUpsert, doctorValidObservationPayload("obs-two", "local-only"))

	withArgs(t, "engram", "doctor", "--json", "--check", "sync_mutation_required_fields")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}

	report := decodeDoctorReport(t, stdout)
	if report["status"] != "ok" {
		t.Fatalf("local-only install must not be blocked, got %v", report)
	}
	check := report["checks"].([]any)[0].(map[string]any)
	if check["result"] != "ok" || check["findings"] != nil {
		t.Fatalf("unexpected check envelope: %v", check)
	}
	if strings.Contains(stdout, "non_enrolled_pending_mutations") || strings.Contains(stdout, "engram cloud enroll") {
		t.Fatalf("local-only doctor must not suggest cloud enrollment: %s", stdout)
	}
}
