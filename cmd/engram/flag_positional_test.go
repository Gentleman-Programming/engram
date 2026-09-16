package main

import (
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

// ─── flag-shaped positional guard (issue #1205) ──────────────────────────────
//
// A flag-shaped token in a REQUIRED positional slot used to be stored
// verbatim ("engram save --project X" saved title "--project"). Pinned
// contract: reject it with a usage error naming the token, before any store
// access; a bare "--" separator makes following tokens positional even when
// dash-prefixed; trailing dangling flags at the END of a command stay
// lenient (pinned by TestCmdSearchAndSaveDanglingFlags).

// flagPositionalRejection describes one command argv whose required
// positional slot receives a flag-shaped token (#1205).
type flagPositionalRejection struct {
	name   string
	run    func(store.Config)
	usage  string
	token  string
	argv   []string
	seed   func(t *testing.T) store.Config
	intact []rowCount
}

// flagPositionalRejections covers the string-positional verbs that could
// silently corrupt data; numeric-id verbs already fail loudly on ParseInt.
var flagPositionalRejections = []flagPositionalRejection{
	{
		name:  "save title slot",
		run:   cmdSave,
		usage: "engram save <title> <content> [--type TYPE] [--project PROJECT] [--scope SCOPE] [--topic TOPIC_KEY]",
		token: "--project",
		argv:  []string{"engram", "save", "--project", "X"},
		seed: func(t *testing.T) store.Config {
			cfg := testConfig(t)
			mustSeedObservation(t, cfg, "sess-1205-title", "proj-1205-title", "note", "seeded", "must survive", "project")
			return cfg
		},
		intact: []rowCount{
			{"SELECT COUNT(*) FROM observations", nil, 1},
			{"SELECT COUNT(*) FROM observations WHERE title = ?", "--project", 0},
		},
	},
	{
		name:  "save content slot",
		run:   cmdSave,
		usage: "engram save <title> <content> [--type TYPE] [--project PROJECT] [--scope SCOPE] [--topic TOPIC_KEY]",
		token: "--project",
		argv:  []string{"engram", "save", "real-title", "--project"},
		seed: func(t *testing.T) store.Config {
			cfg := testConfig(t)
			mustSeedObservation(t, cfg, "sess-1205-content", "proj-1205-content", "note", "seeded", "must survive", "project")
			return cfg
		},
		intact: []rowCount{
			{"SELECT COUNT(*) FROM observations", nil, 1},
			{"SELECT COUNT(*) FROM observations WHERE title = ?", "real-title", 0},
		},
	},
	{
		name:  "delete project name slot",
		run:   cmdDelete,
		usage: "engram delete project <name> [--hard]",
		token: "--hard",
		argv:  []string{"engram", "delete", "project", "--hard"},
		seed: func(t *testing.T) store.Config {
			cfg := testConfig(t)
			mustSeedObservation(t, cfg, "sess-1205-delete", "proj-1205-delete", "note", "seeded", "must survive", "project")
			return cfg
		},
		intact: []rowCount{
			{"SELECT COUNT(*) FROM observations WHERE project = ? AND deleted_at IS NULL", "proj-1205-delete", 1},
			{"SELECT COUNT(*) FROM observations WHERE project = ?", "proj-1205-delete", 1},
		},
	},
	{
		name:  "protocol-mode slug slot",
		run:   cmdProtocolMode,
		usage: "engram protocol-mode <slug>",
		token: "--flag",
		argv:  []string{"engram", "protocol-mode", "--flag"},
		seed: func(t *testing.T) store.Config {
			return testConfig(t)
		},
	},
}

// TestCmdRejectsFlagShapedPositionals proves the guard rejects a flag-shaped
// token in any required positional slot with a named usage error and a
// nonzero exit before the store is touched.
func TestCmdRejectsFlagShapedPositionals(t *testing.T) {
	for _, tt := range flagPositionalRejections {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.seed(t)

			codes := stubbedExit(t)
			withArgs(t, tt.argv...)
			stdout, stderr := captureOutput(t, func() { tt.run(cfg) })

			if len(*codes) == 0 || (*codes)[0] == 0 {
				t.Fatalf("expected nonzero exit for flag-shaped positional %q; command ran anyway (stdout=%q stderr=%q)", tt.token, stdout, stderr)
			}
			if !strings.Contains(stderr, tt.token) {
				t.Errorf("expected stderr to name offending token %q, got: %q", tt.token, stderr)
			}
			if !strings.Contains(stderr, "usage: "+tt.usage) {
				t.Errorf("expected stderr to show usage %q, got: %q", "usage: "+tt.usage, stderr)
			}
			if strings.Contains(stdout, "Memory saved") || strings.Contains(stdout, "deleted") {
				t.Errorf("expected no success output on stdout, got: %q", stdout)
			}
			for _, c := range tt.intact {
				if got := mustQueryCount(t, cfg, c.query, c.arg); got != c.want {
					t.Errorf("store must stay untouched after rejection (%s): got %d rows, want %d", c.query, got, c.want)
				}
			}
		})
	}
}

// TestCmdSaveSeparatorMakesDashPrefixedPositionalLegal proves a bare "--"
// makes the following tokens positional even when dash-prefixed.
func TestCmdSaveSeparatorMakesDashPrefixedPositionalLegal(t *testing.T) {
	cfg := testConfig(t)

	codes := stubbedExit(t)
	withArgs(t, "engram", "save", "--", "--weird-title", "content")
	stdout, stderr := captureOutput(t, func() { cmdSave(cfg) })

	if len(*codes) != 0 {
		t.Fatalf("separator save exited: %v (stderr %q)", *codes, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "Memory saved") {
		t.Fatalf("expected saved memory, got stdout %q", stdout)
	}
	if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE title = ? AND content = ?", "--weird-title", "content"); got != 1 {
		t.Fatalf("dash-prefixed title must be stored verbatim after --: got %d rows, want 1", got)
	}
}

// TestCmdStringPositionalVerbsKeepLenientContract proves the guard did not
// over-tighten: plain operands still save, trailing dangling flags stay
// lenient, and a normal protocol-mode slug still resolves.
func TestCmdStringPositionalVerbsKeepLenientContract(t *testing.T) {
	t.Run("normal save", func(t *testing.T) {
		cfg := testConfig(t)

		codes := stubbedExit(t)
		withArgs(t, "engram", "save", "plain-title", "plain-content")
		stdout, stderr := captureOutput(t, func() { cmdSave(cfg) })

		if len(*codes) != 0 || stderr != "" || !strings.Contains(stdout, "Memory saved") {
			t.Fatalf("normal save broken: exit=%v stdout=%q stderr=%q", *codes, stdout, stderr)
		}
		if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE title = ? AND type = ?", "plain-title", "manual"); got != 1 {
			t.Fatalf("normal save stored wrong rows: got %d, want 1", got)
		}
	})

	t.Run("trailing dangling flag stays lenient", func(t *testing.T) {
		cfg := testConfig(t)

		codes := stubbedExit(t)
		withArgs(t, "engram", "save", "dangling-title", "dangling-content", "--type")
		stdout, stderr := captureOutput(t, func() { cmdSave(cfg) })

		if len(*codes) != 0 || stderr != "" || !strings.Contains(stdout, "Memory saved") {
			t.Fatalf("dangling trailing flag no longer lenient: exit=%v stdout=%q stderr=%q", *codes, stdout, stderr)
		}
		if got := mustQueryCount(t, cfg, "SELECT COUNT(*) FROM observations WHERE title = ? AND type = ?", "dangling-title", "manual"); got != 1 {
			t.Fatalf("dangling flag save stored wrong rows: got %d, want 1", got)
		}
	})

	t.Run("protocol-mode normal slug", func(t *testing.T) {
		cfg := testConfig(t)

		codes := stubbedExit(t)
		withArgs(t, "engram", "protocol-mode", "claude-code")
		stdout, stderr := captureOutput(t, func() { cmdProtocolMode(cfg) })

		if len(*codes) != 0 || stderr != "" {
			t.Fatalf("normal slug rejected: exit=%v stderr=%q", *codes, stderr)
		}
		if strings.TrimSpace(stdout) != "full" {
			t.Fatalf("normal slug output = %q, want full", stdout)
		}
	})
}
