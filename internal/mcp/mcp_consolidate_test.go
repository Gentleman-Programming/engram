package mcp

// mem_consolidate handler tests (engram#242, slices 3 / S5 + S7).
// The plan/confirm protocol: dry-run is the default and writes nothing;
// confirm executes only against the matching plan fingerprint, and a stale
// fingerprint refuses while surfacing the fresh one for re-planning.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

// seedConsolidateFixture creates one session and three live observations.
func seedConsolidateFixture(t *testing.T, s *store.Store) []int64 {
	t.Helper()
	if err := s.CreateSession("s-cons-mcp", "engram", "/tmp"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	var ids []int64
	for _, title := range []string{"Decision v1", "Decision v2", "Decision v3"} {
		id, err := s.AddObservation(store.AddObservationParams{
			SessionID: "s-cons-mcp",
			Type:      "decision",
			Title:     title,
			Content:   "Iteration of the same decision: " + title,
			Project:   "engram",
			Scope:     "project",
		})
		if err != nil {
			t.Fatalf("add obs %s: %v", title, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func liveObservationCount(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM observations WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	return n
}

func consolidateRequest(ids []int64, extra map[string]any) mcppkg.CallToolRequest {
	args := map[string]any{"ids": []any{}}
	for _, id := range ids {
		args["ids"] = append(args["ids"].([]any), float64(id))
	}
	for k, v := range extra {
		args[k] = v
	}
	return mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}}
}

// S5: without confirm the call is a dry-run — full plan, fingerprint, and
// zero writes.
func TestHandleConsolidate_DryRunIsDefaultAndWritesNothing(t *testing.T) {
	s := newMCPTestStore(t)
	ids := seedConsolidateFixture(t, s)
	req := consolidateRequest(ids, nil)

	res, err := handleConsolidate(s)(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", callResultText(t, res))
	}
	text := callResultText(t, res)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatalf("response not valid JSON: %v — got %q", err, text)
	}
	if dry, _ := envelope["dry_run"].(bool); !dry {
		t.Fatalf("default call must report dry_run=true, got %v", envelope)
	}
	fp, _ := envelope["plan_fingerprint"].(string)
	if fp == "" {
		t.Fatalf("plan must include a non-empty plan_fingerprint, got %v", envelope)
	}
	if sources, _ := envelope["source_count"].(float64); sources != 3 {
		t.Fatalf("plan must report source_count=3, got %v", envelope)
	}
	if got := liveObservationCount(t, s); got != 3 {
		t.Fatalf("dry-run must not write, live count %d", got)
	}
}

// S5: confirm + matching fingerprint executes atomically.
func TestHandleConsolidate_ConfirmExecutesWithFingerprint(t *testing.T) {
	s := newMCPTestStore(t)
	ids := seedConsolidateFixture(t, s)

	planRes, err := handleConsolidate(s)(context.Background(), consolidateRequest(ids, map[string]any{
		"title": "Consolidated decision",
	}))
	if err != nil || planRes.IsError {
		t.Fatalf("plan call failed: %v / %s", err, callResultText(t, planRes))
	}
	var plan map[string]any
	if err := json.Unmarshal([]byte(callResultText(t, planRes)), &plan); err != nil {
		t.Fatalf("plan not JSON: %v", err)
	}
	fp, _ := plan["plan_fingerprint"].(string)
	if fp == "" {
		t.Fatal("missing fingerprint")
	}

	res, err := handleConsolidate(s)(context.Background(), consolidateRequest(ids, map[string]any{
		"confirm":          true,
		"plan_fingerprint": fp,
		"title":            "Consolidated decision",
	}))
	if err != nil {
		t.Fatalf("confirm error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected confirm error: %s", callResultText(t, res))
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(callResultText(t, res)), &envelope); err != nil {
		t.Fatalf("confirm response not JSON: %v", err)
	}
	if dry, _ := envelope["dry_run"].(bool); dry {
		t.Fatalf("confirm must not report dry_run, got %v", envelope)
	}
	if survivor, _ := envelope["survivor_id"].(float64); survivor == 0 {
		t.Fatalf("confirm must return the survivor id, got %v", envelope)
	}
	if got := liveObservationCount(t, s); got != 1 {
		t.Fatalf("after consolidation live count must be 1, got %d", got)
	}
}

// S3/S5: a stale fingerprint refuses and surfaces the fresh fingerprint so
// the caller re-plans instead of guessing.
func TestHandleConsolidate_StaleFingerprintSurfacesFreshPlan(t *testing.T) {
	s := newMCPTestStore(t)
	ids := seedConsolidateFixture(t, s)

	planRes, _ := handleConsolidate(s)(context.Background(), consolidateRequest(ids, nil))
	var plan map[string]any
	_ = json.Unmarshal([]byte(callResultText(t, planRes)), &plan)
	stale, _ := plan["plan_fingerprint"].(string)

	res, err := handleConsolidate(s)(context.Background(), consolidateRequest(ids, map[string]any{
		"confirm":          true,
		"plan_fingerprint": "deadbeef" + stale,
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("stale fingerprint must refuse, got %s", callResultText(t, res))
	}
	msg := callResultText(t, res)
	if !strings.Contains(msg, store.ErrConsolidateFingerprintMismatch.Error()) {
		t.Fatalf("error must name the mismatch: %q", msg)
	}

	// Re-plan after the refusal returns the sources' current fingerprint —
	// identical here because the sources never changed (the caller's token was
	// garbage); a real edit between plan and confirm is what diverges them
	// (covered at the store level by the fingerprint-drift test).
	replan, _ := handleConsolidate(s)(context.Background(), consolidateRequest(ids, nil))
	if replan.IsError {
		t.Fatalf("re-plan failed: %s", callResultText(t, replan))
	}
	var fresh map[string]any
	_ = json.Unmarshal([]byte(callResultText(t, replan)), &fresh)
	freshFP, _ := fresh["plan_fingerprint"].(string)
	if freshFP == "" {
		t.Fatal("re-plan must return a fingerprint")
	}
	if !strings.Contains(msg, freshFP) {
		t.Fatalf("mismatch error must surface the fresh plan_fingerprint %q, got %q", freshFP, msg)
	}
	if got := liveObservationCount(t, s); got != 3 {
		t.Fatalf("refused confirm must not write, live count %d", got)
	}
}

// S3: fewer than two IDs is refused before touching the store.
func TestHandleConsolidate_RejectsFewerThanTwoIDs(t *testing.T) {
	s := newMCPTestStore(t)
	ids := seedConsolidateFixture(t, s)

	res, err := handleConsolidate(s)(context.Background(), consolidateRequest(ids[:1], nil))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("single id must refuse, got %s", callResultText(t, res))
	}
}

// S5 hardening: a mistyped truthy dry_run must not bypass the explicit
// contradiction guard, and fractional IDs refuse instead of truncating —
// neither may write.
func TestHandleConsolidate_MalformedArgumentsRefuse(t *testing.T) {
	s := newMCPTestStore(t)
	ids := seedConsolidateFixture(t, s)

	res, err := handleConsolidate(s)(context.Background(), consolidateRequest(ids, map[string]any{
		"confirm": true,
		"dry_run": "true",
	}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("mistyped dry_run with confirm must refuse, got %s", callResultText(t, res))
	}

	fractional := consolidateRequest(ids, nil)
	fractional.Params.Arguments.(map[string]any)["ids"] = []any{float64(ids[0]), float64(ids[1]) + 0.5}
	res2, err := handleConsolidate(s)(context.Background(), fractional)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res2.IsError {
		t.Fatalf("fractional ids must refuse, got %s", callResultText(t, res2))
	}

	// Mixed-type arrays reject the ENTIRE input: malformed entries are never
	// silently skipped, so a destructive tool can never act on a partially
	// parsed ID list (CodeRabbit finding on #1814).
	mixed := consolidateRequest(ids, nil)
	mixed.Params.Arguments.(map[string]any)["ids"] = []any{float64(ids[0]), float64(ids[1]), "13"}
	res3, err := handleConsolidate(s)(context.Background(), mixed)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res3.IsError {
		t.Fatalf("mixed-type ids must refuse the entire input, got %s", callResultText(t, res3))
	}
	if got := liveObservationCount(t, s); got != 3 {
		t.Fatalf("malformed arguments must not write, live count %d", got)
	}
}

// S7: the tool is registered for agents, deferred, and counted as a memory
// writer.
func TestMemConsolidate_ProfileRegistration(t *testing.T) {
	if !ProfileAgent["mem_consolidate"] {
		t.Fatal("mem_consolidate must be in ProfileAgent")
	}
	foundDeferred, foundWriter := false, false
	for _, name := range deferredTools {
		if name == "mem_consolidate" {
			foundDeferred = true
		}
	}
	for _, name := range memoryWritingTools {
		if name == "mem_consolidate" {
			foundWriter = true
		}
	}
	if !foundDeferred {
		t.Fatal("mem_consolidate must be deferred")
	}
	if !foundWriter {
		t.Fatal("mem_consolidate must be a memory-writing tool")
	}
}
