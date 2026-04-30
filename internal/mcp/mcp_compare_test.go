package mcp

// Phase G.1 — mem_compare handler tests.
// REQ-011 | Design §9
//
// mem_compare lets an agent that has already judged two observations externally
// (via its own LLM) persist the verdict into Engram via JudgeBySemantic.
// The agent provides int IDs; the handler resolves them to sync_ids.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// seedCompareFixture creates a session and two observations.
// Returns the integer IDs of both observations.
func seedCompareFixture(t *testing.T, s *store.Store) (idA, idB int64) {
	t.Helper()
	if err := s.CreateSession("s-compare", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	a, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-compare",
		Type:      "architecture",
		Title:     "JWT auth decision",
		Content:   "We use JWT for auth",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs A: %v", err)
	}
	b, err := s.AddObservation(store.AddObservationParams{
		SessionID: "s-compare",
		Type:      "architecture",
		Title:     "Session auth decision",
		Content:   "We use sessions for auth",
		Project:   "engram",
		Scope:     "project",
	})
	if err != nil {
		t.Fatalf("add obs B: %v", err)
	}
	return a, b
}

// TestHandleCompare_HappyPath — valid params persists a relation row and returns sync_id.
// REQ-011 happy path | Design §9
func TestHandleCompare_HappyPath(t *testing.T) {
	s := newMCPTestStore(t)
	idA, idB := seedCompareFixture(t, s)

	h := handleCompare(s, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"memory_id_a": float64(idA),
		"memory_id_b": float64(idB),
		"relation":    "supersedes",
		"confidence":  float64(0.98),
		"reasoning":   "newer post supersedes the older one",
		"model":       "claude-haiku-4-5",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCompare error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("response not valid JSON: %v — got %q", err, text)
	}

	syncID, ok := envelope["sync_id"].(string)
	if !ok || syncID == "" {
		t.Fatalf("expected non-empty sync_id in response, got %v", envelope)
	}
}

// TestHandleCompare_NotConflict_NoRow — not_conflict returns success without inserting a row.
// REQ-011 | Design §9 (not_conflict is still persisted but JudgeBySemantic handles it as no-op)
func TestHandleCompare_NotConflict_NoRow(t *testing.T) {
	s := newMCPTestStore(t)
	idA, idB := seedCompareFixture(t, s)

	h := handleCompare(s, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"memory_id_a": float64(idA),
		"memory_id_b": float64(idB),
		"relation":    "not_conflict",
		"confidence":  float64(0.99),
		"reasoning":   "these are about different topics",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCompare error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error for not_conflict: %s", callResultText(t, res))
	}

	text := callResultText(t, res)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}

	// not_conflict is a no-op — sync_id should be empty string
	syncID, _ := envelope["sync_id"].(string)
	if syncID != "" {
		t.Fatalf("expected empty sync_id for not_conflict, got %q", syncID)
	}
}

// TestHandleCompare_MissingMemoryIDB — missing memory_id_b returns IsError=true.
// REQ-011 validation | Design §9
func TestHandleCompare_MissingMemoryIDB(t *testing.T) {
	s := newMCPTestStore(t)
	idA, _ := seedCompareFixture(t, s)

	h := handleCompare(s, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"memory_id_a": float64(idA),
		// memory_id_b omitted
		"relation":   "compatible",
		"confidence": float64(0.9),
		"reasoning":  "they are compatible",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCompare error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true when memory_id_b missing, got success: %s", callResultText(t, res))
	}
}

// TestHandleCompare_InvalidRelation — invalid relation enum returns IsError=true.
// REQ-011 validation | Design §9
func TestHandleCompare_InvalidRelation(t *testing.T) {
	s := newMCPTestStore(t)
	idA, idB := seedCompareFixture(t, s)

	h := handleCompare(s, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"memory_id_a": float64(idA),
		"memory_id_b": float64(idB),
		"relation":    "totally_invalid_verb",
		"confidence":  float64(0.9),
		"reasoning":   "some reasoning",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCompare error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for invalid relation verb")
	}
}

// TestHandleCompare_NonExistentObservation — non-existent memory_id_a returns descriptive error.
// REQ-011 negative | Design §9
func TestHandleCompare_NonExistentObservation(t *testing.T) {
	s := newMCPTestStore(t)
	_, idB := seedCompareFixture(t, s)

	h := handleCompare(s, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"memory_id_a": float64(9999),
		"memory_id_b": float64(idB),
		"relation":    "conflicts_with",
		"confidence":  float64(0.8),
		"reasoning":   "they conflict",
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCompare error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected IsError=true for non-existent observation id=9999")
	}
	if !strings.Contains(callResultText(t, res), "9999") {
		t.Fatalf("expected error message to mention the unknown id, got %q", callResultText(t, res))
	}
}

// TestHandleCompare_Idempotency — re-calling same pair updates existing row.
// REQ-011 | Design §9 (JudgeBySemantic uses UPSERT)
func TestHandleCompare_Idempotency(t *testing.T) {
	s := newMCPTestStore(t)
	idA, idB := seedCompareFixture(t, s)

	h := handleCompare(s, NewSessionActivity(10*time.Minute))

	// First call: supersedes
	req1 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"memory_id_a": float64(idA),
		"memory_id_b": float64(idB),
		"relation":    "supersedes",
		"confidence":  float64(0.8),
		"reasoning":   "first verdict",
	}}}
	res1, err := h(context.Background(), req1)
	if err != nil || res1.IsError {
		t.Fatalf("first call failed: %v / %s", err, callResultText(t, res1))
	}
	var env1 map[string]any
	_ = json.Unmarshal([]byte(callResultText(t, res1)), &env1)
	syncID1, _ := env1["sync_id"].(string)

	// Second call: compatible (overwrite)
	req2 := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"memory_id_a": float64(idA),
		"memory_id_b": float64(idB),
		"relation":    "compatible",
		"confidence":  float64(0.95),
		"reasoning":   "second verdict overwrite",
	}}}
	res2, err := h(context.Background(), req2)
	if err != nil || res2.IsError {
		t.Fatalf("second call failed: %v / %s", err, callResultText(t, res2))
	}
	var env2 map[string]any
	_ = json.Unmarshal([]byte(callResultText(t, res2)), &env2)
	syncID2, _ := env2["sync_id"].(string)

	// Both calls should return a valid sync_id
	if syncID1 == "" || syncID2 == "" {
		t.Fatalf("expected non-empty sync_ids: first=%q second=%q", syncID1, syncID2)
	}
}

// TestHandleCompare_ProfileAgent — mem_compare is in ProfileAgent.
// Tool count: ProfileAgent should contain mem_compare after Phase G.
func TestHandleCompare_ProfileAgent(t *testing.T) {
	if !ProfileAgent["mem_compare"] {
		t.Errorf("expected mem_compare in ProfileAgent, but it is absent")
	}
}

// TestHandleCompare_ModelOptional — omitting 'model' field succeeds (model is optional).
// REQ-011 | Design §9 schema
func TestHandleCompare_ModelOptional(t *testing.T) {
	s := newMCPTestStore(t)
	idA, idB := seedCompareFixture(t, s)

	h := handleCompare(s, NewSessionActivity(10*time.Minute))
	req := mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{
		"memory_id_a": float64(idA),
		"memory_id_b": float64(idB),
		"relation":    "related",
		"confidence":  float64(0.85),
		"reasoning":   "they are related topics",
		// model intentionally omitted
	}}}

	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCompare error: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success when model omitted, got error: %s", callResultText(t, res))
	}
}
