package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// ─── session end parser (issue #1247, #1084 guarantee) ───────────────────────

func TestParseSessionEndArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    sessionEndArgs
		wantErr string
	}{
		{
			name: "single session id",
			args: []string{"sess-1"},
			want: sessionEndArgs{sessionID: "sess-1"},
		},
		{
			name: "single session id with summary",
			args: []string{"sess-1", "--summary", "done"},
			want: sessionEndArgs{sessionID: "sess-1", summary: "done", hasSummary: true},
		},
		{
			name: "bulk with age filter only",
			args: []string{"--by-age", "72h"},
			want: sessionEndArgs{olderThan: 72 * time.Hour, hasByAge: true},
		},
		{
			name:    "bulk with project filter only requires an explicit window",
			args:    []string{"--project", "web"},
			wantErr: "requires --by-age",
		},
		{
			name: "bulk with project narrowed by an explicit window",
			args: []string{"--project", "web", "--by-age", "30d"},
			want: sessionEndArgs{olderThan: 30 * 24 * time.Hour, hasByAge: true, project: "web"},
		},
		{
			name: "bulk with every flag",
			args: []string{"--by-age", "30d", "--project", "web", "--apply", "--json"},
			want: sessionEndArgs{olderThan: 30 * 24 * time.Hour, hasByAge: true, project: "web", apply: true, jsonOut: true},
		},
		{
			name:    "no id and no filter",
			args:    []string{},
			wantErr: "session ID",
		},
		{
			name:    "id and filter are mutually exclusive",
			args:    []string{"sess-1", "--by-age", "72h"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "id and project are mutually exclusive",
			args:    []string{"sess-1", "--project", "web"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "--apply with id",
			args:    []string{"sess-1", "--apply"},
			wantErr: "only valid",
		},
		{
			name: "single session id with json",
			args: []string{"sess-1", "--json"},
			want: sessionEndArgs{sessionID: "sess-1", jsonOut: true},
		},
		{
			name:    "--summary with bulk filters",
			args:    []string{"--by-age", "72h", "--summary", "done"},
			wantErr: "only valid",
		},
		{
			name:    "--apply without filters",
			args:    []string{"--apply"},
			wantErr: "session ID",
		},
		{
			name:    "--json without filters",
			args:    []string{"--json"},
			wantErr: "session ID",
		},
		{
			name:    "unknown flag",
			args:    []string{"sess-1", "--sumary", "x"},
			wantErr: "unknown flag",
		},
		{
			name:    "extra positional argument",
			args:    []string{"sess-1", "sess-2"},
			wantErr: "unexpected extra argument",
		},
		{
			name:    "--summary missing value",
			args:    []string{"sess-1", "--summary"},
			wantErr: "--summary requires a value",
		},
		{
			name:    "--by-age missing value",
			args:    []string{"--by-age"},
			wantErr: "--by-age requires a value",
		},
		{
			name:    "--project missing value",
			args:    []string{"--project"},
			wantErr: "--project requires a value",
		},
		{
			name:    "--summary value that looks like a flag",
			args:    []string{"sess-1", "--summary", "--apply"},
			wantErr: "--summary requires a value",
		},
		{
			name:    "--summary empty value",
			args:    []string{"sess-1", "--summary", ""},
			wantErr: "--summary requires a value",
		},
		{
			name:    "--summary whitespace-only value",
			args:    []string{"sess-1", "--summary", "   "},
			wantErr: "--summary requires a value",
		},
		{
			name:    "--project value that looks like a flag",
			args:    []string{"sess-1", "--project", "--json"},
			wantErr: "--project requires a value",
		},
		{
			name:    "--project empty value",
			args:    []string{"--by-age", "30d", "--project", ""},
			wantErr: "--project requires a value",
		},
		{
			name:    "--project whitespace-only value",
			args:    []string{"--by-age", "30d", "--project", "  "},
			wantErr: "--project requires a value",
		},
		{
			name:    "garbage duration",
			args:    []string{"--by-age", "soon"},
			wantErr: "invalid --by-age value",
		},
		{
			name:    "zero duration",
			args:    []string{"--by-age", "0"},
			wantErr: "invalid --by-age value",
		},
		{
			name:    "negative duration",
			args:    []string{"--by-age", "-1h"},
			wantErr: "invalid --by-age value",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSessionEndArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseSessionEndArgs(%v) error = %v, want containing %q", tc.args, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSessionEndArgs(%v): %v", tc.args, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseSessionEndArgs(%v) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}

func TestParseSessionEndAge(t *testing.T) {
	tests := []struct {
		value    string
		want     time.Duration
		wantText string
	}{
		{value: "72h", want: 72 * time.Hour},
		{value: "45m", want: 45 * time.Minute},
		{value: "1h30m", want: 90 * time.Minute},
		{value: "30d", want: 30 * 24 * time.Hour},
		{value: "2w", want: 14 * 24 * time.Hour},
		{value: "", wantText: "invalid duration"},
		{value: "soon", wantText: "invalid duration"},
		{value: "30x", wantText: "invalid duration"},
		{value: "d", wantText: "invalid duration"},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			got, err := parseSessionEndAge(tc.value)
			if tc.wantText != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantText) {
					t.Fatalf("parseSessionEndAge(%q) error = %v, want containing %q", tc.value, err, tc.wantText)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSessionEndAge(%q): %v", tc.value, err)
			}
			if got != tc.want {
				t.Fatalf("parseSessionEndAge(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// ─── session end command behavior ────────────────────────────────────────────

// seedStaleSession seeds an open session whose started_at is backdated, so
// staleness windows keyed on real wall-clock time can select it.
func seedStaleSession(t *testing.T, cfg store.Config, sessionID, project string, startedAt time.Time) {
	t.Helper()
	mustSeedSession(t, cfg, sessionID, project)
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE sessions SET started_at = ? WHERE id = ?`, startedAt.UTC().Format("2006-01-02 15:04:05"), sessionID); err != nil {
		t.Fatalf("backdate session %s: %v", sessionID, err)
	}
}

func withSessionEndStoreSpy(t *testing.T, called *bool) {
	t.Helper()
	old := storeNew
	storeNew = func(cfg store.Config) (*store.Store, error) {
		*called = true
		return nil, errors.New("store must not be opened for this invocation")
	}
	t.Cleanup(func() { storeNew = old })
}

func TestCmdSessionEndSingle(t *testing.T) {
	t.Run("ends an open session", func(t *testing.T) {
		cfg := testConfig(t)
		mustSeedSession(t, cfg, "sess-end-1", "proj-end")

		withArgs(t, "engram", "session", "end", "sess-end-1")
		stdout, stderr := captureOutput(t, func() { cmdSession(cfg) })
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		if !strings.Contains(stdout, `Session "sess-end-1" ended`) {
			t.Fatalf("expected end confirmation in stdout, got: %q", stdout)
		}
		if got := mustQueryCount(t, cfg, `SELECT COUNT(*) FROM sessions WHERE id = ? AND ended_at IS NOT NULL`, "sess-end-1"); got != 1 {
			t.Fatalf("ended rows = %d, want 1", got)
		}
	})

	t.Run("reports already ended without failing", func(t *testing.T) {
		cfg := testConfig(t)
		mustSeedSession(t, cfg, "sess-end-2", "proj-end")
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("store.New: %v", err)
		}
		if _, err := s.EndSessionStrict("sess-end-2", nil); err != nil {
			t.Fatalf("EndSessionStrict: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}

		exited := stubbedExit(t)
		withArgs(t, "engram", "session", "end", "sess-end-2")
		stdout, stderr := captureOutput(t, func() { cmdSession(cfg) })
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		if !strings.Contains(stdout, `Session "sess-end-2" already ended`) {
			t.Fatalf("expected already-ended notice in stdout, got: %q", stdout)
		}
		if len(*exited) != 0 {
			t.Fatalf("expected exit code 0 (no exitFunc call), got codes %v", *exited)
		}
	})

	t.Run("fails on unknown session id", func(t *testing.T) {
		cfg := testConfig(t)

		exited := stubbedExit(t)
		withArgs(t, "engram", "session", "end", "no-such-session")
		_, stderr := captureOutput(t, func() { cmdSession(cfg) })
		if len(*exited) != 1 || (*exited)[0] != 1 {
			t.Fatalf("exit codes = %v, want [1]", *exited)
		}
		if !strings.Contains(stderr, `error: session "no-such-session" not found`) {
			t.Fatalf("expected not-found error in stderr, got: %q", stderr)
		}
	})

	t.Run("json output reports id status and ended_at", func(t *testing.T) {
		cfg := testConfig(t)
		mustSeedSession(t, cfg, "sess-end-json", "proj-end")

		withArgs(t, "engram", "session", "end", "sess-end-json", "--json")
		stdout, stderr := captureOutput(t, func() { cmdSession(cfg) })
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		var payload struct {
			ID      string  `json:"id"`
			Status  string  `json:"status"`
			EndedAt *string `json:"ended_at"`
		}
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("invalid single-end JSON %q: %v", stdout, err)
		}
		if payload.ID != "sess-end-json" || payload.Status != "ended" || payload.EndedAt == nil || *payload.EndedAt == "" {
			t.Fatalf("single-end JSON = %+v", payload)
		}
	})

	t.Run("json output for already ended reports original ended_at", func(t *testing.T) {
		cfg := testConfig(t)
		mustSeedSession(t, cfg, "sess-end-json2", "proj-end")
		s, err := store.New(cfg)
		if err != nil {
			t.Fatalf("store.New: %v", err)
		}
		if _, err := s.EndSessionStrict("sess-end-json2", nil); err != nil {
			t.Fatalf("EndSessionStrict: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		// Backdate so the JSON must carry the ORIGINAL ended_at, not a fresh one.
		db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
		if err != nil {
			t.Fatalf("open database: %v", err)
		}
		if _, err := db.Exec(`UPDATE sessions SET ended_at = '2020-01-02 03:04:05' WHERE id = ?`, "sess-end-json2"); err != nil {
			_ = db.Close()
			t.Fatalf("backdate ended_at: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}

		withArgs(t, "engram", "session", "end", "sess-end-json2", "--json")
		stdout, stderr := captureOutput(t, func() { cmdSession(cfg) })
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		var payload2 struct {
			ID      string  `json:"id"`
			Status  string  `json:"status"`
			EndedAt *string `json:"ended_at"`
		}
		if err := json.Unmarshal([]byte(stdout), &payload2); err != nil {
			t.Fatalf("invalid single-end JSON %q: %v", stdout, err)
		}
		if payload2.ID != "sess-end-json2" || payload2.Status != "already_ended" || payload2.EndedAt == nil || *payload2.EndedAt != "2020-01-02 03:04:05" {
			t.Fatalf("single-end JSON = %+v, want already_ended with original ended_at", payload2)
		}
	})

	t.Run("json output is built from the end result without a second read", func(t *testing.T) {
		// The session stays open in the store, so any post-commit re-read would
		// find no ended_at and drop the field. The result-carried timestamp must
		// win, or a committed end could still lose its JSON to a read failure.
		cfg := testConfig(t)
		mustSeedSession(t, cfg, "sess-end-json-result", "proj-end")
		resultEndedAt := "2021-06-07 08:09:10"
		oldEnd := storeEndSessionStrict
		storeEndSessionStrict = func(s *store.Store, id string, summary *string) (store.SessionEndResult, error) {
			return store.SessionEndResult{Status: store.SessionEndStatusEnded, EndedAt: &resultEndedAt}, nil
		}
		t.Cleanup(func() { storeEndSessionStrict = oldEnd })

		withArgs(t, "engram", "session", "end", "sess-end-json-result", "--json")
		stdout, stderr := captureOutput(t, func() { cmdSession(cfg) })
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		var payload struct {
			ID      string  `json:"id"`
			Status  string  `json:"status"`
			EndedAt *string `json:"ended_at"`
		}
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("invalid single-end JSON %q: %v", stdout, err)
		}
		if payload.ID != "sess-end-json-result" || payload.Status != "ended" || payload.EndedAt == nil || *payload.EndedAt != resultEndedAt {
			t.Fatalf("single-end JSON = %+v, want ended with result-carried ended_at %q", payload, resultEndedAt)
		}
	})

	t.Run("unknown id fails even with json", func(t *testing.T) {
		cfg := testConfig(t)

		exited := stubbedExit(t)
		withArgs(t, "engram", "session", "end", "no-such-json", "--json")
		stdout, stderr := captureOutput(t, func() { cmdSession(cfg) })
		if len(*exited) != 1 || (*exited)[0] != 1 {
			t.Fatalf("exit codes = %v, want [1]", *exited)
		}
		if !strings.Contains(stderr, `not found`) {
			t.Fatalf("expected not-found error in stderr, got: %q", stderr)
		}
		if strings.Contains(stdout, "{") {
			t.Fatalf("expected no JSON on stdout for the fatal path, got: %q", stdout)
		}
	})
}

func TestCmdSessionEndBulkDryRunLeavesSessionsOpen(t *testing.T) {
	cfg := testConfig(t)
	now := time.Now()
	seedStaleSession(t, cfg, "bulk-dry-a", "proj-dry", now.Add(-45*24*time.Hour))
	seedStaleSession(t, cfg, "bulk-dry-b", "proj-dry", now.Add(-40*24*time.Hour))
	seedStaleSession(t, cfg, "bulk-dry-fresh", "proj-dry", now.Add(-time.Hour))

	withArgs(t, "engram", "session", "end", "--by-age", "30d")
	stdout, stderr := captureOutput(t, func() { cmdSession(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "DRY RUN — 2 session(s) would be ended:") {
		t.Fatalf("expected dry-run header in stdout, got: %q", stdout)
	}
	if !strings.Contains(stdout, "re-run with --apply to end them") {
		t.Fatalf("expected dry-run hint in stdout, got: %q", stdout)
	}
	for _, id := range []string{"bulk-dry-a", "bulk-dry-b"} {
		if !strings.Contains(stdout, id) {
			t.Fatalf("expected %s listed in dry-run output, got: %q", id, stdout)
		}
	}
	if got := mustQueryCount(t, cfg, `SELECT COUNT(*) FROM sessions WHERE ended_at IS NOT NULL`); got != 0 {
		t.Fatalf("dry-run ended %d rows, want 0", got)
	}
}

func TestCmdSessionEndBulkApplyEndsStale(t *testing.T) {
	cfg := testConfig(t)
	now := time.Now()
	seedStaleSession(t, cfg, "bulk-app-a", "proj-app", now.Add(-45*24*time.Hour))
	seedStaleSession(t, cfg, "bulk-app-b", "proj-other", now.Add(-40*24*time.Hour))
	seedStaleSession(t, cfg, "bulk-app-fresh", "proj-app", now.Add(-time.Hour))

	withArgs(t, "engram", "session", "end", "--by-age", "30d", "--project", "proj-app", "--apply")
	stdout, stderr := captureOutput(t, func() { cmdSession(cfg) })
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "Ended 1 session(s)") {
		t.Fatalf("expected ended count in stdout, got: %q", stdout)
	}
	if !strings.Contains(stdout, "bulk-app-a") {
		t.Fatalf("expected ended id in stdout, got: %q", stdout)
	}
	if got := mustQueryCount(t, cfg, `SELECT COUNT(*) FROM sessions WHERE id = ? AND ended_at IS NOT NULL`, "bulk-app-a"); got != 1 {
		t.Fatalf("bulk-app-a ended rows = %d, want 1", got)
	}
	if got := mustQueryCount(t, cfg, `SELECT COUNT(*) FROM sessions WHERE ended_at IS NOT NULL`); got != 1 {
		t.Fatalf("total ended rows = %d, want only the project match", got)
	}
}

func TestCmdSessionEndBulkJSON(t *testing.T) {
	t.Run("dry-run json reports what would end", func(t *testing.T) {
		cfg := testConfig(t)
		now := time.Now()
		seedStaleSession(t, cfg, "json-dry-a", "proj-json", now.Add(-45*24*time.Hour))
		seedStaleSession(t, cfg, "json-dry-b", "proj-json", now.Add(-40*24*time.Hour))

		withArgs(t, "engram", "session", "end", "--by-age", "30d", "--json")
		stdout, stderr := captureOutput(t, func() { cmdSession(cfg) })
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		var payload struct {
			DryRun   bool     `json:"dry_run"`
			WouldEnd []string `json:"would_end"`
			Count    int      `json:"count"`
		}
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("invalid dry-run JSON %q: %v", stdout, err)
		}
		if !payload.DryRun || payload.Count != 2 || !reflect.DeepEqual(payload.WouldEnd, []string{"json-dry-a", "json-dry-b"}) {
			t.Fatalf("dry-run JSON = %+v", payload)
		}
		if got := mustQueryCount(t, cfg, `SELECT COUNT(*) FROM sessions WHERE ended_at IS NOT NULL`); got != 0 {
			t.Fatalf("dry-run ended %d rows, want 0", got)
		}
	})

	t.Run("apply json reports what ended", func(t *testing.T) {
		cfg := testConfig(t)
		now := time.Now()
		seedStaleSession(t, cfg, "json-app-a", "proj-json", now.Add(-45*24*time.Hour))

		withArgs(t, "engram", "session", "end", "--by-age", "30d", "--json", "--apply")
		stdout, stderr := captureOutput(t, func() { cmdSession(cfg) })
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		var payload struct {
			Ended []string `json:"ended"`
			Count int      `json:"count"`
		}
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("invalid apply JSON %q: %v", stdout, err)
		}
		if payload.Count != 1 || !reflect.DeepEqual(payload.Ended, []string{"json-app-a"}) {
			t.Fatalf("apply JSON = %+v", payload)
		}
		if got := mustQueryCount(t, cfg, `SELECT COUNT(*) FROM sessions WHERE ended_at IS NOT NULL`); got != 1 {
			t.Fatalf("ended rows = %d, want 1", got)
		}
	})
}

func TestCmdSessionEndRejectsInvalidInvocationsBeforeStoreOpen(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "id with bulk filter", args: []string{"end", "sess-1", "--by-age", "72h"}, wantErr: "mutually exclusive"},
		{name: "apply without filters", args: []string{"end", "--apply"}, wantErr: "session ID"},
		{name: "unknown flag", args: []string{"end", "sess-1", "--sumary", "x"}, wantErr: "unknown flag"},
		{name: "apply with id", args: []string{"end", "sess-1", "--apply"}, wantErr: "only valid"},
		{name: "garbage duration", args: []string{"end", "--by-age", "soon"}, wantErr: "invalid --by-age value"},
		{name: "summary value that looks like a flag", args: []string{"end", "sess-1", "--summary", "--apply"}, wantErr: "--summary requires a value"},
		{name: "bulk project without an explicit window", args: []string{"end", "--project", "web"}, wantErr: "requires --by-age"},
		{name: "bulk project with apply but no window", args: []string{"end", "--project", "web", "--apply"}, wantErr: "requires --by-age"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			storeOpened := false
			withSessionEndStoreSpy(t, &storeOpened)
			exited := stubbedExit(t)

			withArgs(t, append([]string{"engram", "session"}, tc.args...)...)
			_, stderr := captureOutput(t, func() { cmdSession(cfg) })
			if len(*exited) != 1 || (*exited)[0] != 1 {
				t.Fatalf("exit codes = %v, want [1]", *exited)
			}
			if !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("expected %q in stderr, got: %q", tc.wantErr, stderr)
			}
			if !strings.Contains(stderr, "usage: engram session end") {
				t.Fatalf("expected usage in stderr, got: %q", stderr)
			}
			if storeOpened {
				t.Fatal("storeNew must not be called for an invalid invocation")
			}
		})
	}
}

func TestCmdSessionDispatch(t *testing.T) {
	t.Run("unknown session subcommand is rejected", func(t *testing.T) {
		cfg := testConfig(t)

		exited := stubbedExit(t)
		withArgs(t, "engram", "session", "bogus")
		_, stderr := captureOutput(t, func() { cmdSession(cfg) })
		if len(*exited) != 1 || (*exited)[0] != 1 {
			t.Fatalf("exit codes = %v, want [1]", *exited)
		}
		if !strings.Contains(stderr, "unknown session command") {
			t.Fatalf("expected unknown-subcommand error in stderr, got: %q", stderr)
		}
	})

	t.Run("missing session subcommand prints usage", func(t *testing.T) {
		cfg := testConfig(t)

		exited := stubbedExit(t)
		withArgs(t, "engram", "session")
		_, stderr := captureOutput(t, func() { cmdSession(cfg) })
		if len(*exited) != 1 || (*exited)[0] != 1 {
			t.Fatalf("exit codes = %v, want [1]", *exited)
		}
		if !strings.Contains(stderr, "usage: engram session") {
			t.Fatalf("expected usage in stderr, got: %q", stderr)
		}
	})
}

func TestSessionEndInUsage(t *testing.T) {
	stdout, _ := captureOutput(t, func() { printUsage() })
	for _, want := range []string{"session end <id>", "--by-age"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected %q in usage output, got:\n%s", want, stdout)
		}
	}
}
