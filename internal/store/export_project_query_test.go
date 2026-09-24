package store

import (
	"encoding/json"
	"strings"
	"testing"
)

type exportedQuery struct {
	query string
	args  []any
}

func captureExportQueries(s *Store, captured *[]exportedQuery) {
	s.hooks.queryIt = func(db queryer, query string, args ...any) (rowScanner, error) {
		*captured = append(*captured, exportedQuery{query: query, args: append([]any(nil), args...)})
		rows, err := db.Query(query, args...)
		if err != nil {
			return nil, err
		}
		return sqlRowScanner{rows: rows}, nil
	}
}

func TestExportProjectQueriesUseProjectIndexes(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("target-session", "target", "/tmp/target"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddObservation(AddObservationParams{SessionID: "target-session", Type: "note", Title: "target", Content: "target", Project: "target", Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPrompt(AddPromptParams{SessionID: "target-session", Content: "target", Project: "target"}); err != nil {
		t.Fatal(err)
	}
	var captured []exportedQuery
	captureExportQueries(s, &captured)
	if _, err := s.ExportProject("target"); err != nil {
		t.Fatal(err)
	}
	assertExportQueryUsesIndex(t, s, captured, "SELECT id, ifnull(project, ''), directory", "idx_sessions_project")
	assertExportQueryUsesIndex(t, s, captured, "SELECT "+observationSelectColumns, "idx_obs_project")
	assertExportQueryUsesIndex(t, s, captured, "SELECT id, ifnull(sync_id, '') as sync_id, session_id, content, ifnull(project, '') as project, created_at FROM user_prompts", "idx_prompts_project")
}

func assertExportQueryUsesIndex(t *testing.T, s *Store, queries []exportedQuery, prefix, index string) {
	t.Helper()
	for _, candidate := range queries {
		if !strings.HasPrefix(strings.TrimSpace(candidate.query), prefix) {
			continue
		}
		rows, err := s.db.Query("EXPLAIN QUERY PLAN "+candidate.query, candidate.args...)
		if err != nil {
			t.Fatal(err)
		}
		outerTable := map[string]string{"idx_sessions_project": "sessions", "idx_obs_project": "observations", "idx_prompts_project": "user_prompts"}[index]
		usesIndex := false
		var details []string
		for rows.Next() {
			var selectID, order, from int
			var detail string
			if err := rows.Scan(&selectID, &order, &from, &detail); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			details = append(details, detail)
			if strings.Contains(detail, index) {
				usesIndex = true
			}
			for _, table := range []string{outerTable, "s", "o", "p"} {
				if detail == "SCAN "+table || strings.HasPrefix(detail, "SCAN "+table+" ") {
					_ = rows.Close()
					t.Fatalf("export query %q scans table or alias %s: %v", prefix, table, details)
				}
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if !usesIndex {
			t.Fatalf("export query %q did not use %s: %v", prefix, index, details)
		}
		return
	}
	t.Fatalf("export query %q not captured (want %s)", prefix, index)
}

func TestProjectRelationExportsAvoidFullScanTail(t *testing.T) {
	s, _, _ := setupExportRelationsStore(t)
	var captured []exportedQuery
	captureExportQueries(s, &captured)
	if _, err := s.ExportProject("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExportRelationMutations("alpha"); err != nil {
		t.Fatal(err)
	}
	plans := 0
	for _, candidate := range captured {
		if !strings.HasPrefix(strings.TrimSpace(candidate.query), "WITH project_observations") {
			continue
		}
		plans++
		rows, err := s.db.Query("EXPLAIN QUERY PLAN "+candidate.query, candidate.args...)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var selectID, order, from int
			var detail string
			if err := rows.Scan(&selectID, &order, &from, &detail); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			if strings.Contains(detail, "SCAN r") {
				_ = rows.Close()
				t.Fatalf("project relation export retained a full relation scan: %s", detail)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if plans != 2 {
		t.Fatalf("project relation query plans = %d, want 2", plans)
	}
}

func TestExportProjectRetainsLegacySessionOwnedRowsAndRelations(t *testing.T) {
	s, relationID, _ := setupExportRelationsStore(t)
	_, betaSyncID := addTestObsSession(t, s, "ses-exp-alpha", "Beta-owned in alpha session", "decision", "beta", "project")
	_, betaLegacySyncID := addTestObsSession(t, s, "ses-exp-beta", "Legacy beta observation", "decision", "beta", "project")
	if _, err := s.db.Exec(`UPDATE observations SET project = '' WHERE sync_id = ?`, betaLegacySyncID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, '')`, "beta-legacy-prompt", "ses-exp-beta", "beta prompt"); err != nil {
		t.Fatal(err)
	}
	var alphaNullSyncID, alphaBlankSyncID string
	if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE session_id = ? AND project = ? ORDER BY id LIMIT 1`, "ses-exp-alpha", "alpha").Scan(&alphaNullSyncID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT sync_id FROM observations WHERE session_id = ? AND project = ? AND sync_id != ?`, "ses-exp-alpha", "alpha", alphaNullSyncID).Scan(&alphaBlankSyncID); err != nil {
		t.Fatal(err)
	}
	crossRelationID := newSyncID("rel")
	if _, err := s.db.Exec(`INSERT INTO memory_relations (sync_id, source_id, target_id, relation, judgment_status, created_at, updated_at) VALUES (?, ?, ?, 'related', 'confirmed', datetime('now'), datetime('now')), (?, ?, ?, 'related', 'confirmed', datetime('now'), datetime('now'))`, crossRelationID, alphaNullSyncID, betaSyncID, "beta-legacy-relation", alphaNullSyncID, betaLegacySyncID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET project = NULL WHERE sync_id = ?`, alphaNullSyncID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE observations SET project = '' WHERE sync_id = ?`, alphaBlankSyncID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO user_prompts (sync_id, session_id, content, project) VALUES (?, ?, ?, NULL), (?, ?, ?, '')`, "legacy-null", "ses-exp-alpha", "null prompt", "legacy-blank", "ses-exp-alpha", "blank prompt"); err != nil {
		t.Fatal(err)
	}
	exported, err := s.ExportProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Sessions) != 1 || exported.Sessions[0].ID != "ses-exp-alpha" {
		t.Fatalf("alpha export sessions = %+v, want only ses-exp-alpha", exported.Sessions)
	}
	if len(exported.Observations) != 2 || len(exported.Prompts) != 2 || len(exported.Relations) != 1 {
		t.Fatalf("legacy export counts = obs:%d prompts:%d relations:%d", len(exported.Observations), len(exported.Prompts), len(exported.Relations))
	}
	seenAlpha := make(map[string]bool)
	for _, obs := range exported.Observations {
		if obs.SyncID == betaSyncID || obs.SyncID == betaLegacySyncID {
			t.Fatalf("beta observation exported in alpha: %s", obs.SyncID)
		}
		seenAlpha[obs.SyncID] = true
	}
	if !seenAlpha[alphaNullSyncID] || !seenAlpha[alphaBlankSyncID] {
		t.Fatal("legacy alpha observations missing")
	}
	for _, prompt := range exported.Prompts {
		if prompt.SyncID == "beta-legacy-prompt" {
			t.Fatal("beta legacy prompt exported")
		}
	}
	for _, relation := range exported.Relations {
		if relation.SyncID == crossRelationID || relation.SyncID == "beta-legacy-relation" {
			t.Fatalf("cross-project relation exported: %s", relation.SyncID)
		}
	}
	againExported, err := s.ExportProject("alpha")
	if err != nil {
		t.Fatal(err)
	}
	exported.ExportedAt, againExported.ExportedAt = "", ""
	exportBytes, _ := json.Marshal(exported)
	againExportBytes, _ := json.Marshal(againExported)
	if string(exportBytes) != string(againExportBytes) {
		t.Fatal("project export order is not deterministic")
	}
	mutations, err := s.ExportRelationMutations("alpha")
	if err != nil || len(mutations) != 1 || mutations[0].EntityKey != relationID {
		t.Fatalf("legacy relation mutations = %+v, %v", mutations, err)
	}
	for _, mutation := range mutations {
		if mutation.EntityKey == crossRelationID || mutation.EntityKey == "beta-legacy-relation" {
			t.Fatalf("cross-project relation mutation exported: %s", mutation.EntityKey)
		}
	}
	again, err := s.ExportRelationMutations("alpha")
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, _ := json.Marshal(mutations)
	againBytes, _ := json.Marshal(again)
	if string(firstBytes) != string(againBytes) {
		t.Fatal("relation mutation order is not deterministic")
	}
	dst := newTestStore(t)
	if _, err := dst.Import(exported); err != nil {
		t.Fatalf("import project export: %v", err)
	}
	if _, err := dst.GetRelation(relationID); err != nil {
		t.Fatalf("imported relation: %v", err)
	}
}
