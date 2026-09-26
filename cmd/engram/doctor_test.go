package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/diagnostic"
	engrammcp "github.com/Gentleman-Programming/engram/v2/internal/mcp"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
	_ "modernc.org/sqlite"
)

func TestDoctorUnfilteredFixturesIgnoreInheritedKimiHome(t *testing.T) {
	kimiHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(kimiHome, "mcp.json"), []byte("{invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIMI_CODE_HOME", kimiHome)
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"stale generic MCP", TestDoctorReportsStaleGenericMCP},
		{"multiple clients", TestDoctorMultiClientTextAndJSON},
		{"inspection error", TestDoctorMCPInspectionErrorIsReported},
	} {
		t.Run(tc.name, tc.run)
	}
}

func TestDoctorReportsStaleGenericMCP(t *testing.T) {
	cfg := testConfig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")
	t.Setenv("KIMI_CODE_HOME", "")
	path := filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(home, "missing-engram")
	raw, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"engram": map[string]any{"command": missing, "args": []string{"mcp", "--tools=agent"}}, "other": map[string]any{"command": "other"}}})
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	withArgs(t, "engram", "doctor", "--json")
	out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatal(stderr)
	}
	report := decodeDoctorReport(t, out)
	if report["status"] != "warning" || !strings.Contains(out, "engram setup windsurf") {
		t.Fatalf("unexpected report: %s", out)
	}
	if report["summary"].(map[string]any)["warnings"].(float64) < 1 {
		t.Fatalf("summary: %s", out)
	}
}

func TestDoctorMultiClientTextAndJSON(t *testing.T) {
	cfg := testConfig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")
	t.Setenv("KIMI_CODE_HOME", "")
	missing := filepath.Join(home, "missing-engram")
	for _, client := range []struct{ slug, path string }{
		{"windsurf", filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")},
		{"qwen", filepath.Join(home, ".qwen", "settings.json")},
	} {
		if err := os.MkdirAll(filepath.Dir(client.path), 0755); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"engram": map[string]any{"command": missing, "args": []string{"mcp", "--tools=agent"}}, "other": map[string]any{"command": "other"}}})
		if err := os.WriteFile(client.path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	withArgs(t, "engram", "doctor", "--json")
	out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatal(stderr)
	}
	report := decodeDoctorReport(t, out)
	checks := report["checks"].([]any)
	last := checks[len(checks)-1].(map[string]any)
	if last["check_id"] != "stale_mcp_command" || len(last["findings"].([]any)) != 2 || report["summary"].(map[string]any)["warnings"] != float64(1) {
		t.Fatalf("report: %s", out)
	}
	for _, slug := range []string{"windsurf", "qwen"} {
		if !strings.Contains(out, "engram setup "+slug) {
			t.Fatalf("missing %s: %s", slug, out)
		}
	}
	withArgs(t, "engram", "doctor")
	text, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" || !strings.Contains(text, "warnings=1") {
		t.Fatalf("text=%q stderr=%q", text, stderr)
	}
	for _, slug := range []string{"windsurf", "qwen"} {
		if !strings.Contains(text, "engram setup "+slug) {
			t.Fatalf("missing %s: %s", slug, text)
		}
	}
}

func TestDoctorMCPInspectionErrorIsReported(t *testing.T) {
	cfg := testConfig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")
	t.Setenv("KIMI_CODE_HOME", "")
	path := filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	withArgs(t, "engram", "doctor", "--json")
	out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	report := decodeDoctorReport(t, out)
	if report["status"] != "error" || report["summary"].(map[string]any)["errors"] != float64(1) || !strings.Contains(out, "mcp_inspection_error") {
		t.Fatalf("unexpected report: %s", out)
	}
	withArgs(t, "engram", "doctor")
	text, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" || !strings.Contains(text, "errors=1") || !strings.Contains(text, "mcp_inspection_error") {
		t.Fatalf("text=%q stderr=%q", text, stderr)
	}
	withArgs(t, "engram", "doctor", "--json", "--check", "session_project_directory_mismatch")
	filtered, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("filtered stderr=%q", stderr)
	}
	selected := decodeDoctorReport(t, filtered)
	checks := selected["checks"].([]any)
	if len(checks) != 1 || checks[0].(map[string]any)["check_id"] != "session_project_directory_mismatch" || strings.Contains(filtered, "mcp_inspection_error") || selected["status"] != "ok" || selected["summary"].(map[string]any)["errors"] != float64(0) {
		t.Fatalf("filtered report=%s", filtered)
	}
}

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
		{name: "multiple sync mutation modes", args: []string{"engram", "doctor", "repair", "--check", "sync_mutation_required_fields", "--dry-run", "--apply"}, want: "exactly one of --plan, --dry-run, or --apply is required"},
		{name: "missing project", args: []string{"engram", "doctor", "repair", "--check", "session_project_directory_mismatch", "--plan"}, want: "--project is required"},
		{name: "unsupported check", args: []string{"engram", "doctor", "repair", "--project", "sias-app", "--check", "not_real", "--plan"}, want: "unsupported repair check"},
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

// TestCmdDoctorRepairClassificationMatrixAndDispatchInvariant executes every
// advertised repairable code through cmdDoctorRepair, including the special
// sync-mutation branch, so help and validation cannot drift from dispatch.
func TestCmdDoctorRepairClassificationMatrixAndDispatchInvariant(t *testing.T) {
	cfg := testConfig(t)
	repairable := diagnostic.RepairableCodes()
	registered := diagnostic.RegisteredCodes()
	repairableSet := make(map[string]bool, len(repairable))
	for _, code := range repairable {
		repairableSet[code] = true
	}
	diagnosticOnly := make([]string, 0, len(registered)-len(repairable))
	for _, code := range registered {
		if !repairableSet[code] {
			diagnosticOnly = append(diagnosticOnly, code)
		}
	}
	wantDiagnosticOnly := "diagnostic-only checks with no repair: " + strings.Join(diagnosticOnly, ", ")

	withArgs(t, "engram", "doctor", "--help")
	usage, usageErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if usageErr != "" {
		t.Fatalf("usage stderr=%q", usageErr)
	}
	if !strings.Contains(usage, "checks: "+strings.Join(registered, ", ")) {
		t.Fatalf("usage lost registered checks: %q", usage)
	}
	if !strings.Contains(usage, wantDiagnosticOnly) {
		t.Fatalf("usage diagnostic-only checks=%q want %q", usage, wantDiagnosticOnly)
	}

	withArgs(t, "engram", "doctor", "repair", "--help")
	repairUsage, repairUsageErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if repairUsageErr != "" {
		t.Fatalf("repair usage stderr=%q", repairUsageErr)
	}
	if !strings.Contains(repairUsage, "repairable checks: "+strings.Join(repairable, ", ")) {
		t.Fatalf("repair usage repairable checks=%q", repairUsage)
	}
	if !strings.Contains(repairUsage, wantDiagnosticOnly) {
		t.Fatalf("repair usage diagnostic-only checks=%q want %q", repairUsage, wantDiagnosticOnly)
	}

	for _, code := range repairable {
		args := []string{"engram", "doctor", "repair", "--check", code, "--plan"}
		if code != diagnostic.CheckSyncMutationRequiredFields {
			args = append(args, "--project", "engram")
		}
		withArgs(t, args...)
		stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
		if stderr != "" {
			t.Fatalf("repairable %q stderr=%q", code, stderr)
		}
		var plan map[string]any
		if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
			t.Fatalf("repairable %q did not return JSON: %v\n%s", code, err, stdout)
		}
		if _, ok := plan["actions"]; !ok {
			t.Fatalf("repairable %q did not return a plan: %v", code, plan)
		}
	}

	for _, code := range registered {
		if repairableSet[code] {
			continue
		}
		t.Run("diagnostic only "+code, func(t *testing.T) {
			oldExit := exitFunc
			exited := false
			exitFunc = func(code int) { exited = code != 0 }
			t.Cleanup(func() { exitFunc = oldExit })
			withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", code, "--plan")
			stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
			want := code + " is a diagnostic-only check with no repair"
			continuation := "engram doctor --check " + code
			if !exited || !strings.Contains(stderr, want) || !strings.Contains(stderr, continuation) || !strings.Contains(stdout, "usage: engram doctor") {
				t.Fatalf("code=%q exited=%v stdout=%q stderr=%q want=%q continuation=%q", code, exited, stdout, stderr, want, continuation)
			}
		})
	}

	t.Run("unknown", func(t *testing.T) {
		oldExit := exitFunc
		exited := false
		exitFunc = func(code int) { exited = code != 0 }
		t.Cleanup(func() { exitFunc = oldExit })
		withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "not_real", "--plan")
		stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
		if !exited || !strings.Contains(stderr, "unsupported repair check not_real") || strings.Contains(stderr, "engram doctor --check not_real") || !strings.Contains(stdout, "usage: engram doctor") {
			t.Fatalf("exited=%v stdout=%q stderr=%q", exited, stdout, stderr)
		}
	})
}

func TestCmdDoctorRepairManualSessionNamePlanDryRunApplyJSON(t *testing.T) {
	cfg := testConfig(t)
	missingDirectory := filepath.Join(t.TempDir(), "not-a-repository")
	seedDoctorRepairRows(t, cfg, "manual-save-engram", "sias-app", missingDirectory)
	seedDoctorSession(t, cfg, "known-engram", "engram", "/work/engram")

	withArgs(t, "engram", "doctor", "repair", "--project", "sias-app", "--check", "manual_session_name_project_mismatch", "--plan")
	planOut, planErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if planErr != "" {
		t.Fatalf("plan stderr=%q", planErr)
	}
	plan := decodeRepairPlan(t, planOut)
	if plan["status"] != "planned" || plan["mode"] != "plan" || len(plan["actions"].([]any)) != 1 {
		t.Fatalf("plan=%v", plan)
	}
	if action := plan["actions"].([]any)[0].(map[string]any); action["session_id"] != "manual-save-engram" || action["to_project"] != "engram" {
		t.Fatalf("plan action=%v", action)
	}
	counts := plan["counts"].(map[string]any)
	if counts["sessions_planned"] != float64(1) || counts["observations_planned"] != float64(1) || counts["prompts_planned"] != float64(1) {
		t.Fatalf("plan counts=%v", counts)
	}
	assertDoctorRepairProject(t, cfg, "manual-save-engram", "sias-app")

	withArgs(t, "engram", "doctor", "repair", "--project", "sias-app", "--check", "manual_session_name_project_mismatch", "--dry-run")
	dryOut, dryErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if dryErr != "" {
		t.Fatalf("dry-run stderr=%q", dryErr)
	}
	dry := decodeRepairPlan(t, dryOut)
	if dry["status"] != "dry_run" || dry["mode"] != "dry_run" {
		t.Fatalf("dry=%v", dry)
	}
	assertDoctorRepairProject(t, cfg, "manual-save-engram", "sias-app")

	withArgs(t, "engram", "doctor", "repair", "--project", "sias-app", "--check", "manual_session_name_project_mismatch", "--apply")
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
	assertDoctorRepairProject(t, cfg, "manual-save-engram", "engram")

	withArgs(t, "engram", "doctor", "repair", "--project", "sias-app", "--check", "manual_session_name_project_mismatch", "--plan")
	secondPlanOut, secondPlanErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if secondPlanErr != "" {
		t.Fatalf("second plan stderr=%q", secondPlanErr)
	}
	if secondPlan := decodeRepairPlan(t, secondPlanOut); secondPlan["status"] != "noop" || len(secondPlan["actions"].([]any)) != 0 {
		t.Fatalf("second plan=%v", secondPlan)
	}
}

func TestCmdDoctorRepairCleansForeignSyncTargetsWithoutDroppingJournal(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("valid"); err != nil {
		t.Fatalf("enroll valid project: %v", err)
	}
	if err := s.CreateSession("foreign-target-session", "valid", "/work/valid"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	observationID, err := s.AddObservation(store.AddObservationParams{SessionID: "foreign-target-session", Type: "decision", Title: "preserve", Content: "local data", Project: "valid", Scope: "project"})
	if err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES ('satellite:stale', 'idle', datetime('now'));
		INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project)
		VALUES ('satellite:stale', 'observation', 'foreign-journal', 'upsert', '{"sync_id":"foreign-journal","project":"valid"}', 'local', 'valid');`); err != nil {
		t.Fatalf("seed foreign target: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close seeded store: %v", err)
	}

	run := func(args ...string) map[string]any {
		t.Helper()
		withArgs(t, args...)
		stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
		if stderr != "" {
			t.Fatalf("doctor stderr=%q", stderr)
		}
		return decodeRepairPlan(t, stdout)
	}
	withArgs(t, "engram", "doctor", "--json", "--check", "sync_target_closed_space")
	beforeOut, beforeErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if beforeErr != "" || decodeDoctorReport(t, beforeOut)["status"] != "error" {
		t.Fatalf("before repair stderr=%q report=%s", beforeErr, beforeOut)
	}

	for _, mode := range []string{"--plan", "--dry-run"} {
		plan := run("engram", "doctor", "repair", "--project", "valid", "--check", "sync_target_closed_space", mode)
		if plan["status"] == "noop" || len(plan["target_actions"].([]any)) != 1 {
			t.Fatalf("%s plan=%v", mode, plan)
		}
	}
	applied := run("engram", "doctor", "repair", "--project", "valid", "--check", "sync_target_closed_space", "--apply")
	if applied["status"] != "applied" || len(applied["target_actions"].([]any)) != 1 {
		t.Fatalf("apply=%v", applied)
	}
	actual := applied["target_actions"].([]any)[0].(map[string]any)
	if actual["retargeted_mutations"] != float64(1) || actual["retained_mutations"] != float64(0) || actual["state_removed"] != true {
		t.Fatalf("apply action=%v", actual)
	}
	withArgs(t, "engram", "doctor", "--json", "--check", "sync_target_closed_space")
	afterOut, afterErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if afterErr != "" || decodeDoctorReport(t, afterOut)["status"] != "ok" {
		t.Fatalf("after repair stderr=%q report=%s", afterErr, afterOut)
	}

	reopened, err := store.New(cfg)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened store: %v", err)
		}
	})
	if _, err := reopened.GetObservation(observationID); err != nil {
		t.Fatalf("cleanup removed observation: %v", err)
	}
	var target, payload string
	if err := reopened.DB().QueryRow(`SELECT target_key, payload FROM sync_mutations WHERE entity_key = 'foreign-journal'`).Scan(&target, &payload); err != nil {
		t.Fatalf("read preserved journal: %v", err)
	}
	if target != store.DefaultSyncTargetKey || payload != `{"sync_id":"foreign-journal","project":"valid"}` {
		t.Fatalf("journal changed target=%q payload=%q", target, payload)
	}
}

func TestCmdDoctorRepairInvalidSessionIdentityLegacyJournal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enrolled bool
	}{
		{name: "enrolled", enrolled: true},
		{name: "local only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			initDoctorStore(t, cfg)
			db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if _, err := db.Exec(`INSERT INTO sessions(id,project,directory) VALUES ('','alpha','/work')`); err != nil {
				t.Fatal(err)
			}
			if tc.enrolled {
				if _, err := db.Exec(`INSERT INTO sync_enrolled_projects(project) VALUES ('alpha')`); err != nil {
					t.Fatal(err)
				}
			}
			insert := func(entity, key, payload string) {
				t.Helper()
				if _, err := db.Exec(`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud',?,?,'upsert',?,'local','alpha')`, entity, key, payload); err != nil {
					t.Fatal(err)
				}
			}
			insert("session", "", `{"id":"","project":"alpha","directory":"/work"}`)
			for i := 0; i < 7; i++ {
				key := fmt.Sprintf("obs-%d", i)
				if _, err := db.Exec(`INSERT INTO observations(sync_id,session_id,type,title,content,project) VALUES (?,'','note','title','body','alpha')`, key); err != nil {
					t.Fatal(err)
				}
				insert("observation", key, fmt.Sprintf(`{"sync_id":%q,"session_id":"","project":"alpha","scope":"project","type":"note","title":"title","content":"body"}`, key))
			}
			for i := 0; i < 4; i++ {
				key := fmt.Sprintf("prompt-%d", i)
				if _, err := db.Exec(`INSERT INTO user_prompts(sync_id,session_id,content,project) VALUES (?,'','hello','alpha')`, key); err != nil {
					t.Fatal(err)
				}
				insert("prompt", key, fmt.Sprintf(`{"sync_id":%q,"session_id":"","project":"alpha","content":"hello"}`, key))
			}
			run := func(mode string) map[string]any {
				t.Helper()
				withArgs(t, "engram", "doctor", "repair", "--project", "alpha", "--check", "invalid_session_identity", "--replacement-id", "canonical", mode)
				out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
				if stderr != "" {
					t.Fatal(stderr)
				}
				return decodeRepairPlan(t, out)
			}
			wantPublished := 0
			if tc.enrolled {
				wantPublished = 12
			}
			for _, mode := range []string{"--plan", "--dry-run"} {
				plan := run(mode)
				counts := plan["counts"].(map[string]any)
				if counts["corrected_mutations_planned"] != float64(wantPublished) || counts["corrected_mutations_applied"] != float64(0) {
					t.Fatalf("%s counts: %v", mode, counts)
				}
				identity := plan["identity_repair"].(map[string]any)
				if identity["retired_mutations"] != float64(12) || identity["observations"] != float64(7) || identity["prompts"] != float64(4) || identity["enrolled"] != tc.enrolled {
					t.Fatalf("%s: %v", mode, plan)
				}
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE id=''`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("plan mutated source: %d %v", count, err)
				}
				if err := db.QueryRow(`SELECT count(*) FROM sync_mutations WHERE disposition='pending' AND disposition_reason IS NULL`).Scan(&count); err != nil || count != 12 {
					t.Fatalf("%s mutated journal: %d %v", mode, count, err)
				}
				if err := db.QueryRow(`SELECT count(*) FROM observations WHERE session_id='' AND project='alpha'`).Scan(&count); err != nil || count != 7 {
					t.Fatalf("%s migrated observations: got=%d want=7 err=%v", mode, count, err)
				}
				if err := db.QueryRow(`SELECT count(*) FROM user_prompts WHERE session_id='' AND project='alpha'`).Scan(&count); err != nil || count != 4 {
					t.Fatalf("%s migrated prompts: got=%d want=4 err=%v", mode, count, err)
				}
			}
			applied := run("--apply")
			if applied["status"] != "applied" {
				t.Fatal(applied)
			}
			counts := applied["counts"].(map[string]any)
			if counts["corrected_mutations_planned"] != float64(wantPublished) || counts["corrected_mutations_applied"] != float64(wantPublished) {
				t.Fatalf("apply counts: %v", counts)
			}
			var retired, published, children int
			for _, q := range []struct {
				query string
				dest  *int
			}{{`SELECT count(*) FROM sync_mutations WHERE disposition_reason='session_identity_migrated'`, &retired}, {`SELECT count(*) FROM sync_mutations WHERE disposition='pending' AND project='alpha'`, &published}, {`SELECT count(*) FROM observations WHERE session_id='canonical'`, &children}} {
				if err := db.QueryRow(q.query).Scan(q.dest); err != nil {
					t.Fatal(err)
				}
			}
			if retired != 12 || published != wantPublished || children != 7 {
				t.Fatalf("retired=%d published=%d observations=%d", retired, published, children)
			}
			if !tc.enrolled {
				for label, check := range map[string]struct {
					query string
					want  int
				}{
					"source":             {`SELECT count(*) FROM sessions WHERE id='canonical' AND project='alpha' AND directory='/work'`, 1},
					"prompts":            {`SELECT count(*) FROM user_prompts WHERE session_id='canonical' AND content='hello' AND project='alpha'`, 4},
					"observations":       {`SELECT count(*) FROM observations WHERE session_id='canonical' AND title='title' AND content='body' AND project='alpha'`, 7},
					"historical journal": {`SELECT count(*) FROM sync_mutations WHERE disposition_reason='session_identity_migrated' AND disposition='superseded' AND source='local' AND project='alpha' AND json_extract(payload,'$.session_id')=''`, 11},
				} {
					var got int
					if err := db.QueryRow(check.query).Scan(&got); err != nil || got != check.want {
						t.Fatalf("%s: got=%d want=%d err=%v", label, got, check.want, err)
					}
				}
				var oldSession int
				if err := db.QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity='session' AND entity_key='' AND disposition='superseded' AND payload='{"id":"","project":"alpha","directory":"/work"}'`).Scan(&oldSession); err != nil || oldSession != 1 {
					t.Fatalf("historical session: got=%d err=%v", oldSession, err)
				}
			}
		})
	}
}

func TestCmdDoctorRepairInvalidSessionIdentityBlockers(t *testing.T) {
	tests := []struct {
		name                  string
		sources               []string
		replacement, selector string
		selectSource          bool
		want                  string
	}{
		{name: "ambiguous", sources: []string{"", " "}, replacement: "canonical", want: "ambiguous_or_missing_source"},
		{name: "exact empty source", sources: []string{"", " "}, replacement: "canonical", selector: "", selectSource: true, want: "planned"},
		{name: "collision", sources: []string{"", "canonical"}, replacement: "canonical", want: "identity_repair_blocked"},
		{name: "invalid replacement", sources: []string{""}, replacement: "  ", want: "identity_repair_blocked"},
		{name: "missing source", sources: []string{" "}, replacement: "canonical", selector: "", selectSource: true, want: "ambiguous_or_missing_source"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			initDoctorStore(t, cfg)
			db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range tc.sources {
				if _, err := db.Exec(`INSERT INTO sessions(id,project,directory) VALUES (?,'alpha','/work')`, id); err != nil {
					t.Fatal(err)
				}
			}
			_ = db.Close()
			args := []string{"engram", "doctor", "repair", "--project", "alpha", "--check", "invalid_session_identity", "--replacement-id", tc.replacement, "--plan"}
			if tc.selectSource {
				args = append(args, "--source-id", tc.selector)
			}
			withArgs(t, args...)
			out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
			if stderr != "" {
				t.Fatal(stderr)
			}
			plan := decodeRepairPlan(t, out)
			if tc.want == "planned" {
				if plan["status"] != "planned" || plan["identity_repair"].(map[string]any)["source_id"] != "" {
					t.Fatal(plan)
				}
			} else if plan["status"] != "blocked" || plan["blockers"].([]any)[0].(map[string]any)["reason_code"] != tc.want {
				t.Fatal(plan)
			}
			if tc.name == "collision" {
				counts := plan["counts"].(map[string]any)
				for _, field := range []string{"corrected_mutations_planned", "corrected_mutations_applied"} {
					if value, present := counts[field]; !present || value != float64(0) {
						t.Fatalf("blocked %s: value=%v present=%v", field, value, present)
					}
				}
			}
		})
	}
}

func TestCmdDoctorRepairInvalidSessionIdentityPreservesUnrepairedFindings(t *testing.T) {
	cfg := testConfig(t)
	initDoctorStore(t, cfg)
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(id,project,directory) VALUES ('','alpha','/work'),(' ','alpha','/other')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	for _, mode := range []string{"--plan", "--apply"} {
		withArgs(t, "engram", "doctor", "repair", "--project", "alpha", "--check", "invalid_session_identity", "--source-id", "", "--replacement-id", "canonical", mode)
		out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
		if stderr != "" {
			t.Fatal(stderr)
		}
		plan := decodeRepairPlan(t, out)
		if mode == "--apply" {
			if plan["status"] != "partial" {
				t.Fatalf("apply=%v", plan)
			}
		} else if plan["status"] != "planned" {
			t.Fatalf("plan=%v", plan)
		}
		skipped, ok := plan["skipped"].([]any)
		if !ok || len(skipped) != 1 || skipped[0].(map[string]any)["session_id"] != " " {
			t.Fatalf("unrepaired source lost: %v", plan)
		}
	}
	withArgs(t, "engram", "doctor", "--json", "--project", "alpha", "--check", "invalid_session_identity")
	out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" || decodeDoctorReport(t, out)["status"] != "blocked" {
		t.Fatalf("doctor=%s stderr=%s", out, stderr)
	}
}

func TestCmdDoctorRepairInvalidSessionIdentityRejectsIrrelevantFlags(t *testing.T) {
	for _, flag := range []string{"--replacement-id", "--source-id"} {
		t.Run(flag, func(t *testing.T) {
			cfg := testConfig(t)
			old := exitFunc
			exited := false
			exitFunc = func(int) { exited = true }
			t.Cleanup(func() { exitFunc = old })
			withArgs(t, "engram", "doctor", "repair", "--project", "alpha", "--check", "orphaned_observation_session", "--plan", flag, "value")
			_, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
			if !exited || !strings.Contains(stderr, "identity flags require") {
				t.Fatalf("stderr=%q exited=%v", stderr, exited)
			}
		})
	}
}

func TestCmdDoctorRepairInvalidSessionIdentityPlanApply(t *testing.T) {
	cfg := testConfig(t)
	initDoctorStore(t, cfg)
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO sessions(id,project,directory) VALUES ('','engram','/tmp/engram'); INSERT INTO observations(sync_id,session_id,type,title,content,project,scope,normalized_hash,revision_count,duplicate_count,created_at,updated_at) VALUES ('legacy','','bugfix','title','content','engram','project','legacy',1,1,datetime('now'),datetime('now'));`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	for _, mode := range []string{"--plan", "--dry-run"} {
		withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "invalid_session_identity", "--replacement-id", "canonical-1", mode)
		out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
		if stderr != "" {
			t.Fatal(stderr)
		}
		plan := decodeRepairPlan(t, out)
		identity, ok := plan["identity_repair"].(map[string]any)
		if !ok || identity["source_id"] != "" || identity["replacement_id"] != "canonical-1" || identity["observations"] != float64(1) {
			t.Fatalf("plan=%v", plan)
		}
	}
	withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "invalid_session_identity", "--replacement-id", "canonical-1", "--apply")
	out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatal(stderr)
	}
	if plan := decodeRepairPlan(t, out); plan["status"] != "applied" || plan["backup_path"] == nil {
		t.Fatalf("apply=%v", plan)
	}
	withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "invalid_session_identity")
	out, stderr = captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" || decodeDoctorReport(t, out)["status"] != "ok" {
		t.Fatalf("doctor=%s stderr=%s", out, stderr)
	}
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
			counts := plan["counts"].(map[string]any)
			for _, field := range []string{"corrected_mutations_planned", "corrected_mutations_applied"} {
				if value, present := counts[field]; !present || value != float64(0) {
					t.Fatalf("noop %s: value=%v present=%v", field, value, present)
				}
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
	if len(checks) != 1 {
		t.Fatalf("checks=%v", checks)
	}
	check := checks[0].(map[string]any)
	if check["check_id"] != "session_project_directory_mismatch" || check["reason_code"] != "session_project_directory_mismatch" {
		t.Fatalf("check=%v", check)
	}
	findings := check["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("findings=%v", findings)
	}
	finding := findings[0].(map[string]any)
	if finding["reason_code"] != "session_project_directory_mismatch" || finding["evidence"].(map[string]any)["directory_project"] != "other" {
		t.Fatalf("finding=%v", finding)
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

// TestCmdDoctorOrphanedObservationSessionRepairCreatesLocalEndedPlaceholder
// proves the full plan/dry-run/apply flow: apply creates an immediately ended,
// project-owned placeholder with no directory and no session sync mutation,
// preserves the observation, and reports a noop with zero applied sessions on
// the idempotent rerun.
func TestCmdDoctorOrphanedObservationSessionRepairCreatesLocalEndedPlaceholder(t *testing.T) {
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
		VALUES ('obs-orphan', 'missing-session', 'bugfix', 'orphan', 'content', 'engram', 'project', 'obs-orphan', 1, 1, '2026-01-01 00:00:00', '2026-01-01 00:00:00');
	`); err != nil {
		_ = db.Close()
		t.Fatalf("seed orphaned observation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}

	runRepair := func(mode string) map[string]any {
		t.Helper()
		withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "orphaned_observation_session", mode)
		stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
		if stderr != "" {
			t.Fatalf("%s stderr=%q", mode, stderr)
		}
		return decodeRepairPlan(t, stdout)
	}
	for _, mode := range []string{"--plan", "--dry-run"} {
		plan := runRepair(mode)
		if len(plan["placeholder_sessions"].([]any)) != 1 || plan["counts"].(map[string]any)["sessions_planned"] != float64(1) || plan["counts"].(map[string]any)["observations_planned"] != float64(1) {
			t.Fatalf("%s plan=%v", mode, plan)
		}
	}
	applied := runRepair("--apply")
	if applied["status"] != "applied" || applied["counts"].(map[string]any)["sessions_applied"] != float64(1) || applied["counts"].(map[string]any)["observations_applied"] != float64(1) {
		t.Fatalf("apply=%v", applied)
	}
	db, err = sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()
	var project, ownership, directory, startedAt, endedAt string
	if err := db.QueryRow(`SELECT project, ownership_mode, directory, started_at, ended_at FROM sessions WHERE id = 'missing-session'`).Scan(&project, &ownership, &directory, &startedAt, &endedAt); err != nil {
		t.Fatalf("read placeholder: %v", err)
	}
	if project != "engram" || ownership != store.SessionOwnershipProjectOwned || directory != "" || startedAt != "2026-01-01 00:00:00" || endedAt != startedAt {
		t.Fatalf("placeholder project=%q ownership=%q directory=%q started=%q ended=%q", project, ownership, directory, startedAt, endedAt)
	}
	var observations, mutations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM observations WHERE sync_id = 'obs-orphan'`).Scan(&observations); err != nil || observations != 1 {
		t.Fatalf("observations=%d err=%v", observations, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = 'session' AND entity_key = 'missing-session'`).Scan(&mutations); err != nil || mutations != 0 {
		t.Fatalf("session mutations=%d err=%v", mutations, err)
	}
	if rerun := runRepair("--apply"); rerun["status"] != "noop" || rerun["counts"].(map[string]any)["sessions_applied"] != float64(0) {
		t.Fatalf("rerun=%v", rerun)
	}
}

// TestCmdDoctorOrphanedObservationSessionApplyWithoutInsertionsReportsNoop
// proves the reporting boundary uses the store's actual insert result when a
// session appears after diagnostics planned an orphan placeholder.
func TestCmdDoctorOrphanedObservationSessionApplyWithoutInsertionsReportsNoop(t *testing.T) {
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
		VALUES ('obs-orphan', 'existing-session', 'bugfix', 'orphan', 'content', 'engram', 'project', 'obs-orphan', 1, 1, '2026-01-01 00:00:00', '2026-01-01 00:00:00');
	`); err != nil {
		_ = db.Close()
		t.Fatalf("seed orphaned observation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}

	oldBuildRepairPlan := buildRepairPlan
	buildRepairPlan = func(ctx context.Context, scope diagnostic.Scope, report diagnostic.Report, check string, mode diagnostic.RepairMode) (diagnostic.RepairPlan, error) {
		plan, err := diagnostic.BuildRepairPlan(ctx, scope, report, check, mode)
		if err != nil {
			return plan, err
		}
		if err := scope.Store.CreateSession("existing-session", "engram", "/work/engram"); err != nil {
			return diagnostic.RepairPlan{}, err
		}
		return plan, nil
	}
	t.Cleanup(func() { buildRepairPlan = oldBuildRepairPlan })

	withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "orphaned_observation_session", "--apply")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	plan := decodeRepairPlan(t, stdout)
	if len(plan["placeholder_sessions"].([]any)) != 1 || plan["status"] != "noop" {
		t.Fatalf("apply=%v", plan)
	}
	counts := plan["counts"].(map[string]any)
	if counts["sessions_planned"] != float64(1) || counts["observations_planned"] != float64(1) || counts["sessions_applied"] != float64(0) || counts["observations_applied"] != float64(0) {
		t.Fatalf("apply counts=%v", counts)
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

func TestCmdDoctorJSONMatchesMemDoctorEnvelopeForAmbiguousRuntimeSessions(t *testing.T) {
	cfg := testConfig(t)
	seedDoctorSession(t, cfg, "runtime-a", "engram", "/work/engram")
	seedDoctorSession(t, cfg, "runtime-b", "engram", "/work/engram")

	withArgs(t, "engram", "doctor", "--json", "--project", "engram", "--check", "ambiguous_active_runtime_sessions")
	cliStdout, cliStderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if cliStderr != "" {
		t.Fatalf("cli stderr=%q", cliStderr)
	}
	var cliEnvelope map[string]any
	if err := json.Unmarshal([]byte(cliStdout), &cliEnvelope); err != nil {
		t.Fatalf("cli json invalid: %v\n%s", err, cliStdout)
	}
	if cliEnvelope["status"] != "warning" {
		t.Fatalf("cli envelope=%v, want ambiguous-session warning", cliEnvelope)
	}

	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()
	mcpRes, err := engrammcp.DoctorToolHandler(s)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"project": "engram",
		"check":   "ambiguous_active_runtime_sessions",
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

func TestCmdDoctorRepairDefaultsSourceObservationRepairToDryRun(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.CreateSession("source-observation", "engram", "/work/engram"); err != nil {
		s.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	id, err := s.AddObservation(store.AddObservationParams{SessionID: "source-observation", Type: "decision", Title: "valid", Content: "Recovered source title. Details.\nA later line.", Project: "engram", Scope: "project"})
	if err != nil {
		s.Close()
		t.Fatalf("AddObservation: %v", err)
	}
	observation, err := s.GetObservation(id)
	if err != nil {
		s.Close()
		t.Fatalf("GetObservation: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE observations SET title = '' WHERE id = ?`, id); err != nil {
		s.Close()
		t.Fatalf("blank source title: %v", err)
	}
	if _, err := s.DB().Exec(`DELETE FROM sync_mutations WHERE entity = ? AND entity_key = ?`, store.SyncEntityObservation, observation.SyncID); err != nil {
		s.Close()
		t.Fatalf("remove pending mutation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "sync_mutation_required_fields")
	dryOut, dryErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if dryErr != "" {
		t.Fatalf("dry-run stderr=%q", dryErr)
	}
	dry := decodeRepairPlan(t, dryOut)
	if dry["applied"] != false {
		t.Fatalf("dry-run applied=%v, want false", dry["applied"])
	}
	sourceRepairs := dry["source_repairs"].([]any)
	if len(sourceRepairs) != 1 || sourceRepairs[0].(map[string]any)["id"] != float64(id) || sourceRepairs[0].(map[string]any)["title"] != "Recovered source title." {
		t.Fatalf("dry-run=%v", dry)
	}
	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New after dry-run: %v", err)
	}
	observation, err = s.GetObservation(id)
	if err != nil {
		s.Close()
		t.Fatalf("GetObservation after dry-run: %v", err)
	}
	if observation.Title != "" {
		s.Close()
		t.Fatalf("title after dry-run=%q, want empty", observation.Title)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close store after dry-run: %v", err)
	}

	withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "sync_mutation_required_fields", "--apply")
	applyOut, applyErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if applyErr != "" {
		t.Fatalf("apply stderr=%q", applyErr)
	}
	applied := decodeRepairPlan(t, applyOut)
	if applied["applied"] != true || len(applied["source_repairs"].([]any)) != 1 || applied["source_repair_backup_path"] == "" {
		t.Fatalf("apply=%v", applied)
	}
	s, err = store.New(cfg)
	if err != nil {
		t.Fatalf("store.New after apply: %v", err)
	}
	defer s.Close()
	observation, err = s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation after apply: %v", err)
	}
	if observation.Title != "Recovered source title." {
		t.Fatalf("title after apply=%q", observation.Title)
	}
}

func TestCmdDoctorRepairApplyNoOpReportsNoAction(t *testing.T) {
	cfg := testConfig(t)
	withArgs(t, "engram", "doctor", "repair", "--check", "sync_mutation_required_fields", "--apply")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	report := decodeRepairPlan(t, stdout)
	if report["applied"] != false || len(report["actions"].([]any)) != 0 || len(report["repairs"].([]any)) != 0 || len(report["source_repairs"].([]any)) != 0 {
		t.Fatalf("no-op apply report=%v", report)
	}
	if _, ok := report["source_repair_backup_path"]; ok {
		t.Fatalf("no-op apply created source repair backup: %v", report)
	}
}

func TestCmdDoctorRepairQuarantinesInvalidEmptyProjectMutations(t *testing.T) {
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
	seedDoctorPendingMutation(t, cfg, "legacy", store.SyncEntityPrompt, "retired-prompt", store.SyncOpUpsert, `{"sync_id":"retired-prompt","session_id":"legacy-session","content":"obsolete","project":"legacy"}`)
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open legacy tombstone fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO prompt_tombstones (sync_id, session_id, project) VALUES (?, ?, ?)`, "retired-prompt", "legacy-session", "legacy"); err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy tombstone fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy tombstone fixture: %v", err)
	}

	withArgs(t, "engram", "doctor", "repair", "--check", "sync_mutation_required_fields", "--dry-run")
	dryOut, dryErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if dryErr != "" {
		t.Fatalf("dry-run stderr=%q", dryErr)
	}
	dry := decodeRepairPlan(t, dryOut)
	if dry["applied"] != false || len(dry["actions"].([]any)) != 2 || len(dry["superseded"].([]any)) != 1 {
		t.Fatalf("dry-run=%v", dry)
	}

	withArgs(t, "engram", "doctor", "repair", "--check", "sync_mutation_required_fields", "--apply")
	applyOut, applyErr := captureOutput(t, func() { cmdDoctor(cfg) })
	if applyErr != "" {
		t.Fatalf("apply stderr=%q", applyErr)
	}
	applied := decodeRepairPlan(t, applyOut)
	if applied["applied"] != true || len(applied["actions"].([]any)) != 2 || len(applied["superseded"].([]any)) != 1 {
		t.Fatalf("apply=%v", applied)
	}
	db, err = sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var poison, later, retired string
	if err := db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity_key = 'poison'`).Scan(&poison); err != nil {
		t.Fatalf("read poison: %v", err)
	}
	if err := db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity_key = 'later'`).Scan(&later); err != nil {
		t.Fatalf("read later: %v", err)
	}
	if err := db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity_key = 'retired-prompt'`).Scan(&retired); err != nil {
		t.Fatalf("read retired prompt: %v", err)
	}
	if poison != store.SyncMutationDispositionQuarantined || later != store.SyncMutationDispositionQuarantined || retired != "superseded" {
		t.Fatalf("dispositions poison=%q later=%q retired=%q", poison, later, retired)
	}
}

func TestCmdDoctorRepairSupersedesBeforeQuarantine(t *testing.T) {
	cfg := testConfig(t)
	initDoctorStore(t, cfg)
	const project, sessionID = "legacy", "retired-session"
	seedDoctorPendingMutation(t, cfg, project, store.SyncEntitySession, sessionID, store.SyncOpUpsert, `{}`)
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open tombstone fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sync_delete_tombstones (entity, entity_key, project, active) VALUES (?, ?, ?, 1)`, store.SyncEntitySession, sessionID, project); err != nil {
		_ = db.Close()
		t.Fatalf("seed tombstone: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close tombstone fixture: %v", err)
	}

	runRepair := func(mode string) map[string]any {
		t.Helper()
		withArgs(t, "engram", "doctor", "repair", "--project", project, "--check", "sync_mutation_required_fields", mode)
		stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
		if stderr != "" {
			t.Fatalf("%s stderr=%q", mode, stderr)
		}
		return decodeRepairPlan(t, stdout)
	}
	for _, mode := range []string{"--dry-run", "--apply"} {
		report := runRepair(mode)
		if len(report["actions"].([]any)) != 0 || len(report["superseded"].([]any)) != 1 {
			t.Fatalf("%s report=%v", mode, report)
		}
	}
	db, err = sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("reopen fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close fixture: %v", err)
		}
	})
	var disposition string
	if err := db.QueryRow(`SELECT disposition FROM sync_mutations WHERE entity_key = ?`, sessionID).Scan(&disposition); err != nil || disposition != store.SyncMutationDispositionSuperseded {
		t.Fatalf("disposition=%q err=%v", disposition, err)
	}
}

func TestCmdDoctorRepairRepairsTitleOnlyObservationMutation(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := s.EnrollProject("engram"); err != nil {
		if closeErr := s.Close(); closeErr != nil {
			t.Fatalf("close store after failed enrollment: %v", closeErr)
		}
		t.Fatalf("enroll project: %v", err)
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

func TestPrintDoctorUsageDocumentsOptionalRequiredFieldsProject(t *testing.T) {
	withArgs(t, "engram", "doctor", "--help")
	stdout, stderr := captureOutput(t, func() { cmdDoctor(testConfig(t)) })
	if stderr != "" {
		t.Fatalf("stderr=%q", stderr)
	}
	if !strings.Contains(stdout, "doctor repair [--project PROJECT] --check sync_mutation_required_fields") || !strings.Contains(stdout, "optionally scopes title repair, supersession, quarantine, and source-title repair") || !strings.Contains(stdout, "--project is required for every repair check except sync_mutation_required_fields") {
		t.Fatalf("usage=%q", stdout)
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
		"       engram doctor repair [--project PROJECT] --check sync_mutation_required_fields [--plan|--dry-run|--apply] (default: --dry-run)",
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
