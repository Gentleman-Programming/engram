package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestProjectsMergeSpaceNativeCLI(t *testing.T) {
	binary := buildCompatibilityBinary(t)
	cfg := testConfig(t)
	mustSeedObservation(t, cfg, "space-session", "plugin obsidian", "note", "original title", "original content", "project")
	home, temp := t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		name, target, mode string
		wantExit           int
		wantOutput         string
	}{
		{"refusal", "unrelated", "--dry-run", 1, "must be a distinct legacy"},
		{"preview", "plugin-obsidian", "--dry-run", 0, "No changes made"},
		{"apply", "plugin-obsidian", "--apply", 0, "observations 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "projects", "merge", "--from", "plugin obsidian", "--to", tc.target, tc.mode)
			cmd.Dir = home
			cmd.Env = []string{"HOME=" + home, "USERPROFILE=" + home, "TEMP=" + temp, "TMP=" + temp, "ENGRAM_DATA_DIR=" + cfg.DataDir, "ENGRAM_CLOUD_AUTOSYNC=0"}
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			code := 0
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					code = exit.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tc.wantExit || !strings.Contains(string(out), tc.wantOutput) {
				t.Fatalf("exit=%d output=%q, want exit=%d containing %q", code, out, tc.wantExit, tc.wantOutput)
			}
			sourceObs, sourceSessions, _ := projectRecordCounts(t, cfg, "plugin obsidian")
			targetObs, targetSessions, _ := projectRecordCounts(t, cfg, "plugin-obsidian")
			if tc.name == "apply" {
				if sourceObs != 0 || sourceSessions != 0 || targetObs != 1 || targetSessions != 1 {
					t.Fatal("apply did not move exactly the selected records")
				}
			} else if sourceObs != 1 || sourceSessions != 1 || targetObs != 0 || targetSessions != 0 {
				t.Fatal("preview or refusal changed project records")
			}
		})
	}
}
