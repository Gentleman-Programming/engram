package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	httpserver "github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Exercise registered MCP dispatch and the existing HTTP read/write interface.
func TestPromptSearchTransportParity(t *testing.T) {
	t.Setenv("ENGRAM_PROJECT", "prompt-a")
	st := newMCPTestStore(t)
	h := httpserver.New(st, 0).Handler()
	contents := make(map[int64]string)
	for i := 0; i < 12; i++ {
		project := "prompt-a"
		if i == 11 {
			project = "prompt-b"
		}
		session := fmt.Sprintf("prompt-session-%d", i)
		if err := st.CreateSession(session, project, ""); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("orbit ux exact turn %d\n%s", i, strings.TrimSpace(strings.Repeat("verbatim 世界 ", 150)))
		body, _ := json.Marshal(map[string]any{"session_id": session, "project": project, "content": content})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/prompts", strings.NewReader(string(body))))
		if rec.Code != 201 {
			t.Fatalf("save prompt: %d %s", rec.Code, rec.Body.String())
		}
		var saved struct {
			ID       int64 `json:"id"`
			PromptID int64 `json:"prompt_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		id := saved.ID
		if id == 0 {
			id = saved.PromptID
		}
		if id == 0 {
			t.Fatalf("missing saved id: %s", rec.Body.String())
		}
		contents[id] = content
	}
	srv := NewServerWithConfig(st, MCPConfig{DefaultProject: "prompt-a"}, ProfileAgent)
	publicSnapshot := func() string {
		t.Helper()
		var snapshot strings.Builder
		for _, path := range []string{"/prompts/recent?all_projects=true&limit=100", "/stats?all_projects=true"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			if rec.Code != 200 {
				t.Fatalf("snapshot: %d %s", rec.Code, rec.Body.String())
			}
			snapshot.WriteString(rec.Body.String())
		}
		return snapshot.String()
	}
	for _, tc := range []struct {
		name, query, project string
		all                  bool
		limit, want          int
		invalid              bool
	}{
		{name: "current default", query: "orbit", want: 10},
		{name: "explicit isolation", query: "orbit", project: "prompt-b", want: 1},
		{name: "all projects", query: "orbit", all: true, want: 10},
		{name: "explicit limit", query: "orbit", all: true, limit: 12, want: 12},
		{name: "nonpositive default", query: "orbit", limit: -1, want: 10},
		{name: "short term ordering", query: "ux", all: true, limit: 12, want: 12},
		{name: "no hits", query: "unmatchedtoken", want: 0},
		{name: "missing query", invalid: true},
		{name: "conflicting scope", query: "orbit", project: "prompt-a", all: true, invalid: true},
		{name: "unknown project", query: "orbit", project: "not-stored", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := publicSnapshot()
			t.Cleanup(func() {
				if after := publicSnapshot(); after != before {
					t.Error("search changed stored prompts or counters")
				}
			})
			args := map[string]any{"query": tc.query}
			params := url.Values{"q": {tc.query}}
			if tc.project != "" {
				args["project"] = tc.project
				params.Set("project", tc.project)
			}
			if tc.all {
				args["all_projects"] = true
				params.Set("all_projects", "true")
			}
			if tc.limit != 0 {
				args["limit"] = tc.limit
				params.Set("limit", fmt.Sprint(tc.limit))
			}
			wire, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_search_prompts", "arguments": args}})
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
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("GET", "/prompts/search?"+params.Encode(), nil))
			if tc.invalid {
				if rpc.Error == nil && !rpc.Result.IsError {
					t.Fatalf("MCP accepted invalid query: %s", raw)
				}
				if rec.Code < 400 {
					t.Fatalf("HTTP accepted invalid query: %d %s", rec.Code, rec.Body.String())
				}
				return
			}
			if rpc.Error != nil || rpc.Result.IsError || len(rpc.Result.Content) != 1 {
				t.Fatalf("MCP search failed: %s", raw)
			}
			var got struct {
				Results []store.Prompt `json:"results"`
			}
			if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &got); err != nil {
				t.Fatal(err)
			}
			var want []store.Prompt
			if rec.Code != 200 {
				t.Fatalf("HTTP: %d %s", rec.Code, rec.Body.String())
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &want); err != nil {
				t.Fatal(err)
			}
			if got.Results == nil || len(got.Results) != tc.want || !reflect.DeepEqual(got.Results, want) {
				t.Fatalf("result parity/count: MCP=%+v HTTP=%+v want=%d", got.Results, want, tc.want)
			}
			for i, p := range got.Results {
				if p.Content != contents[p.ID] || p.SessionID == "" || p.CreatedAt == "" {
					t.Fatalf("lost original content/context for prompt %d", p.ID)
				}
				if !tc.all && p.Project != map[bool]string{true: tc.project, false: "prompt-a"}[tc.project != ""] {
					t.Fatalf("project leak: %+v", p)
				}
				if tc.query == "ux" && i > 0 && p.ID >= got.Results[i-1].ID {
					t.Fatal("short query not newest first")
				}
			}
		})
	}
	// A dedicated prompt tool must not make observation search return prompt rows.
	wire := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mem_search","arguments":{"query":"orbit","project":"prompt-a"}}}`)
	raw, _ := json.Marshal(srv.HandleMessage(context.Background(), wire))
	if !strings.Contains(string(raw), "No memories found") || strings.Contains(string(raw), "exact turn") {
		t.Fatalf("observation search changed: %s", raw)
	}
}

func TestPromptSearchStoreFailure(t *testing.T) {
	st := newMCPTestStore(t)
	srv := NewServerWithConfig(st, MCPConfig{}, ProfileAgent)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(srv.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mem_search_prompts","arguments":{"query":"orbit","all_projects":true}}}`)))
	if !strings.Contains(string(raw), `"isError":true`) || !strings.Contains(string(raw), "search prompts") {
		t.Fatalf("store error hidden: %s", raw)
	}
}
