package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"github.com/mark3labs/mcp-go/server"
)

func batchCall(t *testing.T, srv *server.MCPServer, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(srv.HandleMessage(context.Background(), raw))
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Error  any `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Error != nil || len(wire.Result.Content) != 1 {
		t.Fatalf("unexpected response %s", encoded)
	}
	text := wire.Result.Content[0].Text
	if wire.Result.IsError {
		return map[string]any{"error": text}, true
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	return out, false
}

func batchFixture(t *testing.T) (*store.Store, *server.MCPServer, string, string) {
	t.Helper()
	s := newMCPTestStore(t)
	for _, p := range []string{"batch-project", "other-project"} {
		if err := s.CreateSession(p, p, ""); err != nil {
			t.Fatal(err)
		}
	}
	add := func(project, title string) string {
		id, err := s.AddObservation(store.AddObservationParams{SessionID: project, Project: project, Title: title, Content: title, Type: "decision"})
		if err != nil {
			t.Fatal(err)
		}
		obs, err := s.GetObservation(id)
		if err != nil {
			t.Fatal(err)
		}
		return obs.SyncID
	}
	a, b, c := add("batch-project", "first"), add("batch-project", "second"), add("other-project", "third")
	for id, target := range map[string]string{"rel-batch": b, "rel-cross": c} {
		if _, err := s.SaveRelation(store.SaveRelationParams{SyncID: id, SourceID: a, TargetID: target}); err != nil {
			t.Fatal(err)
		}
	}
	return s, NewServer(s), "rel-batch", "rel-cross"
}

func batchRelation(t *testing.T, s *store.Store, id string) *store.Relation {
	t.Helper()
	r, err := s.GetRelation(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func batchMutations(t *testing.T, s *store.Store) []store.SyncMutation {
	t.Helper()
	m, err := s.ListPendingProjectMutations("")
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func batchItem(id, verb string) map[string]any {
	return map[string]any{"judgment_id": id, "relation": verb}
}
func batchOutcomes(t *testing.T, out map[string]any) []any {
	t.Helper()
	items, ok := out["outcomes"].([]any)
	if !ok {
		t.Fatalf("missing outcomes: %#v", out)
	}
	return items
}

func TestJudgeManyPersistenceIsolationSync(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "enrolled"}[enrolled], func(t *testing.T) {
			s, srv, id, cross := batchFixture(t)
			if enrolled {
				if err := s.EnrollProject("batch-project"); err != nil {
					t.Fatal(err)
				}
			}
			initial := batchMutations(t, s)
			beforeCross := batchRelation(t, s, cross)
			revision := batchItem(id, "compatible")
			revision["reason"] = "revised"
			revision["evidence"] = "user approved"
			revision["confidence"] = 0.9
			items := []any{batchItem(id, "related"), batchItem(cross, "supersedes"), nil, batchItem("missing", "related"), map[string]any{"judgment_id": id, "relation": "invalid"}, map[string]any{"judgment_id": id, "relation": "related", "confidence": "bad"}, revision}
			for attempt := 0; attempt < 2; attempt++ {
				out, failed := batchCall(t, srv, "mem_judge_many", map[string]any{"judgments": items, "session_id": "batch-project"})
				if failed {
					t.Fatalf("batch failed %#v", out)
				}
				outcomes := batchOutcomes(t, out)
				if len(outcomes) != len(items) {
					t.Fatal(outcomes)
				}
				for i, raw := range outcomes {
					item := raw.(map[string]any)
					if item["index"] != float64(i) {
						t.Fatal(item)
					}
					expectedID := any(nil)
					if args, ok := items[i].(map[string]any); ok {
						expectedID = args["judgment_id"]
					}
					if item["judgment_id"] != expectedID {
						t.Fatal(item)
					}
					if i == 0 || i == 6 {
						if item["status"] != "success" || item["judgment_status"] != "judged" {
							t.Fatal(item)
						}
						rel := item["relation"].(map[string]any)
						if rel["judgment_status"] != "judged" {
							t.Fatal(item)
						}
					} else {
						if item["status"] != "error" || item["error"] == nil || item["error"] == "" {
							t.Fatal(item)
						}
					}
				}
				if !strings.Contains(outcomes[1].(map[string]any)["error"].(string), "project") {
					t.Fatal(outcomes[1])
				}
				persisted := batchRelation(t, s, id)
				if persisted.Relation != "compatible" || persisted.Reason == nil || *persisted.Reason != "revised" || persisted.Evidence == nil || *persisted.Evidence != "user approved" || persisted.Confidence == nil || *persisted.Confidence != 0.9 || persisted.SessionID == nil || *persisted.SessionID != "batch-project" || persisted.MarkedByActor == nil || *persisted.MarkedByActor != "agent" || persisted.MarkedByKind == nil || *persisted.MarkedByKind != "agent" {
					t.Fatalf("persisted %#v", persisted)
				}
				if !reflect.DeepEqual(beforeCross, batchRelation(t, s, cross)) {
					t.Fatal("cross-project row mutated")
				}
				mutations := batchMutations(t, s)
				if !enrolled {
					if !reflect.DeepEqual(initial, mutations) {
						t.Fatal("local judgment enqueued mutation")
					}
				} else {
					count := 0
					for _, m := range mutations {
						if m.Entity == store.SyncEntityRelation && m.EntityKey == id {
							count++
							var payload map[string]any
							if err := json.Unmarshal([]byte(m.Payload), &payload); err != nil {
								t.Fatal(err)
							}
							if payload["judgment_status"] != "judged" || payload["session_id"] != "batch-project" {
								t.Fatal(payload)
							}
							if count%2 == 0 {
								if payload["relation"] != "compatible" || payload["reason"] != "revised" || payload["evidence"] != "user approved" || payload["confidence"] != 0.9 {
									t.Fatal(payload)
								}
							} else if payload["relation"] != "related" {
								t.Fatal(payload)
							}
						}
					}
					if count != 2*(attempt+1) {
						t.Fatalf("pending relation mutations %d; expected two writes per attempt", count)
					}
				}
			}
		})
	}
}

func TestJudgeManyInvalidInputNoMutation(t *testing.T) {
	s, srv, id, cross := batchFixture(t)
	if err := s.EnrollProject("batch-project"); err != nil {
		t.Fatal(err)
	}
	before, beforeCross, mutations := batchRelation(t, s, id), batchRelation(t, s, cross), batchMutations(t, s)
	oversized := make([]any, 33)
	for i := range oversized {
		oversized[i] = batchItem(id, "supersedes")
	}
	for _, args := range []map[string]any{{}, {"judgments": nil}, {"judgments": "bad"}, {"judgments": []any{}}, {"judgments": oversized}, {"judgments": []any{batchItem(id, "related")}, "session_id": 42}} {
		if out, failed := batchCall(t, srv, "mem_judge_many", args); !failed || out["error"] == "" {
			t.Fatal(out)
		}
		if !reflect.DeepEqual(before, batchRelation(t, s, id)) || !reflect.DeepEqual(beforeCross, batchRelation(t, s, cross)) || !reflect.DeepEqual(mutations, batchMutations(t, s)) {
			t.Fatal("invalid batch mutated persistent state")
		}
	}
	entries := []any{false, 42, "bad", []any{}, map[string]any{}, map[string]any{"judgment_id": 42, "relation": "related"}, map[string]any{"judgment_id": id, "relation": 42}, map[string]any{"judgment_id": id, "relation": "related", "reason": 42}, map[string]any{"judgment_id": id, "relation": "related", "evidence": 42}, map[string]any{"judgment_id": id, "relation": "related", "confidence": -1}}
	out, failed := batchCall(t, srv, "mem_judge_many", map[string]any{"judgments": entries})
	if failed {
		t.Fatal(out)
	}
	for _, raw := range batchOutcomes(t, out) {
		if raw.(map[string]any)["status"] != "error" {
			t.Fatal(raw)
		}
	}
	if !reflect.DeepEqual(before, batchRelation(t, s, id)) || !reflect.DeepEqual(mutations, batchMutations(t, s)) {
		t.Fatal("malformed entries mutated state")
	}
	for _, count := range []int{1, 32} {
		entries := make([]any, count)
		for i := range entries {
			entries[i] = batchItem(id, "scoped")
		}
		out, failed := batchCall(t, srv, "mem_judge_many", map[string]any{"judgments": entries})
		if failed || len(batchOutcomes(t, out)) != count {
			t.Fatal(out)
		}
	}
}

func TestJudgeManyQueuedSerialization(t *testing.T) {
	s, srv, id, _ := batchFixture(t)
	if err := s.EnrollProject("batch-project"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan bool, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, reason := range []string{"batch-a", "batch-b"} {
		go func(reason string) {
			items := make([]any, 16)
			for i := range items {
				item := batchItem(id, "related")
				item["reason"] = reason
				items[i] = item
			}
			ready.Done()
			<-start
			out, failed := batchCall(t, srv, "mem_judge_many", map[string]any{"judgments": items})
			results <- (!failed && len(batchOutcomes(t, out)) == 16)
		}(reason)
	}
	ready.Wait()
	close(start)
	for range 2 {
		if !<-results {
			t.Fatal("concurrent batch failed")
		}
	}
	reasons := []string{}
	for _, m := range batchMutations(t, s) {
		if m.Entity == store.SyncEntityRelation && m.EntityKey == id {
			var p map[string]any
			if err := json.Unmarshal([]byte(m.Payload), &p); err != nil {
				t.Fatal(err)
			}
			reasons = append(reasons, p["reason"].(string))
		}
	}
	if len(reasons) != 32 {
		t.Fatal(reasons)
	}
	for i, reason := range reasons {
		if i < 16 && reason != reasons[0] || i >= 16 && reason != reasons[16] {
			t.Fatalf("batch writes interleaved: %v", reasons)
		}
	}
	if reasons[0] == reasons[16] {
		t.Fatal(reasons)
	}
}

func TestJudgeManyRegistrationGuidanceAndLegacy(t *testing.T) {
	s, srv, id, _ := batchFixture(t)
	for _, profile := range []map[string]bool{nil, ProfileAgent, ProfileAdmin, {"mem_judge_many": true}} {
		tool, ok := NewServerWithTools(s, profile).ListTools()["mem_judge_many"]
		if !ok || !tool.Tool.DeferLoading {
			t.Fatal("batch must be registered deferred")
		}
	}
	if _, ok := NewServerWithTools(s, map[string]bool{"mem_judge": true}).ListTools()["mem_judge_many"]; ok {
		t.Fatal("batch leaked into single allowlist")
	}
	for _, tc := range []struct {
		allow        map[string]bool
		want, absent string
	}{{map[string]bool{"mem_save": true, "mem_judge_many": true}, "prefer mem_judge_many", "call mem_judge once"}, {map[string]bool{"mem_save": true, "mem_judge": true}, "call mem_judge once", "mem_judge_many"}, {map[string]bool{"mem_judge_many": true}, "DELIVERY GUARANTEE", "CONFLICT SURFACING"}} {
		text := buildServerInstructions(tc.allow)
		if !strings.Contains(text, tc.want) || strings.Contains(text, tc.absent) {
			t.Fatal(text)
		}
	}
	out, failed := batchCall(t, srv, "mem_judge", batchItem(id, "not_conflict"))
	if failed || out["relation"].(map[string]any)["judgment_status"] != "judged" {
		t.Fatal(out)
	}
	before := batchRelation(t, s, id)
	out, failed = batchCall(t, srv, "mem_judge", batchItem(id, "invalid"))
	if !failed || !reflect.DeepEqual(before, batchRelation(t, s, id)) {
		t.Fatal(out)
	}
	TestMCPToolContractV1(t)
	TestResolveToolsAgentProfile(t)
	TestResolveToolsAdminProfile(t)
	TestHandleJudge_HappyPath(t)
	TestHandleJudge_OptionalFieldsStayNull(t)
	TestHandleJudge_UnknownID_IsError(t)
	TestHandleJudge_InvalidVerb_IsError(t)
	TestHandleJudge_Idempotent_Overwrite(t)
	TestHandleJudge_RequiresJudgmentID(t)
	TestHandleJudge_RequiresRelation(t)
}
