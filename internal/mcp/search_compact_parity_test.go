package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	httpserver "github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Compare equivalent explicit selectors through public HTTP and JSON-RPC dispatch.
// Implicit personal-scope project resolution intentionally remains out of scope.
func TestSearchCompactTransportParity(t *testing.T) {
	st := newMCPTestStore(t)
	ids := make([]int64, 0, 4)
	for i, seed := range []struct{ project, scope, typ, title, content string }{
		{"parity-a", "project", "decision", "Aurora decision", "aurora " + strings.Repeat("世界", 160)},
		{"parity-b", "project", "note", "Nebula note", "nebula alternate"},
		{"parity-a", "personal", "note", "Aurora personal", "aurora personal"},
		// A same-project, same-scope relation endpoint that does not match search terms.
		{"parity-a", "project", "note", "Prior decision", "previous rationale"},
	} {
		session := []string{"parity-one", "parity-two", "parity-three", "parity-four"}[i]
		if err := st.CreateSession(session, seed.project, ""); err != nil {
			t.Fatal(err)
		}
		id, err := st.AddObservation(store.AddObservationParams{SessionID: session, Project: seed.project, Scope: seed.scope, Type: seed.typ, Title: seed.title, Content: seed.content})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	source, err := st.GetObservation(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.GetObservation(ids[3])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveRelation(store.SaveRelationParams{SyncID: "parity-relation", SourceID: source.SyncID, TargetID: target.SyncID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.JudgeRelation(store.JudgeRelationParams{JudgmentID: "parity-relation", Relation: store.RelationSupersedes, MarkedByActor: "agent:test", MarkedByKind: "agent"}); err != nil {
		t.Fatal(err)
	}
	h := httpserver.New(st, 0).Handler()
	srv := NewServer(st)
	for _, tc := range []struct {
		name, query, project, scope, typ, mode string
		all                                    bool
		limit, want                            int
	}{
		{name: "project bounded relations", query: "aurora", project: "parity-a", scope: "project", limit: 10, want: 1},
		{name: "all projects any", query: "aurora nebula", scope: "project", mode: "any", all: true, limit: 10, want: 2},
		{name: "all terms", query: "aurora nebula", scope: "project", mode: "all", all: true, limit: 10, want: 0},
		{name: "limit", query: "aurora nebula", scope: "project", mode: "any", all: true, limit: 1, want: 1},
		{name: "type filter", query: "aurora nebula", scope: "project", typ: "note", mode: "any", all: true, limit: 10, want: 1},
		{name: "explicit personal", query: "aurora", scope: "personal", all: true, limit: 10, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"query": tc.query, "response_format": "compact", "limit": tc.limit, "scope": tc.scope}
			params := url.Values{"q": {tc.query}, "response_format": {"compact"}, "scope": {tc.scope}}
			limitJSON, _ := json.Marshal(tc.limit)
			params.Set("limit", string(limitJSON))
			for key, value := range map[string]string{"project": tc.project, "type": tc.typ, "match_mode": tc.mode} {
				if value != "" {
					args[key] = value
					params.Set(key, value)
				}
			}
			if tc.all {
				args["all_projects"] = true
				params.Set("all_projects", "true")
			}
			wire, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_search", "arguments": args}})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(srv.HandleMessage(context.Background(), wire))
			if err != nil {
				t.Fatal(err)
			}
			var rpc struct {
				Error  any `json:"error"`
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal(raw, &rpc); err != nil {
				t.Fatal(err)
			}
			if rpc.Error != nil || rpc.Result.IsError || len(rpc.Result.Content) != 1 {
				t.Fatalf("MCP response: %s", raw)
			}
			var mcpBody map[string]any
			if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &mcpBody); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/search?"+params.Encode(), nil))
			var httpBody map[string]any
			if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &httpBody) != nil {
				t.Fatalf("HTTP compact envelope: %d %s", rec.Code, rec.Body.String())
			}
			entries, ok := httpBody["results"].([]any)
			if !ok || len(entries) != tc.want {
				t.Fatalf("HTTP results: %#v", httpBody)
			}
			if httpBody["result"] != mcpBody["result"] {
				t.Fatalf("summary mismatch HTTP=%#v MCP=%#v", httpBody, mcpBody)
			}
			if tc.want == 0 {
				return
			} // MCP keeps its legacy empty envelope without results.
			if !reflect.DeepEqual(entries, mcpBody["results"]) {
				t.Fatalf("projection mismatch HTTP=%#v MCP=%#v", entries, mcpBody["results"])
			}
			if tc.name == "project bounded relations" {
				entry := entries[0].(map[string]any)
				rels, ok := entry["relations"].(map[string]any)
				if !ok {
					t.Fatalf("missing relations: %#v", entry)
				}
				asSource, ok := rels["as_source"].([]any)
				if !ok || len(asSource) != 1 || asSource[0].(map[string]any)["relation"] != store.RelationSupersedes {
					t.Fatalf("source relations: %#v", rels)
				}
			}
		})
	}
}
