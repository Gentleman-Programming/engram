package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func discardCLI(t *testing.T, cfg store.Config, args ...string) (string, int) {
	t.Helper()
	oldArgs, oldExit := os.Args, exitFunc
	os.Args = append([]string{"engram", "doctor", "discard-empty-prompt"}, args...)
	code := 0
	exitFunc = func(n int) { code = n }
	defer func() { os.Args = oldArgs; exitFunc = oldExit }()
	out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	return out, code
}

func discardCLIJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("not JSON: %s: %v", out, err)
	}
	return report
}

func TestDoctorDiscardEmptyPromptHelp(t *testing.T) {
	old := storeNew
	storeNew = func(store.Config) (*store.Store, error) {
		t.Fatal("help opened database")
		return nil, nil
	}
	t.Cleanup(func() { storeNew = old })
	out, code := discardCLI(t, store.Config{}, "--help")
	if code != 0 || !strings.Contains(out, "--seq SEQ") {
		t.Fatalf("help: %s code=%d", out, code)
	}
}

func TestDoctorDiscardEmptyPromptArguments(t *testing.T) {
	cases := [][]string{
		{}, {"--project", "project"}, {"--seq", "1"},
		{"--project", "--seq", "1"}, {"--project", "project", "--seq"},
		{"--project", "project", "--seq", "0"}, {"--project", "project", "--seq", "-1"},
		{"--project", "project", "--seq", "9223372036854775808"},
		{"--project", "project", "--seq", "x"},
		{"--project", "project", "--seq", "1", "--all"},
		{"--project", "project", "--seq", "1", "--apply"},
		{"--project", "project", "--seq", "1", "--backup", "unused.db"},
		{"--project", "project", "--seq", "1", "--apply", "--dry-run", "--backup", "unused.db"},
		{"--project", "project", "--project", "other", "--seq", "1"},
		{"--project", "project", "--seq", "1", "--dry-run", "--dry-run"},
		{"--project", "project", "--seq", "1", "--apply", "--backup"},
		{"--project", "project", "--seq", "1", "unexpected"},
		{"--project", "project", "--seq", "1.5"},
	}
	old := storeNew
	storeNew = func(store.Config) (*store.Store, error) {
		t.Fatal("invalid args opened database")
		return nil, nil
	}
	t.Cleanup(func() { storeNew = old })
	for i, args := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			out, code := discardCLI(t, store.Config{}, append(args, "--json")...)
			r := discardCLIJSON(t, out)
			if code != 1 || r["blocker"] == nil {
				t.Fatalf("code=%d report=%v", code, r)
			}
		})
	}
}

// Only this fixture bypasses admission to reproduce legacy blank content.
func discardCLIFixture(t *testing.T) (store.Config, *sql.DB, int64) {
	t.Helper()
	cfg := store.Config{DataDir: t.TempDir()}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateSession("session", "project", "/synthetic"); err != nil {
		t.Fatal(err)
	}
	if err = s.EnrollProject("project"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AddPrompt(store.AddPromptParams{
		SessionID: "session", Project: "project", Content: "Valid admission",
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, q := range []string{
		`UPDATE user_prompts SET content='  '`,
		`UPDATE sync_mutations SET payload=json_set(payload,'$.content','  ') WHERE entity='prompt'`,
		`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) SELECT target_key,entity,entity_key,op,payload,source,project FROM sync_mutations WHERE entity='prompt'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var seq int64
	if err := db.QueryRow(`SELECT min(seq) FROM sync_mutations WHERE entity='prompt'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return cfg, db, seq
}

func discardCLIState(t *testing.T, db *sql.DB) string {
	t.Helper()
	var state []any
	for _, table := range []string{
		"sync_mutations", "sync_state", "user_prompts", "prompts_fts",
		"prompt_tombstones", "sessions", "sync_enrolled_projects",
	} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range values {
				ptr[i] = &values[i]
			}
			if err := rows.Scan(ptr...); err != nil {
				t.Fatal(err)
			}
			state = append(state, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDoctorDiscardEmptyPromptPreview(t *testing.T) {
	cfg, db, seq := discardCLIFixture(t)
	before := discardCLIState(t, db)
	for _, mode := range [][]string{nil, {"--dry-run"}} {
		args := append([]string{"--project", "project", "--seq", fmt.Sprint(seq)}, mode...)
		out, code := discardCLI(t, cfg, append(args, "--json")...)
		r := discardCLIJSON(t, out)
		if code != 0 || r["status"] != "dry_run" || r["selected_seq"] != float64(seq) ||
			r["project"] != "project" || r["prompt_id"] != float64(1) ||
			r["session_id"] != "session" || r["sync_id"] == "" || r["source_inbox_id"] != "" ||
			len(r["sequences"].([]any)) != 2 || len(r["planned_effects"].([]any)) != 4 ||
			r["delete_queued"] != false || r["tombstone"] != false ||
			r["remote_confirmation"] != false || r["backup_path"] != "" {
			t.Fatalf("code=%d report=%v", code, r)
		}
		out, code = discardCLI(t, cfg, args...)
		if code != 0 || !strings.Contains(out, "selected_seq: "+fmt.Sprint(seq)) ||
			!strings.Contains(out, "NO REMOTE CONFIRMATION") || !strings.Contains(out, "sequences:") {
			t.Fatalf("text=%s code=%d", out, code)
		}
		if after := discardCLIState(t, db); after != before {
			t.Fatal("preview changed database")
		}
	}
	entries, err := os.ReadDir(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "backup") {
			t.Fatalf("preview backup: %s", entry.Name())
		}
	}
}

func TestDoctorDiscardEmptyPromptApply(t *testing.T) {
	cfg, db, seq := discardCLIFixture(t)
	var payload, occurred, source string
	if err := db.QueryRow(`SELECT payload,occurred_at,source FROM sync_mutations WHERE seq=?`, seq).
		Scan(&payload, &occurred, &source); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "unused.db")
	out, code := discardCLI(t, cfg, "--project", "project", "--seq", fmt.Sprint(seq),
		"--apply", "--backup", backup, "--json")
	r := discardCLIJSON(t, out)
	if code != 0 || r["status"] != "delete_queued" || r["backup_path"] != backup ||
		r["delete_seq"].(float64) <= float64(seq) || r["tombstone"] != true || r["remote_confirmation"] != false {
		t.Fatalf("code=%d report=%v", code, r)
	}
	var afterPayload, afterOccurred, afterSource, disposition string
	var ack sql.NullString
	if err := db.QueryRow(`SELECT payload,occurred_at,source,disposition,acked_at FROM sync_mutations WHERE seq=?`, seq).
		Scan(&afterPayload, &afterOccurred, &afterSource, &disposition, &ack); err != nil {
		t.Fatal(err)
	}
	if payload != afterPayload || occurred != afterOccurred || source != afterSource ||
		disposition != "superseded" || ack.Valid {
		t.Fatal("original journal was not preserved")
	}
	for _, check := range []struct {
		query string
		want  int
	}{
		{`SELECT count(*) FROM user_prompts`, 0},
		{`SELECT count(*) FROM prompts_fts`, 0},
		{`SELECT count(*) FROM prompt_tombstones`, 1},
		{`SELECT count(*) FROM sync_mutations WHERE entity='prompt' AND disposition='superseded'`, 2},
		{`SELECT count(*) FROM sync_mutations WHERE entity='prompt' AND op='delete' AND json_extract(payload,'$.deleted')=1 AND json_extract(payload,'$.hard_delete')=1`, 1},
	} {
		var n int
		if err := db.QueryRow(check.query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != check.want {
			t.Fatalf("%s: %d", check.query, n)
		}
	}
	backupDB, err := sql.Open("sqlite", backup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backupDB.Close() }()
	var integrity string
	var n int
	if err := backupDB.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("backup: %s %v", integrity, err)
	}
	if err := backupDB.QueryRow(`SELECT count(*) FROM user_prompts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("backup before apply: %d %v", n, err)
	}
	before := discardCLIState(t, db)
	repeatBackup := filepath.Join(t.TempDir(), "repeat.db")
	out, code = discardCLI(t, cfg, "--project", "project", "--seq", fmt.Sprint(seq),
		"--apply", "--backup", repeatBackup, "--json")
	r = discardCLIJSON(t, out)
	if code != 1 || r["blocker"].(map[string]any)["code"] != "ambiguous_canonical" {
		t.Fatalf("repeat: %s", out)
	}
	if discardCLIState(t, db) != before {
		t.Fatal("repeat mutated database")
	}
	if _, err := os.Stat(repeatBackup); !os.IsNotExist(err) {
		t.Fatalf("repeat created backup: %v", err)
	}
}

func TestDoctorDiscardEmptyPromptBlockers(t *testing.T) {
	for _, tc := range []struct{ name, query, code string }{
		{"unenrolled", `DELETE FROM sync_enrolled_projects`, "not_enrolled"},
		{"recoverable", `UPDATE user_prompts SET content='Recoverable'`, "recoverable_content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, db, seq := discardCLIFixture(t)
			if _, err := db.Exec(tc.query); err != nil {
				t.Fatal(err)
			}
			before := discardCLIState(t, db)
			out, code := discardCLI(t, cfg, "--project", "project", "--seq", fmt.Sprint(seq), "--json")
			r := discardCLIJSON(t, out)
			if code != 1 || r["blocker"].(map[string]any)["code"] != tc.code {
				t.Fatalf("%s code=%d", out, code)
			}
			if discardCLIState(t, db) != before {
				t.Fatal("blocked selection mutated database")
			}
		})
	}
}

func TestDoctorDiscardEmptyPromptDiagnosisSelection(t *testing.T) {
	cfg, _, seq := discardCLIFixture(t)
	withArgs(t, "engram", "doctor", "--project", "project", "--check", "sync_mutation_required_fields", "--json")
	out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
	if stderr != "" {
		t.Fatal(stderr)
	}
	r := discardCLIJSON(t, out)
	found := false
	for _, check := range r["checks"].([]any) {
		for _, finding := range check.(map[string]any)["findings"].([]any) {
			evidence := finding.(map[string]any)["evidence"].(map[string]any)
			if evidence["seq"] == float64(seq) && evidence["entity"] == "prompt" &&
				evidence["op"] == "upsert" && evidence["project"] == "project" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("documented sequence discovery missing: %s", out)
	}
}

func TestDoctorDiscardEmptyPromptOpenError(t *testing.T) {
	old := storeNew
	storeNew = func(store.Config) (*store.Store, error) { return nil, errors.New("synthetic open failure") }
	t.Cleanup(func() { storeNew = old })
	out, code := discardCLI(t, store.Config{}, "--project", "project", "--seq", "1", "--json")
	r := discardCLIJSON(t, out)
	if code != 1 || r["blocker"].(map[string]any)["detail"] != "synthetic open failure" {
		t.Fatalf("open error: %s code=%d", out, code)
	}
}

func TestDoctorDiscardEmptyPromptExactProject(t *testing.T) {
	cfg, db, seq := discardCLIFixture(t)
	if _, err := db.Exec(`INSERT INTO sync_enrolled_projects(project) VALUES ('other')`); err != nil {
		t.Fatal(err)
	}
	before := discardCLIState(t, db)
	out, code := discardCLI(t, cfg, "--project", "other", "--seq", fmt.Sprint(seq), "--json")
	r := discardCLIJSON(t, out)
	if code != 1 || r["project"] != "other" || r["blocker"].(map[string]any)["code"] != "conflicting_identity" {
		t.Fatalf("wrong project: %s code=%d", out, code)
	}
	if discardCLIState(t, db) != before {
		t.Fatal("wrong project changed database")
	}
}

func TestDoctorDiscardEmptyPromptBackupErrors(t *testing.T) {
	cfg, db, seq := discardCLIFixture(t)
	before := discardCLIState(t, db)
	collision := filepath.Join(t.TempDir(), "occupied.db")
	if err := os.WriteFile(collision, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{collision, filepath.Join(t.TempDir(), "missing", "backup.db")} {
		out, code := discardCLI(t, cfg, "--project", "project", "--seq", fmt.Sprint(seq),
			"--apply", "--backup", path, "--json")
		r := discardCLIJSON(t, out)
		if code != 1 || r["blocker"] == nil || r["delete_queued"] != false {
			t.Fatalf("%s code=%d", out, code)
		}
		if discardCLIState(t, db) != before {
			t.Fatal("backup error mutated database")
		}
	}
	b, err := os.ReadFile(collision)
	if err != nil || string(b) != "preserve" {
		t.Fatalf("collision overwritten: %s %v", b, err)
	}

	// Hold a synthetic writer lock: readers can plan under WAL, but backup/apply
	// cannot complete. Open first so Store initialization is outside the lock.
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := storeNew
	storeNew = func(store.Config) (*store.Store, error) { return s, nil }
	defer func() { storeNew = old }()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Best-effort teardown releases the synthetic lock without changing assertions.
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE user_prompts SET content=content`); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(t.TempDir(), "reserved.db")
	out, code := discardCLI(t, cfg, "--project", "project", "--seq", fmt.Sprint(seq),
		"--apply", "--backup", retained, "--json")
	r := discardCLIJSON(t, out)
	if code != 1 || r["backup_path"] != retained ||
		!strings.Contains(r["backup_path_meaning"].(string), "INTENDED") ||
		!strings.Contains(r["backup_path_meaning"].(string), "not proof") {
		t.Fatalf("retained path report: %s code=%d", out, code)
	}
	if _, err := os.Stat(retained); err != nil {
		t.Fatalf("reservation removed: %v", err)
	}
	if discardCLIState(t, db) != before {
		t.Fatal("failed publication mutated database")
	}
}
