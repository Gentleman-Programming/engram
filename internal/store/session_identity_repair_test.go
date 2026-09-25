package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func seedSessionIdentityRepair(t *testing.T, s *Store, enrolled bool) {
	t.Helper()
	if _, err := s.DB().Exec(`INSERT INTO sessions(id,project,directory) VALUES ('','alpha','/work')`); err != nil {
		t.Fatal(err)
	}
	if enrolled {
		if _, err := s.DB().Exec(`INSERT INTO sync_enrolled_projects(project) VALUES ('alpha')`); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO observations(sync_id,session_id,type,title,content,project) VALUES ('obs-1','','note','title','body','alpha')`,
		`INSERT INTO user_prompts(sync_id,session_id,content,project) VALUES ('prompt-1','','hello','alpha')`,
		`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','session','','upsert','{"id":"","project":"alpha","directory":"/work"}','local','alpha')`,
		`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','observation','obs-1','upsert','{"sync_id":"obs-1","session_id":"","project":"alpha"}','local','alpha')`,
		`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','prompt','prompt-1','upsert','{"sync_id":"prompt-1","session_id":"","project":"alpha"}','local','alpha')`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionIdentityRepairPreservesJournalAndReferences(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		t.Run(map[bool]string{false: "unenrolled", true: "enrolled"}[enrolled], func(t *testing.T) {
			s := newTestStore(t)
			seedSessionIdentityRepair(t, s, enrolled)
			plan, err := s.PlanSessionIdentityRepair("", " opaque ")
			if err != nil {
				t.Fatal(err)
			}
			if plan.Observations != 1 || plan.Prompts != 1 || plan.RetiredMutations != 3 || plan.Enrolled != enrolled {
				t.Fatalf("plan: %+v", plan)
			}
			if scalarInt(t, s, `SELECT count(*) FROM sessions WHERE id=''`) != 1 {
				t.Fatal("plan wrote source")
			}
			result, err := s.ApplySessionIdentityRepair(plan)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(result.BackupPath); err != nil {
				t.Fatal(err)
			}
			if scalarInt(t, s, `SELECT count(*) FROM sessions WHERE id=' opaque '`) != 1 || scalarInt(t, s, `SELECT count(*) FROM sessions WHERE id=''`) != 0 {
				t.Fatal("identity not remapped verbatim")
			}
			for _, table := range []string{"observations", "user_prompts"} {
				if scalarInt(t, s, `SELECT count(*) FROM `+table+` WHERE session_id=' opaque '`) != 1 {
					t.Fatal(table, "not remapped")
				}
			}
			if scalarInt(t, s, `SELECT count(*) FROM sync_mutations WHERE disposition='superseded' AND disposition_reason='session_identity_migrated'`) != 3 {
				t.Fatal("history not retired")
			}
			if got := scalarString(t, s, `SELECT payload FROM sync_mutations WHERE entity='prompt' AND disposition_reason='session_identity_migrated'`); got != `{"sync_id":"prompt-1","session_id":"","project":"alpha"}` {
				t.Fatalf("original linked journal payload changed: %s", got)
			}
			want := 0
			if enrolled {
				want = 3
			}
			if scalarInt(t, s, `SELECT count(*) FROM sync_mutations WHERE disposition='pending' AND project='alpha'`) != want {
				t.Fatalf("pending mutations want %d", want)
			}
		})
	}
}

// Seven observations, four prompts and one session reproduce the historical
// linked journal shape; every original sequence must remain auditable.
func TestSessionIdentityRepairTwelveLinkedJournalRows(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB().Exec(`INSERT INTO sessions(id,project,directory) VALUES ('','alpha','/work')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO sync_enrolled_projects(project) VALUES ('alpha')`); err != nil {
		t.Fatal(err)
	}
	insertJournal := func(entity, key, payload string) {
		t.Helper()
		if _, err := s.DB().Exec(`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud',?,?,'upsert',?,'local','alpha')`, entity, key, payload); err != nil {
			t.Fatal(err)
		}
	}
	insertJournal(SyncEntitySession, "", `{"id":"","project":"alpha","directory":"/work"}`)
	for i := 0; i < 7; i++ {
		key := fmt.Sprintf("obs-%d", i)
		if _, err := s.DB().Exec(`INSERT INTO observations(sync_id,session_id,type,title,content,project) VALUES (?,'','note','title','body','alpha')`, key); err != nil {
			t.Fatal(err)
		}
		insertJournal(SyncEntityObservation, key, fmt.Sprintf(`{"sync_id":%q,"session_id":"","project":"alpha","scope":"project","type":"note","title":"title","content":"body"}`, key))
	}
	for i := 0; i < 4; i++ {
		key := fmt.Sprintf("prompt-%d", i)
		if _, err := s.DB().Exec(`INSERT INTO user_prompts(sync_id,session_id,content,project) VALUES (?,'','hello','alpha')`, key); err != nil {
			t.Fatal(err)
		}
		insertJournal(SyncEntityPrompt, key, fmt.Sprintf(`{"sync_id":%q,"session_id":"","project":"alpha","content":"hello"}`, key))
	}
	plan, err := s.PlanSessionIdentityRepair("", "canonical")
	if err != nil || plan.Observations != 7 || plan.Prompts != 4 || plan.RetiredMutations != 12 {
		t.Fatalf("plan %+v: %v", plan, err)
	}
	result, err := s.ApplySessionIdentityRepair(plan)
	if err != nil || result.PublishedMutations != 12 {
		t.Fatalf("apply %+v: %v", result, err)
	}
	if got := scalarInt(t, s, `SELECT count(*) FROM sync_mutations WHERE seq <= 12 AND disposition_reason='session_identity_migrated'`); got != 12 {
		t.Fatalf("retired original sequences: %d", got)
	}
	if got := scalarInt(t, s, `SELECT count(*) FROM sync_mutations WHERE seq > 12 AND disposition='pending' AND json_extract(payload,'$.session_id')='canonical'`); got != 11 {
		t.Fatalf("republished children: %d", got)
	}
}

func TestSessionIdentityRepairRejectsUnplannedAndChangedJournal(t *testing.T) {
	s := newTestStore(t)
	seedSessionIdentityRepair(t, s, true)
	if _, err := s.ApplySessionIdentityRepair(SessionIdentityRepairPlan{SourceID: "", ReplacementID: "canonical"}); err == nil {
		t.Fatal("accepted plan without fingerprint")
	}
	plan, err := s.PlanSessionIdentityRepair("", "canonical")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE sync_mutations SET payload='{"id":"","project":"alpha","directory":"/changed"}' WHERE entity='session'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySessionIdentityRepair(plan); err == nil || !strings.Contains(err.Error(), "evidence changed since plan") {
		t.Fatalf("linked journal change not rejected as stale: %v", err)
	}
}

func TestSessionIdentityRepairIgnoresUnrelatedActivity(t *testing.T) {
	s := newTestStore(t)
	seedSessionIdentityRepair(t, s, true)
	plan, err := s.PlanSessionIdentityRepair("", "canonical")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','session','other','upsert','{"id":"other","project":"beta"}','local','beta')`,
		`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','observation','other','upsert','{"session_id":"other-session","project":"beta"}','local','beta')`,
		`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','prompt','other','upsert','{"session_id":"other-session"}','local','beta')`,
		`INSERT INTO sync_apply_deferred(sync_id,entity,payload,entity_key,project) VALUES ('other','relation','{"sync_id":"other"}','other','alpha')`,
		`INSERT INTO sync_apply_deferred(sync_id,entity,payload,entity_key,project) VALUES ('other-project','relation','{"sync_id":"other-project","session_id":"other-session"}','other-project','beta')`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ApplySessionIdentityRepair(plan); err != nil {
		t.Fatalf("unrelated activity rejected: %v", err)
	}
	if got := scalarInt(t, s, `SELECT count(*) FROM sync_mutations WHERE entity_key='other' AND disposition='pending'`); got != 3 {
		t.Fatalf("unrelated journal changed: %d", got)
	}
	if got := scalarInt(t, s, `SELECT count(*) FROM sync_apply_deferred WHERE sync_id IN ('other','other-project')`); got != 2 {
		t.Fatalf("unrelated deferred changed: %d", got)
	}
}

func TestSessionIdentityRepairCountsOnlyCommittedRetry(t *testing.T) {
	s := newTestStore(t)
	seedSessionIdentityRepair(t, s, true)
	plan, err := s.PlanSessionIdentityRepair("", "canonical")
	if err != nil {
		t.Fatal(err)
	}
	originalCommit := s.hooks.commit
	attempts := 0
	s.hooks.commit = func(tx *sql.Tx) error {
		attempts++
		if attempts == 1 {
			var published int
			if err := tx.QueryRow(`SELECT count(*) FROM sync_mutations WHERE disposition='pending' AND entity_key='canonical'`).Scan(&published); err != nil {
				t.Fatal(err)
			}
			if published != 1 {
				t.Fatalf("first attempt did not publish session: %d", published)
			}
			return errors.New("database is locked")
		}
		return originalCommit(tx)
	}
	t.Cleanup(func() { s.hooks.commit = originalCommit })
	result, err := s.ApplySessionIdentityRepair(plan)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || result.PublishedMutations != 3 {
		t.Fatalf("attempts=%d result=%+v", attempts, result)
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatal(err)
	}
	if got := scalarInt(t, s, `SELECT count(*) FROM sync_mutations WHERE disposition='pending' AND project='alpha'`); got != int(result.PublishedMutations) {
		t.Fatalf("persisted publications=%d result=%d", got, result.PublishedMutations)
	}
}

func TestSessionIdentityRepairRejectsAndRollsBack(t *testing.T) {
	t.Run("whitespace", func(t *testing.T) {
		s := newTestStore(t)
		seedSessionIdentityRepair(t, s, false)
		if _, err := s.PlanSessionIdentityRepair("", " \t "); err == nil {
			t.Fatal("accepted blank replacement")
		}
	})
	t.Run("collision", func(t *testing.T) {
		s := newTestStore(t)
		seedSessionIdentityRepair(t, s, false)
		if _, err := s.DB().Exec(`INSERT INTO sessions(id,project,directory) VALUES ('taken','alpha','/work')`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PlanSessionIdentityRepair("", "taken"); err == nil {
			t.Fatal("accepted collision")
		}
	})
	for _, scenario := range []struct{ name, query string }{
		{"acked", `UPDATE sync_mutations SET acked_at=datetime('now') WHERE entity='prompt'`},
		{"remote", `UPDATE sync_mutations SET source='remote' WHERE entity='prompt'`},
		{"delete", `UPDATE sync_mutations SET op='delete' WHERE entity='prompt'`},
		{"tombstone", `INSERT INTO prompt_tombstones(sync_id,session_id,project) VALUES ('prompt-1','','alpha')`},
		{"active delete tombstone", `INSERT INTO sync_delete_tombstones(entity,entity_key,session_id,project) VALUES ('observation','obs-1','','alpha')`},
		{"deferred", `INSERT INTO sync_apply_deferred(sync_id,entity,payload,entity_key,project) VALUES ('deferred-1','prompt','{}','prompt-1','alpha')`},
		{"string encoded deferred relation", `INSERT INTO sync_apply_deferred(sync_id,entity,payload,entity_key,project) VALUES ('deferred-relation','relation','"{\"session_id\":\"\",\"sync_id\":\"relation-1\"}"','relation-1','alpha')`},
		{"direct deferred relation", `INSERT INTO sync_apply_deferred(sync_id,entity,payload,entity_key,project) VALUES ('direct-relation','relation','{"session_id":"","sync_id":"relation-2"}','relation-2','alpha')`},
		{"unscoped string encoded relation", `INSERT INTO sync_apply_deferred(sync_id,entity,payload,entity_key,project) VALUES ('unscoped','relation','"{\"session_id\":\"\",\"sync_id\":\"relation-3\"}"','relation-3','')`},
		{"malformed same-project deferred", `INSERT INTO sync_apply_deferred(sync_id,entity,payload,entity_key,project) VALUES ('malformed','relation','not-json','other','alpha')`},
		{"mismatched payload", `UPDATE sync_mutations SET payload='{"sync_id":"other","session_id":"","project":"alpha"}' WHERE entity='prompt'`},
		{"relation reference", `INSERT INTO memory_relations(sync_id,session_id) VALUES ('relation-1','')`},
		{"linked relation mutation", `INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','relation','relation-1','upsert','{"session_id":""}','local','alpha')`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s := newTestStore(t)
			seedSessionIdentityRepair(t, s, true)
			if _, err := s.DB().Exec(scenario.query); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PlanSessionIdentityRepair("", "new-id"); err == nil {
				t.Fatal("accepted unsafe evidence")
			}
		})
	}
	for _, enrolled := range []bool{false, true} {
		t.Run(fmt.Sprintf("refresh lifecycle enrolled=%t", enrolled), func(t *testing.T) {
			s := newTestStore(t)
			seedSessionIdentityRepair(t, s, enrolled)
			projectTarget := syncTargetKeyForProject("alpha")
			if _, err := s.DB().Exec(`INSERT INTO sync_state(target_key,lifecycle) VALUES (?,?)`, projectTarget, SyncLifecycleHealthy); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`UPDATE sync_state SET lifecycle=? WHERE target_key=?`, SyncLifecyclePending, DefaultSyncTargetKey); err != nil {
				t.Fatal(err)
			}
			plan, err := s.PlanSessionIdentityRepair("", "canonical")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ApplySessionIdentityRepair(plan); err != nil {
				t.Fatal(err)
			}
			want := SyncLifecycleHealthy
			if enrolled {
				want = SyncLifecyclePending
			}
			for _, target := range []string{DefaultSyncTargetKey, projectTarget} {
				if got := scalarString(t, s, `SELECT lifecycle FROM sync_state WHERE target_key=?`, target); got != want {
					t.Errorf("%s lifecycle=%q, want %q", target, got, want)
				}
			}
		})
	}
	t.Run("unrelated deferred allowed", func(t *testing.T) {
		s := newTestStore(t)
		seedSessionIdentityRepair(t, s, true)
		if _, err := s.DB().Exec(`INSERT INTO sync_apply_deferred(sync_id,entity,payload,entity_key,project) VALUES ('unrelated','prompt','{}','other','beta')`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`INSERT INTO sync_apply_deferred(sync_id,entity,payload,entity_key,project) VALUES ('same-project-unrelated','relation','{"sync_id":"other"}','other','alpha')`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PlanSessionIdentityRepair("", "canonical"); err != nil {
			t.Fatalf("unrelated deferred blocked: %v", err)
		}
	})
	t.Run("retire count mismatch rolls back", func(t *testing.T) {
		s := newTestStore(t)
		seedSessionIdentityRepair(t, s, true)
		plan, err := s.PlanSessionIdentityRepair("", "canonical")
		if err != nil {
			t.Fatal(err)
		}
		original := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "UPDATE sync_mutations SET disposition=") {
				return db.Exec(`UPDATE sync_mutations SET disposition='pending' WHERE seq=-1`)
			}
			return db.Exec(query, args...)
		}
		defer func() { s.hooks.exec = original }()
		if _, err := s.ApplySessionIdentityRepair(plan); err == nil {
			t.Fatal("accepted missing retirement")
		}
		if got := scalarInt(t, s, `SELECT count(*) FROM sessions WHERE id=''`); got != 1 {
			t.Fatalf("source after rollback: %d", got)
		}
	})
	t.Run("missing cloud state rolls back", func(t *testing.T) {
		s := newTestStore(t)
		seedSessionIdentityRepair(t, s, true)
		plan, err := s.PlanSessionIdentityRepair("", "canonical")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`DELETE FROM sync_state WHERE target_key='cloud'`); err == nil {
			t.Fatal("expected journal foreign key to prevent deleting cloud state")
		}
		// Inject a zero-row update to prove the publication cursor is checked.
		original := s.hooks.exec
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			if strings.Contains(query, "UPDATE sync_state SET lifecycle") {
				return db.Exec(`UPDATE sync_state SET lifecycle='pending' WHERE target_key='absent'`)
			}
			return db.Exec(query, args...)
		}
		defer func() { s.hooks.exec = original }()
		if _, err := s.ApplySessionIdentityRepair(plan); err == nil {
			t.Fatal("accepted zero-row sync state update")
		}
		if got := scalarInt(t, s, `SELECT count(*) FROM sessions WHERE id=''`); got != 1 {
			t.Fatalf("source after rollback: %d", got)
		}
		if got := scalarInt(t, s, `SELECT count(*) FROM sync_mutations WHERE disposition_reason='session_identity_migrated'`); got != 0 {
			t.Fatalf("retired rows after rollback: %d", got)
		}
	})
	t.Run("stale", func(t *testing.T) {
		s := newTestStore(t)
		seedSessionIdentityRepair(t, s, true)
		plan, err := s.PlanSessionIdentityRepair("", "new-id")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`UPDATE observations SET title='changed title' WHERE sync_id='obs-1'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApplySessionIdentityRepair(plan); err == nil || !strings.Contains(err.Error(), "evidence changed since plan") {
			t.Fatalf("expected stale evidence error, got %v", err)
		}
		if scalarInt(t, s, `SELECT count(*) FROM sessions WHERE id=''`) != 1 || scalarInt(t, s, `SELECT count(*) FROM sessions WHERE id='new-id'`) != 0 {
			t.Fatal("changed source")
		}
		if got := scalarString(t, s, `SELECT title FROM observations WHERE sync_id='obs-1'`); got != "changed title" {
			t.Fatalf("source title changed: %q", got)
		}
	})
	t.Run("write failure", func(t *testing.T) {
		s := newTestStore(t)
		seedSessionIdentityRepair(t, s, true)
		plan, err := s.PlanSessionIdentityRepair("", "new-id")
		if err != nil {
			t.Fatal(err)
		}
		original := s.hooks.exec
		sentinel := errors.New("injected write failure")
		s.hooks.exec = func(db execer, q string, args ...any) (sql.Result, error) {
			if strings.Contains(q, "UPDATE user_prompts SET session_id") {
				return nil, sentinel
			}
			return db.Exec(q, args...)
		}
		defer func() { s.hooks.exec = original }()
		result, err := s.ApplySessionIdentityRepair(plan)
		if !errors.Is(err, sentinel) {
			t.Fatalf("error %v", err)
		}
		if _, err := os.Stat(result.BackupPath); err != nil {
			t.Fatal(err)
		}
		backup, err := sql.Open("sqlite", result.BackupPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := backup.Close(); err != nil {
				t.Errorf("close backup: %v", err)
			}
		}()
		var originalSession, originalJournal int
		if err := backup.QueryRow(`SELECT count(*) FROM sessions WHERE id=''`).Scan(&originalSession); err != nil {
			t.Fatal(err)
		}
		if err := backup.QueryRow(`SELECT count(*) FROM sync_mutations WHERE disposition='pending'`).Scan(&originalJournal); err != nil {
			t.Fatal(err)
		}
		if originalSession != 1 || originalJournal != 3 {
			t.Fatalf("backup cannot restore pre-apply state: sessions=%d journal=%d", originalSession, originalJournal)
		}
		if scalarInt(t, s, `SELECT count(*) FROM sessions WHERE id=''`) != 1 || scalarInt(t, s, `SELECT count(*) FROM sessions WHERE id='new-id'`) != 0 {
			t.Fatal("partial identity")
		}
	})
}
