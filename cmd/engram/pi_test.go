package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
)

// writeFile is a tiny helper kept local to the Pi bridge tests.
func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ─── parser ──────────────────────────────────────────────────────────────────

func TestParsePiImportArray(t *testing.T) {
	raw := `[{"title":"a","content":"aa"},{"title":"b","content":"bb","type":"decision"}]`
	recs, err := parsePiImport([]byte(raw))
	if err != nil {
		t.Fatalf("parsePiImport: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if recs[1].Type != "decision" {
		t.Fatalf("expected second type 'decision', got %q", recs[1].Type)
	}
}

func TestParsePiImportEnvelope(t *testing.T) {
	raw := `{"memories":[{"title":"env","content":"body","project":"p","scope":"project"}]}`
	recs, err := parsePiImport([]byte(raw))
	if err != nil {
		t.Fatalf("parsePiImport: %v", err)
	}
	if len(recs) != 1 || recs[0].Title != "env" {
		t.Fatalf("unexpected records: %+v", recs)
	}
}

func TestParsePiImportSingleObject(t *testing.T) {
	raw := `{"title":"only","content":"one"}`
	recs, err := parsePiImport([]byte(raw))
	if err != nil {
		t.Fatalf("parsePiImport: %v", err)
	}
	if len(recs) != 1 || recs[0].Title != "only" {
		t.Fatalf("unexpected records: %+v", recs)
	}
}

func TestParsePiImportRejectsBadShape(t *testing.T) {
	if _, err := parsePiImport([]byte(`{"unrelated":true}`)); err == nil {
		t.Fatal("expected error for unrecognized shape")
	}
}

// ─── redaction ───────────────────────────────────────────────────────────────

func TestRedactSecretPatterns(t *testing.T) {
	// Cover the full secret+keys pipeline (the combination the bridge itself uses
	// in import flows): high-entropy tokens via --redact-secrets, key/value creds
	// like `token=...` via --redact-keys.
	rd := newPiRedactor(true, true, false)
	out := rd.redactString("key sk_live_AB12CD34EF56 token=supersecret and ghp_" + strings.Repeat("a", 30))
	for _, needle := range []string{"sk_live", "supersecret", "ghp_"} {
		if strings.Contains(out, needle) {
			t.Fatalf("secret %q leaked: %s", needle, out)
		}
	}
	if !strings.Contains(out, "[REDACTED") {
		t.Fatalf("expected redaction marker, got %q", out)
	}
}

func TestRedactKeyPatterns(t *testing.T) {
	rd := newPiRedactor(false, true, false)
	in := "api_key: mykey123\npassword=letmein\n  secret: \"topsecret\"\nBearer xyz456"
	out := rd.redactString(in)
	for _, leak := range []string{"mykey123", "letmein", "topsecret", "xyz456"} {
		if strings.Contains(out, leak) {
			t.Fatalf("key value %q leaked: %s", leak, out)
		}
	}
	// Labels and structural punctuation must survive so the content stays readable.
	for _, keep := range []string{"api_key", "password", "secret", "Bearer"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("expected label %q preserved, got %q", keep, out)
		}
	}
}

func TestRedactLogLines(t *testing.T) {
	rd := newPiRedactor(false, false, true)
	in := "2026-06-28T12:00:00Z INFO starting up\n[ERROR] boom\nkeep me\n[module] DEBUG trace\nalso keep"
	out := rd.redactString(in)
	if strings.Contains(out, "starting up") || strings.Contains(out, "boom") || strings.Contains(out, "trace") {
		t.Fatalf("log lines not stripped: %q", out)
	}
	if !strings.Contains(out, "keep me") || !strings.Contains(out, "also keep") {
		t.Fatalf("non-log lines were removed: %q", out)
	}
}

// ─── end-to-end via cmdPi* ────────────────────────────────────────────────────

// TestCmdPiImportRoundTrip exercises: parse -> redact -> dedup skip -> export.
func TestCmdPiImportRoundTrip(t *testing.T) {
	cfg := testConfig(t)
	inFile := filepath.Join(t.TempDir(), "pi-in.json")
	payload := `{"memories":[{"title":"Stripe leak","content":"live key sk_live_AB12CD34EF56 at ?token=supersecret","type":"bugfix","project":"pi-e2e","scope":"project"}]}`
	writeFile(t, inFile, payload)

	// First import: real insert with redaction on.
	withArgs(t, "engram", "pi", "import", inFile, "--source", "pi", "--redact-secrets", "--redact-keys")
	stdout, stderr := captureOutput(t, func() { cmdPiImport(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "imported #1") {
		t.Fatalf("expected one import, got: %s", stdout)
	}

	// Verify the persisted content was redacted.
	s, err := storeNew(cfg)
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()
	results, err := storeSearch(s, "Stripe leak", store.SearchOptions{Type: "bugfix", Project: "pi-e2e", Limit: 10})
	if err != nil || len(results) != 1 {
		t.Fatalf("expected 1 redacted result, got %d (err=%v)", len(results), err)
	}
	if strings.Contains(results[0].Content, "sk_live_AB12CD34EF56") || strings.Contains(results[0].Content, "supersecret") {
		t.Fatalf("redaction failed, content: %q", results[0].Content)
	}

	// Second import of the same payload: dedup must skip it.
	withArgs(t, "engram", "pi", "import", inFile, "--source", "pi")
	stdout2, _ := captureOutput(t, func() { cmdPiImport(cfg) })
	if !strings.Contains(stdout2, "skipped(dedup) 1") || !strings.Contains(stdout2, "imported 0") {
		t.Fatalf("expected dedup skip, got: %s", stdout2)
	}

	// Third import with --no-dedup: inserts again.
	withArgs(t, "engram", "pi", "import", inFile, "--source", "pi", "--no-dedup")
	stdout3, _ := captureOutput(t, func() { cmdPiImport(cfg) })
	if !strings.Contains(stdout3, "imported 1") || !strings.Contains(stdout3, "skipped(dedup) 0") {
		t.Fatalf("expected re-import with --no-dedup, got: %s", stdout3)
	}
}

// TestCmdPiDryRunInsertsNothing confirms --dry-run never persists.
func TestCmdPiDryRunInsertsNothing(t *testing.T) {
	cfg := testConfig(t)
	inFile := filepath.Join(t.TempDir(), "pi-in.json")
	writeFile(t, inFile, `{"memories":[{"title":"Dry","content":"x","type":"discovery","project":"pi-dry"}]}`)

	withArgs(t, "engram", "pi", "import", inFile, "--dry-run", "--source", "pi")
	stdout, stderr := captureOutput(t, func() { cmdPiImport(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "would import 1") {
		t.Fatalf("expected dry-run message, got: %s", stdout)
	}

	s, err := storeNew(cfg)
	if err != nil {
		t.Fatalf("storeNew: %v", err)
	}
	defer s.Close()
	results, err := storeSearch(s, "Dry", store.SearchOptions{Type: "discovery", Project: "pi-dry", Limit: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("dry-run must not persist, found %d", len(results))
	}
}

// TestCmdPiExportAllAndProject checks the envelope shape and --project scoping.
func TestCmdPiExportAllAndProject(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "s1", "proj-a", "decision", "A-title", "A-content", "project")
	mustSeedObservation(t, cfg, "s2", "proj-b", "decision", "B-title", "B-content", "project")

	// Export ALL.
	withArgs(t, "engram", "pi", "export", "--format", "json")
	stdout, stderr := captureOutput(t, func() { cmdPiExport(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "A-title") || !strings.Contains(stdout, "B-title") {
		t.Fatalf("expected both observations in export: %s", stdout)
	}

	var env piExportEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &env); err != nil {
		t.Fatalf("export is not valid JSON: %v\n%s", err, stdout)
	}
	if env.Source != "engram" || env.Count != 2 || len(env.Observations) != 2 {
		t.Fatalf("bad envelope: %+v", env)
	}

	// Export scoped to a single project.
	withArgs(t, "engram", "pi", "export", "--format", "json", "--project", "proj-a")
	stdout2, _ := captureOutput(t, func() { cmdPiExport(cfg) })
	var env2 piExportEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout2)), &env2); err != nil {
		t.Fatalf("project export is not valid JSON: %v\n%s", err, stdout2)
	}
	if env2.Count != 1 || env2.Project != "proj-a" {
		t.Fatalf("expected single proj-a observation, got %+v", env2)
	}
	if len(env2.Observations) != 1 || env2.Observations[0].Title != "A-title" {
		t.Fatalf("unexpected project-scoped export: %+v", env2.Observations)
	}
}

// TestCmdPiSearchJSON verifies machine-readable output with metadata.
func TestCmdPiSearchJSON(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "s1", "proj-search", "decision", "Use transactions for writes", "always wrap writes", "project")

	withArgs(t, "engram", "pi", "search", "transactions", "--project", "proj-search", "--limit", "5")
	stdout, stderr := captureOutput(t, func() { cmdPiSearch(cfg) })
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "\"query\":") || !strings.Contains(stdout, "\"results\":") {
		t.Fatalf("expected JSON envelope, got: %s", stdout)
	}
	var env struct {
		Query   string `json:"query"`
		Count   int    `json:"count"`
		Results []struct {
			ID    int64   `json:"id"`
			Title string  `json:"title"`
			Type  string  `json:"type"`
			Rank  float64 `json:"rank"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &env); err != nil {
		t.Fatalf("search output not valid JSON: %v\n%s", err, stdout)
	}
	if env.Count < 1 || len(env.Results) < 1 {
		t.Fatalf("expected >=1 hit, got %d", env.Count)
	}
	if env.Results[0].Title != "Use transactions for writes" {
		t.Fatalf("unexpected title: %q", env.Results[0].Title)
	}
	if env.Results[0].ID <= 0 || env.Results[0].Rank == 0 {
		t.Fatalf("expected populated metadata, got id=%d rank=%f", env.Results[0].ID, env.Results[0].Rank)
	}
}

// TestCmdPiDispatchAndUsage covers the dispatcher routing + unknown subcommand.
func TestCmdPiDispatchAndUsage(t *testing.T) {
	cfg := testConfig(t)

	// No subcommand -> usage + exit 1.
	exited := false
	oldExit := exitFunc
	exitFunc = func(code int) { exited = true }
	t.Cleanup(func() { exitFunc = oldExit })

	withArgs(t, "engram", "pi")
	_, stderr := captureOutput(t, func() { cmdPi(cfg) })
	if !exited {
		t.Fatalf("expected exitFunc(1) when no subcommand given")
	}
	if !strings.Contains(stderr, "usage: engram pi") {
		t.Fatalf("expected usage, got: %s", stderr)
	}

	// Unknown subcommand -> usage + exit.
	exited = false
	withArgs(t, "engram", "pi", "frobnicate")
	_, stderr = captureOutput(t, func() { cmdPi(cfg) })
	if !exited || !strings.Contains(stderr, "unknown pi subcommand: frobnicate") {
		t.Fatalf("expected unknown-subcommand error, got exited=%v stderr=%s", exited, stderr)
	}
}

// (searchOpts helper removed: tests use store.SearchOptions directly.)
