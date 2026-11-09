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

func TestHandleSaveRejectsInvalidCapturePrompt(t *testing.T) {
	for _, value := range []any{"false", float64(1), nil} {
		for _, seeded := range []bool{false, true} {
			t.Run(fmt.Sprintf("value=%v/seeded=%t", value, seeded), func(t *testing.T) {
				st := newMCPTestStore(t) // Isolated t.TempDir-backed store.
				activity := NewSessionActivity(10 * time.Minute)
				activity.RecordPrompt(defaultSessionID("engram"), "engram", "current user prompt")
				h := handleSave(st, MCPConfig{DefaultProject: "engram"}, activity)
				args := map[string]any{"project": "engram", "title": "Capture validation", "content": "Same observation content"}
				if seeded {
					args["capture_prompt"] = true
					res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
					if err != nil || res.IsError {
						t.Fatalf("seed save: %v %v", res, err)
					}
				}
				// Public collection APIs expose full records, including duplicate/revision counters.
				httpHandler := server.New(st, 0).Handler()
				snapshot := func() string {
					t.Helper()
					var result strings.Builder
					for _, path := range []string{"/observations?all_projects=true", "/prompts/recent?all_projects=true", "/sessions/recent?all_projects=true", "/stats?all_projects=true"} {
						rec := httptest.NewRecorder()
						httpHandler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
						if rec.Code != http.StatusOK {
							t.Fatalf("snapshot %s: %d %s", path, rec.Code, rec.Body.String())
						}
						result.WriteString(rec.Body.String())
					}
					return result.String()
				}
				before := snapshot()
				args["capture_prompt"] = value
				// An invalid project also proves capture validation precedes project resolution.
				for _, project := range []string{"engram", "invalid/project"} {
					args["project"] = project
					res, err := h(context.Background(), mcppkg.CallToolRequest{Params: mcppkg.CallToolParams{Arguments: args}})
					if err != nil || !res.IsError || callResultText(t, res) != "capture_prompt must be a boolean" {
						t.Errorf("expected exact capture_prompt error: result=%v err=%v", res, err)
					}
					if after := snapshot(); after != before {
						t.Errorf("rejected save changed records, sessions, or counters: before=%s after=%s", before, after)
					}
				}
			})
		}
	}
}

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
