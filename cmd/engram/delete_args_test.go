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
// the strict contract: any unexpected token after a delete target must produce
// a nonzero exit and a stderr message naming the token(s) BEFORE any
// destructive store operation, while the documented invocations keep working.

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
	defer db.Close()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

func TestCmdDeleteObservationRejectsTrailingArgs(t *testing.T) {
	tests := []struct {
		name     string
		trailing []string
		wantErr  []string
	}{
		{"dry-run flag", []string{"--dry-run"}, []string{"--dry-run"}},
		{"before cutoff flag and value", []string{"--before", "2000-01-01"}, []string{"--before", "2000-01-01"}},
		{"help flag after target", []string{"--help"}, []string{"--help"}},
		{"typoed flag", []string{"--dry-ru"}, []string{"--dry-ru"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			id := mustSeedObservation(t, cfg, "sess-strict-obs", "proj-strict-obs", "decision", "strict", "must survive", "project")

			codes := stubbedExit(t)
			args := append([]string{"engram", "delete", strconv.FormatInt(id, 10)}, tt.trailing...)
			withArgs(t, args...)
			stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

			if len(*codes) == 0 {
				t.Fatalf("expected nonzero exit for trailing args %v; command ran anyway (stdout=%q stderr=%q)", tt.trailing, stdout, stderr)
			}
			if (*codes)[0] == 0 {
				t.Fatalf("expected nonzero exit code, got 0")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(stderr, want) {
					t.Errorf("expected stderr to name %q, got: %q", want, stderr)
				}
			}
			if strings.Contains(stdout, "deleted") {
				t.Errorf("expected no deletion confirmation on stdout, got: %q", stdout)
			}
			if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE id = ? AND deleted_at IS NULL", id); got != 1 {
				t.Errorf("observation must remain live after rejection, got %d live rows", got)
			}
			if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE id = ?", id); got != 1 {
				t.Errorf("observation row must remain physically present after rejection, got %d rows", got)
			}
		})
	}
}

func TestCmdDeleteSessionRejectsTrailingArgs(t *testing.T) {
	tests := []struct {
		name     string
		trailing []string
		wantErr  []string
	}{
		{"dry-run flag", []string{"--dry-run"}, []string{"--dry-run"}},
		{"before cutoff flag and value", []string{"--before", "2000-01-01"}, []string{"--before", "2000-01-01"}},
		{"help flag after target", []string{"--help"}, []string{"--help"}},
		{"typoed flag", []string{"--dry-ru"}, []string{"--dry-ru"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			mustSeedSession(t, cfg, "sess-strict", "proj-strict-sess")

			codes := stubbedExit(t)
			args := append([]string{"engram", "delete", "session", "sess-strict"}, tt.trailing...)
			withArgs(t, args...)
			stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

			if len(*codes) == 0 {
				t.Fatalf("expected nonzero exit for trailing args %v; command ran anyway (stdout=%q stderr=%q)", tt.trailing, stdout, stderr)
			}
			if (*codes)[0] == 0 {
				t.Fatalf("expected nonzero exit code, got 0")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(stderr, want) {
					t.Errorf("expected stderr to name %q, got: %q", want, stderr)
				}
			}
			if strings.Contains(stdout, "deleted") {
				t.Errorf("expected no deletion confirmation on stdout, got: %q", stdout)
			}
			if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM sessions WHERE id = ?", "sess-strict"); got != 1 {
				t.Errorf("session must survive rejection, got %d rows", got)
			}
		})
	}
}

func TestCmdDeletePromptRejectsTrailingArgs(t *testing.T) {
	tests := []struct {
		name     string
		trailing []string
		wantErr  []string
	}{
		{"dry-run flag", []string{"--dry-run"}, []string{"--dry-run"}},
		{"before cutoff flag and value", []string{"--before", "2000-01-01"}, []string{"--before", "2000-01-01"}},
		{"help flag after target", []string{"--help"}, []string{"--help"}},
		{"typoed flag", []string{"--dry-ru"}, []string{"--dry-ru"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			promptID := mustSeedPrompt(t, cfg, "sess-strict-prompt", "proj-strict-prompt")

			codes := stubbedExit(t)
			args := append([]string{"engram", "delete", "prompt", strconv.FormatInt(promptID, 10)}, tt.trailing...)
			withArgs(t, args...)
			stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

			if len(*codes) == 0 {
				t.Fatalf("expected nonzero exit for trailing args %v; command ran anyway (stdout=%q stderr=%q)", tt.trailing, stdout, stderr)
			}
			if (*codes)[0] == 0 {
				t.Fatalf("expected nonzero exit code, got 0")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(stderr, want) {
					t.Errorf("expected stderr to name %q, got: %q", want, stderr)
				}
			}
			if strings.Contains(stdout, "deleted") {
				t.Errorf("expected no deletion confirmation on stdout, got: %q", stdout)
			}
			if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM user_prompts WHERE id = ?", promptID); got != 1 {
				t.Errorf("prompt must survive rejection, got %d rows", got)
			}
		})
	}
}

func TestCmdDeleteProjectRejectsTrailingArgs(t *testing.T) {
	tests := []struct {
		name     string
		trailing []string
		wantErr  []string
	}{
		{"dry-run flag", []string{"--dry-run"}, []string{"--dry-run"}},
		{"before cutoff flag and value", []string{"--before", "2000-01-01"}, []string{"--before", "2000-01-01"}},
		{"help flag after target", []string{"--help"}, []string{"--help"}},
		{"typoed flag", []string{"--dry-ru"}, []string{"--dry-ru"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			mustSeedObservation(t, cfg, "sess-strict-proj", "proj-strict-target", "decision", "strict", "must survive", "project")

			codes := stubbedExit(t)
			args := append([]string{"engram", "delete", "project", "proj-strict-target"}, tt.trailing...)
			withArgs(t, args...)
			stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

			if len(*codes) == 0 {
				t.Fatalf("expected nonzero exit for trailing args %v; command ran anyway (stdout=%q stderr=%q)", tt.trailing, stdout, stderr)
			}
			if (*codes)[0] == 0 {
				t.Fatalf("expected nonzero exit code, got 0")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(stderr, want) {
					t.Errorf("expected stderr to name %q, got: %q", want, stderr)
				}
			}
			if strings.Contains(stdout, "deleted") {
				t.Errorf("expected no deletion confirmation on stdout, got: %q", stdout)
			}
			if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE project = ? AND deleted_at IS NULL", "proj-strict-target"); got != 1 {
				t.Errorf("project observations must remain live after rejection, got %d live rows", got)
			}
		})
	}
}

// A destructive flag combination must still be rejected before mutation:
// "--hard --dry-run" must not physically delete anything.
func TestCmdDeleteObservationHardWithUnsupportedFlagDoesNotDelete(t *testing.T) {
	cfg := testConfig(t)
	id := mustSeedObservation(t, cfg, "sess-hard-dry", "proj-hard-dry", "decision", "hard-dry", "must survive", "project")

	codes := stubbedExit(t)
	withArgs(t, "engram", "delete", strconv.FormatInt(id, 10), "--hard", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

	if len(*codes) == 0 {
		t.Fatalf("expected nonzero exit for --hard --dry-run; command ran anyway (stdout=%q stderr=%q)", stdout, stderr)
	}
	if !strings.Contains(stderr, "--dry-run") {
		t.Fatalf("expected stderr to name \"--dry-run\", got: %q", stderr)
	}
	if strings.Contains(stdout, "deleted") {
		t.Errorf("expected no deletion confirmation on stdout, got: %q", stdout)
	}
	if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE id = ?", id); got != 1 {
		t.Fatalf("observation must not be physically deleted, got %d rows", got)
	}
}

func TestCmdDeleteProjectHardWithUnsupportedFlagDoesNotDelete(t *testing.T) {
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "sess-hard-dry-proj", "proj-hard-dry-target", "decision", "hard-dry", "must survive", "project")

	codes := stubbedExit(t)
	withArgs(t, "engram", "delete", "project", "proj-hard-dry-target", "--hard", "--dry-run")
	stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

	if len(*codes) == 0 {
		t.Fatalf("expected nonzero exit for --hard --dry-run; command ran anyway (stdout=%q stderr=%q)", stdout, stderr)
	}
	if !strings.Contains(stderr, "--dry-run") {
		t.Fatalf("expected stderr to name \"--dry-run\", got: %q", stderr)
	}
	if strings.Contains(stdout, "deleted") {
		t.Errorf("expected no deletion confirmation on stdout, got: %q", stdout)
	}
	if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE project = ?", "proj-hard-dry-target"); got != 1 {
		t.Fatalf("project observations must not be physically deleted, got %d rows", got)
	}
}

// Valid documented invocations keep working exactly as before, now with
// row-level assertions so regressions in the strict validation cannot break
// the supported paths silently.
func TestCmdDeleteValidArgsUnchangedBehavior(t *testing.T) {
	t.Run("observation soft delete", func(t *testing.T) {
		cfg := testConfig(t)
		id := mustSeedObservation(t, cfg, "sess-valid-soft", "proj-valid-soft", "decision", "valid", "soft delete me", "project")

		codes := stubbedExit(t)
		withArgs(t, "engram", "delete", strconv.FormatInt(id, 10))
		stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

		if len(*codes) != 0 {
			t.Fatalf("expected no exit for valid soft delete, got codes %v", *codes)
		}
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		if !strings.Contains(stdout, "soft-deleted") {
			t.Fatalf("expected soft-delete confirmation, got: %q", stdout)
		}
		if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE id = ? AND deleted_at IS NULL", id); got != 0 {
			t.Errorf("expected observation to be soft-deleted, got %d live rows", got)
		}
		if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE id = ?", id); got != 1 {
			t.Errorf("soft delete must keep the row, got %d rows", got)
		}
	})

	t.Run("observation hard delete", func(t *testing.T) {
		cfg := testConfig(t)
		id := mustSeedObservation(t, cfg, "sess-valid-hard", "proj-valid-hard", "decision", "valid", "hard delete me", "project")

		codes := stubbedExit(t)
		withArgs(t, "engram", "delete", strconv.FormatInt(id, 10), "--hard")
		stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

		if len(*codes) != 0 {
			t.Fatalf("expected no exit for valid hard delete, got codes %v", *codes)
		}
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		if !strings.Contains(stdout, "hard-deleted") {
			t.Fatalf("expected hard-delete confirmation, got: %q", stdout)
		}
		if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE id = ?", id); got != 0 {
			t.Errorf("hard delete must remove the row, got %d rows", got)
		}
	})

	t.Run("session delete", func(t *testing.T) {
		cfg := testConfig(t)
		mustSeedSession(t, cfg, "sess-valid", "proj-valid-sess")

		codes := stubbedExit(t)
		withArgs(t, "engram", "delete", "session", "sess-valid")
		stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

		if len(*codes) != 0 {
			t.Fatalf("expected no exit for valid session delete, got codes %v", *codes)
		}
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		if !strings.Contains(stdout, "deleted") {
			t.Fatalf("expected deletion confirmation, got: %q", stdout)
		}
		if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM sessions WHERE id = ?", "sess-valid"); got != 0 {
			t.Errorf("session must be deleted, got %d rows", got)
		}
	})

	t.Run("prompt delete", func(t *testing.T) {
		cfg := testConfig(t)
		promptID := mustSeedPrompt(t, cfg, "sess-valid-prompt", "proj-valid-prompt")

		codes := stubbedExit(t)
		withArgs(t, "engram", "delete", "prompt", strconv.FormatInt(promptID, 10))
		stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

		if len(*codes) != 0 {
			t.Fatalf("expected no exit for valid prompt delete, got codes %v", *codes)
		}
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		if !strings.Contains(stdout, "deleted") {
			t.Fatalf("expected deletion confirmation, got: %q", stdout)
		}
		if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM user_prompts WHERE id = ?", promptID); got != 0 {
			t.Errorf("prompt must be deleted, got %d rows", got)
		}
	})

	t.Run("project soft delete", func(t *testing.T) {
		cfg := testConfig(t)
		mustSeedObservation(t, cfg, "sess-valid-proj-soft", "proj-valid-softdel", "decision", "valid", "content", "project")

		codes := stubbedExit(t)
		withArgs(t, "engram", "delete", "project", "proj-valid-softdel")
		stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

		if len(*codes) != 0 {
			t.Fatalf("expected no exit for valid project soft delete, got codes %v", *codes)
		}
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		if !strings.Contains(stdout, "soft-deleted") {
			t.Fatalf("expected soft-delete confirmation, got: %q", stdout)
		}
		if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE project = ? AND deleted_at IS NULL", "proj-valid-softdel"); got != 0 {
			t.Errorf("project observations must be soft-deleted, got %d live rows", got)
		}
		if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE project = ?", "proj-valid-softdel"); got != 1 {
			t.Errorf("soft delete must keep the row, got %d rows", got)
		}
	})

	t.Run("project hard delete", func(t *testing.T) {
		cfg := testConfig(t)
		mustSeedObservation(t, cfg, "sess-valid-proj-hard", "proj-valid-harddel", "decision", "valid", "content", "project")

		codes := stubbedExit(t)
		withArgs(t, "engram", "delete", "project", "proj-valid-harddel", "--hard")
		stdout, stderr := captureOutput(t, func() { cmdDelete(cfg) })

		if len(*codes) != 0 {
			t.Fatalf("expected no exit for valid project hard delete, got codes %v", *codes)
		}
		if stderr != "" {
			t.Fatalf("expected no stderr, got: %q", stderr)
		}
		if !strings.Contains(stdout, "hard-deleted") {
			t.Fatalf("expected hard-delete confirmation, got: %q", stdout)
		}
		if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE project = ?", "proj-valid-harddel"); got != 0 {
			t.Errorf("hard delete must remove the rows, got %d rows", got)
		}
	})
}
