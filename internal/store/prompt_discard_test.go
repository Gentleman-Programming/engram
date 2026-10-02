package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sqlite "modernc.org/sqlite"
)

func discardExec(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func discardFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s := newTestStore(t)
	for _, q := range []string{
		`INSERT INTO sessions(id,project,directory) VALUES ('session','project','/synthetic'),('control','control','/control')`,
		`INSERT INTO sync_enrolled_projects(project) VALUES ('project')`,
		`INSERT OR IGNORE INTO sync_state(target_key) VALUES ('other')`,
		`INSERT INTO sync_state(target_key,last_enqueued_seq,last_acked_seq,last_pulled_seq) VALUES ('cloud:control',41,23,17)`,
		`INSERT INTO prompt_tombstones(sync_id,session_id,project) VALUES ('control-deleted','control','control')`,
		`INSERT INTO user_prompts(sync_id,session_id,project,content) VALUES ('prompt','session','project','  '),('control','control','control','Keep')`,
		`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','prompt','prompt','upsert','{"sync_id":"prompt","session_id":"session","project":"project","content":"  "}','local','project'),('cloud','prompt','control','upsert','{"sync_id":"control","session_id":"control","project":"control","content":"Keep"}','local','control')`,
	} {
		discardExec(t, s, q)
	}
	var seq int64
	if err := s.db.QueryRow(`SELECT seq FROM sync_mutations WHERE entity_key='prompt'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return s, seq
}

func discardRead(t *testing.T, s *Store, q string, args ...any) []map[string]any {
	t.Helper()
	var rows []map[string]any
	err := s.withReadTx(func(tx *sql.Tx) error {
		var err error
		rows, err = discardRows(tx, q, args...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Complete row snapshots protect rollback and immutable journal/cursor fields.
func discardSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	var tables []any
	for _, table := range []string{"sync_mutations", "sync_state", "user_prompts", "prompts_fts", "prompt_tombstones", "sessions", "sync_enrolled_projects"} {
		tables = append(tables, discardRead(t, s, "SELECT * FROM "+table+" ORDER BY rowid"))
	}
	b, err := json.Marshal(tables)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Compare every relevant control-project row, excluding the global outbound
// cursor which must legitimately advance when the target project's delete queues.
func discardControlSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	var tables []any
	for _, table := range []string{"user_prompts", "sessions", "sync_mutations", "prompt_tombstones", "sync_enrolled_projects"} {
		tables = append(tables, discardRead(t, s, "SELECT * FROM "+table+" WHERE project='control' ORDER BY rowid"))
	}
	tables = append(tables, discardRead(t, s, `SELECT * FROM sync_state WHERE target_key='cloud:control'`))
	b, err := json.Marshal(tables)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func discardUnchanged(t *testing.T, s *Store, before string) {
	t.Helper()
	if after := discardSnapshot(t, s); before != after {
		t.Fatalf("unexpected database change\nbefore=%s\nafter=%s", before, after)
	}
}

func discardCode(t *testing.T, err error, code string) {
	t.Helper()
	var blocker *LegacyEmptyPromptDiscardBlocker
	if !errors.As(err, &blocker) || blocker.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestLegacyEmptyPromptDiscardEligibility(t *testing.T) {
	cases := []struct{ name, query, code string }{
		{"unenrolled", `DELETE FROM sync_enrolled_projects`, "not_enrolled"},
		{"canonical recovered", `UPDATE user_prompts SET content='Recovered' WHERE sync_id='prompt'`, "recoverable_content"},
		{"payload recovered", `UPDATE sync_mutations SET payload=json_set(payload,'$.content','Recovered') WHERE entity_key='prompt'`, "recoverable_content"},
		{"historical recovered", `INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project,acked_at) SELECT target_key,entity,entity_key,op,json_set(payload,'$.content','Historical'),source,project,datetime('now') FROM sync_mutations WHERE entity_key='prompt'`, "recoverable_content"},
		{"historical remote", `INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) SELECT target_key,entity,entity_key,op,payload,'remote',project FROM sync_mutations WHERE entity_key='prompt'`, "unsupported_lineage"},
		{"acked", `UPDATE sync_mutations SET acked_at=datetime('now') WHERE entity_key='prompt'`, "unsupported_lineage"},
		{"remote", `UPDATE sync_mutations SET source='remote' WHERE entity_key='prompt'`, "unsupported_lineage"},
		{"target conflict", `UPDATE sync_mutations SET target_key='other' WHERE entity_key='prompt'`, "unsupported_lineage"},
		{"session provenance", `UPDATE sessions SET local_creation_project='control' WHERE id='session'`, "conflicting_session"},
		{"delete lineage", `UPDATE sync_mutations SET op='delete' WHERE entity_key='prompt'`, "unsupported_lineage"},
		{"missing canonical", `DELETE FROM user_prompts WHERE sync_id='prompt'`, "ambiguous_canonical"},
		{"duplicate canonical", `INSERT INTO user_prompts(sync_id,session_id,project,content) SELECT sync_id,session_id,project,content FROM user_prompts WHERE sync_id='prompt'`, "ambiguous_canonical"},
		{"blank key", `UPDATE sync_mutations SET entity_key='' WHERE entity_key='prompt'`, "missing_identity"},
		{"missing session", `UPDATE sync_mutations SET payload=json_remove(payload,'$.session_id') WHERE entity_key='prompt'`, "conflicting_payload_identity"},
		{"missing project", `UPDATE sync_mutations SET payload=json_remove(payload,'$.project') WHERE entity_key='prompt'`, "conflicting_payload_identity"},
		{"conflicting identity", `UPDATE sync_mutations SET payload=json_set(payload,'$.sync_id','other') WHERE entity_key='prompt'`, "conflicting_payload_identity"},
		{"conflicting project", `UPDATE sync_mutations SET project='control' WHERE entity_key='prompt'`, "unsupported_lineage"},
		{"canonical project", `UPDATE user_prompts SET project='control' WHERE sync_id='prompt'`, "conflicting_identity"},
		{"creation provenance", `UPDATE user_prompts SET local_creation_project='control' WHERE sync_id='prompt'`, "conflicting_provenance"},
		{"source conflict", `UPDATE sync_mutations SET payload=json_set(payload,'$.source_inbox_id','other') WHERE entity_key='prompt'`, "conflicting_payload_identity"},
		{"session project", `UPDATE sessions SET project='control' WHERE id='session'`, "conflicting_session"},
		{"malformed", `UPDATE sync_mutations SET payload='{' WHERE entity_key='prompt'`, "malformed_payload"},
		{"unsupported field", `UPDATE sync_mutations SET payload=json_set(payload,'$.unknown','x') WHERE entity_key='prompt'`, "malformed_payload"},
		{"duplicate content", `UPDATE sync_mutations SET payload='{"sync_id":"prompt","session_id":"session","project":"project","content":"Recovered","content":""}' WHERE entity_key='prompt'`, "malformed_payload"},
		{"case alias content", `UPDATE sync_mutations SET payload='{"sync_id":"prompt","session_id":"session","project":"project","Content":"Recovered","content":""}' WHERE entity_key='prompt'`, "malformed_payload"},
		{"hidden identity", `INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','prompt','other','upsert','{"sync_id":"other","sync_id":"prompt","session_id":"session","project":"project","content":""}','local','project')`, "malformed_payload"},
		{"tombstone", `INSERT INTO prompt_tombstones(sync_id,session_id,project) VALUES ('prompt','session','project')`, "prior_tombstone"},
		{"conflicting duplicate", `INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) SELECT target_key,entity,entity_key,op,json_set(payload,'$.session_id','control'),source,project FROM sync_mutations WHERE entity_key='prompt'`, "conflicting_payload_identity"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s, seq := discardFixture(t)
			discardExec(t, s, tt.query)
			before := discardSnapshot(t, s)
			_, err := s.PlanLegacyEmptyPromptDiscard("project", seq)
			discardCode(t, err, tt.code)
			discardUnchanged(t, s, before)
		})
	}
}

func TestLegacyEmptyPromptDiscardInboxIdentity(t *testing.T) {
	for _, scenario := range []string{"valid", "canonical alias", "tombstone alias", "journal alias"} {
		t.Run(scenario, func(t *testing.T) {
			s, seq := discardFixture(t)
			discardExec(t, s, `UPDATE user_prompts SET source_inbox_id='inbox',
				local_creation_session_id='session', local_creation_project='project', local_creation_inbox_id='inbox'
				WHERE sync_id='prompt'`)
			discardExec(t, s, `UPDATE sync_mutations SET payload=json_set(payload,'$.source_inbox_id','inbox') WHERE seq=?`, seq)
			code := ""
			switch scenario {
			case "canonical alias":
				// The current schema itself forbids a duplicate nonempty inbox
				// identity. Do not weaken that constraint to manufacture a row.
				before := discardSnapshot(t, s)
				_, err := s.db.Exec(`INSERT INTO user_prompts(sync_id,session_id,project,source_inbox_id,content)
					VALUES ('alias','session','project','inbox','')`)
				if err == nil {
					t.Fatal("schema accepted duplicate inbox identity")
				}
				discardUnchanged(t, s, before)
			case "tombstone alias":
				discardExec(t, s, `INSERT INTO prompt_tombstones(sync_id,session_id,project,source_inbox_id)
					VALUES ('alias','session','project','inbox')`)
				code = "prior_tombstone"
			case "journal alias":
				discardExec(t, s, `INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project)
					SELECT target_key,entity,'alias',op,json_set(payload,'$.sync_id','alias'),source,project FROM sync_mutations WHERE seq=?`, seq)
				code = "unsupported_lineage"
			}
			before := discardSnapshot(t, s)
			plan, err := s.PlanLegacyEmptyPromptDiscard("project", seq)
			if code != "" {
				discardCode(t, err, code)
				discardUnchanged(t, s, before)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.ApplyLegacyEmptyPromptDiscard(plan, filepath.Join(t.TempDir(), "inbox.db")); err != nil {
				t.Fatal(err)
			}
			tombstone := discardRead(t, s, `SELECT * FROM prompt_tombstones WHERE sync_id='prompt'`)[0]
			for _, key := range []string{"source_inbox_id", "local_creation_inbox_id"} {
				if tombstone[key] != "inbox" {
					t.Fatalf("lost tombstone %s: %+v", key, tombstone)
				}
			}
		})
	}
}

func TestLegacyEmptyPromptDiscardStale(t *testing.T) {
	queries := []string{
		`UPDATE sync_mutations SET acked_at=datetime('now') WHERE entity_key='prompt'`,
		`UPDATE sync_mutations SET payload=json_set(payload,'$.created_at','2000-01-01') WHERE entity_key='prompt'`,
		`UPDATE sync_mutations SET disposition='superseded' WHERE entity_key='prompt'`,
		`UPDATE sync_mutations SET seq=777 WHERE entity_key='prompt'`,
		`UPDATE sync_mutations SET project='control' WHERE entity_key='prompt'`,
		`UPDATE sync_mutations SET source='remote' WHERE entity_key='prompt'`,
		`UPDATE sync_mutations SET target_key='other' WHERE entity_key='prompt'`,
		`INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) SELECT target_key,entity,entity_key,op,payload,source,project FROM sync_mutations WHERE entity_key='prompt'`,
		`UPDATE user_prompts SET content='Recovered' WHERE sync_id='prompt'`,
		`UPDATE user_prompts SET session_id='control' WHERE sync_id='prompt'`,
		`UPDATE user_prompts SET created_at='2000-01-01' WHERE sync_id='prompt'`,
		`DELETE FROM sync_enrolled_projects`,
		`UPDATE sync_enrolled_projects SET enrolled_at='2000-01-01'`,
		`INSERT INTO prompt_tombstones(sync_id,session_id,project) VALUES ('prompt','session','project')`,
	}
	for i, query := range queries {
		t.Run(fmt.Sprintf("change_%d", i), func(t *testing.T) {
			s, seq := discardFixture(t)
			plan, err := s.PlanLegacyEmptyPromptDiscard("project", seq)
			if err != nil {
				t.Fatal(err)
			}
			discardExec(t, s, query)
			before := discardSnapshot(t, s)
			path := filepath.Join(t.TempDir(), "stale.db")
			_, err = s.ApplyLegacyEmptyPromptDiscard(plan, path)
			discardCode(t, err, "stale_plan")
			discardUnchanged(t, s, before)
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("stale plan created backup: %v", err)
			}
		})
	}
}

// Substitution happens synchronously at the pathname-writing seam, not in a
// probabilistic goroutine. An error after writing attacker-selected bytes is
// insufficient: the adversarial empty file must remain completely untouched.
func TestLegacyEmptyPromptDiscardBackupRedirect(t *testing.T) {
	for _, attack := range []string{"destination", "parent"} {
		t.Run(attack, func(t *testing.T) {
			s, seq := discardFixture(t)
			plan, err := s.PlanLegacyEmptyPromptDiscard("project", seq)
			if err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(t.TempDir(), "destination-directory")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "backup.db")
			attackerDirectory := t.TempDir()
			attackerFile := filepath.Join(attackerDirectory, "backup.db")
			if err := os.WriteFile(attackerFile, nil, 0600); err != nil {
				t.Fatal(err)
			}
			before := discardSnapshot(t, s)
			original := s.hooks.exec
			injected := false
			s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
				if query == `VACUUM INTO ?` {
					injected = true
					if attack == "destination" {
						if err := os.Rename(path, path+".reserved"); err != nil {
							// Windows denies replacing the held-open file. This
							// is prevention, not an unavailable/skipped race test.
							t.Logf("OS prevented replacement of reserved handle: %v", err)
							return nil, fmt.Errorf("destination replacement prevented: %w", err)
						}
						if err := os.Symlink(attackerFile, path); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.Rename(parent, parent+".reserved"); err != nil {
							t.Logf("OS prevented replacement of held-open parent: %v", err)
							return nil, fmt.Errorf("parent replacement prevented: %w", err)
						}
						if err := os.Symlink(attackerDirectory, parent); err != nil {
							t.Fatal(err)
						}
					}
				}
				return original(db, query, args...)
			}
			result, err := s.ApplyLegacyEmptyPromptDiscard(plan, path)
			if !injected || err == nil || result.Status != "" {
				t.Fatalf("replacement was not rejected: injected=%v result=%+v err=%v", injected, result, err)
			}
			discardUnchanged(t, s, before)
			info, err := os.Stat(attackerFile)
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() != 0 {
				t.Fatalf("database bytes reached unreserved attacker file: size=%d", info.Size())
			}
			entries, err := os.ReadDir(s.cfg.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".prompt-discard-backup-") {
					t.Fatalf("private snapshot directory was not cleaned: %s", entry.Name())
				}
			}
		})
	}
}

func TestLegacyEmptyPromptDiscardBackupErrors(t *testing.T) {
	for _, scenario := range []string{"blank", "existing", "empty existing", "source", "wal", "shm", "missing parent", "injected", "hardlink", "symlink", "parent alias"} {
		t.Run(scenario, func(t *testing.T) {
			s, seq := discardFixture(t)
			plan, err := s.PlanLegacyEmptyPromptDiscard("project", seq)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "backup.db")
			switch scenario {
			case "blank":
				path = ""
			case "existing", "empty existing":
				content := []byte("Keep existing file")
				if scenario == "empty existing" {
					content = nil
				}
				if err := os.WriteFile(path, content, 0600); err != nil {
					t.Fatal(err)
				}
			case "source", "wal", "shm":
				path = filepath.Join(s.cfg.DataDir, "engram.db")
				if scenario != "source" {
					path += "-" + scenario
				}
			case "missing parent":
				path = filepath.Join(t.TempDir(), "missing", "backup.db")
			case "injected":
				original := s.hooks.exec
				s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
					if query == `VACUUM INTO ?` {
						return nil, errors.New("injected vacuum failure")
					}
					return original(db, query, args...)
				}
			case "hardlink":
				if err := os.Link(filepath.Join(s.cfg.DataDir, "engram.db"), path); err != nil {
					t.Fatal(err)
				}
			case "parent alias":
				alias := filepath.Join(t.TempDir(), "live-directory")
				if err := os.Symlink(s.cfg.DataDir, alias); err != nil {
					t.Skipf("directory symlink unavailable: %v", err)
				}
				path = filepath.Join(alias, "engram.db")
			case "symlink":
				if err := os.Symlink(filepath.Join(s.cfg.DataDir, "engram.db"), path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			before := discardSnapshot(t, s)
			_, err = s.ApplyLegacyEmptyPromptDiscard(plan, path)
			if err == nil {
				t.Fatal("backup error accepted")
			}
			discardUnchanged(t, s, before)
			if scenario == "existing" {
				content, err := os.ReadFile(path)
				if err != nil || string(content) != "Keep existing file" {
					t.Fatalf("overwrote destination: %q %v", content, err)
				}
			}
		})
	}
}

func TestLegacyEmptyPromptDiscardRollback(t *testing.T) {
	for _, failure := range []string{"INSERT INTO prompt_tombstones", "DELETE FROM user_prompts", "INSERT INTO sync_mutations", "UPDATE sync_state"} {
		t.Run(failure, func(t *testing.T) {
			s, seq := discardFixture(t)
			plan, err := s.PlanLegacyEmptyPromptDiscard("project", seq)
			if err != nil {
				t.Fatal(err)
			}
			before := discardSnapshot(t, s)
			original := s.hooks.exec
			injected := false
			s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
				if strings.Contains(query, failure) {
					injected = true
					return nil, errors.New("injected intermediate failure")
				}
				return original(db, query, args...)
			}
			result, err := s.ApplyLegacyEmptyPromptDiscard(plan, filepath.Join(t.TempDir(), "rollback.db"))
			if err == nil || !injected || result.Status != "" || result.DeleteSeq != 0 || result.BackupPath == "" {
				t.Fatalf("rollback not exercised: %+v %v", result, err)
			}
			discardUnchanged(t, s, before)
		})
	}
}

func TestLegacyEmptyPromptDiscardRevalidationErrors(t *testing.T) {
	for _, phase := range []string{"preflight", "post-backup"} {
		t.Run(phase, func(t *testing.T) {
			s, seq := discardFixture(t)
			plan, err := s.PlanLegacyEmptyPromptDiscard("project", seq)
			if err != nil {
				t.Fatal(err)
			}
			before := discardSnapshot(t, s)
			// Preserve all rows while making the enrollment query fail in
			// replanning, rather than manufacturing a domain ineligibility.
			breakQuery := func() {
				discardExec(t, s, `ALTER TABLE sync_enrolled_projects RENAME COLUMN project TO unavailable_project`)
			}
			backedUp := false
			original := s.hooks.exec
			s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
				result, err := original(db, query, args...)
				if err == nil && query == `VACUUM INTO ?` {
					backedUp = true
					breakQuery()
				}
				return result, err
			}
			if phase == "preflight" {
				breakQuery()
			}
			path := filepath.Join(t.TempDir(), "revalidation.db")
			result, err := s.ApplyLegacyEmptyPromptDiscard(plan, path)
			discardExec(t, s, `ALTER TABLE sync_enrolled_projects RENAME COLUMN unavailable_project TO project`)
			discardUnchanged(t, s, before)
			if result.Status != "" || result.DeleteSeq != 0 || len(result.Sequences) != 0 {
				t.Fatalf("failure reported discard effects: %+v", result)
			}
			if phase == "post-backup" {
				info, statErr := os.Stat(path)
				if !backedUp || result.BackupPath != path || statErr != nil || info.Size() == 0 {
					t.Fatalf("transactional revalidation not reached: backedUp=%v result=%+v stat=%v", backedUp, result, statErr)
				}
			} else if _, statErr := os.Lstat(path); backedUp || result.BackupPath != "" || !os.IsNotExist(statErr) {
				t.Fatalf("preflight failure created backup: backedUp=%v result=%+v stat=%v", backedUp, result, statErr)
			}
			var blocker *LegacyEmptyPromptDiscardBlocker
			if errors.As(err, &blocker) {
				t.Errorf("SQL revalidation error became domain blocker: %v", err)
			}
			var sqlErr *sqlite.Error
			if !errors.As(err, &sqlErr) || sqlErr.Code() != 1 || !strings.Contains(sqlErr.Error(), "no such column: project") {
				t.Fatalf("SQL revalidation error lost concrete type or cause: %T %v", err, err)
			}
		})
	}
}

func TestLegacyEmptyPromptDiscardPostBackupStale(t *testing.T) {
	s, seq := discardFixture(t)
	plan, err := s.PlanLegacyEmptyPromptDiscard("project", seq)
	if err != nil {
		t.Fatal(err)
	}
	original := s.hooks.exec
	var expected string
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		result, err := original(db, query, args...)
		if err == nil && query == `VACUUM INTO ?` {
			_, err = s.db.Exec(`UPDATE sync_mutations SET acked_at=datetime('now') WHERE seq=?`, seq)
			expected = discardSnapshot(t, s)
		}
		return result, err
	}
	path := filepath.Join(t.TempDir(), "before-change.db")
	result, err := s.ApplyLegacyEmptyPromptDiscard(plan, path)
	discardCode(t, err, "stale_plan")
	discardUnchanged(t, s, expected)
	if result.BackupPath != path || result.Status != "" {
		t.Fatalf("bad failure result: %+v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("backup should remain", err)
	}
}

func TestLegacyEmptyPromptDiscardTamperedDuplicateSelection(t *testing.T) {
	s, seq := discardFixture(t)
	discardExec(t, s, `INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project)
		SELECT target_key,entity,entity_key,op,payload,source,project FROM sync_mutations WHERE seq=?`, seq)
	plan, err := s.PlanLegacyEmptyPromptDiscard("project", seq)
	if err != nil {
		t.Fatal(err)
	}
	plan.SelectedSeq = plan.Sequences[1]
	before := discardSnapshot(t, s)
	path := filepath.Join(t.TempDir(), "tampered-selection.db")
	_, err = s.ApplyLegacyEmptyPromptDiscard(plan, path)
	discardCode(t, err, "stale_plan")
	discardUnchanged(t, s, before)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("tampered selection created backup: %v", err)
	}
}

func TestLegacyEmptyPromptDiscardSelection(t *testing.T) {
	s, seq := discardFixture(t)
	for _, project := range []string{"", " project "} {
		_, err := s.PlanLegacyEmptyPromptDiscard(project, seq)
		discardCode(t, err, "invalid_selection")
	}
	plan, err := s.PlanLegacyEmptyPromptDiscard("project", seq)
	if err != nil {
		t.Fatal(err)
	}
	plan.Sequences = []int64{999}
	_, err = s.ApplyLegacyEmptyPromptDiscard(plan, filepath.Join(t.TempDir(), "tampered.db"))
	discardCode(t, err, "stale_plan")
	_, err = s.ApplyLegacyEmptyPromptDiscard(LegacyEmptyPromptDiscardPlan{}, "")
	discardCode(t, err, "invalid_plan")
}

func TestLegacyEmptyPromptDiscardSuccess(t *testing.T) {
	s, seq := discardFixture(t)
	// Coherent duplicates are retired together, preserving the original rows.
	discardExec(t, s, `INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project,occurred_at)
		SELECT target_key,entity,entity_key,op,payload,source,project,'2000-01-01' FROM sync_mutations WHERE seq=?`, seq)
	originals := discardRead(t, s, `SELECT * FROM sync_mutations ORDER BY seq`)
	control := discardControlSnapshot(t, s)
	if postings := discardRead(t, s, `SELECT rowid FROM prompts_fts WHERE prompts_fts MATCH 'project : "project"'`); len(postings) != 1 {
		t.Fatalf("fixture lacks target project FTS posting: %+v", postings)
	}
	state := discardRead(t, s, `SELECT * FROM sync_state WHERE target_key='cloud'`)[0]
	before := discardSnapshot(t, s)
	files, err := os.ReadDir(s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.PlanLegacyEmptyPromptDiscard("project", seq)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sequences) != 2 || plan.Sequences[0] != seq || plan.SyncID != "prompt" || plan.SessionID != "session" {
		t.Fatalf("bad plan: %+v", plan)
	}
	discardUnchanged(t, s, before)
	afterFiles, err := os.ReadDir(s.cfg.DataDir)
	if err != nil || !reflect.DeepEqual(files, afterFiles) {
		t.Fatalf("planning changed filesystem: %v", err)
	}
	wal, err := os.Stat(filepath.Join(s.cfg.DataDir, "engram.db-wal"))
	if err != nil || wal.Size() == 0 {
		t.Fatalf("fixture lacks WAL writes: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	result, err := s.ApplyLegacyEmptyPromptDiscard(plan, path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "delete_queued" || result.DeleteSeq <= plan.Sequences[1] ||
		result.BackupPath != path || !reflect.DeepEqual(result.Sequences, plan.Sequences) {
		t.Fatalf("bad result: %+v", result)
	}
	for _, original := range originals {
		current := discardRead(t, s, `SELECT * FROM sync_mutations WHERE seq=?`, original["seq"])[0]
		for key, value := range original {
			if original["entity_key"] == "prompt" && strings.HasPrefix(key, "disposition") {
				continue
			}
			if !reflect.DeepEqual(value, current[key]) {
				t.Fatalf("immutable %s changed: %v -> %v", key, value, current[key])
			}
		}
		if original["entity_key"] == "prompt" {
			if current["disposition"] != SyncMutationDispositionSuperseded ||
				current["disposition_reason"] != legacyPromptDiscardReason || current["disposition_at"] == nil {
				t.Fatalf("missing audit: %+v", current)
			}
			var evidence map[string]any
			if err := json.Unmarshal([]byte(discardText(current, "disposition_evidence")), &evidence); err != nil || evidence["trigger"] != legacyPromptDiscardReason {
				t.Fatalf("invalid evidence: %v %+v", err, evidence)
			}
		}
	}
	if len(discardRead(t, s, `SELECT * FROM user_prompts WHERE sync_id='prompt'`)) != 0 ||
		len(discardRead(t, s, `SELECT * FROM prompts_fts WHERE rowid=?`, plan.PromptID)) != 0 {
		t.Fatal("discarded canonical/FTS row survives")
	}
	if after := discardControlSnapshot(t, s); control != after {
		t.Fatalf("control-project state changed\nbefore=%s\nafter=%s", control, after)
	}
	if postings := discardRead(t, s, `SELECT rowid FROM prompts_fts WHERE prompts_fts MATCH 'project : "project"'`); len(postings) != 0 {
		t.Fatalf("stale target project FTS posting survives: %+v", postings)
	}
	// rank=1 verifies the external-content index against its canonical table,
	// not just the virtual table's external-content SELECT projection.
	discardExec(t, s, `INSERT INTO prompts_fts(prompts_fts,rank) VALUES ('integrity-check',1)`)
	tombstone := discardRead(t, s, `SELECT * FROM prompt_tombstones WHERE sync_id='prompt'`)
	if len(tombstone) != 1 || tombstone[0]["sync_id"] != "prompt" ||
		tombstone[0]["session_id"] != "session" || tombstone[0]["project"] != "project" {
		t.Fatalf("bad tombstone: %+v", tombstone)
	}
	lineage := discardRead(t, s, `SELECT * FROM sync_mutations WHERE entity_key='prompt' ORDER BY seq`)
	if len(lineage) != 3 {
		t.Fatalf("expected retained originals plus one delete: %+v", lineage)
	}
	deletion := lineage[2]
	var payload syncPromptPayload
	if err := json.Unmarshal([]byte(discardText(deletion, "payload")), &payload); err != nil {
		t.Fatal(err)
	}
	if deletion["seq"] != result.DeleteSeq || deletion["op"] != SyncOpDelete || deletion["acked_at"] != nil ||
		deletion["source"] != SyncSourceLocal || deletion["disposition"] != SyncMutationDispositionPending ||
		payload.SyncID != "prompt" || payload.SessionID != "session" || payload.Project == nil || *payload.Project != "project" ||
		!payload.Deleted || !payload.HardDelete || payload.DeletedAt == nil || *payload.DeletedAt != tombstone[0]["deleted_at"] {
		t.Fatalf("invalid delete: %+v %+v", deletion, payload)
	}
	afterState := discardRead(t, s, `SELECT * FROM sync_state WHERE target_key='cloud'`)[0]
	for _, key := range []string{"last_acked_seq", "last_pulled_seq"} {
		if !reflect.DeepEqual(state[key], afterState[key]) {
			t.Fatalf("fabricated %s", key)
		}
	}
	if afterState["last_enqueued_seq"] != result.DeleteSeq {
		t.Fatal("outbound cursor did not advance")
	}
	backup, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backup.Close() }()
	var integrity, content string
	if err := backup.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("backup integrity=%s err=%v", integrity, err)
	}
	if err := backup.QueryRow(`SELECT content FROM user_prompts WHERE sync_id='prompt'`).Scan(&content); err != nil || content != "  " {
		t.Fatalf("backup lacks pre-discard canonical: %q %v", content, err)
	}
	var n int
	if err := backup.QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity_key='prompt' AND disposition='pending'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("backup lacks pre-discard journal: %d %v", n, err)
	}
	before = discardSnapshot(t, s)
	repeat := filepath.Join(t.TempDir(), "repeat.db")
	_, err = s.ApplyLegacyEmptyPromptDiscard(plan, repeat)
	discardCode(t, err, "stale_plan")
	discardUnchanged(t, s, before)
	if _, err := os.Lstat(repeat); !os.IsNotExist(err) {
		t.Fatalf("repeat created backup: %v", err)
	}
}
