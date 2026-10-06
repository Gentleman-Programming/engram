package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	_ "modernc.org/sqlite"
)

func TestDoctorDirectoryBackfillCaseVariant(t *testing.T) {
	for _, scope := range []string{"all", "Mixed_Case_Project", "mixed_case_project"} {
		t.Run(scope, func(t *testing.T) {
			cfg := testConfig(t)
			initDoctorStore(t, cfg)
			db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Errorf("close doctor backfill database: %v", err)
				}
			})
			// Legacy spellings cannot be created through current session creation.
			for _, id := range []string{"case-a", "case-b"} {
				if _, err := db.Exec(`INSERT INTO sessions(id,project,ownership_mode,directory) VALUES (?, 'Mixed_Case_Project', 'shared', '/work/case')`, id); err != nil {
					t.Fatal(err)
				}
			}
			for i, id := range []string{"case-a", "case-a", "case-b", "case-a"} {
				payload := fmt.Sprintf(`{"id":%q,"project":"Mixed_Case_Project","directory":""}`, id)
				if _, err := db.Exec(`INSERT INTO sync_mutations(seq,target_key,entity,entity_key,op,payload,source,project) VALUES (?, 'cloud', 'session', ?, 'upsert', ?, 'local', 'Mixed_Case_Project')`, i+1, id, payload); err != nil {
					t.Fatal(err)
				}
			}
			counts := func() []int {
				t.Helper()
				var result []int
				for _, table := range []string{"sessions", "observations", "user_prompts", "sync_mutations"} {
					var n int
					if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
						t.Fatal(err)
					}
					result = append(result, n)
				}
				return result
			}
			before := counts()
			// Compare snapshots across opposite scan orders to detect unordered aggregation.
			if _, err := db.Exec(`PRAGMA reverse_unordered_selects = ON`); err != nil {
				t.Fatal(err)
			}
			var original string
			if err := db.QueryRow(`SELECT group_concat(payload, '|') FROM (SELECT payload FROM sync_mutations ORDER BY seq)`).Scan(&original); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`PRAGMA reverse_unordered_selects = OFF`); err != nil {
				t.Fatal(err)
			}
			for step, mode := range []string{"--dry-run", "--apply", "--apply"} {
				args := []string{"engram", "doctor", "repair", "--check", "sync_mutation_required_fields", mode}
				if scope != "all" {
					args = append(args, "--project", scope)
				}
				withArgs(t, args...)
				stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
				if stderr != "" {
					t.Fatalf("%s stderr=%q", mode, stderr)
				}
				report := decodeRepairPlan(t, stdout)
				actions := report["directory_repairs"].([]any)
				var repaired int
				if err := db.QueryRow(`SELECT count(*) FROM sync_mutations WHERE json_extract(payload, '$.directory')='/work/case'`).Scan(&repaired); err != nil {
					t.Fatal(err)
				}
				wantActions := 4
				if step == 2 {
					wantActions = 0
				}
				if report["applied"] != (step == 1) {
					t.Fatalf("%s applied=%v", mode, report["applied"])
				}
				if len(actions) != wantActions {
					t.Fatalf("%s actions=%v, want %d", mode, actions, wantActions)
				}
				for i, action := range actions {
					want := map[string]any{"seq": float64(i + 1), "project": "mixed_case_project", "entity_key": []string{"case-a", "case-a", "case-b", "case-a"}[i]}
					if !reflect.DeepEqual(action, want) {
						t.Fatalf("action=%v, want=%v", action, want)
					}
				}
				if !reflect.DeepEqual(counts(), before) {
					t.Fatal("row counts changed")
				}
				if mode == "--dry-run" {
					var after string
					if err := db.QueryRow(`SELECT group_concat(payload, '|') FROM (SELECT payload FROM sync_mutations ORDER BY seq)`).Scan(&after); err != nil {
						t.Fatal(err)
					}
					if after != original || repaired != 0 || report["applied"] != false {
						t.Fatalf("dry-run changed state: %v", report)
					}
				} else {
					if repaired != 4 {
						t.Fatalf("repaired=%d, want 4", repaired)
					}
					var valid int
					if err := db.QueryRow(`SELECT count(*) FROM sync_mutations WHERE project='mixed_case_project' AND disposition='pending' AND acked_at IS NULL AND source='local' AND target_key='cloud'`).Scan(&valid); err != nil {
						t.Fatal(err)
					}
					if valid != 4 {
						t.Fatalf("journal metadata/canonical project changed unexpectedly: %d", valid)
					}
				}
			}
		})
	}
}

func TestDoctorDirectoryBackfillWithoutEnrollment(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("directory-backfill", "engram", "/work/engram"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("other-project-session", "other", "/work/other"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("id-only-session", "engram", "/work/engram"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	seedDoctorPendingMutation(t, cfg, "engram", store.SyncEntitySession, "directory-backfill", store.SyncOpUpsert, `{"id":"directory-backfill","project":"engram"}`)
	seedDoctorPendingMutation(t, cfg, "other", store.SyncEntitySession, "other-project-session", store.SyncOpUpsert, `{"id":"other-project-session","project":"other"}`)
	seedDoctorPendingMutation(t, cfg, "engram", store.SyncEntitySession, "id-only-session", store.SyncOpUpsert, `{"project":"engram","directory":"/work/engram"}`)
	seedDoctorPendingMutation(t, cfg, "engram", store.SyncEntitySession, "other-project-session", store.SyncOpUpsert, `{"id":"other-project-session","project":"engram"}`)
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "engram.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close doctor backfill database: %v", err)
		}
	})
	var seq int64
	var occurredAt, source, target string
	if err := db.QueryRow(`SELECT seq, occurred_at, source, target_key FROM sync_mutations WHERE entity_key='directory-backfill' ORDER BY seq DESC LIMIT 1`).Scan(&seq, &occurredAt, &source, &target); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project,disposition,disposition_reason) VALUES ('cloud','session','quarantined-directory','upsert',?, 'local','engram','quarantined','existing')`, `{"id":"quarantined-directory"}`); err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		t.Helper()
		var payload, disposition string
		var reason sql.NullString
		if err := db.QueryRow(`SELECT payload,disposition,disposition_reason FROM sync_mutations WHERE seq=?`, seq).Scan(&payload, &disposition, &reason); err != nil {
			t.Fatal(err)
		}
		if payload != want || disposition != "pending" || reason.Valid {
			t.Fatalf("payload=%s disposition=%s reason=%v", payload, disposition, reason)
		}
		var quarantined string
		if err := db.QueryRow(`SELECT payload FROM sync_mutations WHERE entity_key='quarantined-directory'`).Scan(&quarantined); err != nil {
			t.Fatal(err)
		}
		if quarantined != `{"id":"quarantined-directory"}` {
			t.Fatalf("quarantined payload=%s", quarantined)
		}
		var originalTime, originalSource, originalTarget, otherPayload, idOnlyPayload, misprojectedPayload, quarantineReason string
		if err := db.QueryRow(`SELECT occurred_at,source,target_key FROM sync_mutations WHERE seq=?`, seq).Scan(&originalTime, &originalSource, &originalTarget); err != nil {
			t.Fatal(err)
		}
		if originalTime != occurredAt || originalSource != source || originalTarget != target {
			t.Fatalf("journal metadata changed: %q %q %q", originalTime, originalSource, originalTarget)
		}
		if err := db.QueryRow(`SELECT payload FROM sync_mutations WHERE entity_key='other-project-session'`).Scan(&otherPayload); err != nil {
			t.Fatal(err)
		}
		if otherPayload != `{"id":"other-project-session","project":"other"}` {
			t.Fatalf("other project changed: %s", otherPayload)
		}
		if err := db.QueryRow(`SELECT payload FROM sync_mutations WHERE entity_key='id-only-session'`).Scan(&idOnlyPayload); err != nil {
			t.Fatal(err)
		}
		if idOnlyPayload != `{"project":"engram","directory":"/work/engram"}` {
			t.Fatalf("id-only payload changed: %s", idOnlyPayload)
		}
		if err := db.QueryRow(`SELECT payload FROM sync_mutations WHERE entity_key='other-project-session' AND project='engram'`).Scan(&misprojectedPayload); err != nil {
			t.Fatal(err)
		}
		if misprojectedPayload != `{"id":"other-project-session","project":"engram"}` {
			t.Fatalf("cross-project directory leaked: %s", misprojectedPayload)
		}
		if err := db.QueryRow(`SELECT disposition_reason FROM sync_mutations WHERE entity_key='quarantined-directory'`).Scan(&quarantineReason); err != nil {
			t.Fatal(err)
		}
		if quarantineReason != "existing" {
			t.Fatalf("quarantine reason=%s", quarantineReason)
		}
	}
	original := `{"id":"directory-backfill","project":"engram"}`
	for i, mode := range []string{"--plan", "--dry-run", "--apply", "--apply"} {
		withArgs(t, "engram", "doctor", "repair", "--project", "engram", "--check", "sync_mutation_required_fields", mode)
		out, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
		if stderr != "" {
			t.Fatalf("%s stderr=%s", mode, stderr)
		}
		report := decodeRepairPlan(t, out)
		actions := report["directory_repairs"].([]any)
		if i == 3 {
			if len(actions) != 0 || report["applied"] != false {
				t.Fatalf("second apply=%v", report)
			}
			check(`{"id":"directory-backfill","project":"engram","directory":"/work/engram"}`)
			continue
		}
		if len(actions) != 1 || actions[0].(map[string]any)["seq"] != float64(seq) {
			t.Fatalf("%s report=%v", mode, report)
		}
		if mode != "--apply" {
			check(original)
		} else {
			if report["applied"] != true {
				t.Fatalf("first apply=%v", report)
			}
			check(`{"id":"directory-backfill","project":"engram","directory":"/work/engram"}`)
		}
	}
}
