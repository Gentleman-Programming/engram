package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestHandleSave_CandidatePersistenceFailure(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "all inserts fail"
		if partial {
			name = "partial success"
		}
		t.Run(name, func(t *testing.T) {
			s, dir := newMCPTestStoreWithDataDir(t)
			if err := s.CreateSession("candidate-failure-session", "candidate-failure-test", t.TempDir()); err != nil {
				t.Fatal(err)
			}
			h := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
			save := func(title, content string) map[string]any {
				t.Helper()
				res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
					"title": title, "content": content, "type": "decision", "project": "candidate-failure-test", "session_id": "candidate-failure-session",
				}}})
				if err != nil {
					t.Fatal(err)
				}
				if res.IsError {
					t.Fatalf("observation save failed: %s", callResultText(t, res))
				}
				var envelope map[string]any
				if err := json.Unmarshal([]byte(callResultText(t, res)), &envelope); err != nil {
					t.Fatal(err)
				}
				return envelope
			}
			first := save("Redis cache policy", "Use Redis caching for application reads")
			db, err := sql.Open("sqlite", filepath.Join(dir, "engram.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			// Setup-only SQL injects real persistence failure; assertions use public APIs.
			if _, err := db.Exec(`CREATE TRIGGER fail_candidate_insert BEFORE INSERT ON memory_relations
				BEGIN SELECT RAISE(FAIL, 'injected candidate insertion failure'); END`); err != nil {
				t.Fatal(err)
			}
			if partial {
				save("Redis cache eviction policy", "Evict application cache entries by age")
				// Allow exactly the first insert; all later inserts fail, independent of ranking.
				if _, err := db.Exec(`DROP TRIGGER fail_candidate_insert;
					CREATE TRIGGER fail_candidate_insert BEFORE INSERT ON memory_relations
					WHEN (SELECT count(*) FROM memory_relations) >= 1
					BEGIN SELECT RAISE(FAIL, 'injected candidate insertion failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			got := save("Redis cache replacement policy", "Replace application caching with a new Redis strategy")
			if got["candidate_detection_status"] != "incomplete" {
				t.Errorf("candidate detection status = %v", got["candidate_detection_status"])
			}
			message, _ := got["result"].(string)
			if !strings.Contains(message, "CONFLICT DETECTION INCOMPLETE") {
				t.Errorf("missing incomplete-detection warning: %q", message)
			}
			if !partial {
				if _, present := got["judgment_required"]; present {
					t.Errorf("incomplete detection must not claim no judgments: %v", got)
				}
			} else if got["judgment_required"] != true {
				t.Errorf("confirmed partial candidates require judgment: %v", got)
			}
			candidates, _ := got["candidates"].([]any)
			wantCount := 0
			if partial {
				wantCount = 1
			}
			if len(candidates) != wantCount {
				t.Errorf("confirmed candidates = %d, want %d", len(candidates), wantCount)
			}
			for _, entry := range candidates {
				candidate := entry.(map[string]any)
				if candidate["judgment_id"] == "" {
					t.Error("confirmed candidate missing judgment ID")
				}
			}
			count, err := s.CountRelations(store.ListRelationsOptions{Status: "pending"})
			if err != nil || count != wantCount {
				t.Errorf("persisted relations = %d, err = %v, want %d", count, err, wantCount)
			}
			id, ok := got["id"].(float64)
			if !ok {
				t.Fatal("saved observation ID missing")
			}
			obs, err := s.GetObservation(int64(id))
			if err != nil || obs.Title != "Redis cache replacement policy" {
				t.Fatalf("observation not durable: %v, %v", obs, err)
			}
			if _, err := s.GetObservation(int64(first["id"].(float64))); err != nil {
				t.Fatalf("previous observation lost: %v", err)
			}
		})
	}
}
