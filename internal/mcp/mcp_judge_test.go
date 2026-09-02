package mcp

// Phase D.5 — mem_judge handler tests.
// REQ-003 | Design §6

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// seedJudgeFixture creates a session, two observations, and a pending relation.
// Returns the sync_id of the pending relation row (judgment_id).
func seedJudgeFixture(t *testing.T, s *store.Store) (judgmentID string, sourceSyncID string, targetSyncID string) {
	t.Helper()
	if err := s.CreateSession("s-judge", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	srcID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-judge",
		Type:      "architecture",
		Title:     "Old auth using sessions",
		Content:   "We used session-based auth",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add source obs: %v", err)
	}
	tgtID, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-judge",
		Type:      "architecture",
		Title:     "New auth using JWT",
		Content:   "Switched to JWT-based auth",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add target obs: %v", err)
	}

	srcObs, err := s.GetObservation(srcID)
	if err != nil {
		t.Fatalf("get src obs: %v", err)
	}
	tgtObs, err := s.GetObservation(tgtID)
	if err != nil {
		t.Fatalf("get tgt obs: %v", err)
	}

	judgmentID = "rel-test-judge-happy-01"
	if _, err := s.SaveRelation(store.SaveRelationParams{
		SyncID:   judgmentID,
		SourceID: srcObs.SyncID,
		TargetID: tgtObs.SyncID,
	}); err != nil {
		t.Fatalf("save relation: %v", err)
	}
	return judgmentID, srcObs.SyncID, tgtObs.SyncID
}

// TestHandleJudge_HappyPath — judging a valid pending relation creates a judged row.
// REQ-003 happy path | Design §6
func TestHandleJudge_HappyPath(t *testing.T) {
	s := newMCPTestStore(t)
	judgmentID, _, _ := seedJudgeFixture(t, s)

	h := handleJudge(s, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"judgment_id": judgmentID,
		"relation":    "not_conflict",
		"confidence":  0.9,
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleJudge error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("response not valid JSON: %v — got %q", err, text)
	}

	relation, _ := envelope["relation"].(map[string]any)
	if relation == nil {
		t.Fatalf("expected relation object in response, got %v", envelope)
	}

	if relation["judgment_status"] != "judged" {
		t.Fatalf("expected judgment_status=judged, got %v", relation["judgment_status"])
	}
	if relation["relation"] != "not_conflict" {
		t.Fatalf("expected relation=not_conflict, got %v", relation["relation"])
	}
	if conf, ok := relation["confidence"].(float64); !ok || conf != 0.9 {
		t.Fatalf("expected confidence=0.9, got %v", relation["confidence"])
	}
}

// TestHandleJudge_OptionalFieldsStayNull — omitting optional fields leaves them NULL.
// REQ-003 edge case | Design §6.2
func TestHandleJudge_OptionalFieldsStayNull(t *testing.T) {
	s := newMCPTestStore(t)
	judgmentID, _, _ := seedJudgeFixture(t, s)

	h := handleJudge(s, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"judgment_id": judgmentID,
		"relation":    "related",
		// reason, evidence, confidence all omitted
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleJudge error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", callResultText(t, res))
	}

	// Verify by reading the relation back from the store.
	rel, err := s.GetRelation(judgmentID)
	if err != nil {
		t.Fatalf("get relation: %v", err)
	}
	if rel.Reason != nil {
		t.Fatalf("expected reason=nil, got %v", *rel.Reason)
	}
	if rel.Evidence != nil {
		t.Fatalf("expected evidence=nil, got %v", *rel.Evidence)
	}
	if rel.Confidence != nil {
		t.Fatalf("expected confidence=nil, got %v", *rel.Confidence)
	}
	if rel.JudgmentStatus != store.JudgmentStatusJudged {
		t.Fatalf("expected judgment_status=judged, got %v", rel.JudgmentStatus)
	}
}

// TestHandleJudge_UnknownID_IsError — unknown judgment_id returns IsError=true.
// REQ-003 negative | Design §6.3
func TestHandleJudge_UnknownID_IsError(t *testing.T) {
	s := newMCPTestStore(t)

	h := handleJudge(s, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"judgment_id": "rel-does-not-exist",
		"relation":    "not_conflict",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleJudge error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for unknown judgment_id, got success: %s", callResultText(t, res))
	}
	if !strings.Contains(callResultText(t, res), "not found") {
		t.Fatalf("expected 'not found' in error message, got %q", callResultText(t, res))
	}
}

// TestHandleJudge_InvalidVerb_IsError — invalid relation verb returns IsError=true, row unchanged.
// REQ-003 negative | Design §6.3
func TestHandleJudge_InvalidVerb_IsError(t *testing.T) {
	s := newMCPTestStore(t)
	judgmentID, _, _ := seedJudgeFixture(t, s)

	h := handleJudge(s, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"judgment_id": judgmentID,
		"relation":    "invalidverb",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleJudge error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for invalid relation verb")
	}

	// Row must remain pending.
	rel, err := s.GetRelation(judgmentID)
	if err != nil {
		t.Fatalf("get relation: %v", err)
	}
	if rel.JudgmentStatus != store.JudgmentStatusPending {
		t.Fatalf("expected row to remain pending, got %v", rel.JudgmentStatus)
	}
}

// TestHandleJudge_Idempotent_Overwrite — re-judging overwrites the existing verdict.
// REQ-003 | Design §6.4
func TestHandleJudge_Idempotent_Overwrite(t *testing.T) {
	s := newMCPTestStore(t)
	judgmentID, _, _ := seedJudgeFixture(t, s)

	h := handleJudge(s, NewSessionActivity(10*time.Minute))

	// First verdict.
	req1 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"judgment_id": judgmentID,
		"relation":    "not_conflict",
	}}}
	res1, err := h(context.Background(), req1)
	if err != nil || res1.IsError {
		t.Fatalf("first judge failed: %v / %s", err, callResultText(t, res1))
	}

	// Second verdict — overwrite.
	req2 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"judgment_id": judgmentID,
		"relation":    "supersedes",
		"confidence":  0.7,
	}}}
	res2, err := h(context.Background(), req2)
	if err != nil {
		t.Fatalf("second judge error: %v", err)
	}
	if res2.IsError {
		t.Fatalf("expected overwrite to succeed, got error: %s", callResultText(t, res2))
	}

	rel, err := s.GetRelation(judgmentID)
	if err != nil {
		t.Fatalf("get relation: %v", err)
	}
	if rel.Relation != store.RelationSupersedes {
		t.Fatalf("expected relation=supersedes after overwrite, got %v", rel.Relation)
	}
	if rel.Confidence == nil || *rel.Confidence != 0.7 {
		t.Fatalf("expected confidence=0.7 after overwrite, got %v", rel.Confidence)
	}
}

// TestHandleJudge_RequiresJudgmentID — missing judgment_id returns IsError=true.
func TestHandleJudge_RequiresJudgmentID(t *testing.T) {
	s := newMCPTestStore(t)
	h := handleJudge(s, NewSessionActivity(10*time.Minute))

	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"relation": "not_conflict",
		// judgment_id omitted
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleJudge error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true when judgment_id missing")
	}
}

// TestHandleJudge_RequiresRelation — missing relation returns IsError=true.
func TestHandleJudge_RequiresRelation(t *testing.T) {
	s := newMCPTestStore(t)
	judgmentID, _, _ := seedJudgeFixture(t, s)

	h := handleJudge(s, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"judgment_id": judgmentID,
		// relation omitted
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleJudge error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true when relation missing")
	}
}
