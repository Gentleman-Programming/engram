package main

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// ─── delete strict trailing-argument validation (issue #1084) ─────────────────
//
// The delete command family used to silently ignore unsupported trailing
// tokens: "delete 1 --dry-run" still deleted the observation. These tests pin
// the strict contract: any unexpected token after a delete target — unknown
// flag, flag plus value, or bare operand — must produce a nonzero exit, name
// the token(s), and print the usage line to stderr BEFORE any destructive
// store operation, while the documented invocations keep working.

// stubbedExit swaps exitFunc for a recorder so tests can assert argument
// rejection without exiting the test binary. It returns the recorded codes.
func stubbedExit(t *testing.T) *[]int {
	t.Helper()
	codes := &[]int{}
	oldExit := exitFunc
	exitFunc = func(code int) { *codes = append(*codes, code) }
	t.Cleanup(func() { exitFunc = oldExit })
	return codes
}

// mustQueryCount runs a scalar COUNT query against the test database so tests
// can assert that rows survived (or were deleted) exactly as expected.
func mustQueryCount(t *testing.T, cfg store.Config, query string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

// rowCount is one expected scalar COUNT result: the query, its bind argument,
// and the row count the suite demands.
type rowCount struct {
	query string
	arg   any
	want  int
}

// deleteSeed plants fixture data in a fresh config and returns the config,
// the argv tokens that follow "engram delete", and expected row counts.
type deleteSeed func(t *testing.T) (store.Config, []string, []rowCount)

// deleteRoute describes one delete subcommand for the rejection suite. Each
// route contributes only its seeding, its argv shape, its usage line, and its
// survival queries; the shared test body owns every behavioral assertion.
type deleteRoute struct {
	name  string // subtest prefix
	usage string // usage line rejection must print on stderr
	seed  deleteSeed
}

// deleteRoutes enumerates every delete subcommand. The intact counts pin the
// soft-keep semantics per route: after a rejection the victim must still be
// live AND still be physically present (no silent soft-delete side effect).
var deleteRoutes = []deleteRoute{
	{
		name:  "observation",
		usage: "engram delete <observation_id> [--hard]",
		seed: func(t *testing.T) (store.Config, []string, []rowCount) {
			cfg := testConfig(t)
			id := mustSeedObservation(t, cfg, "sess-strict-obs", "proj-strict-obs", "decision", "strict", "must survive", "project")
			return cfg, []string{strconv.FormatInt(id, 10)}, []rowCount{
				{"SELECT COUNT(*) FROM observations WHERE id = ? AND deleted_at IS NULL", id, 1},
				{"SELECT COUNT(*) FROM observations WHERE id = ?", id, 1},
			}
		},
	},
	{
		name:  "session",
		usage: "engram delete session <id>",
		seed: func(t *testing.T) (store.Config, []string, []rowCount) {
			cfg := testConfig(t)
			mustSeedSession(t, cfg, "sess-strict", "proj-strict-sess")
			return cfg, []string{"session", "sess-strict"}, []rowCount{
				{"SELECT COUNT(*) FROM sessions WHERE id = ?", "sess-strict", 1},
			}
		},
	},
	{
		name:  "prompt",
		usage: "engram delete prompt <id>",
		seed: func(t *testing.T) (store.Config, []string, []rowCount) {
			cfg := testConfig(t)
			id := mustSeedPrompt(t, cfg, "sess-strict-prompt", "proj-strict-prompt")
			return cfg, []string{"prompt", strconv.FormatInt(id, 10)}, []rowCount{
				{"SELECT COUNT(*) FROM user_prompts WHERE id = ?", id, 1},
			}
		},
	},
	{
		name:  "project",
		usage: "engram delete project <name> [--hard]",
		seed: func(t *testing.T) (store.Config, []string, []rowCount) {
			cfg := testConfig(t)
			mustSeedObservation(t, cfg, "sess-strict-proj", "proj-strict-target", "decision", "strict", "must survive", "project")
			return cfg, []string{"project", "proj-strict-target"}, []rowCount{
				{"SELECT COUNT(*) FROM observations WHERE project = ? AND deleted_at IS NULL", "proj-strict-target", 1},
				{"SELECT COUNT(*) FROM observations WHERE project = ?", "proj-strict-target", 1},
			}
		},
	},
}

// deleteRejectTrailing lists the unsupported trailing-token shapes every route
// must reject before touching the store: an unrelated flag, a flag plus its
// value, --help, a typoed flag, a bare non-flag operand, and a destructive
// flag combination whose unsupported member must still abort the run.
var deleteRejectTrailing = []struct {
	name     string
	trailing []string
	wantErr  []string // token(s) stderr must name
}{
	{"dry-run flag", []string{"--dry-run"}, []string{"--dry-run"}},
	{"before cutoff flag and value", []string{"--before", "2000-01-01"}, []string{"--before", "2000-01-01"}},
	{"help flag after target", []string{"--help"}, []string{"--help"}},
	{"typoed flag", []string{"--dry-ru"}, []string{"--dry-ru"}},
	{"bare trailing operand", []string{"extra"}, []string{"extra"}},
	{"hard with unsupported dry-run flag", []string{"--hard", "--dry-run"}, []string{"--dry-run"}},
}

func TestCmdDeleteRejectsTrailingArgs(t *testing.T) {
	for _, r := range deleteRoutes {
		for _, tt := range deleteRejectTrailing {
			t.Run(r.name+" "+tt.name, func(t *testing.T) {
				cfg, target, intact := r.seed(t)

				codes := stubbedExit(t)
				args := append(append([]string{"engram", "delete"}, target...), tt.trailing...)
				withArgs(t, args...)
				stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

				if len(*codes) == 0 || (*codes)[0] == 0 {
					t.Fatalf("expected nonzero exit for trailing args %v; command ran anyway (stdout=%q stderr=%q)", tt.trailing, stdout, stderr)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(stderr, want) {
						t.Errorf("expected stderr to name %q, got: %q", want, stderr)
					}
				}
				if !strings.Contains(stderr, "usage: "+r.usage) {
					t.Errorf("expected stderr to show usage %q, got: %q", "usage: "+r.usage, stderr)
				}
				if strings.Contains(stdout, "deleted") {
					t.Errorf("expected no deletion confirmation on stdout, got: %q", stdout)
				}
				for _, c := range intact {
					if got := mustQueryCount(t, cfg, c.query, c.arg); got != c.want {
						t.Errorf("victim must remain intact after rejection (%s): got %d rows, want %d", c.query, got, c.want)
					}
				}
			})
		}
	}
}

// TestCmdDeleteRejectsHelpTargets proves that standard help tokens abort before
// deletion and leave matching session and project records intact.
func TestCmdDeleteRejectsHelpTargets(t *testing.T) {
	tests := []struct {
		name  string
		usage string
		seed  deleteSeed
	}{
		{
			name:  "session",
			usage: "engram delete session <id>",
			seed: func(t *testing.T) (store.Config, []string, []rowCount) {
				cfg := testConfig(t)
				mustSeedSession(t, cfg, "--help", "proj-flag-shaped-sess")
				return cfg, []string{"session", "--help"}, []rowCount{
					{"SELECT COUNT(*) FROM sessions WHERE id = ?", "--help", 1},
				}
			},
		},
		{
			name:  "project",
			usage: "engram delete project <name> [--hard]",
			seed: func(t *testing.T) (store.Config, []string, []rowCount) {
				cfg := testConfig(t)
				// Storage canonicalizes project names (-- collapses to -), so the
				// seeded rows live under "-help"; count that stored form.
				mustSeedObservation(t, cfg, "sess-flag-shaped-proj", "--help", "decision", "flag-shaped", "must survive", "project")
				return cfg, []string{"project", "--help"}, []rowCount{
					{"SELECT COUNT(*) FROM observations WHERE project = ? AND deleted_at IS NULL", "-help", 1},
					{"SELECT COUNT(*) FROM observations WHERE project = ?", "-help", 1},
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, target, intact := tt.seed(t)

			codes := stubbedExit(t)
			withArgs(t, append([]string{"engram", "delete"}, target...)...)
			stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

			if len(*codes) == 0 || (*codes)[0] == 0 {
				t.Fatalf("expected nonzero exit for help target %v; command ran anyway (stdout=%q stderr=%q)", target, stdout, stderr)
			}
			if !strings.Contains(stderr, "usage: "+tt.usage) {
				t.Errorf("expected stderr to show usage %q, got: %q", "usage: "+tt.usage, stderr)
			}
			if strings.Contains(stdout, "deleted") {
				t.Errorf("expected no deletion confirmation on stdout, got: %q", stdout)
			}
			for _, c := range intact {
				if got := mustQueryCount(t, cfg, c.query, c.arg); got != c.want {
					t.Errorf("record named --help must remain intact (%s): got %d rows, want %d", c.query, got, c.want)
				}
			}
		})
	}
}

// TestCmdDeleteRejectsLiteralHelpTargets proves that the bare word "help"
// also aborts before store access and leaves matching session and project
// records intact: "engram delete project help" reads as a help request, not
// as consent to cascade-delete a project actually named "help".
func TestCmdDeleteRejectsLiteralHelpTargets(t *testing.T) {
	tests := []struct {
		name  string
		usage string
		seed  deleteSeed
	}{
		{
			name:  "session",
			usage: "engram delete session <id>",
			seed: func(t *testing.T) (store.Config, []string, []rowCount) {
				cfg := testConfig(t)
				mustSeedSession(t, cfg, "help", "proj-literal-help-sess")
				return cfg, []string{"session", "help"}, []rowCount{
					{"SELECT COUNT(*) FROM sessions WHERE id = ?", "help", 1},
				}
			},
		},
		{
			name:  "project",
			usage: "engram delete project <name> [--hard]",
			seed: func(t *testing.T) (store.Config, []string, []rowCount) {
				cfg := testConfig(t)
				mustSeedObservation(t, cfg, "sess-literal-help-proj", "help", "decision", "literal-help", "must survive", "project")
				return cfg, []string{"project", "help"}, []rowCount{
					{"SELECT COUNT(*) FROM observations WHERE project = ? AND deleted_at IS NULL", "help", 1},
					{"SELECT COUNT(*) FROM observations WHERE project = ?", "help", 1},
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, target, intact := tt.seed(t)

			codes := stubbedExit(t)
			withArgs(t, append([]string{"engram", "delete"}, target...)...)
			stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

			if len(*codes) == 0 || (*codes)[0] == 0 {
				t.Fatalf("expected nonzero exit for help target %v; command ran anyway (stdout=%q stderr=%q)", target, stdout, stderr)
			}
			if !strings.Contains(stderr, "usage: "+tt.usage) {
				t.Errorf("expected stderr to show usage %q, got: %q", "usage: "+tt.usage, stderr)
			}
			if strings.Contains(stdout, "deleted") {
				t.Errorf("expected no deletion confirmation on stdout, got: %q", stdout)
			}
			for _, c := range intact {
				if got := mustQueryCount(t, cfg, c.query, c.arg); got != c.want {
					t.Errorf("record named help must remain intact (%s): got %d rows, want %d", c.query, got, c.want)
				}
			}
		})
	}
}

func TestRejectDeleteHelpTargetShortFormOnly(t *testing.T) {
	codes := stubbedExit(t)
	handled := false
	_, _ = captureOutput(t, func() { handled = rejectDeleteHelpTarget("-h", "test") })
	if !handled || rejectDeleteHelpTarget("-session-id", "test") || len(*codes) != 1 {
		t.Fatalf("short help handling=%t exit=%v, want only -h rejected", handled, *codes)
	}
}

// Valid documented invocations keep working exactly as before, now with
// row-level post-state assertions so regressions in the strict validation
// cannot break the supported paths silently. Each case contributes its
// seeding, argv, and expected post-state; the shared body owns the
// exit/stderr/stdout assertions.
func TestCmdDeleteValidArgsUnchangedBehavior(t *testing.T) {
	tests := []struct {
		name       string
		seed       deleteSeed
		wantStdout string
	}{
		{
			name: "observation soft delete",
			seed: func(t *testing.T) (store.Config, []string, []rowCount) {
				cfg := testConfig(t)
				id := mustSeedObservation(t, cfg, "sess-valid-soft", "proj-valid-soft", "decision", "valid", "soft delete me", "project")
				return cfg, []string{strconv.FormatInt(id, 10)}, []rowCount{
					{"SELECT COUNT(*) FROM observations WHERE id = ? AND deleted_at IS NULL", id, 0},
					{"SELECT COUNT(*) FROM observations WHERE id = ?", id, 1},
				}
			},
			wantStdout: "soft-deleted",
		},
		{
			name: "observation hard delete",
			seed: func(t *testing.T) (store.Config, []string, []rowCount) {
				cfg := testConfig(t)
				id := mustSeedObservation(t, cfg, "sess-valid-hard", "proj-valid-hard", "decision", "valid", "hard delete me", "project")
				return cfg, []string{strconv.FormatInt(id, 10), "--hard"}, []rowCount{
					{"SELECT COUNT(*) FROM observations WHERE id = ?", id, 0},
				}
			},
			wantStdout: "hard-deleted",
		},
		{
			name: "session delete",
			seed: func(t *testing.T) (store.Config, []string, []rowCount) {
				cfg := testConfig(t)
				mustSeedSession(t, cfg, "sess-valid", "proj-valid-sess")
				return cfg, []string{"session", "sess-valid"}, []rowCount{
					{"SELECT COUNT(*) FROM sessions WHERE id = ?", "sess-valid", 0},
				}
			},
			wantStdout: "deleted",
		},
		{
			name: "prompt delete",
			seed: func(t *testing.T) (store.Config, []string, []rowCount) {
				cfg := testConfig(t)
				id := mustSeedPrompt(t, cfg, "sess-valid-prompt", "proj-valid-prompt")
				return cfg, []string{"prompt", strconv.FormatInt(id, 10)}, []rowCount{
					{"SELECT COUNT(*) FROM user_prompts WHERE id = ?", id, 0},
				}
			},
			wantStdout: "deleted",
		},
		{
			name: "project soft delete",
			seed: func(t *testing.T) (store.Config, []string, []rowCount) {
				cfg := testConfig(t)
				mustSeedObservation(t, cfg, "sess-valid-proj-soft", "proj-valid-softdel", "decision", "valid", "content", "project")
				return cfg, []string{"project", "proj-valid-softdel"}, []rowCount{
					{"SELECT COUNT(*) FROM observations WHERE project = ? AND deleted_at IS NULL", "proj-valid-softdel", 0},
					{"SELECT COUNT(*) FROM observations WHERE project = ?", "proj-valid-softdel", 1},
				}
			},
			wantStdout: "soft-deleted",
		},
		{
			name: "project hard delete",
			seed: func(t *testing.T) (store.Config, []string, []rowCount) {
				cfg := testConfig(t)
				mustSeedObservation(t, cfg, "sess-valid-proj-hard", "proj-valid-harddel", "decision", "valid", "content", "project")
				return cfg, []string{"project", "proj-valid-harddel", "--hard"}, []rowCount{
					{"SELECT COUNT(*) FROM observations WHERE project = ?", "proj-valid-harddel", 0},
				}
			},
			wantStdout: "hard-deleted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, args, after := tt.seed(t)

			codes := stubbedExit(t)
			withArgs(t, append([]string{"engram", "delete"}, args...)...)
			stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

			if len(*codes) != 0 {
				t.Fatalf("expected no exit for valid %s, got codes %v", tt.name, *codes)
			}
			if stderr != "" {
				t.Fatalf("expected no stderr for valid %s, got: %q", tt.name, stderr)
			}
			if !strings.Contains(stdout, tt.wantStdout) {
				t.Fatalf("expected %q confirmation, got: %q", tt.wantStdout, stdout)
			}
			for _, c := range after {
				if got := mustQueryCount(t, cfg, c.query, c.arg); got != c.want {
					t.Errorf("wrong post-state after valid %s (%s): got %d rows, want %d", tt.name, c.query, got, c.want)
				}
			}
		})
	}
}
