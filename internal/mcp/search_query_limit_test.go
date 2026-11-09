package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	httpserver "github.com/Gentleman-Programming/engram/v3/internal/server"
)

// Exercise current JSON-RPC mem_search dispatch and compact HTTP, both of which
// use SearchPreviewsContext rather than the older full-result search path.
func TestSearchQueryLimitTransportParity(t *testing.T) {
	st := newMCPTestStore(t)
	srv, http := NewServer(st), httpserver.New(st, 0).Handler()
	for _, tc := range []struct {
		name, query, message string
		status               int
	}{
		{"byte-boundary", strings.Repeat("a", 65536), "", 200},
		{"too-many-bytes", strings.Repeat("a", 65537), "got 65537 bytes", 400},
		{"utf8-bytes", strings.Repeat("é", 32769), "got 65538 bytes", 400},
		{"term-boundary", strings.Repeat("x ", 768), "", 200},
		{"too-many-short-terms", strings.Repeat("x ", 769), "got 769 terms", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_search", "arguments": map[string]any{"query": tc.query, "all_projects": true, "response_format": "compact"}}})
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
				} `json:"result"`
			}
			if err := json.Unmarshal(raw, &rpc); err != nil {
				t.Fatal(err)
			}
			if rpc.Error != nil || rpc.Result.IsError != (tc.status == 400) {
				t.Fatalf("unexpected MCP error classification: %.300s", raw)
			}
			if tc.message != "" && (!strings.Contains(string(raw), tc.message) || !strings.Contains(string(raw), "shorten the query and retry")) {
				t.Fatalf("missing actionable MCP limit: %s", raw)
			}
			rec := httptest.NewRecorder()
			http.ServeHTTP(rec, httptest.NewRequest("GET", "/search?all_projects=true&response_format=compact&q="+url.QueryEscape(tc.query), nil))
			if rec.Code != tc.status {
				t.Fatalf("HTTP status=%d, want%d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.message != "" && !strings.Contains(rec.Body.String(), tc.message) {
				t.Fatalf("missing HTTP limit: %s", rec.Body.String())
			}
		})
	}
}
