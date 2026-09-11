package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestListOrphanedObservationSessionEvidenceGroupsScopesAndExcludesBlankIDs(t *testing.T) {
	s := newTestStore(t)
	seedOrphanedObservationSession(t, s, "obs-nil", "missing-0", nil, nil)
	seedOrphanedObservationSession(t, s, "obs-nil-project", "missing-shared", nil, nil)
	seedOrphanedObservationSession(t, s, "obs-empty-project", "missing-shared", "", nil)
	seedOrphanedObservationSession(t, s, "obs-alpha-active", "missing-1", "alpha", nil)
	seedOrphanedObservationSession(t, s, "obs-alpha-deleted", "missing-1", "alpha", "2026-01-01 00:00:00")
	seedOrphanedObservationSession(t, s, "obs-alpha-second", "missing-2", "alpha", nil)
	seedOrphanedObservationSession(t, s, "obs-beta", "missing-a", "beta", nil)
	seedOrphanedObservationSession(t, s, "obs-empty", "", "alpha", nil)
	seedOrphanedObservationSession(t, s, "obs-spaces", "  ", "alpha", nil)
	seedOrphanedObservationSession(t, s, "obs-tab", "\t", "alpha", nil)
	assertForeignKeysEnabled(t, s)

	got, err := s.ListOrphanedObservationSessionEvidence("")
	if err != nil {
		t.Fatalf("ListOrphanedObservationSessionEvidence: %v", err)
	}
	want := []OrphanedObservationSessionEvidence{
		{Project: "", SessionID: "missing-0", ObservationCount: 1},
		{Project: "", SessionID: "missing-shared", ObservationCount: 2},
		{Project: "alpha", SessionID: "missing-1", ObservationCount: 2},
		{Project: "alpha", SessionID: "missing-2", ObservationCount: 1},
		{Project: "beta", SessionID: "missing-a", ObservationCount: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("evidence=%+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("evidence[%d]=%+v, want %+v", i, got[i], want[i])
		}
	}

	scoped, err := s.ListOrphanedObservationSessionEvidence(" Alpha ")
	if err != nil {
		t.Fatalf("ListOrphanedObservationSessionEvidence scoped: %v", err)
	}
	if len(scoped) != 2 || scoped[0].Project != "alpha" || scoped[0].SessionID != "missing-1" || scoped[1].SessionID != "missing-2" {
		t.Fatalf("scoped evidence=%+v", scoped)
	}
}

func TestListOrphanedObservationSessionEvidencePropagatesQueryFailure(t *testing.T) {
	s := newTestStore(t)
	wantErr := errors.New("diagnostic query failed")
	oldQueryIt := s.hooks.queryIt
	s.hooks.queryIt = func(queryer, string, ...any) (rowScanner, error) {
		return nil, wantErr
	}
	t.Cleanup(func() { s.hooks.queryIt = oldQueryIt })

	_, err := s.ListOrphanedObservationSessionEvidence("")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
}

func TestListOrphanedObservationSessionEvidencePropagatesRowProcessingFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows *fakeRows
	}{
		{
			name: "scan failure",
			rows: &fakeRows{next: []bool{true}, scanErr: errors.New("scan failed")},
		},
		{
			name: "rows error",
			rows: &fakeRows{err: errors.New("rows failed")},
		},
		{
			name: "close failure",
			rows: &fakeRows{closeErr: errors.New("close failed")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			oldQueryIt := s.hooks.queryIt
			s.hooks.queryIt = func(queryer, string, ...any) (rowScanner, error) {
				return tc.rows, nil
			}
			t.Cleanup(func() { s.hooks.queryIt = oldQueryIt })

			_, err := s.ListOrphanedObservationSessionEvidence("")
			wantErr := tc.rows.scanErr
			if wantErr == nil {
				wantErr = tc.rows.err
			}
			if wantErr == nil {
				wantErr = tc.rows.closeErr
			}
			if err != wantErr {
				t.Fatalf("error=%v, want exact %v", err, wantErr)
			}
			if !tc.rows.closed {
				t.Fatal("rows were not closed")
			}
		})
	}
}

func seedOrphanedObservationSession(t *testing.T, s *Store, syncID, sessionID string, project, deletedAt any) {
	t.Helper()
	ctx := context.Background()
	conn, err := s.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("database connection: %v", err)
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
			t.Errorf("restore foreign keys: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Errorf("close database connection: %v", err)
		}
	}()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO observations
			(sync_id, session_id, type, title, content, project, scope, normalized_hash, revision_count, duplicate_count, created_at, updated_at, deleted_at)
		VALUES (?, ?, 'bugfix', 'orphan', 'content', ?, 'project', ?, 1, 1, datetime('now'), datetime('now'), ?)
	`, syncID, sessionID, project, syncID, deletedAt); err != nil {
		t.Fatalf("seed orphaned observation %q: %v", syncID, err)
	}
}

func assertForeignKeysEnabled(t *testing.T, s *Store) {
	t.Helper()
	var enabled int
	if err := s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatalf("read foreign key enforcement: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("foreign key enforcement=%d, want 1", enabled)
	}
}

func TestEstimateSessionProjectReclassificationDoesNotMutate(t *testing.T) {
	s := newTestStore(t)
	seedRepairRows(t, s, "repair-s1", "sias-app")

	counts, err := s.EstimateSessionProjectReclassification([]SessionProjectReclassification{{SessionID: "repair-s1", FromProject: "sias-app", ToProject: "engram"}})
	if err != nil {
		t.Fatalf("EstimateSessionProjectReclassification: %v", err)
	}
	if counts.Sessions != 1 || counts.Observations != 1 || counts.Prompts != 1 {
		t.Fatalf("counts=%+v", counts)
	}
	assertRepairProjects(t, s, "repair-s1", "sias-app", "sias-app", "sias-app")
}

func TestApplySessionProjectReclassificationBacksUpAndUpdatesAllowedTables(t *testing.T) {
	s := newTestStore(t)
	seedRepairRows(t, s, "repair-s1", "sias-app")
	beforeSyncState := scalarString(t, s, `SELECT COALESCE(group_concat(target_key || ':' || last_acked_seq || ':' || last_pulled_seq, ','), '') FROM sync_state`)
	beforeMutations := scalarString(t, s, `SELECT COALESCE(group_concat(seq || ':' || entity || ':' || entity_key || ':' || project, ','), '') FROM sync_mutations`)
	beforeSessionCount := scalarInt(t, s, `SELECT count(*) FROM sessions`)
	beforeObservationCount := scalarInt(t, s, `SELECT count(*) FROM observations`)
	beforePromptCount := scalarInt(t, s, `SELECT count(*) FROM user_prompts`)

	result, err := s.ApplySessionProjectReclassification([]SessionProjectReclassification{{SessionID: "repair-s1", FromProject: "sias-app", ToProject: "engram"}})
	if err != nil {
		t.Fatalf("ApplySessionProjectReclassification: %v", err)
	}
	if result.BackupPath == "" {
		t.Fatal("expected backup path")
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if filepath.Dir(result.BackupPath) != filepath.Join(s.cfg.DataDir, "backups") {
		t.Fatalf("backup path outside backups dir: %s", result.BackupPath)
	}
	if result.Counts.Sessions != 1 || result.Counts.Observations != 1 || result.Counts.Prompts != 1 {
		t.Fatalf("counts=%+v", result.Counts)
	}
	assertRepairProjects(t, s, "repair-s1", "engram", "engram", "engram")
	if got := scalarString(t, s, `SELECT ownership_mode FROM sessions WHERE id = ?`, "repair-s1"); got != SessionOwnershipShared {
		t.Fatalf("runtime repair ownership mode=%q, want %q", got, SessionOwnershipShared)
	}
	if got := scalarString(t, s, `SELECT COALESCE(group_concat(target_key || ':' || last_acked_seq || ':' || last_pulled_seq, ','), '') FROM sync_state`); got != beforeSyncState {
		t.Fatalf("sync_state changed: before=%q after=%q", beforeSyncState, got)
	}
	if got := scalarString(t, s, `SELECT COALESCE(group_concat(seq || ':' || entity || ':' || entity_key || ':' || project, ','), '') FROM sync_mutations`); got != beforeMutations {
		t.Fatalf("sync_mutations changed: before=%q after=%q", beforeMutations, got)
	}
	if got := scalarInt(t, s, `SELECT count(*) FROM sessions`); got != beforeSessionCount {
		t.Fatalf("session count changed: before=%d after=%d", beforeSessionCount, got)
	}
	if got := scalarInt(t, s, `SELECT count(*) FROM observations`); got != beforeObservationCount {
		t.Fatalf("observation count changed: before=%d after=%d", beforeObservationCount, got)
	}
	if got := scalarInt(t, s, `SELECT count(*) FROM user_prompts`); got != beforePromptCount {
		t.Fatalf("prompt count changed: before=%d after=%d", beforePromptCount, got)
	}
}

func TestApplySessionProjectReclassificationClassifiesManualSessionsAndCreatesBackup(t *testing.T) {
	s := newTestStore(t)
	seedRepairRows(t, s, "manual-save-engram", "sias-app")
	result, err := s.ApplySessionProjectReclassification([]SessionProjectReclassification{{SessionID: "manual-save-engram", FromProject: "sias-app", ToProject: "engram"}})
	if err != nil {
		t.Fatalf("ApplySessionProjectReclassification: %v", err)
	}
	if result.BackupPath == "" {
		t.Fatal("expected backup path")
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if got := scalarString(t, s, `SELECT ownership_mode FROM sessions WHERE id = ?`, "manual-save-engram"); got != SessionOwnershipProjectOwned {
		t.Fatalf("manual repair ownership mode=%q, want %q", got, SessionOwnershipProjectOwned)
	}
}

func seedRepairRows(t *testing.T, s *Store, sessionID, project string) {
	t.Helper()
	if err := s.CreateSession(sessionID, project, "/work/engram"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: sessionID, Type: "bugfix", Title: "repair", Content: "content", Project: project, Scope: "project"}); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: sessionID, Content: "prompt", Project: project}); err != nil {
		t.Fatalf("AddPrompt: %v", err)
	}
}

func assertRepairProjects(t *testing.T, s *Store, sessionID, sessionProject, observationProject, promptProject string) {
	t.Helper()
	if got := scalarString(t, s, `SELECT project FROM sessions WHERE id = ?`, sessionID); got != sessionProject {
		t.Fatalf("session project=%q want %q", got, sessionProject)
	}
	if got := scalarString(t, s, `SELECT project FROM observations WHERE session_id = ?`, sessionID); got != observationProject {
		t.Fatalf("observation project=%q want %q", got, observationProject)
	}
	if got := scalarString(t, s, `SELECT project FROM user_prompts WHERE session_id = ?`, sessionID); got != promptProject {
		t.Fatalf("prompt project=%q want %q", got, promptProject)
	}
}

func scalarString(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	var got string
	if err := s.db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return got
}

func scalarInt(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var got int
	if err := s.db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return got
}
