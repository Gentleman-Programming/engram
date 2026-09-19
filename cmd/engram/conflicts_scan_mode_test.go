package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdConflictsScanModes(t *testing.T) {
	for _, tt := range []struct {
		name          string
		flags         []string
		wantRelations int
		wantReject    bool
	}{
		{name: "default dry run"},
		{name: "explicit dry run", flags: []string{"--dry-run"}},
		{name: "repeated dry run", flags: []string{"--dry-run", "--dry-run"}},
		{name: "apply", flags: []string{"--apply"}, wantRelations: 1},
		{name: "repeated apply", flags: []string{"--apply", "--apply"}, wantRelations: 1},
		{name: "dry run then apply", flags: []string{"--dry-run", "--apply"}, wantReject: true},
		{name: "apply then dry run", flags: []string{"--apply", "--dry-run"}, wantReject: true},
		{name: "repeated mixed modes", flags: []string{"--dry-run", "--apply", "--dry-run"}, wantReject: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t)
			mustSeedObservation(t, cfg, "scan-modes", "scan-modes", "bugfix", "Auth token missing", "Auth token is missing from requests", "project")
			mustSeedObservation(t, cfg, "scan-modes", "scan-modes", "bugfix", "Auth token duplicate", "Auth token appears twice in header", "project")
			withArgs(t, append([]string{"engram", "conflicts", "scan", "--project", "scan-modes"}, tt.flags...)...)
			stubExitWithPanic(t)

			stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdConflicts(cfg) })
			if tt.wantReject {
				if recovered != exitCode(1) || stderr != "error: --dry-run and --apply are mutually exclusive\n" || stdout != "" {
					t.Errorf("expected mode rejection: stdout=%q stderr=%q exit=%v", stdout, stderr, recovered)
				}
			} else {
				if recovered != nil || stderr != "" {
					t.Errorf("unexpected scan error: stderr=%q exit=%v", stderr, recovered)
				}
				if want := fmt.Sprintf("dry_run:          %v", tt.wantRelations == 0); !strings.Contains(stdout, want) {
					t.Errorf("output missing %q: %q", want, stdout)
				}
			}

			var relations int
			if err := openTestDB(t, cfg).QueryRow("SELECT count(*) FROM memory_relations").Scan(&relations); err != nil {
				t.Fatal(err)
			}
			if relations != tt.wantRelations {
				t.Errorf("relation count = %d, want %d", relations, tt.wantRelations)
			}
		})
	}
}

func TestCmdConflictsScanRejectsMixedModesBeforeStoreOpen(t *testing.T) {
	for _, flags := range [][]string{
		{"--dry-run", "--apply"},
		{"--apply", "--dry-run"},
		{"--dry-run", "--apply", "--semantic", "--yes"},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			cfg := testConfig(t)
			withArgs(t, append([]string{"engram", "conflicts", "scan", "--project", "scan-modes"}, flags...)...)
			stubExitWithPanic(t)

			stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdConflicts(cfg) })
			if recovered != exitCode(1) || stderr != "error: --dry-run and --apply are mutually exclusive\n" || stdout != "" {
				t.Errorf("expected mode rejection: stdout=%q stderr=%q exit=%v", stdout, stderr, recovered)
			}
			if _, err := os.Stat(filepath.Join(cfg.DataDir, "engram.db")); !os.IsNotExist(err) {
				t.Errorf("conflicting modes must not create a database; stat error = %v", err)
			}
		})
	}
}
