package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	mcppkg "github.com/mark3labs/mcp-go/mcp"
)

func TestPromptCaptureCrossTransportDedupe(t *testing.T) {
	for _, httpFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("http_first=%t", httpFirst), func(t *testing.T) {
			st := newMCPTestStore(t)
			h := server.New(st, 0).Handler()
			request := func(method, path, body string, status int) []byte {
				t.Helper()
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
				if rec.Code != status {
					t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
				}
				return rec.Body.Bytes()
			}
			request("POST", "/sessions", `{"id":"parity","project":"engram"}`, http.StatusCreated)
			activity := NewSessionActivity(10 * time.Minute)
			activity.RecordPrompt("parity", "engram", "same current prompt")
			saveMCP := func() {
				t.Helper()
				res, err := handleSave(st, MCPConfig{}, activity)(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: map[string]any{"session_id": "parity", "project": "engram", "title": "MCP save", "content": "MCP observation"}}})
				if err != nil || res.IsError {
					t.Fatalf("MCP save: %v %v", res, err)
				}
			}
			saveHTTP := func() {
				request("POST", "/observations", `{"session_id":"parity","project":"engram","title":"HTTP save","content":"HTTP observation","current_prompt":"same current prompt"}`, http.StatusCreated)
			}
			if httpFirst {
				saveHTTP()
				saveMCP()
			} else {
				saveMCP()
				saveHTTP()
			}
			// Same content in another session/project must not be deduped against this owner.
			request("POST", "/sessions", `{"id":"other","project":"other"}`, http.StatusCreated)
			request("POST", "/observations", `{"session_id":"other","project":"other","title":"Other save","content":"Other observation","current_prompt":"same current prompt"}`, http.StatusCreated)
			for _, project := range []string{"engram", "other"} {
				data := request("GET", "/prompts/recent?project="+project, "", http.StatusOK)
				var prompts []store.Prompt
				if err := json.Unmarshal(data, &prompts); err != nil {
					t.Fatal(err)
				}
				owner := "parity"
				if project == "other" {
					owner = "other"
				}
				if len(prompts) != 1 || prompts[0].SessionID != owner || prompts[0].Project != project || prompts[0].Content != "same current prompt" {
					t.Fatalf("prompt ownership/dedupe: %s", data)
				}
			}
			observationsBefore := string(request("GET", "/observations?project=other", "", http.StatusOK))
			promptsBefore := string(request("GET", "/prompts/recent?project=other", "", http.StatusOK))
			// A conflicting project must reject both the observation and its prompt.
			request("POST", "/observations", `{"session_id":"parity","project":"other","title":"Rejected","content":"Rejected","current_prompt":"must not be captured"}`, http.StatusBadRequest)
			observations := request("GET", "/observations?project=other", "", http.StatusOK)
			var records []store.Observation
			if err := json.Unmarshal(observations, &records); err != nil || len(records) != 1 {
				t.Fatalf("rejected save changed observations: %s (%v)", observations, err)
			}
			prompts := request("GET", "/prompts/recent?project=other", "", http.StatusOK)
			if string(observations) != observationsBefore || string(prompts) != promptsBefore {
				t.Fatalf("rejected save changed persisted data or counters: observations=%s prompts=%s", observations, prompts)
			}
			var rows []store.Prompt
			if err := json.Unmarshal(prompts, &rows); err != nil || len(rows) != 1 {
				t.Fatalf("rejected save changed prompts: %s (%v)", prompts, err)
			}
		})
	}
}
