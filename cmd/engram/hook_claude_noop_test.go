package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	mcppkg "github.com/Gentleman-Programming/engram/v3/internal/mcp"
	"github.com/Gentleman-Programming/engram/v3/internal/server"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func claudeConfirmedInputFixture(t *testing.T) (string, *store.Store, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	root := t.TempDir()
	db, err := store.New(store.FallbackConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.StartSessionWithOwnershipMode("live-host", "project-a", root, store.SessionOwnershipProjectOwned); err != nil {
		t.Fatal(err)
	}
	var registrations, requests atomic.Int32
	production := server.New(db, 0).Handler()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method == http.MethodPost && r.URL.Path == "/sessions" {
			registrations.Add(1)
		}
		production.ServeHTTP(w, r)
	}))
	t.Cleanup(endpoint.Close)
	useLegacyLocalHookEndpoint(t, endpoint.URL)
	t.Setenv("ENGRAM_DATA_DIR", filepath.Join(root, "store"))
	return root, db, &registrations, &requests
}

func TestClaudeConfirmedArgumentsDoNotRewrite(t *testing.T) {
	root, _, registrations, _ := claudeConfirmedInputFixture(t)
	for _, prefix := range claudeEngramToolPrefixes {
		for _, tool := range []struct{ name, field string }{{"mem_save", "session_id"}, {"mem_session_start", "id"}, {"mem_session_end", "id"}} {
			for _, spelling := range []struct {
				name  string
				value any
			}{{"plain", "live-host"}, {"escaped", json.RawMessage(`"\u006cive-host"`)}} {
				t.Run(prefix+tool.name+"/"+spelling.name, func(t *testing.T) {
					input, _ := json.Marshal(map[string]any{"session_id": "live-host", "cwd": root, "permission_mode": "auto", "tool_name": prefix + tool.name, "tool_input": map[string]any{tool.field: spelling.value, "project": "project-a", "title": "kept", "content": "kept"}})
					before := registrations.Load()
					output := guardClaudePreToolUse(input)
					if registrations.Load() != before+1 {
						t.Errorf("already-bound write bypassed mandatory confirmation: before%d after%d", before, registrations.Load())
					}
					if strings.TrimSpace(string(output)) != "{}" {
						t.Errorf("unchanged authoritative input emitted rewrite: %s", output)
					}
				})
			}
		}
	}
}

func TestClaudeReviewOmitsUnusedSessionBinding(t *testing.T) {
	tool := mcppkg.NewServer(nil).GetTool("mem_review")
	if tool == nil {
		t.Fatal("missing registered mem_review schema")
	}
	if _, exists := tool.Tool.InputSchema.Properties["session_id"]; exists {
		t.Fatal("session_id is now declared; reassess old bug claim")
	}
	root, _, registrations, requests := claudeConfirmedInputFixture(t)
	for _, prefix := range claudeEngramToolPrefixes {
		for _, action := range []string{"list", " list ", "mark_reviewed"} {
			t.Run(prefix+action, func(t *testing.T) {
				input, _ := json.Marshal(map[string]any{"session_id": "live-host", "cwd": root, "permission_mode": "auto", "tool_name": prefix + "mem_review", "tool_input": map[string]any{"action": action, "project": "project-a", "observation_id": 1}})
				before := registrations.Load()
				beforeRequests := requests.Load()
				output := guardClaudePreToolUse(input)
				if strings.TrimSpace(action) == "list" && requests.Load() != beforeRequests {
					t.Errorf("read-only list contacted server: before%d after%d", beforeRequests, requests.Load())
				}
				if action == "mark_reviewed" && registrations.Load() != before+1 {
					t.Error("mark_reviewed lost existing mutating-call confirmation")
				}
				if strings.TrimSpace(string(output)) != "{}" {
					t.Errorf("mem_review received undeclared/unused session field: %s", output)
				}
			})
		}
	}
}

func TestClaudeReviewListDoesNotNeedSessionAndRejectsMalformedInput(t *testing.T) {
	for _, prefix := range claudeEngramToolPrefixes {
		input := []byte(`{"tool_name":"` + prefix + `mem_review","tool_input":{"action":"list","project":"project-a"}}`)
		if got := string(guardClaudePreToolUse(input)); got != "{}" {
			t.Fatalf("list without session=%s", got)
		}
		for _, raw := range []string{`null`, `[]`, `"list"`, `42`} {
			input := []byte(`{"tool_name":"` + prefix + `mem_review","tool_input":` + raw + `}`)
			if decision := claudeGuardResult(t, guardClaudePreToolUse(input))["permissionDecision"]; string(decision) != `"deny"` {
				t.Fatalf("malformed input%s decision%s", raw, decision)
			}
		}
	}
}

func claudeGuardResult(t *testing.T, output []byte) map[string]json.RawMessage {
	t.Helper()
	var response struct {
		HookSpecificOutput map[string]json.RawMessage `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	return response.HookSpecificOutput
}

func TestClaudeMismatchedInputStillBinds(t *testing.T) {
	root, _, registrations, _ := claudeConfirmedInputFixture(t)
	for _, prefix := range claudeEngramToolPrefixes {
		for _, tc := range []struct {
			name    string
			value   any
			missing bool
		}{
			{"missing", nil, true}, {"foreign", "other-host", false}, {"null", nil, false}, {"number", 42, false}, {"object", map[string]string{"id": "live-host"}, false}, {"trailing whitespace", "live-host ", false}, {"leading whitespace", " live-host", false},
		} {
			t.Run(prefix+tc.name, func(t *testing.T) {
				args := map[string]any{"project": "project-a", "content": "preserved", "nested": map[string]any{"precise": json.RawMessage(`9007199254740993`)}}
				if !tc.missing {
					args["session_id"] = tc.value
				}
				input, _ := json.Marshal(map[string]any{"session_id": "live-host", "cwd": root, "tool_name": prefix + "mem_save", "tool_input": args})
				before := registrations.Load()
				result := claudeGuardResult(t, guardClaudePreToolUse(input))
				if _, exists := result["permissionDecision"]; exists {
					t.Fatalf("binding granted/changed permission: %s", result["permissionDecision"])
				}
				var bound map[string]json.RawMessage
				if err := json.Unmarshal(result["updatedInput"], &bound); err != nil {
					t.Fatal(err)
				}
				if string(bound["session_id"]) != `"live-host"` || string(bound["content"]) != `"preserved"` || string(bound["nested"]) != `{"precise":9007199254740993}` {
					t.Fatalf("bound args=%s", result["updatedInput"])
				}
				if registrations.Load() != before+1 {
					t.Fatal("binding skipped confirmation")
				}
			})
		}
	}
}

func TestClaudeNoopUsesConfirmedContinuationAndRefusesEndedHost(t *testing.T) {
	root, db, _, _ := claudeConfirmedInputFixture(t)
	if err := db.EndSession("live-host", "ended"); err != nil {
		t.Fatal(err)
	}
	continuation, err := db.ResumeSessionWithOwnershipMode("live-host", "project-a", root, store.SessionOwnershipProjectOwned)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range claudeEngramToolPrefixes {
		for _, tool := range []struct{ name, field string }{{"mem_save", "session_id"}, {"mem_session_end", "id"}} {
			call := func(model string) []byte {
				input, _ := json.Marshal(map[string]any{"session_id": "live-host", "cwd": root, "tool_name": prefix + tool.name, "tool_input": map[string]string{tool.field: model, "project": "project-a"}})
				return guardClaudePreToolUse(input)
			}
			rootResult := claudeGuardResult(t, call("live-host"))
			var bound map[string]string
			if err := json.Unmarshal(rootResult["updatedInput"], &bound); err != nil || bound[tool.field] != continuation {
				t.Fatalf("root ID not rebound to live continuation: %s error%v", rootResult["updatedInput"], err)
			}
			if got := string(call(continuation)); got != "{}" {
				t.Fatalf("already-confirmed continuation rewritten:%s", got)
			}
		}
	}
	if err := db.EndSession(continuation, "ended too"); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"mem_save", "mem_review"} {
		input, _ := json.Marshal(map[string]any{"session_id": "live-host", "cwd": root, "tool_name": "mcp__engram__" + tool, "tool_input": map[string]string{"session_id": continuation, "action": "mark_reviewed"}})
		if decision := claudeGuardResult(t, guardClaudePreToolUse(input))["permissionDecision"]; string(decision) != `"deny"` {
			t.Fatalf("ended %s bypassed confirmation:%s", tool, decision)
		}
	}
}

func TestCodexMatchingArgumentsAndReviewRetainRewriteAndAllow(t *testing.T) {
	for _, prefix := range claudeEngramToolPrefixes {
		for _, tool := range []string{"mem_save", "mem_review"} {
			input, _ := json.Marshal(map[string]any{"session_id": "live-host", "tool_name": prefix + tool, "tool_input": map[string]string{"session_id": "live-host", "action": "mark_reviewed"}})
			result := claudeGuardResult(t, transformCodexPreToolUse(input))
			if string(result["permissionDecision"]) != `"allow"` || len(result["updatedInput"]) == 0 {
				t.Fatalf("existing Codex contract changed:%s", result)
			}
		}
	}
}

func TestClaudeConfirmedCallsKeepInterleavedHostsDistinct(t *testing.T) {
	root, db, registrations, _ := claudeConfirmedInputFixture(t)
	if err := db.StartSessionWithOwnershipMode("peer-host", "project-a", root, store.SessionOwnershipProjectOwned); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		host, model string
		noop        bool
	}{
		{"live-host", "live-host", true}, {"peer-host", "live-host", false}, {"live-host", "peer-host", false}, {"peer-host", "peer-host", true},
	} {
		input, _ := json.Marshal(map[string]any{"session_id": step.host, "cwd": root, "tool_name": "mcp__engram__mem_save", "tool_input": map[string]string{"session_id": step.model}})
		before := registrations.Load()
		output := guardClaudePreToolUse(input)
		if registrations.Load() != before+1 {
			t.Fatal("interleaved call skipped confirmation")
		}
		if step.noop {
			if string(output) != "{}" {
				t.Fatalf("confirmed host%s rewritten:%s", step.host, output)
			}
			continue
		}
		result := claudeGuardResult(t, output)
		var bound map[string]string
		if err := json.Unmarshal(result["updatedInput"], &bound); err != nil || bound["session_id"] != step.host {
			t.Fatalf("interleaved host%s bound%s err%v", step.host, result["updatedInput"], err)
		}
	}
}

func TestClaudeReviewPassthroughRetainsMCPProjectGuard(t *testing.T) {
	root, db, registrations, _ := claudeConfirmedInputFixture(t)
	if err := db.CreateSession("other-host", "project-b", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	owned, err := db.AddObservation(store.AddObservationParams{SessionID: "live-host", Project: "project-a", Type: "note", Title: "own", Content: "own"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := db.AddObservation(store.AddObservationParams{SessionID: "other-host", Project: "project-b", Type: "note", Title: "other", Content: "other"})
	if err != nil {
		t.Fatal(err)
	}
	const oldReview = "2000-01-01 00:00:00"
	if _, err := db.DB().Exec(`UPDATE observations SET review_after=? WHERE id=?`, oldReview, foreign); err != nil {
		t.Fatal(err)
	}
	mcpServer := mcppkg.NewServer(db)
	for _, tc := range []struct {
		id        int64
		wantError bool
	}{{foreign, true}, {owned, false}} {
		args := map[string]any{"action": "mark_reviewed", "project": "project-a", "observation_id": tc.id}
		input, _ := json.Marshal(map[string]any{"session_id": "live-host", "cwd": root, "tool_name": "mcp__engram__mem_review", "tool_input": args})
		before := registrations.Load()
		if output := string(guardClaudePreToolUse(input)); output != "{}" || registrations.Load() != before+1 {
			t.Fatalf("review confirmation/passthrough:%s", output)
		}
		call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_review", "arguments": args}})
		response, err := json.Marshal(mcpServer.HandleMessage(context.Background(), call))
		if err != nil || strings.Contains(string(response), `"isError":true`) != tc.wantError {
			t.Fatalf("review project guard response%s err%v", response, err)
		}
	}
	other, err := db.GetObservation(foreign)
	if err != nil || other.ReviewAfter == nil || *other.ReviewAfter != oldReview {
		t.Fatalf("foreign review changed:%+v err%v", other, err)
	}
}
