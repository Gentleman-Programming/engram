package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/mcp"
	"github.com/Gentleman-Programming/engram/v2/internal/server"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

func claudeHookStdin(t *testing.T, input string, closed bool) *os.File {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	if _, err := writer.Write([]byte(input)); err != nil {
		t.Fatalf("write Claude hook input: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	if closed {
		if err := reader.Close(); err != nil {
			t.Fatalf("close stdin reader: %v", err)
		}
	}
	return reader
}

func TestShouldCheckForUpdatesSkipsInternalHook(t *testing.T) {
	if shouldCheckForUpdates([]string{"hook", "claude-pre-tool-use"}) {
		t.Fatal("internal hook must not run the update check before emitting a Claude hook response")
	}
}

func TestCmdHookWritesTransformedResponse(t *testing.T) {
	oldStdin, oldOutput := os.Stdin, claudeHookOutput
	t.Cleanup(func() { os.Stdin, claudeHookOutput = oldStdin, oldOutput })
	os.Stdin = claudeHookStdin(t, `{"session_id":"claude-session","tool_name":"mcp__engram__mem_save","tool_input":{"title":"decision"}}`, false)
	var output []byte
	claudeHookOutput = func(response []byte) error { output = append([]byte(nil), response...); return nil }
	cmdHook([]string{"claude-pre-tool-use"})
	var response struct {
		HookSpecificOutput struct {
			UpdatedInput       map[string]any `json:"updatedInput"`
			PermissionDecision string         `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil || response.HookSpecificOutput.UpdatedInput["session_id"] != "claude-session" || response.HookSpecificOutput.PermissionDecision != "" {
		t.Fatalf("successful hook output = %s, %v", output, err)
	}
}

func TestClaudeAdapterPersistsWritesForDistinctSameWorktreeHosts(t *testing.T) {
	root := t.TempDir()
	db, err := store.New(store.FallbackConfig(filepath.Join(root, "store")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const project = "same-worktree"
	hosts := []string{"claude-host-one", "claude-host-two"}
	for _, host := range hosts {
		if err := db.CreateSession(host, project, root); err != nil {
			t.Fatal(err)
		}
	}
	production := server.New(db, 0).Handler()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/project/current" {
			_, _ = io.WriteString(w, `{"project":"same-worktree","project_source":"config"}`)
			return
		}
		production.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	t.Setenv("ENGRAM_URL", endpoint.URL)
	oldStdin, oldOutput := os.Stdin, claudeHookOutput
	t.Cleanup(func() { os.Stdin, claudeHookOutput = oldStdin, oldOutput })
	mcpServer := mcp.NewServerWithConfig(db, mcp.MCPConfig{DefaultProject: project}, nil)
	want := map[string]map[string]int{
		hosts[0]: {"one first": 1, "one second": 1},
		hosts[1]: {"two first": 1, "two second": 1},
	}
	for _, step := range []struct{ host, title string }{
		{hosts[0], "one first"}, {hosts[1], "two first"},
		{hosts[0], "one second"}, {hosts[1], "two second"},
	} {
		request, _ := json.Marshal(map[string]any{
			"session_id": step.host, "cwd": root, "tool_name": "mcp__engram__mem_save",
			"tool_input": map[string]any{"title": step.title, "content": step.title, "project": project, "session_id": "foreign-model-session"},
		})
		os.Stdin = claudeHookStdin(t, string(request), false)
		var output []byte
		claudeHookOutput = func(data []byte) error { output = append([]byte(nil), data...); return nil }
		cmdHook([]string{"claude-pre-tool-use"})
		var hook struct {
			HookSpecificOutput struct {
				PermissionDecision string         `json:"permissionDecision"`
				UpdatedInput       map[string]any `json:"updatedInput"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(output, &hook); err != nil {
			t.Fatal(err)
		}
		bound := hook.HookSpecificOutput
		if bound.PermissionDecision == "deny" || bound.UpdatedInput["session_id"] != step.host || bound.UpdatedInput["project"] != project {
			t.Fatalf("host %s bound output = %s", step.host, output)
		}
		call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "mem_save", "arguments": bound.UpdatedInput}})
		result := mcpServer.HandleMessage(context.Background(), call)
		encoded, err := json.Marshal(result)
		if err != nil || strings.Contains(string(encoded), `"isError":true`) || !strings.Contains(string(encoded), step.title) {
			t.Fatalf("host %s MCP result = %s, err=%v", step.host, encoded, err)
		}
	}
	all, err := db.AllObservations(project, "", 100)
	if err != nil || len(all) != 4 {
		t.Fatalf("project observations = %d, err=%v", len(all), err)
	}
	for host, expected := range want {
		observations, err := db.SessionObservations(host, 100)
		if err != nil || len(observations) != 2 {
			t.Fatalf("host %s observations = %v, err=%v", host, observations, err)
		}
		counts := make(map[string]int)
		for _, observation := range observations {
			counts[observation.Title]++
		}
		for title, count := range expected {
			if counts[title] != count {
				t.Fatalf("host %s title %q count = %d, want %d; all titles: %v", host, title, counts[title], count, counts)
			}
		}
		if len(counts) != len(expected) {
			t.Fatalf("host %s has unexpected titles: %v", host, counts)
		}
	}
	if _, err := db.GetSession("foreign-model-session"); err == nil {
		t.Fatal("foreign model session was created")
	}
}

func TestCmdHookExitsWhenClaudeResponseWriteFails(t *testing.T) {
	oldStdin, oldOutput, oldExit := os.Stdin, claudeHookOutput, exitFunc
	t.Cleanup(func() { os.Stdin, claudeHookOutput, exitFunc = oldStdin, oldOutput, oldExit })
	claudeHookOutput = func([]byte) error { return errors.New("write Claude hook response") }
	var exitCodes []int
	exitFunc = func(code int) { exitCodes = append(exitCodes, code) }
	os.Stdin = claudeHookStdin(t, `{"session_id":"claude-session","tool_name":"mcp__engram__mem_save","tool_input":{"title":"decision"}}`, false)
	cmdHook([]string{"claude-pre-tool-use"})
	os.Stdin = claudeHookStdin(t, "", true)
	cmdHook([]string{"claude-pre-tool-use"})
	if len(exitCodes) != 2 || exitCodes[0] != 1 || exitCodes[1] != 1 {
		t.Fatalf("exit codes = %v, want [1 1] after Claude hook response write failures", exitCodes)
	}
}

func TestCmdHookEmitsJSONDenialWhenClaudeInputReadFails(t *testing.T) {
	oldStdin, oldOutput := os.Stdin, claudeHookOutput
	t.Cleanup(func() { os.Stdin, claudeHookOutput = oldStdin, oldOutput })
	os.Stdin = claudeHookStdin(t, "", true)
	var output []byte
	claudeHookOutput = func(response []byte) error { output = append([]byte(nil), response...); return nil }
	cmdHook([]string{"claude-pre-tool-use"})
	var response struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("command output = %q, want JSON denial: %v", output, err)
	}
	if response.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hook event = %q, want PreToolUse", response.HookSpecificOutput.HookEventName)
	}
	if response.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("permissionDecision = %q, want deny", response.HookSpecificOutput.PermissionDecision)
	}
	if response.HookSpecificOutput.PermissionDecisionReason != "cannot read authoritative Claude hook input" {
		t.Fatalf("permissionDecisionReason = %q", response.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestCodexPreToolUseCommandDeniesUnreadableInput(t *testing.T) {
	oldStdin, oldOutput, oldExit := os.Stdin, claudeHookOutput, exitFunc
	t.Cleanup(func() { os.Stdin, claudeHookOutput, exitFunc = oldStdin, oldOutput, oldExit })
	os.Stdin = claudeHookStdin(t, "", true)
	var output []byte
	claudeHookOutput = func(response []byte) error { output = append([]byte(nil), response...); return nil }
	exitFunc = func(code int) { t.Errorf("unexpected exit code %d", code) }
	cmdHook([]string{"codex-pre-tool-use"})
	var response struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("command output = %q, want JSON denial: %v", output, err)
	}
	if got := response.HookSpecificOutput; got.HookEventName != "PreToolUse" || got.PermissionDecision != "deny" || got.PermissionDecisionReason != "cannot read authoritative Codex hook input" {
		t.Fatalf("Codex read failure response = %+v", got)
	}
}

func TestCodexPreToolUseCommandExitsWhenResponseWriteFails(t *testing.T) {
	oldStdin, oldOutput, oldExit := os.Stdin, claudeHookOutput, exitFunc
	t.Cleanup(func() { os.Stdin, claudeHookOutput, exitFunc = oldStdin, oldOutput, oldExit })
	os.Stdin = claudeHookStdin(t, `{"session_id":"host","tool_name":"mcp__engram__mem_save","tool_input":{"title":"decision"}}`, false)
	claudeHookOutput = func([]byte) error { return errors.New("write Codex hook response") }
	var exitCodes []int
	exitFunc = func(code int) { exitCodes = append(exitCodes, code) }
	cmdHook([]string{"codex-pre-tool-use"})
	if len(exitCodes) != 1 || exitCodes[0] != 1 {
		t.Fatalf("exit codes = %v, want [1] after Codex response write failure", exitCodes)
	}
}

func TestCodexPreToolUseBindsWrites(t *testing.T) {
	for _, tool := range claudeEngramWriteAndSessionTools {
		t.Run(tool, func(t *testing.T) {
			field := "session_id"
			if tool == "mem_session_start" || tool == "mem_session_end" {
				field = "id"
			}
			input := []byte(`{"session_id":"host","tool_name":"mcp__engram__` + tool + `","tool_input":{"` + field + `":"model-picked","other":{"keep":true}}}`)
			var response struct {
				HookSpecificOutput struct {
					PermissionDecision string         `json:"permissionDecision"`
					UpdatedInput       map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(transformCodexPreToolUse(input), &response); err != nil {
				t.Fatal(err)
			}
			if response.HookSpecificOutput.PermissionDecision != "allow" || response.HookSpecificOutput.UpdatedInput[field] != "host" || response.HookSpecificOutput.UpdatedInput["other"].(map[string]any)["keep"] != true {
				t.Fatalf("unexpected Codex rewrite: %+v", response)
			}
		})
	}
}

func TestCodexPreToolUseRejectsMalformedInputAndLeavesReadsUntouched(t *testing.T) {
	for _, input := range []string{
		`not json`,
		`{"tool_name":"mcp__engram__mem_save","tool_input":{}}`,
		`{"session_id":"host","tool_name":"mcp__engram__mem_save","tool_input":null}`,
		`{"session_id":"host","tool_name":"mcp__engram__mem_save","tool_input":[]}`,
	} {
		t.Run(input, func(t *testing.T) {
			var response struct {
				HookSpecificOutput struct {
					PermissionDecision string `json:"permissionDecision"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(transformCodexPreToolUse([]byte(input)), &response); err != nil || response.HookSpecificOutput.PermissionDecision != "deny" {
				t.Fatalf("malformed input response: %+v, %v", response, err)
			}
		})
	}
	for _, tool := range []string{"mcp__engram__mem_search", "mcp__other__mem_save"} {
		input := `{"session_id":"host","tool_name":"` + tool + `","tool_input":{"session_id":"model"}}`
		if got := string(transformCodexPreToolUse([]byte(input))); got != "{}" {
			t.Errorf("non-write response for %s = %s", tool, got)
		}
	}
}

func TestCodexPreToolUseInterleavedHostSessionsRemainDistinct(t *testing.T) {
	for _, host := range []string{"host-one", "host-two", "host-one"} {
		input := []byte(`{"session_id":"` + host + `","tool_name":"mcp__engram__mem_save","tool_input":{"session_id":"model-picked","project":"same-project","directory":"same-directory"}}`)
		var response struct {
			HookSpecificOutput struct {
				UpdatedInput map[string]any `json:"updatedInput"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(transformCodexPreToolUse(input), &response); err != nil {
			t.Fatal(err)
		}
		if got := response.HookSpecificOutput.UpdatedInput; got["session_id"] != host || got["project"] != "same-project" || got["directory"] != "same-directory" {
			t.Fatalf("host %q updated input = %#v", host, got)
		}
	}
}

func TestCodexPreToolUseReadsDoNotRequireSessionOrInput(t *testing.T) {
	for _, input := range []string{
		`{"tool_name":"mcp__engram__mem_search"}`,
		`{"session_id":42,"tool_name":"mcp__engram__mem_context","tool_input":null}`,
		`{"tool_name":"mcp__other__mem_save","tool_input":[]}`,
	} {
		if got := string(transformCodexPreToolUse([]byte(input))); got != "{}" {
			t.Errorf("read/non-Engram call %s = %s, want {}", input, got)
		}
	}
}

func TestCodexPreToolUseCommandWritesAllowAndBoundInput(t *testing.T) {
	oldStdin, oldOutput := os.Stdin, claudeHookOutput
	t.Cleanup(func() { os.Stdin, claudeHookOutput = oldStdin, oldOutput })
	os.Stdin = claudeHookStdin(t, `{"session_id":"host","tool_name":"mcp__engram__mem_save","tool_input":{"session_id":"model","title":"retained"}}`, false)
	var output []byte
	claudeHookOutput = func(response []byte) error { output = append([]byte(nil), response...); return nil }
	cmdHook([]string{"codex-pre-tool-use"})
	var response struct {
		HookSpecificOutput struct {
			PermissionDecision string         `json:"permissionDecision"`
			UpdatedInput       map[string]any `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil || response.HookSpecificOutput.PermissionDecision != "allow" || response.HookSpecificOutput.UpdatedInput["session_id"] != "host" || response.HookSpecificOutput.UpdatedInput["title"] != "retained" {
		t.Fatalf("command response = %s, %v", output, err)
	}
}

func TestTransformClaudePreToolUseBindsEngramWritesToAuthoritativeSession(t *testing.T) {
	input := []byte(`{
		"session_id":"claude-parent-session",
		"tool_name":"mcp__engram__mem_save",
		"tool_input":{"title":"decision","content":"keep this","session_id":"model-invented","project":"engram","nested":{"keep":true}}
	}`)

	output := transformClaudePreToolUse(input)
	var response struct {
		HookSpecificOutput struct {
			HookEventName      string         `json:"hookEventName"`
			UpdatedInput       map[string]any `json:"updatedInput"`
			PermissionDecision string         `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode hook response: %v\n%s", err, output)
	}
	if response.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hook event = %q, want PreToolUse", response.HookSpecificOutput.HookEventName)
	}
	if response.HookSpecificOutput.PermissionDecision != "" {
		t.Fatalf("successful rewrite must not auto-allow, got permissionDecision=%q", response.HookSpecificOutput.PermissionDecision)
	}
	updated := response.HookSpecificOutput.UpdatedInput
	if got := updated["session_id"]; got != "claude-parent-session" {
		t.Fatalf("session_id = %#v, want authoritative Claude session", got)
	}
	if got := updated["title"]; got != "decision" {
		t.Fatalf("title = %#v, want preserved", got)
	}
	if got := updated["project"]; got != "engram" {
		t.Fatalf("project = %#v, want preserved", got)
	}
	nested, ok := updated["nested"].(map[string]any)
	if !ok || nested["keep"] != true {
		t.Fatalf("nested arguments = %#v, want preserved", updated["nested"])
	}
}

func TestTransformClaudePreToolUseBindsEveryEngramWriteAndSessionTool(t *testing.T) {
	for _, tool := range claudeEngramWriteAndSessionTools {
		for _, server := range []string{"mcp__engram__", "mcp__plugin_engram_engram__"} {
			t.Run(server+tool, func(t *testing.T) {
				bindingField := "session_id"
				if tool == "mem_session_start" || tool == "mem_session_end" {
					bindingField = "id"
				}
				input := []byte(`{"session_id":"subagent-session","tool_name":"` + server + tool + `","tool_input":{"` + bindingField + `":"wrong","value":"preserved"}}`)
				output := transformClaudePreToolUse(input)
				var response struct {
					HookSpecificOutput struct {
						UpdatedInput map[string]any `json:"updatedInput"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal(output, &response); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if got := response.HookSpecificOutput.UpdatedInput[bindingField]; got != "subagent-session" {
					t.Fatalf("%s = %#v, want subagent authoritative session", bindingField, got)
				}
				if got := response.HookSpecificOutput.UpdatedInput["value"]; got != "preserved" {
					t.Fatalf("unrelated tool input = %#v, want preserved", got)
				}
			})
		}
	}
}

func TestTransformClaudePreToolUseLeavesNonEngramAndReadToolsUntouched(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`{"session_id":"claude-session","tool_name":"mcp__other__mem_save","tool_input":{"session_id":"model"}}`),
		[]byte(`{"session_id":"claude-session","tool_name":"mcp__engram__mem_search","tool_input":{"query":"history"}}`),
	} {
		if got := strings.TrimSpace(string(transformClaudePreToolUse(input))); got != "{}" {
			t.Fatalf("non-target tool response = %s, want {}", got)
		}
	}
}

func TestTransformClaudePreToolUseFailsClosedForMalformedAuthoritativeInput(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`not json`),
		[]byte(`{"tool_name":"mcp__engram__mem_save","tool_input":{}}`),
		[]byte(`{"session_id":" ","tool_name":"mcp__engram__mem_save","tool_input":{}}`),
		[]byte(`{"session_id":"claude-session","tool_name":"mcp__engram__mem_save","tool_input":null}`),
		[]byte(`{"session_id":"claude-session","tool_name":42,"tool_input":{}}`),
	} {
		output := transformClaudePreToolUse(input)
		var response struct {
			HookSpecificOutput struct {
				HookEventName      string `json:"hookEventName"`
				PermissionDecision string `json:"permissionDecision"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(output, &response); err != nil {
			t.Fatalf("decode denial: %v\n%s", err, output)
		}
		if response.HookSpecificOutput.HookEventName != "PreToolUse" || response.HookSpecificOutput.PermissionDecision != "deny" {
			t.Fatalf("malformed authoritative input response = %#v, want PreToolUse deny", response.HookSpecificOutput)
		}
	}
}
