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
		{name: "unsupported check", args: []string{"engram", "doctor", "repair", "--project", "sias-app", "--check", "sync_mutation_required_fields", "--plan"}, want: "unsupported repair check"},
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
