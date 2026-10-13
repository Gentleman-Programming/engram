package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"github.com/mark3labs/mcp-go/server"
)

// Exercise public JSON-RPC dispatch, not the consolidation handler directly.
func callConsolidateRPC(t *testing.T, srv *server.MCPServer, args map[string]any) (bool, map[string]any) {
	t.Helper()
	wire, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_consolidate", "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(srv.HandleMessage(context.Background(), wire))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Error  any `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("RPC error: %s", raw)
	}
	if len(response.Result.Content) != 1 {
		t.Fatalf("content: %s", raw)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(response.Result.Content[0].Text), &body); err != nil {
		t.Fatalf("invalid JSON text %q: %v", response.Result.Content[0].Text, err)
	}
	return response.Result.IsError, body
}

func seedConsolidateMCP(t *testing.T) (*store.Store, *server.MCPServer, map[string]any) {
	t.Helper()
	s := newMCPTestStore(t)
	if err := s.CreateSession("cons-old", "engram", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("cons-new", "engram", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("engram"); err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for i, session := range []string{"cons-old", "cons-new"} {
		id, err := s.AddObservation(store.AddObservationParams{SessionID: session, Project: "engram", Scope: "project", Type: "decision", Title: []string{"Old", "New"}[i], Content: []string{"Old content", "New content"}[i]})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return s, NewServerWithTools(s, ResolveTools("agent")), map[string]any{"ids_to_delete": ids, "new_title": "Consolidated decision", "new_content": "Approved summary", "expected_project": "engram"}
}

func consolidateState(t *testing.T, s *store.Store) string {
	t.Helper()
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	data.ExportedAt = ""
	mutations, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal([]any{data, mutations})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestConsolidateMCPDiscovery(t *testing.T) {
	s := newMCPTestStore(t)
	srv := NewServerWithTools(s, ResolveTools("agent"))
	tool := srv.ListTools()["mem_consolidate"]
	if tool == nil {
		t.Fatal("agent profile missing mem_consolidate")
	}
	ann := tool.Tool.Annotations
	if ann.ReadOnlyHint == nil || *ann.ReadOnlyHint || ann.DestructiveHint == nil || !*ann.DestructiveHint || ann.IdempotentHint == nil || *ann.IdempotentHint {
		t.Fatalf("unsafe annotations: %+v", ann)
	}
	if !strings.Contains(buildServerInstructions(ResolveTools("mem_consolidate")), "mem_consolidate") {
		t.Fatal("discovery instructions missing tool")
	}
	if NewServerWithTools(s, ResolveTools("admin")).ListTools()["mem_consolidate"] != nil {
		t.Fatal("unexpected admin profile tool")
	}
	for _, name := range []string{"mem_update", "mem_delete"} {
		if NewServer(s).ListTools()[name] == nil {
			t.Fatalf("previous writer %s disappeared", name)
		}
	}
}

func TestConsolidateMCPPreviewAndConfirm(t *testing.T) {
	s, srv, args := seedConsolidateMCP(t)
	before := consolidateState(t, s)
	failed, body := callConsolidateRPC(t, srv, args)
	if failed || body["status"] != "preview" {
		t.Fatalf("preview: %+v", body)
	}
	plan := body["plan"].(map[string]any)
	if plan["project"] != "engram" || len(plan["sources"].([]any)) != 2 || plan["fingerprint"] == "" {
		t.Fatalf("plan: %+v", plan)
	}
	survivor := plan["survivor"].(map[string]any)
	if survivor["session_id"] != "cons-new" || survivor["scope"] != "project" || survivor["title"] != "Consolidated decision" || survivor["topic_key"] != "" || !strings.Contains(survivor["content"].(string), "Approved summary") {
		t.Fatalf("survivor: %+v", survivor)
	}
	if after := consolidateState(t, s); after != before {
		t.Fatal("preview mutated data/counters/journal")
	}
	args["confirm"] = true
	args["fingerprint"] = plan["fingerprint"]
	failed, body = callConsolidateRPC(t, srv, args)
	if failed || body["status"] != "consolidated" || len(body["soft_deleted_ids"].([]any)) != 2 {
		t.Fatalf("confirm: %+v", body)
	}
	created := body["survivor"].(map[string]any)
	id := int64(created["id"].(float64))
	obs, err := s.GetObservation(id)
	if err != nil || obs.SessionID != "cons-new" || obs.Type != "decision" || obs.Scope != "project" || obs.Title != "Consolidated decision" || !strings.Contains(obs.Content, "## consolidated_from") {
		t.Fatalf("survivor=%+v err=%v", obs, err)
	}
	data, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	deleted := 0
	for _, o := range data.Observations {
		if o.ID != id {
			if o.DeletedAt == nil || o.SupersededBySyncID == nil || *o.SupersededBySyncID != obs.SyncID {
				t.Fatalf("source: %+v", o)
			}
			deleted++
		}
	}
	if deleted != 2 {
		t.Fatalf("deleted=%d", deleted)
	}
	queue, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1000)
	if err != nil {
		t.Fatal(err)
	}
	upsertAt := -1
	deletes := 0
	for i, m := range queue {
		if m.Entity != store.SyncEntityObservation {
			continue
		}
		if m.EntityKey == obs.SyncID && m.Op == store.SyncOpUpsert {
			upsertAt = i
		}
		if m.Op == store.SyncOpDelete {
			if upsertAt < 0 || i < upsertAt {
				t.Fatal("source delete preceded survivor journal")
			}
			deletes++
		}
	}
	if upsertAt < 0 || deletes != 2 {
		t.Fatalf("journal upsert=%d deletes=%d", upsertAt, deletes)
	}
}

func TestConsolidateMCPRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     any
		remove    bool
	}{
		{"missing IDs", "ids_to_delete", nil, true},
		{"not array", "ids_to_delete", "12,18", false},
		{"one source", "ids_to_delete", []any{1}, false},
		{"duplicate source", "ids_to_delete", []any{1, 1}, false},
		{"fractional ID", "ids_to_delete", []any{1.5, 2}, false},
		{"nonpositive ID", "ids_to_delete", []any{0, 2}, false},
		{"unsafe integer", "ids_to_delete", []any{9007199254740992., 2}, false},
		{"missing sources", "ids_to_delete", []any{999999, 999998}, false},
		{"missing title", "new_title", nil, true},
		{"empty title", "new_title", " ", false},
		{"nonstring content", "new_content", 7, false},
		{"missing content", "new_content", nil, true},
		{"wrong owner", "expected_project", "elsewhere", false},
		{"missing owner", "expected_project", nil, true},
		{"invalid confirm", "confirm", "true", false},
		{"missing fingerprint", "confirm", true, false},
		{"invalid optional type", "type", 3, false},
		{"invalid optional topic", "topic_key", 3, false},
		{"invalid fingerprint", "fingerprint", 3, false},
		{"unsupported scope", "scope", "personal", false},
		{"unsupported project", "project", "elsewhere", false},
		{"unsupported session", "session_id", "cons-old", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, srv, args := seedConsolidateMCP(t)
			if tc.remove {
				delete(args, tc.key)
			} else {
				args[tc.key] = tc.value
			}
			before := consolidateState(t, s)
			failed, body := callConsolidateRPC(t, srv, args)
			errorBody, ok := body["error"].(map[string]any)
			wantCode := "invalid_arguments"
			switch tc.name {
			case "missing sources", "duplicate source":
				wantCode = "consolidation_refused"
			case "wrong owner":
				wantCode = "project_mismatch"
			}
			message, hasMessage := errorBody["message"].(string)
			if !failed || !ok || !hasMessage || message == "" || errorBody["code"] != wantCode || body["plan"] != nil {
				t.Fatalf("expected %s error without disclosed plan: %+v", wantCode, body)
			}
			if consolidateState(t, s) != before {
				t.Fatal("rejected input mutated public state")
			}
		})
	}
}

func TestConsolidateMCPChangedPreview(t *testing.T) {
	s, srv, args := seedConsolidateMCP(t)
	failed, preview := callConsolidateRPC(t, srv, args)
	if failed {
		t.Fatal(preview)
	}
	args["confirm"] = true
	args["fingerprint"] = preview["plan"].(map[string]any)["fingerprint"]
	args["new_content"] = "Changed summary"
	before := consolidateState(t, s)
	failed, body := callConsolidateRPC(t, srv, args)
	if !failed || body["error"].(map[string]any)["code"] != "fingerprint_mismatch" {
		t.Fatalf("mismatch: %+v", body)
	}
	fresh := body["plan"].(map[string]any)
	if reflect.DeepEqual(fresh["fingerprint"], args["fingerprint"]) || !strings.Contains(fresh["survivor"].(map[string]any)["content"].(string), "Changed summary") {
		t.Fatalf("fresh: %+v", fresh)
	}
	if consolidateState(t, s) != before {
		t.Fatal("mismatch mutated data")
	}
	args["fingerprint"] = fresh["fingerprint"]
	args["type"] = "architecture"
	args["topic_key"] = "consolidated/decision"
	delete(args, "confirm")
	delete(args, "fingerprint")
	failed, body = callConsolidateRPC(t, srv, args)
	if failed {
		t.Fatal(body)
	}
	plan := body["plan"].(map[string]any)
	if plan["survivor"].(map[string]any)["type"] != "architecture" || plan["survivor"].(map[string]any)["topic_key"] != "consolidated/decision" {
		t.Fatal(plan)
	}
	args["confirm"] = true
	args["fingerprint"] = plan["fingerprint"]
	failed, body = callConsolidateRPC(t, srv, args)
	if failed {
		t.Fatal(body)
	}
	if body["survivor"].(map[string]any)["type"] != "architecture" || body["survivor"].(map[string]any)["topic_key"] != "consolidated/decision" {
		t.Fatal(body)
	}
}
