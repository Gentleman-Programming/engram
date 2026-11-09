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
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Exercise JSON-RPC dispatch rather than the private tool handler.
func historyRPC(t *testing.T, srv *mcpserver.MCPServer, args map[string]any) (map[string]any, string) {
	t.Helper()
	wire, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_get_observation", "arguments": args}})
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
	if rpc.Error != nil || len(rpc.Result.Content) != 1 {
		t.Fatalf("RPC envelope: %s", raw)
	}
	text := rpc.Result.Content[0].Text
	if rpc.Result.IsError {
		return nil, text
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatal(err)
	}
	return body, ""
}

func TestObservationHistoryTransportParity(t *testing.T) {
	for _, count := range []int{0, 1, 50, 51, 123} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			st := newMCPTestStore(t)
			id := seedIncludeHistoryObservation(t, st, count)
			if err := st.CreateSession("other-session", "unrelated-owner", ""); err != nil {
				t.Fatal(err)
			}
			srv := NewServer(st)
			h := httpserver.New(st, 0).Handler()
			legacy := httptest.NewRecorder()
			h.ServeHTTP(legacy, httptest.NewRequest("GET", fmt.Sprintf("/observations/%d", id), nil))
			var wantCurrent map[string]any
			if legacy.Code != 200 || json.Unmarshal(legacy.Body.Bytes(), &wantCurrent) != nil {
				t.Fatalf("legacy observation: %d %s", legacy.Code, legacy.Body.String())
			}
			seen := map[int]bool{}
			cursor, upper := 0, count+1
			for page := 0; page < 5; page++ {
				args := map[string]any{"id": id, "include_history": true, "project": "unrelated-owner"}
				query := url.Values{"include_history": {"true"}, "project": {"unrelated-owner"}}
				if cursor != 0 {
					args["history_cursor"] = cursor
					query.Set("history_cursor", fmt.Sprint(cursor))
				}
				mcpBody, message := historyRPC(t, srv, args)
				if message != "" {
					t.Fatal(message)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest("GET", fmt.Sprintf("/observations/%d?%s", id, query.Encode()), nil))
				var body map[string]any
				if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
					t.Fatalf("HTTP: %d %s", rec.Code, rec.Body.String())
				}
				for key, value := range wantCurrent {
					if !reflect.DeepEqual(body[key], value) {
						t.Fatalf("current %s: got %#v want %#v", key, body[key], value)
					}
				}
				if text, _ := mcpBody["result"].(string); !strings.Contains(text, wantCurrent["content"].(string)) || !strings.Contains(text, wantCurrent["title"].(string)) || !strings.Contains(text, fmt.Sprintf("#%d", id)) {
					t.Fatalf("MCP current record: %#v", mcpBody)
				}
				for _, key := range []string{"history", "version_count", "history_truncated", "history_from_version", "history_cursor"} {
					got, present := body[key]
					want, expected := mcpBody[key]
					if present != expected || !reflect.DeepEqual(got, want) {
						t.Fatalf("%s parity HTTP=%#v MCP=%#v", key, body, mcpBody)
					}
				}
				if body["version_count"] != float64(count) {
					t.Fatalf("total: %#v", body)
				}
				entries, ok := body["history"].([]any)
				wantLen := upper - 1
				if wantLen > 50 {
					wantLen = 50
				}
				if !ok || len(entries) != wantLen {
					t.Fatalf("page size: %#v", body)
				}
				oldest := upper - wantLen
				for i, raw := range entries {
					entry, ok := raw.(map[string]any)
					v := oldest + i
					if !ok || entry["version"] != float64(v) || entry["title"] != "Auth architecture" || entry["content"] != fmt.Sprintf("state-%02d", v-1) || seen[v] {
						t.Fatalf("snapshot %d: %#v", v, raw)
					}
					if stamp, ok := entry["created_at"].(string); !ok || stamp == "" {
						t.Fatalf("timestamp: %#v", entry)
					}
					seen[v] = true
				}
				if oldest <= 1 {
					for _, key := range []string{"history_truncated", "history_from_version", "history_cursor"} {
						if _, ok := body[key]; ok {
							t.Fatalf("terminal %s present", key)
						}
					}
					break
				}
				if body["history_truncated"] != true || body["history_from_version"] != float64(oldest) || body["history_cursor"] != float64(oldest) {
					t.Fatalf("continuation: %#v", body)
				}
				cursor = int(body["history_cursor"].(float64))
				upper = cursor
			}
			if len(seen) != count {
				t.Fatalf("traversed %d of %d snapshots", len(seen), count)
			}
		})
	}
}

func TestObservationHistoryPublicMCPValidation(t *testing.T) {
	st := newMCPTestStore(t)
	id := seedIncludeHistoryObservation(t, st, 3)
	srv := NewServer(st)
	h := httpserver.New(st, 0).Handler()
	get := func(target string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return rec.Code, body
	}
	path := fmt.Sprintf("/observations/%d", id)
	snapshot := func() []map[string]any {
		t.Helper()
		status, current := get(path)
		if status != 200 {
			t.Fatalf("current: %d %#v", status, current)
		}
		status, stats := get("/stats")
		if status != 200 {
			t.Fatalf("stats: %d %#v", status, stats)
		}
		history, message := historyRPC(t, srv, map[string]any{"id": id, "project": "engram", "include_history": true})
		if message != "" {
			t.Fatal(message)
		}
		entries, ok := history["history"].([]any)
		if !ok || len(entries) != 3 || history["version_count"] != float64(3) {
			t.Fatalf("full history: %#v", history)
		}
		return []map[string]any{current, stats, history}
	}
	assertUnchanged := func(before []map[string]any) {
		t.Helper()
		if after := snapshot(); !reflect.DeepEqual(before, after) {
			t.Fatalf("rejected read mutated public current/counters/history: before=%#v after=%#v", before, after)
		}
	}
	for _, tc := range []struct {
		name    string
		id      any
		cursor  any
		message string
	}{
		{"empty", id, "", "history_cursor must be a non-negative integer"},
		{"fraction", id, 1.5, "history_cursor must be a non-negative integer"},
		{"negative", id, -1, "history_cursor must be a non-negative integer"},
		{"boundary", id, 0x1p63, "history_cursor must be a non-negative integer"},
		{"missing ID first", nil, -1, "id is required"},
		{"not found first", 999999, -1, "Observation #999999 not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := snapshot()
			_, message := historyRPC(t, srv, map[string]any{"id": tc.id, "project": "engram", "include_history": true, "history_cursor": tc.cursor})
			assertUnchanged(before)
			if message != tc.message {
				t.Fatalf("error = %q want %q", message, tc.message)
			}
		})
	}
	for _, tc := range []struct {
		cursor  any
		wantLen int
	}{
		{json.Number("0e0"), 3}, {json.Number("3.0"), 2}, {json.Number("3e0"), 2}, {1, 0}, {json.Number("9223372036854774784"), 3},
	} {
		body, message := historyRPC(t, srv, map[string]any{"id": id, "project": "engram", "include_history": true, "history_cursor": tc.cursor})
		entries, ok := body["history"].([]any)
		if message != "" || !ok || len(entries) != tc.wantLen || body["version_count"] != float64(3) {
			t.Fatalf("numeric cursor %v: %q %#v", tc.cursor, message, body)
		}
		for _, key := range []string{"history_truncated", "history_from_version", "history_cursor"} {
			if _, ok := body[key]; ok {
				t.Fatalf("exhausted cursor leaked %s", key)
			}
		}
	}
	for _, include := range []any{nil, false} {
		args := map[string]any{"id": id, "project": "engram", "history_cursor": "invalid"}
		if include != nil {
			args["include_history"] = include
		}
		body, message := historyRPC(t, srv, args)
		if message != "" {
			t.Fatal(message)
		}
		for _, key := range []string{"history", "version_count", "history_truncated", "history_from_version", "history_cursor"} {
			if _, ok := body[key]; ok {
				t.Fatalf("default leaked %s", key)
			}
		}
	}
	for _, tc := range []struct {
		name, target, message string
		status                int
	}{
		{"empty", path + "?include_history=true&history_cursor=", "history_cursor must be a non-negative integer", 400},
		{"fraction", path + "?include_history=true&history_cursor=1.5", "history_cursor must be a non-negative integer", 400},
		{"negative", path + "?include_history=true&history_cursor=-1", "history_cursor must be a non-negative integer", 400},
		{"text", path + "?include_history=true&history_cursor=no", "history_cursor must be a non-negative integer", 400},
		{"boundary", path + "?include_history=true&history_cursor=9223372036854775808", "history_cursor must be a non-negative integer", 400},
		{"overflow", path + "?include_history=true&history_cursor=1e309", "history_cursor must be a non-negative integer", 400},
		{"invalid ID first", "/observations/no?include_history=true&history_cursor=-1", "invalid observation id", 400},
		{"not found first", "/observations/999999?include_history=true&history_cursor=-1", "observation not found", 404},
	} {
		t.Run("HTTP "+tc.name, func(t *testing.T) {
			before := snapshot()
			status, body := get(tc.target)
			assertUnchanged(before)
			if status != tc.status || body["error"] != tc.message {
				t.Fatalf("HTTP error: %d %#v want %d %q", status, body, tc.status, tc.message)
			}
		})
	}
}
