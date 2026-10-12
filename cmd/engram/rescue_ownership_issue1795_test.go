package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestCmdProjectsRescueOwnershipClassifiesBeforeNextCLIInvocation(t *testing.T) {
	for _, tc := range []struct {
		id, mode string
	}{
		{"manual-save", "shared"}, // Exact session ID from issue #1795.
		{"manual-save-target", "project_owned"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			cfg := testConfig(t)
			seedLegacyNullableSession(t, cfg, tc.id)
			raw, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(`UPDATE sessions SET project = '' WHERE id = ?`, tc.id); err != nil {
				t.Fatal(err)
			}
			oldExit := exitFunc
			var exits []int
			exitFunc = func(code int) { exits = append(exits, code) }
			t.Cleanup(func() { exitFunc = oldExit })
			want := []string{
				"Rescued ownership into \"target\": 1 sessions, 0 observations, 0 prompts\nEverything selected now belongs to the target project.\n",
				"Rescued ownership into \"target\": 0 sessions, 0 observations, 0 prompts\nEverything selected now belongs to the target project.\n",
			}
			for i := range want {
				withArgs(t, "engram", "projects", "rescue-ownership", "--project", "target", "--session", tc.id)
				stdout, stderr := captureOutput(t, func() { cmdProjects(cfg) })
				if stdout != want[i] || stderr != "" || len(exits) != 0 {
					t.Errorf("call %d: stdout=%q stderr=%q exit calls=%v, want stdout=%q and success", i+1, stdout, stderr, exits, want[i])
				}
				// Read raw SQLite, not store.New: opening another store would run
				// migration and conceal the original delayed classification.
				var project string
				var mode sql.NullString
				if err := raw.QueryRow(`SELECT project, ownership_mode FROM sessions WHERE id = ?`, tc.id).Scan(&project, &mode); err != nil {
					t.Fatal(err)
				}
				t.Logf("call %d: stdout=%q project=%q ownership_mode=%#v", i+1, stdout, project, mode)
				if project != "target" || !mode.Valid || mode.String != tc.mode {
					t.Errorf("call %d persisted project=%q mode=%#v, want target/%s", i+1, project, mode, tc.mode)
				}
			}
		})
	}
}
