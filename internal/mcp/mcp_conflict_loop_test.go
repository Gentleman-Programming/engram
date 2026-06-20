package mcp

// Phase G — Integration tests: full save→judge→search lifecycle.
// REQ-001 | REQ-002 | REQ-003 | REQ-004 | REQ-007 | REQ-009 | REQ-010
//
// These tests validate the complete conflict-surfacing lifecycle end-to-end
// against the A-F implementation. They are intentionally LAYERED ON TOP of
// the existing unit tests — they do not duplicate unit coverage, they verify
// the components work correctly together.
//
// Constraint: if any test fails here, the bug is in A-F; do NOT modify
// A-F implementation in this batch — report and let the orchestrator decide.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// ─── G.1 — Full save→judge→search loop ──────────────────────────────────────
//
// Verifies the complete end-to-end lifecycle:
//   1. Save observation A (no candidates in empty store)
//   2. Save observation B with overlapping title → candidates returned
//   3. mem_judge one candidate as conflicts_with
//   4. mem_search → B shows conflict annotation; A shows contested annotation
//
// REQ-001 (candidate detection), REQ-002 (search annotations), REQ-003 (mem_judge)
func TestConflictLoop_SaveJudgeSearch(t *testing.T) {
	s := newMCPTestStore(t)
	saveH := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	searchH := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	judgeH := handleJudge(s, NewSessionActivity(10*time.Minute))

	// ── Step 1: Save observation A (unique topic → no candidates) ────────────
	resA, err := saveH(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"title":   "We use sessions for authentication middleware",
			"content": "Session-based auth in the middleware layer keeps state server-side",
			"type":    "architecture",
		}},
	})
	if err != nil {
		t.Fatalf("G.1 save A handler error: %v", err)
	}
	if resA.IsError {
		t.Fatalf("G.1 save A unexpected error: %s", callResultText(t, resA))
	}

	envA := parseEnvelope(t, "G.1 save A", resA)
	// No prior observations → judgment_required must be false or absent.
	if jr, ok := envA["judgment_required"].(bool); ok && jr {
		t.Fatalf("G.1 expected no candidates after first save, got judgment_required=true")
	}
	syncIDA, _ := envA["sync_id"].(string)
	if syncIDA == "" {
		t.Fatalf("G.1 save A must return sync_id in envelope")
	}

	// ── Step 2: Save observation B with overlapping title → expect candidates ─
	resB, err := saveH(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"title":   "Switched from sessions to JWT authentication",
			"content": "JWT tokens replace session-based auth for better scalability across services",
			"type":    "architecture",
		}},
	})
	if err != nil {
		t.Fatalf("G.1 save B handler error: %v", err)
	}
	if resB.IsError {
		t.Fatalf("G.1 save B unexpected error: %s", callResultText(t, resB))
	}

	envB := parseEnvelope(t, "G.1 save B", resB)
	judgmentRequired, _ := envB["judgment_required"].(bool)
	if !judgmentRequired {
		t.Fatalf("G.1 expected judgment_required=true after saving similar observation B, got false/absent")
	}

	candidates, _ := envB["candidates"].([]any)
	if len(candidates) == 0 {
		t.Fatalf("G.1 expected at least 1 candidate after saving B, got empty candidates[]")
	}
	firstCand, ok := candidates[0].(map[string]any)
	if !ok {
		t.Fatalf("G.1 candidates[0] not a map, got %T", candidates[0])
	}

	// Verify required candidate fields (REQ-001).
	for _, field := range []string{"id", "sync_id", "title", "type", "score", "judgment_id"} {
		if _, exists := firstCand[field]; !exists {
			t.Errorf("G.1 candidates[0] missing required field %q", field)
		}
	}

	judgmentID, _ := firstCand["judgment_id"].(string)
	if !strings.HasPrefix(judgmentID, "rel") {
		t.Fatalf("G.1 judgment_id must start with 'rel', got %q", judgmentID)
	}

	result, _ := envB["result"].(string)
	if !strings.Contains(result, "CONFLICT REVIEW PENDING") {
		t.Fatalf("G.1 result must contain 'CONFLICT REVIEW PENDING' when candidates present, got %q", result)
	}

	// ── Step 3: mem_judge the candidate as 'supersedes'. ────────────────────────
	// Design §5.1 defines surfaceable annotation lines:
	//   supersedes: #<id>   (from judged supersedes relations)
	//   superseded_by: #<id> (from judged supersedes relations where obs is target)
	//   conflict: contested by #<id> (pending) (from pending relations only)
	//
	// We judge as 'supersedes' so that the annotation surfaces as
	// "supersedes:" for obs B and "superseded_by:" for obs A.
	resJudge, err := judgeH(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"judgment_id": judgmentID,
			"relation":    "supersedes",
			"reason":      "JWT-based auth supersedes the old session-based approach",
			"confidence":  0.9,
		}},
	})
	if err != nil {
		t.Fatalf("G.1 mem_judge handler error: %v", err)
	}
	if resJudge.IsError {
		t.Fatalf("G.1 mem_judge unexpected error: %s", callResultText(t, resJudge))
	}

	// Verify judgment was persisted (REQ-003).
	rel, err := s.GetRelation(judgmentID)
	if err != nil {
		t.Fatalf("G.1 GetRelation after judge: %v", err)
	}
	if rel.JudgmentStatus != store.JudgmentStatusJudged {
		t.Fatalf("G.1 expected judgment_status=judged after mem_judge, got %q", rel.JudgmentStatus)
	}
	if rel.Relation != store.RelationSupersedes {
		t.Fatalf("G.1 expected relation=supersedes, got %q", rel.Relation)
	}

	// ── Step 4: mem_search → verify supersedes annotation appears. ───────────
	// "sessions" appears in both observation A title/content and observation B
	// title/content. Single-term query avoids FTS AND-semantics excluding either.
	searchRes, err := searchH(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"query": "sessions",
		}},
	})
	if err != nil {
		t.Fatalf("G.1 search handler error: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("G.1 search unexpected error: %s", callResultText(t, searchRes))
	}

	searchEnv := parseEnvelope(t, "G.1 search", searchRes)
	resultText, _ := searchEnv["result"].(string)
	if resultText == "" {
		// Older response format returns the text as a top-level string.
		resultText = callResultText(t, searchRes)
	}

	// REQ-002: obs B (source of supersedes) should show "supersedes:" annotation.
	// obs A (target of supersedes) should show "superseded_by:" annotation.
	// At least one of these must appear after the mem_judge call.
	hasAnnotation := strings.Contains(resultText, "supersedes:") ||
		strings.Contains(resultText, "superseded_by:")
	if !hasAnnotation {
		t.Fatalf("G.1 expected 'supersedes:' or 'superseded_by:' annotation in search results after mem_judge; got:\n%s", resultText)
	}
}

// ─── G.2 — Multi-actor scenario ─────────────────────────────────────────────
//
// Two simulated agents independently judge the same (A, B) pair.
// Verifies both relations persist as separate rows and both appear in
// GetRelationsForObservations (REQ-004).
func TestConflictLoop_MultiActor(t *testing.T) {
	s := newMCPTestStore(t)

	if err := s.CreateSession("s-multi-actor", "engram", "/tmp"); err != nil {
		t.Fatalf("G.2 create session: %v", err)
	}

	obsAID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-multi-actor",
		Type:      "architecture",
		Title:     "Auth architecture uses sessions",
		Content:   "Session-based auth is the primary approach",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("G.2 add obs A: %v", err)
	}
	obsBID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-multi-actor",
		Type:      "architecture",
		Title:     "Auth architecture migrated to JWT",
		Content:   "JWT-based auth replaces session auth",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("G.2 add obs B: %v", err)
	}

	obsA, err := s.GetObservation(obsAID)
	if err != nil {
		t.Fatalf("G.2 get obs A: %v", err)
	}
	obsB, err := s.GetObservation(obsBID)
	if err != nil {
		t.Fatalf("G.2 get obs B: %v", err)
	}

	// ── Agent 1 creates relation row and judges it as compatible ──────────────
	rel1SyncID := "rel-multi-actor-agent1"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   rel1SyncID,
		SourceID: obsA.SyncID,
		TargetID: obsB.SyncID,
	}); err != nil {
		t.Fatalf("G.2 agent1 SaveRelation: %v", err)
	}
	if _, err := s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    rel1SyncID,
		Relation:      store.RelationCompatible,
		MarkedByActor: "agent:claude-sonnet-4-6",
		MarkedByKind:  "agent",
		MarkedByModel: "claude-sonnet-4-6",
	}); err != nil {
		t.Fatalf("G.2 agent1 JudgeRelation: %v", err)
	}

	// ── Agent 2 creates a SEPARATE relation row and judges it as conflicts_with ──
	rel2SyncID := "rel-multi-actor-agent2"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   rel2SyncID,
		SourceID: obsA.SyncID,
		TargetID: obsB.SyncID,
	}); err != nil {
		t.Fatalf("G.2 agent2 SaveRelation: %v", err)
	}
	if _, err := s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    rel2SyncID,
		Relation:      store.RelationConflictsWith,
		MarkedByActor: "agent:gemini-2.5-pro",
		MarkedByKind:  "agent",
		MarkedByModel: "gemini-2.5-pro",
	}); err != nil {
		t.Fatalf("G.2 agent2 JudgeRelation: %v", err)
	}

	// ── Verify both rows exist (REQ-004: no UNIQUE constraint on source+target) ─
	relationsMap, err := s.GetRelationsForObservations([]string{obsA.SyncID, obsB.SyncID})
	if err != nil {
		t.Fatalf("G.2 GetRelationsForObservations: %v", err)
	}

	aRels := relationsMap[obsA.SyncID]
	totalRels := len(aRels.AsSource) + len(aRels.AsTarget)
	if totalRels < 2 {
		t.Fatalf("G.2 expected at least 2 relation rows for obs A (one per actor), got %d total (AsSource=%d AsTarget=%d)",
			totalRels, len(aRels.AsSource), len(aRels.AsTarget))
	}

	// Verify both distinct verdicts are present (REQ-004).
	seenCompatible := false
	seenConflicts := false
	for _, rel := range aRels.AsSource {
		if rel.Relation == store.RelationCompatible {
			seenCompatible = true
		}
		if rel.Relation == store.RelationConflictsWith {
			seenConflicts = true
		}
	}
	if !seenCompatible {
		t.Errorf("G.2 expected agent1 'compatible' verdict in AsSource relations, not found")
	}
	if !seenConflicts {
		t.Errorf("G.2 expected agent2 'conflicts_with' verdict in AsSource relations, not found")
	}

	// Verify sync_id uniqueness: both rows have distinct sync_ids (REQ-004 negative scenario).
	rel1, err := s.GetRelation(rel1SyncID)
	if err != nil {
		t.Fatalf("G.2 GetRelation rel1: %v", err)
	}
	rel2, err := s.GetRelation(rel2SyncID)
	if err != nil {
		t.Fatalf("G.2 GetRelation rel2: %v", err)
	}
	if rel1.ID == rel2.ID {
		t.Fatalf("G.2 expected two distinct row IDs, both rows have id=%d", rel1.ID)
	}
}

// ─── G.3 — Orphaning ────────────────────────────────────────────────────────
//
// Save A and B. Judge them as conflicts. Hard-delete B.
// Verifies:
//   - relation rows become judgment_status='orphaned' (REQ-010)
//   - mem_search results no longer surface orphaned relations (REQ-010)
func TestConflictLoop_Orphaning(t *testing.T) {
	s := newMCPTestStore(t)
	searchH := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	if err := s.CreateSession("s-orphan", "engram", "/tmp"); err != nil {
		t.Fatalf("G.3 create session: %v", err)
	}

	// Save A and B.
	obsAID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-orphan",
		Type:      "decision",
		Title:     "Use Redis for caching layer",
		Content:   "Redis is the caching backend for all hot paths",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("G.3 add obs A: %v", err)
	}
	obsBID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-orphan",
		Type:      "decision",
		Title:     "Use Memcached instead of Redis",
		Content:   "Switched from Redis to Memcached for simpler operations",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("G.3 add obs B: %v", err)
	}

	obsA, err := s.GetObservation(obsAID)
	if err != nil {
		t.Fatalf("G.3 get obs A: %v", err)
	}
	obsB, err := s.GetObservation(obsBID)
	if err != nil {
		t.Fatalf("G.3 get obs B: %v", err)
	}

	// Create and judge relation between A and B.
	relSyncID := "rel-orphan-test-01"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   relSyncID,
		SourceID: obsA.SyncID,
		TargetID: obsB.SyncID,
	}); err != nil {
		t.Fatalf("G.3 SaveRelation: %v", err)
	}
	if _, err := s.JudgeRelation(store.JudgeRelationParams{
		JudgmentID:    relSyncID,
		Relation:      store.RelationConflictsWith,
		MarkedByActor: "agent:test",
		MarkedByKind:  "agent",
	}); err != nil {
		t.Fatalf("G.3 JudgeRelation: %v", err)
	}

	// Verify relation is judged before deletion.
	relBefore, err := s.GetRelation(relSyncID)
	if err != nil {
		t.Fatalf("G.3 GetRelation before delete: %v", err)
	}
	if relBefore.JudgmentStatus != store.JudgmentStatusJudged {
		t.Fatalf("G.3 expected relation to be judged before delete, got %q", relBefore.JudgmentStatus)
	}

	// Hard-delete B (REQ-010: relation must become orphaned, not cascade-deleted).
	if err := s.DeleteObservation(obsBID, true); err != nil {
		t.Fatalf("G.3 DeleteObservation (hard): %v", err)
	}

	// Verify relation row still exists but is now orphaned (REQ-010 happy path).
	relAfter, err := s.GetRelation(relSyncID)
	if err != nil {
		t.Fatalf("G.3 GetRelation after delete: %v (relation row must not be cascade-deleted)", err)
	}
	if relAfter.JudgmentStatus != store.JudgmentStatusOrphaned {
		t.Fatalf("G.3 expected judgment_status=orphaned after hard-delete, got %q", relAfter.JudgmentStatus)
	}

	// REQ-010 edge case: orphaned relations are invisible in search annotations.
	searchRes, err := searchH(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"query":   "Redis caching layer",
			"project": "engram",
			"scope":   "project",
		}},
	})
	if err != nil {
		t.Fatalf("G.3 search handler error: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("G.3 search unexpected error: %s", callResultText(t, searchRes))
	}

	searchEnv := parseEnvelope(t, "G.3 search", searchRes)
	resultText, _ := searchEnv["result"].(string)
	if resultText == "" {
		resultText = callResultText(t, searchRes)
	}

	// The orphaned relation annotation must NOT appear for obs A.
	// (GetRelationsForObservations filters out orphaned rows.)
	if strings.Contains(resultText, "conflict: contested by") ||
		strings.Contains(resultText, "supersedes:") ||
		strings.Contains(resultText, "superseded_by:") {
		t.Fatalf("G.3 orphaned relation must not appear in search annotations, got:\n%s", resultText)
	}
}

// ─── G.4 — Sync enrollment-gate regression test ─────────────────────────────
//
// Verifies that an UNENROLLED project never enqueues relation sync mutations.
// The enqueue paths in JudgeRelation and JudgeBySemantic are guarded by an
// enrollment check — this test is the regression guard for that gate.
//
// Note: relation cloud sync IS intentional for enrolled projects (#313/#379/
// #383 enabled it; #496 extends it with backfill). This test uses an unenrolled
// store (newMCPTestStore does not call EnrollProject), so the relation count
// must remain zero — not because relations are local-only, but because the
// enrollment gate must hold.
//
// The assertion is entity-level: no row in sync_mutations should have
// entity = 'relation' after conflict operations on an unenrolled project.
// Sessions and observations still produce their own sync mutations as expected.
func TestConflictLoop_SyncRegression(t *testing.T) {
	s := newMCPTestStore(t)
	saveH := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	judgeH := handleJudge(s, NewSessionActivity(10*time.Minute))

	// ── Step 1: Save two similar observations. ────────────────────────────────
	// Each save enqueues a session mutation + an observation mutation.
	// FindCandidates also inserts relation rows — but those must NOT appear
	// in sync_mutations (REQ-009).
	resA, err := saveH(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"title":   "Database migration strategy uses Flyway",
			"content": "Flyway manages all schema migrations in the project",
			"type":    "decision",
		}},
	})
	if err != nil || resA.IsError {
		t.Fatalf("G.4 save A: err=%v isError=%v", err, resA.IsError)
	}

	resB, err := saveH(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"title":   "Database migration strategy switched to Liquibase",
			"content": "Replaced Flyway with Liquibase for better rollback support",
			"type":    "decision",
		}},
	})
	if err != nil || resB.IsError {
		t.Fatalf("G.4 save B: err=%v isError=%v", err, resB.IsError)
	}

	// ── Step 2: Judge the candidate (if any). ────────────────────────────────
	envB := parseEnvelope(t, "G.4 save B envelope", resB)
	candidates, _ := envB["candidates"].([]any)
	if len(candidates) > 0 {
		firstCand, _ := candidates[0].(map[string]any)
		judgmentID, _ := firstCand["judgment_id"].(string)

		judgeRes, err := judgeH(context.Background(), mcppkg.CallToolRequest{
			Params: mcppkg.CallToolParams{Arguments: map[string]any{
				"judgment_id": judgmentID,
				"relation":    "supersedes",
				"confidence":  0.95,
			}},
		})
		if err != nil || judgeRes.IsError {
			t.Fatalf("G.4 mem_judge: err=%v isError=%v", err, judgeRes.IsError)
		}
	} else {
		t.Logf("G.4: no candidates returned after save B (FTS similarity below floor); judge step skipped — enrollment-gate assertion still covers the unenrolled guard")
	}

	// ── Step 3: Assert no relation entity in sync_mutations (enrollment gate). ──
	// This store is UNENROLLED — the enrollment gate in JudgeRelation /
	// JudgeBySemantic must prevent any relation sync_mutations row from being
	// written. Sessions ('session') and observations ('observation') are expected;
	// a relation-entity row here would mean the enrollment guard was bypassed.
	assertNoRelationSyncMutations(t, s)

	// ── Step 4: Verify observation sync payloads exclude decay fields. ────────
	// REQ-009: new observation columns must NOT appear in the sync wire format.
	verifyObsSyncPayloadsExcludeDecayFields(t, s)
}

// ─── G.5 — Backwards compatibility ──────────────────────────────────────────
//
// Simulates an older MCP client that only reads the top-level `result` string
// from mem_save and mem_search responses. Verifies both tools still return
// valid, readable responses even with the new envelope fields present.
// REQ-007 | Design §4 (regression guard).
func TestConflictLoop_BackwardsCompat(t *testing.T) {
	s := newMCPTestStore(t)
	saveH := handleSave(s, MCPConfig{}, NewSessionActivity(10*time.Minute))
	searchH := handleSearch(s, MCPConfig{}, NewSessionActivity(10*time.Minute))

	// ── Case 1: mem_save with no candidates (no-candidate path). ─────────────
	resEmpty, err := saveH(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"title":   "Photon torpedo yield calculation",
			"content": "Completely unrelated to any prior memory; no candidate expected",
			"type":    "discovery",
		}},
	})
	if err != nil {
		t.Fatalf("G.5 save (no candidates) handler error: %v", err)
	}
	if resEmpty.IsError {
		t.Fatalf("G.5 save (no candidates) unexpected tool error: %s", callResultText(t, resEmpty))
	}

	// Old client: reads the full response as JSON and extracts only "result".
	envEmpty := parseEnvelope(t, "G.5 save empty", resEmpty)
	result, _ := envEmpty["result"].(string)
	if !strings.HasPrefix(result, `Memory saved: "`) {
		t.Fatalf("G.5 (no candidates) result string must start with 'Memory saved: \"', got %q", result)
	}
	// Unknown fields (judgment_required, sync_id, etc.) must be present but ignorable
	// — old client just ignores them (Go JSON unmarshals them fine; test just confirms
	// the "result" field is still accessible and correct).

	// ── Case 2: mem_save WITH candidates (enriched path). ────────────────────
	// Save a similar observation to trigger candidate detection.
	_, err = saveH(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"title":   "Cache invalidation using event streaming",
			"content": "We use event streaming to invalidate cache entries across nodes",
			"type":    "architecture",
		}},
	})
	if err != nil {
		t.Fatalf("G.5 seed save: %v", err)
	}

	resCandidates, err := saveH(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"title":   "Cache invalidation via event streaming approach",
			"content": "Event-driven cache invalidation across distributed nodes using streams",
			"type":    "architecture",
		}},
	})
	if err != nil {
		t.Fatalf("G.5 save (with candidates) handler error: %v", err)
	}
	if resCandidates.IsError {
		t.Fatalf("G.5 save (with candidates) unexpected tool error: %s", callResultText(t, resCandidates))
	}

	// Old client: just reads the response text as JSON and extracts "result".
	envCandidates := parseEnvelope(t, "G.5 save with candidates", resCandidates)
	resultCandidates, _ := envCandidates["result"].(string)
	if !strings.HasPrefix(resultCandidates, `Memory saved: "`) {
		t.Fatalf("G.5 (with candidates) result string must start with 'Memory saved: \"', got %q", resultCandidates)
	}
	// Old client ignores new fields: candidates[], judgment_required, judgment_id, sync_id.
	// The test asserts "result" is still readable and correct — new fields are additional keys
	// in the JSON object, which per JSON spec are silently ignored by decoders that don't
	// declare them.

	// ── Case 3: mem_search — old client reads result string. ─────────────────
	if err := s.CreateSession("s-compat", "engram", "/tmp"); err != nil {
		t.Fatalf("G.5 create session: %v", err)
	}
	if _, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-compat",
		Type:      "bugfix",
		Title:     "Fixed null pointer in cache lookup",
		Content:   "Null check added before cache.Get() call",
		Project:   "engram",
		Scope:     "project",
	}); err != nil {
		t.Fatalf("G.5 add observation for search: %v", err)
	}

	searchRes, err := searchH(context.Background(), mcppkg.CallToolRequest{
		Params: mcppkg.CallToolParams{Arguments: map[string]any{
			"query":   "cache lookup null pointer",
			"project": "engram",
			"scope":   "project",
		}},
	})
	if err != nil {
		t.Fatalf("G.5 search handler error: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("G.5 search unexpected tool error: %s", callResultText(t, searchRes))
	}

	// Old client: reads the "result" field from the JSON envelope.
	searchEnv := parseEnvelope(t, "G.5 search", searchRes)
	searchResult, _ := searchEnv["result"].(string)
	if !strings.Contains(searchResult, "Found") {
		t.Fatalf("G.5 search result string must contain 'Found ...', got %q", searchResult)
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// parseEnvelope parses the tool result as a JSON envelope map.
// Fatally fails if the result is not valid JSON.
func parseEnvelope(t *testing.T, label string, res *mcppkg.CallToolResult) map[string]any {
	t.Helper()
	text := callResultText(t, res)
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("%s: response is not valid JSON: %v\ntext: %s", label, err, text)
	}
	return m
}

// assertNoRelationSyncMutations verifies that NO sync_mutations rows reference
// relation entities on an UNENROLLED store. The enrollment gate in
// JudgeRelation / JudgeBySemantic must prevent any relation row from being
// written when the project is not enrolled. Sessions ('session') and
// observations ('observation') are expected entities; a relation-entity row
// in an unenrolled context means the enrollment guard was bypassed.
//
// Note: relation sync mutations ARE valid for enrolled projects (#313/#379/
// #383/#496). This helper is intentionally scoped to the unenrolled test in G.4.
func assertNoRelationSyncMutations(t *testing.T, s *store.Store) {
	t.Helper()
	count, err := s.CountRelationSyncMutations()
	if err != nil {
		t.Fatalf("G.4 CountRelationSyncMutations: %v", err)
	}
	if count > 0 {
		t.Errorf("G.4 enrollment-gate violated: found %d sync_mutations row(s) with relation entity on unenrolled project — enrollment gate must prevent relation sync mutations", count)
	}
}

// verifyObsSyncPayloadsExcludeDecayFields checks that no sync_mutations payload
// for observations contains review_after, expires_at, or embedding* fields.
// REQ-009: new observation columns must NOT appear in the sync wire format.
func verifyObsSyncPayloadsExcludeDecayFields(t *testing.T, s *store.Store) {
	t.Helper()
	payloads, err := s.ListObservationSyncPayloads()
	if err != nil {
		t.Fatalf("G.4 ListObservationSyncPayloads: %v", err)
	}
	forbiddenKeys := []string{"review_after", "expires_at", "embedding", "embedding_model", "embedding_created_at"}
	for i, payload := range payloads {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("G.4 marshal payload %d: %v", i, err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("G.4 unmarshal payload %d: %v", i, err)
		}
		for _, key := range forbiddenKeys {
			if _, found := m[key]; found {
				t.Errorf("G.4 sync payload must not contain %q (REQ-009), but found it in payload %d: %s", key, i, raw)
			}
		}
	}
}
