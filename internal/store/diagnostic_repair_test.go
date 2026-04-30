package store

import (
	"os"
	"path/filepath"
	"testing"
)

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
